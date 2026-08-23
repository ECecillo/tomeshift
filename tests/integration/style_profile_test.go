package integration_test

import (
	"bytes"
	"context"
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

// TestStyleProfile_MergeKeepsVolumeSynopsesIntact pins the boundary between the
// two passes: `style-profile` owns style, voices and address rules, and must
// not touch the synopses `series-bible` paid for.
//
// The seed prompt tells the model to keep summaries empty, but a prompt is not
// an enforcement. A volunteered summary carries a chapter ID, and ValidateBible
// rejects any summary that has one — so merging it would turn a working bible
// into one literary mode refuses to load, with nothing in the output to say so.
func TestStyleProfile_MergeKeepsVolumeSynopsesIntact(t *testing.T) {
	t.Parallel()

	input := writeEPUB(t, []string{`<h1>One</h1><p>Alice arrives in the capital.</p>`})
	dbPath := filepath.Join(t.TempDir(), "narrative.sqlite")

	// A bible as `series-bible` leaves it: volume-scoped synopses, no chapter IDs.
	seeded := narrative.Bible{
		SeriesID:   "lumen@vol33",
		StyleGuide: "",
		Summaries: []narrative.Summary{
			{VolumeID: "vol1", Summary: "Kai arrive dans la capitale."},
			{VolumeID: "vol2", Summary: "Kai retourne au manoir."},
		},
	}
	store, err := narrative.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	require.NoError(t, store.Save(context.Background(), seeded))
	require.NoError(t, store.Close())

	// The model volunteers a summary despite being told not to.
	srv := openaitest.NewServer(func(openaitest.Request) string {
		return `{
			"series_id":"",
			"style_guide":"Français littéraire fluide.",
			"characters":[{"name":"Kai","register":"familier","notes":"Parle vite.","sample_fr":[]}],
			"address":[{"from":"Kai","to":"Elena","form":"tu","history":[]}],
			"summaries":[{"chapter_id":"ch_0001","summary":"NE DOIT PAS ÊTRE FUSIONNÉ"}],
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
		"style-profile", "--input", input, "--series", "lumen@vol33",
		"--samples", "1", "--merge",
	}, &stdout, &stderr, loader)
	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	reopened, err := narrative.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	merged, found, err := reopened.Load(context.Background(), "lumen@vol33")
	require.NoError(t, err)
	require.True(t, found)

	// The synopses survive untouched, and nothing chapter-scoped crept in.
	require.Len(t, merged.Summaries, 2)
	for _, summary := range merged.Summaries {
		require.Empty(t, summary.ChapterID)
		require.NotEqual(t, "NE DOIT PAS ÊTRE FUSIONNÉ", summary.Summary)
	}

	// What the pass does own did land.
	require.Len(t, merged.Characters, 1)
	require.Len(t, merged.Address, 1)
	require.Equal(t, "Français littéraire fluide.", merged.StyleGuide)

	// And the result is loadable by literary mode.
	require.NoError(t, narrative.ValidateBible(merged))
}
