package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// repaintInterval bounds the redraw rate. A TM hit resolves in
	// microseconds, so an unthrottled bar would emit thousands of repaints a
	// second — enough to cost more than the work it reports on.
	repaintInterval = 100 * time.Millisecond

	// etaAfterRatio withholds the ETA until this much of the book is done. A
	// run front-loaded with TM hits advances at a rate no provider call will
	// ever match, and announcing "eta 8s" for an hour of work is worse than
	// announcing nothing.
	etaAfterRatio = 0.10

	// barCells is the width of the gauge itself, excluding its brackets.
	barCells = 24

	// stageWidth pads the stage name so the fields to its right do not shift
	// as the run moves from "translate" to "qa".
	stageWidth = 9

	filledCell = "█"
	emptyCell  = "░"
)

// Bar is a single-line terminal gauge for one run. It is safe for concurrent
// use, and every method is a no-op on a disabled bar, so callers wire it
// unconditionally and never branch on whether it draws.
type Bar struct {
	mu     sync.Mutex
	w      io.Writer // nil = disabled, or already finished
	prefix string

	stage    string
	total    int
	chapters int
	chapter  int
	fresh    int
	reused   int
	usd      float64

	started   time.Time
	lastPaint time.Time
	painted   bool

	// now is injected so the tests can drive elapsed time and the repaint
	// throttle without sleeping.
	now func() time.Time
}

// New returns a bar writing to w, prefixed with prefix (a book id for `batch`,
// empty for a single run). When enabled is false the bar is inert: the decision
// to draw is made once, here.
func New(w io.Writer, enabled bool, prefix string) *Bar {
	b := &Bar{prefix: prefix, now: time.Now}
	if enabled {
		b.w = w
	}
	b.started = b.now()
	return b
}

// hasControllingTerminal reports whether this process is attached to a terminal
// at all, regardless of where its streams point. Opening /dev/tty succeeds only
// then. It is a variable so the tests can drive both branches: whether the
// process running them has a terminal is not something a test may depend on.
var hasControllingTerminal = func() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = tty.Close()
	return true
}

// IsTerminal reports whether drawing on w will reach someone watching.
//
// A character device is the easy case. A pipe is the one that matters: every
// recipe needing a credential is wrapped in `op run`, and 1Password masks
// secrets in the streams it forwards — which it can only do by replacing them
// with pipes. Measured through it, stderr comes back as prw-rw---- and a bar
// keyed on "is this a character device" silently turned itself off on exactly
// the commands a human sits and watches. The frames themselves survive that
// relay unchanged: `\r` and all, op forwards them byte for byte.
//
// So a pipe draws when the process has a controlling terminal. What can be
// known is that something is relaying our output rather than storing it, plus
// whether a human is present; what cannot be known is what the relay then does
// with it. `2>&1 > file` through such a wrapper would collect the carriage
// returns, which is what --no-progress is for.
//
// A regular file never draws. That is the redirection this distinction exists
// to protect: `2> run.log` keeps a readable log.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return true
	}
	if info.Mode()&os.ModeNamedPipe != 0 {
		return hasControllingTerminal()
	}
	return false
}

// Stage announces the stage now starting. Before the book has been segmented
// there is nothing to measure, so the line degrades to the stage and its
// elapsed time.
func (b *Bar) Stage(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stage = name
	b.paint(true)
}

// Total sizes the work. It is called once, after segmentation: the segment
// count is not knowable before it.
func (b *Bar) Total(segments, chapters int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total = segments
	b.chapters = chapters
	b.paint(true)
}

// Chapter reports the 1-based chapter now being worked on. Only the literary
// pipeline walks chapters; the NMT one flattens the book, so its line carries
// no chapter field.
func (b *Bar) Chapter(index int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chapter = index
	b.paint(false)
}

// Advance records segments finished since the last call, split between freshly
// translated ones and TM reuse. The split is what makes the reported rate
// legible: a book that is 60% reuse is not going at the speed the last minute
// suggests.
func (b *Bar) Advance(fresh, reused int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fresh += fresh
	b.reused += reused
	b.paint(false)
}

// Cost records what the run has spent so far. It is displayed only once it is
// non-zero: a self-hosted engine with no LLM pass costs nothing, and a column
// of "$0.0000" would be noise.
func (b *Bar) Cost(usd float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usd = usd
	b.paint(false)
}

// Finish terminates the line. It is idempotent and safe on a bar that never
// drew anything, so callers can defer it on every exit path — including the
// failure paths, where the error message is about to be written to the same
// stream and must not land inside the bar.
func (b *Bar) Finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.w == nil {
		return
	}
	if b.painted {
		b.paint(true)
		_, _ = fmt.Fprintln(b.w)
	}
	b.w = nil
}

// paint redraws the line in place. force bypasses the throttle, for the events
// a user would otherwise see arrive late: a stage change, and the final frame.
func (b *Bar) paint(force bool) {
	if b.w == nil {
		return
	}
	now := b.now()
	if !force && b.painted && now.Sub(b.lastPaint) < repaintInterval {
		return
	}
	b.lastPaint = now
	b.painted = true
	// \r returns to the line start, \x1b[K clears what the previous, possibly
	// longer, frame left behind.
	_, _ = fmt.Fprintf(b.w, "\r\x1b[K%s", b.line(now))
}

// line renders the current state. It is pure, which is what the tests assert
// against.
func (b *Bar) line(now time.Time) string {
	var sb strings.Builder
	if b.prefix != "" {
		sb.WriteString(b.prefix)
		sb.WriteString(" · ")
	}
	fmt.Fprintf(&sb, "%-*s", stageWidth, b.stage)

	elapsed := now.Sub(b.started)
	if b.total <= 0 {
		sb.WriteString(" … " + humanDuration(elapsed))
		return sb.String()
	}

	done := b.fresh + b.reused
	if done > b.total {
		done = b.total // a provider that answered twice must not print 103%
	}
	ratio := float64(done) / float64(b.total)

	filled := int(ratio * barCells)
	sb.WriteString(" [")
	sb.WriteString(strings.Repeat(filledCell, filled))
	sb.WriteString(strings.Repeat(emptyCell, barCells-filled))
	fmt.Fprintf(&sb, "] %3.0f%% %d/%d seg", ratio*100, done, b.total)

	if b.chapters > 0 && b.chapter > 0 {
		fmt.Fprintf(&sb, " · ch %d/%d", b.chapter, b.chapters)
	}
	if b.reused > 0 {
		fmt.Fprintf(&sb, " · tm %.0f%%", float64(b.reused)/float64(done)*100)
	}
	if b.usd > 0 {
		fmt.Fprintf(&sb, " · $%.4f", b.usd)
	}
	sb.WriteString(" · " + humanDuration(elapsed))
	if eta, ok := b.eta(elapsed, done, ratio); ok {
		sb.WriteString(" · eta " + humanDuration(eta))
	}
	return sb.String()
}

// eta extrapolates the remaining time from the rate observed so far, and
// declines to answer while that rate is still meaningless.
func (b *Bar) eta(elapsed time.Duration, done int, ratio float64) (time.Duration, bool) {
	if done == 0 || done >= b.total || ratio < etaAfterRatio {
		return 0, false
	}
	return time.Duration(float64(elapsed) / float64(done) * float64(b.total-done)), true
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
