package cost

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/segment"
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

func TestTokenRates_PrefixWrittenPerCall(t *testing.T) {
	t.Parallel()

	// One chapter, six calls, a prefix of 35000 English chars (~8750 tokens at
	// the EN ratio) and no frozen part, so the arithmetic is checkable by hand.
	pass := LLMPass{
		Segments: 258, Chars: 31149, Calls: 6,
		Chapters: []ChapterShape{{SourceChars: 35000, Calls: 6}},
	}
	rates := TokenRates{
		InputPerMTokUSD:     3,
		OutputPerMTokUSD:    15,
		CacheReadPerMTokUSD: 0.30,
	}

	writeOnceIn, writeOnceOut, writeOnceUSD := rates.Project(pass)

	rates.PrefixWrittenPerCall = true
	perCallIn, perCallOut, perCallUSD := rates.Project(pass)

	// The same tokens travel either way — the transport changes the rate they
	// are billed at, not the size of the prompt.
	require.Equal(t, writeOnceIn, perCallIn)
	require.Equal(t, writeOnceOut, perCallOut)

	// Six writes at 1.25x input instead of one write plus five reads at 0.1x.
	perCall := 35000.0 / 4 // charsPerTokenEN
	writes := 6 * perCall / 1e6 * 3 * 1.25
	saved := perCall/1e6*3*1.25 + 5*perCall/1e6*0.30
	require.InDelta(t, writeOnceUSD-saved+writes, perCallUSD, 1e-9)
	require.Greater(t, perCallUSD, writeOnceUSD, "a session-per-call transport cannot be cheaper")
}

func TestTokenRates_PrefixWrittenPerCall_IgnoredWhenTheProviderDoesNotCache(t *testing.T) {
	t.Parallel()

	pass := LLMPass{
		Segments: 10, Chars: 1200, Calls: 3,
		Chapters: []ChapterShape{{SourceChars: 4000, Calls: 3}},
	}
	// No cache read rate: the prefix is paid at full input rate on every call
	// already, so there is no write premium to add and nothing to change.
	rates := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15}

	_, _, without := rates.Project(pass)
	rates.PrefixWrittenPerCall = true
	_, _, with := rates.Project(pass)

	require.InDelta(t, without, with, 1e-12)
}

func TestTokenRates_MeasuredOutputBeatsTheHeuristic(t *testing.T) {
	t.Parallel()

	// The measured chapter: 258 segments, 31149 source chars, 6 calls.
	pass := LLMPass{
		Segments: 258, Chars: 31149, Calls: 6,
		Chapters:     []ChapterShape{{SourceChars: 31149, Calls: 6}},
		ContextRunes: LiteraryContextRunes,
	}
	rates := TokenRates{
		InputPerMTokUSD: 3, OutputPerMTokUSD: 15, CacheReadPerMTokUSD: 0.30,
		PromptOverheadTokens: 20024, TokenMultiplier: 1.3,
	}

	_, heuristicOut, heuristicUSD := rates.Project(pass)
	// The heuristic's answer on that chapter, which the run then contradicted.
	require.InDelta(t, 16180, heuristicOut, 40)

	// What the run actually consumed: 53409 output tokens, i.e. 207 per segment.
	rates.OutputTokensPerSegment = 53409.0 / 258.0
	_, measuredOut, measuredUSD := rates.Project(pass)

	require.Equal(t, 53409, measuredOut)
	require.Greater(t, measuredUSD, heuristicUSD)

	// The multiplier corrects the character heuristic, so it must not also
	// scale a figure that came from the provider's own tokenizer.
	rates.TokenMultiplier = 2.6
	_, doubled, _ := rates.Project(pass)
	require.Equal(t, measuredOut, doubled)
}

func TestTokenRates_MeasuredOutputOnlyTouchesTheOutputSide(t *testing.T) {
	t.Parallel()

	pass := LLMPass{
		Segments: 100, Chars: 12000, Calls: 2,
		Chapters: []ChapterShape{{SourceChars: 12000, Calls: 2}},
	}
	rates := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, CacheReadPerMTokUSD: 0.30}

	baseIn, _, _ := rates.Project(pass)
	rates.OutputTokensPerSegment = 207
	withIn, withOut, _ := rates.Project(pass)

	// The input side is dominated by the prefix, which a run measures from the
	// bible it loaded — a measurement of output verbosity says nothing about it.
	require.Equal(t, baseIn, withIn)
	require.Equal(t, 20700, withOut)
}

func TestTokenRates_UncachedPassIgnoresTheChatTransport(t *testing.T) {
	t.Parallel()

	// The escalation pass: one call per segment, no chapter source, no cache.
	pass := LLMPass{Segments: 50, Chars: 6000, Calls: 50, Uncached: true}
	rates := TokenRates{
		InputPerMTokUSD: 3, OutputPerMTokUSD: 15, CacheReadPerMTokUSD: 0.30,
		PromptOverheadTokens: 20000,
	}

	_, _, plain := rates.Project(pass)

	// Neither the chat provider's cache nor its session behaviour can change
	// what a different service, which caches nothing, is charged.
	rates.PrefixWrittenPerCall = true
	_, _, withTransport := rates.Project(pass)
	require.InDelta(t, plain, withTransport, 1e-12)

	// And the prefix is paid in full on every call: 50 x 20000 tokens at $3/Mtok.
	require.Greater(t, plain, 50*20000/1e6*3.0*0.99)
}
