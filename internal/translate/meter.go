package translate

import (
	"sync/atomic"
	"unicode/utf8"
)

// Meter reports the source characters an adapter has actually put into provider
// requests. Providers bill per character per request, so this — not the size of
// the text the pipeline wanted translated — is what was paid for.
type Meter interface {
	CharsSent() int
}

// charMeter counts at the request boundary, which is the only place that can be
// right. Counting around the retry loop instead got four things wrong at once:
// a preflight rejection was billed although nothing was sent, a cancelled
// backoff booked a resend that never happened, a run where every attempt failed
// never counted the first one, and a logical batch that the adapter splits into
// several requests could not say which of them went out before a failure.
//
// It is cumulative for the adapter's lifetime; `batch` reuses one across books,
// so callers take a delta.
type charMeter struct {
	sent atomic.Int64
}

// record is called immediately before a request leaves for the provider.
func (m *charMeter) record(texts ...string) {
	total := 0
	for _, text := range texts {
		total += utf8.RuneCountInString(text)
	}
	m.sent.Add(int64(total))
}

// CharsSent returns the running total of characters sent.
func (m *charMeter) CharsSent() int { return int(m.sent.Load()) }
