package cost

import "github.com/ECecillo/ebooks-translator/internal/segment"

// Model describes what a run is charged for: the NMT draft, priced per segment
// and per character with a monthly free allowance, and the LLM pass, priced by
// token.
//
// There is one cost model and one projection. The `estimate` command and the
// run-time budget guardrail both go through Project, so what `estimate` reports
// is what the per-book cap enforces.
//
// A zero Model yields zero projected and actual cost, which is the correct
// answer for a self-hosted NMT engine with no LLM pass.
type Model struct {
	PerSegmentUSD   float64 // flat cost charged per translated segment
	Per1000CharsUSD float64 // cost per 1000 source characters translated
	// FreeCharsPerMonth is the provider's monthly free character allowance,
	// shared across the whole billing project. Google Cloud Translation grants
	// 500,000 characters; 0 means no free tier.
	FreeCharsPerMonth int
	// Tokens prices the LLM pass. A zero value means no LLM cost.
	Tokens TokenRates
}

// LLMPass fully describes an LLM stage: how much text it rewrites, how many
// calls it takes to do it, how large the prefix each call repeats is, and how
// many times that prefix has to be written to the provider's prompt cache. A
// zero LLMPass means there is no LLM stage.
//
// The count of calls — not the count of segments — drives the dominant part of
// input cost, because every call repeats the stable prefix. That is why the two
// are separate fields: the literary pass batches 50 segments per call, while
// the HTTP post-editor sends one, and they cost wildly different amounts for
// the same words.
type LLMPass struct {
	// Segments and Chars are the text the pass rewrites. They are not
	// necessarily the whole book: a selective escalation only covers part of it.
	Segments int
	Chars    int
	// Calls is the number of chat completions the pass will make. Literary
	// chunking is per chapter, so it is the sum of
	// ceil(chapterSegments/batchSize), not ceil(totalSegments/batchSize).
	Calls int
	// PrefixChars is the measured size of the frozen part of the prefix (style
	// guide, character voices, address rules, glossary) repeated on every call.
	// 0 falls back to TokenRates.PromptOverheadTokens, which is what `estimate`
	// uses: it has no bible to measure.
	PrefixChars int
	// Chapters is the per-chapter shape of the pass, when the prefix carries
	// the chapter source. Each chapter writes its own prefix to the cache once
	// and reads it back on every remaining call.
	//
	// It is a list rather than an average because the two quantities move
	// together: a long chapter has both a bigger prefix and more calls to
	// repeat it in. Multiplying a mean chapter by the total call count ignores
	// that covariance and under-counts exactly where the cost concentrates.
	// Empty means the pass carries no chapter source — the escalation path.
	Chapters []ChapterShape
	// Uncached says this pass gets no prompt cache at all, whatever the
	// provider's rates say. It is a property of the transport executing the
	// pass, not of the account: the HTTP post-editor is a plain JSON POST to a
	// custom endpoint with no cache_control, so its prefix is paid in full on
	// every one of its per-segment calls.
	//
	// Without it, the escalation pass borrowed the chat provider's caching —
	// and then the chat transport's write-per-call correction on top, which is
	// how a projection for a service that does not cache came to depend on how
	// another one does.
	Uncached bool
	// ContextRunes is the volatile prose each call carries besides its payload.
	// The literary pass sends the previous batch's French so register and
	// pronouns survive the seam; the HTTP post-editor sends nothing but the
	// segment, so it is 0 there. Charging it per call regardless would multiply
	// a window that does not exist by one call per escalated segment.
	ContextRunes int
}

// ChapterShape is one chapter's contribution to an LLM pass.
type ChapterShape struct {
	// SourceChars is the chapter source folded into the cacheable prefix. It is
	// English, so it converts at a different ratio from the French bible.
	SourceChars int
	// Calls is how many completions this chapter is chunked into: one cache
	// write and Calls-1 reads.
	Calls int
}

// TokenUsage is the LLM token consumption the provider actually reported. These
// counts come from the provider's own tokenizer, so neither the character
// heuristic nor TokenRates.TokenMultiplier applies to them.
type TokenUsage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// Projection is the estimated spend for a run before any provider call.
type Projection struct {
	Segments      int     `json:"segments"`
	Chars         int     `json:"chars"`
	BillableChars int     `json:"billable_chars"` // chars beyond the monthly free allowance
	LLMSegments   int     `json:"llm_segments"`
	LLMCalls      int     `json:"llm_calls"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	NMTCostUSD    float64 `json:"nmt_cost_usd"`
	LLMCostUSD    float64 `json:"llm_cost_usd"`
	EstimatedUSD  float64 `json:"estimated_usd"`
}

// Project estimates the worst-case cost of drafting every segment (i.e.
// assuming no TM reuse) and running the LLM stage described by pass. A zero
// pass prices the draft alone.
//
// usedFreeChars is how much of this month's free allowance the ledger says has
// already been spent.
func (m Model) Project(segments []segment.Segment, pass LLMPass, usedFreeChars int) Projection {
	proj := Projection{Segments: len(segments), Chars: segment.SourceChars(segments)}
	proj.BillableChars = m.billableChars(proj.Chars, usedFreeChars)
	proj.NMTCostUSD = m.nmtCost(proj.Segments, proj.BillableChars)

	proj.LLMSegments, proj.LLMCalls = pass.Segments, pass.Calls
	proj.InputTokens, proj.OutputTokens, proj.LLMCostUSD = m.Tokens.Project(pass)
	proj.EstimatedUSD = proj.NMTCostUSD + proj.LLMCostUSD
	return proj
}

// Actual computes the realized cost: the segments actually sent to the NMT
// provider (TM hits are free, and the free tier is applied) plus the LLM cost
// derived from the token counts the provider reported.
func (m Model) Actual(nmtSegments, nmtChars, usedFreeChars int, usage TokenUsage) float64 {
	return m.nmtCost(nmtSegments, m.billableChars(nmtChars, usedFreeChars)) + m.Tokens.UsageCost(usage)
}

// billableChars applies the monthly free allowance: only the overflow is
// charged. usedFreeChars is the allowance already consumed this month.
func (m Model) billableChars(chars, usedFreeChars int) int {
	remaining := max(m.FreeCharsPerMonth-usedFreeChars, 0)
	return max(chars-remaining, 0)
}

func (m Model) nmtCost(segments, billableChars int) float64 {
	return float64(segments)*m.PerSegmentUSD + (float64(billableChars)/1000.0)*m.Per1000CharsUSD
}

// IsZero reports whether the model carries no cost (local/free provider).
func (m Model) IsZero() bool {
	return m.PerSegmentUSD == 0 && m.Per1000CharsUSD == 0 && m.Tokens.IsZero()
}
