package qa

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEPUBChecker_SkipWhenBinaryMissing(t *testing.T) {
	t.Parallel()

	checker := EPUBChecker{Binary: "epubcheck-definitely-not-installed-xyz"}
	res, err := checker.Check(t.Context(), "/tmp/whatever.epub")

	require.NoError(t, err)
	require.True(t, res.Skipped)
	require.False(t, res.Passed)
}

func TestEPUBChecker_Passes(t *testing.T) {
	t.Parallel()

	// "true" always exits 0, standing in for a clean epubcheck run.
	checker := EPUBChecker{Binary: "true"}
	res, err := checker.Check(t.Context(), "/tmp/whatever.epub")

	require.NoError(t, err)
	require.False(t, res.Skipped)
	require.True(t, res.Passed)
}

func TestEPUBChecker_JarMode(t *testing.T) {
	t.Parallel()

	// With a jar path set, the checker invokes "java -jar". Pointing at a
	// missing jar makes java exit non-zero -> reported as a failed check (no
	// Go error), proving the jar code path is taken.
	checker := EPUBChecker{JarPath: "/nonexistent/epubcheck.jar"}
	res, err := checker.Check(t.Context(), "/tmp/whatever.epub")

	require.NoError(t, err)
	if _, lookErr := exec.LookPath("java"); lookErr != nil {
		require.True(t, res.Skipped, "no java on PATH -> skipped")
		return
	}
	require.False(t, res.Skipped, "java present -> jar path attempted")
	require.False(t, res.Passed)
}

func TestEPUBChecker_FailsOnNonZeroExit(t *testing.T) {
	t.Parallel()

	// "false" always exits 1, standing in for epubcheck validation errors.
	checker := EPUBChecker{Binary: "false"}
	res, err := checker.Check(t.Context(), "/tmp/whatever.epub")

	require.NoError(t, err)
	require.False(t, res.Skipped)
	require.False(t, res.Passed)
}
