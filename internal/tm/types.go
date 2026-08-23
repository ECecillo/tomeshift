package tm

import (
	"context"
	"strings"
	"time"
)

// Engine tags recorded on a TM entry. The distinction is load-bearing: a hit on
// a raw draft must never be served as a finished translation, or the segment
// skips refinement on every subsequent run.
//
// The NMT adapters in internal/translate name themselves with EngineNMTPrefix
// (nmt-http, nmt-libretranslate, nmt-google); the refined paths use the two
// constants below.
const (
	EngineNMTPrefix   = "nmt-"
	EngineLiterary    = "literary"
	EngineLLMPostEdit = "llm-postedit"
)

// IsDraftEngine reports whether an engine tag marks a raw machine translation
// rather than a finished one.
func IsDraftEngine(engine string) bool {
	return strings.HasPrefix(engine, EngineNMTPrefix)
}

// Entry is a single translation-memory record.
type Entry struct {
	SourceHash   string
	SourceText   string
	TargetText   string
	SourceLang   string
	TargetLang   string
	Engine       string
	QualityScore float64
}

// RunSpend records the realized cost of a completed run for budget accounting.
type RunSpend struct {
	RunID     string
	BookID    string
	StartedAt time.Time
	EndedAt   time.Time
	// ActualCostUSD is what the run was billed, free tier already deducted.
	ActualCostUSD float64
	// NMTEngine tags which provider consumed NMTChars. The allowance being
	// counted belongs to one provider, so a run against the local dev server or
	// a self-hosted engine must not eat into it.
	NMTEngine string
	// NMTChars is how many source characters the run sent to that provider. It
	// is tracked separately from cost because the provider's free tier is
	// denominated in characters per calendar month, not in dollars: a run that
	// cost nothing still consumes the allowance the next one will be billed
	// against.
	NMTChars int
	// LLMProvider and LLMModel identify what consumed the tokens below. They
	// are the key the projection is calibrated on: how verbose a model is per
	// segment is a property of that model on that transport, and transfers from
	// one book to the next — while nothing about a book's characters predicts
	// it.
	LLMProvider string
	LLMModel    string
	// LLMSegments is how many segments the LLM pass carried, usable result or
	// not. It is the denominator that pairs with the token counters: counting
	// only the segments that came back improved would divide the tokens a
	// degraded batch consumed by the segments it did not deliver.
	LLMSegments int
	// The token counters the provider reported, split the way it bills them.
	// They were previously kept only in each run's report.json, which meant no
	// second run could learn from the first.
	LLMInputTokens         int
	LLMOutputTokens        int
	LLMCacheReadTokens     int
	LLMCacheCreationTokens int
}

// MeasuredLLM is what the ledger has observed for one (provider, model) pair.
// It holds sums, not ratios: the caller decides which ratio it needs, and a sum
// stays meaningful when a new one is added to it.
type MeasuredLLM struct {
	// Attempts is how many run attempts contributed.
	Attempts int
	// Segments is the total the LLM pass carried across them.
	Segments int

	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// OutputTokensPerSegment is the measured output verbosity, or 0 when nothing
// has been measured yet. It is the one ratio the cost model takes as an input:
// the input side is dominated by the prefix, which a run measures directly from
// the bible it loaded.
func (m MeasuredLLM) OutputTokensPerSegment() float64 {
	if m.Segments <= 0 {
		return 0
	}
	return float64(m.OutputTokens) / float64(m.Segments)
}

// Store is the exact-match translation memory and spend ledger used by the
// pipeline.
type Store interface {
	// Lookup returns the stored entry for a hash. found is false on a miss.
	Lookup(ctx context.Context, sourceHash string) (entry Entry, found bool, err error)
	// Save inserts or replaces an entry keyed by its source hash.
	Save(ctx context.Context, entry Entry) error
	// RecordRun persists a completed run's spend for budget accounting.
	RecordRun(ctx context.Context, spend RunSpend) error
	// SpendSince returns the total realized spend recorded at or after since.
	SpendSince(ctx context.Context, since time.Time) (float64, error)
	// CharsSince returns the NMT characters engine was sent at or after since,
	// which is how much of that provider's monthly free allowance is gone.
	CharsSince(ctx context.Context, since time.Time, engine string) (int, error)
	// MeasuredLLM returns what past attempts of this (provider, model) pair
	// consumed, so a projection can be calibrated on measurement instead of on
	// character heuristics.
	MeasuredLLM(ctx context.Context, provider, model string) (MeasuredLLM, error)
	// Close releases the underlying database handle.
	Close() error
}
