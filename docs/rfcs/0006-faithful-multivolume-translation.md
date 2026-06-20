# RFC 0006: Faithful Multi-Volume Literary Translation via Persistent Narrative Memory

- Status: `draft`
- Authors: ECecillo
- Created: 2026-06-20
- Updated: 2026-06-20
- Related:
  - Architecture: `docs/ARCHITECTURE.md` (§6.5 LLM post-edit, §3.1 V1 scope)
  - Milestones: `docs/milestones/M3-quality-optimization.md`
  - Related RFCs: `0003-llm-escalation-strategy.md`, `0004-epub-pipeline.md`, `0005-tm-storage-evolution.md`
  - Supersedes: n/a

## Context

The V1 pipeline (NMT → TM → optional selective LLM post-edit) produces
*adequate* translations: the meaning is right, but the **narrative voice is
not faithful**. A real-NMT run of Lumen V1 chapter 1 (LibreTranslate) gave
fluent but flat French — it does not preserve Kai's casual, sarcastic
first-person register, and it has no notion of how the book *tells* its story.

This is structural, not a tuning problem:

1. **NMT translates sentence-by-sentence, context-free and tone-flat.** No
   amount of NMT configuration yields voice fidelity.
2. **A faithful human translator carries forward state** within and across
   volumes: each character's idiolect, the formality of address between pairs
   of characters (and how it *evolves*), invented terminology, recurring
   motifs, and the narrator's stylistic conventions. Chapter N's translation
   depends on chapters 1..N-1; volume N depends on volumes 1..N-1.

Faithful literary translation is therefore an **LLM-with-persistent-narrative-
memory** problem. This RFC specifies that memory (a "series bible"), how it is
seeded/updated/consumed, how it threads through chapters and volumes, and the
cost on real API pricing using the locally-available Lumen volume as the
basis.

## Goals

- Preserve narrative voice and register across an entire multi-volume series.
- Carry an explicit, inspectable, evolving **series bible** chapter→chapter and
  volume→volume.
- Translate with the LLM as the primary engine for literary mode, using
  adjacent context + bible + locked glossary, while keeping NMT as a cheap
  draft/coverage and TM as exact-reuse.
- Keep cost predictable and bounded (reuse the M2 budget guardrails).
- Measure *fidelity*, not just adequacy.

## Non-Goals

- Replacing the V1 NMT path (it stays for cheap/coverage runs and TM warming).
- Fully autonomous publishable quality with zero human review.
- Real-time/interactive translation.
- Training or fine-tuning a model.

## Key Concept: The Series Bible

A persisted, versioned, **series-scoped** store (extends `internal/tm` SQLite,
or a sibling `internal/narrative`) holding:

| Section | Contents | Evolves? |
|---|---|---|
| **Characters** | per character: voice profile (register, verbal tics, formality, sample FR lines), role | yes — voices develop across volumes |
| **Glossary** | locked names/places/invented terms (reuses `internal/glossary`) | append-only |
| **Address matrix** | per ordered character pair: tutoiement/vouvoiement + an *evolution log* (when/why it changes) | **yes — the central French-fidelity state** |
| **Style guide** | POV, tense, dialogue rendering, narrator tone, honorific policy (-san/-sama) | rarely |
| **Rolling summaries** | per-chapter plot/voice summary, threaded forward | append-only |

The **tu/vous matrix** is the canonical hard problem for JP/EN→FR literary
translation: it must be consistent within a volume and evolve at narrative
beats (characters grow closer → `vous`→`tu`). NMT cannot track this; the bible
makes it explicit and auditable.

## Design

### Packages

```
internal/narrative/   # bible store (SQLite, series-scoped, versioned) + types
internal/translate/   # add LLMLiteraryTranslator (uses internal/llm client)
internal/pipeline/    # literary mode: thread bible+context; bible-update stage
cmd/.../              # `style-profile`, `bible` inspect/edit subcommands
```

### Flow (literary mode)

```mermaid
flowchart TB
  A[Seed bible: style-profile samples vol.1] --> B[Per chapter, in spine order]
  B --> C[Compose prompt: style guide + bible slice<br/>(chars present, address rules) + rolling summary<br/>+ prev/next paragraph context + glossary]
  C --> D[LLM translate chapter segments]
  D --> E[Restore placeholders + glossary enforce]
  E --> F[Continuity check: drift vs bible?]
  F --> G[Bible-update pass: extract new terms,<br/>voice notes, address changes]
  G --> B
  G --> H[Next volume starts from final bible state]
```

- **Chapters processed sequentially** (narrative causality) — the rolling
  summary + bible are threaded forward. This is the opposite of parallelizable,
  and intentionally so.
- **Volumes batched in series order**, persisting the bible across runs
  (extends the M2 shared-TM batch to a shared-bible batch).
- **Context window**: send prev/next paragraph (schema already has
  `context_prev`/`context_next`, §16.1) so the LLM preserves flow.
- **Prompt caching is the key cost lever**: the style guide + bible prefix is
  stable within a volume, so it is cached (read at ~0.1× input price) across
  every chapter call.

### Seeding & updating the bible

- `style-profile`: sample N chapters of vol.1, ask the LLM to characterize the
  narrator voice + main character idiolects → draft style guide + character
  profiles (human-reviewable).
- **Bible-update pass** after each chapter: an LLM call extracts new terms,
  voice observations, and address-relationship changes from the
  just-translated chapter; appends to the bible (versioned, reviewable).

### Measuring fidelity

- chrF/COMET (`internal/eval`) measure *adequacy* only — keep for that.
- Add an **LLM-as-judge fidelity pass** (rubric: voice match, register,
  naturalness, faithfulness to source style). Low scores → flag for the
  selective-escalation path (M3) or human review. (Note: the locally-provided
  FR file is a *web-novel* fan translation of a *different* source text than
  the EN light novel, so it is **not** usable as an automated reference — useful
  only for glossary/name bootstrapping.)

## Cost Model (real API pricing)

**Pricing across providers** (per 1M tokens — Anthropic from the `claude-api`
skill, cached 2026-06-04; OpenAI/DeepSeek from web sources June 2026; **prices
are volatile — re-verify before relying on them**):

| Provider / Model | Input | Output | Cached input |
|---|---|---|---|
| **Anthropic** Opus 4.8 | $5.00 | $25.00 | ~0.1× ($0.50) |
| **Anthropic** Sonnet 4.6 | $3.00 | $15.00 | ~0.1× ($0.30) |
| **Anthropic** Haiku 4.5 | $1.00 | $5.00 | ~0.1× ($0.10) |
| **OpenAI** GPT-5.5 | $5.00 | $30.00 | $0.50 |
| **OpenAI** GPT-5.4 | $2.50 | $15.00 | $0.25 |
| **OpenAI** GPT-5.4 mini | $0.75 | $4.50 | — |
| **OpenAI** GPT-5 | $1.25 | $10.00 | — |
| **OpenAI** GPT-5 mini | $0.25 | $2.00 | — |
| **OpenAI** GPT-4o (legacy) | $2.50 | $10.00 | — |
| **DeepSeek** V4 Flash (`deepseek-chat` alias) | $0.14 | $0.28 | $0.014 (1/10) |
| **DeepSeek** V4 Pro | $1.74 | $3.48 | 1/10 |

Levers: **prompt caching** (all three support it — Anthropic reads ≈0.1×,
writes 1.25×/2×; OpenAI cached input ≈10× cheaper; DeepSeek cache-hit 1/10) is
the biggest lever here because the bible/style-guide prefix is reused on every
chapter call. **Batch API** ≈ ~50% off on Anthropic and OpenAI for *independent*
calls (helps within a chapter, not across the sequential bible updates).

> **Design implication — one adapter covers OpenAI *and* DeepSeek.** Both expose
> an **OpenAI-compatible** `/v1/chat/completions` API, so a single
> `OpenAICompatibleLLM` adapter in `internal/llm` (base URL + key + model from
> config) reaches OpenAI, DeepSeek, and most other hosted LLMs. Anthropic uses
> its own Messages API (separate adapter). `internal/llm.PostEditor` /
> `LLMLiteraryTranslator` stay interfaces; provider is pure config. **Translation
> is output-heavy**, so the *output* price dominates the cost below — that is why
> DeepSeek and the GPT-5 mini/nano tiers look so cheap.

**Measured basis** (local Lumen LN V1): **75,083 words ≈ 429,803 chars ≈
~107,000 tokens** of source text, 2,833 segments, 40 XHTML docs.

**Per-volume assumptions** (literary mode, chapter-at-a-time, caching on):

| Component | Tokens |
|---|---|
| Source text input (full pass) | ~107k |
| Context-window duplication + glossary slices | ~+90k input |
| Bible/style-guide prefix, **cache-read** (~5k × ~40 calls) | ~200k @ 0.1× |
| French output (~1.1× source) | ~120k output |
| Bible-update pass (per chapter) | ~80k input / ~40k output |
| **Billable input (full-rate equiv.)** | **~300k** |
| **Cache-read input** | **~200k @ 0.1× = ~20k-equiv** |
| **Output** | **~160k** |

**Per-volume and per-series cost** (~320k input-equiv. + ~160k output per
volume; ~26-volume series; caching applied; order-of-magnitude):

| Model | ≈ /volume | ≈ /series (26 vol.) |
|---|---|---|
| OpenAI GPT-5.5 | ~$6.4 | ~$166 |
| Anthropic Opus 4.8 | ~$5.6 | ~$146 |
| Anthropic Sonnet 4.6 | ~$3.4 | ~$87 |
| OpenAI GPT-5.4 | ~$3.2 | ~$83 |
| OpenAI GPT-4o (legacy) | ~$2.4 | ~$62 |
| OpenAI GPT-5 | ~$2.0 | ~$52 |
| Anthropic Haiku 4.5 | ~$1.1 | ~$29 |
| DeepSeek V4 Pro | ~$1.1 | ~$29 |
| OpenAI GPT-5.4 mini | ~$1.0 | ~$25 |
| OpenAI GPT-5 mini | ~$0.4 | ~$10 |
| DeepSeek V4 Flash | ~$0.09 | ~$2.3 |

Price is **not** quality. For nuanced literary French, the cheap tiers
(DeepSeek Flash, GPT-5 mini/nano) are unproven and may regress voice/idiom — and
DeepSeek's hosting raises a separate data-governance question (see Risks). The
spread is ~70× (DeepSeek Flash to GPT-5.5), so the right move is a **bake-off**
(below), not picking by price.

Add a sampled fidelity-judge pass (~10-20% of segments) ≈ +10-15%. Batch API on
the independent within-chapter calls can shave a further ~20-30% off the
translation portion. These are estimates to size the work, **not** a quote —
the M2 `estimate` command + budget caps enforce the real ceiling per run.

## Proposed Decision

Adopt **LLM-as-primary-translator in a new "literary mode", driven by a
persistent series bible**, with a **provider-agnostic** LLM layer: an Anthropic
Messages adapter and a single **OpenAI-compatible adapter covering OpenAI,
DeepSeek, and others** (base URL + model from config). **Default to Sonnet 4.6**
as the workhorse pending a bake-off, with Opus 4.8 / GPT-5.x for flagged or
low-fidelity segments and final polish; NMT (LibreTranslate, free) retained for
cheap drafts + TM warming. Pick the production model by a **fidelity bake-off**
(same chapter across Sonnet 4.6 / GPT-5.4 / DeepSeek V4, scored by the fidelity
rubric), not by price. Prompt caching of the bible prefix is mandatory for cost
control. Keep V1 NMT mode as the cheap path; literary mode is opt-in
(`--mode literary`).

## Implementation Plan

> Step-by-step, TDD, executor-ready plan:
> `docs/implementation/0006-literary-translation-impl.md` (7 phases, with Go
> signatures, RED tests, per-phase commit messages and reviewer checklists).
> The summary below is the shape; that document is the build order.

1. `internal/narrative`: bible schema (SQLite, series-scoped, versioned) +
   types + load/save + address-matrix with evolution log.
2. `style-profile` command: seed the bible from vol.1 samples.
3. `internal/llm`: add an **`OpenAICompatibleLLM`** adapter
   (`/v1/chat/completions`, base URL + key + model from config) alongside the
   Anthropic Messages adapter — one adapter reaches OpenAI, DeepSeek, and
   compatible hosts. Provider selected by config (mirrors the existing NMT
   provider factory). `LLMLiteraryTranslator` composes the prompt (style guide +
   bible slice + context + glossary) behind the `internal/llm` interface, with a
   prompt-cache breakpoint on the stable bible prefix.
4. Pipeline literary mode: sequential chapter processing threading the rolling
   summary; bible-update stage after each chapter.
5. Continuity check + LLM-as-judge fidelity scoring; wire into M3 escalation.
6. Volume batching with shared bible (extends M2 batch).
7. `bible` inspect/edit subcommands (human review of the evolving state).

### Testing strategy (TDD, as in M1–M4)

Same discipline as the rest of the project: write the test first, table-driven,
deterministic, no live network in unit/integration tests.

- **Deterministic mock LLM servers** (mirroring `internal/translate/nmttest`
  and `internal/llm/llmtest`): an OpenAI-compatible mock (`/v1/chat/completions`)
  and the existing Anthropic-shaped mock, so the literary translator, the
  `OpenAICompatibleLLM` adapter, the bible-update pass, and the fidelity judge
  are all tested **offline** against canned responses.
- `internal/narrative`: unit tests for the bible store (save/load, versioning,
  address-matrix evolution log) and the tu/vous consistency check.
- `LLMLiteraryTranslator`: tests that the composed prompt contains the right
  bible slice + context + glossary, that placeholders survive (reuse the
  existing strict round-trip + the `<x></x>` regression), and that prompt-cache
  breakpoints sit on the stable prefix.
- `OpenAICompatibleLLM`: adapter tests (request shape, auth header, error/retry,
  non-2xx) against the mock — same coverage the NMT/HTTP adapters already have.
- Pipeline literary mode: an end-to-end integration test (fixture EPUB + mock
  LLM) asserting sequential chapter order, bible carry-over, QA gate, and
  artifacts — extends the existing `tests/integration` suite.
- **No live-API test in CI.** Real-provider calls happen only in the manual
  bake-off and real runs, gated by `estimate` + budget caps.

## Rollout Plan

- **Provider bake-off first**: translate Lumen V1 ch.1 with the same bible +
  prompt across Sonnet 4.6 / GPT-5.4 / DeepSeek V4 Flash, score with the
  fidelity rubric + a human read, and pick the production model on quality (cost
  is the tiebreaker). Output is the dominant cost, so a cheap-output model that
  passes the rubric is the win.
- Validate the chosen model on V1 ch.1 vs the current NMT output — side-by-side
  voice comparison.
- Then full V1; then a second volume to exercise cross-volume bible carry-over.
- Gate spend with the existing per-book/daily/monthly budget caps.

## Impact

- **Technical**: new `internal/narrative`; literary mode is sequential
  (slower, by design); cache discipline is load-bearing for cost.
- **Cost**: moves from ~free (local NMT) to ~$3-6/volume on Sonnet — bounded by
  M2 caps and the pre-run `estimate`.
- **Security/Compliance**: source + bible text sent to the LLM provider —
  respect data-retention/usage policy (§11); only process rights-cleared books.

## Acceptance Criteria

- Voice/register fidelity judged better than NMT on a human read of a sample.
- tu/vous consistency within a volume = 100% vs the bible's address matrix;
  changes only at logged narrative beats.
- Locked-term compliance stays 100% (existing glossary gate).
- Placeholder integrity ≥ 99.5% (existing QA gate) preserved through the LLM.
- Per-run spend stays within the configured budget cap.

## Risks and Mitigations

- **Cost runaway** → prompt caching of the bible + M2 budget caps + pre-run
  `estimate` + Sonnet default.
- **Bible drift / hallucinated state** → versioned, human-reviewable bible;
  continuity-check pass; conservative append-only updates.
- **Inconsistent tu/vous** → explicit address matrix consulted on every call;
  continuity check flags violations.
- **Placeholder loss via LLM** → existing strict restore + QA gate (already
  catches the LibreTranslate `<x></x>` case fixed in `94a6f9c`).
- **Provider lock-in / pricing shifts** → `internal/llm` is an interface; the
  OpenAI-compatible adapter + config-driven model make switching providers a
  config change. Prices are volatile (the table is a June 2026 snapshot) — the
  M2 cost model is config-driven so rates update without code.
- **Data governance (DeepSeek)** → DeepSeek is hosted in China; source text +
  bible would leave for that jurisdiction. Flag to the user before selecting it
  for rights-sensitive or personal content; prefer Anthropic/OpenAI (or a
  self-hosted open model via the same OpenAI-compatible adapter) when retention
  policy matters. Honor each provider's data-retention/usage policy (§11).
- **Cheap-tier quality regressions** → the bake-off gates model choice on the
  fidelity rubric, not price; cheap output tiers are unproven for literary voice.

## Open Questions

- Bible granularity: per-series vs per-author shared bible?
- Auto-apply fidelity-judge corrections, or always surface for human review in
  early runs?
- How aggressively to chunk (segment vs paragraph-group vs chapter) for the
  best fidelity/cost trade-off — needs a small benchmark.

## Decision Log

- 2026-06-20: status `draft`. Motivated by the real LibreTranslate run on
  Lumen V1 ch.1 showing adequate-but-not-faithful output. Cost model grounded
  in the measured local volume (~107k tokens) and 2026-06-04 API pricing.
- 2026-06-20: added multi-provider comparison (Anthropic / OpenAI / DeepSeek)
  with a single OpenAI-compatible adapter covering OpenAI + DeepSeek; added the
  TDD testing strategy; model choice gated on a fidelity bake-off, not price.
