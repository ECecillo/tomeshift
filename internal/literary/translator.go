package literary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/segment"
)

const defaultMaxTokens = 4000

var inlineTokenPattern = regexp.MustCompile(`<x id="(\d+)"\s*/?>(?:\s*</x>)?`)

// Translator translates a chapter with series-bible and optional glossary
// context through a provider-agnostic chat client.
type Translator struct {
	Chat      llm.ChatClient
	Model     string
	MaxTokens int
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

// TranslateChapter returns a translated copy of segs. The current TargetText on
// each input segment is treated as the safe NMT draft fallback.
func (t *Translator) TranslateChapter(
	ctx context.Context,
	bible narrative.Bible,
	chapterID string,
	segs []segment.Segment,
	prevCtx string,
	nextCtx string,
) ([]segment.Segment, error) {
	if t.Chat == nil {
		return nil, ErrNilChatClient
	}
	if len(segs) == 0 {
		return nil, nil
	}

	payload, states, err := t.buildPayload(segs)
	if err != nil {
		return nil, err
	}

	resp, err := t.Chat.Chat(ctx, llm.ChatRequest{
		Model:     t.Model,
		MaxTokens: t.maxTokens(),
		Messages:  composeMessages(bible, t.glossaryTerms(), string(payload), prevCtx, nextCtx),
	})
	if err != nil {
		return nil, fmt.Errorf("unable to translate literary chapter %s, err: %w", chapterID, err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		return nil, ErrEmptyResponse
	}

	decoded, err := decodeResponse(resp.Text)
	if err != nil {
		return nil, fmt.Errorf("unable to parse literary response for chapter %s, err: %w", chapterID, err)
	}

	byID := make(map[string]string, len(decoded.Segments))
	for _, out := range decoded.Segments {
		byID[out.ID] = out.TargetText
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

	return out, nil
}

func (t *Translator) buildPayload(segs []segment.Segment) ([]byte, map[string]segmentState, error) {
	req := struct {
		Segments []requestSegment `json:"segments"`
	}{Segments: make([]requestSegment, 0, len(segs))}

	states := make(map[string]segmentState, len(segs))
	for _, seg := range segs {
		source := seg.SourceText
		draft := fallbackText(seg)
		var subs map[string]string
		if t.Glossary != nil {
			source, subs = t.Glossary.Protect(source)
		}

		req.Segments = append(req.Segments, requestSegment{
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

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, nil, fmt.Errorf("unable to encode literary chapter request, err: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), states, nil
}

func (t *Translator) maxTokens() int {
	if t.MaxTokens > 0 {
		return t.MaxTokens
	}
	return defaultMaxTokens
}

func (t *Translator) glossaryTerms() []glossary.Term {
	if t.Glossary == nil {
		return nil
	}
	return t.Glossary.Terms()
}

func decodeResponse(raw string) (responseBody, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()

	var out responseBody
	if err := dec.Decode(&out); err != nil {
		return responseBody{}, fmt.Errorf("unable to decode literary response JSON, err: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return responseBody{}, fmt.Errorf("unable to decode literary response JSON, err: %w", err)
	}
	if len(out.Segments) == 0 {
		return responseBody{}, ErrEmptyResponse
	}
	return out, nil
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
