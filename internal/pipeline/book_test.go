package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/cost"
	"github.com/ECecillo/tomeshift/internal/literary"
	"github.com/ECecillo/tomeshift/internal/llm"
	"github.com/ECecillo/tomeshift/internal/pipeline"
	"github.com/ECecillo/tomeshift/internal/qa"
)

// ---------------------------------------------------------------------------
// Fake Steps: a struct of func fields recording calls and returning scripted
// results. It is the level-2 test double the RFC calls for — plain Go, no
// engine — so RunBook's control flow, accumulation and cleanup are proven in
// isolation from the stage bodies.
// ---------------------------------------------------------------------------

type fakeSteps struct {
	prepare   func(pipeline.BookInput) (pipeline.Manifest, error)
	project   func(pipeline.ProjectInput) (pipeline.Projection, error)
	resolve   func(pipeline.ResolveInput) (pipeline.ChapterPlan, error)
	draft     func(pipeline.DraftInput) (pipeline.DraftResult, error)
	escalate  func(pipeline.EscalateInput) (pipeline.EscalateResult, error)
	refine    func(pipeline.RefineInput) (pipeline.RefineResult, error)
	commit    func(pipeline.CommitInput) (pipeline.CommitResult, error)
	bible     func(pipeline.BibleInput) (pipeline.BibleResult, error)
	recompose func(pipeline.RunRef) (pipeline.RecomposeResult, error)
	check     func(pipeline.CheckInput) (pipeline.CheckResult, error)
	finish    func(pipeline.FinishInput) (qa.Report, error)
	spend     func(pipeline.SpendInput) error
	price     func(pipeline.PriceInput) (float64, error)

	calls        []string
	draftIDs     [][]string
	finishCalls  int
	spendCalls   int
	commitCalls  int
	updateBible  int
	refineInputs []pipeline.RefineInput
	spendInput   pipeline.SpendInput
}

func (f *fakeSteps) PrepareBook(in pipeline.BookInput) (pipeline.Manifest, error) {
	f.calls = append(f.calls, "prepare")
	return f.prepare(in)
}

func (f *fakeSteps) ProjectCost(in pipeline.ProjectInput) (pipeline.Projection, error) {
	f.calls = append(f.calls, "project")
	if f.project != nil {
		return f.project(in)
	}
	return pipeline.Projection{}, nil
}

func (f *fakeSteps) ResolveChapter(in pipeline.ResolveInput) (pipeline.ChapterPlan, error) {
	f.calls = append(f.calls, "resolve:"+in.ChapterID)
	return f.resolve(in)
}

func (f *fakeSteps) DraftChapter(in pipeline.DraftInput) (pipeline.DraftResult, error) {
	f.calls = append(f.calls, "draft:"+in.ChapterID)
	f.draftIDs = append(f.draftIDs, in.Fresh)
	if f.draft != nil {
		return f.draft(in)
	}
	return pipeline.DraftResult{Chars: len(in.Fresh), Engine: "nmt"}, nil
}

func (f *fakeSteps) EscalateChapter(in pipeline.EscalateInput) (pipeline.EscalateResult, error) {
	f.calls = append(f.calls, "escalate:"+in.ChapterID)
	if f.escalate != nil {
		return f.escalate(in)
	}
	return pipeline.EscalateResult{}, nil
}

func (f *fakeSteps) RefineBatch(in pipeline.RefineInput) (pipeline.RefineResult, error) {
	f.calls = append(f.calls, "refine:"+in.ChapterID)
	f.refineInputs = append(f.refineInputs, in)
	if f.refine != nil {
		return f.refine(in)
	}
	return pipeline.RefineResult{Refined: len(in.IDs)}, nil
}

func (f *fakeSteps) CommitChapter(in pipeline.CommitInput) (pipeline.CommitResult, error) {
	f.calls = append(f.calls, "commit:"+in.ChapterID)
	f.commitCalls++
	if f.commit != nil {
		return f.commit(in)
	}
	return pipeline.CommitResult{}, nil
}

func (f *fakeSteps) UpdateBible(in pipeline.BibleInput) (pipeline.BibleResult, error) {
	f.calls = append(f.calls, "bible:"+in.ChapterID)
	f.updateBible++
	if f.bible != nil {
		return f.bible(in)
	}
	return pipeline.BibleResult{}, nil
}

func (f *fakeSteps) RecomposeBook(ref pipeline.RunRef) (pipeline.RecomposeResult, error) {
	f.calls = append(f.calls, "recompose")
	if f.recompose != nil {
		return f.recompose(ref)
	}
	return pipeline.RecomposeResult{OutputEPUB: ref.OutputDir + "/translated.epub"}, nil
}

func (f *fakeSteps) CheckEPUB(in pipeline.CheckInput) (pipeline.CheckResult, error) {
	f.calls = append(f.calls, "check")
	if f.check != nil {
		return f.check(in)
	}
	return pipeline.CheckResult{Skipped: true}, nil
}

func (f *fakeSteps) FinishBook(in pipeline.FinishInput) (qa.Report, error) {
	f.calls = append(f.calls, "finish")
	f.finishCalls++
	if f.finish != nil {
		return f.finish(in)
	}
	return passingReport(in), nil
}

func (f *fakeSteps) RecordSpend(in pipeline.SpendInput) error {
	f.calls = append(f.calls, "spend")
	f.spendCalls++
	f.spendInput = in
	if f.spend != nil {
		return f.spend(in)
	}
	return nil
}

func (f *fakeSteps) PriceRun(in pipeline.PriceInput) (float64, error) {
	if f.price != nil {
		return f.price(in)
	}
	return 0, nil
}

// passingReport echoes the epubcheck verdict the flow stamped and reports clean
// static QA, mirroring the real FinishBook.
func passingReport(in pipeline.FinishInput) qa.Report {
	return qa.Report{
		PlaceholderRestoreRatio: 1,
		EPUBCheckPassed:         in.Result.QA.EPUBCheckPassed,
		EPUBCheckSkipped:        in.Result.QA.EPUBCheckSkipped,
	}
}

// ---------------------------------------------------------------------------
// A scriptable book: chapters with a reuse/fresh split, so the fake resolve and
// draft can produce consistent counters without a workspace.
// ---------------------------------------------------------------------------

type chapterScript struct {
	id     string
	reused int
	fresh  []string
	chars  int
}

func script(mode string, chapters ...chapterScript) *fakeSteps {
	byID := make(map[string]chapterScript, len(chapters))
	infos := make([]pipeline.ChapterInfo, 0, len(chapters))
	for _, c := range chapters {
		byID[c.id] = c
		infos = append(infos, pipeline.ChapterInfo{ID: c.id, Segments: c.reused + len(c.fresh)})
	}

	batchSize := 0
	if mode == pipeline.ModeLiterary {
		batchSize = 50
	}

	f := &fakeSteps{}
	f.prepare = func(pipeline.BookInput) (pipeline.Manifest, error) {
		return pipeline.Manifest{Chapters: infos, NMTEngine: "nmt", BatchSize: batchSize}, nil
	}
	f.resolve = func(in pipeline.ResolveInput) (pipeline.ChapterPlan, error) {
		c := byID[in.ChapterID]
		return pipeline.ChapterPlan{Reused: c.reused, Fresh: c.fresh}, nil
	}
	f.draft = func(in pipeline.DraftInput) (pipeline.DraftResult, error) {
		return pipeline.DraftResult{Chars: byID[in.ChapterID].chars, Engine: "nmt"}, nil
	}
	return f
}

func nominalInput(mode string) pipeline.BookInput {
	return pipeline.BookInput{
		InputPath: "book.epub", OutputDir: "out", SourceLang: "en", TargetLang: "fr",
		RunID: "r1", BookID: "b1", Mode: mode, SeriesID: "series-a",
	}
}

func newRuntime(rep *recordingReporter) *pipeline.InProcessRuntime {
	return &pipeline.InProcessRuntime{Progress: rep}
}

// recordingReporter captures the Progress snapshots a watcher would have seen.
type recordingReporter struct{ frames []pipeline.Progress }

func (r *recordingReporter) Report(p pipeline.Progress) { r.frames = append(r.frames, p) }

func (r *recordingReporter) last() pipeline.Progress {
	if len(r.frames) == 0 {
		return pipeline.Progress{}
	}
	return r.frames[len(r.frames)-1]
}

// ---------------------------------------------------------------------------
// Scenarios.
// ---------------------------------------------------------------------------

func TestRunBook_NominalSumsAcrossChapters(t *testing.T) {
	t.Parallel()

	steps := script(pipeline.ModeNMT,
		chapterScript{id: "ch_0001", reused: 1, fresh: []string{"a", "b"}, chars: 20},
		chapterScript{id: "ch_0002", reused: 0, fresh: []string{"c", "d", "e"}, chars: 30},
	)
	steps.escalate = func(in pipeline.EscalateInput) (pipeline.EscalateResult, error) {
		// One segment escalated per chapter, all improved.
		return pipeline.EscalateResult{Attempts: 1, Chars: 5, Improved: 1}, nil
	}
	steps.project = func(pipeline.ProjectInput) (pipeline.Projection, error) {
		return pipeline.Projection{NoReuseUSD: 0.6}, nil
	}
	steps.price = func(in pipeline.PriceInput) (float64, error) {
		return float64(in.Result.NMTChars) / 100, nil
	}
	rep := &recordingReporter{}

	result, err := pipeline.RunBook(steps, newRuntime(rep), nominalInput(pipeline.ModeNMT))
	require.NoError(t, err)

	require.Equal(t, 6, result.TotalSegments)
	require.Equal(t, 1, result.TMHits)
	require.Equal(t, 5, result.NMTSegments)
	require.Equal(t, 50, result.NMTChars) // 20 + 30
	require.Equal(t, 2, result.LLMSegments)
	require.Equal(t, 2, result.LLMAttempts)
	require.Equal(t, 2, result.LLMPassSegments)
	require.InDelta(t, float64(1)/6, result.TMHitRatio, 1e-9)
	require.InDelta(t, float64(2)/6, result.LLMEscalationRatio, 1e-9)
	require.InDelta(t, 0.5, result.ActualCostUSD, 1e-9) // 50 chars / 100
	require.InDelta(t, 0.6, result.EstimatedCostUSD, 1e-9)
	require.InDelta(t, 0.1, result.CostSavingsUSD, 1e-9)
	require.Equal(t, "nmt", result.NMTEngine)

	// Progress: every segment accounted for once, cost monotonic, ending on the
	// figure the report carries.
	require.Equal(t, 6, rep.last().SegmentsDone)
	require.Equal(t, 1, rep.last().TMHits)
	require.InDelta(t, result.ActualCostUSD, rep.last().CostUSD, 1e-9)
	var prev float64
	for _, fr := range rep.frames {
		require.GreaterOrEqual(t, fr.CostUSD, prev, "spend cannot go down")
		prev = fr.CostUSD
	}

	// One FinishBook and one RecordSpend on the success path.
	require.Equal(t, 1, steps.finishCalls)
	require.Equal(t, 1, steps.spendCalls)
}

func TestRunBook_BudgetCapRefusesWithoutDrafting(t *testing.T) {
	t.Parallel()

	steps := script(pipeline.ModeNMT, chapterScript{id: "ch_0001", fresh: []string{"a"}, chars: 10})
	steps.project = func(pipeline.ProjectInput) (pipeline.Projection, error) {
		return pipeline.Projection{NoReuseUSD: 3}, budgetErr()
	}

	result, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeNMT))
	require.ErrorIs(t, err, cost.ErrBudgetExceeded)
	require.NotNil(t, result)
	require.InDelta(t, 3.0, result.EstimatedCostUSD, 1e-9, "the report still knows the no-reuse cost")

	// Nothing was drafted, and no spend was recorded: the guardrail refuses
	// before the run can call a provider.
	require.NotContains(t, steps.calls, "draft:ch_0001")
	require.Zero(t, steps.draftIDs)
	require.Zero(t, steps.spendCalls)
	require.Zero(t, steps.finishCalls)
}

func budgetErr() error { return fmt.Errorf("unable to start run, err: %w", cost.ErrBudgetExceeded) }

func TestRunBook_RollingCapRefusalPropagates(t *testing.T) {
	t.Parallel()

	steps := script(pipeline.ModeNMT, chapterScript{id: "ch_0001", fresh: []string{"a"}})
	steps.project = func(pipeline.ProjectInput) (pipeline.Projection, error) {
		return pipeline.Projection{NoReuseUSD: 1}, wrapErr(cost.ErrBudgetExceeded)
	}

	_, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeNMT))
	require.ErrorIs(t, err, cost.ErrBudgetExceeded)
	require.Zero(t, steps.spendCalls)
}

func TestRunBook_StageErrorCarriesItsStage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		wire  func(*fakeSteps)
		stage pipeline.Stage
	}{
		{
			name:  "recompose failure",
			stage: pipeline.StageRecompose,
			wire: func(f *fakeSteps) {
				f.recompose = func(pipeline.RunRef) (pipeline.RecomposeResult, error) {
					return pipeline.RecomposeResult{}, &pipeline.StageError{Stage: pipeline.StageRecompose, Err: errors.New("boom")}
				}
			},
		},
		{
			name:  "draft failure",
			stage: pipeline.StageTranslate,
			wire: func(f *fakeSteps) {
				f.draft = func(pipeline.DraftInput) (pipeline.DraftResult, error) {
					return pipeline.DraftResult{}, &pipeline.StageError{Stage: pipeline.StageTranslate, Err: errors.New("provider down")}
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			steps := script(pipeline.ModeNMT, chapterScript{id: "ch_0001", fresh: []string{"a"}, chars: 10})
			tt.wire(steps)

			_, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeNMT))
			var stageErr *pipeline.StageError
			require.ErrorAs(t, err, &stageErr)
			require.Equal(t, tt.stage, stageErr.Stage)

			// A failure past the guardrail still records spend.
			require.Equal(t, 1, steps.spendCalls)
		})
	}
}

func TestRunBook_QAGateFailureIsReported(t *testing.T) {
	t.Parallel()

	steps := script(pipeline.ModeNMT, chapterScript{id: "ch_0001", fresh: []string{"a"}, chars: 10})
	steps.finish = func(pipeline.FinishInput) (qa.Report, error) {
		// Placeholder restoration below the threshold fails the gate, but the
		// artifacts were still written.
		return qa.Report{PlaceholderRestoreRatio: 0.5, EPUBCheckSkipped: true}, nil
	}

	result, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeNMT))
	require.ErrorIs(t, err, pipeline.ErrQAGateFailed)
	require.NotNil(t, result)
	require.Equal(t, 1, steps.finishCalls, "artifacts written despite the gate failure")
	require.Equal(t, 1, steps.spendCalls)
}

func TestRunBook_GlossaryViolationsFailTheGate(t *testing.T) {
	t.Parallel()

	steps := script(pipeline.ModeNMT, chapterScript{id: "ch_0001", fresh: []string{"a"}, chars: 10})
	steps.commit = func(pipeline.CommitInput) (pipeline.CommitResult, error) {
		return pipeline.CommitResult{GlossaryViolations: 1}, nil
	}

	result, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeNMT))
	require.ErrorIs(t, err, pipeline.ErrQAGateFailed)
	require.Equal(t, 1, result.GlossaryViolations)
}

func TestRunBook_CancelStillFinishesAndRecordsSpend(t *testing.T) {
	t.Parallel()

	steps := script(pipeline.ModeNMT, chapterScript{id: "ch_0001", fresh: []string{"a"}, chars: 10})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt := &pipeline.InProcessRuntime{Ctx: ctx}

	_, err := pipeline.RunBook(steps, rt, nominalInput(pipeline.ModeNMT))
	require.ErrorIs(t, err, context.Canceled)

	// The cleanup runs under a detached runtime: a cancelled run still writes
	// what artifacts it can and records its spend.
	require.Equal(t, 1, steps.finishCalls)
	require.Equal(t, 1, steps.spendCalls)
	// It was cancelled before any chapter work.
	require.NotContains(t, steps.calls, "resolve:ch_0001")
}

func TestRunBook_LiteraryPathDriftsBatchesAndBible(t *testing.T) {
	t.Parallel()

	steps := script(pipeline.ModeLiterary,
		chapterScript{id: "ch_0001", reused: 1, fresh: []string{"a", "b"}, chars: 12},
		chapterScript{id: "ch_0002", reused: 0, fresh: []string{"c", "d"}, chars: 8},
	)
	steps.refine = func(in pipeline.RefineInput) (pipeline.RefineResult, error) {
		return pipeline.RefineResult{
			Usage:   literaryUsage(6, 3),
			Refined: len(in.IDs),
			Tail:    "queue-" + in.ChapterID,
		}, nil
	}
	steps.commit = func(in pipeline.CommitInput) (pipeline.CommitResult, error) {
		require.True(t, in.Persist, "literary commit must persist")
		return pipeline.CommitResult{}, nil
	}
	steps.bible = func(in pipeline.BibleInput) (pipeline.BibleResult, error) {
		if in.ChapterID == "ch_0001" {
			return pipeline.BibleResult{Version: 1}, nil
		}
		return pipeline.BibleResult{Version: 2}, nil
	}

	in := nominalInput(pipeline.ModeLiterary)
	in.UpdateBible = true
	result, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), in)
	require.NoError(t, err)

	require.Equal(t, pipeline.ModeLiterary, result.Mode)
	require.Equal(t, "series-a", result.SeriesID)
	require.Equal(t, 4, result.NMTSegments) // 2 + 2 fresh drafted
	require.Equal(t, 1, result.TMHits)
	require.Equal(t, 4, result.LLMSegments)
	require.Equal(t, 4, result.LLMPassSegments)
	require.Equal(t, 12, result.LLMInputTokens) // one batch per chapter, 6 in each
	require.Equal(t, 6, result.LLMOutputTokens) // one batch per chapter, 3 in each
	require.Equal(t, 2, result.BibleVersion)
	require.Equal(t, 0, result.GlossaryViolations)
	require.Equal(t, 2, steps.updateBible)

	// One refine call per chapter (2 fresh < batch size). A chapter's first batch
	// carries no translated tail; the previous chapter's id is what RefineBatch
	// turns into the source-tail context.
	require.Len(t, steps.refineInputs, 2)
	require.Equal(t, "", steps.refineInputs[0].PrevTail)
	require.Equal(t, "", steps.refineInputs[0].PrevChapterID)
	require.Equal(t, "", steps.refineInputs[1].PrevTail)
	require.Equal(t, "ch_0001", steps.refineInputs[1].PrevChapterID)
}

func TestRunBook_LiteraryChunksFreshByBatchSize(t *testing.T) {
	t.Parallel()

	fresh := freshIDs(25)
	steps := literaryBatched(fresh, 10)
	steps.refine = func(in pipeline.RefineInput) (pipeline.RefineResult, error) {
		return pipeline.RefineResult{Refined: len(in.IDs), Tail: "queue-" + in.IDs[len(in.IDs)-1]}, nil
	}

	_, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeLiterary))
	require.NoError(t, err)

	// 25 fresh at batch size 10 → three batches partitioning the ids in order.
	require.Len(t, steps.refineInputs, 3)
	require.Equal(t, fresh[0:10], steps.refineInputs[0].IDs)
	require.Equal(t, fresh[10:20], steps.refineInputs[1].IDs)
	require.Equal(t, fresh[20:25], steps.refineInputs[2].IDs)

	// The tail threads batch to batch within the chapter: each batch's preceding
	// context is the previous batch's returned tail; the first opens with none.
	require.Equal(t, "", steps.refineInputs[0].PrevTail)
	require.Equal(t, "queue-"+fresh[9], steps.refineInputs[1].PrevTail)
	require.Equal(t, "queue-"+fresh[19], steps.refineInputs[2].PrevTail)
	// PrevChapterID is only ever set on a chapter's first batch; there is no
	// previous chapter here.
	require.Equal(t, "", steps.refineInputs[0].PrevChapterID)
	require.Equal(t, "", steps.refineInputs[1].PrevChapterID)
	require.Equal(t, "", steps.refineInputs[2].PrevChapterID)
}

func TestRunBook_LiteraryDegradedBatchDoesNotStopTheChapter(t *testing.T) {
	t.Parallel()

	fresh := freshIDs(25)
	steps := literaryBatched(fresh, 10)
	calls := 0
	steps.refine = func(in pipeline.RefineInput) (pipeline.RefineResult, error) {
		calls++
		res := pipeline.RefineResult{Usage: literaryUsage(6, 3), Refined: len(in.IDs)}
		if calls == 1 {
			// The first batch kept two ids on their draft — a degraded result,
			// not an error. The chapter must carry on.
			res.Refined = len(in.IDs) - 2
			res.Degraded = in.IDs[:2]
		}
		return res, nil
	}

	result, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeLiterary))
	require.NoError(t, err, "a degraded batch keeps its draft but does not stop the chapter")

	require.Len(t, steps.refineInputs, 3, "the batches after the degraded one still run")
	require.Equal(t, 1, steps.commitCalls, "the chapter still reaches commit")
	// Only the refined segments count; the degraded pair kept its draft.
	require.Equal(t, 23, result.LLMSegments)
}

// TestRunBook_LiteraryChapterRefiningNothingFailsLoudly is the chapter-level
// backstop the per-batch split would otherwise drop: when every batch degrades
// and not one segment is refined, the chapter is a wall of NMT drafts literary
// mode never persists. QA cannot see it — a draft has a non-empty target — so
// the run must fail here rather than report a finished volume with nothing to
// resume from, exactly as the old chapter-level pass returned ErrEmptyResponse.
func TestRunBook_LiteraryChapterRefiningNothingFailsLoudly(t *testing.T) {
	t.Parallel()

	fresh := freshIDs(20)
	steps := literaryBatched(fresh, 10)
	steps.refine = func(in pipeline.RefineInput) (pipeline.RefineResult, error) {
		// Every batch degrades: the model refused, so each segment keeps its
		// draft and nothing is refined. Not an error per batch — a degraded
		// result.
		return pipeline.RefineResult{Usage: literaryUsage(6, 3), Refined: 0, Degraded: in.IDs}, nil
	}

	_, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeLiterary))

	var stageErr *pipeline.StageError
	require.ErrorAs(t, err, &stageErr)
	require.Equal(t, pipeline.StageTranslate, stageErr.Stage)
	require.ErrorIs(t, err, pipeline.ErrChapterUnrefined)

	require.Zero(t, steps.commitCalls, "a chapter that refined nothing must not commit drafts")
	// A failure past the guardrail still records what the degraded calls spent.
	require.Equal(t, 1, steps.spendCalls)
}

// TestRunBook_LiteraryProgressAdvancesOnRefinement pins the bar cadence: in
// literary mode "done" tracks the refine pass (segments a completed batch
// carried), not the draft. Drafting a whole chapter must not fill the bar and
// leave it at 100% for the slow LLM pass — the regression the watcher of a
// multi-hour volume would feel most.
func TestRunBook_LiteraryProgressAdvancesOnRefinement(t *testing.T) {
	t.Parallel()

	fresh := freshIDs(20)
	steps := literaryBatched(fresh, 10)
	var atFirstRefine pipeline.Progress
	seen := false
	rep := &recordingReporter{}
	steps.refine = func(in pipeline.RefineInput) (pipeline.RefineResult, error) {
		// Snapshot what a watcher saw the instant the first refine batch was
		// dispatched: the whole chapter is drafted by now, but no batch has been
		// counted yet, so the bar must still read zero done.
		if !seen {
			seen = true
			atFirstRefine = rep.last()
		}
		return pipeline.RefineResult{Usage: literaryUsage(6, 3), Refined: len(in.IDs)}, nil
	}

	_, err := pipeline.RunBook(steps, newRuntime(rep), nominalInput(pipeline.ModeLiterary))
	require.NoError(t, err)

	require.Equal(t, 20, atFirstRefine.Segments)
	require.Zero(t, atFirstRefine.SegmentsDone,
		"drafting must not advance the bar in literary mode; only the refine pass does")
	require.Equal(t, 20, rep.last().SegmentsDone, "the bar reaches the total once every batch is refined")
}

func TestRunBook_LiteraryRateLimitFailsRunButStillRecordsSpend(t *testing.T) {
	t.Parallel()

	fresh := freshIDs(20)
	steps := literaryBatched(fresh, 10)
	calls := 0
	steps.refine = func(in pipeline.RefineInput) (pipeline.RefineResult, error) {
		calls++
		if calls == 2 {
			// The quota is exhausted mid-chapter. RefineBatch reports the tokens
			// the refused call consumed alongside a translate-stage error.
			return pipeline.RefineResult{Usage: literaryUsage(5, 1)},
				&pipeline.StageError{Stage: pipeline.StageTranslate, Err: wrapErr(llm.ErrRateLimited)}
		}
		return pipeline.RefineResult{Usage: literaryUsage(6, 3), Refined: len(in.IDs)}, nil
	}

	_, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeLiterary))

	var stageErr *pipeline.StageError
	require.ErrorAs(t, err, &stageErr)
	require.Equal(t, pipeline.StageTranslate, stageErr.Stage)
	require.ErrorIs(t, err, llm.ErrRateLimited)

	// The third batch never ran: the rate limit stopped the chapter.
	require.Len(t, steps.refineInputs, 2)

	// A failure past the guardrail still records spend, with the usage the run
	// accumulated before it died — the first batch's tokens. The refused call's
	// usage is not folded in: the flow drops the failing batch's result.
	require.Equal(t, 1, steps.spendCalls)
	require.Equal(t, 6, steps.spendInput.Result.LLMInputTokens)
	require.Equal(t, 3, steps.spendInput.Result.LLMOutputTokens)
}

func TestRunBook_ResumeIsAllReuse(t *testing.T) {
	t.Parallel()

	// A resumed book: the TM serves every segment, so no chapter is drafted.
	steps := script(pipeline.ModeNMT,
		chapterScript{id: "ch_0001", reused: 3, fresh: nil},
		chapterScript{id: "ch_0002", reused: 2, fresh: nil},
	)

	result, err := pipeline.RunBook(steps, newRuntime(&recordingReporter{}), nominalInput(pipeline.ModeNMT))
	require.NoError(t, err)

	require.Equal(t, 5, result.TMHits)
	require.Zero(t, result.NMTSegments)
	require.Zero(t, result.NMTChars)
	require.NotContains(t, steps.calls, "draft:ch_0001")
	require.NotContains(t, steps.calls, "draft:ch_0002")
	require.InDelta(t, 1.0, result.TMHitRatio, 1e-9)
}

func wrapErr(err error) error { return fmt.Errorf("wrapped, err: %w", err) }

func literaryUsage(in, out int) literary.Usage {
	return literary.Usage{InputTokens: in, OutputTokens: out}
}

// freshIDs builds n numbered segment ids, the fresh split a literary chapter is
// chunked into by batch size.
func freshIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("s%03d", i+1)
	}
	return ids
}

// literaryBatched scripts a single literary chapter whose fresh ids the flow
// chunks by batchSize, overriding the manifest's default literary batch size.
func literaryBatched(fresh []string, batchSize int) *fakeSteps {
	steps := script(pipeline.ModeLiterary, chapterScript{id: "ch_0001", fresh: fresh, chars: 100})
	steps.prepare = func(pipeline.BookInput) (pipeline.Manifest, error) {
		return pipeline.Manifest{
			Chapters:  []pipeline.ChapterInfo{{ID: "ch_0001", Segments: len(fresh)}},
			NMTEngine: "nmt", BatchSize: batchSize,
		}, nil
	}
	return steps
}
