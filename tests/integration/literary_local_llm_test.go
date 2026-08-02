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
	ollamaHost    = "http://localhost:11434"
	ollamaBaseURL = ollamaHost + "/v1"
	ollamaModel   = "mistral"
)

// skipIfOllamaUnavailable skips the test when the local Ollama endpoint does
// not respond within a short probe timeout.
func skipIfOllamaUnavailable(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ollamaHost + "/api/tags")
	if err != nil {
		t.Skip("local Ollama not reachable — skipping live LLM test")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Skip("local Ollama not reachable — skipping live LLM test")
	}
}

// TestLiterary_LocalMistral_CompletesEverySegment checks against a real small
// model that no segment is left in the English fallback. The two cases cover
// the configuration production runs, and the early-stop condition the halving
// retry exists for — which a stub can only approximate.
func TestLiterary_LocalMistral_CompletesEverySegment(t *testing.T) {
	skipIfOllamaUnavailable(t)

	tests := []struct {
		name      string
		segments  int
		batchSize int
		maxTokens int
	}{
		{
			// Shipped defaults: one call for a chapter this size on a 7B model.
			name:     "default batch size",
			segments: 40,
		},
		{
			// A token budget far below what the batch needs forces the model to
			// stop mid-output, so the response is unusable. Only splitting the
			// batch down to something that fits can finish the chapter: without
			// the retry every segment here stays English.
			name:      "forced early stop",
			segments:  16,
			batchSize: 16,
			maxTokens: 200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := literary.Translator{
				Chat:      llm.NewOpenAICompatibleClient(ollamaBaseURL, ""),
				Model:     ollamaModel,
				BatchSize: tt.batchSize,
				MaxTokens: tt.maxTokens,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			segs := syntheticChapter(tt.segments)
			outcome, err := tr.TranslateChapter(ctx,
				narrative.Bible{StyleGuide: "Prose littéraire sobre, phrases courtes."},
				"ch_0001", segs, "", "")
			require.NoError(t, err)

			t.Logf("tokens — input: %d, output: %d", outcome.Usage.InputTokens, outcome.Usage.OutputTokens)

			untranslated := 0
			for _, seg := range segs {
				// A segment is considered untranslated when its target equals its
				// source (English) — exactly the fallback behaviour we are guarding.
				if strings.EqualFold(strings.TrimSpace(seg.TargetText), strings.TrimSpace(seg.SourceText)) {
					untranslated++
					t.Logf("untranslated: %s => %q", seg.ID, seg.TargetText)
				}
			}
			require.Zero(t, untranslated, "all segments must be translated; %d remained in English", untranslated)
		})
	}
}

func syntheticChapter(n int) []*segment.Segment {
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

	segs := make([]*segment.Segment, n)
	for i := range segs {
		segs[i] = &segment.Segment{
			ID:         fmt.Sprintf("ch_0001_s%04d", i+1),
			SourceText: sentences[i%len(sentences)],
		}
	}
	return segs
}
