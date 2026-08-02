package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"unicode/utf8"
)

const (
	engineGoogle = "nmt-google"
	// defaultGoogleEndpoint is Cloud Translation v2 (Basic). v3 (Advanced) is
	// deliberately not targeted: it has no API-key authentication, and its
	// server-side glossary would not replace Protect/Apply on the LLM side.
	defaultGoogleEndpoint = "https://translation.googleapis.com/language/translate/v2"
	// googleMaxBatchTexts is the documented ceiling on `q` entries per request.
	googleMaxBatchTexts = 128
	// googleMaxBatchChars keeps a request comfortably inside the payload limit;
	// a single text longer than this is sent on its own rather than rejected.
	googleMaxBatchChars = 5000
	// googleMaxTextChars is the provider's own ceiling on one request. A single
	// segment past it cannot be batched around and must not be split either —
	// splitting mid-sentence would cut placeholder tokens in half — so it is
	// rejected with a precise error instead of a 400 from the API.
	googleMaxTextChars = 30000
)

// googleRequestBody is the Cloud Translation v2 wire contract. format=html is
// what keeps our <x id="N"/> placeholder tokens intact through translation:
// Google treats them as markup and copies them across instead of translating
// their attributes.
type googleRequestBody struct {
	Q      []string `json:"q"`
	Source string   `json:"source"`
	Target string   `json:"target"`
	Format string   `json:"format"`
}

type googleResponseBody struct {
	Data struct {
		Translations []struct {
			TranslatedText string `json:"translatedText"`
		} `json:"translations"`
	} `json:"data"`
}

// GoogleTranslator is a Cloud Translation v2 (Basic) adapter. It authenticates
// with an API key, so it maps directly onto the existing NMTEndpoint/NMTAPIKey
// configuration and needs no service-account credentials.
type GoogleTranslator struct {
	Endpoint string
	APIKey   string
	Client   *http.Client

	charMeter
}

// NewGoogleTranslator builds an adapter targeting Cloud Translation v2. An
// empty endpoint falls back to the public API host.
func NewGoogleTranslator(endpoint, apiKey string) *GoogleTranslator {
	if endpoint == "" {
		endpoint = defaultGoogleEndpoint
	}
	return &GoogleTranslator{
		Endpoint: endpoint,
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// Engine returns the provider identifier used for TM accounting/reporting.
func (t *GoogleTranslator) Engine() string { return engineGoogle }

// Translate sends one segment to Cloud Translation.
func (t *GoogleTranslator) Translate(ctx context.Context, req Request) (Response, error) {
	resps, err := t.TranslateBatch(ctx, BatchRequest{
		Texts:      []string{req.Text},
		SourceLang: req.SourceLang,
		TargetLang: req.TargetLang,
	})
	if err != nil {
		return Response{}, err
	}
	return resps[0], nil
}

// TranslateBatch translates every text in req, splitting it into as few
// requests as the provider's limits allow. Results stay aligned with req.Texts
// by index; a response array shorter than the request it answers is an error
// rather than a silent shift of every following translation.
func (t *GoogleTranslator) TranslateBatch(ctx context.Context, req BatchRequest) ([]Response, error) {
	if t.Endpoint == "" {
		return nil, ErrEmptyEndpoint
	}
	if len(req.Texts) == 0 {
		return nil, nil
	}

	for i, text := range req.Texts {
		if size := utf8.RuneCountInString(text); size > googleMaxTextChars {
			return nil, fmt.Errorf("unable to translate text %d: %d characters exceeds the %d-character request limit, err: %w",
				i, size, googleMaxTextChars, ErrTextTooLong)
		}
	}

	out := make([]Response, 0, len(req.Texts))
	for _, chunk := range chunkTexts(req.Texts, googleMaxBatchTexts, googleMaxBatchChars) {
		translated, err := t.translateChunk(ctx, chunk, req.SourceLang, req.TargetLang)
		if err != nil {
			return nil, err
		}
		out = append(out, translated...)
	}
	return out, nil
}

// translateChunk performs one HTTP round-trip for a bounded slice of texts.
func (t *GoogleTranslator) translateChunk(ctx context.Context, texts []string, source, target string) ([]Response, error) {
	payload, err := json.Marshal(googleRequestBody{
		Q:      texts,
		Source: source,
		Target: target,
		Format: "html",
	})
	if err != nil {
		return nil, fmt.Errorf("unable to marshal google translate request, err: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("unable to build google translate request, err: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	// The key goes in a header, not the documented `?key=` query parameter:
	// net/http embeds the full URL in every transport error, which the pipeline
	// wraps and writes to the run log. A query-string key would end up on disk
	// the first time the network hiccuped.
	if t.APIKey != "" {
		httpReq.Header.Set("X-goog-api-key", t.APIKey)
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	t.record(texts...)
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("unable to call google translate endpoint, err: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("unable to translate: status %d body %q, err: %w", resp.StatusCode, string(body), ErrProviderStatus)
	}

	var decoded googleResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("unable to decode google translate response, err: %w", err)
	}

	translations := decoded.Data.Translations
	if len(translations) != len(texts) {
		return nil, fmt.Errorf("unable to map google translate response: got %d translations for %d texts, err: %w",
			len(translations), len(texts), ErrShortBatchResponse)
	}

	out := make([]Response, 0, len(translations))
	for i := range translations {
		// With format=html Google escapes the text content it returns (&#39;,
		// &amp;, ...) while leaving our placeholder tags alone. Unescaping once
		// restores the plain text the segmenter handed us.
		text := html.UnescapeString(translations[i].TranslatedText)
		if text == "" {
			return nil, ErrEmptyTranslation
		}
		out = append(out, Response{Text: text, Engine: engineGoogle})
	}
	return out, nil
}

// chunkTexts splits texts into slices of at most maxTexts entries and maxChars
// total characters. A single text over maxChars is not split — it travels alone
// so the caller still gets one translation per input.
func chunkTexts(texts []string, maxTexts, maxChars int) [][]string {
	var (
		chunks  [][]string
		current []string
		chars   int
	)
	for _, text := range texts {
		size := utf8.RuneCountInString(text)
		if len(current) > 0 && (len(current) >= maxTexts || chars+size > maxChars) {
			chunks = append(chunks, current)
			current, chars = nil, 0
		}
		current = append(current, text)
		chars += size
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

var _ Translator = (*GoogleTranslator)(nil)
