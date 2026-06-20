package commandexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ECecillo/ebooks-translator/internal/cli"
	"github.com/ECecillo/ebooks-translator/internal/cost"
	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/eval"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/literary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/qa"
	"github.com/ECecillo/ebooks-translator/internal/segment"
	"github.com/ECecillo/ebooks-translator/internal/tm"
	"github.com/ECecillo/ebooks-translator/internal/translate"
)

type RuntimeConfig struct {
	// Env carries runtime environment context and is used in run output/logging.
	Env string
	// NMTProvider identifies which translation provider is selected for this run.
	NMTProvider string
	// NMTEndpoint is the HTTP NMT endpoint the default provider targets.
	NMTEndpoint string
	// NMTAPIKey is an optional bearer token for the NMT endpoint.
	NMTAPIKey string
	// LogLevel controls the structured run log verbosity.
	LogLevel string
	// TMPath is the SQLite translation-memory database path.
	TMPath string
	// NarrativePath is the SQLite series-bible database path.
	NarrativePath string
	// PerBookBudgetUSD is the per-book hard spend cap; <= 0 disables the check.
	PerBookBudgetUSD float64
	// DailyBudgetUSD and MonthlyBudgetUSD are rolling spend caps; <= 0 disables.
	DailyBudgetUSD   float64
	MonthlyBudgetUSD float64
	// CostPerSegmentUSD and CostPer1000CharsUSD configure the cost model.
	CostPerSegmentUSD   float64
	CostPer1000CharsUSD float64
	// NMTMaxAttempts and NMTRetryBaseMS configure transient-failure retries.
	NMTMaxAttempts int
	NMTRetryBaseMS int
	// EPUBCheckBin / EPUBCheckJar select how the QA gate runs epubcheck.
	EPUBCheckBin string
	EPUBCheckJar string
	// RunTimeoutSeconds bounds a single book's run; 0 disables.
	RunTimeoutSeconds int
	// LLM post-edit (M3) settings; disabled unless LLMEnabled and LLMEndpoint set.
	LLMEnabled           bool
	LLMEndpoint          string
	LLMAPIKey            string
	LLMChatProvider      string
	LLMBaseURL           string
	LLMModel             string
	LLMCachePath         string
	LLMEscalateMinChars  int
	CostLLMPerSegmentUSD float64
	// LLM token pricing for the `estimate` cost projection.
	LLMInputPerMTokUSD    float64
	LLMOutputPerMTokUSD   float64
	LLMOutputExpansion    float64
	LLMPromptOverheadToks int
	// COMET-QE quality estimation (optional) for quality-driven escalation.
	CometEndpoint      string
	CometAPIKey        string
	EscalateBelowScore float64
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
func (c RuntimeConfig) qualityEstimator() pipeline.QualityEstimator {
	if c.CometEndpoint == "" {
		return nil
	}
	return eval.NewCometClient(c.CometEndpoint, c.CometAPIKey)
}

// newCachedChatClient builds the configured chat client wrapped in the SQLite
// LLM cache, so re-running over the same chapter reuses prior completions at no
// provider cost. The caller must invoke the returned closer.
func (c RuntimeConfig) newCachedChatClient() (llm.ChatClient, func() error, error) {
	inner := llm.NewChatClient(c.LLMChatProvider, c.LLMBaseURL, c.LLMAPIKey)
	cache, err := llm.NewSQLiteCache(c.LLMCachePath)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to open llm cache, err: %w", err)
	}
	return &llm.CachedChatClient{Inner: inner, Cache: cache}, cache.Close, nil
}

// ConfigLoader loads runtime configuration for command execution. It is
// injected to keep command orchestration decoupled from config sources.
type ConfigLoader func() (RuntimeConfig, error)

// Execute dispatches CLI commands and returns process exit codes consumed by
// main() via os.Exit.
func Execute(args []string, stdout, stderr io.Writer, loadConfig ConfigLoader) int {
	if len(args) == 0 {
		cli.PrintRootUsage(stderr)
		return exitcode.InvalidInputConfig
	}

	switch args[0] {
	case "run":
		cmd, err := cli.ParseRunArgs(args[1:], stderr)
		if err != nil {
			writef(stderr, "invalid run args: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		cfg, err := loadConfig()
		if err != nil {
			writef(stderr, "%v\n", err)
			return exitcode.InvalidInputConfig
		}
		return runPipeline(stdout, stderr, cmd, cfg)
	case "estimate":
		cmd, err := cli.ParseEstimateArgs(args[1:], stderr)
		if err != nil {
			writef(stderr, "invalid estimate args: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		cfg, err := loadConfig()
		if err != nil {
			writef(stderr, "%v\n", err)
			return exitcode.InvalidInputConfig
		}
		return runEstimate(stdout, stderr, cmd, cfg)
	case "batch":
		cmd, err := cli.ParseBatchArgs(args[1:], stderr)
		if err != nil {
			writef(stderr, "invalid batch args: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		cfg, err := loadConfig()
		if err != nil {
			writef(stderr, "%v\n", err)
			return exitcode.InvalidInputConfig
		}
		return runBatch(stdout, stderr, cmd, cfg)
	case "eval":
		cmd, err := cli.ParseEvalArgs(args[1:], stderr)
		if err != nil {
			writef(stderr, "invalid eval args: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		return runEval(stdout, stderr, cmd)
	case "glossary-suggest":
		cmd, err := cli.ParseSuggestArgs(args[1:], stderr)
		if err != nil {
			writef(stderr, "invalid glossary-suggest args: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		return runSuggest(stdout, stderr, cmd)
	case "style-profile":
		cmd, err := cli.ParseStyleProfileArgs(args[1:], stderr)
		if err != nil {
			writef(stderr, "invalid style-profile args: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		cfg, err := loadConfig()
		if err != nil {
			writef(stderr, "%v\n", err)
			return exitcode.InvalidInputConfig
		}
		return runStyleProfile(stdout, stderr, cmd, cfg)
	case "-h", "--help", "help":
		cli.PrintRootUsage(stdout)
		return exitcode.Success
	default:
		writef(stderr, "unknown command: %s\n", args[0])
		cli.PrintRootUsage(stderr)
		return exitcode.InvalidInputConfig
	}
}

// runPipeline assembles the orchestrator from runtime config and executes a
// single translation run, mapping stage failures to process exit codes.
func runPipeline(stdout, stderr io.Writer, cmd cli.RunCommand, cfg RuntimeConfig) int {
	runID := cmd.RunID
	if runID == "" {
		runID = time.Now().UTC().Format("20060102-150405")
	}
	outputDir := filepath.Join(cmd.OutputDir, runID)

	logger, closeLog, err := pipeline.NewRunLogger(outputDir, cfg.LogLevel)
	if err != nil {
		writef(stderr, "unable to initialize run logger: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = closeLog() }()

	store, err := tm.NewSQLiteStore(cfg.TMPath)
	if err != nil {
		writef(stderr, "unable to open translation memory: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = store.Close() }()

	var gloss *glossary.Glossary
	if cmd.Glossary != "" {
		gloss, err = glossary.Load(cmd.Glossary)
		if err != nil {
			writef(stderr, "unable to load glossary: %v\n", err)
			return exitcode.InvalidInputConfig
		}
	}

	translator := translate.NewRetryingTranslator(
		translate.NewProvider(cfg.NMTProvider, cfg.NMTEndpoint, cfg.NMTAPIKey),
		cfg.NMTMaxAttempts,
		time.Duration(cfg.NMTRetryBaseMS)*time.Millisecond,
	)

	var postEditor llm.PostEditor
	if cfg.LLMEnabled && cfg.LLMEndpoint != "" {
		postEditor = llm.NewHTTPPostEditor(cfg.LLMEndpoint, cfg.LLMAPIKey)
	}

	var narrativeStore narrative.Store
	var narrativeProfiler *narrative.Profiler
	var literaryTranslator *literary.Translator
	if cmd.Mode == pipeline.ModeLiterary {
		narrativeStore, err = narrative.NewSQLiteStore(runtimeNarrativePath(cfg))
		if err != nil {
			writef(stderr, "unable to open narrative store: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		defer func() { _ = narrativeStore.Close() }()

		cachedChatClient, closeCache, err := cfg.newCachedChatClient()
		if err != nil {
			writef(stderr, "%v\n", err)
			return exitcode.InvalidInputConfig
		}
		defer func() { _ = closeCache() }()

		narrativeProfiler = &narrative.Profiler{Chat: cachedChatClient, Model: cfg.LLMModel}
		literaryTranslator = &literary.Translator{
			Chat:     cachedChatClient,
			Model:    cfg.LLMModel,
			Glossary: gloss,
		}
	}

	orch := &pipeline.Orchestrator{
		Extractor:          epub.ZipExtractor{},
		Parser:             epub.OPFParser{},
		Segmenter:          segment.NewParagraphSegmenter(),
		TM:                 store,
		Translator:         translator,
		Recomposer:         epub.EPUBRecomposer{},
		EPUBCheck:          qa.EPUBChecker{Binary: cfg.EPUBCheckBin, JarPath: cfg.EPUBCheckJar},
		Glossary:           gloss,
		PostEditor:         postEditor,
		LiteraryTranslator: literaryTranslator,
		NarrativeStore:     narrativeStore,
		NarrativeProfiler:  narrativeProfiler,
		EscalateMinChars:   cfg.LLMEscalateMinChars,
		QualityEstimator:   cfg.qualityEstimator(),
		EscalateBelowScore: cfg.EscalateBelowScore,
		CostModel: cost.Model{
			PerSegmentUSD:    cfg.CostPerSegmentUSD,
			Per1000CharsUSD:  cfg.CostPer1000CharsUSD,
			LLMPerSegmentUSD: cfg.CostLLMPerSegmentUSD,
		},
		BudgetUSD:  cfg.PerBookBudgetUSD,
		DailyUSD:   cfg.DailyBudgetUSD,
		MonthlyUSD: cfg.MonthlyBudgetUSD,
		Logger:     logger,
	}

	baseCtx, stop := signalContext()
	defer stop()
	ctx, cancel := withRunTimeout(baseCtx, cfg.RunTimeoutSeconds)
	defer cancel()

	result, err := orch.Run(ctx, pipeline.RunConfig{
		InputPath:   cmd.Input,
		OutputDir:   outputDir,
		SourceLang:  cmd.SourceLang,
		TargetLang:  cmd.TargetLang,
		RunID:       runID,
		BookID:      bookIDFromPath(cmd.Input),
		Mode:        cmd.Mode,
		SeriesID:    cmd.Series,
		UpdateBible: cmd.UpdateBible,
		Resume:      cmd.Resume,
	})
	if err != nil {
		writef(stderr, "run failed: %v\n", err)
		return exitCodeForError(err)
	}

	writef(stdout, "run %s completed: %d segments, %d tm hits, %d nmt, output=%s\n",
		runID, result.TotalSegments, result.TMHits, result.NMTSegments, result.OutputEPUB)
	return exitcode.Success
}

// runEstimate parses+segments the book without translating and prints a cost
// projection across NMT-only / hybrid / full-LLM strategies.
func runEstimate(stdout, stderr io.Writer, cmd cli.EstimateCommand, cfg RuntimeConfig) int {
	orch := &pipeline.Orchestrator{
		Extractor: epub.ZipExtractor{},
		Parser:    epub.OPFParser{},
		Segmenter: segment.NewParagraphSegmenter(),
		CostModel: cost.Model{
			PerSegmentUSD:    cfg.CostPerSegmentUSD,
			Per1000CharsUSD:  cfg.CostPer1000CharsUSD,
			LLMPerSegmentUSD: cfg.CostLLMPerSegmentUSD,
		},
	}

	rates := cost.TokenRates{
		InputPerMTokUSD:      cfg.LLMInputPerMTokUSD,
		OutputPerMTokUSD:     cfg.LLMOutputPerMTokUSD,
		OutputExpansion:      cfg.LLMOutputExpansion,
		PromptOverheadTokens: cfg.LLMPromptOverheadToks,
	}

	plan, err := orch.Plan(context.Background(), pipeline.RunConfig{
		InputPath:  cmd.Input,
		SourceLang: cmd.SourceLang,
		TargetLang: cmd.TargetLang,
		BookID:     bookIDFromPath(cmd.Input),
	}, rates, cmd.EscalationRate)
	if err != nil {
		writef(stderr, "estimate failed: %v\n", err)
		return exitCodeForError(err)
	}

	writef(stdout, "estimate for %s: %d segments, %d source chars (~%d tokens), escalation=%.0f%%\n",
		plan.BookID, plan.TotalSegments, plan.TotalSourceChars, plan.TotalSourceChars/4, cmd.EscalationRate*100)
	writef(stdout, "%-10s %10s %12s %12s %12s\n", "strategy", "llm_segs", "nmt_usd", "llm_usd", "total_usd")
	for _, s := range plan.Scenarios {
		writef(stdout, "%-10s %10d %12.4f %12.4f %12.4f\n", s.Name, s.LLMSegments, s.NMTCostUSD, s.LLMCostUSD, s.TotalUSD)
	}
	return exitcode.Success
}

// runBatch translates every .epub in a directory through one shared-TM,
// shared-glossary orchestrator (cross-book consistency + reuse), continuing on
// per-book failure and emitting an aggregate batch-report.json.
func runBatch(stdout, stderr io.Writer, cmd cli.BatchCommand, cfg RuntimeConfig) int {
	epubs, err := filepath.Glob(filepath.Join(cmd.InputDir, "*.epub"))
	if err != nil {
		writef(stderr, "unable to scan input dir: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	sort.Strings(epubs)
	if len(epubs) == 0 {
		writef(stderr, "no .epub files found in %s\n", cmd.InputDir)
		return exitcode.InvalidInputConfig
	}

	store, err := tm.NewSQLiteStore(cfg.TMPath)
	if err != nil {
		writef(stderr, "unable to open translation memory: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = store.Close() }()

	var gloss *glossary.Glossary
	if cmd.Glossary != "" {
		gloss, err = glossary.Load(cmd.Glossary)
		if err != nil {
			writef(stderr, "unable to load glossary: %v\n", err)
			return exitcode.InvalidInputConfig
		}
	}

	translator := translate.NewRetryingTranslator(
		translate.NewProvider(cfg.NMTProvider, cfg.NMTEndpoint, cfg.NMTAPIKey),
		cfg.NMTMaxAttempts, time.Duration(cfg.NMTRetryBaseMS)*time.Millisecond,
	)
	var postEditor llm.PostEditor
	if cfg.LLMEnabled && cfg.LLMEndpoint != "" {
		postEditor = llm.NewHTTPPostEditor(cfg.LLMEndpoint, cfg.LLMAPIKey)
	}

	orch := &pipeline.Orchestrator{
		Extractor:          epub.ZipExtractor{},
		Parser:             epub.OPFParser{},
		Segmenter:          segment.NewParagraphSegmenter(),
		TM:                 store,
		Translator:         translator,
		Recomposer:         epub.EPUBRecomposer{},
		EPUBCheck:          qa.EPUBChecker{Binary: cfg.EPUBCheckBin, JarPath: cfg.EPUBCheckJar},
		Glossary:           gloss,
		PostEditor:         postEditor,
		EscalateMinChars:   cfg.LLMEscalateMinChars,
		QualityEstimator:   cfg.qualityEstimator(),
		EscalateBelowScore: cfg.EscalateBelowScore,
		CostModel: cost.Model{
			PerSegmentUSD:    cfg.CostPerSegmentUSD,
			Per1000CharsUSD:  cfg.CostPer1000CharsUSD,
			LLMPerSegmentUSD: cfg.CostLLMPerSegmentUSD,
		},
		BudgetUSD:  cfg.PerBookBudgetUSD,
		DailyUSD:   cfg.DailyBudgetUSD,
		MonthlyUSD: cfg.MonthlyBudgetUSD,
	}

	baseCtx, stop := signalContext()
	defer stop()

	outcomes := make([]pipeline.BookOutcome, 0, len(epubs))
	var firstErr error
	for _, in := range epubs {
		if baseCtx.Err() != nil {
			writef(stderr, "batch interrupted; stopping before remaining books\n")
			break
		}

		bookID := bookIDFromPath(in)
		bookDir := filepath.Join(cmd.OutputDir, bookID)

		logger, closeLog, lerr := pipeline.NewRunLogger(bookDir, cfg.LogLevel)
		if lerr != nil {
			outcomes = append(outcomes, pipeline.BookOutcome{BookID: bookID, RunID: bookID, OK: false, Error: lerr.Error()})
			writef(stderr, "book %s: logger error: %v\n", bookID, lerr)
			continue
		}
		orch.Logger = logger

		ctx, cancel := withRunTimeout(baseCtx, cfg.RunTimeoutSeconds)
		result, runErr := orch.Run(ctx, pipeline.RunConfig{
			InputPath: in, OutputDir: bookDir,
			SourceLang: cmd.SourceLang, TargetLang: cmd.TargetLang,
			RunID: bookID, BookID: bookID,
		})
		cancel()
		_ = closeLog()

		outcome := pipeline.BookOutcome{BookID: bookID, RunID: bookID, OK: runErr == nil, Result: result}
		if runErr != nil {
			outcome.Error = runErr.Error()
			if firstErr == nil {
				firstErr = runErr
			}
			writef(stdout, "book %-20s FAILED: %v\n", bookID, runErr)
		} else {
			if score, ok := scoreAgainstReference(stderr, cmd.ReferenceDir, bookID, result.OutputEPUB); ok {
				outcome.ChrF = &score
			}
			writef(stdout, "book %-20s ok: %d segments, %d tm hits, %d nmt, %d llm%s\n",
				bookID, result.TotalSegments, result.TMHits, result.NMTSegments, result.LLMSegments,
				chrFSuffix(outcome.ChrF))
		}
		outcomes = append(outcomes, outcome)
	}

	summary := pipeline.NewBatchSummary(outcomes)
	if err := pipeline.WriteBatchReport(cmd.OutputDir, summary); err != nil {
		writef(stderr, "unable to write batch report: %v\n", err)
	}

	writef(stdout, "\nbatch: %d books, %d ok, %d failed | %d segments, tm-hit-ratio %.1f%%, total cost $%.4f, savings $%.4f%s\n",
		summary.Books, summary.Succeeded, summary.Failed, summary.TotalSegments,
		summary.TMHitRatio*100, summary.TotalActualCostUSD, summary.TotalSavingsUSD,
		avgChrFSuffix(summary))

	if summary.Failed > 0 {
		return exitCodeForError(firstErr)
	}
	return exitcode.Success
}

// scoreAgainstReference computes chrF for a produced book against
// <referenceDir>/<bookID>.epub when that reference exists.
func scoreAgainstReference(stderr io.Writer, referenceDir, bookID, producedEPUB string) (float64, bool) {
	if referenceDir == "" {
		return 0, false
	}
	refPath := filepath.Join(referenceDir, bookID+".epub")
	if _, err := os.Stat(refPath); err != nil {
		return 0, false // no reference for this book; skip silently
	}

	ctx := context.Background()
	cand, err := bookPlainText(ctx, producedEPUB)
	if err != nil {
		writef(stderr, "book %s: unable to read produced epub for eval: %v\n", bookID, err)
		return 0, false
	}
	ref, err := bookPlainText(ctx, refPath)
	if err != nil {
		writef(stderr, "book %s: unable to read reference epub for eval: %v\n", bookID, err)
		return 0, false
	}
	return eval.ChrF(cand, ref, eval.DefaultOrder, eval.DefaultBeta), true
}

func chrFSuffix(score *float64) string {
	if score == nil {
		return ""
	}
	return fmt.Sprintf(", chrF %.4f", *score)
}

func avgChrFSuffix(s pipeline.BatchSummary) string {
	if s.EvaluatedBooks == 0 {
		return ""
	}
	return fmt.Sprintf(", avg chrF %.4f over %d books", s.AvgChrF, s.EvaluatedBooks)
}

// runEval compares a candidate translation against a human reference EPUB using
// chrF and writes eval-report.json beside the candidate.
func runEval(stdout, stderr io.Writer, cmd cli.EvalCommand) int {
	ctx := context.Background()

	candText, err := bookPlainText(ctx, cmd.Candidate)
	if err != nil {
		writef(stderr, "unable to read candidate: %v\n", err)
		return exitCodeForError(err)
	}
	refText, err := bookPlainText(ctx, cmd.Reference)
	if err != nil {
		writef(stderr, "unable to read reference: %v\n", err)
		return exitCodeForError(err)
	}

	score := eval.ChrF(candText, refText, eval.DefaultOrder, eval.DefaultBeta)

	report := map[string]any{
		"candidate":       cmd.Candidate,
		"reference":       cmd.Reference,
		"metric":          "chrf",
		"score":           score,
		"candidate_chars": len([]rune(candText)),
		"reference_chars": len([]rune(refText)),
	}
	if data, err := json.MarshalIndent(report, "", "  "); err == nil {
		out := filepath.Join(filepath.Dir(cmd.Candidate), "eval-report.json")
		if werr := os.WriteFile(out, append(data, '\n'), 0o644); werr != nil {
			writef(stderr, "unable to write eval report: %v\n", werr)
		}
	}

	writef(stdout, "chrF = %.4f (candidate %d chars vs reference %d chars)\n",
		score, len([]rune(candText)), len([]rune(refText)))
	return exitcode.Success
}

// runStyleProfile seeds or refreshes a series bible from an EPUB sample set.
func runStyleProfile(stdout, stderr io.Writer, cmd cli.StyleProfileCommand, cfg RuntimeConfig) int {
	store, err := narrative.NewSQLiteStore(runtimeNarrativePath(cfg))
	if err != nil {
		writef(stderr, "unable to open narrative store: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = store.Close() }()

	baseCtx, stop := signalContext()
	defer stop()
	ctx, cancel := withRunTimeout(baseCtx, cfg.RunTimeoutSeconds)
	defer cancel()

	samples, err := styleProfileSamples(ctx, cmd.Input, cmd.Samples)
	if err != nil {
		writef(stderr, "unable to collect style samples: %v\n", err)
		return exitCodeForError(err)
	}

	cachedChatClient, closeCache, err := cfg.newCachedChatClient()
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = closeCache() }()

	profiler := narrative.Profiler{
		Chat:  cachedChatClient,
		Model: cfg.LLMModel,
	}
	bible, err := profiler.Seed(ctx, samples)
	if err != nil {
		writef(stderr, "unable to seed style profile: %v\n", err)
		return exitCodeForError(err)
	}
	bible.SeriesID = cmd.Series

	if err := store.Save(ctx, bible); err != nil {
		writef(stderr, "unable to save style profile: %v\n", err)
		return exitcode.InvalidInputConfig
	}

	saved, found, err := store.Load(ctx, cmd.Series)
	if err != nil {
		writef(stderr, "unable to reload style profile: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	if !found {
		writef(stderr, "unable to reload style profile: series %q not found\n", cmd.Series)
		return exitcode.InvalidInputConfig
	}

	writef(stdout, "style-profile %s saved: version %d, %d samples, %d characters, %d address rules\n",
		cmd.Series, saved.Version, len(samples), len(saved.Characters), len(saved.Address))
	return exitcode.Success
}

func runtimeNarrativePath(cfg RuntimeConfig) string {
	if cfg.NarrativePath == "" {
		return "./narrative.sqlite"
	}
	return cfg.NarrativePath
}

// runSuggest scans an EPUB for recurring proper-noun candidates and prints them
// (and optionally writes a glossary JSON skeleton) to bootstrap a series glossary.
func runSuggest(stdout, stderr io.Writer, cmd cli.SuggestCommand) int {
	text, err := bookPlainText(context.Background(), cmd.Input)
	if err != nil {
		writef(stderr, "unable to read epub: %v\n", err)
		return exitCodeForError(err)
	}

	candidates := glossary.Suggest(text, cmd.MinCount)

	if cmd.Out != "" {
		data, err := json.MarshalIndent(glossary.SuggestTerms(candidates), "", "  ")
		if err != nil {
			writef(stderr, "unable to marshal glossary skeleton: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		if err := os.WriteFile(cmd.Out, append(data, '\n'), 0o644); err != nil {
			writef(stderr, "unable to write glossary skeleton: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		writef(stdout, "wrote %d candidates to %s (edit term_fr, then pass via --glossary)\n", len(candidates), cmd.Out)
	}

	limit := len(candidates)
	if limit > 30 {
		limit = 30
	}
	writef(stdout, "%d proper-noun candidates (min-count %d); top %d:\n", len(candidates), cmd.MinCount, limit)
	for _, c := range candidates[:limit] {
		writef(stdout, "  %5d  %s\n", c.Count, c.Term)
	}
	return exitcode.Success
}

// styleProfileSamples extracts up to limit spine-document samples in order.
func styleProfileSamples(ctx context.Context, path string, limit int) ([]string, error) {
	raw, err := epub.ZipExtractor{}.Extract(ctx, path)
	if err != nil {
		return nil, &pipeline.StageError{Stage: pipeline.StageExtract, Err: err}
	}
	book, err := epub.OPFParser{}.Parse(ctx, raw)
	if err != nil {
		return nil, &pipeline.StageError{Stage: pipeline.StageParse, Err: err}
	}

	if limit < 1 {
		limit = 1
	}

	samples := make([]string, 0, limit)
	for _, item := range book.Spine {
		mi, ok := book.Manifest.Items[item.IDRef]
		if !ok || mi.MediaType != "application/xhtml+xml" || hasProperty(mi, "nav") {
			continue
		}
		doc, ok := book.Contents[mi.Href]
		if !ok || doc == nil {
			continue
		}

		var sb strings.Builder
		for _, node := range doc.Nodes {
			if strings.TrimSpace(node.Text) == "" {
				continue
			}
			sb.WriteString(node.Text)
			sb.WriteByte('\n')
		}
		sample := strings.TrimSpace(sb.String())
		if sample == "" {
			continue
		}
		samples = append(samples, sample)
		if len(samples) >= limit {
			break
		}
	}

	return samples, nil
}

func hasProperty(item epub.ManifestItem, property string) bool {
	for _, p := range item.Properties {
		if p == property {
			return true
		}
	}
	return false
}

// bookPlainText extracts a book's visible text in spine order for evaluation.
func bookPlainText(ctx context.Context, path string) (string, error) {
	raw, err := epub.ZipExtractor{}.Extract(ctx, path)
	if err != nil {
		return "", &pipeline.StageError{Stage: pipeline.StageExtract, Err: err}
	}
	book, err := epub.OPFParser{}.Parse(ctx, raw)
	if err != nil {
		return "", &pipeline.StageError{Stage: pipeline.StageParse, Err: err}
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
