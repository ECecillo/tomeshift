package cli

import (
	"flag"
	"fmt"
	"io"
)

// EvalCommand is the parsed contract for `tomeshift eval`, comparing a
// candidate translation against a human reference EPUB.
type EvalCommand struct {
	Candidate string
	Reference string
	// Segments, when set, switches to reference-free COMET-QE: it scores the
	// source vs target pairs in a run's segments.jsonl (needs a COMET endpoint).
	Segments string
}

// ParseEvalArgs parses flags for `tomeshift eval`.
func ParseEvalArgs(args []string, stderr io.Writer) (EvalCommand, error) {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cmd EvalCommand
	fs.StringVar(&cmd.Candidate, "candidate", "", "chrF mode: path to the produced (translated) EPUB")
	fs.StringVar(&cmd.Reference, "reference", "", "chrF mode: path to a human reference translation EPUB")
	fs.StringVar(&cmd.Segments, "segments", "", "QE mode: path to a run's segments.jsonl (reference-free COMET-QE)")

	if err := fs.Parse(args); err != nil {
		return EvalCommand{}, fmt.Errorf("unable to parse cli flags, err: %w", err)
	}
	if fs.NArg() > 0 {
		return EvalCommand{}, fmt.Errorf("unable to parse cli flags: positional args %v, err: %w", fs.Args(), ErrUnexpectedPositionalArgs)
	}
	// QE mode (segments) and chrF mode (candidate+reference) are mutually
	// exclusive; require one complete set.
	if cmd.Segments == "" {
		if cmd.Candidate == "" {
			return EvalCommand{}, ErrCandidateRequired
		}
		if cmd.Reference == "" {
			return EvalCommand{}, ErrReferenceRequired
		}
	}

	return cmd, nil
}
