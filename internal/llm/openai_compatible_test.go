package llm_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/llm/openaitest"
)

func TestOpenAICompatible_Chat(t *testing.T) {
	t.Parallel()

	srv := openaitest.NewServer(func(req openaitest.Request) string {
		require.Equal(t, "/v1/chat/completions", req.Path)
		require.Equal(t, "deepseek-chat", req.Model)
		require.Equal(t, 200, req.MaxTokens)
		require.Len(t, req.Messages, 2)
		return "Bonjour."
	})
	t.Cleanup(srv.Close)

	client := llm.NewOpenAICompatibleClient(srv.URL+"/v1", "")
	resp, err := client.Chat(t.Context(), llm.ChatRequest{
		Model: "deepseek-chat",
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "Translate literary prose."},
			{Role: "user", Content: "Hello."},
		},
		MaxTokens: 200,
	})

	require.NoError(t, err)
	require.Equal(t, "Bonjour.", resp.Text)
	require.Equal(t, 12, resp.InputTokens)
	require.Equal(t, 7, resp.OutputTokens)
}

func TestOpenAICompatible_SendsBearer(t *testing.T) {
	t.Parallel()

	srv := openaitest.NewServer(func(req openaitest.Request) string {
		require.Equal(t, "Bearer k", req.Authorization)
		return "ok"
	})
	t.Cleanup(srv.Close)

	client := llm.NewOpenAICompatibleClient(srv.URL+"/v1", "k")
	_, err := client.Chat(t.Context(), llm.ChatRequest{
		Model:    "gpt-4.1-mini",
		Messages: []llm.ChatMessage{{Role: "user", Content: "x"}},
	})
	require.NoError(t, err)
}

func TestOpenAICompatible_EmptyBaseURL(t *testing.T) {
	t.Parallel()

	client := llm.NewOpenAICompatibleClient("", "")
	_, err := client.Chat(t.Context(), llm.ChatRequest{})
	require.ErrorIs(t, err, llm.ErrEmptyBaseURL)
}

func TestOpenAICompatible_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	client := llm.NewOpenAICompatibleClient(srv.URL, "")
	_, err := client.Chat(t.Context(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.ChatMessage{{Role: "user", Content: "x"}},
	})
	require.ErrorIs(t, err, llm.ErrProviderStatus)
}

func TestOpenAICompatible_EmptyChoices(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	t.Cleanup(srv.Close)

	client := llm.NewOpenAICompatibleClient(srv.URL, "")
	_, err := client.Chat(t.Context(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.ChatMessage{{Role: "user", Content: "x"}},
	})
	require.True(t, errors.Is(err, llm.ErrEmptyResponse))
}
