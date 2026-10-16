package cli

import (
	"flag"
	"fmt"
	"io"
)

// BatchCommand is the parsed contract for `tomeshift batch`, which
// translates every EPUB in a directory through a shared TM and glossary.
type BatchCommand struct {
	InputDir   string
	OutputDir  string
	SourceLang string
	TargetLang string
	Glossary   string
	// ReferenceDir optionally holds human reference translations named
	// <book-id>.epub; when present each produced book is chrF-scored against it.
	ReferenceDir string
	// NoProgress is set by --no-progress to silence the per-book progress bar.
	NoProgress bool
}

// ParseBatchArgs parses flags for `tomeshift batch`.
func ParseBatchArgs(args []string, stderr io.Writer) (BatchCommand, error) {
	fs := flag.NewFlagSet("batch", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cmd BatchCommand
	fs.StringVar(&cmd.InputDir, "input-dir", "", "directory of input .epub files (required)")
	fs.StringVar(&cmd.OutputDir, "output-dir", "", "output directory for run artifacts (required)")
	fs.StringVar(&cmd.SourceLang, "source-lang", "en", "source language code (V1=en)")
	fs.StringVar(&cmd.TargetLang, "target-lang", "fr", "target language code (V1=fr)")
	fs.StringVar(&cmd.Glossary, "glossary", "", "optional shared glossary file path (applied to every book)")
	fs.StringVar(&cmd.ReferenceDir, "reference-dir", "", "optional directory of <book-id>.epub human references for chrF scoring")
	fs.BoolVar(&cmd.NoProgress, "no-progress", false, "disable the per-book progress bar (auto-disabled when stderr is not a terminal)")

	if err := fs.Parse(args); err != nil {
		return BatchCommand{}, fmt.Errorf("unable to parse cli flags, err: %w", err)
	}
	if fs.NArg() > 0 {
		return BatchCommand{}, fmt.Errorf("unable to parse cli flags: positional args %v, err: %w", fs.Args(), ErrUnexpectedPositionalArgs)
	}
	if cmd.InputDir == "" {
		return BatchCommand{}, ErrInputDirRequired
	}
	if cmd.OutputDir == "" {
		return BatchCommand{}, ErrOutputDirRequired
	}
	if cmd.SourceLang != "en" {
		return BatchCommand{}, fmt.Errorf("unable to validate batch args: source-lang %q is not supported in V1, err: %w", cmd.SourceLang, ErrUnsupportedSourceLang)
	}
	if cmd.TargetLang != "fr" {
		return BatchCommand{}, fmt.Errorf("unable to validate batch args: target-lang %q is not supported in V1, err: %w", cmd.TargetLang, ErrUnsupportedTargetLang)
	}

	return cmd, nil
}
