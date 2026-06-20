package narrative_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
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

func TestProfiler_UpdateAppendsSummaryAndAddressChange(t *testing.T) {
	t.Parallel()

	chat := &stubChat{reply: `{
		"series_id":"series-a",
		"style_guide":"Use polished contemporary French.",
		"characters":[{"name":"Alice","register":"direct","notes":"Now warmer.","sample_fr":[]}],
		"address":[{"from":"Alice","to":"Bruno","form":"tu","history":[{"at_chapter":"ch_0003","note":"They reconcile."}]}],
		"summaries":[{"chapter_id":"ch_0003","summary":"Alice and Bruno reconcile."}],
		"version":2
	}`}
	profiler := narrative.Profiler{Chat: chat}

	got, err := profiler.Update(t.Context(), narrative.Bible{
		SeriesID:   "series-a",
		StyleGuide: "Use polished contemporary French.",
	}, "ch_0003", "Alice forgave Bruno.", "Alice pardonna Bruno.")

	require.NoError(t, err)
	require.Equal(t, "series-a", got.SeriesID)
	require.Equal(t, []narrative.ChapterSummary{{
		ChapterID: "ch_0003",
		Summary:   "Alice and Bruno reconcile.",
	}}, got.Summaries)
	require.True(t, narrative.Consistent(got, "Alice", "Bruno", "tu"))
	require.Contains(t, chat.req.Messages[1].Content, "CURRENT_BIBLE_JSON")
	require.Contains(t, chat.req.Messages[1].Content, "Alice forgave Bruno.")
	require.Contains(t, chat.req.Messages[1].Content, "Alice pardonna Bruno.")
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
