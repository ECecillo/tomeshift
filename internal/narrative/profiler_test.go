package narrative_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/llm"
	"github.com/ECecillo/tomeshift/internal/narrative"
)

type stubChat struct {
	reply string
	req   llm.ChatRequest
}

func (s *stubChat) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	s.req = req
	return llm.ChatResponse{Text: s.reply}, nil
}

func TestProfiler_Seed(t *testing.T) {
	t.Parallel()

	chat := &stubChat{reply: `{
		"series_id":"",
		"style_guide":"Use polished contemporary French.",
		"characters":[{"name":"Alice","register":"direct","notes":"Brief answers.","sample_fr":["J'arrive."]}],
		"address":[{"from":"Alice","to":"Bruno","form":"tu","history":[{"at_chapter":"ch_0001","note":"Close friends."}]}],
		"summaries":[],
		"version":0
	}`}
	profiler := narrative.Profiler{Chat: chat, Model: "model-a"}

	got, err := profiler.Seed(t.Context(), []string{"Alice waved."})
	require.NoError(t, err)
	require.Equal(t, "Use polished contemporary French.", got.StyleGuide)
	require.Len(t, got.Characters, 1)
	require.Equal(t, "Alice", got.Characters[0].Name)
	require.True(t, narrative.Consistent(got, "Alice", "Bruno", "tu"))
	require.Equal(t, "model-a", chat.req.Model)
	require.Contains(t, chat.req.Messages[1].Content, "Alice waved.")
}

func TestProfiler_UpdatePreservesVolumeSummariesAndScope(t *testing.T) {
	t.Parallel()

	chat := &stubChat{reply: `{
		"series_id":"wrong-scope",
		"style_guide":"Use polished contemporary French.",
		"characters":[{"name":"Alice","register":"direct","notes":"Now warmer.","sample_fr":[]}],
		"address":[{"from":"Alice","to":"Bruno","form":"tu","history":[{"at_chapter":"ch_0003","note":"They reconcile."}]}],
		"summaries":[{"chapter_id":"ch_0003","summary":"MUST BE DISCARDED"}],
		"version":2
	}`}
	profiler := narrative.Profiler{Chat: chat}

	got, err := profiler.Update(t.Context(), narrative.Bible{
		SeriesID:   "series-a",
		StyleGuide: "Use polished contemporary French.",
		Summaries:  []narrative.Summary{{VolumeID: "volume-01", Summary: "Volume synopsis."}},
	}, "ch_0003", "Alice forgave Bruno.", "Alice pardonna Bruno.")

	require.NoError(t, err)
	require.Equal(t, "series-a", got.SeriesID)
	require.Equal(t, []narrative.Summary{{
		VolumeID: "volume-01",
		Summary:  "Volume synopsis.",
	}}, got.Summaries)
	require.True(t, narrative.Consistent(got, "Alice", "Bruno", "tu"))
	require.Contains(t, chat.req.Messages[1].Content, "CURRENT_BIBLE_JSON")
	require.Contains(t, chat.req.Messages[1].Content, "Alice forgave Bruno.")
	require.Contains(t, chat.req.Messages[1].Content, "Alice pardonna Bruno.")
	require.NotContains(t, chat.req.Messages[1].Content, "Append one summary")
}

func TestProfiler_JSONWrappedInText(t *testing.T) {
	t.Parallel()

	chat := &stubChat{reply: "Here is the result:\n```json\n" + `{
		"series_id":"series-b",
		"style_guide":"Use polished contemporary French.",
		"characters":[],
		"address":[],
		"summaries":[],
		"version":0
	}` + "\n```\nPreserve existing facts."}
	profiler := narrative.Profiler{Chat: chat, Model: "model-a"}

	got, err := profiler.Seed(t.Context(), []string{"sample"})
	require.NoError(t, err)
	require.Equal(t, "series-b", got.SeriesID)
}

func TestProfiler_MalformedJSON(t *testing.T) {
	t.Parallel()

	profiler := narrative.Profiler{Chat: &stubChat{reply: `{not json`}}
	_, err := profiler.Seed(t.Context(), []string{"sample"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "unable to parse seeded narrative bible")
}

func TestProfiler_ValidatesInputs(t *testing.T) {
	t.Parallel()

	profiler := narrative.Profiler{}
	_, err := profiler.Seed(t.Context(), []string{"sample"})
	require.ErrorIs(t, err, narrative.ErrNilChatClient)

	profiler.Chat = &stubChat{reply: `{}`}
	_, err = profiler.Seed(t.Context(), []string{"   "})
	require.True(t, errors.Is(err, narrative.ErrEmptySamples))

	_, err = profiler.Seed(t.Context(), []string{"sample"})
	require.True(t, errors.Is(err, narrative.ErrInvalidBible))

	profiler.Chat = &stubChat{reply: `{"summaries":[{"volume_id":"volume-01","summary":"plot only"}]}`}
	_, err = profiler.Seed(t.Context(), []string{"sample"})
	require.ErrorIs(t, err, narrative.ErrInvalidBible)
}

func TestProfiler_SeedPromptStrictJSON(t *testing.T) {
	t.Parallel()

	chat := &stubChat{reply: `{"series_id":"","style_guide":"x","characters":[],"address":[],"summaries":[],"version":0}`}
	profiler := narrative.Profiler{Chat: chat}

	_, err := profiler.Seed(t.Context(), []string{"line one", "line two"})
	require.NoError(t, err)
	require.Contains(t, chat.req.Messages[0].Content, "strict JSON only")
	require.False(t, strings.Contains(chat.req.Messages[0].Content, "markdown except"))
}

// recordingChat captures every request so the map step can be checked call by
// call, which is what makes the two synopsis operations testable without a
// bible round-trip.
type recordingChat struct {
	replies []string
	reqs    []llm.ChatRequest
}

func (c *recordingChat) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.reqs = append(c.reqs, req)
	reply := "résumé par défaut"
	if len(c.reqs) <= len(c.replies) {
		reply = c.replies[len(c.reqs)-1]
	}
	return llm.ChatResponse{Text: reply}, nil
}

func TestProfiler_SummariseChapter(t *testing.T) {
	t.Parallel()

	chat := &stubChat{reply: "  Kai arrive à Valoria et rencontre Selene.  "}
	profiler := narrative.Profiler{Chat: chat, Model: "model-a"}

	got, err := profiler.SummariseChapter(t.Context(), "ch_0001", "Kai stepped out of the convenience store.")
	require.NoError(t, err)
	require.Equal(t, "Kai arrive à Valoria et rencontre Selene.", got)
	require.Equal(t, "model-a", chat.req.Model)
	require.Contains(t, chat.req.Messages[1].Content, "ch_0001")
	require.Contains(t, chat.req.Messages[1].Content, "Kai stepped out of the convenience store.")
	// Source only: no target text, and nothing that round-trips a bible.
	require.NotContains(t, chat.req.Messages[1].Content, "CURRENT_BIBLE_JSON")
}

func TestProfiler_SummariseChapterValidatesInputs(t *testing.T) {
	t.Parallel()

	profiler := narrative.Profiler{}
	_, err := profiler.SummariseChapter(t.Context(), "ch_0001", "text")
	require.ErrorIs(t, err, narrative.ErrNilChatClient)

	profiler.Chat = &stubChat{reply: "x"}
	_, err = profiler.SummariseChapter(t.Context(), "ch_0001", "   ")
	require.ErrorIs(t, err, narrative.ErrEmptySource)

	profiler.Chat = &stubChat{reply: "   "}
	_, err = profiler.SummariseChapter(t.Context(), "ch_0001", "text")
	require.ErrorIs(t, err, narrative.ErrEmptySummary)
}

func TestProfiler_SynthesiseVolumeReducesOverEveryChapter(t *testing.T) {
	t.Parallel()

	chat := &stubChat{reply: "Le tome suit Kai de son arrivée jusqu'à sa mort au manoir."}
	profiler := narrative.Profiler{Chat: chat}

	got, err := profiler.SynthesiseVolume(t.Context(), "volume-01", []string{
		"Ouverture : Kai arrive.",
		"Milieu : le vol de l'insigne.",
		"",
		"Fin : la boucle se referme.",
	})
	require.NoError(t, err)
	require.Equal(t, "Le tome suit Kai de son arrivée jusqu'à sa mort au manoir.", got)

	prompt := chat.req.Messages[1].Content
	require.Contains(t, prompt, "volume-01")
	require.Contains(t, prompt, "Ouverture : Kai arrive.")
	require.Contains(t, prompt, "Milieu : le vol de l'insigne.")
	require.Contains(t, prompt, "Fin : la boucle se referme.")
	// Blanks are dropped, so the numbering the model sees stays contiguous.
	require.Contains(t, prompt, "[3] Fin : la boucle se referme.")
}

func TestProfiler_SynthesiseVolumeValidatesInputs(t *testing.T) {
	t.Parallel()

	profiler := narrative.Profiler{}
	_, err := profiler.SynthesiseVolume(t.Context(), "volume-01", []string{"a"})
	require.ErrorIs(t, err, narrative.ErrNilChatClient)

	profiler.Chat = &stubChat{reply: "x"}
	_, err = profiler.SynthesiseVolume(t.Context(), "volume-01", []string{"  ", ""})
	require.ErrorIs(t, err, narrative.ErrEmptySamples)
}

// The map step must not mutate the caller's slice: the caller keeps the same
// summaries to store as a by-product.
func TestProfiler_SynthesiseVolumeLeavesInputIntact(t *testing.T) {
	t.Parallel()

	profiler := narrative.Profiler{Chat: &recordingChat{replies: []string{"synopsis"}}}
	summaries := []string{"un", "", "deux"}

	_, err := profiler.SynthesiseVolume(t.Context(), "volume-01", summaries)
	require.NoError(t, err)
	require.Equal(t, []string{"un", "", "deux"}, summaries)
}

// A blank sample used to compact the caller's backing array in place, so the
// surviving samples reappeared further down the prompt.
func TestProfiler_SeedLeavesSamplesIntact(t *testing.T) {
	t.Parallel()

	chat := &stubChat{reply: `{"series_id":"","style_guide":"x","characters":[],"address":[],"summaries":[],"version":0}`}
	profiler := narrative.Profiler{Chat: chat}
	samples := []string{"première", "  ", "seconde"}

	_, err := profiler.Seed(t.Context(), samples)
	require.NoError(t, err)
	require.Equal(t, []string{"première", "  ", "seconde"}, samples)
	require.Equal(t, 1, strings.Count(chat.req.Messages[1].Content, "première"))
	require.Equal(t, 1, strings.Count(chat.req.Messages[1].Content, "seconde"))
}
