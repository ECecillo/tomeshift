package literary_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/literary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/segment"
)

// stubChat records every request and answers with replies in order; the last
// reply repeats once the slice is exhausted.
type stubChat struct {
	replies []llm.ChatResponse
	reqs    []llm.ChatRequest
}

func newStubChat(texts ...string) *stubChat {
	replies := make([]llm.ChatResponse, 0, len(texts))
	for _, text := range texts {
		replies = append(replies, llm.ChatResponse{Text: text})
	}
	return &stubChat{replies: replies}
}

func (s *stubChat) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	s.reqs = append(s.reqs, req)
	return s.replies[min(len(s.reqs)-1, len(s.replies)-1)], nil
}

func (s *stubChat) lastReq() llm.ChatRequest {
	return s.reqs[len(s.reqs)-1]
}

// echoChat answers each batch with a target text for every requested segment
// ID. maxSegments caps how many segments a single response carries, simulating
// a small model that stops generating before finishing the batch; 0 answers the
// whole batch.
type echoChat struct {
	maxSegments int
	// truncateOver cuts the response off mid-JSON when the batch holds more than
	// this many segments, as a model hitting its output limit does; 0 disables.
	truncateOver int
	// errOnCall makes the nth call (1-based) fail like a transport error would;
	// 0 disables.
	errOnCall int
	// batches records the segment IDs requested by each call, in order.
	batches [][]string
	reqs    []llm.ChatRequest
}

func (e *echoChat) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	ids, err := requestedIDs(req)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	e.batches = append(e.batches, ids)
	e.reqs = append(e.reqs, req)

	if e.errOnCall == len(e.reqs) {
		return llm.ChatResponse{}, errors.New("provider unavailable")
	}
	if e.truncateOver > 0 && len(ids) > e.truncateOver {
		// Cut mid-object: the JSON cannot be parsed at all.
		return llm.ChatResponse{
			Text:        fmt.Sprintf(`{"segments":[{"id":%q,"target_`, ids[0]),
			InputTokens: 100, OutputTokens: 10,
		}, nil
	}

	answered := ids
	if e.maxSegments > 0 && len(answered) > e.maxSegments {
		answered = answered[:e.maxSegments]
	}
	body := struct {
		Segments []map[string]string `json:"segments"`
	}{Segments: make([]map[string]string, 0, len(answered))}
	for _, id := range answered {
		body.Segments = append(body.Segments, map[string]string{"id": id, "target_text": "traduction " + id})
	}

	text, err := json.Marshal(body)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	return llm.ChatResponse{
		Text:                     string(text),
		InputTokens:              100,
		OutputTokens:             10,
		CacheReadInputTokens:     1,
		CacheCreationInputTokens: 2,
	}, nil
}

// requestedIDs extracts the segment IDs from the JSON payload the translator
// embeds at the end of the volatile message.
func requestedIDs(req llm.ChatRequest) ([]string, error) {
	content := req.Messages[len(req.Messages)-1].Content
	start := strings.Index(content, `{"segments"`)
	if start == -1 {
		return nil, fmt.Errorf("no segments payload in request: %q", content)
	}

	var payload struct {
		Segments []struct {
			ID string `json:"id"`
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(content[start:]), &payload); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(payload.Segments))
	for _, seg := range payload.Segments {
		ids = append(ids, seg.ID)
	}
	return ids, nil
}

func TestTranslator_ComposesPromptWithBibleAndGlossary(t *testing.T) {
	t.Parallel()

	gloss, err := glossary.New([]glossary.Term{{EN: "Bilbo", FR: "Bilbon", Locked: true}})
	require.NoError(t, err)

	chat := newStubChat(`{"segments":[{"id":"s1","target_text":"Bonjour."}]}`)
	tr := literary.Translator{Chat: chat, Model: "model-a", Glossary: gloss}

	_, err = tr.TranslateChapter(t.Context(), bibleFixture(), "ch_0001", chapterOf(segment.Segment{
		ID:         "s1",
		SourceText: "Bilbo arrived.",
		TargetText: "Bilbon arriva.",
	}), "previous", "Bilbo arrived. And then he left.")
	require.NoError(t, err)

	require.Len(t, chat.lastReq().Messages, 2)
	stable := chat.lastReq().Messages[0].Content
	volatile := chat.lastReq().Messages[1].Content
	require.Contains(t, stable, "formal, precise literary French")
	require.Contains(t, stable, "Alice")
	require.Contains(t, stable, "Alice -> Bruno: vous")
	require.Contains(t, stable, "RESUME_DE_L_HISTOIRE")
	require.Contains(t, stable, "Alice meets Bruno at the gate.")
	require.Contains(t, stable, "Bilbo => Bilbon")
	// The chapter's own source rides in the cacheable prefix, not in the
	// per-batch turn: it is identical across every batch of the chapter.
	require.Contains(t, stable, "SOURCE_INTEGRALE_DU_CHAPITRE")
	require.Contains(t, stable, "And then he left.")
	require.Contains(t, volatile, "SEGMENTS_DU_CHAPITRE_JSON")
	require.Contains(t, volatile, "previous")
	require.NotContains(t, volatile, "CONTEXTE_SUIVANT")

	combined := stable + volatile
	require.Less(t,
		strings.Index(combined, "GUIDE_DE_STYLE"),
		strings.Index(combined, "SEGMENTS_DU_CHAPITRE_JSON"),
		"stable prefix must precede volatile chapter content")
	require.Equal(t, "model-a", chat.lastReq().Model)
}

func TestTranslator_RoundTripPreservesPlaceholders(t *testing.T) {
	t.Parallel()

	seg := tokenizedSegment(t, "s1", `<p>A <em>bold</em> claim.</p>`)
	seg.TargetText = "Une affirmation <x id=\"1\"/>grasse<x id=\"2\"/>."

	chat := newStubChat(`{"segments":[{"id":"s1","target_text":"Une affirmation <x id=\"1\"/>grasse<x id=\"2\"/>."}]}`)
	tr := literary.Translator{Chat: chat}

	segs := chapterOf(seg)
	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)
	require.Equal(t, segment.StatusPostEdited, segs[0].Status)

	require.NoError(t, segment.InlinePlaceholderManager{}.Restore(segs[0].TargetText, segs[0].Placeholders, segs[0].Node))
	rendered := renderNode(t, segs[0].Node)
	require.Contains(t, rendered, "<em>grasse</em>")
	require.Contains(t, rendered, "Une affirmation")
}

func TestTranslator_FallsBackWhenPlaceholderDropped(t *testing.T) {
	t.Parallel()

	seg := segment.Segment{
		ID:         "s1",
		SourceText: `A <x id="1"/> claim.`,
		TargetText: `Brouillon <x id="1"/> sûr.`,
	}
	chat := newStubChat(`{"segments":[{"id":"s1","target_text":"Texte sans jeton."}]}`)
	tr := literary.Translator{Chat: chat}

	segs := chapterOf(seg)
	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)
	require.Equal(t, `Brouillon <x id="1"/> sûr.`, segs[0].TargetText)
	require.Equal(t, segment.StatusTranslated, segs[0].Status)
}

func TestTranslator_GlossaryLockedTermEnforced(t *testing.T) {
	t.Parallel()

	gloss, err := glossary.New([]glossary.Term{{EN: "Bilbo", FR: "Bilbon", Locked: true}})
	require.NoError(t, err)

	chat := newStubChat(`{"segments":[{"id":"s1","target_text":"Bonjour <g id=\"1\"/>."}]}`)
	tr := literary.Translator{Chat: chat, Glossary: gloss}

	segs := chapterOf(segment.Segment{
		ID:         "s1",
		SourceText: "Bilbo waved.",
		TargetText: "Bilbon salua.",
	})
	_, err = tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)
	require.Equal(t, "Bonjour Bilbon.", segs[0].TargetText)
	require.Contains(t, chat.lastReq().Messages[1].Content, `<g id=\"1\"/>`)
}

func TestTranslator_SplitsChapterIntoBatchesAndSumsUsage(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(25)
	chat := &echoChat{}
	tr := literary.Translator{Chat: chat, BatchSize: 10}

	usage, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)

	require.Len(t, chat.batches, 3, "25 segments at BatchSize 10 must be 3 calls")
	require.Len(t, chat.batches[0], 10)
	require.Len(t, chat.batches[1], 10)
	require.Len(t, chat.batches[2], 5)

	var requested []string
	for _, batch := range chat.batches {
		requested = append(requested, batch...)
	}
	require.ElementsMatch(t, segmentIDs(segs), requested, "batches must partition the chapter without overlap")

	for _, seg := range segs {
		require.Equal(t, "traduction "+seg.ID, seg.TargetText)
		require.Equal(t, segment.StatusPostEdited, seg.Status)
	}

	// Usage is summed over the three calls rather than taken from the last one.
	require.Equal(t, 300, usage.InputTokens)
	require.Equal(t, 30, usage.OutputTokens)
	require.Equal(t, 3, usage.CacheReadTokens)
	require.Equal(t, 6, usage.CacheCreationTokens)
}

func TestTranslator_RetriesSegmentsTheModelStoppedShortOf(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(8)
	// The model answers at most 3 segments per call, as a small model that stops
	// generating mid-output does. Without a retry the last 5 would silently keep
	// their English draft.
	chat := &echoChat{maxSegments: 3}
	tr := literary.Translator{Chat: chat, BatchSize: len(segs)}

	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)

	require.Greater(t, len(chat.batches), 1, "an incomplete batch must be retried on smaller batches")
	for _, batch := range chat.batches[1:] {
		require.Less(t, len(batch), len(segs), "retries must be smaller than the batch that failed")
	}
	for _, seg := range segs {
		require.Equal(t, "traduction "+seg.ID, seg.TargetText)
		require.Equal(t, segment.StatusPostEdited, seg.Status)
	}
}

func TestTranslator_ContextFollowsTheBatchBoundary(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(20)
	chat := &echoChat{}
	tr := literary.Translator{Chat: chat, BatchSize: 10}

	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001",
		segs, "AVANT_LE_CHAPITRE", "SOURCE_DU_CHAPITRE_ENTIER")
	require.NoError(t, err)
	require.Len(t, chat.reqs, 2)

	// What follows a batch no longer travels per call: the chapter's whole
	// source sits in the cached prefix, identical for both batches.
	for _, req := range chat.reqs {
		require.Contains(t, req.Messages[0].Content, "SOURCE_DU_CHAPITRE_ENTIER")
	}
	require.Equal(t, chat.reqs[0].Messages[0].Content, chat.reqs[1].Messages[0].Content,
		"the cacheable prefix must be byte-identical across a chapter's batches")

	// The first batch opens the chapter, so it gets the caller's context.
	require.Contains(t, precedingContext(t, chat.reqs[0]), "AVANT_LE_CHAPITRE")

	// The second batch continues from the French just produced, not from the
	// previous chapter.
	second := precedingContext(t, chat.reqs[1])
	require.Contains(t, second, "traduction s010", "a batch must see the translation that precedes it")
	require.NotContains(t, second, "AVANT_LE_CHAPITRE")
}

func TestTranslator_RefusalIsANamedFailure(t *testing.T) {
	t.Parallel()

	// An HTTP 200 with empty content and stop_reason=refusal. Splitting the
	// batch will not change the model's mind, so it must fail loudly rather
	// than look like a malformed reply and trigger the halving retry.
	chat := &stubChat{replies: []llm.ChatResponse{{StopReason: llm.StopReasonRefusal}}}
	tr := literary.Translator{Chat: chat, BatchSize: 10}

	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001",
		numberedSegments(20), "", "source")

	require.ErrorIs(t, err, literary.ErrProviderRefusal)
	require.Len(t, chat.reqs, 2, "a refusal must fail its batch outright, not split it")
}

func TestTranslator_TruncationOnASingleSegmentIsNamed(t *testing.T) {
	t.Parallel()

	// One segment, cut off mid-JSON with stop_reason=max_tokens. There is
	// nothing left to split, so the segment keeps its draft — but the reason is
	// reported instead of silently reading as a decode failure.
	chat := &stubChat{replies: []llm.ChatResponse{{
		Text:       `{"segments":[{"id":"s001","target_`,
		StopReason: llm.StopReasonMaxTokens,
	}}}
	tr := literary.Translator{Chat: chat}

	segs := chapterOf(segment.Segment{ID: "s001", SourceText: "Claim.", TargetText: "brouillon"})
	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "source")

	require.ErrorIs(t, err, literary.ErrIncompleteResponse)
	require.Equal(t, "brouillon", segs[0].TargetText,
		"a chapter where nothing was translated leaves its drafts untouched")
}

func TestTranslator_ChapterSurvivesOneFailedBatch(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(20)
	// The second of two batches fails outright, as a transport error would.
	chat := &echoChat{errOnCall: 2}
	tr := literary.Translator{Chat: chat, BatchSize: 10}

	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err, "one bad batch must not discard the batch that succeeded")

	for _, seg := range segs[:10] {
		require.Equal(t, "traduction "+seg.ID, seg.TargetText)
		require.Equal(t, segment.StatusPostEdited, seg.Status)
	}
	for _, seg := range segs[10:] {
		// Same degradation as a segment the model simply did not answer: the
		// draft survives and the status tells the caller it was not post-edited.
		require.Equal(t, "Claim "+seg.ID, seg.TargetText)
		require.Equal(t, segment.StatusTranslated, seg.Status)
	}
}

func TestTranslator_ErrorsWhenNoBatchProducesAnything(t *testing.T) {
	t.Parallel()

	chat := &echoChat{errOnCall: 1}
	tr := literary.Translator{Chat: chat, BatchSize: 10}

	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", numberedSegments(5), "", "")
	require.ErrorContains(t, err, "provider unavailable",
		"a chapter that produced nothing must fail loudly, not return English")
}

func TestTranslator_RetriesUnparseableResponseOnSmallerBatches(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(8)
	// Batches over 3 segments come back cut off mid-JSON, as a model hitting its
	// output limit does.
	chat := &echoChat{truncateOver: 3}
	tr := literary.Translator{Chat: chat, BatchSize: len(segs)}

	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)

	require.Greater(t, len(chat.batches), 1, "a truncated response must be retried on smaller batches")
	for _, seg := range segs {
		require.Equal(t, "traduction "+seg.ID, seg.TargetText)
		require.Equal(t, segment.StatusPostEdited, seg.Status)
	}
}

func TestTranslator_NilChat(t *testing.T) {
	t.Parallel()

	tr := literary.Translator{}
	_, err := tr.TranslateChapter(t.Context(), narrative.Bible{}, "ch_0001", chapterOf(segment.Segment{ID: "s1"}), "", "")
	require.ErrorIs(t, err, literary.ErrNilChatClient)
}

// chapterOf builds the pointer slice TranslateChapter rewrites in place.
func chapterOf(segs ...segment.Segment) []*segment.Segment {
	out := make([]*segment.Segment, len(segs))
	for i := range segs {
		out[i] = &segs[i]
	}
	return out
}

func numberedSegments(n int) []*segment.Segment {
	segs := make([]segment.Segment, n)
	for i := range segs {
		id := fmt.Sprintf("s%03d", i+1)
		segs[i] = segment.Segment{ID: id, SourceText: "Claim " + id}
	}
	return chapterOf(segs...)
}

// precedingContext returns the context block the translator sends ahead of a
// batch in the volatile message.
func precedingContext(t *testing.T, req llm.ChatRequest) string {
	t.Helper()

	content := req.Messages[len(req.Messages)-1].Content
	prevStart := strings.Index(content, "CONTEXTE_PRECEDENT:")
	payloadStart := strings.Index(content, "SEGMENTS_DU_CHAPITRE_JSON:")
	require.True(t, prevStart >= 0 && prevStart < payloadStart,
		"volatile message must carry the preceding context before the payload")

	return content[prevStart:payloadStart]
}

func segmentIDs(segs []*segment.Segment) []string {
	ids := make([]string, 0, len(segs))
	for _, seg := range segs {
		ids = append(ids, seg.ID)
	}
	return ids
}

func bibleFixture() narrative.Bible {
	return narrative.Bible{
		StyleGuide: "Use formal, precise literary French.",
		Characters: []narrative.CharacterVoice{{
			Name:     "Alice",
			Register: "measured",
			Notes:    "Avoid slang.",
		}},
		Address: []narrative.AddressRule{{
			From: "Alice",
			To:   "Bruno",
			Form: "vous",
			History: []narrative.AddressChange{{
				AtChapter: "ch_0001",
				Note:      "Formal distance.",
			}},
		}},
		Summaries: []narrative.ChapterSummary{{
			ChapterID: "ch_0001",
			Summary:   "Alice meets Bruno at the gate.",
		}},
	}
}

func tokenizedSegment(t *testing.T, id, raw string) segment.Segment {
	t.Helper()

	root, err := html.Parse(strings.NewReader(raw))
	require.NoError(t, err)

	var p *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "p" {
			p = n
			return
		}
		for c := n.FirstChild; c != nil && p == nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	require.NotNil(t, p)

	text, placeholders, err := segment.InlinePlaceholderManager{}.Tokenize(p)
	require.NoError(t, err)
	return segment.Segment{
		ID:           id,
		ChapterID:    "ch_0001",
		SourceText:   text,
		Placeholders: placeholders,
		Node:         p,
	}
}

func renderNode(t *testing.T, n *html.Node) string {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, html.Render(&buf, n))
	return buf.String()
}
