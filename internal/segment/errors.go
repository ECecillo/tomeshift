package segment

import "errors"

var (
	ErrNilDocument        = errors.New("segment: nil xhtml document")
	ErrMissingPlaceholder = errors.New("segment: translated text dropped one or more placeholder tokens")
	// ErrMisorderedPlaceholder covers a translation that returned every token
	// but not in a shape the markup can be rebuilt from: a pair reordered, a
	// token duplicated, or an inline element left unclosed. Presence alone is
	// not enough — rebuilding from a reordered pair produces valid-looking but
	// wrong markup, silently.
	ErrMisorderedPlaceholder = errors.New("segment: translated text returned placeholder tokens out of order")
	// ErrUnknownSegment covers a re-application whose targets reference segment
	// ids the document does not produce: a stale or mismatched translation set.
	// The ids are listed in the wrapping message so the caller can act on them.
	ErrUnknownSegment = errors.New("segment: target references unknown segment id(s)")
)
