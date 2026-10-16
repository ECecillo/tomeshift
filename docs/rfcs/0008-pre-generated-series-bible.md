# RFC 0008: Pre-Generated Series Bible as a Job Separate from Translation

- Status: `accepted`
- Authors: ECecillo
- Created: 2026-08-01
- Updated: 2026-08-02
- Related:
  - Architecture: `docs/ARCHITECTURE.md`
  - Evolves: [0006 - Faithful Multi-Volume Literary Translation via Persistent Narrative Memory](./0006-faithful-multivolume-translation.md)
  - Resolves an open question of: [0007 - Google Cloud Translation as the Draft Engine](./0007-google-translate-draft-and-token-cost-model.md)
  - Cost model reference: `scripts/cost_model.py`
  - Supersedes: n/a
  - Superseded by: n/a

## Context

RFC 0006 established the series bible as persistent narrative memory, populated
incrementally by `--update-bible` as a translation run walks a book chapter by
chapter. RFC 0007 quantified what that costs and left it as an open question. This
RFC closes it.

`updateBible` mutates the bible in place at the end of every chapter
(`internal/pipeline/orchestrator.go:467-475`, then `483-513`), and the same pointer
feeds the next chapter's `TranslateChapter` call. Because `stablePrefix` embeds
`RESUME_DE_L_HISTOIRE` (`internal/literary/prompt.go:63`), the cacheable prefix
changes 68 times per volume instead of once.

Three defects follow. Measured on WN vol. 1 (68 chapters, 130 LLM calls at the
shipped batch size of 50) with summaries measured at 274 tokens each, priced on
`claude-sonnet-5` at standard rates:

1. **Cost.** The prefix grows from 2,997 tokens at chapter 1 to 21,355 at chapter 68,
   and is written to cache 68 times rather than read 129 times. Prefix cost rises from
   0.17 $ to **4.33 $ per volume**.
2. **Re-runs stop being free or reproducible.** `llm_cache.sqlite` is keyed by model
   plus messages. With a frozen bible a re-run costs 0 $; with `--update-bible` the
   bible on disk has changed since the previous run, so every prompt differs, every
   cache lookup misses, and the whole pass is paid again. The operation is also not
   idempotent: chapter 1 sees 0 summaries on the first run and 68 on the second.
3. **The version number does not move.** `updated.Version = bible.Version`
   (`orchestrator.go:492`) keeps the version fixed while the content evolves, so
   `just bible` reports `v3` whether the bible holds 3 summaries or 70.

What is **not** a defect: the asymmetry whereby chapter 1 knows less than chapter 68.
That mirrors sequential translation — a human translating chapter 2 has not read
chapter 50 either. Earlier framing in RFC 0007 treated it as a problem; it is not.

The proposal that resolves all three: build the bible **before** any translation, from
every volume available, and regenerate it only when a new volume is published.
Translation runs then consume a frozen artefact and never write to it.

## Goals

- The bible is an input to a translation run, never an output. Frozen for the whole
  run, byte-identical across chapters, and identical across re-runs.
- Style, character voices and address rules draw on **every** available volume — this
  is where completeness is wanted and where it is nearly free.
- Narrative summaries stay bounded in size and in scope.
- Building the bible is its own command with its own cost, run once per published
  volume rather than once per translation.

## Non-Goals

- Changing the translation pipeline's prompt composition (RFC 0007 decision 10 owns
  that).
- Removing `--update-bible` from the CLI. It becomes non-default and documented as
  incompatible with a frozen prefix, but tearing it out is not required here.
- Cross-series bibles, or any notion of a shared style across unrelated works.
- Automatic detection of newly published volumes.

## Options Considered

### Summary granularity

#### Option A: One synopsis per volume — recommended

- Pros: bounded. 15 volumes cost ~300 tokens each, so the prefix stays under 7.5k
  tokens and **0.41 $ per volume**. Arguably more useful too: a chapter-42-of-volume-7
  summary is noise when translating chapter 3 of volume 1. What a translator needs from
  other volumes is the arc, not the beat sheet.
- Cons: loses fine-grained recall of a specific earlier scene.

#### Option B: Keep per-chapter summaries, pre-generated

- Pros: maximum recall; no change to `ChapterSummary` semantics beyond the volume key.
- Cons: unbounded. At 5 volumes the prefix costs **5.31 $ per volume** — worse than the
  `--update-bible` behaviour this RFC exists to remove. At 15 volumes it is **15.59 $**,
  more than the entire LLM pass.

| Variant | Prefix tokens | $ / volume |
| --- | ---: | ---: |
| `--update-bible` (current) | grows 2,997 → 21,355 | 4.33 |
| Frozen, 3 summaries (today's de-facto state) | 2,997 | 0.17 |
| Pre-generated, per chapter, 5 volumes | 96,157 | 5.31 |
| Pre-generated, per chapter, 15 volumes | 282,477 | 15.59 |
| **Pre-generated, one synopsis per volume, 5 volumes** | 4,497 | **0.25** |
| **Pre-generated, one synopsis per volume, 15 volumes** | 7,497 | **0.41** |

#### Option C: Hierarchical — per-volume synopsis plus per-chapter for the current volume only

- Pros: bounded across the series while keeping fine detail where it is most likely to
  matter.
- Cons: two summary kinds to generate, store and render; and RFC 0007 decision 10
  already puts the current chapter's full source in the prefix, which covers local
  recall better than a summary of it would. Deferred, not rejected.

### How a volume synopsis is produced

Neither existing profiler operation fits. `Seed(samples []string)` makes one call and
returns a whole `Bible` from **excerpts**; `Update` does the same after a chapter. Both
round-trip the entire bible JSON, whereas a synopsis needs a narrower operation: a
volume's source in, one string out.

More importantly, **sampling is the right method for style and the wrong one for plot.**
Register, tone and idiolect are recoverable from five excerpts — that is exactly what
`Seed` exists for. A plot synopsis is not: you cannot summarise a story from selected
passages. Two different extraction problems, two different methods.

#### Option A: One call per volume, whole volume in context

- Pros: simplest to write and to review. 119k source tokens fits comfortably (200k on
  Haiku 4.5, 1M on Sonnet 5). Cheapest.
- Cons: **298:1 compression in a single pass.** The model must hold a whole novel and
  decide unaided what matters. Mid-context degradation is a documented failure mode on
  this shape of task, and it is silent — the synopsis would read well on the opening and
  the ending while thinning out in the middle, with nothing to flag it.

#### Option B: Map-reduce — recommended

- Map: one source-only summary per chapter, 68 calls at ~1,020 tokens in and ~274 out
  (4:1).
- Reduce: one call over the 68 summaries, ~18,600 tokens in and ~400 out (47:1).
- Pros: every chapter is actually read, so there is no invisible gap. Both compression
  ratios are comfortable. It reuses the source-only primitive this RFC already requires
  (decision 5) rather than adding a second mechanism. The 68 chapter summaries are a
  by-product worth keeping — they are precisely what Option C of the summary-granularity
  question would need if per-volume synopses ever prove too coarse.
- Cons: 69 calls instead of 1, and a two-stage prompt to maintain.

| Model | A — one call | B — map-reduce |
| --- | ---: | ---: |
| Haiku 4.5 | 0.12 $ | 0.18 $ |
| Sonnet 4.6 | 0.36 $ | 0.55 $ |
| Sonnet 5, standard rates | 0.47 $ | 0.71 $ |

The cost is **one-off per volume and never replayed** — across 15 volumes on Sonnet 4.6
that is 5.46 $ against 8.24 $, once, for the life of the project. Cost does not decide
here; coverage does.

#### Option C: Sampling, as `Seed` does today

- Rejected for synopses, for the reason above. Retained unchanged for style, voices and
  address rules, where it is the correct method.

### Narrative scope

#### Option A: Summaries bounded to volumes 1..N when translating volume N — recommended

- Pros: the model never receives the ending of a story it is rendering the beginning
  of. This matters concretely here: the style guide states the horror of *Return by
  Death* rests on matter-of-fact description in loop sections, an effect that depends
  on not knowing. A model aware of a character's later-revealed nature may colour their
  early dialogue.
- Cons: one bible artefact per volume rather than one per series; regenerating on a new
  volume produces N artefacts, not one.

#### Option B: Whole series, unbounded

- Pros: a single bible to maintain; defensible as "a professional translator has read
  the work before starting".
- Cons: an improvement attributable to richer context cannot be told apart from a drift
  caused by knowing the ending.

Style, voices and address rules are **not** bounded under either option — they are
drawn from every available volume. Only `Summaries` is scoped.

## Proposed Decision

| # | Decision |
| --- | --- |
| 1 | Build the bible in a dedicated pre-translation job. Translation runs treat it as read-only. |
| 2 | `Summaries` becomes one synopsis per volume, not one per chapter. |
| 3 | Style guide, character voices and address rules are seeded from **all** available volumes; `Summaries` is scoped to volumes 1..N for the bible used to translate volume N. |
| 4 | Add a volume dimension to summary identity, and make `mergeSummaries` collision-safe (see below — this is a correctness blocker, not a nicety). |
| 5 | Add a source-only summarisation path to `narrative.Profiler`, and produce each volume synopsis by **map-reduce**: one summary per chapter, then one reduction over those summaries. Keep the per-chapter summaries as a stored by-product. |
| 6 | Bump the bible version on every content change, so the version identifies the content. |
| 7 | `--update-bible` stays available but is documented as mutually exclusive with a cached prefix, and is no longer part of any recommended workflow. |

### Decision 4: the blocker

`segmentBookByChapter` derives chapter IDs per book:

```go
chapterID := fmt.Sprintf("ch_%04d", chapterIdx)   // orchestrator.go:605
```

Volume 1's first chapter is `ch_0001`. So is volume 2's. And `mergeSummaries` dedupes
by `ChapterID`, with base winning:

```go
for _, in := range incoming {
    if seen[in.ChapterID] { continue }            // internal/narrative/merge.go
```

Merging volume 2 into a bible that already holds volume 1 therefore **discards every
chapter of volume 2 silently** — no error, no warning, no count. `ChapterSummary` is
`{ChapterID, Summary}` and carries no volume field, so the collision is structural.

Decision 2 reduces the blast radius (one synopsis per volume means the key is the
volume, not the chapter) but does not remove it: the key must still be unique per
volume, and `mergeSummaries` must stop treating a duplicate key as a no-op. A
merge that drops input should report how many entries it dropped.

Corroborating observation: the summaries in `data/lumen-series.sqlite` are keyed
`prologue`, `1`, `2` — not `ch_0001`. They came from `style-profile` seeding, not from
`--update-bible` during a run, which suggests the incremental path has never actually
executed against this bible. That is consistent with the version sitting at 3.

### Decision 5: why a new path is needed, and its shape

`Profiler.Update` requires the translation:

```go
func (p Profiler) Update(ctx context.Context, bible Bible, chapterID, sourceText, targetText string) (Bible, error)
```

Building the bible before translating means only the source exists. A plot synopsis is
narrative content and can be written from the source alone, but the current signature
and prompt (`updateUserPrompt`) both assume a source/target pair — and both return a
whole `Bible`, which a synopsis step does not need. This is a code path, not a flag.

The two new operations are narrow and independently testable:

```
SummariseChapter(ctx, chapterID, sourceText) (string, error)     // map,    4:1
SynthesiseVolume(ctx, volumeID, []string)    (string, error)     // reduce, 47:1
```

Neither touches `Bible`; the caller assembles the result. Even for volumes already
translated, both operate on the **source** only — uniformity across volumes matters more
than the marginal gain from feeding an existing translation, and a synopsis is narrative
content rather than translation commentary.

## Implementation Plan

1. `internal/narrative/types.go`: add a volume dimension to summary identity; keep
   `ChapterSummary` for backward compatibility or rename to a volume-scoped type,
   whichever reads better once the store schema is settled.
2. `internal/narrative/merge.go`: make `mergeSummaries` collision-safe and return (or
   log) a dropped-entry count. Add a table test with two volumes whose chapter IDs
   collide — it must fail before the fix.
3. `internal/narrative/store.go`: schema addition for the volume key; migration for
   the three existing rows.
4. `internal/narrative/profiler.go`: `SummariseChapter` and `SynthesiseVolume`, each
   with its own prompt. The map step is trivially parallelisable across chapters —
   unlike the translation pass, there is no context chaining between them — but keep it
   sequential until the sequential cost is shown to matter.
5. `internal/cli`: extend `style-profile` (or add a sibling) to walk a directory of
   volumes, seed style/voices/address from all of them, and emit summaries scoped to
   volumes 1..N. Emit one bible per N when the series has more than one volume.
6. `internal/pipeline`: bump the version on content change; leave `--update-bible`
   functional but drop it from the documented workflow.
7. `docs/translation-guide.md`, `docs/improving-translations.md`, `Justfile`: the new
   pre-translation step, and the removal of `--update-bible` from the recipes.

## Rollout Plan

- Local only.
- Order: regenerate the Lumen bible from the volumes on disk, inspect it with
  `just bible`, then translate chapter 1 and compare against the RFC 0007 batch-1
  baseline.
- The existing `data/lumen-series.sqlite` is copied before migration; rollback is
  restoring the copy and reverting the batch.
- A bible regenerated from more volumes changes the prefix, so it invalidates
  `llm_cache.sqlite` once, by design. That is a one-off cost per regeneration, not a
  per-chapter one.

## Impact

### Technical

- `narrative.Store` gains a schema field; three existing rows to migrate.
- `mergeSummaries` changes semantics from silently-lossy to explicit.
- Translation runs become pure with respect to the bible: same inputs, same prompts,
  same cache keys.

### Cost

- Prefix cost per translated volume drops from **4.33 $** (current `--update-bible`
  behaviour) to **0.25 $–0.41 $** depending on how many volumes exist.
- Re-runs return to 0 $.
- Building the bible costs, per published volume and once only: the existing
  `style-profile` seed (~0.10 $ per the current guide) plus the map-reduce synopsis at
  **0.55 $ on Sonnet 4.6** (69 calls). Across 15 volumes that is ~9.75 $ total, never
  replayed — against 4.33 $ *per translated volume* under the behaviour being removed.
  The bible job pays for itself after roughly three volumes.

### Security/Compliance

- No new external dependency. The bible job reads the same EPUBs the translation job
  already reads.

## Acceptance Criteria

- Merging two volumes whose chapter IDs collide preserves both volumes' summaries, and
  a deliberately lossy merge reports a non-zero dropped count.
- A bible built from N volumes contains style, voices and address rules referencing
  material from all N, and summaries for exactly volumes 1..N.
- Two consecutive translation runs of the same book with the same bible produce
  byte-identical prompts, and the second costs 0 $ against `llm_cache.sqlite`.
- The bible version changes whenever the content changes, and `just bible` reports it.
- Prefix tokens for a 15-volume series stay under 10k.
- `SummariseChapter` and `SynthesiseVolume` are unit-testable against a stub chat client
  without any bible round-trip, and the map step's chapter count matches the volume's
  spine document count.
- The synopsis of WN vol. 1 names events from the opening, the middle and the ending of
  the volume — the mid-volume check being the one that distinguishes map-reduce from a
  single-pass summary.
- The 68 per-chapter summaries are persisted alongside the synopsis, not discarded.

## Risks and Mitigations

- Risk: a source-only synopsis is thinner than one written from a source/target pair.
  - Mitigation: it is narrative content, not translation commentary. Compare a
    generated synopsis against one of the three existing summaries before committing to
    the prompt.
- Risk: the reduce step loses mid-volume material anyway, just one stage later.
  - Mitigation: 47:1 over 68 short summaries is a far easier task than 298:1 over raw
    prose, and the acceptance criterion explicitly checks that the synopsis names a
    mid-volume event. If it still thins out, the reduce step can go hierarchical (reduce
    in groups, then reduce the groups) without touching the map step.
- Risk: 69 calls per volume makes the bible job slow enough to discourage regenerating it.
  - Mitigation: the map step has no context chaining, so it parallelises freely if the
    sequential wall-clock turns out to matter. Measure before adding concurrency.
- Risk: the schema migration corrupts the only curated bible in existence.
  - Mitigation: copy `data/lumen-series.sqlite` first; the migration is additive.
- Risk: per-volume synopses lose a detail that a per-chapter summary would have caught.
  - Mitigation: RFC 0007 decision 10 puts the current chapter's full source in the
    prefix, which covers local recall better than any summary. Option C remains
    available if measurement shows a gap.
- Risk: one bible artefact per volume becomes tedious to manage as the series grows.
  - Mitigation: generate them from one command in one pass; they are derived data and
    can be rebuilt at any time.

## Open Questions

- Should the bible be content-addressed (a hash of its rendered prefix) so a run can
  record exactly which bible produced it? The version number would then be derived
  rather than assigned.
- Does the QA report need to name the bible version and scope, so a translation can be
  traced to the narrative memory that produced it?
- When a new volume is published, should previously translated volumes be re-translated
  against the enriched bible, or left as they are? Leaving them is cheaper and
  reproducible; re-translating them makes the series internally consistent.
- Should Option C (per-chapter summaries for the current volume only) be measured, or
  is the chapter source from RFC 0007 decision 10 sufficient?

## Decision Log

- 2026-08-02: implemented and accepted. `ChapterSummary` became `narrative.Summary`
  with a `VolumeID`, keyed on the `(VolumeID, ChapterID)` pair; `MergeBibles` now
  returns a `MergeStats` reporting dropped entries. The volume dimension lives
  inside `summaries_json` rather than in a column, so pre-0008 rows need no DDL
  migration — they load with an empty `VolumeID` and render under their own
  label. The map step's per-chapter summaries go to a new `chapter_summaries`
  table, deliberately outside the bible so they never reach the cacheable prefix.
  Version assignment moved into `SQLiteStore.Save`, which compares stored content
  and keeps the version when nothing changed. The new `series-bible` command
  writes one bible per volume under `<series>@<volume>` plus the full series
  under `<series>`; scoping by series ID avoided adding a `--volume` flag to
  `run` and a scope column to the store.
- 2026-08-01: synopsis generation settled on **map-reduce** (68 chapter summaries at 4:1,
  then one reduction at 47:1) over a single 298:1 pass. Cost was not the deciding factor —
  0.55 $ against 0.36 $ per volume, once — but silent mid-context degradation was: a
  single-pass synopsis would read well at the edges and thin out in the middle with
  nothing to signal it. Two narrow operations added (`SummariseChapter`,
  `SynthesiseVolume`), neither round-tripping `Bible`. Sampling is retained for style,
  voices and address rules, where it is the correct method, and rejected for plot.
- 2026-08-01: created as `proposed`. Resolves the `--update-bible` open question left by
  RFC 0007, following a proposal to pre-generate the bible from all available volumes.
  Investigation found a silent data-loss blocker (chapter-ID collision across volumes in
  `mergeSummaries`) and established that per-chapter summaries across a series cost more
  than the behaviour being replaced — hence one synopsis per volume. Narrative scope
  bounded to volumes 1..N to avoid handing the model the ending; style and voices remain
  series-wide.
