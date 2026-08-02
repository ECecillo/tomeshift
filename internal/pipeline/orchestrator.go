package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/ECecillo/ebooks-translator/internal/cost"
	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/literary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
	"github.com/ECecillo/ebooks-translator/internal/qa"
	"github.com/ECecillo/ebooks-translator/internal/segment"
	"github.com/ECecillo/ebooks-translator/internal/tm"
	"github.com/ECecillo/ebooks-translator/internal/translate"
)

const xhtmlMediaType = "application/xhtml+xml"

// ledgerWriteTimeout bounds the detached write used when a run is recorded
// after its context was cancelled.
const ledgerWriteTimeout = 5 * time.Second

type chapterSegments struct {
	ID       string
	Href     string
	Segments []segment.Segment
}

// QualityEstimator scores a translated segment without a reference (e.g.
// COMET-QE). Scores are higher-is-better; the pipeline uses them to drive
// selective LLM escalation and to report per-book quality.
type QualityEstimator interface {
	Estimate(ctx context.Context, source, candidate string) (float64, error)
}

// Orchestrator coordinates the V1 translation pipeline across its stages. All
// collaborators are injected so each stage stays independently testable.
type Orchestrator struct {
	Extractor          epub.Extractor
	Parser             epub.Parser
	Segmenter          segment.Segmenter
	Placeholders       segment.PlaceholderManager
	TM                 tm.Store
	Translator         translate.Translator
	Recomposer         epub.Recomposer
	EPUBCheck          qa.EPUBChecker
	Glossary           *glossary.Glossary // optional; nil disables locked-term enforcement
	PostEditor         llm.PostEditor     // optional; nil disables selective LLM post-edit
	LiteraryTranslator *literary.Translator
	NarrativeStore     narrative.Store
	NarrativeProfiler  *narrative.Profiler
	EscalateMinChars   int              // escalate NMT segments with >= this many source chars (0 = all, when PostEditor set)
	QualityEstimator   QualityEstimator // optional; reference-free QE for escalation + reporting
	EscalateBelowScore float64          // escalate fresh segments whose QE score < this (0 disables)
	CostModel          cost.Model       // zero-value = free local provider
	BudgetUSD          float64          // per-book hard cap; <= 0 disables the check
	DailyUSD           float64          // daily spend cap; <= 0 disables the check
	MonthlyUSD         float64          // monthly spend cap; <= 0 disables the check
	Logger             *zap.Logger
}

// Run executes the full pipeline and always attempts to emit run artifacts. It
// returns a *StageError on stage failure and a wrapped ErrQAGateFailed when the
// output is produced but fails QA.
//
// The NMT and literary pipelines share this whole body: they differ only in how
// they project cost and how they translate, which is what translationMode
// abstracts.
func (o *Orchestrator) Run(ctx context.Context, cfg RunConfig) (*RunResult, error) {
	log := o.logger().With(
		zap.String("run_id", cfg.RunID),
		zap.String("book_id", cfg.BookID),
		zap.String("mode", runMode(cfg)),
		zap.Bool("resume", cfg.Resume),
	)
	started := time.Now().UTC()

	raw, err := o.Extractor.Extract(ctx, cfg.InputPath)
	if err != nil {
		return nil, &StageError{Stage: StageExtract, Err: err}
	}

	book, err := o.Parser.Parse(ctx, raw)
	if err != nil {
		return nil, &StageError{Stage: StageParse, Err: err}
	}

	chapters, err := o.segmentBookByChapter(ctx, book)
	if err != nil {
		return nil, &StageError{Stage: StageSegment, Err: err}
	}
	log.Info("segmented book",
		zap.Int("segments", countSegments(chapters)), zap.Int("chapters", len(chapters)))

	mode, err := o.newTranslationMode(ctx, cfg)
	if err != nil {
		return nil, &StageError{Stage: StageTranslate, Err: err}
	}

	result := &RunResult{
		SchemaVersion: 1,
		RunID:         cfg.RunID,
		BookID:        cfg.BookID,
		StartedAt:     started,
		TotalSegments: countSegments(chapters),
		NMTEngine:     o.nmtEngine(),
	}
	mode.Init(result)

	// Pre-run budget guardrail: project worst-case (no-reuse) spend and abort
	// before any provider call if it exceeds the per-book cap.
	usedFreeChars, err := o.freeCharsUsed(ctx, started)
	if err != nil {
		return nil, &StageError{Stage: StageTranslate, Err: err}
	}

	projection := mode.Project(chapters, usedFreeChars)
	result.EstimatedCostUSD = projection.EstimatedUSD
	log.Info("projected run cost",
		zap.Int("llm_calls", projection.LLMCalls),
		zap.Int("billable_nmt_chars", projection.BillableChars),
		zap.Int("llm_input_tokens", projection.InputTokens),
		zap.Int("llm_output_tokens", projection.OutputTokens),
		zap.Float64("nmt_usd", projection.NMTCostUSD),
		zap.Float64("llm_usd", projection.LLMCostUSD),
		zap.Float64("estimated_usd", projection.EstimatedUSD))
	if o.BudgetUSD > 0 && projection.EstimatedUSD > o.BudgetUSD {
		log.Warn("projected spend exceeds per-book budget cap",
			zap.Float64("estimated_usd", projection.EstimatedUSD),
			zap.Float64("budget_usd", o.BudgetUSD))
		return result, fmt.Errorf("unable to start run (estimated $%.4f > per-book cap $%.4f), err: %w",
			projection.EstimatedUSD, o.BudgetUSD, cost.ErrBudgetExceeded)
	}
	if err := o.checkRollingBudgets(ctx, log, projection.EstimatedUSD, started); err != nil {
		return result, err
	}

	// From here on the run can call paid providers, so its consumption is
	// totalled and recorded whatever happens next. A run that drafted half a
	// book and then failed to recompose has still been billed for that half,
	// and dropping the record would leave the next run's free-tier projection
	// wrong by exactly that much.
	//
	// finalise is single-shot: the success path calls it before writing the
	// report so the artifacts carry the same figures the ledger does, and the
	// deferred call covers every path that never got there.
	// The retry counter lives as long as the translator, and `batch` shares one
	// across every book, so a run is charged the delta over its own lifetime
	// rather than the running total.
	retriedBefore := o.retriedChars()

	var finalised bool
	finalise := func() {
		if finalised {
			return
		}
		finalised = true
		result.NMTChars += o.retriedChars() - retriedBefore
		result.ActualCostUSD = mode.Cost(result, usedFreeChars)
	}
	defer func() {
		finalise()
		o.recordSpend(ctx, cfg, log, result)
	}()

	segments, err := mode.Translate(ctx, log, chapters, usedFreeChars, result)
	if err != nil {
		return nil, &StageError{Stage: StageTranslate, Err: err}
	}

	outputEPUB := fmt.Sprintf("%s/translated.epub", cfg.OutputDir)
	if err := o.Recomposer.Recompose(ctx, raw, book, outputEPUB); err != nil {
		return nil, &StageError{Stage: StageRecompose, Err: err}
	}
	result.OutputEPUB = outputEPUB

	report := qa.Evaluate(book, segments)
	check, err := o.EPUBCheck.Check(ctx, outputEPUB)
	if err != nil {
		return nil, &StageError{Stage: StageQA, Err: err}
	}
	report.EPUBCheckPassed = check.Passed
	report.EPUBCheckSkipped = check.Skipped
	result.QA = report

	finalise()
	if result.TotalSegments > 0 {
		result.TMHitRatio = float64(result.TMHits) / float64(result.TotalSegments)
		result.LLMEscalationRatio = float64(result.LLMSegments) / float64(result.TotalSegments)
	}
	if savings := result.EstimatedCostUSD - result.ActualCostUSD; savings > 0 {
		result.CostSavingsUSD = savings
	}
	result.EndedAt = time.Now().UTC()

	// Artifacts are emitted regardless of the QA verdict so failed runs remain
	// diagnosable.
	if err := WriteArtifacts(cfg, result, segments); err != nil {
		return result, &StageError{Stage: StageQA, Err: err}
	}

	if !qaGatePassed(report) || result.GlossaryViolations > 0 {
		log.Warn("QA gate failed",
			zap.Float64("placeholder_restore_ratio", report.PlaceholderRestoreRatio),
			zap.Int("missing_spine_docs", report.MissingSpineDocs),
			zap.Int("untranslated_segments", report.UntranslatedSegments),
			zap.Int("glossary_violations", result.GlossaryViolations),
			zap.Bool("epubcheck_passed", report.EPUBCheckPassed),
			zap.Bool("epubcheck_skipped", report.EPUBCheckSkipped),
		)
		return result, fmt.Errorf("unable to pass QA gate, err: %w", ErrQAGateFailed)
	}

	log.Info("run completed",
		zap.Int("tm_hits", result.TMHits),
		zap.Int("nmt_segments", result.NMTSegments),
		zap.Int("llm_segments", result.LLMSegments),
		zap.Int("bible_version", result.BibleVersion),
		zap.String("output", outputEPUB),
	)
	return result, nil
}

// nmtEngine identifies the provider whose allowance a run consumes. It is empty
// when no translator is configured, which is the case for a dry run.
func (o *Orchestrator) nmtEngine() string {
	if o.Translator == nil {
		return ""
	}
	return o.Translator.Engine()
}

// retryReporter is implemented by translators that re-send text on a transient
// failure. It is optional: an adapter with no retries has nothing to report.
type retryReporter interface {
	RetriedChars() int
}

// retriedChars reads the translator's lifetime count of re-sent characters, or
// 0 from an adapter that does not retry.
func (o *Orchestrator) retriedChars() int {
	reporter, ok := o.Translator.(retryReporter)
	if !ok {
		return 0
	}
	return reporter.RetriedChars()
}

// recordSpend persists what the run consumed, for rolling-budget and free-tier
// accounting. It is deferred, so it fires on the failure paths too — the
// provider does not refund a run that died after it was called.
//
// Best effort: a ledger write failure must not turn an otherwise successful run
// into a failed one.
func (o *Orchestrator) recordSpend(ctx context.Context, cfg RunConfig, log *zap.Logger, result *RunResult) {
	if o.TM == nil {
		return
	}

	if result.EndedAt.IsZero() {
		result.EndedAt = time.Now().UTC()
	}

	// A context that is already cancelled would reject the write, and losing
	// the record is worse than the delay: fall back to a detached one.
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
		defer cancel()
	}

	if err := o.TM.RecordRun(ctx, tm.RunSpend{
		RunID:         cfg.RunID,
		BookID:        cfg.BookID,
		StartedAt:     result.StartedAt,
		EndedAt:       result.EndedAt,
		ActualCostUSD: result.ActualCostUSD,
		NMTEngine:     result.NMTEngine,
		NMTChars:      result.NMTChars,
	}); err != nil {
		log.Warn("unable to record run spend", zap.Error(err))
	}
}

// freeCharsUsed reports how much of this provider's monthly free character
// allowance the calendar month has already consumed, so the projection bills
// only the overflow. It fails closed: a ledger read error aborts the run rather
// than silently projecting a free draft.
//
// The count is per engine. The ledger is shared and the default provider is a
// local dev server, so a month of free local translation would otherwise be
// charged against a paid provider's allowance.
func (o *Orchestrator) freeCharsUsed(ctx context.Context, now time.Time) (int, error) {
	if o.TM == nil || o.CostModel.FreeCharsPerMonth <= 0 {
		return 0, nil
	}

	since := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	used, err := o.TM.CharsSince(ctx, since, o.nmtEngine())
	if err != nil {
		return 0, fmt.Errorf("unable to read this month's NMT characters, err: %w", err)
	}
	return used, nil
}

// checkRollingBudgets enforces daily and monthly spend caps against the ledger
// plus this run's projected cost. It fails closed: a ledger read error aborts
// the run rather than silently bypassing the cap.
func (o *Orchestrator) checkRollingBudgets(ctx context.Context, log *zap.Logger, projected float64, now time.Time) error {
	if o.TM == nil {
		return nil
	}

	windows := []struct {
		name  string
		cap   float64
		since time.Time
	}{
		{"daily", o.DailyUSD, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)},
		{"monthly", o.MonthlyUSD, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)},
	}

	for _, w := range windows {
		if w.cap <= 0 {
			continue
		}
		spent, err := o.TM.SpendSince(ctx, w.since)
		if err != nil {
			return fmt.Errorf("unable to read %s spend, err: %w", w.name, err)
		}
		if spent+projected > w.cap {
			log.Warn("projected spend exceeds rolling budget cap",
				zap.String("window", w.name),
				zap.Float64("already_spent_usd", spent),
				zap.Float64("projected_usd", projected),
				zap.Float64("cap_usd", w.cap))
			return fmt.Errorf("unable to start run (%s spend $%.4f + projected $%.4f > cap $%.4f), err: %w",
				w.name, spent, projected, w.cap, cost.ErrBudgetExceeded)
		}
	}

	return nil
}

func (o *Orchestrator) loadBible(ctx context.Context, seriesID string) (narrative.Bible, error) {
	if seriesID == "" {
		return narrative.Bible{}, narrative.ErrEmptySeriesID
	}
	if o.NarrativeStore == nil {
		return narrative.Bible{SeriesID: seriesID}, nil
	}

	bible, found, err := o.NarrativeStore.Load(ctx, seriesID)
	if err != nil {
		return narrative.Bible{}, err
	}
	if !found {
		return narrative.Bible{SeriesID: seriesID}, nil
	}
	return bible, nil
}

func (o *Orchestrator) updateBible(ctx context.Context, seriesID string, bible *narrative.Bible, chapter *chapterSegments) error {
	if o.NarrativeProfiler == nil {
		return nil
	}

	updated, err := o.NarrativeProfiler.Update(ctx, *bible, chapter.ID, chapterSourceText(chapter.Segments), chapterTargetText(chapter.Segments))
	if err != nil {
		return err
	}
	if updated.SeriesID == "" {
		updated.SeriesID = seriesID
	}
	updated.Version = bible.Version

	if o.NarrativeStore != nil {
		if err := o.NarrativeStore.Save(ctx, updated); err != nil {
			return err
		}
		reloaded, found, err := o.NarrativeStore.Load(ctx, updated.SeriesID)
		if err != nil {
			return err
		}
		if found {
			*bible = reloaded
			return nil
		}
	}

	*bible = updated
	return nil
}

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

// PlanResult is a dry-run projection: the book is parsed and segmented but not
// translated, and per-strategy cost estimates are returned.
type PlanResult struct {
	BookID           string                  `json:"book_id"`
	TotalSegments    int                     `json:"total_segments"`
	TotalSourceChars int                     `json:"total_source_chars"`
	Chapters         int                     `json:"chapters"`
	BatchSize        int                     `json:"batch_size"`
	EscalationRate   float64                 `json:"escalation_rate"`
	Scenarios        []cost.ScenarioEstimate `json:"scenarios"`
}

// Plan runs extract → parse → segment WITHOUT translating, then projects cost
// across NMT-only / hybrid / full-LLM strategies. It only needs the Extractor,
// Parser, Segmenter and CostModel to be set.
//
// It segments by chapter, not just by book, because prompt overhead is charged
// per LLM call and calls are chunked per chapter. That is also what makes this
// projection comparable with the one the run-time guardrail enforces — down to
// reading the same free-tier counter off the same ledger.
func (o *Orchestrator) Plan(
	ctx context.Context, cfg RunConfig, batchSize int, escalationRate float64,
) (*PlanResult, error) {
	raw, err := o.Extractor.Extract(ctx, cfg.InputPath)
	if err != nil {
		return nil, &StageError{Stage: StageExtract, Err: err}
	}
	book, err := o.Parser.Parse(ctx, raw)
	if err != nil {
		return nil, &StageError{Stage: StageParse, Err: err}
	}
	chapters, err := o.segmentBookByChapter(ctx, book)
	if err != nil {
		return nil, &StageError{Stage: StageSegment, Err: err}
	}

	usedFreeChars, err := o.freeCharsUsed(ctx, time.Now().UTC())
	if err != nil {
		return nil, &StageError{Stage: StageSegment, Err: err}
	}

	grouped := make([][]segment.Segment, len(chapters))
	for i := range chapters {
		grouped[i] = chapters[i].Segments
	}
	segments := flattenChapters(chapters)

	return &PlanResult{
		BookID:           cfg.BookID,
		TotalSegments:    len(segments),
		TotalSourceChars: segment.SourceChars(segments),
		Chapters:         len(chapters),
		BatchSize:        batchSize,
		EscalationRate:   escalationRate,
		Scenarios:        o.CostModel.EstimateScenarios(grouped, batchSize, escalationRate, usedFreeChars),
	}, nil
}

func (o *Orchestrator) segmentBookByChapter(ctx context.Context, book *epub.Book) ([]chapterSegments, error) {
	var chapters []chapterSegments
	chapterIdx := 0

	for _, item := range book.Spine {
		manifestItem, ok := book.Manifest.Items[item.IDRef]
		if !ok {
			continue
		}
		if manifestItem.MediaType != xhtmlMediaType {
			continue
		}
		if manifestItem.IsNav() {
			continue
		}

		doc, ok := book.Contents[manifestItem.Href]
		if !ok || doc == nil {
			continue
		}

		chapterIdx++
		chapterID := fmt.Sprintf("ch_%04d", chapterIdx)

		segs, err := o.Segmenter.Segment(ctx, doc, chapterID)
		if err != nil {
			return nil, fmt.Errorf("unable to segment %q, err: %w", manifestItem.Href, err)
		}
		chapters = append(chapters, chapterSegments{
			ID:       chapterID,
			Href:     manifestItem.Href,
			Segments: segs,
		})
	}

	return chapters, nil
}

// translateSegments resolves each segment via the TM cache first and the NMT
// provider otherwise, then restores placeholders into the live node tree. It
// accumulates the consumption counters onto result as it goes, so a failure
// partway through still leaves an accurate record of what was paid for.
// Pricing belongs to the mode, not to the translation loop.
func (o *Orchestrator) translateSegments(
	ctx context.Context, cfg RunConfig, log *zap.Logger,
	segments []segment.Segment, result *RunResult,
) error {
	pm := o.Placeholders
	if pm == nil {
		pm = segment.InlinePlaceholderManager{}
	}

	var qualitySum float64
	qualityCount := 0
	for i := range segments {
		if err := ctx.Err(); err != nil {
			return err
		}

		seg := &segments[i]

		target, reused, err := o.resolveTranslation(ctx, cfg, seg)
		if err != nil {
			return err
		}

		seg.TargetText = target
		if reused {
			seg.Status = segment.StatusReused
			result.TMHits++
		} else {
			seg.Status = segment.StatusTranslated
			result.NMTSegments++
			result.NMTChars += utf8.RuneCountInString(seg.SourceText)

			// Reference-free quality estimation (e.g. COMET-QE) on the NMT draft,
			// used both to drive escalation and to report per-book quality.
			var qeScore float64
			haveQE := false
			if o.QualityEstimator != nil {
				if s, err := o.QualityEstimator.Estimate(ctx, seg.SourceText, target); err == nil {
					qeScore, haveQE = s, true
					qualitySum += s
					qualityCount++
				} else {
					log.Warn("quality estimation failed", zap.String("segment_id", seg.ID), zap.Error(err))
				}
			}

			// Selective LLM post-edit of fresh NMT drafts. SafePostEdit falls
			// back to the NMT draft on any error or placeholder change, so the
			// output can never be degraded by escalation.
			if o.shouldEscalate(seg, qeScore, haveQE) {
				edited, postEdited := llm.SafePostEdit(ctx, o.PostEditor, llm.PostEditRequest{
					SourceText: seg.SourceText,
					NMTText:    target,
					SourceLang: cfg.SourceLang,
					TargetLang: cfg.TargetLang,
				})
				if postEdited {
					target = edited
					seg.TargetText = target
					seg.Status = segment.StatusPostEdited
					result.LLMSegments++
					result.LLMChars += utf8.RuneCountInString(seg.SourceText)
					if o.TM != nil {
						if err := o.TM.Save(ctx, tm.Entry{
							SourceHash: seg.Hash, SourceText: seg.SourceText, TargetText: target,
							SourceLang: cfg.SourceLang, TargetLang: cfg.TargetLang, Engine: tm.EngineLLMPostEdit,
						}); err != nil {
							log.Warn("unable to persist post-edited TM entry",
								zap.String("segment_id", seg.ID), zap.Error(err))
						}
					}
				}
			}
		}

		if o.Glossary != nil {
			result.GlossaryViolations += o.Glossary.Violations(seg.SourceText, target)
		}

		if err := pm.Restore(target, seg.Placeholders, seg.Node); err != nil {
			// Restore validates every issued token before it touches the node,
			// so a failure genuinely leaves the original (source) markup in
			// place; QA flags the dropped placeholder. Do not abort the run.
			log.Warn("placeholder restore failed",
				zap.String("segment_id", seg.ID), zap.Error(err))
		}
	}

	if qualityCount > 0 {
		result.AvgQualityScore = qualitySum / float64(qualityCount)
		result.QualityScoredSegments = qualityCount
	}
	return nil
}

// shouldEscalate decides whether a freshly translated segment is sent to the
// LLM post-editor. The heuristic (source length threshold) is a configurable
// proxy for "complex/high-value" and is intentionally simple: precise
// confidence-based routing is deferred until a labeled benchmark exists.
func (o *Orchestrator) shouldEscalate(seg *segment.Segment, qeScore float64, haveQE bool) bool {
	if o.PostEditor == nil {
		return false
	}
	// Quality-driven: escalate the weakest drafts when QE is available.
	if haveQE && o.EscalateBelowScore > 0 {
		return qeScore < o.EscalateBelowScore
	}
	// Fallback heuristic: escalate by source length.
	if o.EscalateMinChars <= 0 {
		return true
	}
	return utf8.RuneCountInString(seg.SourceText) >= o.EscalateMinChars
}

// lookupTranslation fills seg.TargetText from an exact TM hit and reports
// whether one was found. Unlike resolveTranslation it never calls the provider
// and never writes, which is what lets the literary path batch its misses.
//
// A raw draft is not a hit here. Literary mode stopped persisting drafts, but
// databases written by earlier runs still hold them, and serving one would skip
// refinement for that segment permanently — the exact defect the change was
// made to remove. Such an entry is treated as a miss: the segment is re-drafted
// and re-refined, and the finished translation replaces it.
func (o *Orchestrator) lookupTranslation(ctx context.Context, seg *segment.Segment) (bool, error) {
	if o.TM == nil {
		return false, nil
	}

	entry, found, err := o.TM.Lookup(ctx, seg.Hash)
	if err != nil {
		return false, fmt.Errorf("unable to look up TM for %s, err: %w", seg.ID, err)
	}
	if !found || tm.IsDraftEngine(entry.Engine) {
		return false, nil
	}

	seg.TargetText = entry.TargetText
	return true, nil
}

// resolveTranslation returns the target text for a segment, preferring an exact
// TM hit and falling back to the NMT provider (persisting the new translation).
//
// Unlike lookupTranslation it accepts a draft as a hit, and saves one. That is
// correct here and only here: in NMT mode the draft is the final output for a
// segment that is not escalated, so refusing it would throw away all TM reuse.
func (o *Orchestrator) resolveTranslation(ctx context.Context, cfg RunConfig, seg *segment.Segment) (string, bool, error) {
	if o.TM != nil {
		entry, found, err := o.TM.Lookup(ctx, seg.Hash)
		if err != nil {
			return "", false, fmt.Errorf("unable to look up TM for %s, err: %w", seg.ID, err)
		}
		if found {
			return entry.TargetText, true, nil
		}
	}

	// Protect locked glossary terms so the provider cannot alter them, then
	// substitute the canonical target term back after translation.
	sendText := seg.SourceText
	var subs map[string]string
	if o.Glossary != nil {
		sendText, subs = o.Glossary.Protect(seg.SourceText)
	}

	resp, err := o.Translator.Translate(ctx, translate.Request{
		Text:       sendText,
		SourceLang: cfg.SourceLang,
		TargetLang: cfg.TargetLang,
	})
	if err != nil {
		return "", false, fmt.Errorf("unable to translate %s, err: %w", seg.ID, err)
	}

	target := resp.Text
	if o.Glossary != nil {
		target = o.Glossary.Apply(target, subs)
	}

	if o.TM != nil {
		if err := o.TM.Save(ctx, tm.Entry{
			SourceHash: seg.Hash,
			SourceText: seg.SourceText,
			TargetText: target,
			SourceLang: cfg.SourceLang,
			TargetLang: cfg.TargetLang,
			Engine:     resp.Engine,
		}); err != nil {
			return "", false, fmt.Errorf("unable to persist TM for %s, err: %w", seg.ID, err)
		}
	}

	return target, false, nil
}

// placeholders returns the configured placeholder manager, defaulting to the
// inline one.
func (o *Orchestrator) placeholders() segment.PlaceholderManager {
	if o.Placeholders != nil {
		return o.Placeholders
	}
	return segment.InlinePlaceholderManager{}
}

func (o *Orchestrator) logger() *zap.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return zap.NewNop()
}

// countSegments totals a book's segments without materialising the flat slice.
func countSegments(chapters []chapterSegments) int {
	total := 0
	for i := range chapters {
		total += len(chapters[i].Segments)
	}
	return total
}

func flattenChapters(chapters []chapterSegments) []segment.Segment {
	var all []segment.Segment
	for _, chapter := range chapters {
		all = append(all, chapter.Segments...)
	}
	return all
}

func adjacentChapterText(chapters []chapterSegments, idx int) string {
	if idx < 0 || idx >= len(chapters) {
		return ""
	}
	return chapterSourceText(chapters[idx].Segments)
}

func chapterSourceText(segments []segment.Segment) string {
	var sb strings.Builder
	for _, seg := range segments {
		if strings.TrimSpace(seg.SourceText) == "" {
			continue
		}
		sb.WriteString(seg.SourceText)
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}

func chapterTargetText(segments []segment.Segment) string {
	var sb strings.Builder
	for _, seg := range segments {
		if strings.TrimSpace(seg.TargetText) == "" {
			continue
		}
		sb.WriteString(seg.TargetText)
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}

func runMode(cfg RunConfig) string {
	if cfg.Mode == "" {
		return ModeNMT
	}
	return cfg.Mode
}
