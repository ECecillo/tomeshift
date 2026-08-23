package pipeline

// Reporter renders a run's advancement for a human watching the terminal. It is
// optional — a nil Reporter disables reporting — and it is never a data source:
// report.json and logs.ndjson remain the machine-readable record, so a Reporter
// may drop, throttle or reorder anything it is told.
//
// Implementations must tolerate a run that never sizes itself: Plan segments a
// book without translating it, and a failed run stops mid-count.
type Reporter interface {
	// Stage announces the stage now starting.
	Stage(name string)
	// Total sizes the work, once the book has been segmented.
	Total(segments, chapters int)
	// Chapter reports the 1-based chapter now being worked on. Only the
	// literary pipeline walks chapters.
	Chapter(index int)
	// Advance records segments finished since the last call, split between
	// fresh translations and TM reuse.
	Advance(fresh, reused int)
	// Cost reports what the run has spent so far, in USD. It is priced by the
	// same mode that prices report.ActualCostUSD, so the last figure a watcher
	// sees is the one the report carries.
	Cost(usd float64)
	// Finish terminates the report. It must be idempotent: it is deferred on
	// every exit path.
	Finish()
}

// nopReporter is the default, so the orchestrator calls the reporter
// unconditionally instead of guarding every call site with a nil check.
type nopReporter struct{}

func (nopReporter) Stage(string)     {}
func (nopReporter) Total(int, int)   {}
func (nopReporter) Chapter(int)      {}
func (nopReporter) Advance(int, int) {}
func (nopReporter) Cost(float64)     {}
func (nopReporter) Finish()          {}

func (o *Orchestrator) progress() Reporter {
	if o.Progress != nil {
		return o.Progress
	}
	return nopReporter{}
}
