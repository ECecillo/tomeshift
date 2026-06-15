package cost

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/segment"
)

func makeSegments(n, charsEach int) []segment.Segment {
	segs := make([]segment.Segment, n)
	text := make([]byte, charsEach)
	for i := range text {
		text[i] = 'a'
	}
	for i := range segs {
		segs[i] = segment.Segment{SourceText: string(text)}
	}
	return segs
}

func TestEstimateScenarios_Shape(t *testing.T) {
	t.Parallel()

	m := Model{}
	rates := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, OutputExpansion: 1.2, PromptOverheadTokens: 100}

	// 10 segments × 400 chars = 4000 chars ≈ 1000 source tokens.
	got := m.EstimateScenarios(makeSegments(10, 400), rates, 0.2)
	require.Len(t, got, 3)

	nmtOnly, hybrid, full := got[0], got[1], got[2]
	require.Equal(t, "nmt-only", nmtOnly.Name)
	require.Equal(t, "hybrid", hybrid.Name)
	require.Equal(t, "full-llm", full.Name)

	// NMT-only has no LLM cost; cost model is zero so total is 0.
	require.Equal(t, 0, nmtOnly.LLMSegments)
	require.InDelta(t, 0.0, nmtOnly.TotalUSD, 0.0001)

	// Hybrid escalates 20% = 2 segments; full escalates all 10.
	require.Equal(t, 2, hybrid.LLMSegments)
	require.Equal(t, 10, full.LLMSegments)

	// Cost ordering: nmt-only <= hybrid <= full.
	require.LessOrEqual(t, nmtOnly.TotalUSD, hybrid.TotalUSD)
	require.Less(t, hybrid.TotalUSD, full.TotalUSD)
	require.Greater(t, full.LLMCostUSD, 0.0)
}

func TestEstimateScenarios_FullCostMath(t *testing.T) {
	t.Parallel()

	m := Model{}
	rates := TokenRates{InputPerMTokUSD: 1, OutputPerMTokUSD: 2, OutputExpansion: 1.0, PromptOverheadTokens: 0}

	// 1 segment, 4000 chars ≈ 1000 tokens in and out.
	full := m.EstimateScenarios(makeSegments(1, 4000), rates, 0)[2]
	require.Equal(t, 1000, full.InputTokens)
	require.Equal(t, 1000, full.OutputTokens)
	// 1000/1e6*1 + 1000/1e6*2 = 0.001 + 0.002 = 0.003
	require.InDelta(t, 0.003, full.LLMCostUSD, 1e-9)
}

func TestEstimateScenarios_NMTContributesToTotal(t *testing.T) {
	t.Parallel()

	m := Model{PerSegmentUSD: 0.01}
	got := m.EstimateScenarios(makeSegments(5, 100), TokenRates{}, 0.5)
	// 5 segments × $0.01 NMT each in every scenario.
	require.InDelta(t, 0.05, got[0].NMTCostUSD, 1e-9)
	require.InDelta(t, 0.05, got[1].NMTCostUSD, 1e-9)
}
