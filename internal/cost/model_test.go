package cost

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/segment"
)

func TestModel_ZeroIsFree(t *testing.T) {
	t.Parallel()

	var m Model
	require.True(t, m.IsZero())

	proj := m.Project([]segment.Segment{{SourceText: "hello"}, {SourceText: "world"}}, LLMPass{}, 0)
	require.Equal(t, 2, proj.Segments)
	require.Equal(t, 10, proj.Chars)
	require.InDelta(t, 0.0, proj.EstimatedUSD, 0.0001)
	require.InDelta(t, 0.0, m.Actual(2, 10, 0, TokenUsage{}), 0.0001)
}

func TestModel_Project(t *testing.T) {
	t.Parallel()

	m := Model{PerSegmentUSD: 0.01, Per1000CharsUSD: 2.0}

	// 2 segments, 10 chars total: 2*0.01 + (10/1000)*2.0 = 0.02 + 0.02 = 0.04
	proj := m.Project([]segment.Segment{{SourceText: "hello"}, {SourceText: "world"}}, LLMPass{}, 0)
	require.InDelta(t, 0.04, proj.EstimatedUSD, 0.0001)
}

func TestModel_ProjectAppliesTheFreeTier(t *testing.T) {
	t.Parallel()

	m := Model{Per1000CharsUSD: 2.0, FreeCharsPerMonth: 8}
	segs := []segment.Segment{{SourceText: "hello"}, {SourceText: "world"}}

	// 10 chars, 8 free: only 2 are billed.
	proj := m.Project(segs, LLMPass{}, 0)
	require.Equal(t, 2, proj.BillableChars)
	require.InDelta(t, 0.004, proj.EstimatedUSD, 1e-9)

	// The allowance is already spent this month: everything is billed.
	spent := m.Project(segs, LLMPass{}, 8)
	require.Equal(t, 10, spent.BillableChars)
	require.InDelta(t, 0.02, spent.EstimatedUSD, 1e-9)
}

func TestModel_ProjectAddsTheLLMPass(t *testing.T) {
	t.Parallel()

	m := Model{
		Per1000CharsUSD: 0.02,
		Tokens:          TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, PromptOverheadTokens: 3000},
	}
	segs := makeSegments(100, 400)

	draftOnly := m.Project(segs, LLMPass{}, 0)
	literary := m.Project(segs, LLMPass{
		Segments: len(segs), Chars: segment.SourceChars(segs), Calls: 2,
		Chapters: []ChapterShape{{SourceChars: 40_000, Calls: 2}},
	}, 0)

	require.InDelta(t, draftOnly.NMTCostUSD, literary.NMTCostUSD, 1e-9)
	require.Equal(t, 2, literary.LLMCalls)
	require.Greater(t, literary.LLMCostUSD, 0.0)
	require.InDelta(t, literary.NMTCostUSD+literary.LLMCostUSD, literary.EstimatedUSD, 1e-9)
	require.Greater(t, literary.EstimatedUSD, draftOnly.EstimatedUSD)
}

func TestModel_Actual(t *testing.T) {
	t.Parallel()

	m := Model{PerSegmentUSD: 0.01, Per1000CharsUSD: 2.0, Tokens: TokenRates{OutputPerMTokUSD: 15}}
	require.InDelta(t, 0.04, m.Actual(2, 10, 0, TokenUsage{}), 0.0001)
	require.InDelta(t, 0.0, m.Actual(0, 0, 0, TokenUsage{}), 0.0001)
	// 2 NMT segments plus 1M reported output tokens: 0.04 + 15 = 15.04
	require.InDelta(t, 15.04, m.Actual(2, 10, 0, TokenUsage{OutputTokens: 1_000_000}), 0.0001)
}

func TestTokenRates_CallCountDominatesTheBill(t *testing.T) {
	t.Parallel()

	rates := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, PromptOverheadTokens: 3000}

	// Same words either way. One segment per call — how the HTTP post-editor
	// escalates — repeats the prefix 100 times; batching 50 at a time repeats
	// it twice. That gap is why the two passes are priced from the same value.
	_, _, perSegment := rates.Project(LLMPass{Segments: 100, Chars: 40_000, Calls: 100})
	_, _, batched := rates.Project(LLMPass{Segments: 100, Chars: 40_000, Calls: 2})
	require.Greater(t, perSegment, batched)

	// A pass that rewrites nothing costs nothing.
	_, _, none := rates.Project(LLMPass{})
	require.Zero(t, none)
}

func TestModel_CountsRunesNotBytes(t *testing.T) {
	t.Parallel()

	var m Model
	// "café" is 4 runes but 5 bytes.
	proj := m.Project([]segment.Segment{{SourceText: "café"}}, LLMPass{}, 0)
	require.Equal(t, 4, proj.Chars)
}
