package narrative

import "errors"

var (
	ErrEmptySeriesID = errors.New("narrative: series ID is empty")
	ErrEmptySamples  = errors.New("narrative: profile samples are empty")
	ErrEmptySource   = errors.New("narrative: chapter source text is empty")
	ErrEmptySummary  = errors.New("narrative: model returned an empty summary")
	ErrInvalidBible  = errors.New("narrative: bible is invalid")
	ErrMissingBible  = errors.New("narrative: required bible was not found")
	ErrNilChatClient = errors.New("narrative: chat client is nil")
	ErrNilDatabase   = errors.New("narrative: store has no open database")
)
