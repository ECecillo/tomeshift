package llm

import "errors"

var (
	ErrEmptyEndpoint     = errors.New("llm: post-edit endpoint is empty")
	ErrEmptyBaseURL      = errors.New("llm: chat base URL is empty")
	ErrProviderStatus    = errors.New("llm: provider returned a non-2xx status")
	ErrEmptyResponse     = errors.New("llm: provider returned empty translated text")
	ErrPlaceholderChange = errors.New("llm: post-edit altered placeholder tokens")
)
