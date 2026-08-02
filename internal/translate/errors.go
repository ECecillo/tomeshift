package translate

import "errors"

var (
	ErrEmptyEndpoint    = errors.New("translate: NMT endpoint is empty")
	ErrProviderStatus   = errors.New("translate: NMT provider returned a non-2xx status")
	ErrEmptyTranslation = errors.New("translate: NMT provider returned empty translated text")
	// ErrShortBatchResponse guards the index alignment between a batch request
	// and its response: a provider that answers fewer texts than it was given
	// would otherwise shift every following translation onto the wrong segment.
	ErrShortBatchResponse = errors.New("translate: NMT provider returned fewer translations than requested")
	// ErrTextTooLong marks a single segment larger than the provider accepts in
	// one request. It cannot be batched around, and splitting it would cut
	// placeholder tokens in half.
	ErrTextTooLong = errors.New("translate: segment exceeds the provider's per-request size limit")
)
