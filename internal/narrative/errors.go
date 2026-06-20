package narrative

import "errors"

var (
	ErrEmptySeriesID = errors.New("narrative: series ID is empty")
	ErrEmptySamples  = errors.New("narrative: profile samples are empty")
	ErrInvalidBible  = errors.New("narrative: bible is invalid")
	ErrNilChatClient = errors.New("narrative: chat client is nil")
	ErrNilDatabase   = errors.New("narrative: store has no open database")
)
