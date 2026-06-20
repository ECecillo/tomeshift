package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Chat provider identifiers accepted by NewChatClient.
const (
	ChatProviderOpenAI           = "openai"
	ChatProviderDeepSeek         = "deepseek"
	ChatProviderOpenAICompatible = "openai-compatible"
)

type openAIChatRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
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
}

// NewChatClient builds the configured chat client. DeepSeek uses the same
// OpenAI-compatible adapter with a different base URL.
func NewChatClient(provider, baseURL, apiKey string) ChatClient {
	switch provider {
	case ChatProviderDeepSeek, ChatProviderOpenAICompatible, "":
		return NewOpenAICompatibleClient(baseURL, apiKey)
	default:
		return NewOpenAICompatibleClient(baseURL, apiKey)
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

	payload, err := json.Marshal(openAIChatRequest(req))
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
		client = &http.Client{Timeout: 60 * time.Second}
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
	}, nil
}

var _ ChatClient = (*OpenAICompatibleClient)(nil)
