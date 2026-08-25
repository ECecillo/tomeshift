package llm

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	ErrEmptyEndpoint     = errors.New("llm: post-edit endpoint is empty")
	ErrEmptyBaseURL      = errors.New("llm: chat base URL is empty")
	ErrProviderStatus    = errors.New("llm: provider returned a non-2xx status")
	ErrEmptyResponse     = errors.New("llm: provider returned empty translated text")
	ErrPlaceholderChange = errors.New("llm: post-edit altered placeholder tokens")
	// ErrRateLimited marks a refusal to serve rather than a refusal to answer:
	// the quota or the rate is exhausted, and the next call will fail the same
	// way. It is wrapped alongside ErrProviderStatus on an HTTP 429, so a caller
	// that only cares that the provider said no still matches.
	//
	// The distinction is load-bearing for the literary pass, where a failed
	// batch normally degrades to its NMT draft and the chapter carries on. That
	// is right for one bad reply and wrong for an exhausted seat: it would walk
	// the rest of the book failing every batch, emit a book that looks finished
	// but is half draft, and persist none of it — literary mode does not save
	// drafts — so there would be nothing to resume from either.
	ErrRateLimited = errors.New("llm: provider rate limit or quota reached")
)

// statusError describes a non-2xx provider response, naming a 429 as a rate
// limit as well. Both sentinels are wrapped so existing callers keep matching
// ErrProviderStatus.
func statusError(status int, format string, args ...any) error {
	if status == http.StatusTooManyRequests {
		return fmt.Errorf(format+", err: %w %w", append(args, ErrProviderStatus, ErrRateLimited)...)
	}
	return fmt.Errorf(format+", err: %w", append(args, ErrProviderStatus)...)
}
