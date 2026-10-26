package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/ECecillo/tomeshift/internal/cost"
	"github.com/ECecillo/tomeshift/internal/epub"
	"github.com/ECecillo/tomeshift/internal/glossary"
	"github.com/ECecillo/tomeshift/internal/literary"
	"github.com/ECecillo/tomeshift/internal/llm"
	"github.com/ECecillo/tomeshift/internal/narrative"
	"github.com/ECecillo/tomeshift/internal/qa"
	"github.com/ECecillo/tomeshift/internal/segment"
	"github.com/ECecillo/tomeshift/internal/tm"
	"github.com/ECecillo/tomeshift/internal/translate"
)

// ledgerWriteTimeout bounds the detached write used when a run is recorded
// after its context was cancelled.
const ledgerWriteTimeout = 5 * time.Second

// Stages holds the injected collaborators the run needs and exposes one method
// per activity in the RFC table. Each method takes a real context.Context, reads
// and writes the workspace, and returns a small serialisable value — it is the
// activity body a Temporal worker will register unchanged. The control flow
// (RunBook) never touches these fields; it reaches them only through the Steps
// port.
type Stages struct {
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
	// LLMProvider and LLMModel name what the LLM pass talks to. The pipeline
	// does no routing with them — the clients are already built — it records
	// them, because a measured token count is only reusable if you know what
	// produced it.
	LLMProvider string
	LLMModel    string
}

// chapterSegments is a chapter's segments in spine order — the in-memory shape
// the stage bodies rebuild from the workspace or fresh segmentation.
type chapterSegments struct {
	ID       string
	Href     string
	Segments []segment.Segment
}

// ---------------------------------------------------------------------------
// Activity bodies.
// ---------------------------------------------------------------------------

// PrepareBook extracts, parses and segments the book, writes one unit file per
// chapter to the workspace, and returns the chapter manifest plus the run-scoped
// identifiers the flow stamps onto the result but cannot learn otherwise.
func (s *Stages) PrepareBook(ctx context.Context, in BookInput) (Manifest, error) {
	raw, err := s.Extractor.Extract(ctx, in.InputPath)
	if err != nil {
		return Manifest{}, &StageError{Stage: StageExtract, Err: err}
	}
	book, err := s.Parser.Parse(ctx, raw)
	if err != nil {
		return Manifest{}, &StageError{Stage: StageParse, Err: err}
	}
	chapters, err := s.segmentBookByChapter(ctx, book)
	if err != nil {
		return Manifest{}, &StageError{Stage: StageSegment, Err: err}
	}

	ws := NewWorkspace(in.OutputDir)
	infos := make([]ChapterInfo, 0, len(chapters))
	for i := range chapters {
		ch := &chapters[i]
		units := make([]segment.Unit, len(ch.Segments))
		for j := range ch.Segments {
			units[j] = ch.Segments[j].Unit()
		}
		if err := ws.WriteChapter(ch.ID, units); err != nil {
			return Manifest{}, &StageError{Stage: StageSegment, Err: err}
		}
		infos = append(infos, ChapterInfo{
			ID:       ch.ID,
			Href:     ch.Href,
			Segments: len(ch.Segments),
			Chars:    segment.SourceChars(ch.Segments),
		})
	}

	s.logger().Info("segmented book",
		zap.Int("segments", countChapterInfos(infos)), zap.Int("chapters", len(infos)))

	return Manifest{
		Chapters:    infos,
		NMTEngine:   s.nmtEngine(),
		LLMProvider: s.LLMProvider,
		LLMModel:    s.LLMModel,
		BatchSize:   s.literaryBatchSize(),
	}, nil
}

// ProjectCost is the pre-run budget guardrail: it prices the pending work and
// the whole book with no reuse, and refuses the run before any provider call if
// a per-book or rolling cap is exceeded. NoReuseUSD is returned even on the
// refusal so the flow can still stamp the report's estimated cost.
func (s *Stages) ProjectCost(ctx context.Context, in ProjectInput) (Projection, error) {
	chapters, err := s.readChapters(in.Ref)
	if err != nil {
		return Projection{}, &StageError{Stage: StageTranslate, Err: err}
	}

	now := time.Now().UTC()
	usedFreeChars, err := s.freeCharsUsed(ctx, now)
	if err != nil {
		return Projection{}, &StageError{Stage: StageTranslate, Err: err}
	}

	var bible narrative.Bible
	if in.Mode == ModeLiterary {
		bible, err = s.loadBible(ctx, in.SeriesID)
		if err != nil {
			return Projection{}, &StageError{Stage: StageTranslate, Err: err}
		}
	}

	reusesDrafts := in.Mode != ModeLiterary
	pending, reusable, err := s.pendingChapters(ctx, chapters, reusesDrafts)
	if err != nil {
		return Projection{}, &StageError{Stage: StageTranslate, Err: err}
	}

	enforced := s.projectMode(in.Mode, pending, bible, usedFreeChars)
	noReuse := s.projectMode(in.Mode, chapters, bible, usedFreeChars)

	proj := Projection{
		EnforcedUSD:   enforced.EstimatedUSD,
		NoReuseUSD:    noReuse.EstimatedUSD,
		Pending:       countSegments(pending),
		Reusable:      reusable,
		UsedFreeChars: usedFreeChars,
		BibleVersion:  bible.Version,
	}

	if reusable > 0 {
		s.logger().Info("projecting the remaining work",
			zap.Int("reusable_segments", reusable),
			zap.Int("pending_segments", proj.Pending))
	}
	s.logger().Info("projected run cost",
		zap.Float64("enforced_usd", enforced.EstimatedUSD),
		zap.Float64("no_reuse_usd", noReuse.EstimatedUSD),
		zap.Int("llm_calls", enforced.LLMCalls))

	if s.BudgetUSD > 0 && enforced.EstimatedUSD > s.BudgetUSD {
		s.logger().Warn("projected spend exceeds per-book budget cap",
			zap.Float64("estimated_usd", enforced.EstimatedUSD),
			zap.Float64("budget_usd", s.BudgetUSD))
		return proj, fmt.Errorf("unable to start run (estimated $%.4f > per-book cap $%.4f), err: %w",
			enforced.EstimatedUSD, s.BudgetUSD, cost.ErrBudgetExceeded)
	}
	if err := s.checkRollingBudgets(ctx, enforced.EstimatedUSD, now); err != nil {
		return proj, err
	}
	return proj, nil
}

// ResolveChapter serves a chapter's segments from the TM, writing every hit's
// target onto its unit and returning the reuse split. A draft is a hit only when
// the mode treats a draft as output (ReusesDrafts).
func (s *Stages) ResolveChapter(ctx context.Context, in ResolveInput) (ChapterPlan, error) {
	ws := NewWorkspace(in.Ref.OutputDir)
	units, err := ws.ReadChapter(in.ChapterID)
	if err != nil {
		return ChapterPlan{}, &StageError{Stage: StageTranslate, Err: err}
	}

	plan := ChapterPlan{}
	for i := range units {
		u := &units[i]
		hit := false
		if s.TM != nil {
			entry, found, err := s.TM.Lookup(ctx, u.Hash)
			if err != nil {
				return ChapterPlan{}, &StageError{Stage: StageTranslate,
					Err: fmt.Errorf("unable to look up TM for %s, err: %w", u.ID, err)}
			}
			if found && (in.ReusesDrafts || !tm.IsDraftEngine(entry.Engine)) {
				u.TargetText = entry.TargetText
				u.Status = segment.StatusReused
				hit = true
			}
		}
		if hit {
			plan.Reused++
		} else {
			plan.Fresh = append(plan.Fresh, u.ID)
		}
	}

	if err := ws.WriteChapter(in.ChapterID, units); err != nil {
		return ChapterPlan{}, &StageError{Stage: StageTranslate, Err: err}
	}
	return plan, nil
}

// DraftChapter drafts a chapter's TM misses in one batched provider call,
// protecting glossary terms around it, and persists the drafts only when the
// mode treats them as output. It returns the source characters it actually sent
// (retries included) so the flow can total NMT consumption.
func (s *Stages) DraftChapter(ctx context.Context, in DraftInput) (DraftResult, error) {
	if len(in.Fresh) == 0 {
		return DraftResult{Engine: s.nmtEngine()}, nil
	}

	ws := NewWorkspace(in.Ref.OutputDir)
	units, err := ws.ReadChapter(in.ChapterID)
	if err != nil {
		return DraftResult{}, &StageError{Stage: StageTranslate, Err: err}
	}

	fresh := selectUnits(units, in.Fresh)
	texts := make([]string, len(fresh))
	subs := make([]map[string]string, len(fresh))
	for i, u := range fresh {
		texts[i] = u.SourceText
		if s.Glossary != nil {
			texts[i], subs[i] = s.Glossary.Protect(u.SourceText)
		}
	}

	before := s.charsSent()
	resps, err := s.Translator.TranslateBatch(ctx, translate.BatchRequest{
		Texts:      texts,
		SourceLang: in.SourceLang,
		TargetLang: in.TargetLang,
	})
	if err != nil {
		return DraftResult{}, &StageError{Stage: StageTranslate,
			Err: fmt.Errorf("unable to draft chapter %s, err: %w", in.ChapterID, err)}
	}
	if len(resps) != len(texts) {
		return DraftResult{}, &StageError{Stage: StageTranslate,
			Err: fmt.Errorf("unable to draft chapter %s: got %d drafts for %d segments, err: %w",
				in.ChapterID, len(resps), len(texts), translate.ErrShortBatchResponse)}
	}

	engine := s.nmtEngine()
	for i, resp := range resps {
		text := resp.Text
		if s.Glossary != nil {
			text = s.Glossary.Apply(text, subs[i])
		}
		fresh[i].TargetText = text
		fresh[i].Status = segment.StatusTranslated
		if resp.Engine != "" {
			engine = resp.Engine
		}
		if in.PersistDrafts && s.TM != nil {
			if err := s.TM.Save(ctx, tm.Entry{
				SourceHash: fresh[i].Hash, SourceText: fresh[i].SourceText, TargetText: text,
				SourceLang: in.SourceLang, TargetLang: in.TargetLang, Engine: resp.Engine,
			}); err != nil {
				return DraftResult{}, &StageError{Stage: StageTranslate,
					Err: fmt.Errorf("unable to persist TM for %s, err: %w", fresh[i].ID, err)}
			}
		}
	}
	chars := s.charsSent() - before

	if err := ws.WriteChapter(in.ChapterID, units); err != nil {
		return DraftResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	return DraftResult{Chars: chars, Engine: engine}, nil
}

// EscalateChapter runs the NMT-mode quality estimation over a chapter's fresh
// drafts and selectively post-edits the weakest with the LLM. SafePostEdit falls
// back to the draft on any error or placeholder change, so the output can never
// be degraded by escalation.
func (s *Stages) EscalateChapter(ctx context.Context, in EscalateInput) (EscalateResult, error) {
	if len(in.Fresh) == 0 || (s.PostEditor == nil && s.QualityEstimator == nil) {
		return EscalateResult{}, nil
	}

	ws := NewWorkspace(in.Ref.OutputDir)
	units, err := ws.ReadChapter(in.ChapterID)
	if err != nil {
		return EscalateResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	fresh := selectUnits(units, in.Fresh)

	var res EscalateResult
	for _, u := range fresh {
		target := u.TargetText

		var qeScore float64
		haveQE := false
		if s.QualityEstimator != nil {
			if score, err := s.QualityEstimator.Estimate(ctx, u.SourceText, target); err == nil {
				qeScore, haveQE = score, true
				res.QESum += score
				res.QECount++
			} else {
				s.logger().Warn("quality estimation failed", zap.String("segment_id", u.ID), zap.Error(err))
			}
		}

		if !s.shouldEscalate(u.SourceText, qeScore, haveQE) {
			continue
		}

		edited, postEdited := llm.SafePostEdit(ctx, s.PostEditor, llm.PostEditRequest{
			SourceText: u.SourceText,
			NMTText:    target,
			SourceLang: in.SourceLang,
			TargetLang: in.TargetLang,
		})
		res.Attempts++
		res.Chars += utf8.RuneCountInString(u.SourceText)

		if postEdited {
			u.TargetText = edited
			u.Status = segment.StatusPostEdited
			res.Improved++
			if s.TM != nil {
				if err := s.TM.Save(ctx, tm.Entry{
					SourceHash: u.Hash, SourceText: u.SourceText, TargetText: edited,
					SourceLang: in.SourceLang, TargetLang: in.TargetLang, Engine: tm.EngineLLMPostEdit,
				}); err != nil {
					s.logger().Warn("unable to persist post-edited TM entry",
						zap.String("segment_id", u.ID), zap.Error(err))
				}
			}
		}
	}

	if err := ws.WriteChapter(in.ChapterID, units); err != nil {
		return EscalateResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	return res, nil
}

// RefineBatch rewrites one literary batch with the LLM against the series bible,
// halving on truncation inside the single call. It writes the refined targets
// onto their units and returns the tokens consumed, how many it improved, the
// ids it left on their draft, and the French tail fed to the next batch.
func (s *Stages) RefineBatch(ctx context.Context, in RefineInput) (RefineResult, error) {
	if s.LiteraryTranslator == nil {
		return RefineResult{}, &StageError{Stage: StageTranslate, Err: literary.ErrNilChatClient}
	}
	if len(in.IDs) == 0 {
		return RefineResult{}, nil
	}

	bible, err := s.loadBible(ctx, in.SeriesID)
	if err != nil {
		return RefineResult{}, &StageError{Stage: StageTranslate, Err: err}
	}

	ws := NewWorkspace(in.Ref.OutputDir)
	units, err := ws.ReadChapter(in.ChapterID)
	if err != nil {
		return RefineResult{}, &StageError{Stage: StageTranslate, Err: err}
	}

	// A chapter's first batch has no in-chapter predecessor, so its preceding
	// context is the tail of the PREVIOUS chapter's source — read from the
	// workspace, exactly as the in-process walk did. Later batches carry the
	// previous batch's translated tail in PrevTail.
	prevCtx := in.PrevTail
	if prevCtx == "" && in.PrevChapterID != "" {
		prevUnits, err := ws.ReadChapter(in.PrevChapterID)
		if err != nil {
			return RefineResult{}, &StageError{Stage: StageTranslate, Err: err}
		}
		prevCtx = chapterSourceOfUnits(prevUnits)
	}

	batch := selectUnits(units, in.IDs)
	segs := make([]*segment.Segment, len(batch))
	for i, u := range batch {
		seg := unitToSegment(*u)
		segs[i] = &seg
	}

	// The chapter's whole source is the cacheable prefix; only the preceding
	// tail travels per call.
	outcome, err := s.LiteraryTranslator.TranslateBatch(
		ctx, bible, in.ChapterID, segs, prevCtx, chapterSourceOfUnits(units))
	if err != nil {
		// A rate limit or a transport/credential failure is fatal — the flow
		// fails the run and the units keep their drafts, unwritten.
		return RefineResult{Usage: outcome.Usage, Tail: outcome.Tail},
			&StageError{Stage: StageTranslate, Err: err}
	}

	res := refineFrom(batch, segs)
	res.Usage = outcome.Usage
	res.Tail = outcome.Tail
	for _, d := range outcome.Degraded {
		res.Degraded = append(res.Degraded, d.Error())
		s.logger().Warn("literary batch kept its draft",
			zap.String("chapter_id", in.ChapterID), zap.Error(d))
	}

	if err := ws.WriteChapter(in.ChapterID, units); err != nil {
		return RefineResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	return res, nil
}

// CommitChapter re-derives the chapter's placeholders from the source document,
// validates each translation by restoring it (a refused restore never aborts —
// it lands in RestoreFailures and the node keeps its source), counts glossary
// violations, and persists accepted translations to the TM only when the mode
// asks for it (literary: after Restore accepted).
func (s *Stages) CommitChapter(ctx context.Context, in CommitInput) (CommitResult, error) {
	ws := NewWorkspace(in.Ref.OutputDir)
	units, err := ws.ReadChapter(in.ChapterID)
	if err != nil {
		return CommitResult{}, &StageError{Stage: StageTranslate, Err: err}
	}

	var res CommitResult
	targets := make(map[string]string, len(units))
	for i := range units {
		u := &units[i]
		if u.TargetText != "" {
			// Normalize to the project's single French typography before any
			// consumer reads the target: glossary-violation counting, TM save,
			// segments.jsonl persistence (via WriteChapter) and the EPUB
			// restore then all agree on the same spacing. It runs after the
			// glossary Apply that produced this target, so locked terms are
			// already in place, and it is idempotent.
			u.TargetText = segment.FrenchTypography(u.TargetText)
			targets[u.ID] = u.TargetText
		}
		if s.Glossary != nil {
			res.GlossaryViolations += s.Glossary.Violations(u.SourceText, u.TargetText)
		}
	}

	doc, err := s.chapterDoc(ctx, in.Ref.InputPath, in.ChapterID)
	if err != nil {
		return CommitResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	report, err := segment.ApplyTranslations(ctx, s.Segmenter, s.placeholders(), doc, in.ChapterID, targets)
	if err != nil {
		return CommitResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	res.RestoreFailures = len(report.Failed)

	if in.Persist && s.TM != nil {
		for i := range units {
			u := &units[i]
			if u.Status != segment.StatusPostEdited {
				continue
			}
			if _, failed := report.Failed[u.ID]; failed {
				continue
			}
			if err := s.TM.Save(ctx, tm.Entry{
				SourceHash: u.Hash, SourceText: u.SourceText, TargetText: u.TargetText,
				SourceLang: in.SourceLang, TargetLang: in.TargetLang, Engine: tm.EngineLiterary,
			}); err != nil {
				s.logger().Warn("unable to persist literary TM entry",
					zap.String("segment_id", u.ID), zap.Error(err))
			}
		}
	}

	if err := ws.WriteChapter(in.ChapterID, units); err != nil {
		return CommitResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	return res, nil
}

// UpdateBible evolves the series bible from a committed chapter and returns the
// stored version.
func (s *Stages) UpdateBible(ctx context.Context, in BibleInput) (BibleResult, error) {
	if s.NarrativeProfiler == nil {
		bible, err := s.loadBible(ctx, in.SeriesID)
		if err != nil {
			return BibleResult{}, &StageError{Stage: StageTranslate, Err: err}
		}
		return BibleResult{Version: bible.Version}, nil
	}

	bible, err := s.loadBible(ctx, in.SeriesID)
	if err != nil {
		return BibleResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	if err := s.updateBible(ctx, in.Ref, in.SeriesID, &bible, in.ChapterID); err != nil {
		return BibleResult{}, &StageError{Stage: StageTranslate, Err: err}
	}
	return BibleResult{Version: bible.Version}, nil
}

// RecomposeBook re-parses the source, applies every chapter's committed targets
// to a fresh node tree and renders the translated EPUB.
func (s *Stages) RecomposeBook(ctx context.Context, ref RunRef) (RecomposeResult, error) {
	raw, err := s.Extractor.Extract(ctx, ref.InputPath)
	if err != nil {
		return RecomposeResult{}, &StageError{Stage: StageRecompose, Err: err}
	}
	book, err := s.Parser.Parse(ctx, raw)
	if err != nil {
		return RecomposeResult{}, &StageError{Stage: StageRecompose, Err: err}
	}

	ws := NewWorkspace(ref.OutputDir)
	for _, ch := range book.Chapters() {
		units, err := ws.ReadChapter(ch.ID)
		if err != nil {
			return RecomposeResult{}, &StageError{Stage: StageRecompose, Err: err}
		}
		targets := make(map[string]string, len(units))
		for i := range units {
			if units[i].TargetText != "" {
				// Normalize here too so recomposing a run committed before this
				// pass existed still emits the canonical French typography.
				// Idempotent, so re-normalizing freshly committed units is a
				// no-op.
				targets[units[i].ID] = segment.FrenchTypography(units[i].TargetText)
			}
		}
		if _, err := segment.ApplyTranslations(ctx, s.Segmenter, s.placeholders(), ch.Doc, ch.ID, targets); err != nil {
			return RecomposeResult{}, &StageError{Stage: StageRecompose, Err: err}
		}
	}

	outputEPUB := fmt.Sprintf("%s/translated.epub", ref.OutputDir)
	if err := s.Recomposer.Recompose(ctx, raw, book, outputEPUB); err != nil {
		return RecomposeResult{}, &StageError{Stage: StageRecompose, Err: err}
	}
	return RecomposeResult{OutputEPUB: outputEPUB}, nil
}

// CheckEPUB runs epubcheck on the produced EPUB. A skipped check (binary absent)
// does not block the run.
func (s *Stages) CheckEPUB(ctx context.Context, in CheckInput) (CheckResult, error) {
	check, err := s.EPUBCheck.Check(ctx, in.OutputEPUB)
	if err != nil {
		return CheckResult{}, &StageError{Stage: StageQA, Err: err}
	}
	return CheckResult{Passed: check.Passed, Skipped: check.Skipped}, nil
}

// FinishBook evaluates the static QA checks over the committed units and writes
// report.json, metrics.json and segments.jsonl. It re-segments the source to
// recover each segment's placeholders and node path, then overlays the target
// text and status the run produced. The EPUB-check verdict is carried in the
// result the flow passes in.
func (s *Stages) FinishBook(ctx context.Context, in FinishInput) (qa.Report, error) {
	if in.Ref.InputPath != "" && ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
		defer cancel()
	}

	raw, err := s.Extractor.Extract(ctx, in.Ref.InputPath)
	if err != nil {
		return qa.Report{}, &StageError{Stage: StageQA, Err: err}
	}
	book, err := s.Parser.Parse(ctx, raw)
	if err != nil {
		return qa.Report{}, &StageError{Stage: StageQA, Err: err}
	}

	ws := NewWorkspace(in.Ref.OutputDir)
	var segments []segment.Segment
	for _, ch := range book.Chapters() {
		segs, err := s.Segmenter.Segment(ctx, ch.Doc, ch.ID)
		if err != nil {
			return qa.Report{}, &StageError{Stage: StageQA, Err: err}
		}
		units, err := ws.ReadChapter(ch.ID)
		if err != nil {
			return qa.Report{}, &StageError{Stage: StageQA, Err: err}
		}
		byID := make(map[string]segment.Unit, len(units))
		for i := range units {
			byID[units[i].ID] = units[i]
		}
		for i := range segs {
			if u, ok := byID[segs[i].ID]; ok {
				segs[i].TargetText = u.TargetText
				segs[i].Status = u.Status
			}
		}
		segments = append(segments, segs...)
	}

	report := qa.Evaluate(book, segments)
	report.EPUBCheckPassed = in.Result.QA.EPUBCheckPassed
	report.EPUBCheckSkipped = in.Result.QA.EPUBCheckSkipped

	result := in.Result
	result.QA = report
	if err := WriteArtifacts(in.Ref, &result, segments); err != nil {
		return qa.Report{}, &StageError{Stage: StageQA, Err: err}
	}
	return report, nil
}

// RecordSpend persists what the run consumed, for rolling-budget and free-tier
// accounting. It fires on failure paths too — the provider does not refund a run
// that died after it was called — and detaches from a cancelled context so the
// record survives. Best effort: a ledger write failure never fails the run.
func (s *Stages) RecordSpend(ctx context.Context, in SpendInput) error {
	if s.TM == nil {
		return nil
	}

	result := in.Result
	if result.EndedAt.IsZero() {
		result.EndedAt = time.Now().UTC()
	}

	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
		defer cancel()
	}

	if err := s.TM.RecordRun(ctx, tm.RunSpend{
		RunID:                  in.Ref.RunID,
		BookID:                 in.Ref.BookID,
		StartedAt:              result.StartedAt,
		EndedAt:                result.EndedAt,
		ActualCostUSD:          result.ActualCostUSD,
		NMTEngine:              result.NMTEngine,
		NMTChars:               result.NMTChars,
		LLMProvider:            result.LLMProvider,
		LLMModel:               result.LLMModel,
		LLMSegments:            result.LLMPassSegments,
		LLMInputTokens:         result.LLMInputTokens,
		LLMOutputTokens:        result.LLMOutputTokens,
		LLMCacheReadTokens:     result.LLMCacheReadTokens,
		LLMCacheCreationTokens: result.LLMCacheCreationTokens,
	}); err != nil {
		s.logger().Warn("unable to record run spend", zap.Error(err))
	}
	return nil
}

// PriceRun prices what the result says has been consumed so far through the
// mode's cost model. It is pure arithmetic over the counters, callable at any
// point including after a failure.
func (s *Stages) PriceRun(_ context.Context, in PriceInput) (float64, error) {
	if in.Mode == ModeLiterary {
		return s.costLiterary(in.Result, in.UsedFreeChars), nil
	}
	return s.costNMT(in.Result, in.UsedFreeChars), nil
}

// ---------------------------------------------------------------------------
// Shared helpers.
// ---------------------------------------------------------------------------

// nmtEngine identifies the provider whose allowance a run consumes. It is empty
// when no translator is configured, which is the case for a dry run.
func (s *Stages) nmtEngine() string {
	if s.Translator == nil {
		return ""
	}
	return s.Translator.Engine()
}

// charsSent reads the adapter's running total of characters actually put into
// provider requests, or 0 from one that does not meter itself. Only the adapter
// knows what really went out: retried attempts, glossary-protected text whose
// length differs from the source, and discarded responses.
func (s *Stages) charsSent() int {
	meter, ok := s.Translator.(translate.Meter)
	if !ok {
		return 0
	}
	return meter.CharsSent()
}

func (s *Stages) placeholders() segment.PlaceholderManager {
	if s.Placeholders != nil {
		return s.Placeholders
	}
	return segment.InlinePlaceholderManager{}
}

func (s *Stages) logger() *zap.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return zap.NewNop()
}

// literaryBatchSize is the batch size the flow chunks fresh literary segments
// by; 0 outside literary mode.
func (s *Stages) literaryBatchSize() int {
	if s.LiteraryTranslator == nil {
		return 0
	}
	return s.LiteraryTranslator.EffectiveBatchSize()
}

// shouldEscalate decides whether a freshly translated segment is sent to the LLM
// post-editor. The heuristic (source length threshold) is a configurable proxy
// for "complex/high-value" and is intentionally simple.
func (s *Stages) shouldEscalate(source string, qeScore float64, haveQE bool) bool {
	if s.PostEditor == nil {
		return false
	}
	if haveQE && s.EscalateBelowScore > 0 {
		return qeScore < s.EscalateBelowScore
	}
	if s.EscalateMinChars <= 0 {
		return true
	}
	return utf8.RuneCountInString(source) >= s.EscalateMinChars
}

func (s *Stages) segmentBookByChapter(ctx context.Context, book *epub.Book) ([]chapterSegments, error) {
	bookChapters := book.Chapters()
	chapters := make([]chapterSegments, 0, len(bookChapters))
	for _, chapter := range bookChapters {
		segs, err := s.Segmenter.Segment(ctx, chapter.Doc, chapter.ID)
		if err != nil {
			return nil, fmt.Errorf("unable to segment %q, err: %w", chapter.Href, err)
		}
		chapters = append(chapters, chapterSegments{ID: chapter.ID, Href: chapter.Href, Segments: segs})
	}
	return chapters, nil
}

// readChapters rebuilds the workspace's chapters as minimal segments (source
// text and hash), enough for the projection and the reuse lookup, without
// re-parsing the source.
func (s *Stages) readChapters(ref RunRef) ([]chapterSegments, error) {
	ws := NewWorkspace(ref.OutputDir)
	infos, err := s.chapterOrder(ref)
	if err != nil {
		return nil, err
	}
	out := make([]chapterSegments, 0, len(infos))
	for _, info := range infos {
		units, err := ws.ReadChapter(info.ID)
		if err != nil {
			return nil, err
		}
		segs := make([]segment.Segment, len(units))
		for i := range units {
			segs[i] = unitToSegment(units[i])
		}
		out = append(out, chapterSegments{ID: info.ID, Href: info.Href, Segments: segs})
	}
	return out, nil
}

// chapterOrder recovers the spine order of the chapters written to the
// workspace by re-parsing the source. ProjectCost needs the order because the
// literary projection prices per chapter.
func (s *Stages) chapterOrder(ref RunRef) ([]ChapterInfo, error) {
	raw, err := s.Extractor.Extract(context.Background(), ref.InputPath)
	if err != nil {
		return nil, err
	}
	book, err := s.Parser.Parse(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	chapters := book.Chapters()
	infos := make([]ChapterInfo, 0, len(chapters))
	for _, ch := range chapters {
		infos = append(infos, ChapterInfo{ID: ch.ID, Href: ch.Href})
	}
	return infos, nil
}

// chapterDoc re-parses the source and returns the named chapter's document, so
// a stage can re-derive that chapter's placeholders and node tree.
func (s *Stages) chapterDoc(ctx context.Context, inputPath, chapterID string) (*epub.XHTMLDocument, error) {
	raw, err := s.Extractor.Extract(ctx, inputPath)
	if err != nil {
		return nil, err
	}
	book, err := s.Parser.Parse(ctx, raw)
	if err != nil {
		return nil, err
	}
	for _, ch := range book.Chapters() {
		if ch.ID == chapterID {
			return ch.Doc, nil
		}
	}
	return nil, fmt.Errorf("unable to find chapter %s in %q, err: %w", chapterID, inputPath, epub.ErrMissingOPF)
}

// freeCharsUsed reports how much of this provider's monthly free character
// allowance the calendar month has already consumed, so the projection bills
// only the overflow. It fails closed.
func (s *Stages) freeCharsUsed(ctx context.Context, now time.Time) (int, error) {
	if s.TM == nil || s.CostModel.FreeCharsPerMonth <= 0 {
		return 0, nil
	}
	since := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	used, err := s.TM.CharsSince(ctx, since, s.nmtEngine())
	if err != nil {
		return 0, fmt.Errorf("unable to read this month's NMT characters, err: %w", err)
	}
	return used, nil
}

// checkRollingBudgets enforces daily and monthly spend caps against the ledger
// plus this run's projected cost. It fails closed.
func (s *Stages) checkRollingBudgets(ctx context.Context, projected float64, now time.Time) error {
	if s.TM == nil {
		return nil
	}
	windows := []struct {
		name  string
		cap   float64
		since time.Time
	}{
		{"daily", s.DailyUSD, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)},
		{"monthly", s.MonthlyUSD, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, w := range windows {
		if w.cap <= 0 {
			continue
		}
		spent, err := s.TM.SpendSince(ctx, w.since)
		if err != nil {
			return fmt.Errorf("unable to read %s spend, err: %w", w.name, err)
		}
		if spent+projected > w.cap {
			s.logger().Warn("projected spend exceeds rolling budget cap",
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

func (s *Stages) loadBible(ctx context.Context, seriesID string) (narrative.Bible, error) {
	if seriesID == "" {
		return narrative.Bible{}, narrative.ErrEmptySeriesID
	}
	if s.NarrativeStore == nil {
		return narrative.Bible{}, fmt.Errorf("unable to load required narrative bible %q, err: %w", seriesID, narrative.ErrMissingBible)
	}
	bible, found, err := s.NarrativeStore.Load(ctx, seriesID)
	if err != nil {
		return narrative.Bible{}, err
	}
	if !found {
		return narrative.Bible{}, fmt.Errorf("unable to load required narrative bible %q, err: %w", seriesID, narrative.ErrMissingBible)
	}
	if bible.SeriesID != seriesID {
		return narrative.Bible{}, fmt.Errorf("unable to validate narrative bible %q, err: %w", seriesID, narrative.ErrInvalidBible)
	}
	if err := narrative.ValidateBible(bible); err != nil {
		return narrative.Bible{}, fmt.Errorf("unable to validate narrative bible %q, err: %w", seriesID, err)
	}
	return bible, nil
}

// updateBible re-runs the profiler over a committed chapter and stores the new
// bible, normalising the model's version to the loaded baseline so the store
// stays the single assigner of versions.
func (s *Stages) updateBible(ctx context.Context, ref RunRef, seriesID string, bible *narrative.Bible, chapterID string) error {
	source, target, err := s.chapterText(ref, chapterID)
	if err != nil {
		return err
	}

	updated, err := s.NarrativeProfiler.Update(ctx, *bible, chapterID, source, target)
	if err != nil {
		return err
	}
	updated.SeriesID = seriesID
	updated.Summaries = append([]narrative.Summary(nil), bible.Summaries...)
	updated.Version = bible.Version

	if s.NarrativeStore != nil {
		for {
			saved, err := s.NarrativeStore.SaveIfVersion(ctx, updated)
			if err != nil {
				return err
			}
			reloaded, found, err := s.NarrativeStore.Load(ctx, seriesID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("unable to reload updated narrative bible %q, err: %w", seriesID, narrative.ErrMissingBible)
			}
			if saved {
				*bible = reloaded
				return nil
			}
			updated.Version = reloaded.Version
			updated.Summaries = append([]narrative.Summary(nil), reloaded.Summaries...)
		}
	}
	*bible = updated
	return nil
}

// chapterText returns a chapter's source and target prose from the workspace,
// for the bible profiler.
func (s *Stages) chapterText(ref RunRef, chapterID string) (source, target string, err error) {
	ws := NewWorkspace(ref.OutputDir)
	units, err := ws.ReadChapter(chapterID)
	if err != nil {
		return "", "", err
	}
	var src, tgt strings.Builder
	for i := range units {
		if strings.TrimSpace(units[i].SourceText) != "" {
			src.WriteString(units[i].SourceText)
			src.WriteByte('\n')
		}
		if strings.TrimSpace(units[i].TargetText) != "" {
			tgt.WriteString(units[i].TargetText)
			tgt.WriteByte('\n')
		}
	}
	return strings.TrimSpace(src.String()), strings.TrimSpace(tgt.String()), nil
}

// ---------------------------------------------------------------------------
// Cost projection and pricing (per mode).
// ---------------------------------------------------------------------------

func (s *Stages) projectMode(mode string, chapters []chapterSegments, bible narrative.Bible, usedFreeChars int) cost.Projection {
	if mode == ModeLiterary {
		segments := flattenChapters(chapters)
		return s.CostModel.Project(segments, s.literaryPass(chapters, segments, bible), usedFreeChars)
	}
	segments := flattenChapters(chapters)
	var pass cost.LLMPass
	if s.PostEditor != nil {
		pass = postEditPass(len(segments), segment.SourceChars(segments))
	}
	return s.CostModel.Project(segments, pass, usedFreeChars)
}

func (s *Stages) literaryPass(chapters []chapterSegments, segments []segment.Segment, bible narrative.Bible) cost.LLMPass {
	batchSize, prefixChars := 0, 0
	if s.LiteraryTranslator != nil {
		batchSize = s.LiteraryTranslator.EffectiveBatchSize()
		prefixChars = s.LiteraryTranslator.StablePrefixChars(bible)
	}
	counts := make([]int, len(chapters))
	shapes := make([]cost.ChapterShape, len(chapters))
	for i := range chapters {
		counts[i] = len(chapters[i].Segments)
		shapes[i] = cost.ChapterShape{
			SourceChars: segment.SourceChars(chapters[i].Segments),
			Calls:       cost.CallsForChapters(counts[i:i+1], batchSize),
		}
	}
	return cost.LLMPass{
		Segments:     len(segments),
		Chars:        segment.SourceChars(segments),
		Calls:        cost.CallsForChapters(counts, batchSize),
		PrefixChars:  prefixChars,
		Chapters:     shapes,
		ContextRunes: cost.LiteraryContextRunes,
	}
}

func (s *Stages) costNMT(result RunResult, usedFreeChars int) float64 {
	_, _, llmUSD := s.CostModel.Tokens.Project(postEditPass(result.LLMAttempts, result.LLMChars))
	return s.CostModel.Actual(result.NMTSegments, result.NMTChars, usedFreeChars, cost.TokenUsage{}) + llmUSD
}

func (s *Stages) costLiterary(result RunResult, usedFreeChars int) float64 {
	return s.CostModel.Actual(result.NMTSegments, result.NMTChars, usedFreeChars, cost.TokenUsage{
		InputTokens:         result.LLMInputTokens,
		OutputTokens:        result.LLMOutputTokens,
		CacheReadTokens:     result.LLMCacheReadTokens,
		CacheCreationTokens: result.LLMCacheCreationTokens,
	})
}

// postEditPass describes an escalation pass over segments totalling chars. The
// HTTP post-editor sends one segment per call, so the stable prompt overhead is
// paid once per segment.
func postEditPass(segments, chars int) cost.LLMPass {
	return cost.LLMPass{Segments: segments, Chars: chars, Calls: segments, Uncached: true}
}

// pendingChapters keeps only the segments this run will pay for — the ones the
// TM cannot serve — and reports how many it dropped. A draft is reusable only
// for the mode that treats a draft as output.
func (s *Stages) pendingChapters(ctx context.Context, chapters []chapterSegments, reusesDrafts bool) ([]chapterSegments, int, error) {
	if s.TM == nil {
		return chapters, 0, nil
	}
	pending := make([]chapterSegments, 0, len(chapters))
	reusable := 0
	for _, chapter := range chapters {
		kept := make([]segment.Segment, 0, len(chapter.Segments))
		for _, seg := range chapter.Segments {
			entry, found, err := s.TM.Lookup(ctx, seg.Hash)
			if err != nil {
				return nil, 0, fmt.Errorf("unable to look up TM for %s, err: %w", seg.ID, err)
			}
			if found && (reusesDrafts || !tm.IsDraftEngine(entry.Engine)) {
				reusable++
				continue
			}
			kept = append(kept, seg)
		}
		if len(kept) == 0 {
			continue
		}
		chapter.Segments = kept
		pending = append(pending, chapter)
	}
	return pending, reusable, nil
}

// ---------------------------------------------------------------------------
// Small pure helpers.
// ---------------------------------------------------------------------------

func unitToSegment(u segment.Unit) segment.Segment {
	return segment.Segment{
		ID:             u.ID,
		ChapterID:      u.ChapterID,
		NodePath:       u.NodePath,
		SourceText:     u.SourceText,
		NormalizedText: u.NormalizedText,
		Hash:           u.Hash,
		TargetText:     u.TargetText,
		Status:         u.Status,
	}
}

// selectUnits returns pointers to the units whose ids are in ids, in unit order.
func selectUnits(units []segment.Unit, ids []string) []*segment.Unit {
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]*segment.Unit, 0, len(ids))
	for i := range units {
		if _, ok := want[units[i].ID]; ok {
			out = append(out, &units[i])
		}
	}
	return out
}

// refineFrom writes the refined targets back onto the batch units and reports
// how many were improved. The batch's French tail comes from the translator's
// Outcome, not from here, so it reads the raw model answers rather than the
// glossary-applied draft fallback.
func refineFrom(batch []*segment.Unit, segs []*segment.Segment) RefineResult {
	var res RefineResult
	for i, seg := range segs {
		batch[i].TargetText = seg.TargetText
		batch[i].Status = seg.Status
		if seg.Status == segment.StatusPostEdited {
			res.Refined++
		}
	}
	return res
}

func chapterSourceOfUnits(units []segment.Unit) string {
	var sb strings.Builder
	for i := range units {
		if strings.TrimSpace(units[i].SourceText) == "" {
			continue
		}
		sb.WriteString(units[i].SourceText)
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}

func countSegments(chapters []chapterSegments) int {
	total := 0
	for i := range chapters {
		total += len(chapters[i].Segments)
	}
	return total
}

func countChapterInfos(infos []ChapterInfo) int {
	total := 0
	for i := range infos {
		total += infos[i].Segments
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

// Plan runs extract → parse → segment WITHOUT translating, then projects cost
// across NMT-only / hybrid / full-LLM strategies. It only needs the Extractor,
// Parser, Segmenter and CostModel to be set — it makes no provider call and
// needs no server, which is what `estimate` runs on.
//
// It segments by chapter, not just by book, because prompt overhead is charged
// per LLM call and calls are chunked per chapter — down to reading the same
// free-tier counter off the same ledger the run-time guardrail enforces on.
func (s *Stages) Plan(ctx context.Context, in BookInput, batchSize int, escalationRate float64) (*PlanResult, error) {
	raw, err := s.Extractor.Extract(ctx, in.InputPath)
	if err != nil {
		return nil, &StageError{Stage: StageExtract, Err: err}
	}
	book, err := s.Parser.Parse(ctx, raw)
	if err != nil {
		return nil, &StageError{Stage: StageParse, Err: err}
	}
	chapters, err := s.segmentBookByChapter(ctx, book)
	if err != nil {
		return nil, &StageError{Stage: StageSegment, Err: err}
	}

	usedFreeChars, err := s.freeCharsUsed(ctx, time.Now().UTC())
	if err != nil {
		return nil, &StageError{Stage: StageSegment, Err: err}
	}

	// Only finished translations count as reuse here, which is the literary
	// rule rather than the NMT one: `estimate` prices three strategies at once
	// and cannot hold two definitions of "already done". It over-projects the
	// nmt-only row rather than read low.
	pending, reusable, err := s.pendingChapters(ctx, chapters, false)
	if err != nil {
		return nil, &StageError{Stage: StageSegment, Err: err}
	}

	grouped := make([][]segment.Segment, len(pending))
	for i := range pending {
		grouped[i] = pending[i].Segments
	}
	all := flattenChapters(chapters)
	segments := flattenChapters(pending)

	return &PlanResult{
		BookID:            in.BookID,
		TotalSegments:     len(all),
		TotalSourceChars:  segment.SourceChars(all),
		Chapters:          len(chapters),
		ReusableSegments:  reusable,
		PendingSegments:   len(segments),
		PendingSourceChar: segment.SourceChars(segments),
		BatchSize:         batchSize,
		EscalationRate:    escalationRate,
		Scenarios:         s.CostModel.EstimateScenarios(grouped, batchSize, escalationRate, usedFreeChars),
	}, nil
}
