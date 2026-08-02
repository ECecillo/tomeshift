package integration_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/translate/nmttest"
)

// fixtures maps a fixture name to its XHTML chapter bodies (in spine order).
var fixtures = map[string][]string{
	"single-chapter": {
		`<h1>Chapter One</h1><p>Hello world.</p>`,
	},
	"inline-markup": {
		`<h1>Title</h1><p>A <em>bold</em> and <strong>strong</strong> claim.</p><p>Plain paragraph.</p>`,
	},
	"multi-chapter": {
		`<h1>One</h1><p>First chapter body.</p>`,
		`<h1>Two</h1><p>Second chapter body.</p>`,
	},
}

func writeEPUB(t *testing.T, chapters []string) string {
	t.Helper()
	return writeEPUBAt(t, filepath.Join(t.TempDir(), "source.epub"), chapters)
}

func writeEPUBAt(t *testing.T, path string, chapters []string) string {
	t.Helper()

	manifestItems := &bytes.Buffer{}
	spineItems := &bytes.Buffer{}
	files := map[string]string{
		"mimetype": "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
	}

	for i, body := range chapters {
		id := "chap" + string(rune('1'+i))
		href := id + ".xhtml"
		manifestItems.WriteString(`<item id="` + id + `" href="` + href + `" media-type="application/xhtml+xml"/>`)
		spineItems.WriteString(`<itemref idref="` + id + `"/>`)
		files["OEBPS/"+href] = `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body>` + body + `</body></html>`
	}

	files["OEBPS/content.opf"] = `<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf">
  <manifest>` + manifestItems.String() + `</manifest>
  <spine>` + spineItems.String() + `</spine>
</package>`

	f, err := os.Create(path)
	require.NoError(t, err)

	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	require.NoError(t, err)
	_, err = w.Write([]byte(files["mimetype"]))
	require.NoError(t, err)

	for name, content := range files {
		if name == "mimetype" {
			continue
		}
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return path
}

func TestRun_FixtureEPUBs(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil) // deterministic "[fr] " prefix
	t.Cleanup(srv.Close)

	for name, chapters := range fixtures {
		name, chapters := name, chapters
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			input := writeEPUB(t, chapters)
			runsDir := filepath.Join(t.TempDir(), "runs")

			loader := func() (config.Config, error) {
				return config.Config{
					NMTProvider: "http",
					NMTEndpoint: srv.URL,
					LogLevel:    "info",
					TMPath:      filepath.Join(t.TempDir(), "tm.sqlite"),
				}, nil
			}

			var stdout, stderr bytes.Buffer
			code := commandexec.Execute([]string{
				"run",
				"--input", input,
				"--output-dir", runsDir,
				"--source-lang", "en",
				"--target-lang", "fr",
				"--run-id", "itest",
			}, &stdout, &stderr, loader)

			require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

			outDir := filepath.Join(runsDir, "itest")
			require.FileExists(t, filepath.Join(outDir, "translated.epub"))
			require.FileExists(t, filepath.Join(outDir, "report.json"))
			require.FileExists(t, filepath.Join(outDir, "segments.jsonl"))
			require.FileExists(t, filepath.Join(outDir, "logs.ndjson"))
			require.FileExists(t, filepath.Join(outDir, "metrics.json"))

			report := readReport(t, filepath.Join(outDir, "report.json"))
			require.True(t, report.QA.Passed(), "QA must pass: %+v", report.QA)
			require.Equal(t, 0, report.QA.MissingSpineDocs)
			require.InDelta(t, 1.0, report.QA.PlaceholderRestoreRatio, 0.0001)
			require.GreaterOrEqual(t, report.TotalSegments, len(chapters))
			require.Equal(t, "itest", report.RunID)

			// The metrics list in docs/ARCHITECTURE.md §10 is a contract; this
			// is what keeps it one. nmt_chars in particular is not derivable
			// from cost: the provider's free tier is denominated in characters.
			metrics := readMetrics(t, filepath.Join(outDir, "metrics.json"))
			for _, name := range []string{
				"translation_segments_total",
				"translation_nmt_chars_total",
				"translation_cache_hit_ratio",
				"translation_llm_escalation_ratio",
				"translation_cost_usd_total",
				"translation_placeholder_restore_ratio",
				"epub_validation_failures_total",
			} {
				require.Contains(t, metrics, name, "metrics.json must always carry %s", name)
			}
			require.Equal(t, float64(report.NMTChars), metrics["translation_nmt_chars_total"])
			require.Positive(t, metrics["translation_nmt_chars_total"])

			// Translated text is present and inline markup survived.
			chapter := readZipEntry(t, filepath.Join(outDir, "translated.epub"), "OEBPS/chap1.xhtml")
			require.Contains(t, chapter, "[fr] ")
			if name == "inline-markup" {
				require.Contains(t, chapter, "<em>bold</em>")
				require.Contains(t, chapter, "<strong>strong</strong>")
			}
		})
	}
}

func TestRun_GlossaryEnforcement(t *testing.T) {
	t.Parallel()

	// A translator that "translates" proper nouns wrongly; glossary must still
	// guarantee the canonical French term appears in the output.
	srv := nmttest.NewServer(func(text string) string {
		return strings.ReplaceAll(text, "Bilbo", "WRONG")
	})
	t.Cleanup(srv.Close)

	input := writeEPUB(t, []string{`<h1>Story</h1><p>Bilbo went home.</p>`})
	runsDir := filepath.Join(t.TempDir(), "runs")

	glossaryPath := filepath.Join(t.TempDir(), "glossary.json")
	require.NoError(t, os.WriteFile(glossaryPath,
		[]byte(`[{"term_en":"Bilbo","term_fr":"Bilbon","locked":true}]`), 0o600))

	loader := func() (config.Config, error) {
		return config.Config{
			NMTProvider: "http", NMTEndpoint: srv.URL,
			LogLevel: "info", TMPath: filepath.Join(t.TempDir(), "tm.sqlite"),
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"run", "--input", input, "--output-dir", runsDir,
		"--source-lang", "en", "--target-lang", "fr",
		"--run-id", "gloss", "--glossary", glossaryPath,
	}, &stdout, &stderr, loader)

	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	outDir := filepath.Join(runsDir, "gloss")
	report := readReport(t, filepath.Join(outDir, "report.json"))
	require.Equal(t, 0, report.GlossaryViolations)

	chapter := readZipEntry(t, filepath.Join(outDir, "translated.epub"), "OEBPS/chap1.xhtml")
	require.Contains(t, chapter, "Bilbon", "locked term must be enforced")
	require.NotContains(t, chapter, "WRONG", "provider's wrong proper noun must not survive")
}

func TestRun_ResumeIsIdempotent(t *testing.T) {
	t.Parallel()

	// Count provider calls to prove the resumed run makes none.
	var calls int
	srv := nmttest.NewServer(func(text string) string {
		calls++
		return "[fr] " + text
	})
	t.Cleanup(srv.Close)

	input := writeEPUB(t, []string{`<h1>One</h1><p>First chapter body.</p>`})
	runsDir := filepath.Join(t.TempDir(), "runs")
	sharedTM := filepath.Join(t.TempDir(), "tm.sqlite") // persists across runs

	loader := func() (config.Config, error) {
		return config.Config{
			NMTProvider: "http", NMTEndpoint: srv.URL,
			LogLevel: "info", TMPath: sharedTM,
		}, nil
	}

	run := func(runID string, resume bool) pipeline.RunResult {
		args := []string{
			"run", "--input", input, "--output-dir", runsDir,
			"--source-lang", "en", "--target-lang", "fr", "--run-id", runID,
		}
		if resume {
			args = append(args, "--resume")
		}
		var stdout, stderr bytes.Buffer
		code := commandexec.Execute(args, &stdout, &stderr, loader)
		require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())
		return readReport(t, filepath.Join(runsDir, runID, "report.json"))
	}

	first := run("first", false)
	require.Equal(t, 2, first.NMTSegments)
	require.Equal(t, 0, first.TMHits)
	callsAfterFirst := calls

	// Resume over the same book + shared TM: no new provider calls.
	second := run("resumed", true)
	require.Equal(t, 0, second.NMTSegments, "resume must not re-translate")
	require.Equal(t, 2, second.TMHits)
	require.Equal(t, callsAfterFirst, calls, "no duplicate provider calls on resume")

	// Output is byte-identical across the original and resumed runs.
	require.Equal(t,
		readZipEntry(t, filepath.Join(runsDir, "first", "translated.epub"), "OEBPS/chap1.xhtml"),
		readZipEntry(t, filepath.Join(runsDir, "resumed", "translated.epub"), "OEBPS/chap1.xhtml"),
	)
}

// TestEstimate_DryRun also pins that a dry run needs no NMT endpoint: it never
// translates, so requiring one only ever refused to start.
func TestEstimate_DryRun(t *testing.T) {
	t.Parallel()

	input := writeEPUB(t, []string{`<h1>One</h1><p>Hello world.</p><p>Second paragraph here.</p>`})

	loader := func() (config.Config, error) {
		return config.Config{
			LLMInputPerMTokUSD: 3, LLMOutputPerMTokUSD: 15, LLMOutputExpansion: 1.15,
			LLMPromptOverheadToks: 100,
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"estimate", "--input", input, "--escalation-rate", "0.2",
	}, &stdout, &stderr, loader)

	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	out := stdout.String()
	// No translation happened (endpoint unused), but the projection prints.
	require.Contains(t, out, "nmt-only")
	require.Contains(t, out, "hybrid")
	require.Contains(t, out, "full-llm")
	require.Contains(t, out, "segments")
}

func readMetrics(t *testing.T, path string) map[string]float64 {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var metrics map[string]float64
	require.NoError(t, json.Unmarshal(raw, &metrics))
	return metrics
}

func readReport(t *testing.T, path string) pipeline.RunResult {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var result pipeline.RunResult
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func readZipEntry(t *testing.T, zipPath, entry string) string {
	t.Helper()

	zr, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = zr.Close() })

	for _, f := range zr.File {
		if f.Name == entry {
			rc, err := f.Open()
			require.NoError(t, err)
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(rc)
			require.NoError(t, err)
			return string(data)
		}
	}
	t.Fatalf("entry %q not found", entry)
	return ""
}

func TestRun_MissingNMTEndpointFailsBeforeTranslating(t *testing.T) {
	t.Parallel()

	input := writeEPUB(t, []string{`<h1>One</h1><p>Hello world.</p>`})
	loader := func() (config.Config, error) {
		return config.Config{NMTProvider: "http", TMPath: filepath.Join(t.TempDir(), "tm.sqlite")}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"run", "--input", input, "--output-dir", filepath.Join(t.TempDir(), "runs"),
		"--source-lang", "en", "--target-lang", "fr",
	}, &stdout, &stderr, loader)

	// The setting is named, and nothing was extracted or parsed first.
	require.Equal(t, exitcode.InvalidInputConfig, code)
	require.Contains(t, stderr.String(), "EPUB_TRANSLATOR_NMT_ENDPOINT")
}
