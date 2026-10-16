package integration_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/commandexec"
	"github.com/ECecillo/tomeshift/internal/config"
	"github.com/ECecillo/tomeshift/internal/exitcode"
	"github.com/ECecillo/tomeshift/internal/llm/openaitest"
	"github.com/ECecillo/tomeshift/internal/narrative"
	"github.com/ECecillo/tomeshift/internal/pipeline"
	"github.com/ECecillo/tomeshift/internal/segment"
	"github.com/ECecillo/tomeshift/internal/tm"
	"github.com/ECecillo/tomeshift/internal/translate/nmttest"
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
	seedLiteraryBible(t, narrativePath, "series-a")

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

	loader := func() (config.Config, error) {
		return config.Config{
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
	// One translation call per chapter, each reporting 12/7 tokens on the fake
	// provider. The counters are accumulated per batch now, so this is also the
	// assertion that they are accumulated exactly ONCE: adding the chapter's
	// Outcome.Usage on top would read 48/28 here.
	require.Equal(t, 24, report.LLMInputTokens)
	require.Equal(t, 14, report.LLMOutputTokens)
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
	require.Equal(t, []narrative.Summary{{VolumeID: "volume-01", Summary: "Synopsis du tome."}}, bible.Summaries)
	require.True(t, narrative.Consistent(bible, "Alice", "Bruno", "tu"))

	// Decision 4: literary mode must not persist raw NMT drafts. Every row the
	// run left behind is a finished translation, so a TM hit on the next run
	// can never shadow refinement.
	tmStore, err := tm.NewSQLiteStore(tmPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tmStore.Close() })
	for _, source := range []string{"One", "Alice met Bruno.", "Two", "Alice trusted Bruno."} {
		entry, hit, lookupErr := tmStore.Lookup(t.Context(), segment.Hash(segment.Normalize(source)))
		require.NoError(t, lookupErr)
		if !hit {
			continue
		}
		require.Equal(t, "literary", entry.Engine,
			"a literary run must only persist refined translations, never the %q draft", entry.Engine)
	}
}

// TestEstimate_CalibratesOnWhatARunMeasured is the loop closing: a run records
// what its LLM pass consumed, and the next projection stops guessing.
//
// It matters because the guess is not slightly wrong. On a measured chapter the
// character heuristic projected 16,180 output tokens against 53,409 consumed —
// a factor, not a margin — and a per-book cap enforced on the former lets
// through three times the work it thinks it is allowing.
func TestEstimate_CalibratesOnWhatARunMeasured(t *testing.T) {
	t.Parallel()

	input := writeEPUB(t, []string{
		`<h1>One</h1><p>Alice met <em>Bruno</em>.</p>`,
		`<h1>Two</h1><p>Alice trusted Bruno.</p>`,
	})
	runsDir := filepath.Join(t.TempDir(), "runs")
	narrativePath := filepath.Join(t.TempDir(), "narrative.sqlite")
	tmPath := filepath.Join(t.TempDir(), "tm.sqlite")
	seedLiteraryBible(t, narrativePath, "series-a")

	nmt := nmttest.NewServer(func(text string) string { return "[nmt] " + text })
	t.Cleanup(nmt.Close)
	// Answers every requested id, so all four segments come back refined and
	// the token counters describe a complete pass.
	chat := openaitest.NewServer(func(req openaitest.Request) string {
		var out []string
		for _, id := range requestedIDs(req.Messages[1].Content) {
			out = append(out, fmt.Sprintf(`{"id":%q,"target_text":"Traduit %s"}`, id, id))
		}
		return `{"segments":[` + strings.Join(out, ",") + `]}`
	})
	t.Cleanup(chat.Close)

	loader := func() (config.Config, error) {
		return config.Config{
			NMTProvider: "http", NMTEndpoint: nmt.URL, LogLevel: "info",
			TMPath: tmPath, NarrativePath: narrativePath,
			LLMChatProvider: "openai", LLMBaseURL: chat.URL + "/v1", LLMModel: "test-literary",
			// Rates, so the projection is not identically zero.
			LLMInputPerMTokUSD: 3, LLMOutputPerMTokUSD: 15, LLMCacheReadPerMTokUSD: 0.30,
		}, nil
	}

	estimate := func() string {
		var stdout, stderr bytes.Buffer
		code := commandexec.Execute([]string{
			"estimate", "--input", input,
		}, &stdout, &stderr, loader)
		require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())
		return stdout.String()
	}

	// Before any run there is no ledger at all, so nothing is measured — and
	// `estimate` says so rather than quietly projecting on a heuristic.
	first := estimate()
	require.Contains(t, first, "calibration: none yet")
	require.NoFileExists(t, tmPath, "a dry run must not leave a ledger behind")

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"run", "--input", input, "--output-dir", runsDir,
		"--source-lang", "en", "--target-lang", "fr",
		"--run-id", "lit", "--mode", "literary", "--series", "series-a",
	}, &stdout, &stderr, loader)
	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	// The run measured 7 output tokens per call over 2 calls, for 4 segments.
	report := readReport(t, filepath.Join(runsDir, "lit", "report.json"))
	require.Equal(t, 14, report.LLMOutputTokens)
	require.Equal(t, 4, report.LLMPassSegments)
	require.Equal(t, "openai", report.LLMProvider)
	require.Equal(t, "test-literary", report.LLMModel)

	second := estimate()
	require.Contains(t, second, "measured on openai/test-literary")
	require.Contains(t, second, "over 4 segments in 1 past attempt(s)")

	// And the projection moved: 14/4 = 3.5 output tokens per segment, where the
	// heuristic had its own idea. Same book, same rates — only the basis
	// changed, which is the whole point.
	require.NotEqual(t, fullLLMRow(t, first), fullLLMRow(t, second))
}

// requestedIDs pulls the segment ids out of a literary request payload.
var requestedIDPattern = regexp.MustCompile(`"id":"([^"]+)"`)

func requestedIDs(payload string) []string {
	var ids []string
	for _, m := range requestedIDPattern.FindAllStringSubmatch(payload, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// fullLLMRow isolates the projection line the calibration acts on.
func fullLLMRow(t *testing.T, out string) string {
	t.Helper()

	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "full-llm") {
			return line
		}
	}
	t.Fatalf("no full-llm row in estimate output:\n%s", out)
	return ""
}

// TestRun_LiteraryRedraftsAnNMTDraftFromAPreviousRun covers the shadowing
// defect: before decision 4, a raw draft written by an earlier NMT-engine run
// was served as a TM hit and the segment never reached the LLM again.
func TestRun_LiteraryRedraftsAnNMTDraftFromAPreviousRun(t *testing.T) {
	t.Parallel()

	const shadowed = "Alice met Bruno."

	input := writeEPUB(t, []string{`<h1>One</h1><p>` + shadowed + `</p>`})
	runsDir := filepath.Join(t.TempDir(), "runs")
	narrativePath := filepath.Join(t.TempDir(), "narrative.sqlite")
	tmPath := filepath.Join(t.TempDir(), "tm.sqlite")
	seedLiteraryBible(t, narrativePath, "series-a")

	// Seed the TM the way a previous Argos run would have: a raw draft, keyed
	// by the segment's source hash.
	seed, err := tm.NewSQLiteStore(tmPath)
	require.NoError(t, err)
	require.NoError(t, seed.Save(t.Context(), tm.Entry{
		SourceHash: segment.Hash(segment.Normalize(shadowed)),
		SourceText: shadowed,
		TargetText: "[argos] Alice a rencontre Bruno.",
		SourceLang: "en", TargetLang: "fr",
		Engine: "nmt-libretranslate",
	}))
	require.NoError(t, seed.Close())

	nmt := nmttest.NewServer(func(text string) string { return "[nmt] " + text })
	t.Cleanup(nmt.Close)

	var (
		mu       sync.Mutex
		refined  bool
		sawDraft bool
	)
	chat := openaitest.NewServer(func(req openaitest.Request) string {
		mu.Lock()
		defer mu.Unlock()

		user := req.Messages[1].Content
		if !strings.Contains(user, `"id":"ch_0001_p_0002"`) {
			return `{"segments":[]}`
		}
		refined = true
		sawDraft = strings.Contains(user, "[nmt] "+shadowed)
		return `{"segments":[
			{"id":"ch_0001_p_0001","target_text":"Un"},
			{"id":"ch_0001_p_0002","target_text":"Alice rencontra Bruno."}
		]}`
	})
	t.Cleanup(chat.Close)

	loader := func() (config.Config, error) {
		return config.Config{
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
		"run", "--input", input, "--output-dir", runsDir,
		"--source-lang", "en", "--target-lang", "fr",
		"--run-id", "lit", "--mode", "literary", "--series", "series-a",
	}, &stdout, &stderr, loader)
	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	mu.Lock()
	require.True(t, refined, "the shadowed segment must reach the LLM")
	require.True(t, sawDraft, "the LLM must be correcting a fresh draft, not the stale Argos one")
	mu.Unlock()

	chapter := readZipEntry(t, filepath.Join(runsDir, "lit", "translated.epub"), "OEBPS/chap1.xhtml")
	require.Contains(t, chapter, "Alice rencontra Bruno.")
	require.NotContains(t, chapter, "[argos]")
}

func seedLiteraryBible(t *testing.T, path, seriesID string) {
	t.Helper()
	store, err := narrative.NewSQLiteStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Save(t.Context(), narrative.Bible{
		SeriesID:   seriesID,
		StyleGuide: "Use clear literary French.",
		Summaries:  []narrative.Summary{{VolumeID: "volume-01", Summary: "Synopsis du tome."}},
	}))
	require.NoError(t, store.Close())
}
