// Package llmtest provides a deterministic in-process LLM post-edit endpoint
// for tests. It is imported only by test code and never linked into the binary.
package llmtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

type requestBody struct {
	SourceText string `json:"source_text"`
	NMTText    string `json:"nmt_text"`
	SourceLang string `json:"source_lang"`
	TargetLang string `json:"target_lang"`
}

type responseBody struct {
	TranslatedText string   `json:"translated_text"`
	Notes          string   `json:"notes"`
	ChangedTerms   []string `json:"changed_terms"`
}

// EditFunc maps an NMT draft to a refined translation. It must preserve any
// placeholder tokens present in the draft.
type EditFunc func(nmtText string) string

// DefaultEdit returns the draft unchanged (a no-op post-edit that trivially
// preserves placeholders).
func DefaultEdit(nmtText string) string { return nmtText }

// NewServer starts an httptest.Server responding with edit(nmt_text). If edit
// is nil, DefaultEdit is used. Callers must Close the server.
func NewServer(edit EditFunc) *httptest.Server {
	if edit == nil {
		edit = DefaultEdit
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req requestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responseBody{
			TranslatedText: edit(req.NMTText),
			Notes:          "post-edited",
		})
	})

	return httptest.NewServer(handler)
}
