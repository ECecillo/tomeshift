package config

import "errors"

// ErrNegativeBudget is returned when a spend cap is configured below zero,
// which would silently disable the guardrail rather than tighten it.
var ErrNegativeBudget = errors.New("config: budget values must be >= 0")

// ErrMissingAPIKey is returned when a provider that cannot work without
// credentials is selected without them.
var ErrMissingAPIKey = errors.New("config: provider requires an API key")
