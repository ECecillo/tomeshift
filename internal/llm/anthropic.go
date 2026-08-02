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

// ChatProviderAnthropic selects the Anthropic Messages adapter in NewChatClient.
const ChatProviderAnthropic = "anthropic"

const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	anthropicVersion        = "2023-06-01"
	defaultChatMaxTokens    = 4096
)

type anthropicCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type anthropicSystemBlock struct {
	Type         string                 `json:"type"` // "text"
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicThinking controls extended thinking. Sending it is not optional
// here: omitting the field means "no thinking" on Sonnet 4.6 but "adaptive
// thinking on" from Sonnet 5, and that default change lands badly on this
// pipeline. Thinking tokens bill at the output rate, and max_tokens caps
// thinking plus response together — so on a 50-segment JSON reply the thinking
// would compete with the answer, truncate it, and be silently absorbed (at
// extra cost) by the halved-batch retry.
type anthropicThinking struct {
	Type string `json:"type"` // "disabled" | "enabled"
}

type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       []anthropicSystemBlock `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
}

type anthropicOutputConfig struct {
	Format anthropicOutputFormat `json:"format"`
}

type anthropicOutputFormat struct {
	Type   string          `json:"type"` // "json_schema"
	Schema json.RawMessage `json:"schema"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason  string         `json:"stop_reason"`
	StopDetails map[string]any `json:"stop_details"`
	Usage       struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// AnthropicClient calls the Anthropic Messages API. It folds ChatMessage entries
// with role "system" into the request's system field and places a prompt-cache
// breakpoint on it, so a stable system prefix (style guide + bible) is cached
// across calls.
type AnthropicClient struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

// NewAnthropicClient builds an Anthropic Messages adapter. An empty baseURL
// defaults to the public API host.
func NewAnthropicClient(baseURL, apiKey string) *AnthropicClient {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = defaultAnthropicBaseURL
	}
	return &AnthropicClient{
		BaseURL: base,
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// Chat sends req to POST {BaseURL}/v1/messages and returns the assistant text
// plus total prompt/output token usage (cached + uncached input summed).
func (c *AnthropicClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultChatMaxTokens
	}

	var sysParts []string
	msgs := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			sysParts = append(sysParts, m.Content)
			continue
		}
		msgs = append(msgs, anthropicMessage(m))
	}

	body := anthropicRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		Messages:  msgs,
		Thinking:  &anthropicThinking{Type: "disabled"},
	}
	if len(req.JSONSchema) > 0 {
		body.OutputConfig = &anthropicOutputConfig{
			Format: anthropicOutputFormat{Type: "json_schema", Schema: req.JSONSchema},
		}
	}
	if len(sysParts) > 0 {
		body.System = []anthropicSystemBlock{{
			Type:         "text",
			Text:         strings.Join(sysParts, "\n\n"),
			CacheControl: &anthropicCacheControl{Type: "ephemeral"},
		}}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("unable to marshal anthropic request, err: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("unable to build anthropic request, err: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if c.APIKey != "" {
		httpReq.Header.Set("x-api-key", c.APIKey)
	}

	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("unable to call anthropic endpoint, err: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ChatResponse{}, fmt.Errorf("unable to chat: status %d body %q, err: %w", resp.StatusCode, string(raw), ErrProviderStatus)
	}

	var decoded anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ChatResponse{}, fmt.Errorf("unable to decode anthropic response, err: %w", err)
	}

	var text strings.Builder
	for _, block := range decoded.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}

	out := ChatResponse{
		Text:                     text.String(),
		InputTokens:              decoded.Usage.InputTokens, // uncached only
		OutputTokens:             decoded.Usage.OutputTokens,
		CacheReadInputTokens:     decoded.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: decoded.Usage.CacheCreationInputTokens,
		StopReason:               decoded.StopReason,
		StopDetails:              decoded.StopDetails,
	}

	// An empty reply is only a protocol error when the provider claims it ended
	// normally. A refusal or a truncation also arrives empty, and the caller
	// needs the stop reason to tell those apart rather than a bare
	// ErrEmptyResponse.
	if text.Len() == 0 && (decoded.StopReason == "" || decoded.StopReason == StopReasonEndTurn) {
		return ChatResponse{}, ErrEmptyResponse
	}

	return out, nil
}

var _ ChatClient = (*AnthropicClient)(nil)
