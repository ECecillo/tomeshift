package narrative

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// The volume dimension added by RFC 0008 lives inside summaries_json rather
// than in a column, so pre-0008 rows need no DDL migration: they decode with an
// empty VolumeID and stay addressable. chapter_summaries is the new table —
// the map step's per-chapter output, kept out of the bible so it never reaches
// the cacheable prefix.
const schema = `
CREATE TABLE IF NOT EXISTS bibles (
  series_id       TEXT PRIMARY KEY,
  style_guide     TEXT NOT NULL,
  characters_json TEXT NOT NULL,
  address_json    TEXT NOT NULL,
  summaries_json  TEXT NOT NULL,
  version         INTEGER NOT NULL,
  updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chapter_summaries (
  series_id  TEXT NOT NULL,
  volume_id  TEXT NOT NULL,
  chapter_id TEXT NOT NULL,
  summary    TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (series_id, volume_id, chapter_id)
);`

// SQLiteStore is a series bible store backed by SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens or creates the narrative database at path.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("unable to open narrative database %q, err: %w", path, err)
	}

	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("unable to configure narrative database, err: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("unable to initialize narrative schema, err: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// Load retrieves the bible for seriesID. found is false on a miss.
func (s *SQLiteStore) Load(ctx context.Context, seriesID string) (Bible, bool, error) {
	if s.db == nil {
		return Bible{}, false, ErrNilDatabase
	}
	if seriesID == "" {
		return Bible{}, false, ErrEmptySeriesID
	}

	const query = `SELECT series_id, style_guide, characters_json, address_json, summaries_json, version
FROM bibles WHERE series_id = ?`

	var (
		bible         Bible
		charactersRaw string
		addressRaw    string
		summariesRaw  string
	)
	err := s.db.QueryRowContext(ctx, query, seriesID).Scan(
		&bible.SeriesID, &bible.StyleGuide, &charactersRaw, &addressRaw, &summariesRaw, &bible.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Bible{}, false, nil
	}
	if err != nil {
		return Bible{}, false, fmt.Errorf("unable to load narrative bible %q, err: %w", seriesID, err)
	}

	if err := decodeJSON(charactersRaw, &bible.Characters, "characters"); err != nil {
		return Bible{}, false, err
	}
	if err := decodeJSON(addressRaw, &bible.Address, "address"); err != nil {
		return Bible{}, false, err
	}
	if err := decodeJSON(summariesRaw, &bible.Summaries, "summaries"); err != nil {
		return Bible{}, false, err
	}

	return bible, true, nil
}

// Save inserts or replaces bible. Version identifies the content: a save that
// changes nothing keeps the stored version, any content change takes the next
// one. So `just bible` reporting v3 means the v3 content, not "saved three
// times".
func (s *SQLiteStore) Save(ctx context.Context, bible Bible) error {
	return s.SaveAll(ctx, []Bible{bible})
}

// SaveIfVersion conditionally saves bible against the version the caller
// loaded. It is the primitive used by load-modify-save operations that must
// not overwrite a concurrent writer.
func (s *SQLiteStore) SaveIfVersion(ctx context.Context, bible Bible) (bool, error) {
	if s.db == nil {
		return false, ErrNilDatabase
	}
	if bible.SeriesID == "" {
		return false, ErrEmptySeriesID
	}
	return s.saveCAS(ctx, bible, bible.Version > 0)
}

// SaveAll persists one complete bible generation in a single transaction.
// Version selection happens inside each UPSERT, where SQLite serializes the
// read of the current content and its replacement even across processes.
func (s *SQLiteStore) SaveAll(ctx context.Context, bibles []Bible) error {
	if s.db == nil {
		return ErrNilDatabase
	}
	if len(bibles) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("unable to begin narrative bible transaction, err: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const stmt = `INSERT INTO bibles
  (series_id, style_guide, characters_json, address_json, summaries_json, version, updated_at)
VALUES (?, ?, ?, ?, ?, 1, ?)
ON CONFLICT(series_id) DO UPDATE SET
  style_guide=excluded.style_guide,
  characters_json=excluded.characters_json,
  address_json=excluded.address_json,
  summaries_json=excluded.summaries_json,
  version=CASE WHEN
    bibles.style_guide=excluded.style_guide AND
    bibles.characters_json=excluded.characters_json AND
    bibles.address_json=excluded.address_json AND
    bibles.summaries_json=excluded.summaries_json
    THEN bibles.version ELSE bibles.version + 1 END,
  updated_at=excluded.updated_at;`

	for _, bible := range bibles {
		if bible.SeriesID == "" {
			return ErrEmptySeriesID
		}
		charactersRaw, err := encodeJSON(bible.Characters, "characters")
		if err != nil {
			return err
		}
		addressRaw, err := encodeJSON(bible.Address, "address")
		if err != nil {
			return err
		}
		summariesRaw, err := encodeJSON(bible.Summaries, "summaries")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, stmt, bible.SeriesID, bible.StyleGuide,
			string(charactersRaw), string(addressRaw), string(summariesRaw),
			time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("unable to save narrative bible %q, err: %w", bible.SeriesID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("unable to commit narrative bible generation, err: %w", err)
	}
	return nil
}

// PutChapterSummaries replaces the map step's complete snapshot for one
// (series, volume), including with an empty snapshot. They are deliberately
// not part of the bible: keeping them costs nothing, rendering them into the
// prefix costs 5.31 $ per translated volume at five volumes.
func (s *SQLiteStore) PutChapterSummaries(ctx context.Context, seriesID, volumeID string, summaries []Summary) error {
	if s.db == nil {
		return ErrNilDatabase
	}
	if seriesID == "" {
		return ErrEmptySeriesID
	}
	if volumeID == "" {
		return fmt.Errorf("unable to replace chapter summaries, err: %w", ErrInvalidBible)
	}
	for _, summary := range summaries {
		if summary.VolumeID != volumeID || summary.ChapterID == "" || summary.Summary == "" {
			return fmt.Errorf("unable to replace chapter summaries for %q, err: %w", volumeID, ErrInvalidBible)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("unable to begin chapter summary transaction %q, err: %w", seriesID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chapter_summaries WHERE series_id = ? AND volume_id = ?`, seriesID, volumeID); err != nil {
		return fmt.Errorf("unable to clear chapter summaries %q/%q, err: %w", seriesID, volumeID, err)
	}

	const stmt = `INSERT INTO chapter_summaries
  (series_id, volume_id, chapter_id, summary, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(series_id, volume_id, chapter_id) DO UPDATE SET
  summary=excluded.summary,
  updated_at=excluded.updated_at;`

	now := time.Now().UTC().Format(time.RFC3339)
	for _, summary := range summaries {
		if _, err := tx.ExecContext(ctx, stmt,
			seriesID, summary.VolumeID, summary.ChapterID, summary.Summary, now,
		); err != nil {
			return fmt.Errorf("unable to save chapter summary %q/%q, err: %w", summary.VolumeID, summary.ChapterID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("unable to commit chapter summaries %q, err: %w", seriesID, err)
	}
	return nil
}

// ChapterSummaries returns the stored per-chapter summaries in chapter order.
// An empty volumeID returns every volume's.
func (s *SQLiteStore) ChapterSummaries(ctx context.Context, seriesID, volumeID string) ([]Summary, error) {
	if s.db == nil {
		return nil, ErrNilDatabase
	}
	if seriesID == "" {
		return nil, ErrEmptySeriesID
	}

	const query = `SELECT volume_id, chapter_id, summary FROM chapter_summaries
WHERE series_id = ? AND (? = '' OR volume_id = ?)
ORDER BY volume_id, chapter_id`

	rows, err := s.db.QueryContext(ctx, query, seriesID, volumeID, volumeID)
	if err != nil {
		return nil, fmt.Errorf("unable to load chapter summaries %q, err: %w", seriesID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Summary
	for rows.Next() {
		var summary Summary
		if err := rows.Scan(&summary.VolumeID, &summary.ChapterID, &summary.Summary); err != nil {
			return nil, fmt.Errorf("unable to scan chapter summary %q, err: %w", seriesID, err)
		}
		out = append(out, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unable to read chapter summaries %q, err: %w", seriesID, err)
	}
	return out, nil
}

// AppendSummary appends a chapter summary in call order and bumps Version.
func (s *SQLiteStore) AppendSummary(ctx context.Context, seriesID string, summary Summary) error {
	if seriesID == "" {
		return ErrEmptySeriesID
	}

	return s.mutate(ctx, seriesID, func(bible *Bible) bool {
		bible.Summaries = append(bible.Summaries, summary)
		return true
	})
}

// SetAddress creates or changes an ordered address rule. Existing rules are
// unchanged when the form is unchanged; form changes append the provided
// history entries and bump Version.
func (s *SQLiteStore) SetAddress(ctx context.Context, seriesID string, rule AddressRule) error {
	if seriesID == "" {
		return ErrEmptySeriesID
	}

	return s.mutate(ctx, seriesID, func(bible *Bible) bool {
		for i := range bible.Address {
			if bible.Address[i].From != rule.From || bible.Address[i].To != rule.To {
				continue
			}
			if bible.Address[i].Form == rule.Form {
				return false
			}
			bible.Address[i].Form = rule.Form
			bible.Address[i].History = append(bible.Address[i].History, rule.History...)
			return true
		}
		bible.Address = append(bible.Address, rule)
		return true
	})
}

// mutate applies a load-modify-save operation with optimistic concurrency.
// A writer that loses the version race reloads and reapplies its mutation, so
// helpers cannot silently overwrite changes committed by another process.
func (s *SQLiteStore) mutate(ctx context.Context, seriesID string, change func(*Bible) bool) error {
	for {
		bible, found, err := s.Load(ctx, seriesID)
		if err != nil {
			return err
		}
		if !found {
			bible = Bible{SeriesID: seriesID}
		}
		if !change(&bible) {
			return nil
		}

		saved, err := s.saveCAS(ctx, bible, found)
		if err != nil {
			return err
		}
		if saved {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (s *SQLiteStore) saveCAS(ctx context.Context, bible Bible, found bool) (bool, error) {
	charactersRaw, err := encodeJSON(bible.Characters, "characters")
	if err != nil {
		return false, err
	}
	addressRaw, err := encodeJSON(bible.Address, "address")
	if err != nil {
		return false, err
	}
	summariesRaw, err := encodeJSON(bible.Summaries, "summaries")
	if err != nil {
		return false, err
	}

	var result sql.Result
	if found {
		const stmt = `UPDATE bibles SET style_guide = ?, characters_json = ?, address_json = ?,
summaries_json = ?, version = CASE WHEN
  style_guide = ? AND characters_json = ? AND address_json = ? AND summaries_json = ?
  THEN version ELSE version + 1 END, updated_at = ?
WHERE series_id = ? AND version = ?`
		result, err = s.db.ExecContext(ctx, stmt, bible.StyleGuide, string(charactersRaw),
			string(addressRaw), string(summariesRaw), bible.StyleGuide, string(charactersRaw),
			string(addressRaw), string(summariesRaw), time.Now().UTC().Format(time.RFC3339), bible.SeriesID, bible.Version)
	} else {
		const stmt = `INSERT INTO bibles
(series_id, style_guide, characters_json, address_json, summaries_json, version, updated_at)
VALUES (?, ?, ?, ?, ?, 1, ?) ON CONFLICT(series_id) DO NOTHING`
		result, err = s.db.ExecContext(ctx, stmt, bible.SeriesID, bible.StyleGuide,
			string(charactersRaw), string(addressRaw), string(summariesRaw),
			time.Now().UTC().Format(time.RFC3339))
	}
	if err != nil {
		return false, fmt.Errorf("unable to conditionally save narrative bible %q, err: %w", bible.SeriesID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("unable to inspect narrative bible save %q, err: %w", bible.SeriesID, err)
	}
	return rows == 1, nil
}

// Close releases the underlying database handle.
func (s *SQLiteStore) Close() error {
	if s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("unable to close narrative database, err: %w", err)
	}
	return nil
}

func encodeJSON(v any, label string) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("unable to encode narrative %s, err: %w", label, err)
	}
	return data, nil
}

func decodeJSON(data string, v any, label string) error {
	if err := json.Unmarshal([]byte(data), v); err != nil {
		return fmt.Errorf("unable to decode narrative %s, err: %w", label, err)
	}
	return nil
}

var _ Store = (*SQLiteStore)(nil)
