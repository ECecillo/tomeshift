package main

import "errors"

var (
	ErrNegativeBudget = errors.New("budget values must be >= 0")
)
