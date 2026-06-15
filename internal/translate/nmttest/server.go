// Package nmttest provides a deterministic, in-process NMT endpoint for tests.
//
// It is imported only by test code (unit and integration), so it is never
// linked into the production binary. The server speaks the same JSON wire
// contract as translate.HTTPTranslator.
package nmttest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

type requestBody struct {
	Text       string `json:"text"`
	SourceLang string `json:"source_lang"`
	TargetLang string `json:"target_lang"`
}

type responseBody struct {
	TranslatedText string `json:"translated_text"`
}

// TranslateFunc maps a source text to its (deterministic) translation. It must
// preserve any placeholder tokens present in the input.
type TranslateFunc func(text string) string

// DefaultTranslate is a deterministic stand-in translation that prefixes the
// source with a marker while leaving placeholder tokens untouched (they remain
// part of the returned string verbatim).
func DefaultTranslate(text string) string {
	return "[fr] " + text
}

// NewServer starts an httptest.Server that responds with translate(text). If
// translate is nil, DefaultTranslate is used. Callers must Close the server.
func NewServer(translate TranslateFunc) *httptest.Server {
	if translate == nil {
		translate = DefaultTranslate
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
		_ = json.NewEncoder(w).Encode(responseBody{TranslatedText: translate(req.Text)})
	})

	return httptest.NewServer(handler)
}
