package narrative

import "errors"

var (
	ErrEmptySeriesID = errors.New("narrative: series ID is empty")
	ErrNilDatabase   = errors.New("narrative: store has no open database")
)
