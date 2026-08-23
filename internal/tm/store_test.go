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

func TestSQLiteStore_MeasuredLLM_WeightsBySegment(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := t.Context()

	record := func(runID string, segments, out int) {
		require.NoError(t, store.RecordRun(ctx, RunSpend{
			RunID: runID, BookID: "b",
			StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
			LLMProvider: "claude-cli", LLMModel: "claude-sonnet-5",
			LLMSegments: segments, LLMOutputTokens: out,
			LLMInputTokens: 10, LLMCacheReadTokens: 20, LLMCacheCreationTokens: 30,
		}))
	}
	record("small", 20, 8000)    // 400 tokens/segment
	record("volume", 4000, 4000) // 1 token/segment

	measured, err := store.MeasuredLLM(ctx, "claude-cli", "claude-sonnet-5")
	require.NoError(t, err)
	require.Equal(t, 2, measured.Attempts)
	require.Equal(t, 4020, measured.Segments)
	require.Equal(t, 12000, measured.OutputTokens)
	require.Equal(t, 20, measured.InputTokens, "both rows' 10 input tokens")
	require.Equal(t, 40, measured.CacheReadTokens)
	require.Equal(t, 60, measured.CacheCreationTokens)

	// Weighted by segments, so the 4000-segment volume dominates: 12000/4020,
	// not the mean of 400 and 1. A smoke test must not outvote a book.
	require.InDelta(t, 12000.0/4020.0, measured.OutputTokensPerSegment(), 1e-9)
}

func TestSQLiteStore_MeasuredLLM_IgnoresWhatCannotCalibrate(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := t.Context()

	base := RunSpend{
		BookID: "b", StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
		LLMProvider: "claude-cli", LLMModel: "claude-sonnet-5",
	}
	usable := base
	usable.RunID, usable.LLMSegments, usable.LLMOutputTokens = "usable", 100, 20000

	// A run that died before its LLM pass: segments 0.
	crashed := base
	crashed.RunID = "crashed"

	// A transport that reports no usage (the HTTP post-editor): segments but no
	// tokens. Counting it would say the model emits 0 tokens per segment.
	unmeasured := base
	unmeasured.RunID, unmeasured.LLMSegments = "unmeasured", 500

	// Another model entirely: verbosity does not transfer across models.
	other := usable
	other.RunID, other.LLMModel = "other", "claude-haiku-4-5"

	for _, spend := range []RunSpend{usable, crashed, unmeasured, other} {
		require.NoError(t, store.RecordRun(ctx, spend))
	}

	measured, err := store.MeasuredLLM(ctx, "claude-cli", "claude-sonnet-5")
	require.NoError(t, err)
	require.Equal(t, 1, measured.Attempts)
	require.Equal(t, 100, measured.Segments)
	require.InDelta(t, 200.0, measured.OutputTokensPerSegment(), 1e-9)
}

func TestSQLiteStore_MeasuredLLM_NoSamplesIsZeroNotAnError(t *testing.T) {
	t.Parallel()

	measured, err := newTestStore(t).MeasuredLLM(t.Context(), "claude-cli", "claude-sonnet-5")
	require.NoError(t, err)
	require.Zero(t, measured.Attempts)
	// 0 is what tells the cost model to keep its heuristic.
	require.Zero(t, measured.OutputTokensPerSegment())
}

func TestNewSQLiteStore_AddsTheLLMColumnsToAnOlderLedger(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "older.sqlite")

	// run_attempts as it shipped, without the LLM columns. CREATE TABLE IF NOT
	// EXISTS would leave it exactly like this.
	older, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = older.Exec(`CREATE TABLE run_attempts (
  attempt_id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL, book_id TEXT NOT NULL,
  started_at TEXT NOT NULL, ended_at TEXT NOT NULL, actual_cost_usd REAL NOT NULL,
  nmt_engine TEXT NOT NULL DEFAULT '', nmt_chars INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = older.Exec(`INSERT INTO run_attempts
  (run_id, book_id, started_at, ended_at, actual_cost_usd, nmt_engine, nmt_chars, created_at)
VALUES ('old', 'b', '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z', 1.5, 'nmt-google', 1000, '2026-08-01T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, older.Close())

	store, err := NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// The pre-existing row survives, and a new one can carry the LLM counters.
	spend, err := store.SpendSince(t.Context(), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.InDelta(t, 1.5, spend, 0.0001)

	require.NoError(t, store.RecordRun(t.Context(), RunSpend{
		RunID: "new", BookID: "b", StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
		LLMProvider: "claude-cli", LLMModel: "m", LLMSegments: 10, LLMOutputTokens: 2000,
	}))
	measured, err := store.MeasuredLLM(t.Context(), "claude-cli", "m")
	require.NoError(t, err)
	require.InDelta(t, 200.0, measured.OutputTokensPerSegment(), 1e-9)

	// The old row contributes nothing rather than a zero: it predates the
	// columns, so it has no measurement to offer.
	require.Equal(t, 1, measured.Attempts)
}
