package cli

import (
	"flag"
	"fmt"
	"io"
)

// SuggestCommand is the parsed contract for `epub-translator glossary-suggest`.
type SuggestCommand struct {
	Input    string
	Out      string
	MinCount int
}

// ParseSuggestArgs parses flags for `epub-translator glossary-suggest`.
func ParseSuggestArgs(args []string, stderr io.Writer) (SuggestCommand, error) {
	fs := flag.NewFlagSet("glossary-suggest", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cmd SuggestCommand
	fs.StringVar(&cmd.Input, "input", "", "input EPUB file path (required)")
	fs.StringVar(&cmd.Out, "out", "", "optional path to write a glossary JSON skeleton")
	fs.IntVar(&cmd.MinCount, "min-count", 3, "minimum occurrences for a candidate")

	if err := fs.Parse(args); err != nil {
		return SuggestCommand{}, fmt.Errorf("unable to parse cli flags, err: %w", err)
	}
	if fs.NArg() > 0 {
		return SuggestCommand{}, fmt.Errorf("unable to parse cli flags: positional args %v, err: %w", fs.Args(), ErrUnexpectedPositionalArgs)
	}
	if cmd.Input == "" {
		return SuggestCommand{}, ErrInputRequired
	}
	if cmd.MinCount < 1 {
		cmd.MinCount = 1
	}

	return cmd, nil
}
