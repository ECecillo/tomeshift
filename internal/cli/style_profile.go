package cli

import (
	"flag"
	"fmt"
	"io"
)

// StyleProfileCommand is the parsed contract for `epub-translator style-profile`.
type StyleProfileCommand struct {
	Input   string
	Series  string
	Samples int
}

// ParseStyleProfileArgs parses flags for `epub-translator style-profile`.
func ParseStyleProfileArgs(args []string, stderr io.Writer) (StyleProfileCommand, error) {
	fs := flag.NewFlagSet("style-profile", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cmd StyleProfileCommand
	fs.StringVar(&cmd.Input, "input", "", "input EPUB file path (required)")
	fs.StringVar(&cmd.Series, "series", "", "series identifier (required)")
	fs.IntVar(&cmd.Samples, "samples", 3, "number of spine documents to sample")

	if err := fs.Parse(args); err != nil {
		return StyleProfileCommand{}, fmt.Errorf("unable to parse cli flags, err: %w", err)
	}
	if fs.NArg() > 0 {
		return StyleProfileCommand{}, fmt.Errorf("unable to parse cli flags: positional args %v, err: %w", fs.Args(), ErrUnexpectedPositionalArgs)
	}
	if cmd.Input == "" {
		return StyleProfileCommand{}, ErrInputRequired
	}
	if cmd.Series == "" {
		return StyleProfileCommand{}, ErrSeriesRequired
	}
	if cmd.Samples < 1 {
		cmd.Samples = 1
	}

	return cmd, nil
}
