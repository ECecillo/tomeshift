package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/translate/nmttest"
)

func TestBatch_SharedTMAcrossBooks(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	inputDir := filepath.Join(t.TempDir(), "books")
	require.NoError(t, os.MkdirAll(inputDir, 0o755))

	// Two books sharing an identical paragraph -> book b reuses book a's TM.
	writeEPUBAt(t, filepath.Join(inputDir, "a.epub"),
		[]string{`<h1>A</h1><p>A shared sentence across the series.</p><p>Unique to A.</p>`})
	writeEPUBAt(t, filepath.Join(inputDir, "b.epub"),
		[]string{`<h1>B</h1><p>A shared sentence across the series.</p><p>Unique to B.</p>`})

	outputDir := filepath.Join(t.TempDir(), "runs")
	loader := func() (config.Config, error) {
		return config.Config{
			NMTProvider: "http", NMTEndpoint: srv.URL,
			LogLevel: "info", TMPath: filepath.Join(t.TempDir(), "shared-tm.sqlite"),
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"batch", "--input-dir", inputDir, "--output-dir", outputDir,
		"--source-lang", "en", "--target-lang", "fr",
	}, &stdout, &stderr, loader)

	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	// Per-book artifacts.
	require.FileExists(t, filepath.Join(outputDir, "a", "translated.epub"))
	require.FileExists(t, filepath.Join(outputDir, "b", "translated.epub"))
	require.FileExists(t, filepath.Join(outputDir, "batch-report.json"))

	data, err := os.ReadFile(filepath.Join(outputDir, "batch-report.json"))
	require.NoError(t, err)
	var summary pipeline.BatchSummary
	require.NoError(t, json.Unmarshal(data, &summary))

	require.Equal(t, 2, summary.Books)
	require.Equal(t, 2, summary.Succeeded)
	require.Equal(t, 0, summary.Failed)
	require.GreaterOrEqual(t, summary.TMHits, 1, "the shared sentence must be reused cross-book")
}

// TestBatch_RunTimeoutDoesNotSkipLaterBooks pins the fix for the per-book
// timeout context leaking into RunSeries' cancellation check: with
// RunTimeoutSeconds set, book 1's context is cancelled in the Done hook, and
// before the fix that cancelled context was still the shared runtime's when the
// loop tested Cancelled() for book 2 — silently dropping every book after the
// first while still exiting Success.
func TestBatch_RunTimeoutDoesNotSkipLaterBooks(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	inputDir := filepath.Join(t.TempDir(), "books")
	require.NoError(t, os.MkdirAll(inputDir, 0o755))
	writeEPUBAt(t, filepath.Join(inputDir, "a.epub"), []string{`<h1>A</h1><p>First book body.</p>`})
	writeEPUBAt(t, filepath.Join(inputDir, "b.epub"), []string{`<h1>B</h1><p>Second book body.</p>`})
	writeEPUBAt(t, filepath.Join(inputDir, "c.epub"), []string{`<h1>C</h1><p>Third book body.</p>`})

	outputDir := filepath.Join(t.TempDir(), "runs")
	loader := func() (config.Config, error) {
		return config.Config{
			NMTProvider: "http", NMTEndpoint: srv.URL,
			LogLevel: "info", TMPath: filepath.Join(t.TempDir(), "tm.sqlite"),
			RunTimeoutSeconds: 120, // generous; the point is that it is non-zero
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"batch", "--input-dir", inputDir, "--output-dir", outputDir,
		"--source-lang", "en", "--target-lang", "fr",
	}, &stdout, &stderr, loader)

	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	data, err := os.ReadFile(filepath.Join(outputDir, "batch-report.json"))
	require.NoError(t, err)
	var summary pipeline.BatchSummary
	require.NoError(t, json.Unmarshal(data, &summary))
	require.Equal(t, 3, summary.Books, "every book must run even with a per-book timeout set")
	require.Equal(t, 3, summary.Succeeded)
	for _, id := range []string{"a", "b", "c"} {
		require.FileExists(t, filepath.Join(outputDir, id, "translated.epub"))
	}
}

func TestBatch_WithReferenceScoring(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	inputDir := filepath.Join(t.TempDir(), "books")
	refDir := filepath.Join(t.TempDir(), "refs")
	require.NoError(t, os.MkdirAll(inputDir, 0o755))
	require.NoError(t, os.MkdirAll(refDir, 0o755))

	writeEPUBAt(t, filepath.Join(inputDir, "tome.epub"),
		[]string{`<h1>Titre</h1><p>Hello world.</p>`})
	// Reference named after the book id (filename without extension).
	writeEPUBAt(t, filepath.Join(refDir, "tome.epub"),
		[]string{`<h1>Titre</h1><p>[fr] Hello world.</p>`})

	outputDir := filepath.Join(t.TempDir(), "runs")
	loader := func() (config.Config, error) {
		return config.Config{
			NMTProvider: "http", NMTEndpoint: srv.URL,
			LogLevel: "info", TMPath: filepath.Join(t.TempDir(), "tm.sqlite"),
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{
		"batch", "--input-dir", inputDir, "--output-dir", outputDir,
		"--source-lang", "en", "--target-lang", "fr", "--reference-dir", refDir,
	}, &stdout, &stderr, loader)
	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())

	data, err := os.ReadFile(filepath.Join(outputDir, "batch-report.json"))
	require.NoError(t, err)
	var summary pipeline.BatchSummary
	require.NoError(t, json.Unmarshal(data, &summary))

	require.Equal(t, 1, summary.EvaluatedBooks)
	require.Len(t, summary.Outcomes, 1)
	require.NotNil(t, summary.Outcomes[0].ChrF)
	// The dev NMT prefixes "[fr] ", matching the reference -> high chrF.
	require.Greater(t, *summary.Outcomes[0].ChrF, 0.5)
	require.Greater(t, summary.AvgChrF, 0.5)
}
