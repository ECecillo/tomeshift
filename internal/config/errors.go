package config

import "errors"

// ErrNegativeValue is returned for any rate, allowance or cap configured below
// zero. A negative rate produces a negative projection, which passes every cap
// — so it silently disables the guardrail rather than tightening it.
var ErrNegativeValue = errors.New("config: rates, allowances and caps must be >= 0")
