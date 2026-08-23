package literary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/segment"
)

// defaultMaxTokens must comfortably fit a batch's JSON output.
const defaultMaxTokens = 16000

// DefaultBatchSize is the number of segments in the first LLM call for a
// chapter. It is a throughput hint, not a correctness parameter: a response
// that omits some of the requested segments is retried on smaller batches, so
// an oversized value costs extra calls rather than untranslated text.
//
// It is deliberately large. Every call repeats the stable prefix (bible plus
// glossary), so splitting a chapter that the model could have answered in one
// pass multiplies input tokens for nothing: a 40-segment chapter measured
// against a local 7B model cost 4284 input tokens over four calls versus 2288
// in one, with no difference in output and no retry triggered. Prompt caching
// absorbs that on Anthropic but not on OpenAI-compatible hosts.
const DefaultBatchSize = 50

var inlineTokenPattern = regexp.MustCompile(`<x id="(\d+)"\s*/?>(?:\s*</x>)?`)

// responseSchema constrains the chat model to well-formed JSON, sidestepping
// the escaping fragility of free-form JSON-in-prose for literary output.
var responseSchema = json.RawMessage(`{"type":"object","properties":{"segments":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"target_text":{"type":"string"}},"required":["id","target_text"],"additionalProperties":false}}},"required":["segments"],"additionalProperties":false}`)

// Translator translates a chapter with series-bible and optional glossary
// context through a provider-agnostic chat client.
type Translator struct {
	Chat      llm.ChatClient
	Model     string
	MaxTokens int
	// BatchSize is the number of segments sent in the first LLM call per
	// chapter. Smaller models stop generating before completing long structured
	// outputs; incomplete responses are retried on halved batches regardless of
	// this value. 0 uses defaultBatchSize.
	BatchSize int
	Glossary  *glossary.Glossary
}

type requestSegment struct {
	ID         string `json:"id"`
	SourceText string `json:"source_text"`
	DraftText  string `json:"draft_text"`
}

type responseBody struct {
	Segments []responseSegment `json:"segments"`
}

type responseSegment struct {
	ID         string `json:"id"`
	TargetText string `json:"target_text"`
}

type segmentState struct {
	source string
	draft  string
	subs   map[string]string
}

// Outcome is what a chapter's pass produced besides the translations
// themselves.
type Outcome struct {
	Usage Usage
	// Degraded holds the failures of batches that did not stop the chapter:
	// their segments kept their NMT draft while other batches succeeded. It is
	// not an error — the chapter produced output — but it is the only record of
	// why part of it went unrefined, and the named refusal and truncation
	// errors are worth nothing if they are dropped here.
	Degraded []error
}

// Usage aggregates token accounting for a chapter's LLM call. CacheRead and
// CacheCreation expose prompt-cache activity so callers can verify caching and
// price reads at the cache rate.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// contextRunes bounds the French already produced that is re-sent ahead of a
// batch. The model needs enough to carry register, tense and pronouns across
// the seam, not the whole chapter — the chapter's source is in the cached
// prefix, so only the translation of the batch before it has to be repeated.
const contextRunes = 1200

// chapterRun carries the state shared by every LLM call made for one chapter,
// so the recursive batch walk does not have to thread it through parameters.
type chapterRun struct {
	bible     narrative.Bible
	chapterID string
	// source is the chapter's full source text, sent once in the cacheable
	// prefix rather than as windows around each batch.
	source  string
	prevCtx string
	// targets holds the translations returned so far, keyed by segment ID.
	targets map[string]string
	usage   Usage
}

// batch is one unit of work: the segments to translate plus the French that
// immediately precedes them.
type batch struct {
	idx   int
	items []requestSegment
	prev  string
}

// withItems returns b narrowed to items, keeping its surrounding prose so a
// retry sees the same context as the batch it was split from.
func (b batch) withItems(items []requestSegment) batch {
	b.items = items
	return b
}

// precedingContext returns the prose immediately before batch idx: the
// translation just produced for the previous batch, or — for the first batch —
// the chapter-level context supplied by the caller. Batches are translated in
// order precisely so this is available.
func (run *chapterRun) precedingContext(idx int, batches [][]requestSegment) string {
	if idx == 0 {
		return tailRunes(run.prevCtx, contextRunes)
	}

	var sb strings.Builder
	for _, item := range batches[idx-1] {
		if target := run.targets[item.ID]; strings.TrimSpace(target) != "" {
			sb.WriteString(target)
			sb.WriteByte('\n')
		}
	}
	return tailRunes(strings.TrimSpace(sb.String()), contextRunes)
}

// TranslateChapter rewrites segs in place and reports the LLM token usage. The
// TargetText each segment arrives with is treated as the safe NMT draft
// fallback. The chapter is split into batches of BatchSize segments so that
// small models (≤8B) do not stop generating before all segments are done.
//
// It takes pointers because the caller owns the segments — they live in the
// chapter the pipeline is going to recompose. Returning translated copies would
// only oblige every caller to write them back by index.
//
// A batch that fails degrades rather than aborting the chapter: its segments
// keep their draft and stay StatusTranslated, which the caller can count. Only
// a chapter where no batch produced anything is returned as an error, so a
// systematic failure (bad credentials, wrong model) stays loud while a single
// bad batch does not discard the batches that already succeeded. Nothing is
// written in that case: the segments keep the drafts they came in with.
func (t *Translator) TranslateChapter(
	ctx context.Context,
	bible narrative.Bible,
	chapterID string,
	segs []*segment.Segment,
	prevCtx string,
	chapterSource string,
) (Outcome, error) {
	if t.Chat == nil {
		return Outcome{}, ErrNilChatClient
	}
	if len(segs) == 0 {
		return Outcome{}, nil
	}

	items, states := t.buildPayload(segs)

	run := &chapterRun{
		bible:     bible,
		chapterID: chapterID,
		source:    chapterSource,
		prevCtx:   prevCtx,
		targets:   make(map[string]string, len(segs)),
	}
	batches := chunk(items, t.batchSize())
	var batchErrs []error
	for idx, batchItems := range batches {
		err := t.translateBatch(ctx, run, batch{
			idx:   idx,
			items: batchItems,
			prev:  run.precedingContext(idx, batches),
		})
		if err != nil {
			batchErrs = append(batchErrs, err)
		}
	}
	if len(missingItems(items, run.targets)) == len(items) {
		if len(batchErrs) > 0 {
			return Outcome{Usage: run.usage}, errors.Join(batchErrs...)
		}
		return Outcome{Usage: run.usage}, ErrEmptyResponse
	}

	for i, seg := range segs {
		state := states[i]

		// An unanswered segment reads as the empty string here, which is the
		// same predicate missingItems retries on: "retried" and "fell back to
		// the draft" stay one condition.
		target := t.Glossary.Apply(run.targets[seg.ID], state.subs)
		if strings.TrimSpace(target) == "" || !inlinePlaceholdersPreserved(state.source, target) {
			seg.TargetText = state.draft
			seg.Status = segment.StatusTranslated
			continue
		}

		seg.TargetText = target
		seg.Status = segment.StatusPostEdited
	}

	return Outcome{Usage: run.usage, Degraded: batchErrs}, nil
}

// translateBatch translates b in a single LLM call and merges the results into
// run.targets.
//
// An unusable response is not an error, because a smaller batch usually fixes
// it: a small model that stops generating answers only the first few segments,
// and one that stops mid-JSON produces output that will not parse at all. Both
// leave segments unanswered, and both are retried on two halved batches down to
// a single segment. Whatever is still unanswered keeps its NMT draft.
//
// Transport and provider errors do fail the batch — a smaller batch will not
// fix bad credentials — so they propagate to TranslateChapter, which decides
// whether the chapter as a whole produced anything.
func (t *Translator) translateBatch(ctx context.Context, run *chapterRun, b batch) error {
	resp, err := t.Chat.Chat(ctx, llm.ChatRequest{
		Model:      t.Model,
		MaxTokens:  t.maxTokens(),
		Messages:   composeMessages(run.bible, t.glossaryTerms(), run.source, encodePayload(b.items), b.prev),
		JSONSchema: responseSchema,
	})
	// Usage first, before the error check, for the same reason refine() does it:
	// a reply that came back empty was still generated and still billed. A
	// reasoning model that spent its whole budget thinking consumed every one of
	// those tokens.
	run.usage.InputTokens += resp.InputTokens
	run.usage.OutputTokens += resp.OutputTokens
	run.usage.CacheReadTokens += resp.CacheReadInputTokens
	run.usage.CacheCreationTokens += resp.CacheCreationInputTokens

	// An empty reply belongs with the unusable ones, not with the fatal ones.
	// The taxonomy above had two cases — unusable content, retried on halves;
	// transport and credential errors, fatal — and a reasoning model exposed a
	// third that had been filed under the wrong one: max_tokens is shared
	// between thinking and answering, so a batch large enough to think about is
	// a batch too large to answer. Halving is exactly the fix, and treating it
	// as fatal left 100 of 258 segments on their raw draft with the run still
	// reporting success.
	if err != nil && !errors.Is(err, llm.ErrEmptyResponse) {
		return fmt.Errorf("unable to translate literary chapter %s batch %d, err: %w", run.chapterID, b.idx, err)
	}

	// A refusal and a truncated reply both arrive as HTTP 200 with unusable
	// content, and both used to surface only as "decodeResponse failed, retry
	// on halved batches". Naming them keeps the two apart in the logs.
	if resp.StopReason == llm.StopReasonRefusal {
		return fmt.Errorf("unable to translate literary chapter %s batch %d (%s), err: %w",
			run.chapterID, b.idx, resp.StopDetailsSummary(), ErrProviderRefusal)
	}

	if decoded, err := decodeResponse(resp.Text); err == nil {
		run.mergeTargets(b.items, decoded.Segments)
	}

	missing := missingItems(b.items, run.targets)
	if len(missing) == 0 {
		return nil
	}
	if len(b.items) == 1 {
		// Nothing left to split, and the segment still has no translation. Say
		// so whatever the provider claimed: a reply that stopped cleanly but
		// came back malformed, empty, or answering a different id leaves the
		// segment on its draft just as a truncation does, and reporting only
		// the truncation would keep the commoner case silent.
		reason := resp.StopReason
		if reason == "" {
			reason = "no usable segment in the response"
		}
		return fmt.Errorf("unable to translate literary chapter %s batch %d (%s): %s, err: %w",
			run.chapterID, b.idx, resp.StopDetailsSummary(), reason, ErrIncompleteResponse)
	}

	// missing is a subset of b.items and len(b.items) >= 2 here, so both halves
	// are strictly shorter: the recursion terminates, after at most one call per
	// segment in the batch. Both halves are attempted even if the first fails —
	// a bad half must not discard the segments the other one could still get.
	half := (len(missing) + 1) / 2
	firstErr := t.translateBatch(ctx, run, b.withItems(missing[:half]))
	if half == len(missing) {
		return firstErr
	}
	return errors.Join(firstErr, t.translateBatch(ctx, run, b.withItems(missing[half:])))
}

// mergeTargets records the answers for items. A batch only answers its own
// segments: an ID the model invented, or echoed from an earlier batch, must not
// overwrite a good translation.
func (run *chapterRun) mergeTargets(items []requestSegment, answers []responseSegment) {
	requested := make(map[string]bool, len(items))
	for _, item := range items {
		requested[item.ID] = true
	}
	for _, answer := range answers {
		if requested[answer.ID] {
			run.targets[answer.ID] = answer.TargetText
		}
	}
}

// missingItems returns the items that targets has no usable translation for.
// The emptiness test matches the draft-fallback condition in TranslateChapter,
// so "retried" and "fell back to the draft" stay the same predicate.
func missingItems(items []requestSegment, targets map[string]string) []requestSegment {
	missing := make([]requestSegment, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(targets[item.ID]) == "" {
			missing = append(missing, item)
		}
	}
	return missing
}

// buildPayload renders the request items and, alongside them, what has to be
// remembered to interpret the answers: the unprotected source, the draft to
// fall back to, and the glossary substitutions to undo. Both slices are in segs
// order, so a state is found by position rather than by segment ID.
func (t *Translator) buildPayload(segs []*segment.Segment) ([]requestSegment, []segmentState) {
	items := make([]requestSegment, 0, len(segs))
	states := make([]segmentState, 0, len(segs))

	for _, seg := range segs {
		source := seg.SourceText
		draft := fallbackText(seg)
		var subs map[string]string
		if t.Glossary != nil {
			source, subs = t.Glossary.Protect(source)
		}

		items = append(items, requestSegment{
			ID:         seg.ID,
			SourceText: source,
			DraftText:  draft,
		})
		states = append(states, segmentState{
			source: seg.SourceText,
			draft:  draft,
			subs:   subs,
		})
	}

	return items, states
}

// chunk splits items into batches of at most size segments. A size below 1
// yields a single batch, matching the "no batching" behaviour.
func chunk(items []requestSegment, size int) [][]requestSegment {
	if size < 1 {
		size = len(items)
	}
	batches := make([][]requestSegment, 0, (len(items)+size-1)/size)
	for i := 0; i < len(items); i += size {
		batches = append(batches, items[i:min(i+size, len(items))])
	}
	return batches
}

// encodePayload renders items as the JSON request body. Encoding a struct of
// plain strings cannot fail, so no error is plumbed through the callers.
func encodePayload(items []requestSegment) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct {
		Segments []requestSegment `json:"segments"`
	}{Segments: items})
	return strings.TrimSpace(buf.String())
}

func (t *Translator) maxTokens() int {
	if t.MaxTokens > 0 {
		return t.MaxTokens
	}
	return defaultMaxTokens
}

// ResolveBatchSize turns a configured batch size into the one that will be
// used. It is exported because the cost projection has to count the calls the
// run will actually make, and `estimate` projects without building a
// Translator at all.
func ResolveBatchSize(configured int) int {
	if configured > 0 {
		return configured
	}
	return DefaultBatchSize
}

func (t *Translator) batchSize() int { return ResolveBatchSize(t.BatchSize) }

// EffectiveBatchSize reports the batch size this translator will actually use.
func (t *Translator) EffectiveBatchSize() int { return t.batchSize() }

// StablePrefixChars measures the cacheable prefix this translator repeats on
// every call, excluding the per-chapter source. The budget guardrail measures
// it rather than trusting a configured constant: the prefix is the dominant
// share of input cost, and it grows with the bible.
func (t *Translator) StablePrefixChars(bible narrative.Bible) int {
	return utf8.RuneCountInString(stablePrefix(bible, t.glossaryTerms(), ""))
}

func (t *Translator) glossaryTerms() []glossary.Term {
	if t.Glossary == nil {
		return nil
	}
	return t.Glossary.Terms()
}

func decodeResponse(raw string) (responseBody, error) {
	// Models often wrap the JSON in prose or a ```json code fence, or append a
	// note after it. Isolate the first balanced JSON object and decode leniently
	// (unknown extra fields are ignored).
	var out responseBody
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &out); err != nil {
		return responseBody{}, fmt.Errorf("unable to decode literary response JSON (len=%d), err: %w", len(raw), err)
	}
	if len(out.Segments) == 0 {
		return responseBody{}, ErrEmptyResponse
	}
	return out, nil
}

// extractJSONObject returns the first balanced {...} object in raw, ignoring
// braces inside JSON strings. It tolerates code fences and trailing prose.
func extractJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	if start == -1 {
		return raw
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return raw[start:]
}

// tailRunes returns the last n runes of s.
func tailRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimSpace(string(runes[len(runes)-n:]))
}

func fallbackText(seg *segment.Segment) string {
	if seg.TargetText != "" {
		return seg.TargetText
	}
	return seg.SourceText
}

func inlinePlaceholdersPreserved(before, after string) bool {
	a := inlineTokenIDs(before)
	b := inlineTokenIDs(after)
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func inlineTokenIDs(text string) []string {
	matches := inlineTokenPattern.FindAllStringSubmatch(text, -1)
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match[1])
	}
	return ids
}
