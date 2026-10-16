package cost

import (
	"github.com/ECecillo/tomeshift/internal/segment"
)

// Tokenisation heuristics. Deliberately coarse and stated in one place: for an
// exact figure you need the provider's tokenizer (Anthropic's count_tokens),
// which costs a network round-trip an upfront estimate should not make. They
// mirror the constants in scripts/cost_model.py.
const (
	// charsPerTokenEN is the English source ratio.
	charsPerTokenEN = 4.0
	// charsPerTokenFR is lower: accents and elisions fragment more.
	charsPerTokenFR = 3.5
	// jsonOverheadInChars / jsonOverheadOutChars are the per-segment cost of
	// the JSON envelope (keys, quotes, escapes) in the literary schema.
	jsonOverheadInChars  = 40.0
	jsonOverheadOutChars = 30.0

	// LiteraryContextRunes is the preceding-context window a literary call
	// carries alongside its batch, matching literary.contextRunes. It is
	// exported because the pipeline builds the same pass shape.
	LiteraryContextRunes = 1200

	defaultOutputExpansion      = 1.15
	defaultCacheWriteMultiplier = 1.25
	defaultTokenMultiplier      = 1.0
)

// TokenRates describes an LLM provider's token pricing.
type TokenRates struct {
	InputPerMTokUSD  float64 // USD per 1M input tokens
	OutputPerMTokUSD float64 // USD per 1M output tokens
	// CacheReadPerMTokUSD prices a prompt-cache hit. 0 means the provider does
	// not cache, so the stable prefix is paid at full input rate on every call
	// — the case for OpenAI-compatible hosts without a server-side cache.
	CacheReadPerMTokUSD float64
	// CacheWriteMultiplier is the premium on writing the cache (1.25 at
	// Anthropic). <= 0 uses that default.
	CacheWriteMultiplier float64
	// OutputExpansion is the FR/EN output token expansion. <= 0 uses 1.15.
	OutputExpansion float64
	// PromptOverheadTokens is the stable prefix, in tokens, repeated on every
	// call. It is the fallback for callers that cannot measure the real prefix:
	// `estimate` has no bible loaded.
	PromptOverheadTokens int
	// TokenMultiplier corrects the character heuristics for models whose
	// tokenizer is denser than the family they were derived from. Claude
	// Sonnet 5 and the Opus 4.7 family emit roughly 30% more tokens for the
	// same text at an identical sticker price, so 1.30 there. <= 0 uses 1.0.
	//
	// It scales token counts, never rates, and never applies to token counts
	// the provider reported itself.
	TokenMultiplier float64
	// OutputTokensPerSegment replaces the output heuristic with what past runs
	// of this model actually emitted, read off the spend ledger. 0 means no
	// measurement exists yet and the character heuristic stands.
	//
	// It exists because the heuristic is not a little off, it is off by a
	// factor: chapter 97 of vol33 projected 16,180 output tokens and consumed
	// 53,409 — 207 per segment against 63. Characters can predict the length of
	// a translation; they cannot predict how much a model spends producing it.
	//
	// Being a measurement, TokenMultiplier does not apply to it: that
	// correction exists to fix the character heuristic this bypasses.
	OutputTokensPerSegment float64
	// PrefixWrittenPerCall prices the cacheable prefix as written on every call
	// rather than written once and read back. A subprocess-per-call transport
	// (the Claude CLI) is the case it exists for: each invocation is an
	// independent session, so nothing is guaranteed to survive to be read.
	//
	// It is an upper bound. Two runs of the same chapter measured 46,970 then
	// 13,927 cache-creation tokens per call — a factor of 3.4 on the very term
	// this prices — so no single multiplier describes the transport. What it
	// buys is a bound in the right direction: without it the projection was 1.9x
	// to 3.7x too low on those runs, and a cap that undershoots does not bound
	// anything. See llm.RewritesPrefixPerCall for the measurements.
	PrefixWrittenPerCall bool
}

func (r TokenRates) outputExpansion() float64 {
	if r.OutputExpansion <= 0 {
		return defaultOutputExpansion
	}
	return r.OutputExpansion
}

func (r TokenRates) cacheWriteMultiplier() float64 {
	if r.CacheWriteMultiplier <= 0 {
		return defaultCacheWriteMultiplier
	}
	return r.CacheWriteMultiplier
}

func (r TokenRates) tokenMultiplier() float64 {
	if r.TokenMultiplier <= 0 {
		return defaultTokenMultiplier
	}
	return r.TokenMultiplier
}

func (r TokenRates) caches() bool { return r.CacheReadPerMTokUSD > 0 }

// IsZero reports whether the rates price nothing.
func (r TokenRates) IsZero() bool {
	return r.InputPerMTokUSD == 0 && r.OutputPerMTokUSD == 0
}

// frozenPrefixTokens returns the part of the prefix that is identical in every
// call of every chapter: French prose (style guide, character voices,
// glossary), so it converts at the French ratio. A run measures it from the
// bible it loaded; `estimate` falls back to the configured constant because it
// has no bible.
func (r TokenRates) frozenPrefixTokens(pass LLMPass) float64 {
	if pass.PrefixChars > 0 {
		return float64(pass.PrefixChars) / charsPerTokenFR
	}
	return float64(r.PromptOverheadTokens)
}

// prefixCost prices the cacheable prefix across the whole pass, chapter by
// chapter: each writes its own prefix once and reads it back on every further
// call — unless the transport reopens a session per call, in which case every
// call writes. It also returns the input tokens those prefixes account for,
// which do not depend on that distinction: the same tokens travel either way,
// only the rate they are billed at changes.
func (r TokenRates) prefixCost(pass LLMPass, factor float64) (tokens, usd float64) {
	frozen := r.frozenPrefixTokens(pass)

	chapters := pass.Chapters
	if len(chapters) == 0 {
		// No chapter source: one uniform prefix repeated across every call.
		chapters = []ChapterShape{{Calls: pass.Calls}}
	}

	for _, chapter := range chapters {
		if chapter.Calls <= 0 {
			continue
		}
		perCall := (frozen + float64(chapter.SourceChars)/charsPerTokenEN) * factor
		tokens += perCall * float64(chapter.Calls)

		if pass.Uncached || !r.caches() {
			usd += float64(chapter.Calls) * perCall / 1e6 * r.InputPerMTokUSD
			continue
		}
		if r.PrefixWrittenPerCall {
			// Every call writes, none reads back. Not the same thing as an
			// uncached provider: the write premium is still charged, and it is
			// the dominant term — 1.25x input on the whole prefix, per call.
			usd += float64(chapter.Calls) * perCall / 1e6 * r.InputPerMTokUSD * r.cacheWriteMultiplier()
			continue
		}
		usd += perCall / 1e6 * r.InputPerMTokUSD * r.cacheWriteMultiplier()
		usd += float64(chapter.Calls-1) * perCall / 1e6 * r.CacheReadPerMTokUSD
	}
	return tokens, usd
}

// Project returns the input tokens, output tokens and USD cost of the LLM stage
// described by pass. A pass that rewrites nothing, or that no rates are
// configured for, costs nothing.
//
// The payload and the output do not depend on the batch size: every segment is
// sent and re-translated exactly once. Only the prefix and the volatile context
// are repeated per call — which is where the batch size acts.
func (r TokenRates) Project(pass LLMPass) (inputTokens, outputTokens int, usd float64) {
	if pass.Segments <= 0 || pass.Calls <= 0 || r.IsZero() {
		return 0, 0, 0
	}

	factor := r.tokenMultiplier()
	charsPerSegment := float64(pass.Chars) / float64(pass.Segments)

	// Each request segment carries the English source and the French draft the
	// LLM is correcting, plus the JSON envelope.
	payload := float64(pass.Segments) * (charsPerSegment/charsPerTokenEN +
		charsPerSegment*r.outputExpansion()/charsPerTokenFR +
		jsonOverheadInChars/charsPerTokenEN)
	volatile := payload + float64(pass.Calls)*float64(pass.ContextRunes)/charsPerTokenFR

	// A measurement wins over the heuristic, and carries no factor: it is
	// already what the tokenizer counted.
	output := float64(pass.Segments) * (charsPerSegment*r.outputExpansion()/charsPerTokenFR +
		jsonOverheadOutChars/charsPerTokenFR) * factor
	if r.OutputTokensPerSegment > 0 {
		output = float64(pass.Segments) * r.OutputTokensPerSegment
	}

	prefixTokens, prefixUSD := r.prefixCost(pass, factor)

	inputTokens = int(prefixTokens + volatile*factor + 0.5)
	outputTokens = int(output + 0.5)
	usd = prefixUSD + volatile*factor/1e6*r.InputPerMTokUSD + output/1e6*r.OutputPerMTokUSD
	return inputTokens, outputTokens, usd
}

// UsageCost prices token counts the provider reported. Cache reads and cache
// writes are billed at their own rates; InputTokens is the uncached remainder.
func (r TokenRates) UsageCost(u TokenUsage) float64 {
	return float64(u.InputTokens)/1e6*r.InputPerMTokUSD +
		float64(u.CacheCreationTokens)/1e6*r.InputPerMTokUSD*r.cacheWriteMultiplier() +
		float64(u.CacheReadTokens)/1e6*r.CacheReadPerMTokUSD +
		float64(u.OutputTokens)/1e6*r.OutputPerMTokUSD
}

// CallsForChapters counts the LLM calls a book will make. Every chapter is
// chunked independently, so a 37-segment chapter consumes a whole call even at
// batchSize 50 — the total is therefore above the naive
// ceil(totalSegments/batchSize).
func CallsForChapters(segmentsPerChapter []int, batchSize int) int {
	if batchSize < 1 {
		batchSize = 1
	}
	calls := 0
	for _, count := range segmentsPerChapter {
		if count <= 0 {
			continue
		}
		calls += (count + batchSize - 1) / batchSize
	}
	return calls
}

// ScenarioEstimate is the projected cost of one named translation strategy. It
// is a Projection: a strategy is nothing more than a particular LLM pass over
// the same book.
type ScenarioEstimate struct {
	Name string `json:"name"`
	Projection
}

// EstimateScenarios projects the cost of translating a book under three
// strategies: NMT-only (no LLM), hybrid (a fraction escalated to the LLM), and
// full-LLM (every segment post-edited). escalationRate is the hybrid fraction
// in [0,1].
//
// chapters carries the book's segments grouped the way the pipeline chunks
// them, because prompt overhead is charged per call and calls are chunked per
// chapter. usedFreeChars is the monthly free allowance already consumed.
func (m Model) EstimateScenarios(
	chapters [][]segment.Segment,
	batchSize int,
	escalationRate float64,
	usedFreeChars int,
) []ScenarioEstimate {
	var (
		segments []segment.Segment
		counts   = make([]int, len(chapters))
	)
	for i, chapter := range chapters {
		counts[i] = len(chapter)
		segments = append(segments, chapter...)
	}
	total, totalChars := len(segments), segment.SourceChars(segments)
	escalationRate = min(max(escalationRate, 0), 1)

	// full-llm is the literary pass: segments batched per chapter, each
	// chapter's own source in a cacheable prefix. Only the frozen part of that
	// prefix differs from a run — `estimate` has no bible and falls back to the
	// configured constant.
	shapes := make([]ChapterShape, len(chapters))
	for i, chapter := range chapters {
		shapes[i] = ChapterShape{
			SourceChars: segment.SourceChars(chapter),
			Calls:       CallsForChapters([]int{len(chapter)}, batchSize),
		}
	}
	literaryPass := func(segs, chars int, perChapter []int) LLMPass {
		return LLMPass{
			Segments: segs, Chars: chars,
			Calls:        CallsForChapters(perChapter, batchSize),
			Chapters:     shapes,
			ContextRunes: LiteraryContextRunes,
		}
	}

	// hybrid is the NMT-mode escalation, and it is a different animal: the HTTP
	// post-editor sends one segment per call and carries neither the bible nor
	// the chapter source. Pricing it like the literary pass made `estimate`
	// disagree with what a hybrid run would actually be charged.
	postEditPass := func(segs, chars int) LLMPass {
		return LLMPass{Segments: segs, Chars: chars, Calls: segs, Uncached: true}
	}

	hybridSegs := int(float64(total)*escalationRate + 0.5)
	hybridChars := 0
	if total > 0 {
		hybridChars = int(float64(totalChars) * float64(hybridSegs) / float64(total))
	}

	return []ScenarioEstimate{
		{Name: "nmt-only", Projection: m.Project(segments, LLMPass{}, usedFreeChars)},
		{Name: "hybrid", Projection: m.Project(segments, postEditPass(hybridSegs, hybridChars), usedFreeChars)},
		{Name: "full-llm", Projection: m.Project(segments, literaryPass(total, totalChars, counts), usedFreeChars)},
	}
}
