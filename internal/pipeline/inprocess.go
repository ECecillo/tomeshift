package pipeline

import (
	"context"
	"time"

	"github.com/ECecillo/ebooks-translator/internal/qa"
)

// InProcessSteps runs each activity as a direct Stages call under one context.
// It is the flow's test double and its portability proof: the same RunBook that
// a Temporal worker drives runs here against a plain function call, which is
// what keeps the flow engine-agnostic.
type InProcessSteps struct {
	Stages *Stages
	Ctx    context.Context
}

func (a InProcessSteps) ctx() context.Context {
	if a.Ctx != nil {
		return a.Ctx
	}
	return context.Background()
}

func (a InProcessSteps) PrepareBook(in BookInput) (Manifest, error) {
	return a.Stages.PrepareBook(a.ctx(), in)
}
func (a InProcessSteps) ProjectCost(in ProjectInput) (Projection, error) {
	return a.Stages.ProjectCost(a.ctx(), in)
}
func (a InProcessSteps) ResolveChapter(in ResolveInput) (ChapterPlan, error) {
	return a.Stages.ResolveChapter(a.ctx(), in)
}
func (a InProcessSteps) DraftChapter(in DraftInput) (DraftResult, error) {
	return a.Stages.DraftChapter(a.ctx(), in)
}
func (a InProcessSteps) EscalateChapter(in EscalateInput) (EscalateResult, error) {
	return a.Stages.EscalateChapter(a.ctx(), in)
}
func (a InProcessSteps) RefineBatch(in RefineInput) (RefineResult, error) {
	return a.Stages.RefineBatch(a.ctx(), in)
}
func (a InProcessSteps) CommitChapter(in CommitInput) (CommitResult, error) {
	return a.Stages.CommitChapter(a.ctx(), in)
}
func (a InProcessSteps) UpdateBible(in BibleInput) (BibleResult, error) {
	return a.Stages.UpdateBible(a.ctx(), in)
}
func (a InProcessSteps) RecomposeBook(ref RunRef) (RecomposeResult, error) {
	return a.Stages.RecomposeBook(a.ctx(), ref)
}
func (a InProcessSteps) CheckEPUB(in CheckInput) (CheckResult, error) {
	return a.Stages.CheckEPUB(a.ctx(), in)
}
func (a InProcessSteps) FinishBook(in FinishInput) (qa.Report, error) {
	return a.Stages.FinishBook(a.ctx(), in)
}
func (a InProcessSteps) RecordSpend(in SpendInput) error {
	return a.Stages.RecordSpend(a.ctx(), in)
}
func (a InProcessSteps) PriceRun(in PriceInput) (float64, error) {
	return a.Stages.PriceRun(a.ctx(), in)
}

var _ Steps = InProcessSteps{}

// InProcessRuntime backs the flow's Runtime with the standard library: a
// context for cancellation, a clock, a resume channel and a Reporter that drives
// the terminal bar. It is what the CLI runs `run` and `batch` on until the
// Temporal engine lands.
type InProcessRuntime struct {
	// Ctx is the run's context; Cancelled reads it. A detached runtime ignores it.
	Ctx context.Context
	// Progress receives the flow's snapshots; nil disables reporting.
	Progress Reporter
	// Steps runs child books for RunSeries.
	Steps Steps
	// Clock overrides Now for deterministic tests; nil uses time.Now.
	Clock func() time.Time
	// ResumeCh ends a WaitResume early; nil means only the timer fires.
	ResumeCh <-chan struct{}
	// PausePending is what Paused reports.
	PausePending bool

	detached bool
}

func (r *InProcessRuntime) Now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

// WaitResume blocks for d or until the resume channel fires, whichever comes
// first. The phase-1 flow never calls it; it exists and is tested so phase 4 is
// a flow change only.
func (r *InProcessRuntime) WaitResume(d time.Duration) (bool, error) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	var done <-chan struct{}
	if r.Ctx != nil {
		done = r.Ctx.Done()
	}

	select {
	case <-r.ResumeCh: // a nil channel blocks forever, leaving the timer/ctx
		return true, nil
	case <-timer.C:
		return false, nil
	case <-done:
		return false, r.Ctx.Err()
	}
}

func (r *InProcessRuntime) Paused() bool { return r.PausePending }

func (r *InProcessRuntime) Cancelled() bool {
	if r.detached {
		return false
	}
	return r.Ctx != nil && r.Ctx.Err() != nil
}

func (r *InProcessRuntime) Detached() Runtime {
	c := *r
	c.detached = true
	return &c
}

func (r *InProcessRuntime) Report(p Progress) {
	if r.Progress != nil {
		r.Progress.Report(p)
	}
}

func (r *InProcessRuntime) RunBook(in BookInput) (RunResult, error) {
	res, err := RunBook(r.Steps, r, in)
	if res == nil {
		return RunResult{}, err
	}
	return *res, err
}

var _ Runtime = (*InProcessRuntime)(nil)
