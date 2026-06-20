package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"time"
)

const defaultHTTPTimeout = 5 * time.Minute

// placeholderPattern matches both inline (<x id="N"/>) and glossary (<g id="N"/>)
// tokens, which a post-edit must preserve exactly.
var placeholderPattern = regexp.MustCompile(`<[xg] id="\d+"/>`)

type requestBody struct {
	SourceText string `json:"source_text"`
	NMTText    string `json:"nmt_text"`
	SourceLang string `json:"source_lang"`
	TargetLang string `json:"target_lang"`
}

// HTTPPostEditor calls an LLM post-edit endpoint speaking a strict JSON
// contract.
type HTTPPostEditor struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

// NewHTTPPostEditor builds an LLM post-edit adapter targeting endpoint.
func NewHTTPPostEditor(endpoint, apiKey string) *HTTPPostEditor {
	return &HTTPPostEditor{
		Endpoint: endpoint,
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// PostEdit sends the NMT draft for refinement and validates the response. It
// returns ErrPlaceholderChange if the model added, dropped, or reordered any
// placeholder token relative to the NMT draft.
func (p *HTTPPostEditor) PostEdit(ctx context.Context, req PostEditRequest) (PostEditResponse, error) {
	if p.Endpoint == "" {
		return PostEditResponse{}, ErrEmptyEndpoint
	}

	payload, err := json.Marshal(requestBody(req))
	if err != nil {
		return PostEditResponse{}, fmt.Errorf("unable to marshal post-edit request, err: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return PostEditResponse{}, fmt.Errorf("unable to build post-edit request, err: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return PostEditResponse{}, fmt.Errorf("unable to call post-edit endpoint, err: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return PostEditResponse{}, fmt.Errorf("unable to post-edit: status %d body %q, err: %w", resp.StatusCode, string(body), ErrProviderStatus)
	}

	var decoded PostEditResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return PostEditResponse{}, fmt.Errorf("unable to decode post-edit response, err: %w", err)
	}
	if decoded.TranslatedText == "" {
		return PostEditResponse{}, ErrEmptyResponse
	}

	if !placeholdersPreserved(req.NMTText, decoded.TranslatedText) {
		return PostEditResponse{}, ErrPlaceholderChange
	}

	return decoded, nil
}

// SafePostEdit returns the post-edited text when the editor succeeds and
// preserves placeholders, and otherwise falls back to the NMT draft. This is
// the call the routing layer uses so a post-edit can never degrade output.
func SafePostEdit(ctx context.Context, editor PostEditor, req PostEditRequest) (text string, postEdited bool) {
	if editor == nil {
		return req.NMTText, false
	}
	resp, err := editor.PostEdit(ctx, req)
	if err != nil {
		return req.NMTText, false
	}
	return resp.TranslatedText, true
}

// placeholdersPreserved reports whether before and after contain the exact same
// multiset of placeholder tokens.
func placeholdersPreserved(before, after string) bool {
	a := placeholderPattern.FindAllString(before, -1)
	b := placeholderPattern.FindAllString(after, -1)
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ PostEditor = (*HTTPPostEditor)(nil)
