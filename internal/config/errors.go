package config

import "errors"

// ErrNegativeValue is returned for any rate, allowance or cap configured below
// zero. A negative rate produces a negative projection, which passes every cap
// — so it silently disables the guardrail rather than tightening it.
var ErrNegativeValue = errors.New("config: rates, allowances and caps must be >= 0")

// ErrMissingAPIKey is returned when a provider that cannot work without
// credentials is selected without them.
var ErrMissingAPIKey = errors.New("config: provider requires an API key")
