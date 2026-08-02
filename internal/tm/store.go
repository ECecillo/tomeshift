package tm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS segments (
  source_hash    TEXT PRIMARY KEY,
  source_text    TEXT NOT NULL,
  target_text    TEXT NOT NULL,
  source_lang    TEXT NOT NULL,
  target_lang    TEXT NOT NULL,
  engine         TEXT NOT NULL,
  quality_score  REAL,
  schema_version INTEGER NOT NULL DEFAULT 1,
  created_at     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_attempts (
  attempt_id      INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id          TEXT NOT NULL,
  book_id         TEXT NOT NULL,
  started_at      TEXT NOT NULL,
  ended_at        TEXT NOT NULL,
  actual_cost_usd REAL NOT NULL,
  nmt_engine      TEXT NOT NULL DEFAULT '',
  nmt_chars       INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_run_attempts_created_at ON run_attempts (created_at);`

// legacyRunColumns brings a pre-existing `runs` table up to the columns
// run_attempts expects before its rows are folded in. A duplicate-column error
// means the migration already ran, which is the expected outcome; anything else
// must not be swallowed.
var legacyRunColumns = []string{
	`ALTER TABLE runs ADD COLUMN nmt_chars INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE runs ADD COLUMN nmt_engine TEXT NOT NULL DEFAULT ''`,
}

// migrateLegacyRuns folds the old `runs` table into run_attempts and drops it.
//
// `runs` was keyed by run_id and upserted, so a resumed or re-run invocation
// replaced the previous attempt's spend instead of adding to it. That is the
// wrong shape for a ledger meant to stand for provider billing: an attempt that
// failed after paying for half a book is a fact, and the retry that follows is
// a second one. run_id identifies the run artifact; each execution is its own
// billing event.
//
// Existing rows carry over as one attempt each, which is the best reading
// available — the upsert already destroyed any earlier attempts under the same
// id, so nothing more can be recovered.
func migrateLegacyRuns(db *sql.DB) error {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unable to inspect tm schema, err: %w", err)
	}

	for _, stmt := range legacyRunColumns {
		if _, err := db.Exec(stmt); err != nil && !isDuplicateColumn(err) {
			return fmt.Errorf("unable to migrate legacy runs table, err: %w", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("unable to begin tm migration, err: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO run_attempts
  (run_id, book_id, started_at, ended_at, actual_cost_usd, nmt_engine, nmt_chars, created_at)
SELECT run_id, book_id, started_at, ended_at, actual_cost_usd, nmt_engine, nmt_chars, created_at FROM runs`); err != nil {
		return fmt.Errorf("unable to copy legacy runs, err: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE runs`); err != nil {
		return fmt.Errorf("unable to drop legacy runs table, err: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("unable to commit tm migration, err: %w", err)
	}
	return nil
}

// isDuplicateColumn recognises the one migration failure that means "already
// applied". The driver reports it as a message rather than a typed error, so
// this has to match on text.
func isDuplicateColumn(err error) bool {
	return strings.Contains(err.Error(), "duplicate column name")
}

// SQLiteStore is a translation memory backed by a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (creating if needed) the SQLite database at path and
// ensures the schema exists. Use ":memory:" for an ephemeral store.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("unable to open tm database %q, err: %w", path, err)
	}

	// SQLite handles a single writer; keep the pool small to avoid
	// "database is locked" and to keep ":memory:" stores coherent.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("unable to initialize tm schema, err: %w", err)
	}
	if err := migrateLegacyRuns(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

// Lookup returns the entry for a source hash, or found=false on a miss.
func (s *SQLiteStore) Lookup(ctx context.Context, sourceHash string) (Entry, bool, error) {
	if s.db == nil {
		return Entry{}, false, ErrNilDatabase
	}
	if sourceHash == "" {
		return Entry{}, false, ErrEmptyHash
	}

	const query = `SELECT source_hash, source_text, target_text, source_lang, target_lang, engine, quality_score
FROM segments WHERE source_hash = ?`

	var (
		entry   Entry
		quality sql.NullFloat64
	)
	err := s.db.QueryRowContext(ctx, query, sourceHash).Scan(
		&entry.SourceHash, &entry.SourceText, &entry.TargetText,
		&entry.SourceLang, &entry.TargetLang, &entry.Engine, &quality,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("unable to look up tm entry %q, err: %w", sourceHash, err)
	}

	if quality.Valid {
		entry.QualityScore = quality.Float64
	}

	return entry, true, nil
}

// Save inserts or replaces an entry keyed by its source hash.
func (s *SQLiteStore) Save(ctx context.Context, entry Entry) error {
	if s.db == nil {
		return ErrNilDatabase
	}
	if entry.SourceHash == "" {
		return ErrEmptyHash
	}

	const stmt = `INSERT INTO segments
  (source_hash, source_text, target_text, source_lang, target_lang, engine, quality_score, schema_version, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_hash) DO UPDATE SET
  source_text=excluded.source_text,
  target_text=excluded.target_text,
  source_lang=excluded.source_lang,
  target_lang=excluded.target_lang,
  engine=excluded.engine,
  quality_score=excluded.quality_score,
  schema_version=excluded.schema_version,
  created_at=excluded.created_at;`

	if _, err := s.db.ExecContext(ctx, stmt,
		entry.SourceHash, entry.SourceText, entry.TargetText,
		entry.SourceLang, entry.TargetLang, entry.Engine, entry.QualityScore,
		schemaVersion, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("unable to save tm entry %q, err: %w", entry.SourceHash, err)
	}

	return nil
}

// RecordRun appends one execution's realized spend.
//
// It appends rather than upserts. A run id names an artifact, not a payment:
// re-running or resuming under the same id is a second execution that the
// provider charged separately, so replacing the earlier row would erase spend
// that really happened.
func (s *SQLiteStore) RecordRun(ctx context.Context, spend RunSpend) error {
	if s.db == nil {
		return ErrNilDatabase
	}

	const stmt = `INSERT INTO run_attempts
  (run_id, book_id, started_at, ended_at, actual_cost_usd, nmt_engine, nmt_chars, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, stmt,
		spend.RunID, spend.BookID,
		spend.StartedAt.UTC().Format(time.RFC3339),
		spend.EndedAt.UTC().Format(time.RFC3339),
		spend.ActualCostUSD, spend.NMTEngine, spend.NMTChars, now,
	); err != nil {
		return fmt.Errorf("unable to record run %q, err: %w", spend.RunID, err)
	}
	return nil
}

// SpendSince returns the sum of realized run costs recorded at or after since.
func (s *SQLiteStore) SpendSince(ctx context.Context, since time.Time) (float64, error) {
	if s.db == nil {
		return 0, ErrNilDatabase
	}

	const query = `SELECT COALESCE(SUM(actual_cost_usd), 0) FROM run_attempts WHERE created_at >= ?`

	var total float64
	if err := s.db.QueryRowContext(ctx, query, since.UTC().Format(time.RFC3339)).Scan(&total); err != nil {
		return 0, fmt.Errorf("unable to sum run spend, err: %w", err)
	}
	return total, nil
}

// CharsSince returns the NMT characters engine was sent at or after since. It
// is how the pipeline knows how much of that provider's monthly free allowance
// is already gone: the allowance is denominated in characters, and a run inside
// it costs nothing while still consuming it.
//
// The filter on engine is load-bearing. The default provider is a local dev
// server and the ledger is shared, so summing every run would let free local
// translation burn a paid provider's allowance. Note the scope is the engine,
// not the billing project: Google's allowance is per GCP project, and an API
// key does not name one, so switching projects still shares a counter.
func (s *SQLiteStore) CharsSince(ctx context.Context, since time.Time, engine string) (int, error) {
	if s.db == nil {
		return 0, ErrNilDatabase
	}

	const query = `SELECT COALESCE(SUM(nmt_chars), 0) FROM run_attempts WHERE created_at >= ? AND nmt_engine = ?`

	var total int
	if err := s.db.QueryRowContext(ctx, query, since.UTC().Format(time.RFC3339), engine).Scan(&total); err != nil {
		return 0, fmt.Errorf("unable to sum run nmt chars, err: %w", err)
	}
	return total, nil
}

// Close releases the underlying database handle.
func (s *SQLiteStore) Close() error {
	if s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("unable to close tm database, err: %w", err)
	}
	return nil
}

var _ Store = (*SQLiteStore)(nil)
