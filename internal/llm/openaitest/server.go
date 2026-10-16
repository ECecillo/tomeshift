// Package openaitest provides a deterministic in-process OpenAI-compatible
// chat endpoint for tests. It is imported only by test code and never linked
// into the production binary.
package openaitest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/ECecillo/tomeshift/internal/llm"
)

type requestBody struct {
	Model     string            `json:"model"`
	Messages  []llm.ChatMessage `json:"messages"`
	MaxTokens int               `json:"max_tokens"`
}

type responseBody struct {
	Choices []choice `json:"choices"`
	Usage   usage    `json:"usage"`
}

type choice struct {
	Message llm.ChatMessage `json:"message"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ReplyFunc maps the decoded OpenAI-compatible chat request to assistant text.
type ReplyFunc func(req Request) string

// Request exposes the decoded request and headers seen by the mock server.
type Request struct {
	Model         string
	Messages      []llm.ChatMessage
	MaxTokens     int
	Authorization string
	Path          string
}

// NewServer starts an httptest.Server speaking /v1/chat/completions. If reply
// is nil, the server returns the user's latest message unchanged.
func NewServer(reply ReplyFunc) *httptest.Server {
	if reply == nil {
		reply = func(req Request) string {
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == "user" {
					return req.Messages[i].Content
				}
			}
			return "ok"
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}

		var req requestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		got := Request{
			Model:         req.Model,
			Messages:      req.Messages,
			MaxTokens:     req.MaxTokens,
			Authorization: r.Header.Get("Authorization"),
			Path:          r.URL.Path,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responseBody{
			Choices: []choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: reply(got)},
			}},
			Usage: usage{PromptTokens: 12, CompletionTokens: 7},
		})
	})

	return httptest.NewServer(handler)
}
