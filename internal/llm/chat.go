package llm

import (
	"context"
	"encoding/json"
)

// ChatMessage is a single turn in a chat-completion request.
// Role is one of "system", "user", or "assistant".
type ChatMessage struct {
	Role    string
	Content string
}

// ChatRequest is a provider-agnostic chat-completion request.
type ChatRequest struct {
	Model     string
	Messages  []ChatMessage
	MaxTokens int // 0 = provider default
	// JSONSchema, when set, requests a structured JSON response conforming to
	// the schema (Anthropic output_config / OpenAI response_format). This makes
	// the provider emit well-formed JSON regardless of content, avoiding the
	// escaping fragility of free-form JSON-in-prose.
	JSONSchema json.RawMessage
}

// ChatResponse is the assistant reply plus token accounting for the cost model.
// InputTokens counts only uncached prompt tokens; cache reads/writes are
// reported separately so callers can verify prompt caching and price reads at
// the ~0.1x cache rate.
type ChatResponse struct {
	Text                     string
	InputTokens              int // uncached prompt tokens
	OutputTokens             int
	CacheReadInputTokens     int // served from prompt cache (~0.1x input price)
	CacheCreationInputTokens int // written to prompt cache (~1.25x input price)
}

// ChatClient is the provider-agnostic chat-completion contract. The
// OpenAI-compatible adapter covers OpenAI, DeepSeek, and compatible hosts.
type ChatClient interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
