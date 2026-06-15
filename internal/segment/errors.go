package segment

import "errors"

var (
	ErrNilDocument        = errors.New("segment: nil xhtml document")
	ErrMissingPlaceholder = errors.New("segment: translated text dropped one or more placeholder tokens")
)
