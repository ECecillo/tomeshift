package llm_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/llm"
)

func TestAnthropicClient_ChatSendsHeadersAndCacheControl(t *testing.T) {
	t.Parallel()

	var gotKey, gotVersion string
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    []struct {
			Type         string `json:"type"`
			Text         string `json:"text"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Bonjour"}],` +
			`"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	c := llm.NewAnthropicClient(srv.URL, "secret-key")
	resp, err := c.Chat(t.Context(), llm.ChatRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 1000,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "STYLE"},
			{Role: "user", Content: "translate"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "Bonjour", resp.Text)
	// Uncached input reported separately from cache read/creation.
	require.Equal(t, 10, resp.InputTokens)
	require.Equal(t, 3, resp.OutputTokens)
	require.Equal(t, 5, resp.CacheReadInputTokens)
	require.Equal(t, 2, resp.CacheCreationInputTokens)

	require.Equal(t, "secret-key", gotKey)
	require.NotEmpty(t, gotVersion, "anthropic-version header must be set")
	// system folded out of messages, with a prompt-cache breakpoint.
	require.Len(t, body.System, 1)
	require.Equal(t, "STYLE", body.System[0].Text)
	require.NotNil(t, body.System[0].CacheControl)
	require.Equal(t, "ephemeral", body.System[0].CacheControl.Type)
	require.Len(t, body.Messages, 1)
	require.Equal(t, "user", body.Messages[0].Role)
}

func TestAnthropicClient_EmptyResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[],"usage":{}}`))
	}))
	t.Cleanup(srv.Close)

	_, err := llm.NewAnthropicClient(srv.URL, "k").Chat(t.Context(),
		llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "x"}}})
	require.ErrorIs(t, err, llm.ErrEmptyResponse)
}

func TestAnthropicClient_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := llm.NewAnthropicClient(srv.URL, "k").Chat(t.Context(),
		llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "x"}}})
	require.ErrorIs(t, err, llm.ErrProviderStatus)
}

func TestNewChatClient_Anthropic(t *testing.T) {
	t.Parallel()

	_, ok := llm.NewChatClient(llm.ChatProviderAnthropic, "", "k").(*llm.AnthropicClient)
	require.True(t, ok, "provider 'anthropic' must build an AnthropicClient")
}

func TestNewAnthropicClient_DefaultBaseURL(t *testing.T) {
	t.Parallel()

	c := llm.NewAnthropicClient("", "k")
	require.Equal(t, "https://api.anthropic.com", c.BaseURL)
}
