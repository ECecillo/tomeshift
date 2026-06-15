package cost

import (
	"unicode/utf8"

	"github.com/ECecillo/ebooks-translator/internal/segment"
)

// Model describes the per-unit translation cost for the configured provider.
// A zero Model (the default for local NMT) yields zero projected and actual
// cost.
type Model struct {
	PerSegmentUSD    float64 // flat cost charged per translated segment
	Per1000CharsUSD  float64 // cost per 1000 source characters translated
	LLMPerSegmentUSD float64 // flat cost per selectively post-edited segment
}

// Projection is the estimated spend for a set of segments before translation.
type Projection struct {
	Segments     int     `json:"segments"`
	Chars        int     `json:"chars"`
	EstimatedUSD float64 `json:"estimated_usd"`
}

// Project estimates the worst-case cost of translating every segment (i.e.
// assuming no TM reuse), which is the safe basis for a pre-run budget check.
func (m Model) Project(segments []segment.Segment) Projection {
	proj := Projection{Segments: len(segments)}
	for i := range segments {
		proj.Chars += utf8.RuneCountInString(segments[i].SourceText)
	}
	proj.EstimatedUSD = m.cost(proj.Segments, proj.Chars)
	return proj
}

// Actual computes the realized cost for the segments actually sent to the NMT
// provider (TM hits are free) plus the LLM post-edit cost for escalated
// segments.
func (m Model) Actual(nmtSegments, nmtChars, llmSegments int) float64 {
	return m.cost(nmtSegments, nmtChars) + float64(llmSegments)*m.LLMPerSegmentUSD
}

func (m Model) cost(segments, chars int) float64 {
	return float64(segments)*m.PerSegmentUSD + (float64(chars)/1000.0)*m.Per1000CharsUSD
}

// IsZero reports whether the model carries no cost (local/free provider).
func (m Model) IsZero() bool {
	return m.PerSegmentUSD == 0 && m.Per1000CharsUSD == 0 && m.LLMPerSegmentUSD == 0
}
