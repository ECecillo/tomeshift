package progress

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// clock returns a now() the test drives by hand, so elapsed time and the
// repaint throttle are assertable without sleeping.
func clock(start time.Time) (func() time.Time, func(time.Duration)) {
	at := start
	return func() time.Time { return at }, func(d time.Duration) { at = at.Add(d) }
}

func newTestBar(t *testing.T, prefix string) (*Bar, *bytes.Buffer, func(time.Duration)) {
	t.Helper()

	var buf bytes.Buffer
	now, advance := clock(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))
	bar := New(&buf, true, prefix)
	bar.now = now
	bar.started = now()
	return bar, &buf, advance
}

func TestBar_Disabled_WritesNothing(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	bar := New(&buf, false, "")

	bar.Stage("translate")
	bar.Total(10, 2)
	bar.Chapter(1)
	bar.Advance(5, 5)
	bar.Finish()

	require.Empty(t, buf.String(), "a disabled bar must be inert on every method")
}

func TestBar_BeforeTotal_ShowsStageOnly(t *testing.T) {
	t.Parallel()

	bar, buf, advance := newTestBar(t, "")
	bar.Stage("extract")
	advance(3 * time.Second)
	bar.Stage("parse")

	require.Contains(t, buf.String(), "parse")
	require.Contains(t, buf.String(), "… 3s")
	require.NotContains(t, buf.String(), "seg", "no total means nothing to gauge")
}

func TestBar_Line_ReportsProgressChapterReuseAndETA(t *testing.T) {
	t.Parallel()

	bar, _, advance := newTestBar(t, "")
	bar.Stage("translate")
	bar.Total(100, 10)
	bar.Chapter(3)
	advance(time.Minute)
	bar.Advance(30, 20) // half the book, 40% of it from the TM

	line := bar.line(bar.now())
	require.Contains(t, line, "translate")
	require.Contains(t, line, "50% 50/100 seg")
	require.Contains(t, line, "ch 3/10")
	require.Contains(t, line, "tm 40%")
	require.Contains(t, line, "1m00s")
	require.Contains(t, line, "eta 1m00s") // 50 segments in 1m ⇒ 1m to go
	require.Equal(t, barCells, strings.Count(line, filledCell)+strings.Count(line, emptyCell))
}

func TestBar_Cost_ShownOnlyOnceItIsNonZero(t *testing.T) {
	t.Parallel()

	bar, _, _ := newTestBar(t, "")
	bar.Stage("translate")
	bar.Total(100, 0)
	bar.Advance(20, 0)

	require.NotContains(t, bar.line(bar.now()), "$", "a free local engine must not print a money column")

	bar.Cost(0.1274)
	require.Contains(t, bar.line(bar.now()), "$0.1274")
}

func TestBar_ETA_WithheldUntilTheRateMeansSomething(t *testing.T) {
	t.Parallel()

	bar, _, advance := newTestBar(t, "")
	bar.Total(100, 0)
	advance(time.Second)

	// 5 TM hits in a second would project an ETA of 19s for a book that has not
	// yet made a single provider call.
	bar.Advance(0, 5)
	require.NotContains(t, bar.line(bar.now()), "eta")

	bar.Advance(0, 10)
	require.Contains(t, bar.line(bar.now()), "eta")

	// A finished bar has nothing left to predict.
	bar.Advance(85, 0)
	require.NotContains(t, bar.line(bar.now()), "eta")
	require.Contains(t, bar.line(bar.now()), "100% 100/100 seg")
}

func TestBar_Advance_NeverExceedsTheTotal(t *testing.T) {
	t.Parallel()

	bar, _, _ := newTestBar(t, "")
	bar.Total(10, 0)
	bar.Advance(12, 0)

	line := bar.line(bar.now())
	require.Contains(t, line, "100% 10/10 seg")
	require.Equal(t, barCells, strings.Count(line, filledCell))
}

func TestBar_Paint_IsThrottled(t *testing.T) {
	t.Parallel()

	bar, buf, advance := newTestBar(t, "")
	bar.Total(1000, 0)
	buf.Reset()

	for range 10 {
		bar.Advance(1, 0)
	}
	require.Empty(t, buf.String(), "advances within the interval must coalesce")

	advance(repaintInterval)
	bar.Advance(1, 0)
	require.Equal(t, 1, strings.Count(buf.String(), "\r"))
}

func TestBar_Stage_BypassesTheThrottle(t *testing.T) {
	t.Parallel()

	bar, buf, _ := newTestBar(t, "")
	bar.Total(10, 0)
	buf.Reset()

	bar.Stage("recompose")
	require.Contains(t, buf.String(), "recompose")
}

func TestBar_Finish_ClosesTheLineOnceAndStaysInert(t *testing.T) {
	t.Parallel()

	bar, buf, _ := newTestBar(t, "")
	bar.Stage("translate")
	bar.Finish()
	require.True(t, strings.HasSuffix(buf.String(), "\n"))

	before := buf.String()
	bar.Finish()
	bar.Advance(1, 0)
	require.Equal(t, before, buf.String(), "a finished bar must not reopen its line")
}

func TestBar_Finish_OnABarThatNeverDrew(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	New(&buf, true, "").Finish()
	require.Empty(t, buf.String(), "nothing was drawn, so there is no line to close")
}

func TestBar_Prefix_IdentifiesTheBook(t *testing.T) {
	t.Parallel()

	bar, _, _ := newTestBar(t, "re-zero-01")
	bar.Stage("translate")
	require.True(t, strings.HasPrefix(bar.line(bar.now()), "re-zero-01 · translate"))
}

func TestIsTerminal(t *testing.T) {
	t.Parallel()

	require.False(t, IsTerminal(&bytes.Buffer{}), "a buffer cannot be watched")

	f, err := os.CreateTemp(t.TempDir(), "out")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	require.False(t, IsTerminal(f), "a redirected file must not get carriage returns")

	// /dev/null is the character device available on every CI box; a real tty
	// is not.
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = devnull.Close() })
	require.True(t, IsTerminal(devnull))
}

func TestHumanDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"negative clamps to zero", -time.Second, "0s"},
		{"seconds", 42 * time.Second, "42s"},
		{"rounds to the second", 1500 * time.Millisecond, "2s"},
		{"minutes pad their seconds", 3*time.Minute + 2*time.Second, "3m02s"},
		{"hours pad their minutes", 2*time.Hour + 4*time.Minute, "2h04m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, humanDuration(tc.in))
		})
	}
}
