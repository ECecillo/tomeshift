package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// ChatMessage is a single turn in a chat-completion request.
// Role is one of "system", "user", or "assistant".
// The tags are load-bearing on the wire, not decoration. Without them Go
// marshals "Role"/"Content", which Go itself then decodes again case
// -insensitively — so a Go test stub accepts what a strict server rejects.
// DeepSeek's parser is strict: it answered 400 "missing field `role`" for every
// request until these existed. The Anthropic adapter never had the bug because
// it maps onto its own tagged struct.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

// Stop reasons a provider can report. Anything other than StopReasonEndTurn
// means the reply is not the complete answer that was asked for.
const (
	StopReasonEndTurn   = "end_turn"
	StopReasonMaxTokens = "max_tokens"
	StopReasonRefusal   = "refusal"
)

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
	// StopReason is why the model stopped generating, when the provider says.
	// Empty means the provider does not report one. A refusal and a truncation
	// both arrive as a successful HTTP response with unusable content, so
	// without this they are indistinguishable from a malformed reply.
	StopReason string
	// StopDetails carries the provider's extra qualification of StopReason
	// (Anthropic's stop_details), verbatim.
	StopDetails map[string]any
}

// StopDetailsSummary renders StopReason and StopDetails for a log line or an
// error message.
func (r ChatResponse) StopDetailsSummary() string {
	if len(r.StopDetails) == 0 {
		return fmt.Sprintf("stop_reason=%q", r.StopReason)
	}
	return fmt.Sprintf("stop_reason=%q stop_details=%v", r.StopReason, r.StopDetails)
}

// ChatClient is the provider-agnostic chat-completion contract. The
// OpenAI-compatible adapter covers OpenAI, DeepSeek, and compatible hosts.
type ChatClient interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
