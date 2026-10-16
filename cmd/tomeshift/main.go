package main

import (
	"os"

	"github.com/ECecillo/tomeshift/internal/commandexec"
	"github.com/ECecillo/tomeshift/internal/config"
)

func main() {
	// Execute the requested command with configuration read from the
	// environment, and exit with the code it returns.
	os.Exit(commandexec.Execute(os.Args[1:], os.Stdout, os.Stderr, config.Load))
}
