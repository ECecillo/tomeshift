package translate

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
	"unicode/utf8"
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

	// retriedChars accumulates the source characters re-sent by attempts after
	// the first. Providers bill per character per request, so a retried batch
	// is paid for twice even though the pipeline only ever sees one
	// translation of it.
	retriedChars atomic.Int64
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
	return retryCall(ctx, r, utf8.RuneCountInString(req.Text),
		func() (Response, error) { return r.Inner.Translate(ctx, req) })
}

// TranslateBatch retries the whole batch. Translation is idempotent and the
// providers charge per character, so a retry costs the batch again rather than
// risking a partially-applied chapter.
func (r *RetryingTranslator) TranslateBatch(ctx context.Context, req BatchRequest) ([]Response, error) {
	chars := 0
	for _, text := range req.Texts {
		chars += utf8.RuneCountInString(text)
	}
	return retryCall(ctx, r, chars,
		func() ([]Response, error) { return r.Inner.TranslateBatch(ctx, req) })
}

// RetriedChars reports the source characters re-sent by retried attempts over
// this translator's lifetime. The pipeline adds them to what it was billed
// for: a batch translated on the third attempt cost the provider three times,
// and the free-tier counter has to say so.
func (r *RetryingTranslator) RetriedChars() int {
	return int(r.retriedChars.Load())
}

// retryCall runs call up to MaxAttempts times with linear backoff, aborting
// immediately if the context is cancelled. chars is what one attempt sends, so
// every attempt after the first can be added to the billable total.
func retryCall[T any](ctx context.Context, r *RetryingTranslator, chars int, call func() (T, error)) (T, error) {
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
		// About to send the same text again; the provider charges for it again.
		r.retriedChars.Add(int64(chars))

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(time.Duration(attempt) * r.BaseDelay):
		}
	}

	return zero, fmt.Errorf("unable to translate after %d attempts, err: %w", attempts, lastErr)
}

var _ Translator = (*RetryingTranslator)(nil)
