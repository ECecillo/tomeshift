package cli

import (
	"flag"
	"fmt"
	"io"
)

// EvalCommand is the parsed contract for `epub-translator eval`, comparing a
// candidate translation against a human reference EPUB.
type EvalCommand struct {
	Candidate string
	Reference string
}

// ParseEvalArgs parses flags for `epub-translator eval`.
func ParseEvalArgs(args []string, stderr io.Writer) (EvalCommand, error) {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cmd EvalCommand
	fs.StringVar(&cmd.Candidate, "candidate", "", "path to the produced (translated) EPUB (required)")
	fs.StringVar(&cmd.Reference, "reference", "", "path to a human reference translation EPUB (required)")

	if err := fs.Parse(args); err != nil {
		return EvalCommand{}, fmt.Errorf("unable to parse cli flags, err: %w", err)
	}
	if fs.NArg() > 0 {
		return EvalCommand{}, fmt.Errorf("unable to parse cli flags: positional args %v, err: %w", fs.Args(), ErrUnexpectedPositionalArgs)
	}
	if cmd.Candidate == "" {
		return EvalCommand{}, ErrCandidateRequired
	}
	if cmd.Reference == "" {
		return EvalCommand{}, ErrReferenceRequired
	}

	return cmd, nil
}
