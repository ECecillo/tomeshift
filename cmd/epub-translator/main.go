package main

import (
	"os"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
	"github.com/ECecillo/ebooks-translator/internal/config"
)

func main() {
	// Execute the requested command with configuration read from the
	// environment, and exit with the code it returns.
	os.Exit(commandexec.Execute(os.Args[1:], os.Stdout, os.Stderr, config.Load))
}
