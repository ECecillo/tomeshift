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
	// Close releases the underlying database handle.
	Close() error
}
