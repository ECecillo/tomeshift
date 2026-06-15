package translate

import "errors"

var (
	ErrEmptyEndpoint    = errors.New("translate: NMT endpoint is empty")
	ErrProviderStatus   = errors.New("translate: NMT provider returned a non-2xx status")
	ErrEmptyTranslation = errors.New("translate: NMT provider returned empty translated text")
)
