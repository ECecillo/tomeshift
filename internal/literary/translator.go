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

// defaultBatchSize is the maximum number of segments per LLM call. Small models
// (≤8B) stop generating before completing long structured outputs, so chapters
// are split into batches of this size and calls are aggregated.
const defaultBatchSize = 20

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
	// BatchSize is the maximum number of segments sent per LLM call. Smaller
	// models stop generating before completing long structured outputs; batching
	// works around this. 0 uses defaultBatchSize.
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

// TranslateChapter returns a translated copy of segs plus the LLM token usage.
// The current TargetText on each input segment is treated as the safe NMT draft
// fallback. The chapter is split into batches of at most BatchSize segments so
// that small models (≤8B) do not stop generating before all segments are done.
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

	payload, states, err := t.buildPayload(segs)
	if err != nil {
		return nil, Usage{}, err
	}

	// Split the request payload into batches so small models do not stop early.
	batches := splitBatches(payload, t.batchSize())

	byID := make(map[string]string, len(segs))
	var totalUsage Usage

	for batchIdx, batchPayload := range batches {
		resp, err := t.Chat.Chat(ctx, llm.ChatRequest{
			Model:      t.Model,
			MaxTokens:  t.maxTokens(),
			Messages:   composeMessages(bible, t.glossaryTerms(), string(batchPayload), prevCtx, nextCtx),
			JSONSchema: responseSchema,
		})
		if err != nil {
			return nil, totalUsage, fmt.Errorf("unable to translate literary chapter %s batch %d, err: %w", chapterID, batchIdx, err)
		}
		if strings.TrimSpace(resp.Text) == "" {
			return nil, totalUsage, ErrEmptyResponse
		}

		totalUsage.InputTokens += resp.InputTokens
		totalUsage.OutputTokens += resp.OutputTokens
		totalUsage.CacheReadTokens += resp.CacheReadInputTokens
		totalUsage.CacheCreationTokens += resp.CacheCreationInputTokens

		decoded, err := decodeResponse(resp.Text)
		if err != nil {
			return nil, totalUsage, fmt.Errorf("unable to parse literary response for chapter %s batch %d, err: %w", chapterID, batchIdx, err)
		}

		for _, out := range decoded.Segments {
			byID[out.ID] = out.TargetText
		}
	}

	out := make([]segment.Segment, len(segs))
	copy(out, segs)
	for i := range out {
		state := states[out[i].ID]
		target, ok := byID[out[i].ID]
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

	return out, totalUsage, nil
}

func (t *Translator) buildPayload(segs []segment.Segment) ([]requestSegment, map[string]segmentState, error) {
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

	return items, states, nil
}

// splitBatches encodes items into JSON payloads of at most size segments each.
func splitBatches(items []requestSegment, size int) [][]byte {
	var batches [][]byte
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		chunk := struct {
			Segments []requestSegment `json:"segments"`
		}{Segments: items[i:end]}

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(chunk); err != nil {
			// Encoding a plain struct cannot fail; panic would be unreachable.
			panic(fmt.Sprintf("literary: unable to encode batch: %v", err))
		}
		batches = append(batches, bytes.TrimSpace(buf.Bytes()))
	}
	return batches
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
