package commandexec

import (
	"context"
	"io"
	"os"

	"github.com/ECecillo/tomeshift/internal/cli"
	"github.com/ECecillo/tomeshift/internal/config"
	"github.com/ECecillo/tomeshift/internal/epub"
	"github.com/ECecillo/tomeshift/internal/exitcode"
	"github.com/ECecillo/tomeshift/internal/literary"
	"github.com/ECecillo/tomeshift/internal/pipeline"
	"github.com/ECecillo/tomeshift/internal/segment"
	"github.com/ECecillo/tomeshift/internal/tm"
	"github.com/ECecillo/tomeshift/internal/translate"
)

// runEstimate parses+segments the book without translating and prints a cost
// projection across NMT-only / hybrid / full-LLM strategies.
func runEstimate(stdout, stderr io.Writer, cmd cli.EstimateCommand, cfg config.Config) int {
	orch := &pipeline.Orchestrator{
		Extractor: epub.ZipExtractor{},
		Parser:    epub.OPFParser{},
		Segmenter: segment.NewParagraphSegmenter(),
		CostModel: cfg.CostModel(),
		// Never called — but it names the engine, and the free-tier counter is
		// per engine. Without it `estimate` would read a different allowance
		// from the one the run enforces.
		Translator: translate.NewProvider(cfg.NMTProvider, cfg.NMTEndpoint, cfg.NMTAPIKey),
	}

	ctx := context.Background()

	// Two things want the ledger: the free tier, which is per calendar month, so
	// what a book costs depends on what has already been spent in it; and the
	// calibration below. Plan reads the first off the store the same way a run
	// does, which is what keeps the two projections in agreement — so the store
	// only has to be attached, not consulted here.
	//
	// An absent ledger is not created. `estimate` makes no provider call, and a
	// dry run that leaves a new database behind is a side effect nobody asked
	// for; with no ledger there is nothing spent and nothing measured, which is
	// exactly what a missing file means.
	if _, err := os.Stat(cfg.TMPath); err == nil {
		store, err := tm.NewSQLiteStore(cfg.TMPath)
		if err != nil {
			writef(stderr, "unable to open translation memory: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		defer func() { _ = store.Close() }()
		orch.TM = store
	}

	// The same calibration a run applies, from the same ledger. `estimate` is
	// the one place it is worth saying out loud: a projection that silently
	// changed basis between two invocations would be worse than a wrong one.
	var note string
	if orch.TM != nil {
		orch.CostModel, note = calibrateLLMTokens(ctx, orch.TM, cfg, orch.CostModel)
	}

	plan, err := orch.Plan(ctx, pipeline.RunConfig{
		InputPath:  cmd.Input,
		SourceLang: cmd.SourceLang,
		TargetLang: cmd.TargetLang,
		BookID:     bookIDFromPath(cmd.Input),
	}, literary.ResolveBatchSize(cfg.LLMBatchSize), cmd.EscalationRate)
	if err != nil {
		writef(stderr, "estimate failed: %v\n", err)
		return exitCodeForError(err)
	}

	writef(stdout, "estimate for %s: %d segments over %d chapters, %d source chars, batch=%d, escalation=%.0f%%\n",
		plan.BookID, plan.TotalSegments, plan.Chapters, plan.TotalSourceChars, plan.BatchSize, cmd.EscalationRate*100)
	if plan.ReusableSegments > 0 {
		writef(stdout, "translation memory: %d segments already translated, %d left to pay for (%d source chars)\n",
			plan.ReusableSegments, plan.PendingSegments, plan.PendingSourceChar)
	}
	if cfg.NMTFreeCharsPerMonth > 0 {
		writef(stdout, "free tier: %d chars/month, %d billable this run\n",
			cfg.NMTFreeCharsPerMonth, plan.Scenarios[0].BillableChars)
	}
	if note != "" {
		writef(stdout, "%s\n", note)
	} else {
		writef(stdout, "calibration: none yet — output tokens come from the character heuristic\n")
	}
	writef(stdout, "%-10s %10s %10s %12s %12s %12s %12s\n",
		"strategy", "llm_segs", "llm_calls", "in_tokens", "nmt_usd", "llm_usd", "total_usd")
	for _, s := range plan.Scenarios {
		writef(stdout, "%-10s %10d %10d %12d %12.4f %12.4f %12.4f\n",
			s.Name, s.LLMSegments, s.LLMCalls, s.InputTokens, s.NMTCostUSD, s.LLMCostUSD, s.EstimatedUSD)
	}
	return exitcode.Success
}
