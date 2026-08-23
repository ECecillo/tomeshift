package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

// TestOpenAICompatible_SendsLowercaseWireFields inspects the raw request body
// rather than a decoded struct.
//
// Go's decoder matches field names case-insensitively, so a Go test stub
// accepts "Role"/"Content" exactly as happily as "role"/"content" — which is
// how this adapter shipped for months emitting capitalised keys that a strict
// server rejects outright. DeepSeek's parser answered 400 "missing field
// `role`" on every call. Asserting on the bytes is the only check that sees it.
func TestOpenAICompatible_SendsLowercaseWireFields(t *testing.T) {
	t.Parallel()

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)

	_, err := llm.NewOpenAICompatibleClient(srv.URL, "k").Chat(context.Background(), llm.ChatRequest{
		Model: "m",
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "usr"},
		},
	})
	require.NoError(t, err)

	require.Contains(t, string(body), `"role":"system"`)
	require.Contains(t, string(body), `"content":"sys"`)
	require.NotContains(t, string(body), `"Role"`)
	require.NotContains(t, string(body), `"Content"`)
}

// TestOpenAICompatible_SchemaModePerProvider pins the one place the shared wire
// contract diverges. DeepSeek answers 400 "This response_format type is
// unavailable now" to the json_schema form OpenAI accepts, and a json_schema
// key sent alongside json_object is rejected too — so it must be absent, not
// empty.
func TestOpenAICompatible_SchemaModePerProvider(t *testing.T) {
	t.Parallel()

	capture := func(mode llm.SchemaMode) string {
		var body []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		}))
		defer srv.Close()

		client := llm.NewOpenAICompatibleClient(srv.URL, "k")
		client.SchemaMode = mode
		_, err := client.Chat(context.Background(), llm.ChatRequest{
			Model:      "m",
			Messages:   []llm.ChatMessage{{Role: "user", Content: "u"}},
			JSONSchema: json.RawMessage(`{"type":"object"}`),
		})
		require.NoError(t, err)
		return string(body)
	}

	schema := capture(llm.SchemaModeJSONSchema)
	require.Contains(t, schema, `"type":"json_schema"`)
	require.Contains(t, schema, `"json_schema"`)

	object := capture(llm.SchemaModeJSONObject)
	require.Contains(t, object, `"type":"json_object"`)
	require.NotContains(t, object, `"json_schema"`)
}

// The factory is what pairs a provider with its wire capability; getting it
// wrong there is invisible until a real request 400s.
func TestNewChatClient_DeepSeekAsksForJSONObjectOnly(t *testing.T) {
	t.Parallel()

	deepseek, ok := llm.NewChatClient(llm.ChatClientConfig{
		Provider: llm.ChatProviderDeepSeek, BaseURL: "https://api.deepseek.com", APIKey: "k",
	}).(*llm.OpenAICompatibleClient)
	require.True(t, ok)
	require.Equal(t, llm.SchemaModeJSONObject, deepseek.SchemaMode)

	openai, ok := llm.NewChatClient(llm.ChatClientConfig{
		Provider: llm.ChatProviderOpenAI, BaseURL: "https://api.openai.com/v1", APIKey: "k",
	}).(*llm.OpenAICompatibleClient)
	require.True(t, ok)
	require.Equal(t, llm.SchemaModeJSONSchema, openai.SchemaMode)
}
