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
)
