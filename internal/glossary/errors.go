package glossary

import "errors"

var (
	ErrEmptyTermEN     = errors.New("glossary: entry has empty source term")
	ErrEmptyTermFR     = errors.New("glossary: entry has empty target term")
	ErrConflictingTerm = errors.New("glossary: source term mapped to conflicting target terms")
)
