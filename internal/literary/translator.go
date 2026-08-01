package literary

import (
	"bytes"
	"context"
	"encoding/json"
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

// TranslateChapter returns a translated copy of segs plus the LLM token usage.
// The current TargetText on each input segment is treated as the safe NMT draft
// fallback. The chapter is split into batches of BatchSize segments so that
// small models (≤8B) do not stop generating before all segments are done.
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
	for batchIdx, batch := range chunk(items, t.batchSize()) {
		if err := t.translateBatch(ctx, run, batchIdx, batch); err != nil {
			return nil, run.usage, err
		}
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

// translateBatch translates items in a single LLM call and merges the results
// into run.targets. Small models stop generating before finishing a long
// structured output, answering only the first few segments of the batch; when
// that happens the unanswered segments are retried on two halved batches until
// each batch holds a single segment. A segment still unanswered at that point
// keeps its NMT draft, as it did before any retry existed.
func (t *Translator) translateBatch(ctx context.Context, run *chapterRun, batchIdx int, items []requestSegment) error {
	resp, err := t.Chat.Chat(ctx, llm.ChatRequest{
		Model:      t.Model,
		MaxTokens:  t.maxTokens(),
		Messages:   composeMessages(run.bible, t.glossaryTerms(), encodePayload(items), run.prevCtx, run.nextCtx),
		JSONSchema: responseSchema,
	})
	if err != nil {
		return fmt.Errorf("unable to translate literary chapter %s batch %d, err: %w", run.chapterID, batchIdx, err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		return ErrEmptyResponse
	}

	run.usage.InputTokens += resp.InputTokens
	run.usage.OutputTokens += resp.OutputTokens
	run.usage.CacheReadTokens += resp.CacheReadInputTokens
	run.usage.CacheCreationTokens += resp.CacheCreationInputTokens

	decoded, err := decodeResponse(resp.Text)
	if err != nil {
		return fmt.Errorf("unable to parse literary response for chapter %s batch %d, err: %w", run.chapterID, batchIdx, err)
	}
	requested := make(map[string]bool, len(items))
	for _, item := range items {
		requested[item.ID] = true
	}
	for _, out := range decoded.Segments {
		// A batch only answers its own segments: an ID the model invented, or
		// echoed from an earlier batch, must not overwrite a good translation.
		if requested[out.ID] {
			run.targets[out.ID] = out.TargetText
		}
	}

	missing := missingItems(items, run.targets)
	if len(missing) == 0 || len(items) == 1 {
		return nil
	}

	// missing is a subset of items and len(items) >= 2 here, so both halves are
	// strictly shorter than items: the recursion terminates, after at most one
	// call per segment in the batch.
	half := (len(missing) + 1) / 2
	if err := t.translateBatch(ctx, run, batchIdx, missing[:half]); err != nil {
		return err
	}
	if half == len(missing) {
		return nil
	}
	return t.translateBatch(ctx, run, batchIdx, missing[half:])
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
