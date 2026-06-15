package qa

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

const defaultEPUBCheckBinary = "epubcheck"

// EPUBChecker runs the external epubcheck validator against a produced EPUB. It
// supports both a PATH binary and the official jar distribution (java -jar).
type EPUBChecker struct {
	// Binary is the executable to invoke; defaults to "epubcheck".
	Binary string
	// JarPath, when set, runs epubcheck via "java -jar <JarPath>" instead of a
	// PATH binary. This matches the official epubcheck release distribution.
	JarPath string
}

// EPUBCheckResult captures the outcome of an epubcheck invocation.
type EPUBCheckResult struct {
	Passed  bool
	Skipped bool // true when the epubcheck binary is not installed
	Output  string
}

// Check runs epubcheck against epubPath. When the binary is not found it
// returns Skipped (no error) so runs are not blocked in environments without
// epubcheck. A non-zero exit is reported as a failed check (no Go error); only
// unexpected execution failures return an error.
func (c EPUBChecker) Check(ctx context.Context, epubPath string) (EPUBCheckResult, error) {
	cmd, ok := c.command(ctx, epubPath)
	if !ok {
		return EPUBCheckResult{Skipped: true}, nil
	}

	out, err := cmd.CombinedOutput()
	if err == nil {
		return EPUBCheckResult{Passed: true, Output: string(out)}, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return EPUBCheckResult{Passed: false, Output: string(out)}, nil
	}

	return EPUBCheckResult{}, fmt.Errorf("unable to run epubcheck, err: %w", err)
}

// command builds the epubcheck invocation, preferring the jar (java -jar) when
// configured. It returns ok=false when the required executable is unavailable,
// signaling the check should be skipped rather than failed.
func (c EPUBChecker) command(ctx context.Context, epubPath string) (*exec.Cmd, bool) {
	if c.JarPath != "" {
		java, err := exec.LookPath("java")
		if err != nil {
			return nil, false
		}
		return exec.CommandContext(ctx, java, "-jar", c.JarPath, epubPath), true
	}

	binary := c.Binary
	if binary == "" {
		binary = defaultEPUBCheckBinary
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, false
	}
	return exec.CommandContext(ctx, resolved, epubPath), true
}
