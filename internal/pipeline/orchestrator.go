package pipeline

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/ECecillo/ebooks-translator/internal/cost"
	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/qa"
	"github.com/ECecillo/ebooks-translator/internal/segment"
	"github.com/ECecillo/ebooks-translator/internal/tm"
	"github.com/ECecillo/ebooks-translator/internal/translate"
)

const xhtmlMediaType = "application/xhtml+xml"

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
	EscalateMinChars   int                // escalate NMT segments with >= this many source chars (0 = all, when PostEditor set)
	QualityEstimator   QualityEstimator   // optional; reference-free QE for escalation + reporting
	EscalateBelowScore float64            // escalate fresh segments whose QE score < this (0 disables)
	CostModel          cost.Model         // zero-value = free local provider
	BudgetUSD          float64            // per-book hard cap; <= 0 disables the check
	DailyUSD           float64            // daily spend cap; <= 0 disables the check
	MonthlyUSD         float64            // monthly spend cap; <= 0 disables the check
	Logger             *zap.Logger
}

// Run executes the full pipeline and always attempts to emit run artifacts. It
// returns a *StageError on stage failure and a wrapped ErrQAGateFailed when the
// output is produced but fails QA.
func (o *Orchestrator) Run(ctx context.Context, cfg RunConfig) (*RunResult, error) {
	log := o.logger().With(
		zap.String("run_id", cfg.RunID),
		zap.String("book_id", cfg.BookID),
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

	segments, err := o.segmentBook(ctx, book)
	if err != nil {
		return nil, &StageError{Stage: StageSegment, Err: err}
	}
	log.Info("segmented book", zap.Int("segments", len(segments)))

	result := &RunResult{
		SchemaVersion: 1,
		RunID:         cfg.RunID,
		BookID:        cfg.BookID,
		StartedAt:     started,
		TotalSegments: len(segments),
	}

	// Pre-run budget guardrail: project worst-case (no-reuse) spend and abort
	// before any provider calls if it exceeds the per-book cap.
	projection := o.CostModel.Project(segments)
	result.EstimatedCostUSD = projection.EstimatedUSD
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

	if err := o.translateSegments(ctx, cfg, log, segments, result); err != nil {
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

	if result.TotalSegments > 0 {
		result.TMHitRatio = float64(result.TMHits) / float64(result.TotalSegments)
		result.LLMEscalationRatio = float64(result.LLMSegments) / float64(result.TotalSegments)
	}
	if savings := result.EstimatedCostUSD - result.ActualCostUSD; savings > 0 {
		result.CostSavingsUSD = savings
	}
	result.EndedAt = time.Now().UTC()

	// Record realized spend for rolling-budget accounting (best effort: a
	// ledger write failure should not fail an otherwise successful run).
	if o.TM != nil {
		if err := o.TM.RecordRun(ctx, tm.RunSpend{
			RunID:         cfg.RunID,
			BookID:        cfg.BookID,
			StartedAt:     result.StartedAt,
			EndedAt:       result.EndedAt,
			ActualCostUSD: result.ActualCostUSD,
		}); err != nil {
			log.Warn("unable to record run spend", zap.Error(err))
		}
	}

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
		zap.String("output", outputEPUB),
	)
	return result, nil
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
	EscalationRate   float64                 `json:"escalation_rate"`
	Scenarios        []cost.ScenarioEstimate `json:"scenarios"`
}

// Plan runs extract → parse → segment WITHOUT translating, then projects cost
// across NMT-only / hybrid / full-LLM strategies. It only needs the Extractor,
// Parser, Segmenter and CostModel to be set.
func (o *Orchestrator) Plan(ctx context.Context, cfg RunConfig, rates cost.TokenRates, escalationRate float64) (*PlanResult, error) {
	raw, err := o.Extractor.Extract(ctx, cfg.InputPath)
	if err != nil {
		return nil, &StageError{Stage: StageExtract, Err: err}
	}
	book, err := o.Parser.Parse(ctx, raw)
	if err != nil {
		return nil, &StageError{Stage: StageParse, Err: err}
	}
	segments, err := o.segmentBook(ctx, book)
	if err != nil {
		return nil, &StageError{Stage: StageSegment, Err: err}
	}

	chars := 0
	for i := range segments {
		chars += utf8.RuneCountInString(segments[i].SourceText)
	}

	return &PlanResult{
		BookID:           cfg.BookID,
		TotalSegments:    len(segments),
		TotalSourceChars: chars,
		EscalationRate:   escalationRate,
		Scenarios:        o.CostModel.EstimateScenarios(segments, rates, escalationRate),
	}, nil
}

// segmentBook segments every content document in spine order, assigning stable
// chapter IDs.
func (o *Orchestrator) segmentBook(ctx context.Context, book *epub.Book) ([]segment.Segment, error) {
	var all []segment.Segment
	chapterIdx := 0

	for _, item := range book.Spine {
		manifestItem, ok := book.Manifest.Items[item.IDRef]
		if !ok {
			continue
		}
		if manifestItem.MediaType != xhtmlMediaType {
			continue
		}
		if isNav(manifestItem) {
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
		all = append(all, segs...)
	}

	return all, nil
}

// translateSegments resolves each segment via the TM cache first and the NMT
// provider otherwise, then restores placeholders into the live node tree.
func (o *Orchestrator) translateSegments(
	ctx context.Context, cfg RunConfig, log *zap.Logger,
	segments []segment.Segment, result *RunResult,
) error {
	pm := o.Placeholders
	if pm == nil {
		pm = segment.InlinePlaceholderManager{}
	}

	nmtChars := 0
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
			nmtChars += utf8.RuneCountInString(seg.SourceText)

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
					if o.TM != nil {
						if err := o.TM.Save(ctx, tm.Entry{
							SourceHash: seg.Hash, SourceText: seg.SourceText, TargetText: target,
							SourceLang: cfg.SourceLang, TargetLang: cfg.TargetLang, Engine: "llm-postedit",
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
			// A restore failure leaves the original (source) markup in place;
			// QA will flag the dropped placeholder. Do not abort the whole run.
			log.Warn("placeholder restore failed",
				zap.String("segment_id", seg.ID), zap.Error(err))
		}
	}

	result.ActualCostUSD = o.CostModel.Actual(result.NMTSegments, nmtChars, result.LLMSegments)
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

// resolveTranslation returns the target text for a segment, preferring an exact
// TM hit and falling back to the NMT provider (persisting the new translation).
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

func (o *Orchestrator) logger() *zap.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return zap.NewNop()
}

func isNav(item epub.ManifestItem) bool {
	for _, p := range item.Properties {
		if p == "nav" {
			return true
		}
	}
	return false
}
