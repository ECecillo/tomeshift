package pipeline

import (
	"context"
	"fmt"

	"github.com/ECecillo/tomeshift/internal/qa"
)

// Progress stage labels. Coarser than the old per-sub-stage bar because a stage
// is now an activity boundary: extract/parse/segment all happen inside
// PrepareBook, which the flow cannot see into.
const (
	stagePrepare   = "prepare"
	stageTranslate = "translate"
	stageRecompose = "recompose"
	stageFinish    = "qa"
)

// flow accumulates the RunResult over the ports as a book is translated. It is
// the receiver the per-chapter mode recipes drive, and it publishes Progress as
// it goes. It holds no engine detail: everything it touches is a Steps call or
// an rt method.
type flow struct {
	steps    Steps
	rt       Runtime
	in       BookInput
	ref      RunRef
	mode     Mode
	result   *RunResult
	chapters []ChapterInfo

	usedFreeChars int
	batchSize     int
	chapterIdx    int    // 1-based chapter currently being worked on
	prevChapterID string // spine-previous chapter id, for a chapter's first literary batch context

	qeSum   float64
	qeCount int

	lastCost  float64
	finalised bool
	finished  bool
}

// RunBook executes one volume over the ports. It accumulates the RunResult,
// publishes Progress, and returns *StageError / wrapped ErrQAGateFailed /
// wrapped cost.ErrBudgetExceeded exactly as the in-process engine did before
// the refactor, so exit codes are unchanged. It is deterministic: rt.Now() is
// its only clock and it makes no decision on map order or goroutine scheduling.
func RunBook(steps Steps, rt Runtime, in BookInput) (*RunResult, error) {
	mode, err := selectMode(in)
	if err != nil {
		return nil, &StageError{Stage: StageTranslate, Err: err}
	}

	ref := RunRef{RunID: in.RunID, BookID: in.BookID, OutputDir: in.OutputDir, InputPath: in.InputPath}
	f := &flow{steps: steps, rt: rt, in: in, ref: ref, mode: mode}
	f.report(stagePrepare)

	manifest, err := steps.PrepareBook(in)
	if err != nil {
		return nil, err
	}

	result := &RunResult{
		SchemaVersion: 1,
		RunID:         in.RunID,
		BookID:        in.BookID,
		Mode:          runMode(in),
		StartedAt:     rt.Now().UTC(),
		TotalSegments: totalSegments(manifest.Chapters),
		NMTEngine:     manifest.NMTEngine,
		LLMProvider:   manifest.LLMProvider,
		LLMModel:      manifest.LLMModel,
	}
	if runMode(in) == ModeLiterary {
		result.SeriesID = in.SeriesID
	}
	f.result = result
	f.chapters = manifest.Chapters
	f.batchSize = manifest.BatchSize

	// Pre-run budget guardrail: it prices the work still to do and refuses the
	// run before any provider call if a cap is exceeded. NoReuseUSD is the whole
	// book with no reuse — the report's baseline — and BibleVersion (the loaded
	// bible) are stamped even on the refusal, matching the pre-split engine.
	proj, err := steps.ProjectCost(ProjectInput{Ref: ref, Mode: runMode(in), SeriesID: in.SeriesID})
	result.EstimatedCostUSD = proj.NoReuseUSD
	result.BibleVersion = proj.BibleVersion
	if err != nil {
		return result, err
	}
	f.usedFreeChars = proj.UsedFreeChars

	// From here the run can call paid providers, so its consumption is recorded
	// whatever happens next — and, like today's deferred ledger write, under a
	// detached runtime so a cancelled context still writes.
	defer f.cleanup()

	f.report(stageTranslate)
	for i := range manifest.Chapters {
		if rt.Cancelled() {
			return nil, &StageError{Stage: StageTranslate, Err: context.Canceled}
		}
		f.chapterIdx = i + 1
		if err := mode.Chapter(f, manifest.Chapters[i]); err != nil {
			return nil, err
		}
	}

	f.report(stageRecompose)
	rec, err := steps.RecomposeBook(ref)
	if err != nil {
		return nil, err
	}
	result.OutputEPUB = rec.OutputEPUB

	f.report(stageFinish)
	check, err := steps.CheckEPUB(CheckInput(rec))
	if err != nil {
		return nil, err
	}
	result.QA.EPUBCheckPassed = check.Passed
	result.QA.EPUBCheckSkipped = check.Skipped

	f.finalise()
	if result.TotalSegments > 0 {
		result.TMHitRatio = float64(result.TMHits) / float64(result.TotalSegments)
		result.LLMEscalationRatio = float64(result.LLMSegments) / float64(result.TotalSegments)
	}
	if f.qeCount > 0 {
		result.AvgQualityScore = f.qeSum / float64(f.qeCount)
		result.QualityScoredSegments = f.qeCount
	}
	if savings := result.EstimatedCostUSD - result.ActualCostUSD; savings > 0 {
		result.CostSavingsUSD = savings
	}
	result.EndedAt = rt.Now().UTC()

	report, err := steps.FinishBook(FinishInput{Ref: ref, Result: *result})
	if err != nil {
		return result, err
	}
	result.QA = report
	f.finished = true

	if !qaGatePassed(report) || result.GlossaryViolations > 0 {
		return result, fmt.Errorf("unable to pass QA gate, err: %w", ErrQAGateFailed)
	}
	return result, nil
}

// cleanup records the run's spend on every exit past the guardrail, and — for a
// run that failed before FinishBook — writes what artifacts it can so a failed
// run stays diagnosable. It runs under a detached runtime: FinishBook and
// RecordSpend detach from a cancelled context internally, and rt.Detached()
// supplies the clock and the not-cancelled view the cleanup runs under.
func (f *flow) cleanup() {
	d := f.rt.Detached()
	f.finalise()
	if f.result.EndedAt.IsZero() {
		f.result.EndedAt = d.Now().UTC()
	}
	if !f.finished {
		if report, err := f.steps.FinishBook(FinishInput{Ref: f.ref, Result: *f.result}); err == nil {
			f.result.QA = report
		}
	}
	_ = f.steps.RecordSpend(SpendInput{Ref: f.ref, Result: *f.result})
}

// finalise prices the run once. It is single-shot so the success path (which
// calls it before writing the report) and the deferred cleanup agree. NMTChars
// is already summed from each DraftChapter, so pricing is pure arithmetic.
func (f *flow) finalise() {
	if f.finalised {
		return
	}
	f.finalised = true
	usd, err := f.steps.PriceRun(PriceInput{Result: *f.result, UsedFreeChars: f.usedFreeChars, Mode: runMode(f.in)})
	if err == nil {
		f.result.ActualCostUSD = usd
	}
}

// ---------------------------------------------------------------------------
// Per-chapter helpers the mode recipes compose.
// ---------------------------------------------------------------------------

// resolve serves a chapter from the TM and records the reuse.
func (f *flow) resolve(ch ChapterInfo, reusesDrafts bool) (ChapterPlan, error) {
	plan, err := f.steps.ResolveChapter(ResolveInput{Ref: f.ref, ChapterID: ch.ID, ReusesDrafts: reusesDrafts})
	if err != nil {
		return ChapterPlan{}, err
	}
	f.result.TMHits += plan.Reused
	f.report(stageTranslate)
	return plan, nil
}

// draft drafts a chapter's fresh segments in one batched provider call.
func (f *flow) draft(ch ChapterInfo, fresh []string, persist bool) error {
	res, err := f.steps.DraftChapter(DraftInput{
		Ref: f.ref, ChapterID: ch.ID, Fresh: fresh, PersistDrafts: persist,
		SourceLang: f.in.SourceLang, TargetLang: f.in.TargetLang,
	})
	if err != nil {
		return err
	}
	f.result.NMTSegments += len(fresh)
	f.result.NMTChars += res.Chars
	return nil
}

// escalate runs the NMT-mode quality estimation and selective post-edit.
func (f *flow) escalate(ch ChapterInfo, fresh []string) error {
	res, err := f.steps.EscalateChapter(EscalateInput{
		Ref: f.ref, ChapterID: ch.ID, Fresh: fresh,
		SourceLang: f.in.SourceLang, TargetLang: f.in.TargetLang,
	})
	if err != nil {
		return err
	}
	f.result.LLMAttempts += res.Attempts
	f.result.LLMPassSegments += res.Attempts
	f.result.LLMChars += res.Chars
	f.result.LLMSegments += res.Improved
	f.qeSum += res.QESum
	f.qeCount += res.QECount
	return nil
}

// preceding French across batches: within a chapter the previous batch's
// translated tail, and for the first batch the previous chapter's source tail —
// resolved by RefineBatch from PrevChapterID, matching the pre-split walk.
func (f *flow) refine(ch ChapterInfo, fresh []string) error {
	prevTail := ""
	refined := 0
	for i, batch := range chunkIDs(fresh, f.batchSize) {
		in := RefineInput{
			Ref: f.ref, ChapterID: ch.ID, SeriesID: f.in.SeriesID,
			IDs: batch, PrevTail: prevTail,
			SourceLang: f.in.SourceLang, TargetLang: f.in.TargetLang,
		}
		if i == 0 {
			in.PrevChapterID = f.prevChapterID
		}
		res, err := f.steps.RefineBatch(in)
		if err != nil {
			return err
		}
		f.result.LLMPassSegments += len(batch)
		f.result.LLMInputTokens += res.Usage.InputTokens
		f.result.LLMOutputTokens += res.Usage.OutputTokens
		f.result.LLMCacheReadTokens += res.Usage.CacheReadTokens
		f.result.LLMCacheCreationTokens += res.Usage.CacheCreationTokens
		f.result.LLMSegments += res.Refined
		refined += res.Refined
		prevTail = res.Tail
		f.priceReport(stageTranslate)
	}
	// The chapter-level backstop the per-batch split would otherwise lose: if
	// every batch degraded and not one segment was refined, the chapter is a
	// wall of NMT drafts that literary mode does not persist — a book that reads
	// as finished with nothing to resume from. QA cannot see it (a draft has a
	// non-empty target), so it is failed here, exactly as the old chapter-level
	// TranslateChapter returned ErrEmptyResponse.
	if refined == 0 {
		return &StageError{Stage: StageTranslate, Err: fmt.Errorf(
			"unable to refine any segment of chapter %s, err: %w", ch.ID, ErrChapterUnrefined)}
	}
	return nil
}

// commit validates and persists a chapter's translations.
func (f *flow) commit(ch ChapterInfo, persist bool) error {
	res, err := f.steps.CommitChapter(CommitInput{
		Ref: f.ref, ChapterID: ch.ID, Persist: persist,
		SourceLang: f.in.SourceLang, TargetLang: f.in.TargetLang,
	})
	if err != nil {
		return err
	}
	f.result.GlossaryViolations += res.GlossaryViolations
	return nil
}

// updateBible evolves the series bible from a committed chapter.
func (f *flow) updateBible(ch ChapterInfo) error {
	res, err := f.steps.UpdateBible(BibleInput{Ref: f.ref, SeriesID: f.in.SeriesID, ChapterID: ch.ID})
	if err != nil {
		return err
	}
	f.result.BibleVersion = res.Version
	return nil
}

// ---------------------------------------------------------------------------
// Progress.
// ---------------------------------------------------------------------------

// report publishes the current progress snapshot without repricing.
func (f *flow) report(stage string) {
	f.rt.Report(f.progress(stage))
}

// priceReport reprices the run and publishes the progress with the new cost.
func (f *flow) priceReport(stage string) {
	if usd, err := f.steps.PriceRun(PriceInput{Result: *f.result, UsedFreeChars: f.usedFreeChars, Mode: runMode(f.in)}); err == nil {
		f.lastCost = usd
	}
	f.rt.Report(f.progress(stage))
}

func (f *flow) progress(stage string) Progress {
	p := Progress{
		State:    "running",
		Stage:    stage,
		Chapter:  f.chapterIdx,
		Chapters: len(f.chapters),
		CostUSD:  f.lastCost,
	}
	if f.result != nil {
		p.Segments = f.result.TotalSegments
		// Literary advances on the refine pass (segments carried by a completed
		// batch), not at draft time — otherwise the bar fills the instant a
		// chapter is drafted and then sits at 100% for the whole LLM pass. NMT
		// has no refine pass, so its output is the draft.
		if runMode(f.in) == ModeLiterary {
			p.SegmentsDone = f.result.TMHits + f.result.LLMPassSegments
		} else {
			p.SegmentsDone = f.result.TMHits + f.result.NMTSegments
		}
		p.TMHits = f.result.TMHits
	}
	return p
}

// ---------------------------------------------------------------------------
// Pure helpers.
// ---------------------------------------------------------------------------

// qaGatePassed combines static QA thresholds with the epubcheck verdict. A
// skipped epubcheck (binary absent) does not block the run.
func qaGatePassed(report qa.Report) bool {
	if !report.Passed() {
		return false
	}
	if !report.EPUBCheckSkipped && !report.EPUBCheckPassed {
		return false
	}
	return true
}

func totalSegments(chapters []ChapterInfo) int {
	total := 0
	for i := range chapters {
		total += chapters[i].Segments
	}
	return total
}

// chunkIDs splits ids into batches of at most size. A size below 1 yields a
// single batch — no batching.
func chunkIDs(ids []string, size int) [][]string {
	if size < 1 {
		size = len(ids)
	}
	if size < 1 {
		return nil
	}
	var out [][]string
	for i := 0; i < len(ids); i += size {
		end := i + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[i:end])
	}
	return out
}
