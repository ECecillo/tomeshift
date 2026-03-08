package main

import (
	"os"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
)

func main() {
	// Execute run entrypoint method with given go flags and exit program with
	// a defined exit code.
	os.Exit(commandexec.Execute(os.Args[1:], os.Stdout, os.Stderr, loadRuntimeConfig))
}

// loadRuntimeConfig adapts app config into the command execution contract so
// command orchestration stays independent from concrete config types.
func loadRuntimeConfig() (commandexec.RuntimeConfig, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return commandexec.RuntimeConfig{}, err
	}
	return commandexec.RuntimeConfig{
		Env:         cfg.Env,
		NMTProvider: cfg.NMTProvider,
	}, nil
}
