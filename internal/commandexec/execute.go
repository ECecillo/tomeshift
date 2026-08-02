package commandexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ECecillo/ebooks-translator/internal/cli"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/cost"
	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/eval"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
)

// ConfigLoader loads runtime configuration for command execution. It is
// injected so command dispatch stays independent of where the configuration
// comes from: the environment in production, a literal in tests.
type ConfigLoader func() (config.Config, error)

// Execute dispatches CLI commands and returns process exit codes consumed by
// main() via os.Exit.
func Execute(args []string, stdout, stderr io.Writer, loadConfig ConfigLoader) int {
	if len(args) == 0 {
		cli.PrintRootUsage(stderr)
		return exitcode.InvalidInputConfig
	}

	switch args[0] {
	case "run":
		return dispatch(stderr, loadConfig, args, cli.ParseRunArgs,
			func(cfg config.Config, cmd cli.RunCommand) int { return runPipeline(stdout, stderr, cmd, cfg) })
	case "estimate":
		return dispatch(stderr, loadConfig, args, cli.ParseEstimateArgs,
			func(cfg config.Config, cmd cli.EstimateCommand) int { return runEstimate(stdout, stderr, cmd, cfg) })
	case "batch":
		return dispatch(stderr, loadConfig, args, cli.ParseBatchArgs,
			func(cfg config.Config, cmd cli.BatchCommand) int { return runBatch(stdout, stderr, cmd, cfg) })
	case "eval":
		return dispatch(stderr, loadConfig, args, cli.ParseEvalArgs,
			func(cfg config.Config, cmd cli.EvalCommand) int { return runEval(stdout, stderr, cmd, cfg) })
	case "style-profile":
		return dispatch(stderr, loadConfig, args, cli.ParseStyleProfileArgs,
			func(cfg config.Config, cmd cli.StyleProfileCommand) int {
				return runStyleProfile(stdout, stderr, cmd, cfg)
			})
	case "glossary-suggest":
		// The one command that needs no configuration at all.
		cmd, err := cli.ParseSuggestArgs(args[1:], stderr)
		if err != nil {
			writef(stderr, "invalid %s args: %v\n", args[0], err)
			return exitcode.InvalidInputConfig
		}
		return runSuggest(stdout, stderr, cmd)
	case "-h", "--help", "help":
		cli.PrintRootUsage(stdout)
		return exitcode.Success
	default:
		writef(stderr, "unknown command: %s\n", args[0])
		cli.PrintRootUsage(stderr)
		return exitcode.InvalidInputConfig
	}
}

// dispatch is the parse-then-load-then-run preamble every configured command
// shares. Spelling it once keeps the switch above a table of commands instead
// of five copies of the same eleven lines — copies that had already drifted
// into naming the wrong command in one of their error messages.
func dispatch[C any](
	stderr io.Writer,
	loadConfig ConfigLoader,
	args []string,
	parse func([]string, io.Writer) (C, error),
	run func(config.Config, C) int,
) int {
	cmd, err := parse(args[1:], stderr)
	if err != nil {
		writef(stderr, "invalid %s args: %v\n", args[0], err)
		return exitcode.InvalidInputConfig
	}
	cfg, err := loadConfig()
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	return run(cfg, cmd)
}

// signalContext returns a context cancelled on SIGINT/SIGTERM for graceful
// shutdown. The caller must invoke the returned stop func.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// withRunTimeout derives a per-book timeout context from parent when seconds>0.
func withRunTimeout(parent context.Context, seconds int) (context.Context, context.CancelFunc) {
	if seconds <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, time.Duration(seconds)*time.Second)
}

// qualityEstimator builds the optional COMET-QE estimator from config.
func qualityEstimator(cfg config.Config) pipeline.QualityEstimator {
	if cfg.CometEndpoint == "" {
		return nil
	}
	return eval.NewCometClient(cfg.CometEndpoint, cfg.CometAPIKey)
}

// newCachedChatClient builds the configured chat client wrapped in the SQLite
// LLM cache, so re-running over the same chapter reuses prior completions at no
// provider cost. The caller must invoke the returned closer.
func newCachedChatClient(cfg config.Config) (llm.ChatClient, func() error, error) {
	inner := llm.NewChatClient(cfg.LLMChatProvider, cfg.LLMBaseURL, cfg.LLMAPIKey)
	cache, err := llm.NewSQLiteCache(cfg.LLMCachePath)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to open llm cache, err: %w", err)
	}
	return &llm.CachedChatClient{Inner: inner, Cache: cache}, cache.Close, nil
}

// openBook extracts and parses an EPUB, tagging the failure with the stage it
// happened in so exitCodeForError can map it to the documented exit code.
func openBook(ctx context.Context, path string) (*epub.Book, error) {
	raw, err := epub.ZipExtractor{}.Extract(ctx, path)
	if err != nil {
		return nil, &pipeline.StageError{Stage: pipeline.StageExtract, Err: err}
	}
	book, err := epub.OPFParser{}.Parse(ctx, raw)
	if err != nil {
		return nil, &pipeline.StageError{Stage: pipeline.StageParse, Err: err}
	}
	return book, nil
}

// bookPlainText extracts a book's visible text in spine order for evaluation.
func bookPlainText(ctx context.Context, path string) (string, error) {
	book, err := openBook(ctx, path)
	if err != nil {
		return "", err
	}
	return book.PlainText(), nil
}

// exitCodeForError maps pipeline errors to the documented process exit codes.
func exitCodeForError(err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return exitcode.Aborted
	}
	var stageErr *pipeline.StageError
	if errors.As(err, &stageErr) {
		switch stageErr.Stage {
		case pipeline.StageExtract, pipeline.StageParse, pipeline.StageSegment:
			return exitcode.ParseSegmentationFailed
		case pipeline.StageTranslate:
			return exitcode.ProviderFailed
		case pipeline.StageRecompose:
			return exitcode.RebuildValidationFailed
		case pipeline.StageQA:
			return exitcode.QAFailed
		}
	}
	if errors.Is(err, pipeline.ErrQAGateFailed) {
		return exitcode.QAFailed
	}
	if errors.Is(err, cost.ErrBudgetExceeded) {
		return exitcode.BudgetExceeded
	}
	return exitcode.InvalidInputConfig
}

// bookIDFromPath derives a stable book identifier from the input filename.
func bookIDFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func writef(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write error: %v\n", err)
	}
}
