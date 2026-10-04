package commandexec

import (
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/progress"
)

// barReporter adapts the flow's absolute Progress snapshots onto the terminal
// bar's incremental API: it sizes the bar once, announces stage and chapter
// changes, and turns cumulative counters into the per-call deltas Advance
// expects. The flow publishes state; this decides what the human sees.
type barReporter struct {
	bar   *progress.Bar
	sized bool
	stage string
	last  pipeline.Progress
}

func newBarReporter(bar *progress.Bar) *barReporter {
	return &barReporter{bar: bar}
}

func (b *barReporter) Report(p pipeline.Progress) {
	if p.Stage != b.stage {
		b.stage = p.Stage
		b.bar.Stage(p.Stage)
	}
	if !b.sized && p.Segments > 0 {
		b.sized = true
		b.bar.Total(p.Segments, p.Chapters)
	}
	if p.Chapter > 0 && p.Chapter != b.last.Chapter {
		b.bar.Chapter(p.Chapter)
	}

	// Cumulative → per-call deltas. SegmentsDone counts both fresh and reused;
	// TMHits is the reused share, so the fresh delta is the rest.
	freshDelta := (p.SegmentsDone - p.TMHits) - (b.last.SegmentsDone - b.last.TMHits)
	reusedDelta := p.TMHits - b.last.TMHits
	if freshDelta > 0 || reusedDelta > 0 {
		b.bar.Advance(freshDelta, reusedDelta)
	}
	if p.CostUSD != b.last.CostUSD {
		b.bar.Cost(p.CostUSD)
	}

	b.last = p
}

// Finish terminates the underlying bar. It is idempotent, so callers can invoke
// it on every exit path — including before writing an error to the same stream.
func (b *barReporter) Finish() { b.bar.Finish() }

var _ pipeline.Reporter = (*barReporter)(nil)
