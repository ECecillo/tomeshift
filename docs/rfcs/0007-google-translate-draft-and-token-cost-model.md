# RFC 0007: Google Cloud Translation as the Draft Engine, and a Token-Based Budget Guardrail

- Status: `proposed`
- Authors: ECecillo
- Created: 2026-08-01
- Updated: 2026-08-01
- Related:
  - Architecture: `docs/ARCHITECTURE.md`
  - Milestones: `docs/milestones/M2-cost-optimization.md`, `docs/milestones/M3-quality-optimization.md`
  - Builds on: [0003 - LLM Escalation Strategy](./0003-llm-escalation-strategy.md), [0006 - Faithful Multi-Volume Literary Translation](./0006-faithful-multivolume-translation.md)
  - Cost model reference: `scripts/cost_model.py`
  - Supersedes: n/a
  - Superseded by: n/a

## Context

Literary mode already implements a two-stage pipeline: an NMT engine produces a
draft, then an LLM refines every fresh segment with the series bible and glossary
in context. The refinement stage is mature (RFC 0006, plus the self-healing batch
work in `342f3bd`/`347ba2d`/`de07509`). The draft stage is not: it is served by a
self-hosted LibreTranslate/Argos instance whose output quality is the weakest link
in the chain.

The request that opened this RFC was to draft with Google Translate instead, then
let the LLM correct against the generated dictionary. Investigating it surfaced
five problems that are independent of Google and that a naive provider swap would
either miss or make worse:

1. **A TM hit short-circuits the LLM entirely.** `resolveTranslation` persists raw
   NMT drafts, and `translateLiteraryChapters` skips any segment with a TM hit
   before it can reach the LLM. A draft written by a previous run permanently
   shadows refinement for that segment.
2. **The budget guardrail is calibrated to zero.** `COST_PER_1000_CHARS_USD` and
   `COST_LLM_PER_SEGMENT_USD` both default to `0`, so the pre-flight projection is
   `0.00 $` for every run and the `2.00 $` per-book cap has never had anything to
   compare against.
3. **Two divergent cost models exist.** The runtime guardrail uses flat per-segment
   and per-1000-character rates; the `estimate` command uses a token model. Only
   the latter can express the dominant cost.
4. **`Restore` does not honour the fallback its caller documents.** It mutates the
   node tree before validating placeholders, so a dropped placeholder loses content
   rather than preserving the original markup.
5. **The LLM sees very little context.** Each call carries the bible plus 1200
   runes either side of the batch. Neither the chapter nor the book is available.

Paying Google also introduces a real constraint the pipeline cannot currently
express: the free tier is 500,000 characters per month, shared between Cloud
Translation Basic and Advanced, per project. A volume is ~450,000 characters, so
the strategy is free at one volume per month and only meaningful if the pipeline
can count characters against that quota.

### Measured baseline

All figures come from the repository, not from estimates. Commands are inline in
`docs/rfc-0007-google-translate.html`; the cost model is `scripts/cost_model.py`.

| Quantity | Value | Source |
| --- | --- | --- |
| WN vol. 1 | 4,269 segments / 472,509 chars / 68 spine chapters | `epub-translator estimate` |
| LN vol. 1 (commercial edition) | 2,833 segments / 414,044 chars | `epub-translator estimate` |
| Stable prefix (bible v3 + glossary) | ~10,500 chars ~ 3,000 tokens | `data/lumen-series.sqlite` |
| Median chapter | ~1,020 tokens | EPUB spine walk |
| Whole book | ~119,000 tokens | EPUB spine walk |
| LLM calls at the shipped `defaultBatchSize` (50) | 130 | chunking is per chapter |
| Stable prefix share of LLM input | 49% | `scripts/cost_model.py` |
| Current cost per volume (`claude-sonnet-4-6`) | 4.22 $ | `scripts/cost_model.py` |

The prefix share is the load-bearing number: the count of LLM *calls*, not of
segments, drives the dominant part of input cost. `de07509` already acted on this
by raising `defaultBatchSize` from 10 to 50; this RFC does not revisit it.

## Goals

- Replace the Argos draft with Google Cloud Translation without touching the
  refinement stage, so any quality change is attributable to the draft alone.
- Make a TM hit mean "a finished translation", never "a raw draft".
- Give the budget guardrail something true to enforce: one token-based cost model,
  aware of the prompt cache and of the Google free tier.
- Make `Restore` behave as its caller documents.
- Give the LLM the whole chapter it is correcting, at a cost that stays marginal.
- Choose the refinement model from measurement rather than from price.
- Move refinement onto `claude-sonnet-5` for output quality, with its silent
  defaults neutralised and its true cost visible to the guardrail.

## Non-Goals

- Parallelising chapters during the LLM pass. Chapters are independent
  (`adjacentChapterText` reads only neighbours' source), so this is feasible and is
  the largest remaining latency lever, but it is a separate change.
- Resolving the `--update-bible` tension (an evolving bible means chapter 1 and
  chapter 68 see different specifications, and invalidates the prompt cache per
  chapter). Recorded here, deliberately unaddressed.
- Adopting Cloud Translation's Translation LLM or Adaptive Translation models.
  Priced at 9.70 $ and ~24 $ per volume against 1.41 $ for `Google NMT + Haiku 4.5`,
  and neither accepts the bible or the address rules.
- Fuzzy TM, PostgreSQL migration (RFC 0005), and any change to the QA gate.

## Options Considered

### Draft engine

#### Option A: Cloud Translation v2 (Basic) — recommended

- Pros: API key authentication maps directly onto the existing `NMTEndpoint` /
  `NMTAPIKey` contract; `format=html` preserves `<x id="N"/>` exactly as the
  LibreTranslate adapter already relies on; up to 128 strings per request; same
  20 $/M characters as v3 for NMT.
- Cons: no native glossary, no batch or document translation, no model choice.

#### Option B: Cloud Translation v3 (Advanced)

- Pros: native Google-side glossaries; access to Translation LLM and Adaptive
  Translation; batch and document translation.
- Cons: no API key support — service account and ADC, with credentials to plumb
  through Docker and CI. The native glossary would not replace `Protect`/`Apply`,
  which is still required on the LLM side, so the project would maintain two
  sources of truth for twelve locked terms. Identical NMT price.

#### Option C: DeepL

- Rejected on availability, not on quality. As of July 2026 API Free and API Pro
  are no longer sold to new customers. The Developer plan grants 1M characters in
  total — two volumes, once. Growth is 26 $/month fixed. Google is free at one
  volume per month and only reaches parity with Growth at four volumes per month.

#### Option D: The undocumented `translate_a/single` endpoint

- Rejected. Outside Google's terms, an undocumented contract that can break without
  notice, and IP-blocked at book volume.

### Translation memory

#### Option A: Stop persisting NMT drafts in literary mode — recommended

- Pros: a TM hit becomes, by construction, a finished translation. Removes the
  shadowing defect in a handful of lines.
- Cons: drafts are no longer reused across runs. Worth ~0 $ here — Google is free at
  this volume and `llm_cache.sqlite` already makes LLM re-runs free.
- Note: `resolveTranslation` is shared by both modes. In NMT mode a draft *is* the
  final output for a non-escalated segment, so the rule is mode-scoped, not global.

#### Option B: Engine-aware lookup

- Pros: keeps drafts in the store for traceability while never serving them as
  final.
- Cons: `source_hash` is the primary key, so this is a schema migration, and
  `Lookup` gains a parameter that propagates into the `tm.Store` interface.

#### Option C: A fresh TM file for Google

- Rejected: isolates the Argos history but does not fix shadowing. As soon as a
  Google run writes its own drafts, the same defect reappears in the new file.

### Cost model

#### Option A: Unify on the token model — recommended

- Pros: one source of truth; what `estimate` reports is what the cap enforces. The
  only shape that can express the LLM cost, which is the only cost actually paid.
- Cons: `Project`/`Actual` must learn the token model; `EstimateScenarios` must
  count prompt overhead per *call* rather than per *segment*.

#### Option B: Populate `COST_LLM_PER_SEGMENT_USD`

- Rejected: a single constant cannot track the LLM model, the batch size and the
  cache hit rate simultaneously. It goes stale silently.

## Proposed Decision

Eleven decisions, grouped into four batches. The ordering is load-bearing: the
context change in batch 3 alters the prompt, so it must not ship alongside the
engine change or the two effects become indistinguishable in measurement.

| # | Decision |
| --- | --- |
| 1 | Add `ProviderGoogle` to `internal/translate`, engine tag `nmt-google`, `format=html`, API key auth. |
| 2 | Target Cloud Translation v2 (Basic). |
| 3 | Add `TranslateBatch` to `translate.Translator`, bounded to `min(128 segments, 5000 chars)`; batching unit is a chapter's TM misses. `http` and `libretranslate` implement it as a loop. |
| 4 | Stop persisting raw NMT drafts to the TM **in literary mode**. |
| 5 | Make `Restore` atomic: validate that every issued token is present in the translated text *before* calling `detachChildren`. |
| 6 | Unify the runtime guardrail on the token model; count prompt overhead per call. |
| 7 | Model the Google free tier: record NMT characters in the run ledger, bill only the overflow. |
| 8 | Add `LLM_CACHE_READ_PER_MTOK_USD` (0 = provider does not cache). |
| 9 | Measure the prefix from the loaded bible at run time; keep the config constant for `estimate`, which has no bible. |
| 10 | Put the chapter's full source in the cacheable prefix, replacing the two 1200-rune windows. Keep the previous batch's French translation as preceding context. |
| 11 | Choose the refinement model by measurement on chapter 1 (chrF against `fr-ch1.epub`, COMET-QE, three-column read). |
| 12 | Migrate the refinement model to `claude-sonnet-5`, with `thinking` explicitly disabled, `stop_reason` surfaced, and a per-model token multiplier in the cost model. |

### Decision 12: what the Sonnet 5 migration actually contains

The adapter was audited against the migration guide. Most breaking changes do not
apply, one does, and the expensive part is not a breaking change at all.

**Not affected.** `anthropicRequest` carries no `temperature`, `top_p` or `top_k`,
so the removal of sampling parameters is a non-event. Messages are `system` + `user`
only, so the assistant-prefill rejection does not apply. `output_config.format`
already uses the current shape, not the deprecated `output_format`. Manual extended
thinking (`budget_tokens`) was never used.

**The one change that fires.** The adapter sends no `thinking` field
(`internal/llm/anthropic.go:37-43`). On Sonnet 4.6 that means *no thinking*; on
Sonnet 5 it means *adaptive thinking on*. This is a silent default change, and its
consequences land squarely on this pipeline:

- Thinking tokens bill at the **output** rate (15 $/MTok).
- `max_tokens` is a hard ceiling on thinking **plus** response text. With
  `MaxTokens: 16000` and a 50-segment JSON reply, thinking competes with the
  answer, and a truncated reply is exactly the failure mode `342f3bd` was written
  to survive — the self-healing retry would absorb it, silently, at extra cost.

Mitigation: send `thinking: {"type": "disabled"}` explicitly. Whether adaptive
thinking *improves* literary post-editing is a legitimate question, but it is a
third arm of the decision-11 comparison, not a default to inherit by omission.

**Refusals and truncation are currently invisible.** `anthropicResponse` decodes
`content` and `usage` but not `stop_reason` (`internal/llm/anthropic.go:54-65`).
Sonnet 5 ships elevated cybersecurity safeguards and can return
`stop_reason: "refusal"` on a successful HTTP 200 with empty or partial content.
Combined with the truncation risk above, two distinct failures would both surface
only as "`decodeResponse` failed, retry on halved batches". Adding `stop_reason`
(and `stop_details` when present) makes them diagnosable for a few lines.

**The expensive part is the tokenizer, not the price list.** Sonnet 5 uses the
Opus 4.7-family tokenizer: roughly **30% more tokens for the same text** than
Sonnet 4.6. The sticker price is identical (3 $/15 $), so the effective cost rises
by the same 30%:

| Model | Sticker | Tokens | Per volume, current context | With chapter context |
| --- | --- | --- | --- | --- |
| `claude-sonnet-4-6` (today) | 3 $/15 $ | 1.00× | 4.22 $ | 4.93 $ |
| `claude-sonnet-5`, intro rates | 2 $/10 $ | 1.30× | **3.66 $** | 4.27 $ |
| `claude-sonnet-5`, standard rates | 3 $/15 $ | 1.30× | **5.49 $** | 6.40 $ |

The introductory rate runs to **2026-08-31** — thirty days from this RFC. Until
then the migration is cheaper than the status quo; afterwards it is ~30% dearer for
identical output. That is a deliberate trade for quality, not a saving, and the
per-book cap must be set with the post-August figure in mind.

Consequence for decisions 6 and 9: the character-to-token heuristic becomes
**model-dependent**. A `LLM_TOKEN_MULTIPLIER` (default `1.0`) is added alongside the
rate configuration and applied on top of `charsPerToken`, mirroring
`Rates.token_multiplier` in `scripts/cost_model.py`. Without it, the guardrail
under-projects Sonnet 5 by 30% — and the measured-prefix path of decision 9 would
inherit the same error.

**Behavioural risks to check in the comparison, not to assume.** Sonnet 5 follows
instructions more literally than Sonnet 4.6. The system prompt opens with
`RÈGLES ABSOLUES` in capitals, and `af84a62` already had to soften a rule that was
pushing the model to translate proper nouns against the glossary. More literal
compliance could revive that class of conflict. Response length also calibrates to
task complexity rather than to a fixed verbosity, which for a fixed-schema JSON
reply should be inert but is worth confirming.

### Why decision 10 does not threaten style coherence

Style coherence is not carried by the model remembering earlier chapters. It is
carried by an explicit, frozen specification that is byte-identical in all 130
calls: `GUIDE_DE_STYLE` (which states, verbatim, *"Epithets ... are consistent
across chapters"* and *"keep the same word for recurring concrete nouns across the
volume (e.g. always « survêtement », never « survêt' »)"*), nine address rules,
per-character voices with French samples, and the glossary — the latter enforced
mechanically by `Protect`/`Apply` rather than entrusted to the model.

Full-book context would supply the *English* of earlier chapters, not the *French*
already produced. It therefore cannot answer "did I translate this word the same
way in chapter 1", which is what style coherence means. Supplying accumulated
French output would: ~148k tokens by the end of a volume, changing every chapter so
uncacheable, at 9 $–15 $ per volume, with maximum dilution risk. The glossary and
the lexical-consistency rule do that job for 0 $.

The two concerns are distinct, and only the second is a context problem:

| Concern | Mechanism | Cost |
| --- | --- | --- |
| Style coherence (register, lexicon, epithets, tu/vous) | Frozen bible + glossary — already in place | 0 $ |
| Narrative comprehension (pronoun referents, callbacks) | Chapter source, then summaries, then book source | ~0.7 $ → 4.8 $ |

## Implementation Plan

### Batch 1 — Google end to end (decisions 1, 2, 3, 4, 5)

1. Rebase `google-translate-api` onto `de07509` (`defaultBatchSize` is 50 there,
   not 10).
2. `internal/translate/google.go`: v2 adapter, `format=html`, API key, batch
   request bounded to `min(128, 5000 chars)`, index-aligned response mapping,
   error on a short response array.
3. `internal/translate/types.go`: add `TranslateBatch`; implement as a loop in the
   `http` and `libretranslate` adapters; extend `retry.go` to wrap it.
4. `internal/translate/provider.go`: register `ProviderGoogle`.
5. `internal/pipeline/orchestrator.go`: batch a chapter's TM misses; keep
   `Protect`/`Apply` per segment; drop the draft `TM.Save` on the literary path.
6. `internal/segment/placeholder.go`: pre-flight token validation in `Restore`;
   correct the now-true comment at the call site.
7. Config, `.env.example`, `docs/INSTALLATION.md`, `docs/guide-traduction.md`.
8. **Checkpoint:** run chapter 1, record chrF and COMET-QE. This is the Google-only
   baseline.

### Batch 2 — Cost model and guardrail (decisions 6, 7, 8, 9)

1. `internal/cost`: token-based `Project`; prompt overhead per call; cache-read
   rate; `FreeCharsPerMonth`.
2. `internal/tm`: add NMT characters to `RunSpend`; add `CharsSince`.
3. `internal/pipeline`: pass the measured prefix; apply the free-tier offset.
4. Config plumbing and `.env.example`.

### Batch 3 — Chapter context (decision 10)

1. `internal/literary/prompt.go`: chapter source section in `stablePrefix`; drop
   `CONTEXTE_SUIVANT`.
2. `internal/literary/translator.go`: thread the chapter source; keep
   `precedingContext` for batch chaining.
3. **Checkpoint:** re-run chapter 1, measure the delta against the batch 1 baseline.
   This is the context-only effect.

### Batch 4 — Sonnet 5 migration (decision 12)

1. `internal/llm/anthropic.go`: add `Thinking *anthropicThinking` to the request and
   send `{"type": "disabled"}`; add `StopReason` and `StopDetails` to the response and
   return them through `llm.ChatResponse`.
2. `internal/literary`: treat a non-empty `stop_reason` other than `end_turn` as a
   named failure rather than letting it fall through to `decodeResponse`.
3. `internal/cost` + config: add `LLM_TOKEN_MULTIPLIER` (default `1.0`), applied on
   top of `charsPerToken` in both the guardrail and `estimate`.
4. `.env.example`, `docs/guide-traduction.md`: `claude-sonnet-5` as the documented
   example, with the Sonnet 5 rates and multiplier.
5. **Checkpoint:** re-run chapter 1 and read `usage.output_tokens`. With thinking
   disabled it should track the Sonnet 4.6 baseline within the tokenizer factor; a
   materially higher figure means the disable did not take effect.

### Batch 5 — Model comparison (decision 11)

Run chapter 1 through DeepSeek V4-Flash, Haiku 4.5, Sonnet 4.6 and Sonnet 5. Add a
fifth arm — Sonnet 5 with adaptive thinking *enabled* at `medium` effort — to test
whether thinking helps literary post-editing, since batch 4 disables it by default.
Compare chrF, COMET-QE and a three-column read. Total spend is a few cents.

## Rollout Plan

- Local only. There is no staging or production environment for this pipeline.
- Order: chapter 1 (67 segments, free, seconds) before any full volume.
- Google is opt-in per run via `EPUB_TRANSLATOR_NMT_PROVIDER`; rollback is one
  environment variable back to `libretranslate`.
- Batches 2 and 3 are behind no flag; rollback is `git revert` of the batch.
- The free-tier counter must be seeded honestly: if characters were already spent
  in the current calendar month outside the pipeline, the projection will
  under-estimate for that month only.

## Impact

### Technical

- `translate.Translator` gains a method. Three adapters and `retry.go` change; two
  implement it as a loop.
- Draft latency for WN vol. 1 drops from ~14 min (4,269 sequential calls) to ~30 s
  (~95 batched calls). Total run time is dominated by the LLM pass either way.
- `tm.Store` gains `CharsSince`; `RunSpend` gains a field. SQLite schema addition,
  no migration of existing rows required.
- `Restore` becomes atomic, which removes the silent content loss and makes the
  documented fallback real. This subsumes the separately-considered draft-stage
  placeholder check, which is therefore not implemented.

### Cost

Per volume, WN vol. 1, `lot = 50`, from `scripts/cost_model.py`:

| Stage | Today | After |
| --- | --- | --- |
| Draft | 0 $ (Argos, self-hosted) | 0 $ within the free tier, then ~4 $–6.50 $ |
| LLM, `claude-sonnet-4-6` | 4.22 $ | 4.93 $ with chapter context |
| LLM, `claude-sonnet-5` at intro rates (to 2026-08-31) | — | **4.27 $** |
| LLM, `claude-sonnet-5` at standard rates | — | **6.40 $** |
| LLM if the comparison selects Haiku 4.5 | — | 1.64 $ |
| LLM if the comparison selects DeepSeek V4-Flash | — | 0.15 $ |

Two consequences worth stating plainly. First, the per-book cap of 2.00 $ will start
rejecting runs once the guardrail is honest — that is the intended behaviour, and the
cap must be set deliberately, per model. Second, `claude-sonnet-5` at standard rates
costs **~52% more per volume than the status quo** once chapter context is included
(6.40 $ against 4.22 $): 30% from the tokenizer and the rest from the larger cached
prefix. It is a quality decision, not an economy, and the cap should be sized on the
post-August figure rather than on the introductory one.

### Security/Compliance

- One new outbound dependency. Book text — third-party copyrighted content — is
  sent to Google. This is the same exposure class as the existing Anthropic call,
  but Argos was local, so the change is real and worth stating.
- `EPUB_TRANSLATOR_NMT_API_KEY` already exists and is already excluded from the
  repository; no new secret-handling pattern.
- Cloud Translation requires a GCP project with billing enabled, including for the
  free tier.

## Acceptance Criteria

- `just check` green: `gofmt -w -s .`, `golangci-lint run`, `go test ./...`.
- Chapter 1 run with `NMT_PROVIDER=google` produces `placeholder_restore_ratio = 1`
  and `untranslated_segments = 0`.
- A segment whose TM entry was written by a previous Argos run is re-drafted and
  re-refined rather than reused.
- With a source text mutilated so a placeholder is dropped, `Restore` returns
  `ErrMissingPlaceholder` and the node still holds its original markup.
- `estimate` and the run guardrail report the same projection for the same book,
  model and batch size, within the tokenisation heuristic's margin.
- A projection for a second volume in the same calendar month is non-zero; the
  first is zero.
- Unit tests cover the Google adapter over `httptest`, including a short response
  array and a `<x id="1"></x>` normalised token. No network access in CI.
- The Anthropic request carries `thinking: {"type": "disabled"}`, asserted over
  `httptest` the way `cache_control` already is in `anthropic_test.go`.
- A response with `stop_reason: "refusal"` and an empty `content` array produces a
  named error, not a JSON decode failure followed by a batch split.
- On chapter 1, `claude-sonnet-5` reports `usage.output_tokens` within the tokenizer
  factor of the `claude-sonnet-4-6` baseline. A materially higher figure means
  thinking is still on.
- `estimate` for `claude-sonnet-5` projects ~30% above the same book on
  `claude-sonnet-4-6` at identical sticker rates.

## Risks and Mitigations

- Risk: Google reorders or drops placeholder tokens despite `format=html`.
  - Mitigation: `Restore` is atomic after decision 5, so the segment keeps its
    source markup and QA counts it. `placeholderTokenPattern` already tolerates the
    `<x id="1"></x>` normalisation some providers apply.
- Risk: the free-tier counter drifts from Google's own accounting.
  - Mitigation: the counter is a guardrail, not an invoice. Google's quota page
    remains authoritative; set a billing alert there as well.
- Risk: the chapter source dilutes the correction instead of improving it.
  - Mitigation: batch 3 is measured against the batch 1 baseline on the same
    chapter, and is a single revert.
- Risk: chapter context raises cost more than projected on very long chapters.
  - Mitigation: the largest chapter in WN vol. 1 is 25,737 chars (~6,400 tokens)
    against a ~1,020-token median. Cache writes are per chapter, so worst case is
    bounded and measurable.
- Risk: the token model's char-per-token heuristic is wrong enough to misjudge the
  cap.
  - Mitigation: heuristics are named constants in one place; `count_tokens` can
    calibrate them against a real prompt when precision matters.
- Risk: `thinking: {"type": "disabled"}` is forgotten or silently dropped, and
  adaptive thinking inflates output cost without anyone noticing.
  - Mitigation: asserted in a unit test, and the batch 4 checkpoint compares
    `usage.output_tokens` against the Sonnet 4.6 baseline. Sensitivity is tabulated
    by `scripts/cost_model.py` — at +50% output the volume goes from 5.49 $ to
    7.36 $.
- Risk: Sonnet 5's more literal instruction following revives the proper-noun
  conflict that `af84a62` had to soften out of the `RÈGLES ABSOLUES` block.
  - Mitigation: the glossary is enforced mechanically by `Protect`/`Apply`, so a
    locked term cannot regress; `glossary_violations` in the run report is the
    detector, and the batch 4 checkpoint reads it.
- Risk: the introductory rate expires on 2026-08-31 and the per-book cap, if sized
  on it, starts rejecting runs a month later for no visible reason.
  - Mitigation: size the cap on the standard rate from the outset and record the
    expiry date in `.env.example` next to the rate values.

## Open Questions

- Should the per-book cap be per model rather than global, now that the projection
  is honest and models span 0.15 $ to 10.67 $ per volume?
- Should adaptive thinking be enabled on Sonnet 5 after all? Batch 4 disables it to
  preserve current behaviour and cost; batch 5 tests it as a fifth arm. If it wins on
  quality, the cost is unbounded by projection and must be governed by measurement.
- ~~`--update-bible` grows the prefix chapter by chapter.~~ **Resolved — moved to
  [RFC 0008](./0008-pre-generated-series-bible.md).** Quantified at 4.33 $ per volume
  in prefix cost plus the loss of free, idempotent re-runs; the fix is to build the
  bible in a job that precedes translation, so a run consumes a frozen artefact and
  never writes to it. Investigating it found a silent data-loss blocker
  (`mergeSummaries` dedupes by `ChapterID`, and chapter IDs collide across volumes) and
  established that per-chapter summaries across a series cost more than the behaviour
  being replaced. RFC 0007 batches are unaffected; decision 10's chapter source and
  RFC 0008's frozen bible compose without conflict.
- Chapter-level parallelism during the LLM pass: worth its own RFC, or a follow-up
  batch here?
- Does the QA gate need a distinct signal for "segment kept its source markup
  because a placeholder was lost", separate from `untranslated_segments`?

## Decision Log

- 2026-08-01: created as `proposed` after a design interview covering the draft
  engine, DeepL, the TM shadowing defect, the cost model, the `Restore` gap and the
  LLM context scope. Eleven decisions recorded; four items explicitly deferred.
- 2026-08-01: decision 12 added — the `claude-sonnet-5` migration is pulled into
  scope at the author's request, moving from Non-Goals to a batch of its own. The
  adapter audit found only one applicable breaking change (adaptive thinking on by
  omission) but two unlogged failure modes (`stop_reason` absent) and a 30% effective
  cost increase from the new tokenizer at identical sticker prices. Model comparison
  becomes batch 5 and gains a fifth arm testing thinking enabled.
- 2026-08-01: the `--update-bible` open question quantified at 3.20 $ per volume and
  a lost re-run guarantee, and corrected — the cross-volume asymmetry it creates is
  not itself a defect.
