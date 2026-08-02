package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	engineHTTP         = "nmt-http"
	defaultHTTPTimeout = 30 * time.Second
)

// httpRequestBody is the wire contract sent to the NMT endpoint.
type httpRequestBody struct {
	Text       string `json:"text"`
	SourceLang string `json:"source_lang"`
	TargetLang string `json:"target_lang"`
}

// httpResponseBody is the wire contract returned by the NMT endpoint.
type httpResponseBody struct {
	TranslatedText string `json:"translated_text"`
}

// HTTPTranslator is the V1 default provider: it POSTs segment text to a single
// configured NMT endpoint and reads back the translated text.
type HTTPTranslator struct {
	Endpoint string
	APIKey   string
	Client   *http.Client

	charMeter
}

// NewHTTPTranslator builds an HTTP NMT adapter targeting endpoint. apiKey is
// optional; when set it is sent as a Bearer token.
func NewHTTPTranslator(endpoint, apiKey string) *HTTPTranslator {
	return &HTTPTranslator{
		Endpoint: endpoint,
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// Engine returns the provider identifier used for TM accounting/reporting.
func (t *HTTPTranslator) Engine() string { return engineHTTP }

// Translate sends one segment to the configured NMT endpoint and returns the
// translated text. Placeholder tokens embedded in req.Text are expected to be
// preserved by the provider.
func (t *HTTPTranslator) Translate(ctx context.Context, req Request) (Response, error) {
	if t.Endpoint == "" {
		return Response{}, ErrEmptyEndpoint
	}

	payload, err := json.Marshal(httpRequestBody(req))
	if err != nil {
		return Response{}, fmt.Errorf("unable to marshal nmt request, err: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("unable to build nmt request, err: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if t.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+t.APIKey)
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	t.record(req.Text)
	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("unable to call nmt endpoint, err: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Response{}, fmt.Errorf("unable to translate: status %d body %q, err: %w", resp.StatusCode, string(body), ErrProviderStatus)
	}

	var decoded httpResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return Response{}, fmt.Errorf("unable to decode nmt response, err: %w", err)
	}
	if decoded.TranslatedText == "" {
		return Response{}, ErrEmptyTranslation
	}

	return Response{Text: decoded.TranslatedText, Engine: engineHTTP}, nil
}

// TranslateBatch loops over Translate: this dev contract carries one text per
// call, so batching only saves the caller from writing the loop.
func (t *HTTPTranslator) TranslateBatch(ctx context.Context, req BatchRequest) ([]Response, error) {
	return translateEach(ctx, t, req)
}

var _ Translator = (*HTTPTranslator)(nil)
