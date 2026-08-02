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

// oneChapter puts every segment in a single chapter, the simplest shape for
// tests that are not about call counting.
func oneChapter(segs []segment.Segment) [][]segment.Segment {
	return [][]segment.Segment{segs}
}

func TestEstimateScenarios_Shape(t *testing.T) {
	t.Parallel()

	m := Model{Tokens: TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, OutputExpansion: 1.2, PromptOverheadTokens: 100}}

	// 10 segments × 400 chars = 4000 chars, one chapter, batches of 5.
	got := m.EstimateScenarios(oneChapter(makeSegments(10, 400)), 5, 0.2, 0)
	require.Len(t, got, 3)

	nmtOnly, hybrid, full := got[0], got[1], got[2]
	require.Equal(t, "nmt-only", nmtOnly.Name)
	require.Equal(t, "hybrid", hybrid.Name)
	require.Equal(t, "full-llm", full.Name)

	// NMT-only has no LLM cost; cost model is zero so total is 0.
	require.Equal(t, 0, nmtOnly.LLMSegments)
	require.InDelta(t, 0.0, nmtOnly.EstimatedUSD, 0.0001)

	// Hybrid escalates 20% = 2 segments; full escalates all 10.
	require.Equal(t, 2, hybrid.LLMSegments)
	require.Equal(t, 10, full.LLMSegments)

	// 10 segments in batches of 5 is 2 calls.
	require.Equal(t, 2, full.LLMCalls)

	// Cost ordering: nmt-only <= hybrid <= full.
	require.LessOrEqual(t, nmtOnly.EstimatedUSD, hybrid.EstimatedUSD)
	require.Less(t, hybrid.EstimatedUSD, full.EstimatedUSD)
	require.Greater(t, full.LLMCostUSD, 0.0)
}

func TestEstimateScenarios_NMTContributesToTotal(t *testing.T) {
	t.Parallel()

	m := Model{PerSegmentUSD: 0.01}
	got := m.EstimateScenarios(oneChapter(makeSegments(5, 100)), 50, 0.5, 0)
	// 5 segments × $0.01 NMT each in every scenario.
	require.InDelta(t, 0.05, got[0].NMTCostUSD, 1e-9)
	require.InDelta(t, 0.05, got[1].NMTCostUSD, 1e-9)
}

func TestEstimateScenarios_FreeTierOffsetsCharacters(t *testing.T) {
	t.Parallel()

	// $20 per million characters, expressed per 1000, with a 500k free tier.
	m := Model{Per1000CharsUSD: 0.02, FreeCharsPerMonth: 500_000}
	segs := makeSegments(1000, 400) // 400,000 chars

	first := m.EstimateScenarios(oneChapter(segs), 50, 0, 0)
	require.Equal(t, 0, first[0].BillableChars)
	require.InDelta(t, 0.0, first[0].EstimatedUSD, 1e-9)

	// A second volume in the same month: 400k already used, so 300k of this
	// one's 400k falls outside the allowance.
	second := m.EstimateScenarios(oneChapter(segs), 50, 0, 400_000)
	require.Equal(t, 300_000, second[0].BillableChars)
	require.InDelta(t, 6.0, second[0].EstimatedUSD, 1e-9)
}

func TestCallsForChapters_ChunksPerChapter(t *testing.T) {
	t.Parallel()

	// Chunking is per chapter: three 37-segment chapters cost three calls at
	// batch 50, where the naive ceil(111/50) would say three... but at batch
	// 100 the naive answer is two and the real one is still three.
	require.Equal(t, 3, CallsForChapters([]int{37, 37, 37}, 50))
	require.Equal(t, 3, CallsForChapters([]int{37, 37, 37}, 100))
	require.Equal(t, 6, CallsForChapters([]int{60, 60, 60}, 50))
	require.Equal(t, 0, CallsForChapters(nil, 50))
	// A batch size below 1 degrades to one call per segment, not a divide by
	// zero.
	require.Equal(t, 3, CallsForChapters([]int{3}, 0))
}

func TestTokenRates_PrefixIsChargedPerCall(t *testing.T) {
	t.Parallel()

	rates := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, PromptOverheadTokens: 3000}

	// Same segments, same characters — only the batch size differs. Fewer
	// calls means the prefix is repeated fewer times, which is the whole point
	// of a large batch size.
	few := LLMPass{Segments: 100, Chars: 40_000, Calls: CallsForChapters([]int{100}, 50)}
	many := LLMPass{Segments: 100, Chars: 40_000, Calls: CallsForChapters([]int{100}, 10)}

	_, _, fewUSD := rates.Project(few)
	_, _, manyUSD := rates.Project(many)
	require.Less(t, fewUSD, manyUSD)
}

func TestTokenRates_CacheReadRateLowersPrefixCost(t *testing.T) {
	t.Parallel()

	base := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, PromptOverheadTokens: 3000}
	cached := base
	cached.CacheReadPerMTokUSD = 0.30

	pass := LLMPass{Segments: 100, Chars: 40_000, Calls: 100, Chapters: []ChapterShape{{Calls: 100}}}
	_, _, uncachedUSD := base.Project(pass)
	_, _, cachedUSD := cached.Project(pass)

	require.Less(t, cachedUSD, uncachedUSD, "a cache-read rate must make the repeated prefix cheaper")
}

func TestTokenRates_MeasuredPrefixOverridesTheConstant(t *testing.T) {
	t.Parallel()

	rates := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, PromptOverheadTokens: 100}

	small := LLMPass{Segments: 50, Chars: 20_000, Calls: 10}
	// 35,000 chars ≈ 10,000 tokens, far above the 100-token constant.
	measured := LLMPass{Segments: 50, Chars: 20_000, Calls: 10, PrefixChars: 35_000}

	_, _, constantUSD := rates.Project(small)
	_, _, measuredUSD := rates.Project(measured)
	require.Greater(t, measuredUSD, constantUSD)
}

func TestTokenRates_TokenMultiplierScalesTokensNotRates(t *testing.T) {
	t.Parallel()

	// Sonnet 5 against Sonnet 4.6: identical sticker price, ~30% more tokens
	// for the same text. The projection must rise by that 30%.
	sonnet46 := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, CacheReadPerMTokUSD: 0.30, PromptOverheadTokens: 3000}
	sonnet5 := sonnet46
	sonnet5.TokenMultiplier = 1.30

	pass := LLMPass{Segments: 4269, Chars: 472_509, Calls: 130,
		Chapters: chapterShapes(68, 0, 130)}
	_, _, baseUSD := sonnet46.Project(pass)
	_, _, denseUSD := sonnet5.Project(pass)

	require.InDelta(t, 1.30, denseUSD/baseUSD, 0.001)
}

func TestTokenRates_UsageCostUsesReportedTokens(t *testing.T) {
	t.Parallel()

	rates := TokenRates{
		InputPerMTokUSD: 3, OutputPerMTokUSD: 15,
		CacheReadPerMTokUSD: 0.30, CacheWriteMultiplier: 1.25,
		// A multiplier must not touch counts the provider measured itself.
		TokenMultiplier: 1.30,
	}

	usd := rates.UsageCost(TokenUsage{
		InputTokens:         1_000_000,
		OutputTokens:        1_000_000,
		CacheReadTokens:     1_000_000,
		CacheCreationTokens: 1_000_000,
	})
	// 3 + 15 + 0.30 + 3*1.25 = 22.05
	require.InDelta(t, 22.05, usd, 1e-9)
}

func TestEstimateAndGuardrailAgree(t *testing.T) {
	t.Parallel()

	// One book, one model, one batch size: `estimate`'s full-llm scenario and
	// the run-time literary projection must land on the same figure. They can
	// only differ by the frozen prefix, which the run measures from the bible
	// and `estimate` takes from the configured constant — so here the constant
	// is set to what the measurement would give.
	const (
		chaptersCount = 20
		segsPerChap   = 30
		charsPerSeg   = 110
		prefixChars   = 10_500
	)

	m := Model{
		Per1000CharsUSD: 0.02,
		Tokens: TokenRates{
			InputPerMTokUSD: 3, OutputPerMTokUSD: 15, CacheReadPerMTokUSD: 0.30,
			PromptOverheadTokens: prefixChars / charsPerTokenFR,
			TokenMultiplier:      1.30,
		},
	}

	chapters := make([][]segment.Segment, chaptersCount)
	counts := make([]int, chaptersCount)
	shapes := make([]ChapterShape, chaptersCount)
	for i := range chapters {
		chapters[i] = makeSegments(segsPerChap, charsPerSeg)
		counts[i] = segsPerChap
		shapes[i] = ChapterShape{SourceChars: segsPerChap * charsPerSeg, Calls: 1}
	}
	segs := makeSegments(chaptersCount*segsPerChap, charsPerSeg)

	estimated := m.EstimateScenarios(chapters, 50, 0, 0)[2]
	guarded := m.Project(segs, LLMPass{
		Segments:    len(segs),
		Chars:       segment.SourceChars(segs),
		Calls:       CallsForChapters(counts, 50),
		PrefixChars: prefixChars,
		Chapters:    shapes,
	}, 0)

	require.Equal(t, estimated.LLMCalls, guarded.LLMCalls)
	require.InDelta(t, estimated.EstimatedUSD, guarded.EstimatedUSD, 1e-9)
}

func TestEstimateScenarios_HybridPricesOneCallPerSegment(t *testing.T) {
	t.Parallel()

	// The hybrid scenario describes NMT-mode escalation, where the HTTP
	// post-editor sends one segment per call. Pricing it with literary batching
	// made `estimate` promise a figure no hybrid run could ever be charged.
	m := Model{Tokens: TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, PromptOverheadTokens: 200}}

	chapters := [][]segment.Segment{makeSegments(100, 400), makeSegments(100, 400)}
	hybrid := m.EstimateScenarios(chapters, 50, 0.5, 0)[1]

	require.Equal(t, 100, hybrid.LLMSegments)
	require.Equal(t, hybrid.LLMSegments, hybrid.LLMCalls,
		"one call per escalated segment, as the post-editor makes them")

	// full-llm still batches, so it makes far fewer calls for twice the work.
	full := m.EstimateScenarios(chapters, 50, 0.5, 0)[2]
	require.Equal(t, 200, full.LLMSegments)
	require.Equal(t, 4, full.LLMCalls)
}

// chapterShapes builds n chapters of equal source length sharing totalCalls.
func chapterShapes(n, sourceChars, totalCalls int) []ChapterShape {
	shapes := make([]ChapterShape, n)
	for i := range shapes {
		calls := totalCalls / n
		if i < totalCalls%n {
			calls++
		}
		shapes[i] = ChapterShape{SourceChars: sourceChars, Calls: calls}
	}
	return shapes
}

func TestTokenRates_LongChaptersRepeatTheirBiggerPrefix(t *testing.T) {
	t.Parallel()

	rates := TokenRates{InputPerMTokUSD: 3, OutputPerMTokUSD: 15, CacheReadPerMTokUSD: 0.30, PromptOverheadTokens: 3000}
	base := LLMPass{Segments: 200, Chars: 80_000, Calls: 6}

	// Same book, same total calls and characters, two ways of describing it.
	// Averaging says every chapter is 40k; the truth is one long chapter that
	// is chunked into more calls and repeats its larger prefix in each of them.
	even := base
	even.Chapters = []ChapterShape{{SourceChars: 40_000, Calls: 3}, {SourceChars: 40_000, Calls: 3}}
	skewed := base
	skewed.Chapters = []ChapterShape{{SourceChars: 70_000, Calls: 5}, {SourceChars: 10_000, Calls: 1}}

	_, _, evenUSD := rates.Project(even)
	_, _, skewedUSD := rates.Project(skewed)
	require.Greater(t, skewedUSD, evenUSD,
		"a skewed book costs more than its average shape suggests")
}

func TestTokenRates_ZeroRatesProjectNothing(t *testing.T) {
	t.Parallel()

	in, out, usd := TokenRates{}.Project(LLMPass{Segments: 100, Chars: 40_000, Calls: 10})
	require.Zero(t, in)
	require.Zero(t, out)
	require.Zero(t, usd)
}
