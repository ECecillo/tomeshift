package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/commandexec"
	"github.com/ECecillo/tomeshift/internal/config"
	"github.com/ECecillo/tomeshift/internal/exitcode"
)

func runEvalCmd(t *testing.T, candidate, reference string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := commandexec.Execute(
		[]string{"eval", "--candidate", candidate, "--reference", reference},
		&stdout, &stderr,
		func() (config.Config, error) { return config.Config{}, nil },
	)
	return stdout.String(), stderr.String(), code
}

func TestEval_IdenticalScoresOne(t *testing.T) {
	t.Parallel()

	book := writeEPUB(t, []string{`<h1>Titre</h1><p>Le chat dort sur le canapé.</p>`})
	out, stderr, code := runEvalCmd(t, book, book)

	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr)
	require.Contains(t, out, "chrF = 1.0000")

	report := filepath.Join(filepath.Dir(book), "eval-report.json")
	require.FileExists(t, report)
	data, err := os.ReadFile(report)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, "chrf", got["metric"])
	require.InDelta(t, 1.0, got["score"].(float64), 1e-9)
}

func TestEval_DifferentScoresLower(t *testing.T) {
	t.Parallel()

	candidate := writeEPUB(t, []string{`<h1>Titre</h1><p>Une voiture roule sur l'autoroute.</p>`})
	reference := writeEPUB(t, []string{`<h1>Titre</h1><p>Le chat dort sur le canapé.</p>`})

	out, stderr, code := runEvalCmd(t, candidate, reference)
	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr)
	require.Contains(t, out, "chrF = 0.")
	require.NotContains(t, out, "chrF = 1.0000")
}
