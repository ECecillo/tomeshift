package translate

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultMaxAttempts = 3
	defaultBaseDelay   = 200 * time.Millisecond
)

// RetryingTranslator wraps a Translator with bounded retries and linear
// backoff. Translation is idempotent, so retrying transient failures is safe.
type RetryingTranslator struct {
	Inner       Translator
	MaxAttempts int           // total attempts; <= 0 falls back to the default
	BaseDelay   time.Duration // delay before retry N is N*BaseDelay
}

// NewRetryingTranslator wraps inner. maxAttempts <= 0 and baseDelay <= 0 use
// sensible defaults.
func NewRetryingTranslator(inner Translator, maxAttempts int, baseDelay time.Duration) *RetryingTranslator {
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	if baseDelay <= 0 {
		baseDelay = defaultBaseDelay
	}
	return &RetryingTranslator{Inner: inner, MaxAttempts: maxAttempts, BaseDelay: baseDelay}
}

// Engine delegates to the wrapped translator.
func (r *RetryingTranslator) Engine() string { return r.Inner.Engine() }

// Translate retries the wrapped call up to MaxAttempts, sleeping between
// attempts. It aborts immediately if the context is cancelled.
func (r *RetryingTranslator) Translate(ctx context.Context, req Request) (Response, error) {
	return retryCall(ctx, r, func() (Response, error) { return r.Inner.Translate(ctx, req) })
}

// TranslateBatch retries the whole batch. Translation is idempotent and the
// providers charge per character, so a retry costs the batch again rather than
// risking a partially-applied chapter.
func (r *RetryingTranslator) TranslateBatch(ctx context.Context, req BatchRequest) ([]Response, error) {
	return retryCall(ctx, r, func() ([]Response, error) { return r.Inner.TranslateBatch(ctx, req) })
}

// retryCall runs call up to MaxAttempts times with linear backoff, aborting
// immediately if the context is cancelled.
func retryCall[T any](ctx context.Context, r *RetryingTranslator, call func() (T, error)) (T, error) {
	var zero T

	attempts := r.MaxAttempts
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := call()
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if attempt == attempts {
			break
		}

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(time.Duration(attempt) * r.BaseDelay):
		}
	}

	return zero, fmt.Errorf("unable to translate after %d attempts, err: %w", attempts, lastErr)
}

var _ Translator = (*RetryingTranslator)(nil)
