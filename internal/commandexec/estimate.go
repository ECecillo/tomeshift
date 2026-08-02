package commandexec

import (
	"context"
	"io"

	"github.com/ECecillo/ebooks-translator/internal/cli"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/literary"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/segment"
	"github.com/ECecillo/ebooks-translator/internal/tm"
)

// runEstimate parses+segments the book without translating and prints a cost
// projection across NMT-only / hybrid / full-LLM strategies.
func runEstimate(stdout, stderr io.Writer, cmd cli.EstimateCommand, cfg config.Config) int {
	orch := &pipeline.Orchestrator{
		Extractor: epub.ZipExtractor{},
		Parser:    epub.OPFParser{},
		Segmenter: segment.NewParagraphSegmenter(),
		CostModel: cfg.CostModel(),
	}

	ctx := context.Background()

	// The free tier is per calendar month, so what a book costs depends on what
	// the ledger already spent this month. Plan reads it off the store the same
	// way a run does, which is what keeps the two projections in agreement — so
	// the store only has to be attached, not consulted here. It is left unopened
	// when no free tier is configured, so `estimate` does not create a ledger as
	// a side effect of a dry run.
	if cfg.NMTFreeCharsPerMonth > 0 {
		store, err := tm.NewSQLiteStore(cfg.TMPath)
		if err != nil {
			writef(stderr, "unable to open translation memory: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		defer func() { _ = store.Close() }()
		orch.TM = store
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
	if cfg.NMTFreeCharsPerMonth > 0 {
		writef(stdout, "free tier: %d chars/month, %d billable this run\n",
			cfg.NMTFreeCharsPerMonth, plan.Scenarios[0].BillableChars)
	}
	writef(stdout, "%-10s %10s %10s %12s %12s %12s %12s\n",
		"strategy", "llm_segs", "llm_calls", "in_tokens", "nmt_usd", "llm_usd", "total_usd")
	for _, s := range plan.Scenarios {
		writef(stdout, "%-10s %10d %10d %12d %12.4f %12.4f %12.4f\n",
			s.Name, s.LLMSegments, s.LLMCalls, s.InputTokens, s.NMTCostUSD, s.LLMCostUSD, s.EstimatedUSD)
	}
	return exitcode.Success
}
