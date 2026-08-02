package integration_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/llm/openaitest"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
)

// TestSeriesBible_BuildsScopedBiblesFromEveryVolume covers the shape RFC 0008
// asks for end to end: style seeded from all volumes, one synopsis per volume
// produced by map-reduce, summaries scoped to volumes 1..N, and the map step's
// per-chapter summaries kept as a by-product rather than discarded.
func TestSeriesBible_BuildsScopedBiblesFromEveryVolume(t *testing.T) {
	t.Parallel()

	booksDir := t.TempDir()
	writeEPUBAt(t, filepath.Join(booksDir, "volume-01.epub"), []string{
		`<h1>One</h1><p>Alice arrives in the capital.</p>`,
		`<h1>Two</h1><p>Alice meets Bruno at the market.</p>`,
		`<h1>Three</h1><p>Alice loses the insignia.</p>`,
	})
	writeEPUBAt(t, filepath.Join(booksDir, "volume-02.epub"), []string{
		`<h1>One</h1><p>Alice wakes at the manor.</p>`,
		`<h1>Two</h1><p>Bruno confesses the truth.</p>`,
	})
	dbPath := filepath.Join(t.TempDir(), "narrative.sqlite")

	var (
		mu             sync.Mutex
		styleCalls     int
		chapterPrompts []string
		reducePrompts  []string
	)
	srv := openaitest.NewServer(func(req openaitest.Request) string {
		mu.Lock()
		defer mu.Unlock()

		system := req.Messages[0].Content
		user := req.Messages[1].Content

		switch {
		case strings.Contains(system, "You build a French literary translation series bible"):
			styleCalls++
			// The style pass must see prose from every volume: this is where
			// completeness is wanted and where it is nearly free.
			require.Contains(t, user, "Alice arrives in the capital.")
			require.Contains(t, user, "Alice wakes at the manor.")
			return `{
				"series_id":"",
				"style_guide":"Use clear literary French.",
				"characters":[{"name":"Alice","register":"direct","notes":"Plain speech.","sample_fr":[]}],
				"address":[{"from":"Alice","to":"Bruno","form":"tu","history":[]}],
				"summaries":[{"chapter_id":"ch_0001","summary":"MUST BE DISCARDED"}],
				"version":0
			}`

		case strings.Contains(system, "Tu résumes un chapitre de roman"):
			chapterPrompts = append(chapterPrompts, user)
			return fmt.Sprintf("Résumé du chapitre %d.", len(chapterPrompts))

		case strings.Contains(system, "Tu écris le synopsis d'un tome"):
			reducePrompts = append(reducePrompts, user)
			return fmt.Sprintf("Synopsis du tome %d.", len(reducePrompts))
		}

		t.Errorf("unexpected system prompt: %s", system)
		return ""
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
		"series-bible",
		"--input-dir", booksDir,
		"--series", "series-a",
		"--samples", "2",
	}, &stdout, &stderr, loader)
	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	mu.Lock()
	// One sampling pass over the whole series, not one per volume.
	require.Equal(t, 1, styleCalls)
	// The map step reads every chapter of every volume — 3 + 2 spine documents.
	require.Len(t, chapterPrompts, 5)
	require.Len(t, reducePrompts, 2)
	// The reduce step sees every chapter summary of its volume, including the
	// middle one: that is what distinguishes map-reduce from a single pass.
	require.Contains(t, reducePrompts[0], "Résumé du chapitre 1.")
	require.Contains(t, reducePrompts[0], "Résumé du chapitre 2.")
	require.Contains(t, reducePrompts[0], "Résumé du chapitre 3.")
	require.Contains(t, reducePrompts[1], "Résumé du chapitre 4.")
	require.Contains(t, reducePrompts[1], "Résumé du chapitre 5.")
	mu.Unlock()

	store, err := narrative.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// Volume 1's bible must not know how volume 2 ends.
	vol1, found, err := store.Load(t.Context(), "series-a@volume-01")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "Use clear literary French.", vol1.StyleGuide)
	require.Len(t, vol1.Characters, 1)
	require.Equal(t, []narrative.Summary{
		{VolumeID: "volume-01", Summary: "Synopsis du tome 1."},
	}, vol1.Summaries)

	vol2, found, err := store.Load(t.Context(), "series-a@volume-02")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []narrative.Summary{
		{VolumeID: "volume-01", Summary: "Synopsis du tome 1."},
		{VolumeID: "volume-02", Summary: "Synopsis du tome 2."},
	}, vol2.Summaries)

	// The bare series ID is the newest volume's bible, so translating the
	// latest volume needs no scope.
	full, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, vol2.Summaries, full.Summaries)

	// The 5 per-chapter summaries are persisted, and stay out of the bible.
	chapters, err := store.ChapterSummaries(t.Context(), "series-a", "")
	require.NoError(t, err)
	require.Len(t, chapters, 5)
	require.Equal(t, narrative.Summary{
		VolumeID: "volume-02", ChapterID: "ch_0001", Summary: "Résumé du chapitre 4.",
	}, chapters[3])
	// Colliding chapter IDs across volumes both survive.
	require.Equal(t, "Résumé du chapitre 1.", chapters[0].Summary)
	require.Equal(t, "ch_0001", chapters[0].ChapterID)

	require.Contains(t, stdout.String(), "bible series-a@volume-01 saved")
}

// A bible built once and left alone yields byte-identical prompts on a re-run,
// which is what lets the LLM cache serve the second pass for free.
func TestSeriesBible_RebuildIsIdempotent(t *testing.T) {
	t.Parallel()

	booksDir := t.TempDir()
	writeEPUBAt(t, filepath.Join(booksDir, "volume-01.epub"), []string{
		`<h1>One</h1><p>Alice arrives in the capital.</p>`,
	})
	dbPath := filepath.Join(t.TempDir(), "narrative.sqlite")

	srv := openaitest.NewServer(func(req openaitest.Request) string {
		system := req.Messages[0].Content
		switch {
		case strings.Contains(system, "You build a French literary translation series bible"):
			return `{"series_id":"","style_guide":"Stable.","characters":[],"address":[],"summaries":[],"version":0}`
		case strings.Contains(system, "Tu résumes un chapitre de roman"):
			return "Résumé stable."
		default:
			return "Synopsis stable."
		}
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

	run := func() {
		var stdout, stderr bytes.Buffer
		code := commandexec.Execute([]string{
			"series-bible", "--input-dir", booksDir, "--series", "series-a", "--samples", "1",
		}, &stdout, &stderr, loader)
		require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())
	}

	run()
	store, err := narrative.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	first, _, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.Equal(t, 1, first.Version)

	run()
	second, _, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	// Same content, same version: the version identifies the bible, not the
	// number of times it was written.
	require.Equal(t, first, second)
}
