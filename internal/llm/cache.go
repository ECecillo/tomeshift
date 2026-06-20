package llm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// SQLiteCache is a SQLite-backed cache for LLM requests.
type SQLiteCache struct {
	db *sql.DB
}

// NewSQLiteCache opens or creates the LLM cache database at path.
func NewSQLiteCache(path string) (*SQLiteCache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("unable to create cache directory, err: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("unable to open llm cache db, err: %w", err)
	}
	// SQLite handles a single writer; keep the pool small to avoid lock errors.
	db.SetMaxOpenConns(1)

	// Create table if not exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS llm_cache (
			hash TEXT PRIMARY KEY,
			response_text TEXT,
			input_tokens INTEGER,
			output_tokens INTEGER
		)
	`)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("unable to init llm cache schema, err: %w", err)
	}

	return &SQLiteCache{db: db}, nil
}

// Close closes the underlying SQLite database.
func (c *SQLiteCache) Close() error {
	return c.db.Close()
}

func hashRequest(req ChatRequest) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// Get retrieves a cached response if it exists for the given request.
func (c *SQLiteCache) Get(ctx context.Context, req ChatRequest) (ChatResponse, bool, error) {
	hash, err := hashRequest(req)
	if err != nil {
		return ChatResponse{}, false, fmt.Errorf("unable to hash request, err: %w", err)
	}

	var resp ChatResponse
	err = c.db.QueryRowContext(ctx, "SELECT response_text, input_tokens, output_tokens FROM llm_cache WHERE hash = ?", hash).Scan(
		&resp.Text,
		&resp.InputTokens,
		&resp.OutputTokens,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatResponse{}, false, nil
	}
	if err != nil {
		return ChatResponse{}, false, fmt.Errorf("unable to lookup cache, err: %w", err)
	}
	return resp, true, nil
}

// Set stores a request-response pair in the cache.
func (c *SQLiteCache) Set(ctx context.Context, req ChatRequest, resp ChatResponse) error {
	hash, err := hashRequest(req)
	if err != nil {
		return fmt.Errorf("unable to hash request, err: %w", err)
	}

	_, err = c.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO llm_cache (hash, response_text, input_tokens, output_tokens)
		VALUES (?, ?, ?, ?)
	`, hash, resp.Text, resp.InputTokens, resp.OutputTokens)
	if err != nil {
		return fmt.Errorf("unable to save to cache, err: %w", err)
	}
	return nil
}

// CachedChatClient wraps a ChatClient with a SQLiteCache.
type CachedChatClient struct {
	Inner ChatClient
	Cache *SQLiteCache
}

// Chat checks the cache first, and delegates to Inner if no cache hit occurs.
func (c *CachedChatClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if c.Cache != nil {
		cachedResp, hit, err := c.Cache.Get(ctx, req)
		if err == nil && hit {
			return cachedResp, nil
		}
	}

	resp, err := c.Inner.Chat(ctx, req)
	if err != nil {
		return ChatResponse{}, err
	}

	if c.Cache != nil {
		_ = c.Cache.Set(ctx, req, resp) // Best effort, ignore errors
	}

	return resp, nil
}

var _ ChatClient = (*CachedChatClient)(nil)
