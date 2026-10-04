package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/translate/nmttest"
)

// goldenPath holds, per fixture, the sha256 of translated.epub and of
// report.json with its volatile fields removed. It was frozen on the in-process
// engine before RFC 0009 phase 1 and is the acceptance check of that phase: the
// same book must come out byte for byte once the stages are decoupled from the
// process. Regenerate deliberately with UPDATE_GOLDEN=1 — a changed golden is
// a changed output, and must be explained in the PR.
const goldenPath = "testdata/golden.json"

type goldenEntry struct {
	EPUB   string `json:"translated_epub_sha256"`
	Report string `json:"report_sha256"`
}

// volatileReportFields differ between two identical runs and carry no
// information about the translation itself.
var volatileReportFields = []string{"started_at", "ended_at", "output_epub"}

func TestRun_GoldenOutputs(t *testing.T) {
	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	got := make(map[string]goldenEntry, len(fixtures))
	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		input := writeEPUB(t, fixtures[name])
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
			"--run-id", "golden",
		}, &stdout, &stderr, loader)
		require.Equal(t, exitcode.Success, code, "%s: stderr: %s", name, stderr.String())

		outDir := filepath.Join(runsDir, "golden")
		got[name] = goldenEntry{
			EPUB:   fileSHA256(t, filepath.Join(outDir, "translated.epub")),
			Report: stableReportSHA256(t, filepath.Join(outDir, "report.json")),
		}
	}

	if os.Getenv("UPDATE_GOLDEN") != "" {
		data, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
		require.NoError(t, os.WriteFile(goldenPath, append(data, '\n'), 0o644))
		t.Logf("golden rewritten: %s", goldenPath)
		return
	}

	data, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "missing golden; run once with UPDATE_GOLDEN=1")
	var want map[string]goldenEntry
	require.NoError(t, json.Unmarshal(data, &want))
	require.Equal(t, want, got, "run outputs drifted from the frozen golden")
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// stableReportSHA256 hashes report.json re-encoded with sorted keys and the
// volatile fields dropped, so two runs of the same book agree.
func stableReportSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var report map[string]any
	require.NoError(t, json.Unmarshal(data, &report))
	for _, field := range volatileReportFields {
		delete(report, field)
	}
	stable, err := json.Marshal(report) // encoding/json sorts map keys
	require.NoError(t, err)
	sum := sha256.Sum256(stable)
	return hex.EncodeToString(sum[:])
}
