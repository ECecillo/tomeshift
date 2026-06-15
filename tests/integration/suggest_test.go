package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
)

func TestGlossarySuggest_WritesSkeleton(t *testing.T) {
	t.Parallel()

	book := writeEPUB(t, []string{
		`<h1>One</h1><p>The hobbit met Gandalf. Later Gandalf spoke to Gandalf.</p>` +
			`<p>Frodo saw Gandalf, and Frodo greeted Frodo again.</p>`,
	})
	out := filepath.Join(t.TempDir(), "glossary.json")

	var stdout, stderr bytes.Buffer
	code := commandexec.Execute(
		[]string{"glossary-suggest", "--input", book, "--min-count", "2", "--out", out},
		&stdout, &stderr,
		func() (commandexec.RuntimeConfig, error) { return commandexec.RuntimeConfig{}, nil },
	)
	require.Equal(t, exitcode.Success, code, "stderr: %s", stderr.String())
	require.Contains(t, stdout.String(), "Gandalf")

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	var terms []struct {
		EN     string `json:"term_en"`
		FR     string `json:"term_fr"`
		Locked bool   `json:"locked"`
	}
	require.NoError(t, json.Unmarshal(data, &terms))
	require.NotEmpty(t, terms)

	var found bool
	for _, term := range terms {
		require.Equal(t, term.EN, term.FR, "skeleton defaults term_fr to term_en")
		require.True(t, term.Locked)
		if term.EN == "Gandalf" {
			found = true
		}
	}
	require.True(t, found, "Gandalf should be a suggested locked term")
}
