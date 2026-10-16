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

	"github.com/ECecillo/tomeshift/internal/glossary"
	"github.com/ECecillo/tomeshift/internal/literary"
	"github.com/ECecillo/tomeshift/internal/llm"
	"github.com/ECecillo/tomeshift/internal/narrative"
	"github.com/ECecillo/tomeshift/internal/segment"
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
// whole batch. The recursive halving inside TranslateBatch is what turns a
// short answer into a fully translated batch.
type echoChat struct {
	maxSegments int
	// truncateOver cuts the response off mid-JSON when the batch holds more than
	// this many segments, as a model hitting its output limit does; 0 disables.
	truncateOver int
	// errOnCall makes the nth call (1-based) fail like a transport error would;
	// 0 disables.
	errOnCall int
	// rateLimitOnCall makes the nth call (1-based) fail the way an exhausted
	// quota does, after reporting the tokens it consumed getting there.
	rateLimitOnCall int
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
	if e.rateLimitOnCall == len(e.reqs) {
		return llm.ChatResponse{InputTokens: 100, OutputTokens: 10},
			fmt.Errorf("unable to chat: status 429, err: %w", llm.ErrRateLimited)
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

func TestTranslateBatch_ComposesPromptWithBibleAndGlossary(t *testing.T) {
	t.Parallel()

	gloss, err := glossary.New([]glossary.Term{{EN: "Bilbo", FR: "Bilbon", Locked: true}})
	require.NoError(t, err)

	chat := newStubChat(`{"segments":[{"id":"s1","target_text":"Bonjour."}]}`)
	tr := literary.Translator{Chat: chat, Model: "model-a", Glossary: gloss}

	_, err = tr.TranslateBatch(t.Context(), bibleFixture(), "ch_0001", chapterOf(segment.Segment{
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

func TestTranslateBatch_PrevContextAndChapterSourceRideTheirOwnSlots(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(10)
	chat := &echoChat{}
	tr := literary.Translator{Chat: chat}

	_, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001",
		segs, "AVANT_LE_CHAPITRE", "SOURCE_DU_CHAPITRE_ENTIER")
	require.NoError(t, err)
	require.Len(t, chat.reqs, 1, "TranslateBatch sends one call; the caller decides the boundaries")

	// The chapter's whole source is the cacheable prefix; the caller's preceding
	// context travels in the volatile turn.
	require.Contains(t, chat.reqs[0].Messages[0].Content, "SOURCE_DU_CHAPITRE_ENTIER")
	require.Contains(t, precedingContext(t, chat.reqs[0]), "AVANT_LE_CHAPITRE")
}

func TestTranslateBatch_RoundTripPreservesPlaceholders(t *testing.T) {
	t.Parallel()

	seg := tokenizedSegment(t, "s1", `<p>A <em>bold</em> claim.</p>`)
	seg.TargetText = "Une affirmation <x id=\"1\"/>grasse<x id=\"2\"/>."

	chat := newStubChat(`{"segments":[{"id":"s1","target_text":"Une affirmation <x id=\"1\"/>grasse<x id=\"2\"/>."}]}`)
	tr := literary.Translator{Chat: chat}

	segs := chapterOf(seg)
	_, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)
	require.Equal(t, segment.StatusPostEdited, segs[0].Status)

	require.NoError(t, segment.InlinePlaceholderManager{}.Restore(segs[0].TargetText, segs[0].Placeholders, segs[0].Node))
	rendered := renderNode(t, segs[0].Node)
	require.Contains(t, rendered, "<em>grasse</em>")
	require.Contains(t, rendered, "Une affirmation")
}

func TestTranslateBatch_FallsBackWhenPlaceholderDropped(t *testing.T) {
	t.Parallel()

	seg := segment.Segment{
		ID:         "s1",
		SourceText: `A <x id="1"/> claim.`,
		TargetText: `Brouillon <x id="1"/> sûr.`,
	}
	chat := newStubChat(`{"segments":[{"id":"s1","target_text":"Texte sans jeton."}]}`)
	tr := literary.Translator{Chat: chat}

	segs := chapterOf(seg)
	_, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)
	require.Equal(t, `Brouillon <x id="1"/> sûr.`, segs[0].TargetText)
	require.Equal(t, segment.StatusTranslated, segs[0].Status)
}

func TestTranslateBatch_GlossaryLockedTermEnforced(t *testing.T) {
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
	_, err = tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)
	// The glossary token is protected in the prompt and substituted back on the
	// way out.
	require.Contains(t, chat.lastReq().Messages[1].Content, `<g id=\"1\"/>`)
	require.Equal(t, "Bonjour Bilbon.", segs[0].TargetText)
}

func TestTranslateBatch_RetriesSegmentsTheModelStoppedShortOf(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(8)
	// The model answers at most 3 segments per call, as a small model that stops
	// generating mid-output does. Without the internal halving the last 5 would
	// silently keep their English draft.
	chat := &echoChat{maxSegments: 3}
	tr := literary.Translator{Chat: chat}

	outcome, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)
	require.Empty(t, outcome.Degraded)

	require.Greater(t, len(chat.batches), 1, "an incomplete answer must be retried on smaller batches")
	for _, batch := range chat.batches[1:] {
		require.Less(t, len(batch), len(segs), "retries must be smaller than the batch that failed")
	}
	for _, seg := range segs {
		require.Equal(t, "traduction "+seg.ID, seg.TargetText)
		require.Equal(t, segment.StatusPostEdited, seg.Status)
	}
}

func TestTranslateBatch_RetriesUnparseableResponseOnSmallerBatches(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(8)
	// Batches over 3 segments come back cut off mid-JSON, as a model hitting its
	// output limit does; the halved retries parse.
	chat := &echoChat{truncateOver: 3}
	tr := literary.Translator{Chat: chat}

	_, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)

	require.Greater(t, len(chat.batches), 1, "a truncated response must be retried on smaller batches")
	for _, seg := range segs {
		require.Equal(t, "traduction "+seg.ID, seg.TargetText)
		require.Equal(t, segment.StatusPostEdited, seg.Status)
	}
}

// TestTranslateBatch_EmptyReplyIsRetriedOnHalvedBatches pins the third case in
// the failure taxonomy. An empty reply used to propagate as a provider error,
// so the batch died without a retry: on a real chapter that left 100 of 258
// segments on their raw NMT draft while the run reported success. It also proves
// usage accumulates across the halved retries — the empty calls were billed too.
func TestTranslateBatch_EmptyReplyIsRetriedOnHalvedBatches(t *testing.T) {
	t.Parallel()

	chat := &emptyOverChat{
		maxSegments:  2,
		usagePerCall: llm.ChatResponse{InputTokens: 10, OutputTokens: 7},
	}
	translator := &literary.Translator{Chat: chat, Model: "m"}

	segs := make([]*segment.Segment, 8)
	for i := range segs {
		segs[i] = &segment.Segment{
			ID:         fmt.Sprintf("ch_0001_p_%04d", i+1),
			SourceText: fmt.Sprintf("Source %d.", i+1),
			TargetText: "BROUILLON",
			Status:     segment.StatusTranslated,
		}
	}

	outcome, err := translator.TranslateBatch(
		context.Background(), narrative.Bible{SeriesID: "s"}, "ch_0001", segs, "", "source")
	require.NoError(t, err)

	// Every segment is refined: the 8-segment batch came back empty, and the
	// halving walked down to batches the model could answer.
	for _, seg := range segs {
		require.Equal(t, segment.StatusPostEdited, seg.Status, seg.ID)
		require.NotEqual(t, "BROUILLON", seg.TargetText)
	}
	require.Empty(t, outcome.Degraded)

	// Usage is summed over every call, empty ones included. Dropping their usage
	// would under-report every reasoning model that runs out of budget.
	require.Equal(t, chat.calls*10, outcome.Usage.InputTokens)
	require.Equal(t, chat.calls*7, outcome.Usage.OutputTokens)
	require.Greater(t, chat.calls, 1, "the first call must have been retried")
}

func TestTranslateBatch_RefusalIsDegradableAndKeepsTheDraft(t *testing.T) {
	t.Parallel()

	// An HTTP 200 with empty content and stop_reason=refusal. Splitting will not
	// change the model's mind, so the batch keeps its drafts rather than looking
	// like a malformed reply and triggering the halving retry. A refusal is a
	// billable response, so its tokens must survive.
	chat := &stubChat{replies: []llm.ChatResponse{{
		StopReason: llm.StopReasonRefusal, InputTokens: 900,
	}}}
	tr := literary.Translator{Chat: chat}

	segs := chapterOf(
		segment.Segment{ID: "s1", SourceText: "A.", TargetText: "brouillon 1"},
		segment.Segment{ID: "s2", SourceText: "B.", TargetText: "brouillon 2"},
	)
	outcome, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "source")
	require.NoError(t, err, "a degradable failure does not fail the run")
	require.Len(t, chat.reqs, 1, "a refusal must fail its batch outright, not split it")

	require.Len(t, outcome.Degraded, 1)
	require.True(t, literary.IsDegradable(outcome.Degraded[0]))
	require.ErrorIs(t, outcome.Degraded[0], literary.ErrProviderRefusal)
	require.Positive(t, outcome.Usage.InputTokens, "billable tokens must survive the degradation")

	for _, seg := range segs {
		require.Equal(t, "brouillon "+strings.TrimPrefix(seg.ID, "s"), seg.TargetText)
		require.Equal(t, segment.StatusTranslated, seg.Status)
	}
}

func TestTranslateBatch_TruncationOnASingleSegmentIsDegraded(t *testing.T) {
	t.Parallel()

	// One segment, cut off mid-JSON with stop_reason=max_tokens. There is
	// nothing left to split, so the segment keeps its draft — but the reason is
	// reported through Degraded instead of silently reading as a decode failure.
	chat := &stubChat{replies: []llm.ChatResponse{{
		Text:       `{"segments":[{"id":"s001","target_`,
		StopReason: llm.StopReasonMaxTokens,
	}}}
	tr := literary.Translator{Chat: chat}

	segs := chapterOf(segment.Segment{ID: "s001", SourceText: "Claim.", TargetText: "brouillon"})
	outcome, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "source")
	require.NoError(t, err)

	require.Len(t, outcome.Degraded, 1)
	require.True(t, literary.IsDegradable(outcome.Degraded[0]))
	require.ErrorIs(t, outcome.Degraded[0], literary.ErrIncompleteResponse)
	require.Equal(t, "brouillon", segs[0].TargetText,
		"a batch where nothing was translated leaves its draft untouched")
	require.Equal(t, segment.StatusTranslated, segs[0].Status)
}

func TestTranslateBatch_TransportErrorIsLoud(t *testing.T) {
	t.Parallel()

	// A transport error is not degradable: a smaller batch will not fix bad
	// credentials, so the batch fails loudly rather than emitting raw drafts the
	// pipeline does not even persist.
	chat := &echoChat{errOnCall: 1}
	tr := literary.Translator{Chat: chat}

	_, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", numberedSegments(5), "", "")
	require.ErrorContains(t, err, "provider unavailable",
		"a batch that produced nothing must fail loudly, not return English")
	require.False(t, literary.IsDegradable(err))
}

func TestTranslateBatch_RateLimitIsFatalAndReportsUsage(t *testing.T) {
	t.Parallel()

	segs := numberedSegments(10)
	chat := &echoChat{rateLimitOnCall: 1}
	tr := literary.Translator{Chat: chat}

	outcome, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "src")

	// Fatal, not degraded: a chapter that carried on would emit raw drafts the
	// pipeline does not even persist, so there would be nothing to resume from.
	require.ErrorIs(t, err, llm.ErrRateLimited)
	require.False(t, literary.IsDegradable(err), "a rate limit is not degradable")

	// A refused call is a billable call: the ledger has to carry it.
	require.Equal(t, 100, outcome.Usage.InputTokens)
	require.Equal(t, 10, outcome.Usage.OutputTokens)

	// The batch is abandoned rather than written back: none reaches
	// StatusPostEdited, and the caller fails the run.
	for _, seg := range segs {
		require.NotEqual(t, segment.StatusPostEdited, seg.Status)
	}
}

func TestTranslateBatch_TailIsTheAnswersInRequestOrder(t *testing.T) {
	t.Parallel()

	// The model answers the first and third segments but never the second. The
	// tail must read the raw answers in request order, skipping the unanswered
	// one — exactly what the next batch is fed as its preceding context.
	chat := newStubChat(`{"segments":[{"id":"s001","target_text":"Un"},{"id":"s003","target_text":"Trois"}]}`)
	tr := literary.Translator{Chat: chat}

	segs := numberedSegments(3)
	outcome, err := tr.TranslateBatch(t.Context(), narrative.Bible{StyleGuide: "x"}, "ch_0001", segs, "", "")
	require.NoError(t, err)

	require.Equal(t, "Un\nTrois", outcome.Tail)
	require.Len(t, outcome.Degraded, 1, "the unanswered segment is reported")
}

func TestTranslateBatch_NilChat(t *testing.T) {
	t.Parallel()

	tr := literary.Translator{}
	_, err := tr.TranslateBatch(t.Context(), narrative.Bible{}, "ch_0001", chapterOf(segment.Segment{ID: "s1"}), "", "")
	require.ErrorIs(t, err, literary.ErrNilChatClient)
}

// chapterOf builds the pointer slice TranslateBatch rewrites in place.
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
		Summaries: []narrative.Summary{{
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

// emptyOverChat answers with an empty reply — and an error — whenever a batch
// holds more than maxSegments, the way a reasoning model behaves when the
// thinking eats the whole max_tokens budget before the answer starts. Smaller
// batches succeed, which is precisely why halving is the right response.
type emptyOverChat struct {
	maxSegments  int
	usagePerCall llm.ChatResponse
	calls        int
}

func (c *emptyOverChat) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.calls++
	var payload struct {
		Segments []struct {
			ID string `json:"id"`
		} `json:"segments"`
	}
	user := req.Messages[len(req.Messages)-1].Content
	_ = json.Unmarshal([]byte(user[strings.Index(user, "{"):]), &payload)

	usage := c.usagePerCall
	if len(payload.Segments) > c.maxSegments {
		return usage, llm.ErrEmptyResponse
	}

	var sb strings.Builder
	sb.WriteString(`{"segments":[`)
	for i, s := range payload.Segments {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":%q,"target_text":"traduit %s"}`, s.ID, s.ID)
	}
	sb.WriteString(`]}`)
	usage.Text = sb.String()
	return usage, nil
}
