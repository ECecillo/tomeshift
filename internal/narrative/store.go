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

const schema = `
CREATE TABLE IF NOT EXISTS bibles (
  series_id       TEXT PRIMARY KEY,
  style_guide     TEXT NOT NULL,
  characters_json TEXT NOT NULL,
  address_json    TEXT NOT NULL,
  summaries_json  TEXT NOT NULL,
  version         INTEGER NOT NULL,
  updated_at      TEXT NOT NULL
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

// Save inserts or replaces bible and bumps Version monotonically.
func (s *SQLiteStore) Save(ctx context.Context, bible Bible) error {
	if s.db == nil {
		return ErrNilDatabase
	}
	if bible.SeriesID == "" {
		return ErrEmptySeriesID
	}

	version, err := s.nextVersion(ctx, bible.SeriesID, bible.Version)
	if err != nil {
		return err
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

	const stmt = `INSERT INTO bibles
  (series_id, style_guide, characters_json, address_json, summaries_json, version, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(series_id) DO UPDATE SET
  style_guide=excluded.style_guide,
  characters_json=excluded.characters_json,
  address_json=excluded.address_json,
  summaries_json=excluded.summaries_json,
  version=excluded.version,
  updated_at=excluded.updated_at;`

	if _, err := s.db.ExecContext(ctx, stmt,
		bible.SeriesID,
		bible.StyleGuide,
		string(charactersRaw),
		string(addressRaw),
		string(summariesRaw),
		version,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("unable to save narrative bible %q, err: %w", bible.SeriesID, err)
	}

	return nil
}

// AppendSummary appends a chapter summary in call order and bumps Version.
func (s *SQLiteStore) AppendSummary(ctx context.Context, seriesID string, summary ChapterSummary) error {
	if seriesID == "" {
		return ErrEmptySeriesID
	}

	bible, found, err := s.Load(ctx, seriesID)
	if err != nil {
		return err
	}
	if !found {
		bible = Bible{SeriesID: seriesID}
	}

	bible.Summaries = append(bible.Summaries, summary)
	return s.Save(ctx, bible)
}

// SetAddress creates or changes an ordered address rule. Existing rules are
// unchanged when the form is unchanged; form changes append the provided
// history entries and bump Version.
func (s *SQLiteStore) SetAddress(ctx context.Context, seriesID string, rule AddressRule) error {
	if seriesID == "" {
		return ErrEmptySeriesID
	}

	bible, found, err := s.Load(ctx, seriesID)
	if err != nil {
		return err
	}
	if !found {
		bible = Bible{SeriesID: seriesID}
	}

	for i := range bible.Address {
		if bible.Address[i].From != rule.From || bible.Address[i].To != rule.To {
			continue
		}
		if bible.Address[i].Form == rule.Form {
			return nil
		}
		bible.Address[i].Form = rule.Form
		bible.Address[i].History = append(bible.Address[i].History, rule.History...)
		return s.Save(ctx, bible)
	}

	bible.Address = append(bible.Address, rule)
	return s.Save(ctx, bible)
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

func (s *SQLiteStore) nextVersion(ctx context.Context, seriesID string, proposed int) (int, error) {
	const query = `SELECT version FROM bibles WHERE series_id = ?`

	var current int
	err := s.db.QueryRowContext(ctx, query, seriesID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return proposed + 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("unable to read narrative bible version %q, err: %w", seriesID, err)
	}
	if current > proposed {
		return current + 1, nil
	}
	return proposed + 1, nil
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
