package pipeline

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/ECecillo/tomeshift/internal/cost"
	"github.com/ECecillo/tomeshift/internal/literary"
	"github.com/ECecillo/tomeshift/internal/narrative"
	"github.com/ECecillo/tomeshift/internal/segment"
	"github.com/ECecillo/tomeshift/internal/tm"
	"github.com/ECecillo/tomeshift/internal/translate"
)

// costFn prices what the run has consumed at the instant it is called. It is
// built by Run, where the meter baseline lives, and handed to the translation
// loops so a watcher can be told what the book has cost so far. It is pure and
// cheap: arithmetic over the counters plus one read of the adapter's meter.
type costFn func() float64

// translationMode is the only part of a run that differs between the NMT and
// literary pipelines. Everything around it — extract, parse, segment, the
// budget guardrail, recompose, QA, the spend ledger, artifacts and the QA gate
// — is identical for both and lives in Orchestrator.Run.
type translationMode interface {
	// Init stamps the mode's own fields onto a fresh result.
	Init(result *RunResult)
	// Project estimates the worst-case spend of the chapters handed to it,
	// assuming none of their segments turns out to be reusable. The caller has
	// already dropped what the TM can serve, so "worst case" is about this run's
	// remaining work rather than about the whole book. It runs before any
	// provider call.
	Project(chapters []chapterSegments, usedFreeChars int) cost.Projection
	// Translate fills in the chapters' target text, restores placeholders into
	// the node tree and accumulates the consumption counters on result as it
	// goes. It returns the translated segments in spine order.
	Translate(
		ctx context.Context, log *zap.Logger,
		chapters []chapterSegments, usedFreeChars int, result *RunResult, price costFn,
	) ([]segment.Segment, error)
	// Cost prices what result says has been consumed so far. It is pure and
	// callable at any point, including after a failure, because a run that died
	// halfway still has to answer for what it already spent.
	Cost(result *RunResult, usedFreeChars int) float64
	// ReusesDrafts reports whether a raw NMT draft already in the TM is a
	// usable hit for this mode. It is the one thing the two modes disagree on,
	// and the projection has to know: a book whose drafts are all cached is free
	// to finish in NMT mode and a full bill in literary mode.
	ReusesDrafts() bool
}

// newTranslationMode selects the pipeline for cfg. Literary mode loads the
// series bible here because both the projection (whose prefix it sizes) and the
// translation need it.
func (o *Orchestrator) newTranslationMode(ctx context.Context, cfg RunConfig) (translationMode, error) {
	if runMode(cfg) != ModeLiterary {
		return &nmtMode{orch: o, cfg: cfg}, nil
	}

	bible, err := o.loadBible(ctx, cfg.SeriesID)
	if err != nil {
		return nil, err
	}
	return &literaryMode{orch: o, cfg: cfg, bible: bible}, nil
}

// nmtMode drafts every segment with the NMT provider and optionally escalates
// the weakest ones to an LLM post-editor. The draft is the final output for a
// segment that is not escalated, which is why this mode does persist it.
type nmtMode struct {
	orch *Orchestrator
	cfg  RunConfig
}

func (m *nmtMode) Init(result *RunResult) { result.Mode = ModeNMT }

// ReusesDrafts is true here: an unescalated draft IS this mode's output, which
// is why it is also persisted.
func (m *nmtMode) ReusesDrafts() bool { return true }

// postEditPass describes an escalation pass over segments totalling chars. The
// HTTP post-editor sends one segment per call and carries neither the bible nor
// the chapter source, so the stable prompt overhead is paid once per segment —
// the opposite of the batched literary pass, and the reason escalation is
// expensive per word. cost.EstimateScenarios builds the hybrid scenario the
// same way, which is what makes `estimate` and this run agree.
func postEditPass(segments, chars int) cost.LLMPass {
	return cost.LLMPass{Segments: segments, Chars: chars, Calls: segments, Uncached: true}
}

func (m *nmtMode) Project(chapters []chapterSegments, usedFreeChars int) cost.Projection {
	segments := flattenChapters(chapters)

	// Worst case with a post-editor attached, every segment clears the
	// escalation threshold; without one there is no LLM stage at all.
	var pass cost.LLMPass
	if m.orch.PostEditor != nil {
		pass = postEditPass(len(segments), segment.SourceChars(segments))
	}
	return m.orch.CostModel.Project(segments, pass, usedFreeChars)
}

// Cost prices the draft plus the escalation. The HTTP post-editor reports no
// token usage, so the escalation is priced from its shape through the same
// model that projected it: counting the escalated segments but not their cost
// would make the guardrail lie by exactly the amount the LLM costs.
func (m *nmtMode) Cost(result *RunResult, usedFreeChars int) float64 {
	_, _, llmUSD := m.orch.CostModel.Tokens.Project(postEditPass(result.LLMAttempts, result.LLMChars))
	return m.orch.CostModel.Actual(
		result.NMTSegments, result.NMTChars, usedFreeChars, cost.TokenUsage{}) + llmUSD
}

func (m *nmtMode) Translate(
	ctx context.Context, log *zap.Logger,
	chapters []chapterSegments, usedFreeChars int, result *RunResult, price costFn,
) ([]segment.Segment, error) {
	// This walks the flattened copies, not chapters, so chapters keeps its
	// source text. That is harmless — and only harmless — because Segment.Node
	// is a pointer into the live tree, so the placeholder restore lands in the
	// document either way, and Run recomposes from the returned slice. Anything
	// added after Translate must read the return value, not chapters.
	segments := flattenChapters(chapters)
	if err := m.orch.translateSegments(ctx, m.cfg, log, segments, result, price); err != nil {
		return nil, err
	}
	return segments, nil
}

// literaryMode drafts a chapter's TM misses in one batched provider call, then
// rewrites every fresh segment with the LLM against the series bible. The draft
// is an intermediate here, never the output.
type literaryMode struct {
	orch  *Orchestrator
	cfg   RunConfig
	bible narrative.Bible
}

// ReusesDrafts is false here: serving a draft would skip refinement for that
// segment permanently, which is the defect this mode exists to prevent.
func (m *literaryMode) ReusesDrafts() bool { return false }

func (m *literaryMode) Init(result *RunResult) {
	result.Mode = ModeLiterary
	result.SeriesID = m.cfg.SeriesID
	result.BibleVersion = m.bible.Version
}

func (m *literaryMode) Project(chapters []chapterSegments, usedFreeChars int) cost.Projection {
	segments := flattenChapters(chapters)
	return m.orch.CostModel.Project(segments, m.pass(chapters, segments), usedFreeChars)
}

// pass describes the LLM pass the run is about to make so the guardrail prices
// the calls it will actually issue. Chunking is per chapter, and every call
// repeats the stable prefix, so the shape of the chapters — not just the
// segment count — decides the bill.
func (m *literaryMode) pass(chapters []chapterSegments, segments []segment.Segment) cost.LLMPass {
	batchSize, prefixChars := 0, 0
	if m.orch.LiteraryTranslator != nil {
		batchSize = m.orch.LiteraryTranslator.EffectiveBatchSize()
		prefixChars = m.orch.LiteraryTranslator.StablePrefixChars(m.bible)
	}

	// Per chapter, not averaged: each one writes its own source into the
	// cacheable prefix and reads it back on every further call, and the long
	// chapters have more calls to read it in.
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

// Cost prices the draft plus the LLM pass from the tokens the provider actually
// reported.
func (m *literaryMode) Cost(result *RunResult, usedFreeChars int) float64 {
	return m.orch.CostModel.Actual(result.NMTSegments, result.NMTChars, usedFreeChars, cost.TokenUsage{
		InputTokens:         result.LLMInputTokens,
		OutputTokens:        result.LLMOutputTokens,
		CacheReadTokens:     result.LLMCacheReadTokens,
		CacheCreationTokens: result.LLMCacheCreationTokens,
	})
}

func (m *literaryMode) Translate(
	ctx context.Context, log *zap.Logger,
	chapters []chapterSegments, usedFreeChars int, result *RunResult, price costFn,
) ([]segment.Segment, error) {
	o := m.orch
	if o.LiteraryTranslator == nil {
		return nil, literary.ErrNilChatClient
	}

	// A chapter is one TranslateChapter call, so without this hook both the
	// progress and the spend would freeze for the whole of it. Wired here rather
	// than by the CLI because this is now the run's ONLY accumulation path for
	// LLM tokens: refine must not add Outcome.Usage on top, or every chapter
	// would be billed twice.
	//
	// A batch is reported whether it succeeded or degraded, so the counters hold
	// what was consumed and not what was usable — which is what the ledger and
	// the budget guardrail need.
	o.LiteraryTranslator.OnBatch = func(segments int, usage literary.Usage) {
		result.LLMPassSegments += segments
		result.LLMInputTokens += usage.InputTokens
		result.LLMOutputTokens += usage.OutputTokens
		result.LLMCacheReadTokens += usage.CacheReadTokens
		result.LLMCacheCreationTokens += usage.CacheCreationTokens
		o.progress().Advance(segments, 0)
		o.progress().Cost(price())
	}

	translated := make([]segment.Segment, 0, result.TotalSegments)
	for i := range chapters {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		o.progress().Chapter(i + 1)
		chapter := &chapters[i]

		// Resolve the chapter against the TM first, then draft everything it
		// missed in one batched provider call. A TM hit here is always a
		// finished translation: literary mode never serves or persists a draft.
		var fresh []*segment.Segment
		for j := range chapter.Segments {
			seg := &chapter.Segments[j]
			reused, err := o.lookupTranslation(ctx, seg)
			if err != nil {
				return nil, err
			}
			if reused {
				seg.Status = segment.StatusReused
				result.TMHits++
				o.progress().Advance(0, 1)
				continue
			}
			fresh = append(fresh, seg)
		}

		if err := o.draftSegments(ctx, m.cfg, chapter.ID, fresh); err != nil {
			return nil, err
		}
		for _, seg := range fresh {
			seg.Status = segment.StatusTranslated
			result.NMTSegments++
		}

		if err := m.refine(ctx, log, chapters, i, fresh, result); err != nil {
			return nil, err
		}

		for j := range chapter.Segments {
			seg := &chapter.Segments[j]
			if o.Glossary != nil {
				result.GlossaryViolations += o.Glossary.Violations(seg.SourceText, seg.TargetText)
			}
			// Restore is atomic: on failure the node keeps its original source
			// markup, so the segment degrades to untranslated instead of losing
			// content. QA counts it; the chapter carries on — and the
			// translation is not persisted, so it cannot come back as a hit.
			if err := o.placeholders().Restore(seg.TargetText, seg.Placeholders, seg.Node); err != nil {
				log.Warn("placeholder restore failed",
					zap.String("segment_id", seg.ID), zap.Error(err))
				continue
			}
			m.persist(ctx, log, seg)
		}

		// Frozen by default: keep the bible (and thus the system prefix / prompt
		// cache) stable across chapters and re-runs. Evolve it only when the run
		// explicitly opts in.
		if m.cfg.UpdateBible {
			if err := o.updateBible(ctx, m.cfg.SeriesID, &m.bible, chapter); err != nil {
				return nil, err
			}
			result.BibleVersion = m.bible.Version
		}
		translated = append(translated, chapter.Segments...)
	}

	return translated, nil
}

// refine rewrites a chapter's freshly-drafted segments with the LLM and
// persists the results. Only a finished translation reaches the TM.
func (m *literaryMode) refine(
	ctx context.Context, log *zap.Logger,
	chapters []chapterSegments, idx int, fresh []*segment.Segment, result *RunResult,
) error {
	if len(fresh) == 0 {
		return nil
	}
	chapter := &chapters[idx]

	// TranslateChapter rewrites the segments in place. The chapter's whole
	// source goes in the cacheable prefix; only the tail of the previous
	// chapter still has to travel per call.
	//
	// Its Outcome.Usage is deliberately ignored: the OnBatch hook wired in
	// Translate already accumulated the same tokens, batch by batch, and it did
	// so before this function was reached — so a chapter where every batch
	// failed has still been counted, which is what the deferred spend recorder
	// needs. Adding it here as well would double every chapter's bill.
	outcome, err := m.orch.LiteraryTranslator.TranslateChapter(
		ctx, m.bible, chapter.ID, fresh,
		adjacentChapterText(chapters, idx-1), chapterSourceText(chapter.Segments))
	if err != nil {
		return err
	}

	// A batch that failed while others succeeded leaves its segments on their
	// draft and does not fail the chapter. Without this the named refusal and
	// truncation errors would be collected and then dropped, which is the same
	// silence they were introduced to end.
	for _, degraded := range outcome.Degraded {
		log.Warn("literary batch kept its draft",
			zap.String("chapter_id", chapter.ID), zap.Error(degraded))
	}

	for _, seg := range fresh {
		if seg.Status == segment.StatusPostEdited {
			result.LLMSegments++
		}
	}
	return nil
}

// persist writes a refined segment to the TM. It is called only after Restore
// has accepted the translation.
//
// The ordering is the point. The literary precheck compares the *set* of
// placeholder ids, so it cannot see a reordering; Restore can. Saving before
// restoring would write a translation that rebuilds into wrong markup as a
// finished "literary" entry, and the next run would serve it as a TM hit and
// skip refinement — the shadowing defect this mode exists to prevent, arriving
// through the back door.
func (m *literaryMode) persist(ctx context.Context, log *zap.Logger, seg *segment.Segment) {
	if m.orch.TM == nil || seg.Status != segment.StatusPostEdited {
		return
	}
	if err := m.orch.TM.Save(ctx, tm.Entry{
		SourceHash: seg.Hash,
		SourceText: seg.SourceText,
		TargetText: seg.TargetText,
		SourceLang: m.cfg.SourceLang,
		TargetLang: m.cfg.TargetLang,
		Engine:     tm.EngineLiterary,
	}); err != nil {
		log.Warn("unable to persist literary TM entry",
			zap.String("segment_id", seg.ID), zap.Error(err))
	}
}

// draftSegments drafts segments in a single batched provider call, writing each
// draft onto its segment.
//
// Locked glossary terms are protected per segment before the call and
// substituted back after it, exactly as the per-segment path does — batching
// changes the transport, not the glossary contract.
//
// The drafts are deliberately not persisted. In literary mode the draft is an
// intermediate the LLM is about to rewrite; saving it would make the next run
// serve it as a TM hit, which short-circuits refinement for that segment
// forever.
func (o *Orchestrator) draftSegments(ctx context.Context, cfg RunConfig, chapterID string, segments []*segment.Segment) error {
	if len(segments) == 0 {
		return nil
	}

	texts := make([]string, len(segments))
	subs := make([]map[string]string, len(segments))
	for i, seg := range segments {
		texts[i] = seg.SourceText
		if o.Glossary != nil {
			texts[i], subs[i] = o.Glossary.Protect(seg.SourceText)
		}
	}

	resps, err := o.Translator.TranslateBatch(ctx, translate.BatchRequest{
		Texts:      texts,
		SourceLang: cfg.SourceLang,
		TargetLang: cfg.TargetLang,
	})
	if err != nil {
		return fmt.Errorf("unable to draft chapter %s, err: %w", chapterID, err)
	}
	if len(resps) != len(texts) {
		return fmt.Errorf("unable to draft chapter %s: got %d drafts for %d segments, err: %w",
			chapterID, len(resps), len(texts), translate.ErrShortBatchResponse)
	}

	for i, resp := range resps {
		text := resp.Text
		if o.Glossary != nil {
			text = o.Glossary.Apply(text, subs[i])
		}
		segments[i].TargetText = text
	}
	return nil
}
