package cli

import (
	"flag"
	"fmt"
	"io"
)

// EstimateCommand is the parsed contract for `epub-translator estimate`, a
// dry-run that projects translation cost without translating.
type EstimateCommand struct {
	// Input is the source EPUB path provided by --input.
	Input string
	// SourceLang / TargetLang gate supported V1 routing.
	SourceLang string
	TargetLang string
	// EscalationRate is the hybrid-scenario fraction of segments sent to the
	// LLM, in [0,1].
	EscalationRate float64
}

// ParseEstimateArgs parses flags for `epub-translator estimate`.
func ParseEstimateArgs(args []string, stderr io.Writer) (EstimateCommand, error) {
	fs := flag.NewFlagSet("estimate", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cmd EstimateCommand
	fs.StringVar(&cmd.Input, "input", "", "input EPUB file path (required)")
	fs.StringVar(&cmd.SourceLang, "source-lang", "en", "source language code (V1=en)")
	fs.StringVar(&cmd.TargetLang, "target-lang", "fr", "target language code (V1=fr)")
	fs.Float64Var(&cmd.EscalationRate, "escalation-rate", 0.15, "hybrid scenario: fraction of segments escalated to the LLM [0,1]")

	if err := fs.Parse(args); err != nil {
		return EstimateCommand{}, fmt.Errorf("unable to parse cli flags, err: %w", err)
	}
	if fs.NArg() > 0 {
		return EstimateCommand{}, fmt.Errorf("unable to parse cli flags: positional args %v, err: %w", fs.Args(), ErrUnexpectedPositionalArgs)
	}
	if cmd.Input == "" {
		return EstimateCommand{}, ErrInputRequired
	}
	if cmd.SourceLang != "en" {
		return EstimateCommand{}, fmt.Errorf("unable to validate estimate args: source-lang %q is not supported in V1, err: %w", cmd.SourceLang, ErrUnsupportedSourceLang)
	}
	if cmd.TargetLang != "fr" {
		return EstimateCommand{}, fmt.Errorf("unable to validate estimate args: target-lang %q is not supported in V1, err: %w", cmd.TargetLang, ErrUnsupportedTargetLang)
	}
	if cmd.EscalationRate < 0 || cmd.EscalationRate > 1 {
		return EstimateCommand{}, ErrEscalationRateRange
	}

	return cmd, nil
}
