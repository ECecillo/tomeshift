package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Chat provider identifiers accepted by NewChatClient.
const (
	ChatProviderOpenAI           = "openai"
	ChatProviderDeepSeek         = "deepseek"
	ChatProviderOpenAICompatible = "openai-compatible"
)

type openAIChatRequest struct {
	Model          string             `json:"model"`
	Messages       []ChatMessage      `json:"messages"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	ResponseFormat *openAIResponseFmt `json:"response_format,omitempty"`
}

type openAIResponseFmt struct {
	Type string `json:"type"` // "json_schema" | "json_object"
	// Omitted in json_object mode: a host that rejects the json_schema form
	// also rejects an empty one alongside it.
	JSONSchema *openAISchemaDef `json:"json_schema,omitempty"`
}

type openAISchemaDef struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// OpenAICompatibleClient calls the OpenAI chat-completions wire contract. It
// also supports DeepSeek and compatible hosts by changing only BaseURL.
type OpenAICompatibleClient struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
	// SchemaMode is how a JSONSchema request is expressed on the wire. The
	// contract is shared but its structured-output corner is not: DeepSeek
	// answers 400 "This response_format type is unavailable now" to the
	// json_schema form OpenAI accepts, so the capability travels with the
	// client instead of being assumed from the endpoint's shape.
	SchemaMode SchemaMode
}

// SchemaMode names how far a host's structured-output support goes.
type SchemaMode int

const (
	// SchemaModeJSONSchema sends the full schema and gets it enforced.
	SchemaModeJSONSchema SchemaMode = iota
	// SchemaModeJSONObject asks only for well-formed JSON. The schema itself
	// then rides in the prompt, and the literary decoder's lenient parse plus
	// its halving retry absorb the difference.
	SchemaModeJSONObject
)

// ChatClientConfig selects and configures a chat client.
//
// It is a struct rather than three positional strings because the CLI-backed
// provider needs an executable, not a base URL, and overloading BaseURL to mean
// "sometimes a path to a binary" would make the field's meaning depend on a
// value elsewhere.
type ChatClientConfig struct {
	Provider string
	BaseURL  string
	APIKey   string
	// CLIBin overrides the executable used by CLI-backed providers. Empty uses
	// the provider's default name from PATH.
	CLIBin string
}

// NewChatClient builds the configured chat client. DeepSeek uses the same
// OpenAI-compatible adapter with a different base URL.
func NewChatClient(cfg ChatClientConfig) ChatClient {
	switch cfg.Provider {
	case ChatProviderAnthropic:
		return NewAnthropicClient(cfg.BaseURL, cfg.APIKey)
	case ChatProviderClaudeCLI:
		return NewCLIChatClient(cfg.CLIBin)
	case ChatProviderDeepSeek:
		client := NewOpenAICompatibleClient(cfg.BaseURL, cfg.APIKey)
		client.SchemaMode = SchemaModeJSONObject
		return client
	case ChatProviderOpenAI, ChatProviderOpenAICompatible, "":
		return NewOpenAICompatibleClient(cfg.BaseURL, cfg.APIKey)
	default:
		return NewOpenAICompatibleClient(cfg.BaseURL, cfg.APIKey)
	}
}

// NewOpenAICompatibleClient builds a chat-completions adapter targeting baseURL.
func NewOpenAICompatibleClient(baseURL, apiKey string) *OpenAICompatibleClient {
	return &OpenAICompatibleClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// Chat sends req to POST {BaseURL}/chat/completions and decodes the assistant
// text plus provider token usage.
func (c *OpenAICompatibleClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if c.BaseURL == "" {
		return ChatResponse{}, ErrEmptyBaseURL
	}

	body := openAIChatRequest{Model: req.Model, Messages: req.Messages, MaxTokens: req.MaxTokens}
	if len(req.JSONSchema) > 0 {
		switch c.SchemaMode {
		case SchemaModeJSONObject:
			body.ResponseFormat = &openAIResponseFmt{Type: "json_object"}
		default:
			body.ResponseFormat = &openAIResponseFmt{
				Type:       "json_schema",
				JSONSchema: &openAISchemaDef{Name: "response", Schema: req.JSONSchema},
			}
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("unable to marshal chat request, err: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("unable to build chat request, err: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("unable to call chat endpoint, err: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ChatResponse{}, fmt.Errorf("unable to chat: status %d body %q, err: %w", resp.StatusCode, string(body), ErrProviderStatus)
	}

	var decoded openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ChatResponse{}, fmt.Errorf("unable to decode chat response, err: %w", err)
	}
	if len(decoded.Choices) == 0 || decoded.Choices[0].Message.Content == "" {
		return ChatResponse{}, ErrEmptyResponse
	}

	return ChatResponse{
		Text:         decoded.Choices[0].Message.Content,
		InputTokens:  decoded.Usage.PromptTokens,
		OutputTokens: decoded.Usage.CompletionTokens,
		StopReason:   normalizeFinishReason(decoded.Choices[0].FinishReason),
	}, nil
}

// normalizeFinishReason maps OpenAI's finish_reason onto the stop reasons the
// rest of the pipeline understands, so a truncation or a filtered reply reads
// the same whichever provider produced it.
func normalizeFinishReason(reason string) string {
	switch reason {
	case "stop":
		return StopReasonEndTurn
	case "length":
		return StopReasonMaxTokens
	case "content_filter":
		return StopReasonRefusal
	default:
		return reason
	}
}

var _ ChatClient = (*OpenAICompatibleClient)(nil)
