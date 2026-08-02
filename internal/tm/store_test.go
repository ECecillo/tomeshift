package tm

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tm.sqlite")
	store, err := NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	return store
}

func TestSQLiteStore_SaveAndLookup(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := t.Context()

	entry := Entry{
		SourceHash:   "abc123",
		SourceText:   "Hello world",
		TargetText:   "Bonjour le monde",
		SourceLang:   "en",
		TargetLang:   "fr",
		Engine:       "nmt-http",
		QualityScore: 0.9,
	}
	require.NoError(t, store.Save(ctx, entry))

	got, found, err := store.Lookup(ctx, "abc123")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, entry, got)
}

func TestSQLiteStore_LookupMiss(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)

	_, found, err := store.Lookup(t.Context(), "does-not-exist")
	require.NoError(t, err)
	require.False(t, found)
}

func TestSQLiteStore_SaveUpserts(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := t.Context()

	base := Entry{SourceHash: "h1", SourceText: "x", TargetText: "v1", SourceLang: "en", TargetLang: "fr", Engine: "nmt-http"}
	require.NoError(t, store.Save(ctx, base))

	updated := base
	updated.TargetText = "v2"
	require.NoError(t, store.Save(ctx, updated))

	got, found, err := store.Lookup(ctx, "h1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "v2", got.TargetText)
}

func TestSQLiteStore_RecordRunAndSpendSince(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := t.Context()

	now := time.Now().UTC()
	require.NoError(t, store.RecordRun(ctx, RunSpend{
		RunID: "r1", BookID: "b", StartedAt: now, EndedAt: now, ActualCostUSD: 1.50,
	}))
	require.NoError(t, store.RecordRun(ctx, RunSpend{
		RunID: "r2", BookID: "b", StartedAt: now, EndedAt: now, ActualCostUSD: 2.25,
	}))

	total, err := store.SpendSince(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.InDelta(t, 3.75, total, 0.0001)

	future, err := store.SpendSince(ctx, now.Add(time.Hour))
	require.NoError(t, err)
	require.InDelta(t, 0.0, future, 0.0001)
}

func TestSQLiteStore_CharsSince(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	// A run inside the provider's free tier costs nothing yet still consumes
	// the monthly character allowance the next run is billed against, so the
	// two are tracked independently.
	require.NoError(t, store.RecordRun(ctx, RunSpend{
		RunID: "r1", BookID: "b", StartedAt: now, EndedAt: now,
		ActualCostUSD: 0, NMTEngine: "nmt-google", NMTChars: 472_509,
	}))
	require.NoError(t, store.RecordRun(ctx, RunSpend{
		RunID: "r2", BookID: "b", StartedAt: now, EndedAt: now,
		ActualCostUSD: 0, NMTEngine: "nmt-google", NMTChars: 414_044,
	}))
	// A run against the bundled dev server shares this ledger and must not
	// burn Google's allowance.
	require.NoError(t, store.RecordRun(ctx, RunSpend{
		RunID: "r3", BookID: "b", StartedAt: now, EndedAt: now,
		ActualCostUSD: 0, NMTEngine: "nmt-http", NMTChars: 1_000_000,
	}))

	total, err := store.CharsSince(ctx, now.Add(-time.Hour), "nmt-google")
	require.NoError(t, err)
	require.Equal(t, 886_553, total)

	local, err := store.CharsSince(ctx, now.Add(-time.Hour), "nmt-http")
	require.NoError(t, err)
	require.Equal(t, 1_000_000, local)

	spend, err := store.SpendSince(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.InDelta(t, 0.0, spend, 0.0001)

	future, err := store.CharsSince(ctx, now.Add(time.Hour), "nmt-google")
	require.NoError(t, err)
	require.Equal(t, 0, future)
}

func TestSQLiteStore_RecordRunAppendsEachAttempt(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	// A run id names an artifact, not a payment. An attempt that failed after
	// drafting half a book was billed for that half, and the --resume that
	// follows is a second, separately billed execution. The ledger used to
	// upsert on run_id and so erased the first.
	require.NoError(t, store.RecordRun(ctx, RunSpend{
		RunID: "r1", BookID: "b", StartedAt: now, EndedAt: now,
		ActualCostUSD: 1.0, NMTEngine: "nmt-google", NMTChars: 100,
	}))
	require.NoError(t, store.RecordRun(ctx, RunSpend{
		RunID: "r1", BookID: "b", StartedAt: now, EndedAt: now,
		ActualCostUSD: 5.0, NMTEngine: "nmt-google", NMTChars: 40,
	}))

	spend, err := store.SpendSince(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.InDelta(t, 6.0, spend, 0.0001, "both attempts were paid for")

	chars, err := store.CharsSince(ctx, now.Add(-time.Hour), "nmt-google")
	require.NoError(t, err)
	require.Equal(t, 140, chars)
}

func TestNewSQLiteStore_FoldsALegacyRunsTable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "legacy.sqlite")

	// A ledger written before run_attempts existed, and before nmt_chars and
	// nmt_engine were added to it.
	legacy, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = legacy.Exec(`CREATE TABLE runs (
  run_id TEXT PRIMARY KEY, book_id TEXT NOT NULL, started_at TEXT NOT NULL,
  ended_at TEXT NOT NULL, actual_cost_usd REAL NOT NULL, created_at TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = legacy.Exec(`INSERT INTO runs VALUES ('old', 'b', '2026-08-01T00:00:00Z',
  '2026-08-01T00:00:00Z', 2.5, '2026-08-01T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())

	store, err := NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// The spend carries over, and the old table is gone rather than left to
	// diverge from the one being written.
	spend, err := store.SpendSince(t.Context(), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.InDelta(t, 2.5, spend, 0.0001)

	var name string
	err = store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&name)
	require.ErrorIs(t, err, sql.ErrNoRows)

	// Opening again must be a no-op, not a second copy.
	reopened, err := NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	spend, err = reopened.SpendSince(t.Context(), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.InDelta(t, 2.5, spend, 0.0001)
}

func TestSQLiteStore_EmptyHash(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := t.Context()

	require.ErrorIs(t, store.Save(ctx, Entry{SourceHash: ""}), ErrEmptyHash)

	_, _, err := store.Lookup(ctx, "")
	require.ErrorIs(t, err, ErrEmptyHash)
}
