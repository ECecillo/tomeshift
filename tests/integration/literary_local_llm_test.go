package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/literary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/segment"
)

const (
	ollamaBaseURL = "http://localhost:11434/v1"
	ollamaModel   = "mistral"
)

// skipIfOllamaUnavailable skips the test when the local Ollama endpoint does
// not respond within a short probe timeout.
func skipIfOllamaUnavailable(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skip("local Ollama not reachable — skipping live LLM test")
	}
	_ = resp.Body.Close()
}

// TestLiterary_LocalMistral_BatchingCompletesAllSegments verifies that
// TranslateChapter returns all segments translated (not English fallback) when
// the chapter has more segments than BatchSize. This guards against the small-
// model early-stop regression that prompted the batching fix.
func TestLiterary_LocalMistral_BatchingCompletesAllSegments(t *testing.T) {
	skipIfOllamaUnavailable(t)

	const totalSegments = 40 // two full batches of 20

	segs := make([]segment.Segment, totalSegments)
	sentences := []string{
		"The wind howled through the broken towers.",
		"Kael pressed his back against the cold stone wall.",
		"He could hear them coming, three or four.",
		"The Veilborn moved like shadows, silent and purposeful.",
		"We need to move, Elder Mira whispered.",
		"He nodded, gripping the hilt of his blade.",
		"The Rift pulsed in the distance, a wound in the sky.",
		"Every step toward it felt like walking into fire.",
		"The village lay in ruins ahead.",
		"Nothing had survived the last incursion.",
	}
	for i := range segs {
		segs[i] = segment.Segment{
			ID:         fmt.Sprintf("ch_0001_s%04d", i+1),
			SourceText: sentences[i%len(sentences)],
		}
	}

	chat := llm.NewOpenAICompatibleClient(ollamaBaseURL, "")
	tr := literary.Translator{
		Chat:      chat,
		Model:     ollamaModel,
		BatchSize: 20,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	out, usage, err := tr.TranslateChapter(ctx, narrative.Bible{StyleGuide: "Prose littéraire sobre, phrases courtes."}, "ch_0001", segs, "", "")
	require.NoError(t, err)
	require.Len(t, out, totalSegments)

	t.Logf("tokens — input: %d, output: %d", usage.InputTokens, usage.OutputTokens)

	untranslated := 0
	for _, seg := range out {
		// A segment is considered untranslated when its target equals its source
		// (English) — which is exactly the fallback behaviour we are guarding.
		if strings.EqualFold(strings.TrimSpace(seg.TargetText), strings.TrimSpace(seg.SourceText)) {
			untranslated++
			t.Logf("untranslated: %s => %q", seg.ID, seg.TargetText)
		}
	}
	require.Zero(t, untranslated, "all segments must be translated; %d remained in English", untranslated)
}
