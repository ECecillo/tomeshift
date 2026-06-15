package tm

import (
	"context"
	"time"
)

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
	RunID         string
	BookID        string
	StartedAt     time.Time
	EndedAt       time.Time
	ActualCostUSD float64
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
	// Close releases the underlying database handle.
	Close() error
}
