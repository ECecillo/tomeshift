package integration_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/llm/openaitest"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
)

func TestStyleProfile_WritesNarrativeBible(t *testing.T) {
	t.Parallel()

	book := writeEPUB(t, []string{
		`<h1>One</h1><p>Alice met Bruno.</p>`,
		`<h1>Two</h1><p>Bruno trusted Alice.</p>`,
	})
	dbPath := filepath.Join(t.TempDir(), "narrative.sqlite")

	srv := openaitest.NewServer(func(req openaitest.Request) string {
		require.Contains(t, req.Messages[1].Content, "Alice met Bruno.")
		return `{
			"series_id":"",
			"style_guide":"Use clear literary French.",
			"characters":[{"name":"Alice","register":"direct","notes":"Plain speech.","sample_fr":[]}],
			"address":[{"from":"Alice","to":"Bruno","form":"tu","history":[{"at_chapter":"ch_0001","note":"Close allies."}]}],
			"summaries":[],
			"version":0
		}`
	})
	t.Cleanup(srv.Close)

	loader := func() (config.Config, error) {
		return config.Config{
			NarrativePath:   dbPath,
			LLMChatProvider: "openai",
			LLMBaseURL:      srv.URL + "/v1",
			LLMModel:        "test-model",
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"style-profile",
		"--input", book,
		"--series", "series-a",
		"--samples", "1",
	}, &stdout, &stderr, loader)

	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())
	require.Contains(t, stdout.String(), "style-profile series-a saved")

	store, err := narrative.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	bible, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1, bible.Version)
	require.Equal(t, "Use clear literary French.", bible.StyleGuide)
	require.True(t, narrative.Consistent(bible, "Alice", "Bruno", "tu"))
}
