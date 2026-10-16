package integration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/commandexec"
	"github.com/ECecillo/tomeshift/internal/config"
	"github.com/ECecillo/tomeshift/internal/eval/comettest"
	"github.com/ECecillo/tomeshift/internal/exitcode"
)

func TestEval_QEMode(t *testing.T) {
	t.Parallel()

	// Mock COMET service: longer MT relative to src scores higher (stand-in).
	srv := comettest.NewServer(func(src, mt, _ string) float64 {
		if mt == "" {
			return 0
		}
		return 0.9
	})
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	segments := filepath.Join(dir, "segments.jsonl")
	require.NoError(t, os.WriteFile(segments, []byte(
		`{"source_text":"Hello <x id=\"1\"/> world","target_text":"Bonjour <x id=\"1\"/> le monde"}`+"\n"+
			`{"source_text":"The end.","target_text":"La fin."}`+"\n"), 0o600))

	loader := func() (config.Config, error) {
		return config.Config{
			CometEndpoint: srv.URL,
		}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{"eval", "--segments", segments}, &stdout, &stderr, loader)

	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())
	require.Contains(t, stdout.String(), "COMET-QE avg = 0.9000")
	require.FileExists(t, filepath.Join(dir, "qe-eval-report.json"))
}

func TestEval_QEMode_NoEndpoint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	segments := filepath.Join(dir, "segments.jsonl")
	require.NoError(t, os.WriteFile(segments, []byte(`{"source_text":"x","target_text":"y"}`+"\n"), 0o600))

	loader := func() (config.Config, error) {
		return config.Config{}, nil
	}

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute([]string{"eval", "--segments", segments}, &stdout, &stderr, loader)

	require.Equal(t, exitcode.InvalidInputConfig, code)
	require.True(t, strings.Contains(stderr.String(), "COMET_ENDPOINT"))
}
