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

	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/segment"
)

// defaultMaxTokens must comfortably fit a batch's JSON output.
const defaultMaxTokens = 16000

// defaultBatchSize is the number of segments in the first LLM call for a
// chapter. It is a throughput hint, not a correctness parameter: a response
// that omits some of the requested segments is retried on smaller batches, so
// an oversized value costs extra calls rather than untranslated text.
const defaultBatchSize = 10

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

// Usage aggregates token accounting for a chapter's LLM call. CacheRead and
// CacheCreation expose prompt-cache activity so callers can verify caching and
// price reads at the cache rate.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// contextRunes bounds the prose sent alongside a batch. The model needs enough
// to carry register, tense and pronouns across the boundary, not the whole
// neighbouring chapter — which would otherwise be re-sent on every batch.
const contextRunes = 1200

// chapterRun carries the state shared by every LLM call made for one chapter,
// so the recursive batch walk does not have to thread it through parameters.
type chapterRun struct {
	bible     narrative.Bible
	chapterID string
	prevCtx   string
	nextCtx   string
	// targets holds the translations returned so far, keyed by segment ID.
	targets map[string]string
	usage   Usage
}

// batch is one unit of work: the segments to translate plus the prose that
// surrounds them in the book.
type batch struct {
	idx   int
	items []requestSegment
	prev  string
	next  string
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

// followingContext returns the prose immediately after batch idx: the source of
// the next batch, or the chapter-level context for the last batch.
func (run *chapterRun) followingContext(idx int, batches [][]requestSegment) string {
	if idx == len(batches)-1 {
		return headRunes(run.nextCtx, contextRunes)
	}

	var sb strings.Builder
	for _, item := range batches[idx+1] {
		sb.WriteString(item.SourceText)
		sb.WriteByte('\n')
	}
	return headRunes(strings.TrimSpace(sb.String()), contextRunes)
}

// TranslateChapter returns a translated copy of segs plus the LLM token usage.
// The current TargetText on each input segment is treated as the safe NMT draft
// fallback. The chapter is split into batches of BatchSize segments so that
// small models (≤8B) do not stop generating before all segments are done.
//
// A batch that fails degrades rather than aborting the chapter: its segments
// keep their draft and stay StatusTranslated, which the caller can count. Only
// a chapter where no batch produced anything is returned as an error, so a
// systematic failure (bad credentials, wrong model) stays loud while a single
// bad batch does not discard the batches that already succeeded.
func (t *Translator) TranslateChapter(
	ctx context.Context,
	bible narrative.Bible,
	chapterID string,
	segs []segment.Segment,
	prevCtx string,
	nextCtx string,
) ([]segment.Segment, Usage, error) {
	if t.Chat == nil {
		return nil, Usage{}, ErrNilChatClient
	}
	if len(segs) == 0 {
		return nil, Usage{}, nil
	}

	items, states := t.buildPayload(segs)

	run := &chapterRun{
		bible:     bible,
		chapterID: chapterID,
		prevCtx:   prevCtx,
		nextCtx:   nextCtx,
		targets:   make(map[string]string, len(segs)),
	}
	batches := chunk(items, t.batchSize())
	var batchErrs []error
	for idx, batchItems := range batches {
		err := t.translateBatch(ctx, run, batch{
			idx:   idx,
			items: batchItems,
			prev:  run.precedingContext(idx, batches),
			next:  run.followingContext(idx, batches),
		})
		if err != nil {
			batchErrs = append(batchErrs, err)
		}
	}
	if len(missingItems(items, run.targets)) == len(items) {
		if len(batchErrs) > 0 {
			return nil, run.usage, errors.Join(batchErrs...)
		}
		return nil, run.usage, ErrEmptyResponse
	}

	out := make([]segment.Segment, len(segs))
	copy(out, segs)
	for i := range out {
		state := states[out[i].ID]
		target, ok := run.targets[out[i].ID]
		if !ok || strings.TrimSpace(target) == "" {
			out[i].TargetText = state.draft
			out[i].Status = segment.StatusTranslated
			continue
		}

		target = t.Glossary.Apply(target, state.subs)
		if !inlinePlaceholdersPreserved(state.source, target) {
			out[i].TargetText = state.draft
			out[i].Status = segment.StatusTranslated
			continue
		}

		out[i].TargetText = target
		out[i].Status = segment.StatusPostEdited
	}

	return out, run.usage, nil
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
		Messages:   composeMessages(run.bible, t.glossaryTerms(), encodePayload(b.items), b.prev, b.next),
		JSONSchema: responseSchema,
	})
	if err != nil {
		return fmt.Errorf("unable to translate literary chapter %s batch %d, err: %w", run.chapterID, b.idx, err)
	}

	run.usage.InputTokens += resp.InputTokens
	run.usage.OutputTokens += resp.OutputTokens
	run.usage.CacheReadTokens += resp.CacheReadInputTokens
	run.usage.CacheCreationTokens += resp.CacheCreationInputTokens

	if decoded, err := decodeResponse(resp.Text); err == nil {
		run.mergeTargets(b.items, decoded.Segments)
	}

	missing := missingItems(b.items, run.targets)
	if len(missing) == 0 || len(b.items) == 1 {
		return nil
	}

	// missing is a subset of b.items and len(b.items) >= 2 here, so both halves
	// are strictly shorter: the recursion terminates, after at most one call per
	// segment in the batch.
	half := (len(missing) + 1) / 2
	if err := t.translateBatch(ctx, run, b.withItems(missing[:half])); err != nil {
		return err
	}
	if half == len(missing) {
		return nil
	}
	return t.translateBatch(ctx, run, b.withItems(missing[half:]))
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

func (t *Translator) buildPayload(segs []segment.Segment) ([]requestSegment, map[string]segmentState) {
	items := make([]requestSegment, 0, len(segs))
	states := make(map[string]segmentState, len(segs))

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
		states[seg.ID] = segmentState{
			source: seg.SourceText,
			draft:  draft,
			subs:   subs,
		}
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

func (t *Translator) batchSize() int {
	if t.BatchSize > 0 {
		return t.BatchSize
	}
	return defaultBatchSize
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

// headRunes returns the first n runes of s, and tailRunes the last n.
func headRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimSpace(string(runes[:n]))
}

func tailRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimSpace(string(runes[len(runes)-n:]))
}

func fallbackText(seg segment.Segment) string {
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
