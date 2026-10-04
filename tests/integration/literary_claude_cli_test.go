package integration_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/literary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/segment"
)

// liveCLIEnv opts into the live Claude Code CLI test.
//
// Unlike the Ollama one, presence of the binary is not enough to run this: each
// call consumes a real subscription quota, so it has to be asked for
// explicitly. It is the check to run once before starting a volume, not on
// every `just test`.
const liveCLIEnv = "EPUB_TRANSLATOR_LIVE_CLI_TEST"

func skipUnlessLiveCLI(t *testing.T) {
	t.Helper()
	if os.Getenv(liveCLIEnv) == "" {
		t.Skipf("set %s=1 to run the live Claude Code CLI test (consumes quota)", liveCLIEnv)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH — skipping live CLI test")
	}
}

// TestLiterary_ClaudeCLI_TranslatesEverySegment drives the real CLI through the
// literary translator. It is the end-to-end check a stub cannot give: that the
// adapter's argument wiring, the JSON contract and the placeholder round trip
// all survive a real model — and it prints the token counters, which are the
// only measure of what a subscription run consumes.
func TestLiterary_ClaudeCLI_TranslatesEverySegment(t *testing.T) {
	skipUnlessLiveCLI(t)

	model := os.Getenv("EPUB_TRANSLATOR_LLM_MODEL")
	if model == "" {
		model = "opus"
	}

	translator := &literary.Translator{
		Chat:  llm.NewCLIChatClient(os.Getenv("EPUB_TRANSLATOR_LLM_CLI_BIN")),
		Model: model,
	}

	sources := []string{
		`She whispered <x id="1"/>Return by Ash<x id="2"/> and vanished.`,
		`The gate closed behind them, and the city fell silent.`,
		`"You came back," she said, without turning around.`,
	}
	segs := make([]*segment.Segment, len(sources))
	for i, src := range sources {
		segs[i] = &segment.Segment{
			ID:         "ch_0001_p_000" + string(rune('1'+i)),
			ChapterID:  "ch_0001",
			SourceText: src,
			TargetText: "BROUILLON NON TRADUIT",
			Status:     segment.StatusTranslated,
		}
	}

	bible := narrative.Bible{
		SeriesID:   "live-cli",
		StyleGuide: "Français littéraire sobre, présent narratif évité, pas de calque de l'anglais.",
	}

	outcome, err := translator.TranslateBatch(
		context.Background(), bible, "ch_0001", segs, "", strings.Join(sources, "\n"))
	require.NoError(t, err)
	require.Empty(t, outcome.Degraded, "no batch should have kept its draft")

	t.Logf("usage: input=%d output=%d cache_read=%d cache_creation=%d",
		outcome.Usage.InputTokens, outcome.Usage.OutputTokens,
		outcome.Usage.CacheReadTokens, outcome.Usage.CacheCreationTokens)

	for _, seg := range segs {
		require.Equal(t, segment.StatusPostEdited, seg.Status,
			"segment %s fell back to its draft", seg.ID)
		require.NotEqual(t, "BROUILLON NON TRADUIT", seg.TargetText)
	}

	// The placeholder pair must come back intact and in order, or the
	// recomposition would rebuild the wrong markup.
	require.Contains(t, segs[0].TargetText, `<x id="1"/>`)
	require.Contains(t, segs[0].TargetText, `<x id="2"/>`)
	require.Less(t,
		strings.Index(segs[0].TargetText, `<x id="1"/>`),
		strings.Index(segs[0].TargetText, `<x id="2"/>`))
}
