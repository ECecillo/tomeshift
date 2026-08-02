package cost

import (
	"github.com/ECecillo/ebooks-translator/internal/segment"
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
	// contextRunesPerCall is the preceding-context window each literary call
	// carries alongside its batch, matching literary.contextRunes.
	contextRunesPerCall = 1200.0

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

// prefixTokens returns the per-call cacheable prefix in tokens. Its frozen part
// is French prose (style guide, character voices, glossary) and converts at the
// French ratio; the chapter source folded in after it is English. A run
// measures the frozen part from the bible it loaded, and `estimate` falls back
// to the configured constant because it has no bible.
func (r TokenRates) prefixTokens(pass LLMPass) float64 {
	frozen := float64(r.PromptOverheadTokens)
	if pass.PrefixChars > 0 {
		frozen = float64(pass.PrefixChars) / charsPerTokenFR
	}
	return frozen + float64(pass.ChapterSourceChars)/charsPerTokenEN
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
	volatile := payload + float64(pass.Calls)*contextRunesPerCall/charsPerTokenFR

	output := float64(pass.Segments) * (charsPerSegment*r.outputExpansion()/charsPerTokenFR +
		jsonOverheadOutChars/charsPerTokenFR)

	prefix := r.prefixTokens(pass) * factor
	var prefixUSD float64
	if r.caches() {
		writes := min(max(pass.CacheWrites, 1), pass.Calls)
		reads := pass.Calls - writes
		prefixUSD = float64(writes)*prefix/1e6*r.InputPerMTokUSD*r.cacheWriteMultiplier() +
			float64(reads)*prefix/1e6*r.CacheReadPerMTokUSD
	} else {
		prefixUSD = float64(pass.Calls) * prefix / 1e6 * r.InputPerMTokUSD
	}

	inputTokens = int(float64(pass.Calls)*prefix + volatile*factor + 0.5)
	outputTokens = int(output*factor + 0.5)
	usd = prefixUSD + volatile*factor/1e6*r.InputPerMTokUSD + output*factor/1e6*r.OutputPerMTokUSD
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

	// The prefix carries the chapter source and is re-written to the cache once
	// per chapter, exactly as it is at run time. Only its frozen part differs:
	// `estimate` has no bible loaded and falls back to the configured constant.
	meanChapterChars := 0
	if len(chapters) > 0 {
		meanChapterChars = totalChars / len(chapters)
	}
	// full-llm is the literary pass: segments batched per chapter, the chapter
	// source in a cacheable prefix.
	literaryPass := func(segs, chars int, perChapter []int) LLMPass {
		return LLMPass{
			Segments: segs, Chars: chars,
			Calls:              CallsForChapters(perChapter, batchSize),
			ChapterSourceChars: meanChapterChars,
			CacheWrites:        len(perChapter),
		}
	}

	// hybrid is the NMT-mode escalation, and it is a different animal: the HTTP
	// post-editor sends one segment per call and carries neither the bible nor
	// the chapter source. Pricing it like the literary pass made `estimate`
	// disagree with what a hybrid run would actually be charged.
	postEditPass := func(segs, chars int) LLMPass {
		return LLMPass{Segments: segs, Chars: chars, Calls: segs}
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
