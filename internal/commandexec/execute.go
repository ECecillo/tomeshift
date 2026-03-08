package commandexec

import (
	"fmt"
	"io"
	"os"

	"github.com/ECecillo/ebooks-translator/internal/cli"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
)

type RuntimeConfig struct {
	// Env carries runtime environment context and is used in run output/logging.
	Env string
	// NMTProvider identifies which translation provider is selected for this run.
	NMTProvider string
}

// ConfigLoader loads runtime configuration for command execution. It is
// injected to keep command orchestration decoupled from config sources.
type ConfigLoader func() (RuntimeConfig, error)

// Execute dispatches CLI commands and returns process exit codes consumed by
// main() via os.Exit.
func Execute(args []string, stdout, stderr io.Writer, loadConfig ConfigLoader) int {
	if len(args) == 0 {
		cli.PrintRootUsage(stderr)
		return exitcode.InvalidInputConfig
	}

	switch args[0] {
	case "run":
		cmd, err := cli.ParseRunArgs(args[1:], stderr)
		if err != nil {
			writef(stderr, "invalid run args: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		cfg, err := loadConfig()
		if err != nil {
			writef(stderr, "%v\n", err)
			return exitcode.InvalidInputConfig
		}
		writef(stdout, "run command wired. input=%s output_dir=%s source_lang=%s target_lang=%s env=%s nmt_provider=%s\n",
			cmd.Input, cmd.OutputDir, cmd.SourceLang, cmd.TargetLang, cfg.Env, cfg.NMTProvider)
		return exitcode.Success
	case "-h", "--help", "help":
		cli.PrintRootUsage(stdout)
		return exitcode.Success
	default:
		writef(stderr, "unknown command: %s\n", args[0])
		cli.PrintRootUsage(stderr)
		return exitcode.InvalidInputConfig
	}
}

func writef(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write error: %v\n", err)
	}
}
