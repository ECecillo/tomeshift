package translate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/translate"
)

// fakeTranslator fails for the first failures calls, then succeeds.
type fakeTranslator struct {
	calls    int
	failUpTo int
	err      error
}

func (f *fakeTranslator) Engine() string { return "fake" }

func (f *fakeTranslator) Translate(_ context.Context, _ translate.Request) (translate.Response, error) {
	f.calls++
	if f.calls <= f.failUpTo {
		return translate.Response{}, f.err
	}
	return translate.Response{Text: "ok", Engine: "fake"}, nil
}

func (f *fakeTranslator) TranslateBatch(_ context.Context, req translate.BatchRequest) ([]translate.Response, error) {
	f.calls++
	if f.calls <= f.failUpTo {
		return nil, f.err
	}
	out := make([]translate.Response, len(req.Texts))
	for i := range out {
		out[i] = translate.Response{Text: "ok", Engine: "fake"}
	}
	return out, nil
}

func TestRetryingTranslator_RetriesBatch(t *testing.T) {
	t.Parallel()

	inner := &fakeTranslator{failUpTo: 2, err: errors.New("transient")}
	rt := translate.NewRetryingTranslator(inner, 3, time.Millisecond)

	resps, err := rt.TranslateBatch(t.Context(), translate.BatchRequest{Texts: []string{"a", "b"}})
	require.NoError(t, err)
	require.Len(t, resps, 2)
	require.Equal(t, 3, inner.calls)
}

func TestRetryingTranslator_SucceedsAfterRetries(t *testing.T) {
	t.Parallel()

	inner := &fakeTranslator{failUpTo: 2, err: errors.New("transient")}
	rt := translate.NewRetryingTranslator(inner, 3, time.Millisecond)

	resp, err := rt.Translate(t.Context(), translate.Request{Text: "x"})
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Text)
	require.Equal(t, 3, inner.calls)
	require.Equal(t, "fake", rt.Engine())
}

func TestRetryingTranslator_ExhaustsAttempts(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("always down")
	inner := &fakeTranslator{failUpTo: 100, err: sentinel}
	rt := translate.NewRetryingTranslator(inner, 3, time.Millisecond)

	_, err := rt.Translate(t.Context(), translate.Request{Text: "x"})
	require.Error(t, err)
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 3, inner.calls)
}

func TestRetryingTranslator_StopsOnCancelledContext(t *testing.T) {
	t.Parallel()

	inner := &fakeTranslator{failUpTo: 100, err: errors.New("down")}
	rt := translate.NewRetryingTranslator(inner, 5, time.Hour) // long backoff

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // already cancelled

	_, err := rt.Translate(ctx, translate.Request{Text: "x"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, inner.calls, "should not sleep through a cancelled context")
}

func TestNewRetryingTranslator_Defaults(t *testing.T) {
	t.Parallel()

	inner := &fakeTranslator{failUpTo: 0}
	rt := translate.NewRetryingTranslator(inner, 0, 0)
	require.Equal(t, 3, rt.MaxAttempts)
	require.Equal(t, 200*time.Millisecond, rt.BaseDelay)
}
