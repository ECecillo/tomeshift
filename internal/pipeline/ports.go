package pipeline

import (
	"context"
	"time"

	"github.com/ECecillo/tomeshift/internal/qa"
)

// Steps is what the control flow can ask an engine to run. There is one method
// per activity in the RFC table, and none of them takes a context: the adapter
// owns it (a Temporal workflow.Context is not a context.Context, and the
// in-process adapter carries a plain one). Inputs and outputs are the small,
// serialisable DTOs from types.go — no live object crosses this boundary.
type Steps interface {
	PrepareBook(BookInput) (Manifest, error)
	ProjectCost(ProjectInput) (Projection, error)
	ResolveChapter(ResolveInput) (ChapterPlan, error)
	DraftChapter(DraftInput) (DraftResult, error)
	EscalateChapter(EscalateInput) (EscalateResult, error)
	RefineBatch(RefineInput) (RefineResult, error)
	CommitChapter(CommitInput) (CommitResult, error)
	UpdateBible(BibleInput) (BibleResult, error)
	RecomposeBook(RunRef) (RecomposeResult, error)
	CheckEPUB(CheckInput) (CheckResult, error)
	FinishBook(FinishInput) (qa.Report, error)
	RecordSpend(SpendInput) error
	PriceRun(PriceInput) (float64, error)
}

// Runtime is what the control flow needs from an engine, and nothing more. The
// in-process implementation backs it with time.Now, channels and a plain
// function call; the Temporal one with workflow timers, signals, disconnected
// contexts and child workflows.
type Runtime interface {
	// Now is the flow's only clock, so the same function replays deterministically.
	Now() time.Time
	// WaitResume blocks for d or until a resume signal, whichever comes first.
	// It is the rate-limit cooldown; the phase-1 flow does not call it, but the
	// runtime implements and tests it so phase 4 is a flow change only.
	WaitResume(d time.Duration) (resumed bool, err error)
	// Paused reports whether a pause signal is pending.
	Paused() bool
	// Cancelled reports whether the run has been cancelled.
	Cancelled() bool
	// Detached returns a runtime whose cleanup steps run even after cancellation
	// — the engine-agnostic form of a disconnected context.
	Detached() Runtime
	// Report publishes a progress snapshot. The in-process runtime drives the
	// terminal bar; the Temporal one updates the value its query serves.
	Report(Progress)
	// RunBook runs one volume; RunSeries uses it to run its children.
	RunBook(BookInput) (RunResult, error)
}

// Reporter receives Progress snapshots. The CLI implements it to drive the
// terminal bar; tests record the sequence a watcher would have seen. A nil
// Reporter disables reporting, so the runtime calls it unconditionally.
type Reporter interface {
	Report(Progress)
}

// QualityEstimator scores a translated segment without a reference (e.g.
// COMET-QE). Scores are higher-is-better; the pipeline uses them to drive
// selective LLM escalation and to report per-book quality.
type QualityEstimator interface {
	Estimate(ctx context.Context, source, candidate string) (float64, error)
}
