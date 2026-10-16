package commandexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/ECecillo/tomeshift/internal/cli"
	"github.com/ECecillo/tomeshift/internal/config"
	"github.com/ECecillo/tomeshift/internal/epub"
	"github.com/ECecillo/tomeshift/internal/eval"
	"github.com/ECecillo/tomeshift/internal/exitcode"
	"github.com/ECecillo/tomeshift/internal/glossary"
	"github.com/ECecillo/tomeshift/internal/literary"
	"github.com/ECecillo/tomeshift/internal/llm"
	"github.com/ECecillo/tomeshift/internal/narrative"
	"github.com/ECecillo/tomeshift/internal/pipeline"
	"github.com/ECecillo/tomeshift/internal/progress"
	"github.com/ECecillo/tomeshift/internal/qa"
	"github.com/ECecillo/tomeshift/internal/segment"
	"github.com/ECecillo/tomeshift/internal/tm"
	"github.com/ECecillo/tomeshift/internal/translate"
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

// newStages assembles the fully-wired stage collaborators for mode: the NMT
// provider with its retries, the optional post-editor, the QA gate, the cost
// guardrail, and — for literary mode only — the series bible and chat client.
//
// It is one step rather than two on purpose: a half-built set of stages is a
// shape no caller should be able to hold. The returned closer releases the
// resources literary mode opens, and is a no-op for every other mode, so
// callers never branch on it. `batch` passes an empty mode.
func newStages(
	cfg config.Config, store tm.Store, gloss *glossary.Glossary, mode string,
) (*pipeline.Stages, func(), error) {
	// Fail here rather than at the first segment: by then the book has been
	// extracted, parsed, segmented and budgeted, and the error would read as a
	// provider outage instead of a missing setting.
	if translate.RequiresEndpoint(cfg.NMTProvider) && cfg.NMTEndpoint == "" {
		return nil, nil, fmt.Errorf(
			"unable to build the %q NMT provider: set TOMESHIFT_NMT_ENDPOINT, err: %w",
			cfg.NMTProvider, translate.ErrEmptyEndpoint)
	}
	if translate.RequiresAPIKey(cfg.NMTProvider) && cfg.NMTAPIKey == "" {
		return nil, nil, fmt.Errorf(
			"unable to build the %q NMT provider: set TOMESHIFT_NMT_API_KEY, err: %w",
			cfg.NMTProvider, translate.ErrEmptyAPIKey)
	}

	var postEditor llm.PostEditor
	if cfg.LLMEnabled && cfg.LLMEndpoint != "" {
		postEditor = llm.NewHTTPPostEditor(cfg.LLMEndpoint, cfg.LLMAPIKey)
	}

	stages := &pipeline.Stages{
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
		return stages, func() {}, nil
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

	// Recorded, not routed: the clients below are already built. The ledger
	// keeps them so a measured token count stays reusable, and only the literary
	// path sets them — the HTTP post-editor is picked by endpoint, not by model,
	// and reports no usage to attribute anyway.
	stages.LLMProvider = cfg.LLMChatProvider
	stages.LLMModel = cfg.LLMModel

	stages.NarrativeStore = bible
	stages.NarrativeProfiler = &narrative.Profiler{Chat: chat, Model: cfg.LLMModel}
	stages.LiteraryTranslator = &literary.Translator{
		Chat:      chat,
		Model:     cfg.LLMModel,
		BatchSize: cfg.LLMBatchSize,
		Glossary:  gloss,
	}
	return stages, func() { _ = closeCache(); _ = bible.Close() }, nil
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

	stages, closeStages, err := newStages(cfg, store, gloss, cmd.Mode)
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer closeStages()
	stages.Logger = logger

	baseCtx, stop := signalContext()
	defer stop()
	ctx, cancel := withRunTimeout(baseCtx, cfg.RunTimeoutSeconds)
	defer cancel()

	// Before RunBook, because ProjectCost enforces the cap on the calibrated model.
	var note string
	if stages.CostModel, note = calibrateLLMTokens(ctx, store, cfg, stages.CostModel); note != "" {
		logger.Info(note)
	}

	bar := newBarReporter(progress.New(stderr, !cmd.NoProgress && progress.IsTerminal(stderr), ""))
	steps := &pipeline.InProcessSteps{Stages: stages, Ctx: ctx}
	rt := &pipeline.InProcessRuntime{
		Ctx:   ctx,
		Steps: steps,
		// stderr, not stdout: the bar is for a human watching, and stdout stays
		// the single parsable summary line.
		Progress: bar,
	}

	result, err := pipeline.RunBook(steps, rt, pipeline.BookInput{
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
	// The line is closed before the caller writes its failure message to the
	// same stream, so the error never lands inside the bar.
	bar.Finish()
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
	stages, closeStages, err := newStages(cfg, store, gloss, pipeline.ModeNMT)
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer closeStages()

	baseCtx, stop := signalContext()
	defer stop()

	// Calibrated once for the whole batch: it describes the model, not the book.
	stages.CostModel, _ = calibrateLLMTokens(baseCtx, store, cfg, stages.CostModel)

	// Resolved once: every book gets its own bar, all writing to the same line.
	showProgress := !cmd.NoProgress && progress.IsTerminal(stderr)

	books := make([]pipeline.BookInput, 0, len(epubs))
	for _, in := range epubs {
		bookID := bookIDFromPath(in)
		books = append(books, pipeline.BookInput{
			InputPath: in, OutputDir: filepath.Join(cmd.OutputDir, bookID),
			SourceLang: cmd.SourceLang, TargetLang: cmd.TargetLang,
			RunID: bookID, BookID: bookID,
		})
	}

	steps := &pipeline.InProcessSteps{Stages: stages, Ctx: baseCtx}
	rt := &pipeline.InProcessRuntime{Ctx: baseCtx, Steps: steps}

	var firstErr error
	var closeLog func() error
	var cancel context.CancelFunc
	var bar *barReporter
	hooks := pipeline.SeriesHooks{
		Prepare: func(in pipeline.BookInput) {
			logger, cl, lerr := pipeline.NewRunLogger(in.OutputDir, cfg.LogLevel)
			if lerr != nil {
				writef(stderr, "book %s: logger error: %v\n", in.BookID, lerr)
				logger, cl = zap.NewNop(), func() error { return nil }
			}
			closeLog = cl
			stages.Logger = logger
			bar = newBarReporter(progress.New(stderr, showProgress, in.BookID))
			rt.Progress = bar
			ctx, c := withRunTimeout(baseCtx, cfg.RunTimeoutSeconds)
			cancel = c
			steps.Ctx, rt.Ctx = ctx, ctx
		},
		Score: func(bookID, outputEPUB string) (float64, bool) {
			return scoreAgainstReference(stderr, cmd.ReferenceDir, bookID, outputEPUB)
		},
		Done: func(in pipeline.BookInput, outcome pipeline.BookOutcome, runErr error) {
			if cancel != nil {
				cancel()
			}
			// Point the shared context back at the stable base before the next
			// book. A per-book run timeout makes withRunTimeout return a
			// cancellable child; leaving the just-cancelled child in place would
			// make RunSeries' top-of-loop Cancelled() check fire on book 2 and
			// silently skip the rest of the series.
			steps.Ctx, rt.Ctx = baseCtx, baseCtx
			if bar != nil {
				bar.Finish()
			}
			if closeLog != nil {
				_ = closeLog()
			}
			if runErr != nil {
				if firstErr == nil {
					firstErr = runErr
				}
				writef(stdout, "book %-20s FAILED: %v\n", in.BookID, runErr)
				return
			}
			r := outcome.Result
			writef(stdout, "book %-20s ok: %d segments, %d tm hits, %d nmt, %d llm%s\n",
				in.BookID, r.TotalSegments, r.TMHits, r.NMTSegments, r.LLMSegments,
				chrFSuffix(outcome.ChrF))
		},
	}

	summary := pipeline.RunSeries(steps, rt, books, hooks)
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
