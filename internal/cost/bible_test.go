package cost_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/cost"
)

func rates() cost.TokenRates {
	return cost.TokenRates{InputPerMTokUSD: 1, OutputPerMTokUSD: 5}
}

func TestProjectSummary_OutputDoesNotScaleWithInput(t *testing.T) {
	t.Parallel()

	// The reason this type exists. LLMPass prices output as input x expansion,
	// which is right for translation and wrong for summarisation: a chapter
	// summary is a few hundred tokens whether the chapter is two pages or forty.
	small := cost.SummaryPass{Calls: 10, InputChars: 10_000, OutputTokensPerCall: 274}
	large := cost.SummaryPass{Calls: 10, InputChars: 1_000_000, OutputTokensPerCall: 274}

	_, smallOut, _ := rates().ProjectSummary(small)
	largeIn, largeOut, _ := rates().ProjectSummary(large)

	require.Equal(t, smallOut, largeOut, "output is bounded per call, not proportional to input")
	require.Equal(t, 250_000, largeIn, "input still scales: 1,000,000 chars at 4 chars/token")
	require.Equal(t, 2740, largeOut)
}

func TestProjectSummary_ReportsTokensWithoutRates(t *testing.T) {
	t.Parallel()

	// `series-bible` is the command routinely run against a subscription-backed
	// provider, where there is no marginal dollar and the token count is the
	// only figure that means anything. Returning zeros here would hide the
	// answer exactly where it is needed.
	in, out, usd := cost.TokenRates{}.ProjectSummary(
		cost.SummaryPass{Calls: 4, InputChars: 4000, OutputTokensPerCall: 100})

	require.Equal(t, 1000, in)
	require.Equal(t, 400, out)
	require.Zero(t, usd)
}

func TestProjectSummary_FrenchInputFragmentsMore(t *testing.T) {
	t.Parallel()

	// The reduce step reads back the French summaries the map step wrote, and
	// French converts at a lower chars-per-token ratio than English source.
	english, _, _ := rates().ProjectSummary(
		cost.SummaryPass{Calls: 1, InputChars: 7000, OutputTokensPerCall: 1})
	french, _, _ := rates().ProjectSummary(
		cost.SummaryPass{Calls: 1, InputChars: 7000, InputIsFrench: true, OutputTokensPerCall: 1})

	require.Equal(t, 1750, english)
	require.Equal(t, 2000, french)
}

func TestProjectSummary_TokenMultiplierScalesBothSides(t *testing.T) {
	t.Parallel()

	dense := rates()
	dense.TokenMultiplier = 1.30

	pass := cost.SummaryPass{Calls: 2, InputChars: 8000, OutputTokensPerCall: 100}
	baseIn, baseOut, baseUSD := rates().ProjectSummary(pass)
	denseIn, denseOut, denseUSD := dense.ProjectSummary(pass)

	require.Equal(t, int(float64(baseIn)*1.30), denseIn)
	require.Equal(t, int(float64(baseOut)*1.30), denseOut)
	require.Greater(t, denseUSD, baseUSD)
}

func TestProjectSummary_NoCalls(t *testing.T) {
	t.Parallel()

	in, out, usd := rates().ProjectSummary(cost.SummaryPass{InputChars: 10_000})
	require.Zero(t, in)
	require.Zero(t, out)
	require.Zero(t, usd)
}

func TestProjectSeriesBible_CountsOneCallPerChapterPlusOnePerVolumePlusStyle(t *testing.T) {
	t.Parallel()

	model := cost.Model{Tokens: rates()}
	projection := model.ProjectSeriesBible(cost.BibleShape{
		StyleSampleChars: 40_000,
		Volumes: []cost.VolumeShape{
			{ID: "vol1", Chapters: 68, SourceChars: 472_509},
			{ID: "vol2", Chapters: 72, SourceChars: 707_531},
		},
	})

	require.Equal(t, 2, projection.Volumes)
	require.Equal(t, 140, projection.Chapters)
	// 1 style + 140 map + 2 reduce. The chapter count dominates, which is the
	// point the projection exists to make visible.
	require.Equal(t, 143, projection.Calls)

	require.Len(t, projection.Stages, 3)
	byName := map[string]cost.BibleStage{}
	for _, stage := range projection.Stages {
		byName[stage.Name] = stage
	}
	require.Equal(t, 1, byName["style"].Calls)
	require.Equal(t, 140, byName["map"].Calls)
	require.Equal(t, 2, byName["reduce"].Calls)

	// The reduce step reads the summaries the map step produced, so its input is
	// derived from them rather than from the source text.
	require.Positive(t, byName["reduce"].InputTokens)
	require.Less(t, byName["reduce"].InputTokens, byName["map"].InputTokens)

	require.Equal(t,
		byName["style"].InputTokens+byName["map"].InputTokens+byName["reduce"].InputTokens,
		projection.InputTokens)
	require.InDelta(t,
		byName["style"].EstimatedUSD+byName["map"].EstimatedUSD+byName["reduce"].EstimatedUSD,
		projection.EstimatedUSD, 1e-9)
}

func TestProjectSeriesBible_StyleCallGrowsWithTheSeries(t *testing.T) {
	t.Parallel()

	// --samples looks like a per-volume knob but the style pass concatenates
	// every sampled chapter of every volume into ONE request, so its input
	// scales with the series. This is what the command warns about.
	model := cost.Model{Tokens: rates()}

	few := model.ProjectSeriesBible(cost.BibleShape{
		StyleSampleChars: 40_000,
		Volumes:          []cost.VolumeShape{{Chapters: 1, SourceChars: 1000}},
	})
	many := model.ProjectSeriesBible(cost.BibleShape{
		StyleSampleChars: 3_866_103,
		Volumes:          []cost.VolumeShape{{Chapters: 1, SourceChars: 1000}},
	})

	require.Equal(t, few.Calls, many.Calls, "same number of calls")
	require.Greater(t, many.InputTokens, few.InputTokens+900_000,
		"but one of them carries a million tokens")
}

func TestProjectSeriesBible_EmptySeries(t *testing.T) {
	t.Parallel()

	projection := cost.Model{Tokens: rates()}.ProjectSeriesBible(cost.BibleShape{})

	require.Zero(t, projection.Calls)
	require.Zero(t, projection.EstimatedUSD)
	require.Empty(t, projection.Stages)
}
