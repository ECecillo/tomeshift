package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const engineLibreTranslate = "nmt-libretranslate"

// libreRequestBody is the LibreTranslate wire contract. format=html keeps our
// placeholder tokens (HTML-like void elements) intact through translation.
type libreRequestBody struct {
	Q      string `json:"q"`
	Source string `json:"source"`
	Target string `json:"target"`
	Format string `json:"format"`
	APIKey string `json:"api_key,omitempty"`
}

type libreResponseBody struct {
	TranslatedText string `json:"translatedText"`
}

// LibreTranslator is a self-hosted NMT adapter for a LibreTranslate / Argos
// endpoint, a practical real EN→FR engine for local/offline use.
type LibreTranslator struct {
	Endpoint string
	APIKey   string
	Client   *http.Client

	charMeter
}

// NewLibreTranslator builds an adapter targeting a LibreTranslate /translate
// endpoint. apiKey is optional (LibreTranslate may run keyless locally).
func NewLibreTranslator(endpoint, apiKey string) *LibreTranslator {
	return &LibreTranslator{
		Endpoint: endpoint,
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// Engine returns the provider identifier used for TM accounting/reporting.
func (t *LibreTranslator) Engine() string { return engineLibreTranslate }

// Translate sends one segment to LibreTranslate and returns the translation,
// requesting HTML format so placeholder tokens survive.
func (t *LibreTranslator) Translate(ctx context.Context, req Request) (Response, error) {
	if t.Endpoint == "" {
		return Response{}, ErrEmptyEndpoint
	}

	payload, err := json.Marshal(libreRequestBody{
		Q:      req.Text,
		Source: req.SourceLang,
		Target: req.TargetLang,
		Format: "html",
		APIKey: t.APIKey,
	})
	if err != nil {
		return Response{}, fmt.Errorf("unable to marshal libretranslate request, err: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("unable to build libretranslate request, err: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	t.record(req.Text)
	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("unable to call libretranslate endpoint, err: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Response{}, fmt.Errorf("unable to translate: status %d body %q, err: %w", resp.StatusCode, string(body), ErrProviderStatus)
	}

	var decoded libreResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return Response{}, fmt.Errorf("unable to decode libretranslate response, err: %w", err)
	}
	if decoded.TranslatedText == "" {
		return Response{}, ErrEmptyTranslation
	}

	return Response{Text: decoded.TranslatedText, Engine: engineLibreTranslate}, nil
}

// TranslateBatch loops over Translate. LibreTranslate accepts an array in `q`,
// but the self-hosted Argos path is being retired in favour of Google, so the
// loop is kept rather than adding a second wire shape to maintain.
func (t *LibreTranslator) TranslateBatch(ctx context.Context, req BatchRequest) ([]Response, error) {
	return translateEach(ctx, t, req)
}

var _ Translator = (*LibreTranslator)(nil)
