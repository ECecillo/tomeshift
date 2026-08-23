package cli

import (
	"flag"
	"fmt"
	"io"
)

type RunCommand struct {
	// Input is the source EPUB path provided by --input, used by the run
	// pipeline as the file to parse and translate.
	Input string
	// OutputDir is the path provided by --output-dir, used as the base
	// location for run artifacts (translated EPUB and reports).
	OutputDir string
	// SourceLang is provided by --source-lang, used to enforce supported
	// V1 language routing before translation starts.
	SourceLang string
	// TargetLang is provided by --target-lang, used to enforce supported
	// V1 language routing before translation starts.
	TargetLang string
	// Glossary is an optional path from --glossary, used to load run-level
	// term overrides when glossary checks are enabled.
	Glossary string
	// RunID is an optional override from --run-id, used to control artifact
	// directory naming and run traceability.
	RunID string
	// Mode selects the translation pipeline mode: nmt (default) or literary.
	Mode string
	// Series identifies the narrative bible for literary mode.
	Series string
	// UpdateBible, when set, lets literary mode evolve the series bible during
	// the run (each chapter feeds the next). Off by default and not part of any
	// recommended workflow: it is mutually exclusive with a cached prefix. A
	// bible that changes every chapter rewrites the cacheable prefix once per
	// chapter instead of once per run, and leaves the bible on disk different
	// from the one the next run starts with, so re-runs stop being free and
	// stop being reproducible. Build the bible up front with `series-bible`.
	UpdateBible bool
	// Resume is set by --resume, used to request continuation from a prior
	// run state instead of starting from scratch.
	Resume bool
	// NoProgress is set by --no-progress to silence the terminal progress bar.
	// The bar already draws only on a terminal, so this is for the case the
	// detection cannot cover: a terminal whose output someone is reading as
	// text anyway (a captured session, a screen reader).
	NoProgress bool
}

// ParseRunArgs parses and validates flags for `epub-translator run`, producing a
// normalized RunCommand that downstream execution uses as the run contract.
func ParseRunArgs(args []string, stderr io.Writer) (RunCommand, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cmd RunCommand
	fs.StringVar(&cmd.Input, "input", "", "input EPUB file path (required)")
	fs.StringVar(&cmd.OutputDir, "output-dir", "", "output directory path (required)")
	fs.StringVar(&cmd.SourceLang, "source-lang", "", "source language code (required, V1=en)")
	fs.StringVar(&cmd.TargetLang, "target-lang", "", "target language code (required, V1=fr)")
	fs.StringVar(&cmd.Glossary, "glossary", "", "optional glossary file path")
	fs.StringVar(&cmd.RunID, "run-id", "", "optional run ID override")
	fs.StringVar(&cmd.Mode, "mode", "nmt", "translation mode: nmt or literary")
	fs.StringVar(&cmd.Series, "series", "", "series identifier for literary mode")
	fs.BoolVar(&cmd.UpdateBible, "update-bible", false, "literary mode: evolve the series bible during the run (default: frozen)")
	fs.BoolVar(&cmd.Resume, "resume", false, "resume a previous run")
	fs.BoolVar(&cmd.NoProgress, "no-progress", false, "disable the terminal progress bar (auto-disabled when stderr is not a terminal)")

	if err := fs.Parse(args); err != nil {
		return RunCommand{}, fmt.Errorf("unable to parse cli flags, err: %w", err)
	}
	if fs.NArg() > 0 {
		return RunCommand{}, fmt.Errorf("unable to parse cli flags: positional args %v, err: %w", fs.Args(), ErrUnexpectedPositionalArgs)
	}
	if cmd.Input == "" {
		return RunCommand{}, ErrInputRequired
	}
	if cmd.OutputDir == "" {
		return RunCommand{}, ErrOutputDirRequired
	}
	if cmd.SourceLang == "" {
		return RunCommand{}, ErrSourceLangRequired
	}
	if cmd.TargetLang == "" {
		return RunCommand{}, ErrTargetLangRequired
	}
	if cmd.SourceLang != "en" {
		return RunCommand{}, fmt.Errorf("unable to validate run args: source-lang %q is not supported in V1, err: %w", cmd.SourceLang, ErrUnsupportedSourceLang)
	}
	if cmd.TargetLang != "fr" {
		return RunCommand{}, fmt.Errorf("unable to validate run args: target-lang %q is not supported in V1, err: %w", cmd.TargetLang, ErrUnsupportedTargetLang)
	}
	if cmd.Mode == "" {
		cmd.Mode = "nmt"
	}
	if cmd.Mode != "nmt" && cmd.Mode != "literary" {
		return RunCommand{}, fmt.Errorf("unable to validate run args: mode %q is not supported, err: %w", cmd.Mode, ErrUnsupportedMode)
	}
	if cmd.Mode == "literary" && cmd.Series == "" {
		return RunCommand{}, ErrSeriesRequired
	}

	return cmd, nil
}

// PrintRootUsage writes root help text to guide users toward the supported V1
// command surface and required run flags.
func PrintRootUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  epub-translator run --input <file.epub> --output-dir <dir> --source-lang en --target-lang fr [--mode nmt|literary] [--series <id>] [--glossary <file>] [--run-id <id>] [--resume] [--no-progress]")
	_, _ = fmt.Fprintln(w, "  epub-translator estimate --input <file.epub> [--escalation-rate 0.15]")
	_, _ = fmt.Fprintln(w, "  epub-translator batch --input-dir <dir> --output-dir <dir> [--glossary <file>] [--no-progress]")
	_, _ = fmt.Fprintln(w, "  epub-translator eval --candidate <out.epub> --reference <human-fr.epub>")
	_, _ = fmt.Fprintln(w, "  epub-translator glossary-suggest --input <file.epub> [--min-count 3] [--out glossary.json]")
	_, _ = fmt.Fprintln(w, "  epub-translator style-profile --input <file.epub> --series <id> [--samples 3]")
	_, _ = fmt.Fprintln(w, "  epub-translator series-bible --input-dir <dir> --series <id> [--samples 3] [--merge]")
}
