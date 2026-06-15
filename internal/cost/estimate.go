package cost

import (
	"unicode/utf8"

	"github.com/ECecillo/ebooks-translator/internal/segment"
)

// charsPerToken is a rough heuristic for English: ~4 characters per token. It
// is good enough for an upfront budget estimate; for exact accounting use the
// provider's tokenizer.
const charsPerToken = 4.0

// TokenRates describes an LLM provider's token pricing for cost estimation.
type TokenRates struct {
	InputPerMTokUSD      float64 // USD per 1M input tokens
	OutputPerMTokUSD     float64 // USD per 1M output tokens
	OutputExpansion      float64 // FR/EN output token expansion (e.g. 1.15)
	PromptOverheadTokens int     // fixed input tokens added per LLM call (system prompt, context)
}

func (r TokenRates) outputExpansion() float64 {
	if r.OutputExpansion <= 0 {
		return 1.15
	}
	return r.OutputExpansion
}

// ScenarioEstimate is the projected cost of one translation strategy.
type ScenarioEstimate struct {
	Name         string  `json:"name"`
	NMTSegments  int     `json:"nmt_segments"`
	LLMSegments  int     `json:"llm_segments"`
	InputTokens  int     `json:"input_tokens"`  // LLM input tokens
	OutputTokens int     `json:"output_tokens"` // LLM output tokens
	NMTCostUSD   float64 `json:"nmt_cost_usd"`
	LLMCostUSD   float64 `json:"llm_cost_usd"`
	TotalUSD     float64 `json:"total_usd"`
}

// tokensFromChars converts a character count to an estimated token count.
func tokensFromChars(chars int) int {
	return int(float64(chars)/charsPerToken + 0.5)
}

// EstimateScenarios projects the cost of translating the given segments under
// three strategies: NMT-only (no LLM), hybrid (a fraction escalated to the LLM),
// and full-LLM (every segment post-edited). escalationRate is the hybrid
// fraction in [0,1].
func (m Model) EstimateScenarios(segments []segment.Segment, rates TokenRates, escalationRate float64) []ScenarioEstimate {
	total := len(segments)
	totalChars := 0
	for i := range segments {
		totalChars += utf8.RuneCountInString(segments[i].SourceText)
	}

	if escalationRate < 0 {
		escalationRate = 0
	}
	if escalationRate > 1 {
		escalationRate = 1
	}

	nmtAll := m.cost(total, totalChars)

	llmFor := func(segs, chars int) (in, out int, usd float64) {
		in = tokensFromChars(chars) + segs*rates.PromptOverheadTokens
		out = int(float64(tokensFromChars(chars))*rates.outputExpansion() + 0.5)
		usd = float64(in)/1_000_000*rates.InputPerMTokUSD + float64(out)/1_000_000*rates.OutputPerMTokUSD
		return in, out, usd
	}

	// NMT-only.
	nmtOnly := ScenarioEstimate{Name: "nmt-only", NMTSegments: total, NMTCostUSD: nmtAll, TotalUSD: nmtAll}

	// Hybrid: all segments go through NMT; a fraction is also LLM post-edited.
	hSegs := int(float64(total)*escalationRate + 0.5)
	hChars := 0
	if total > 0 {
		hChars = int(float64(totalChars) * float64(hSegs) / float64(total))
	}
	hIn, hOut, hLLM := llmFor(hSegs, hChars)
	hybrid := ScenarioEstimate{
		Name: "hybrid", NMTSegments: total, LLMSegments: hSegs,
		InputTokens: hIn, OutputTokens: hOut,
		NMTCostUSD: nmtAll, LLMCostUSD: hLLM, TotalUSD: nmtAll + hLLM,
	}

	// Full-LLM: every segment post-edited (still drafted by NMT).
	fIn, fOut, fLLM := llmFor(total, totalChars)
	full := ScenarioEstimate{
		Name: "full-llm", NMTSegments: total, LLMSegments: total,
		InputTokens: fIn, OutputTokens: fOut,
		NMTCostUSD: nmtAll, LLMCostUSD: fLLM, TotalUSD: nmtAll + fLLM,
	}

	return []ScenarioEstimate{nmtOnly, hybrid, full}
}
