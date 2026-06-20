package llm

import "context"

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
}

// ChatResponse is the assistant reply plus token accounting for the cost model.
type ChatResponse struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// ChatClient is the provider-agnostic chat-completion contract. The
// OpenAI-compatible adapter covers OpenAI, DeepSeek, and compatible hosts.
type ChatClient interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
