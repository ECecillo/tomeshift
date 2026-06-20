package llm_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/llm"
)

type mockChatClient struct {
	calls int
	reply llm.ChatResponse
}

func (m *mockChatClient) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	m.calls++
	return m.reply, nil
}

func TestSQLiteCache_SetAndGet(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "llm_cache.sqlite")
	cache, err := llm.NewSQLiteCache(path)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	req := llm.ChatRequest{
		Model: "test-model",
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	// Not in cache initially
	_, hit, err := cache.Get(t.Context(), req)
	require.NoError(t, err)
	require.False(t, hit)

	// Save to cache
	resp := llm.ChatResponse{
		Text:         "Hi there",
		InputTokens:  10,
		OutputTokens: 20,
	}
	err = cache.Set(t.Context(), req, resp)
	require.NoError(t, err)

	// Fetch from cache
	cached, hit, err := cache.Get(t.Context(), req)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, resp.Text, cached.Text)
	require.Equal(t, resp.InputTokens, cached.InputTokens)
	require.Equal(t, resp.OutputTokens, cached.OutputTokens)
}

func TestCachedChatClient(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "llm_cache.sqlite")
	cache, err := llm.NewSQLiteCache(path)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	mock := &mockChatClient{
		reply: llm.ChatResponse{Text: "mocked reply", InputTokens: 5, OutputTokens: 10},
	}

	client := &llm.CachedChatClient{
		Inner: mock,
		Cache: cache,
	}

	req := llm.ChatRequest{
		Model: "model",
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "prompt"},
		},
	}

	// First call should hit the mock
	resp1, err := client.Chat(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, "mocked reply", resp1.Text)
	require.Equal(t, 1, mock.calls)

	// Second call should hit the cache, not the mock
	resp2, err := client.Chat(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, "mocked reply", resp2.Text)
	require.Equal(t, 1, mock.calls) // calls should still be 1
}
