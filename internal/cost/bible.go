package cost

// This file prices the `series-bible` map-reduce. It lives in the cost package
// for the same reason everything else here does: rates are applied in exactly
// one place, so what a command reports and what a guardrail enforces cannot
// drift apart.

// Bounded reply sizes for the three stages, in tokens.
//
// The two summary figures are measurements, quoted from
// narrative.defaultSummaryMaxTokens: ~274 tokens for a chapter summary, ~400
// for a volume synopsis. The style pass has no equivalent measurement, so it is
// projected at its own ceiling (narrative.defaultProfilerMaxTokens) — the only
// defensible bound for an unmeasured reply, and the direction a guardrail
// should err in.
const (
	bibleChapterSummaryTokens = 274
	bibleVolumeSynopsisTokens = 400
	bibleStyleTokens          = 16000
)

// SummaryPass describes an LLM pass whose reply is bounded rather than
// proportional to its input.
//
// LLMPass cannot express this. Its output term is charsPerSegment ×
// outputExpansion — correct for translation, where the reply restates the
// input, and badly wrong here: it would price a 19,000-character chapter as if
// the summary came back at 19,000 characters. A projection that wrong is worse
// than none, because a cap compares against it.
//
// There is no prefix term either. Every call carries a different chapter behind
// a system prompt of ~100 tokens — far below any provider's cacheable minimum —
// so unlike the literary pass there is nothing to cache and nothing to repeat.
type SummaryPass struct {
	// Calls is how many completions the pass makes.
	Calls int
	// InputChars is the total source text sent across those calls.
	InputChars int
	// InputIsFrench selects the character-to-token ratio. The map step sends
	// English source; the reduce step sends the French summaries the map step
	// produced, which fragment more.
	InputIsFrench bool
	// OutputTokensPerCall is the bounded reply size.
	OutputTokensPerCall int
}

// ProjectSummary prices a bounded-output pass.
//
// Unlike TokenRates.Project it reports token counts even when no rates are
// configured. That is deliberate: `series-bible` is the one command routinely
// run against a subscription-backed provider, where there is no marginal dollar
// and the token count is the only figure that means anything.
func (r TokenRates) ProjectSummary(pass SummaryPass) (inputTokens, outputTokens int, usd float64) {
	if pass.Calls <= 0 {
		return 0, 0, 0
	}

	ratio := charsPerTokenEN
	if pass.InputIsFrench {
		ratio = charsPerTokenFR
	}
	factor := r.tokenMultiplier()

	in := float64(pass.InputChars) / ratio * factor
	out := float64(pass.Calls) * float64(pass.OutputTokensPerCall) * factor

	return int(in + 0.5), int(out + 0.5),
		in/1e6*r.InputPerMTokUSD + out/1e6*r.OutputPerMTokUSD
}

// VolumeShape is one volume's contribution to a bible build.
type VolumeShape struct {
	ID string
	// Chapters counts the NON-EMPTY chapters, which is what the map step bills:
	// blank spine documents stay numbered so chapter IDs line up with the
	// translation pipeline's, but they are never sent.
	Chapters int
	// SourceChars is the text of those chapters.
	SourceChars int
}

// BibleShape is what a `series-bible` run is about to send.
type BibleShape struct {
	// StyleSampleChars is the single style pass: the first --samples chapters of
	// every volume, concatenated into ONE request. It is separate from the
	// volumes below because it scales with --samples rather than with the series.
	StyleSampleChars int
	Volumes          []VolumeShape
}

// BibleStage is one stage's share of the projection.
type BibleStage struct {
	Name         string  `json:"name"`
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	EstimatedUSD float64 `json:"estimated_usd"`
}

// BibleProjection is the projected cost of building a series bible.
type BibleProjection struct {
	Volumes      int          `json:"volumes"`
	Chapters     int          `json:"chapters"`
	Stages       []BibleStage `json:"stages"`
	Calls        int          `json:"calls"`
	InputTokens  int          `json:"input_tokens"`
	OutputTokens int          `json:"output_tokens"`
	EstimatedUSD float64      `json:"estimated_usd"`
}

// ProjectSeriesBible projects the three stages of a bible build: one style pass
// over sampled chapters, one summary per non-empty chapter, and one synopsis per
// volume.
//
// It is a projection of the whole job, not of one volume. A bible build is a
// single invocation spanning the entire series, and its dominant term is the
// chapter count — 1090 chapters is 1090 calls whatever --samples is set to.
func (m Model) ProjectSeriesBible(shape BibleShape) BibleProjection {
	chapters := 0
	sourceChars := 0
	for _, volume := range shape.Volumes {
		chapters += volume.Chapters
		sourceChars += volume.SourceChars
	}

	projection := BibleProjection{Volumes: len(shape.Volumes), Chapters: chapters}
	add := func(name string, pass SummaryPass) {
		in, out, usd := m.Tokens.ProjectSummary(pass)
		if pass.Calls <= 0 {
			return
		}
		projection.Stages = append(projection.Stages, BibleStage{
			Name: name, Calls: pass.Calls,
			InputTokens: in, OutputTokens: out, EstimatedUSD: usd,
		})
		projection.Calls += pass.Calls
		projection.InputTokens += in
		projection.OutputTokens += out
		projection.EstimatedUSD += usd
	}

	// One call, and the one that --samples controls. Its input is every sampled
	// chapter of every volume at once, so it grows with the series: at 33
	// volumes, --samples 8 puts roughly a million tokens in a single request.
	//
	// It is gated on there being something to sample: with no non-empty chapter
	// anywhere, the style pass is not made at all — Profiler.Seed rejects an
	// empty sample set rather than calling the provider.
	if shape.StyleSampleChars > 0 {
		add("style", SummaryPass{
			Calls: 1, InputChars: shape.StyleSampleChars,
			OutputTokensPerCall: bibleStyleTokens,
		})
	}

	add("map", SummaryPass{
		Calls: chapters, InputChars: sourceChars,
		OutputTokensPerCall: bibleChapterSummaryTokens,
	})

	// The reduce step reads back what the map step wrote, so its input is
	// derived from the projected summaries rather than from the source.
	add("reduce", SummaryPass{
		Calls:               len(shape.Volumes),
		InputChars:          int(float64(chapters) * bibleChapterSummaryTokens * charsPerTokenFR),
		InputIsFrench:       true,
		OutputTokensPerCall: bibleVolumeSynopsisTokens,
	})

	return projection
}
