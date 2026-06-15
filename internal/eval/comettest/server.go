// Package comettest provides a deterministic in-process COMET scoring service
// for tests. It is imported only by test code and never linked into the binary.
package comettest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

type sample struct {
	Src string `json:"src"`
	MT  string `json:"mt"`
	Ref string `json:"ref,omitempty"`
}

type request struct {
	Samples []sample `json:"samples"`
}

type response struct {
	Scores []float64 `json:"scores"`
}

// ScoreFunc maps a (src, mt, ref) sample to a score. ref is empty for QE.
type ScoreFunc func(src, mt, ref string) float64

// Constant returns a ScoreFunc that always yields score.
func Constant(score float64) ScoreFunc {
	return func(string, string, string) float64 { return score }
}

// NewServer starts an httptest.Server scoring samples with score. If score is
// nil a constant 0.5 is used. Callers must Close the server.
func NewServer(score ScoreFunc) *httptest.Server {
	if score == nil {
		score = Constant(0.5)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		scores := make([]float64, len(req.Samples))
		for i, s := range req.Samples {
			scores[i] = score(s.Src, s.MT, s.Ref)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{Scores: scores})
	})

	return httptest.NewServer(handler)
}
