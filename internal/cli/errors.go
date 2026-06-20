package cli

import "errors"

var (
	ErrUnexpectedPositionalArgs = errors.New("unexpected positional args")
	ErrInputRequired            = errors.New("--input is required")
	ErrOutputDirRequired        = errors.New("--output-dir is required")
	ErrSourceLangRequired       = errors.New("--source-lang is required")
	ErrTargetLangRequired       = errors.New("--target-lang is required")
	ErrUnsupportedSourceLang    = errors.New("unsupported --source-lang")
	ErrUnsupportedTargetLang    = errors.New("unsupported --target-lang")
	ErrUnsupportedMode          = errors.New("unsupported --mode")
	ErrEscalationRateRange      = errors.New("--escalation-rate must be between 0 and 1")
	ErrInputDirRequired         = errors.New("--input-dir is required")
	ErrCandidateRequired        = errors.New("--candidate is required")
	ErrReferenceRequired        = errors.New("--reference is required")
	ErrSeriesRequired           = errors.New("--series is required")
)
