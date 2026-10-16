package cli

import (
	"flag"
	"fmt"
	"io"
)

// SeriesBibleCommand is the parsed contract for `tomeshift series-bible`,
// the pre-translation job that builds a frozen bible from every volume of a
// series. Translation runs consume its output and never write to it.
type SeriesBibleCommand struct {
	// InputDir holds the series' EPUBs; volume order is the sorted filename
	// order, so `volume-01.epub` … `volume-15.epub` sorts correctly.
	InputDir string
	// Series is the base identifier. Per-volume bibles are stored under
	// `<series>@<volume>`; the full series also lands on `<series>`.
	Series string
	// Samples is how many spine documents feed the style/voice/address pass
	// per volume. That pass draws on every volume — completeness is wanted
	// there and is nearly free.
	Samples int
	// Merge enriches existing bibles instead of overwriting them, keeping a
	// curated style guide in place.
	Merge bool
	// DryRun prints the cost projection and stops before the first provider
	// call. A bible build is the largest job this CLI issues — one call per
	// chapter across every volume — and it is the one job `estimate` cannot
	// price, since `estimate` projects translating a book.
	DryRun bool
}

// ParseSeriesBibleArgs parses flags for `tomeshift series-bible`.
func ParseSeriesBibleArgs(args []string, stderr io.Writer) (SeriesBibleCommand, error) {
	fs := flag.NewFlagSet("series-bible", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cmd SeriesBibleCommand
	fs.StringVar(&cmd.InputDir, "input-dir", "", "directory holding the series' EPUB volumes (required)")
	fs.StringVar(&cmd.Series, "series", "", "series identifier (required)")
	fs.IntVar(&cmd.Samples, "samples", 3, "spine documents sampled per volume for style, voices and address rules")
	fs.BoolVar(&cmd.Merge, "merge", false, "enrich existing bibles instead of overwriting them")
	fs.BoolVar(&cmd.DryRun, "dry-run", false, "print the cost projection and exit without calling the provider")

	if err := fs.Parse(args); err != nil {
		return SeriesBibleCommand{}, fmt.Errorf("unable to parse cli flags, err: %w", err)
	}
	if fs.NArg() > 0 {
		return SeriesBibleCommand{}, fmt.Errorf("unable to parse cli flags: positional args %v, err: %w", fs.Args(), ErrUnexpectedPositionalArgs)
	}
	if cmd.InputDir == "" {
		return SeriesBibleCommand{}, ErrInputDirRequired
	}
	if cmd.Series == "" {
		return SeriesBibleCommand{}, ErrSeriesRequired
	}
	if cmd.Samples < 1 {
		cmd.Samples = 1
	}

	return cmd, nil
}
