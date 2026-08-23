package commandexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ECecillo/ebooks-translator/internal/cli"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/eval"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/literary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/progress"
	"github.com/ECecillo/ebooks-translator/internal/qa"
	"github.com/ECecillo/ebooks-translator/internal/segment"
	"github.com/ECecillo/ebooks-translator/internal/tm"
	"github.com/ECecillo/ebooks-translator/internal/translate"
)

// openTranslationInputs opens the shared TM ledger and loads the optional
// glossary — the two resources both `run` and `batch` need before they can
// build an orchestrator. The caller must close the store.
func openTranslationInputs(cfg config.Config, glossaryPath string) (*tm.SQLiteStore, *glossary.Glossary, error) {
	store, err := tm.NewSQLiteStore(cfg.TMPath)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to open translation memory, err: %w", err)
	}
	if glossaryPath == "" {
		return store, nil, nil
	}

	gloss, err := glossary.Load(glossaryPath)
	if err != nil {
		_ = store.Close()
		return nil, nil, fmt.Errorf("unable to load glossary, err: %w", err)
	}
	return store, gloss, nil
}

// newOrchestrator assembles a fully-wired orchestrator for mode: the NMT
// provider with its retries, the optional post-editor, the QA gate, the cost
// guardrail, and — for literary mode only — the series bible and chat client.
//
// It is one step rather than two on purpose: a half-built orchestrator is a
// shape no caller should be able to hold. The returned closer releases the
// resources literary mode opens, and is a no-op for every other mode, so
// callers never branch on it. `batch` passes an empty mode.
func newOrchestrator(
	cfg config.Config, store tm.Store, gloss *glossary.Glossary, mode string,
) (*pipeline.Orchestrator, func(), error) {
	// Fail here rather than at the first segment: by then the book has been
	// extracted, parsed, segmented and budgeted, and the error would read as a
	// provider outage instead of a missing setting.
	if translate.RequiresEndpoint(cfg.NMTProvider) && cfg.NMTEndpoint == "" {
		return nil, nil, fmt.Errorf(
			"unable to build the %q NMT provider: set EPUB_TRANSLATOR_NMT_ENDPOINT, err: %w",
			cfg.NMTProvider, translate.ErrEmptyEndpoint)
	}
	if translate.RequiresAPIKey(cfg.NMTProvider) && cfg.NMTAPIKey == "" {
		return nil, nil, fmt.Errorf(
			"unable to build the %q NMT provider: set EPUB_TRANSLATOR_NMT_API_KEY, err: %w",
			cfg.NMTProvider, translate.ErrEmptyAPIKey)
	}

	var postEditor llm.PostEditor
	if cfg.LLMEnabled && cfg.LLMEndpoint != "" {
		postEditor = llm.NewHTTPPostEditor(cfg.LLMEndpoint, cfg.LLMAPIKey)
	}

	orch := &pipeline.Orchestrator{
		Extractor: epub.ZipExtractor{},
		Parser:    epub.OPFParser{},
		Segmenter: segment.NewParagraphSegmenter(),
		TM:        store,
		Translator: translate.NewRetryingTranslator(
			translate.NewProvider(cfg.NMTProvider, cfg.NMTEndpoint, cfg.NMTAPIKey),
			cfg.NMTMaxAttempts,
			time.Duration(cfg.NMTRetryBaseMS)*time.Millisecond,
		),
		Recomposer:         epub.EPUBRecomposer{},
		EPUBCheck:          qa.EPUBChecker{Binary: cfg.EPUBCheckBin, JarPath: cfg.EPUBCheckJar},
		Glossary:           gloss,
		PostEditor:         postEditor,
		EscalateMinChars:   cfg.LLMEscalateMinChars,
		QualityEstimator:   qualityEstimator(cfg),
		EscalateBelowScore: cfg.EscalateBelowScore,
		CostModel:          cfg.CostModel(),
		BudgetUSD:          cfg.PerBookBudgetUSD,
		DailyUSD:           cfg.DailyBudgetUSD,
		MonthlyUSD:         cfg.MonthlyBudgetUSD,
	}

	if mode != pipeline.ModeLiterary {
		return orch, func() {}, nil
	}

	bible, err := narrative.NewSQLiteStore(cfg.NarrativePath)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to open narrative store, err: %w", err)
	}
	chat, closeCache, err := newCachedChatClient(cfg)
	if err != nil {
		_ = bible.Close()
		return nil, nil, err
	}

	orch.NarrativeStore = bible
	orch.NarrativeProfiler = &narrative.Profiler{Chat: chat, Model: cfg.LLMModel}
	orch.LiteraryTranslator = &literary.Translator{
		Chat:      chat,
		Model:     cfg.LLMModel,
		BatchSize: cfg.LLMBatchSize,
		Glossary:  gloss,
	}
	return orch, func() { _ = closeCache(); _ = bible.Close() }, nil
}

// runPipeline assembles the orchestrator from runtime config and executes a
// single translation run, mapping stage failures to process exit codes.
func runPipeline(stdout, stderr io.Writer, cmd cli.RunCommand, cfg config.Config) int {
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

	store, gloss, err := openTranslationInputs(cfg, cmd.Glossary)
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = store.Close() }()

	orch, closeOrch, err := newOrchestrator(cfg, store, gloss, cmd.Mode)
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer closeOrch()
	orch.Logger = logger

	// stderr, not stdout: the bar is for a human watching, and stdout stays the
	// single parsable summary line.
	orch.Progress = progress.New(stderr, !cmd.NoProgress && progress.IsTerminal(stderr), "")

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

// runBatch translates every .epub in a directory through one shared-TM,
// shared-glossary orchestrator (cross-book consistency + reuse), continuing on
// per-book failure and emitting an aggregate batch-report.json.
func runBatch(stdout, stderr io.Writer, cmd cli.BatchCommand, cfg config.Config) int {
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

	store, gloss, err := openTranslationInputs(cfg, cmd.Glossary)
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = store.Close() }()

	// Batch is NMT-only, so it needs none of the literary collaborators.
	orch, closeOrch, err := newOrchestrator(cfg, store, gloss, pipeline.ModeNMT)
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer closeOrch()

	baseCtx, stop := signalContext()
	defer stop()

	// Resolved once: every book gets its own bar, all writing to the same line.
	showProgress := !cmd.NoProgress && progress.IsTerminal(stderr)

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
		orch.Progress = progress.New(stderr, showProgress, bookID)

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
