package integration_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/llm/openaitest"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/translate/nmttest"
)

func TestRun_LiteraryModeEndToEnd(t *testing.T) {
	t.Parallel()

	input := writeEPUB(t, []string{
		`<h1>One</h1><p>Alice met <em>Bruno</em>.</p>`,
		`<h1>Two</h1><p>Alice trusted Bruno.</p>`,
	})
	runsDir := filepath.Join(t.TempDir(), "runs")
	narrativePath := filepath.Join(t.TempDir(), "narrative.sqlite")
	tmPath := filepath.Join(t.TempDir(), "tm.sqlite")

	nmt := nmttest.NewServer(func(text string) string { return "[nmt] " + text })
	t.Cleanup(nmt.Close)

	var (
		mu                     sync.Mutex
		ch2SawChapter1Update   bool
		translationChapterSeen []string
	)
	chat := openaitest.NewServer(func(req openaitest.Request) string {
		mu.Lock()
		defer mu.Unlock()

		system := req.Messages[0].Content
		user := req.Messages[1].Content
		if strings.Contains(system, "You update an existing French literary translation series bible") {
			if strings.Contains(user, "CHAPTER_ID:\nch_0001") {
				return `{
					"series_id":"series-a",
					"style_guide":"Use supple literary French.",
					"characters":[{"name":"Alice","register":"warm","notes":"They are now allies.","sample_fr":[]}],
					"address":[{"from":"Alice","to":"Bruno","form":"tu","history":[{"at_chapter":"ch_0001","note":"They are now allies."}]}],
					"summaries":[{"chapter_id":"ch_0001","summary":"Alice meets Bruno."}],
					"version":1
				}`
			}
			return `{
				"series_id":"series-a",
				"style_guide":"Use supple literary French.",
				"characters":[{"name":"Alice","register":"warm","notes":"They are now allies.","sample_fr":[]}],
				"address":[{"from":"Alice","to":"Bruno","form":"tu","history":[{"at_chapter":"ch_0001","note":"They are now allies."}]}],
				"summaries":[
					{"chapter_id":"ch_0001","summary":"Alice meets Bruno."},
					{"chapter_id":"ch_0002","summary":"Alice trusts Bruno."}
				],
				"version":2
			}`
		}

		if strings.Contains(user, `"id":"ch_0001_p_0001"`) {
			translationChapterSeen = append(translationChapterSeen, "ch_0001")
			return `{"segments":[
				{"id":"ch_0001_p_0001","target_text":"Un"},
				{"id":"ch_0001_p_0002","target_text":"Alice rencontra <x id=\"1\"/>Bruno<x id=\"2\"/>."}
			]}`
		}
		if strings.Contains(user, `"id":"ch_0002_p_0001"`) {
			translationChapterSeen = append(translationChapterSeen, "ch_0002")
			ch2SawChapter1Update = strings.Contains(system, "They are now allies.")
			return `{"segments":[
				{"id":"ch_0002_p_0001","target_text":"Deux"},
				{"id":"ch_0002_p_0002","target_text":"Alice fit confiance à Bruno."}
			]}`
		}

		return `{"segments":[]}`
	})
	t.Cleanup(chat.Close)

	loader := func() (commandexec.RuntimeConfig, error) {
		return commandexec.RuntimeConfig{
			Env:             "test",
			NMTProvider:     "http",
			NMTEndpoint:     nmt.URL,
			LogLevel:        "info",
			TMPath:          tmPath,
			NarrativePath:   narrativePath,
			LLMChatProvider: "openai",
			LLMBaseURL:      chat.URL + "/v1",
			LLMModel:        "test-literary",
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"run",
		"--input", input,
		"--output-dir", runsDir,
		"--source-lang", "en",
		"--target-lang", "fr",
		"--run-id", "lit",
		"--mode", "literary",
		"--series", "series-a",
		"--update-bible", // this test asserts cross-chapter bible accumulation
	}, &stdout, &stderr, loader)

	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())
	require.Contains(t, stdout.String(), "run lit completed")

	mu.Lock()
	require.Equal(t, []string{"ch_0001", "ch_0002"}, translationChapterSeen)
	require.True(t, ch2SawChapter1Update, "chapter 2 prompt should include chapter 1 bible update")
	mu.Unlock()

	outDir := filepath.Join(runsDir, "lit")
	require.FileExists(t, filepath.Join(outDir, "translated.epub"))
	require.FileExists(t, filepath.Join(outDir, "report.json"))
	require.FileExists(t, filepath.Join(outDir, "segments.jsonl"))

	report := readReport(t, filepath.Join(outDir, "report.json"))
	require.Equal(t, pipeline.ModeLiterary, report.Mode)
	require.Equal(t, "series-a", report.SeriesID)
	require.Equal(t, 4, report.NMTSegments)
	require.Equal(t, 4, report.LLMSegments)
	require.Equal(t, 2, report.BibleVersion)
	require.True(t, report.QA.Passed(), "QA must pass: %+v", report.QA)
	require.InDelta(t, 1.0, report.QA.PlaceholderRestoreRatio, 0.0001)

	chapter1 := readZipEntry(t, filepath.Join(outDir, "translated.epub"), "OEBPS/chap1.xhtml")
	require.Contains(t, chapter1, "Alice rencontra")
	require.Contains(t, chapter1, "<em>Bruno</em>")

	store, err := narrative.NewSQLiteStore(narrativePath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	bible, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, bible.Summaries, 2)
	require.True(t, narrative.Consistent(bible, "Alice", "Bruno", "tu"))
}
