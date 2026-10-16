# RFC 0005: Translation Memory Storage Evolution (SQLite → PostgreSQL) and Fuzzy TM Spike

- Status: `draft`
- Authors: ECecillo
- Created: 2026-06-15
- Updated: 2026-06-15
- Related:
  - Architecture: `docs/ARCHITECTURE.md` (§6.3, §9)
  - Milestones: `docs/milestones/M2-cost-optimization.md`
  - Related RFC: `docs/rfcs/0004-epub-pipeline.md`
  - Supersedes: n/a
  - Superseded by: n/a

## Context

M1 ships an exact-match translation memory backed by SQLite via
`modernc.org/sqlite` (pure Go, no cgo). The schema is defined in
`docs/ARCHITECTURE.md` §16.2 and implemented in `internal/tm`:

- `segments(source_hash PK, source_text, target_text, source_lang, target_lang, engine, quality_score, schema_version, created_at)`
- `runs(run_id PK, book_id, started_at, ended_at, actual_cost_usd, created_at)` (added in M2 for budget accounting)

Two M2 deliverables motivate this RFC:

1. **TM storage evolution plan for service mode.** SQLite is ideal for the V1
   single-machine CLI, but V2 service mode (`ARCHITECTURE.md` §9.2) needs a
   central, concurrently-writable TM shared by queue workers.
2. **Fuzzy/semantic TM feasibility.** V1 reuses translations only on exact
   normalized-hash match. Near-duplicate sentences (one word changed,
   punctuation differences below the normalization threshold) miss the cache.
   We want a recommendation on whether/how to add fuzzy matching.

The `internal/tm.Store` interface already abstracts persistence, so swapping or
adding a backend does not touch the pipeline.

## Goals

- Define a concrete, low-risk migration path from SQLite to PostgreSQL for V2.
- Keep the V1 SQLite path unchanged and fully supported.
- Produce a recommendation (with rationale) on fuzzy/semantic TM storage.
- Preserve the `tm.Store` interface as the single integration seam.

## Non-Goals

- Implementing PostgreSQL support now (V1 stays SQLite-only).
- Implementing fuzzy matching now (V1 is exact-match only by design).
- Multi-region / sharded TM (beyond V2 scope).
- Changing the segment hashing or normalization scheme.

## Options Considered

### A. Stay on SQLite indefinitely

- Pros: zero new infra; pure-Go; simplest ops; great for single-machine runs.
- Cons: single-writer model; no safe concurrent access from multiple workers;
  no server-side vector/trigram extensions for fuzzy matching.

### B. PostgreSQL for service mode, SQLite for local (dual backend)

- Pros: keeps the frictionless local story; PostgreSQL unlocks concurrent
  workers, richer indexing, and extensions (`pg_trgm`, `pgvector`); both sit
  behind the existing `tm.Store` interface.
- Cons: two SQL dialects to maintain; migration tooling required; CI must cover
  both backends.

### C. PostgreSQL everywhere (drop SQLite)

- Pros: one backend, one dialect.
- Cons: forces every local CLI run to provision a Postgres instance; regresses
  the V1 "clone and run" experience; heavier Docker workflow.

## Proposed Decision

**Adopt Option B.** SQLite remains the default local backend; PostgreSQL is
introduced as an additional `tm.Store` implementation selected by configuration
when running in service mode. This matches the deployment split already
described in `ARCHITECTURE.md` §9 and keeps the local developer loop intact.

### Schema parity

The PostgreSQL schema mirrors the SQLite tables with stricter types:

```sql
CREATE TABLE IF NOT EXISTS segments (
  source_hash    TEXT PRIMARY KEY,
  source_text    TEXT NOT NULL,
  target_text    TEXT NOT NULL,
  source_lang    TEXT NOT NULL,
  target_lang    TEXT NOT NULL,
  engine         TEXT NOT NULL,
  quality_score  DOUBLE PRECISION,
  schema_version INTEGER NOT NULL DEFAULT 1,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS runs (
  run_id          TEXT PRIMARY KEY,
  book_id         TEXT NOT NULL,
  started_at      TIMESTAMPTZ NOT NULL,
  ended_at        TIMESTAMPTZ NOT NULL,
  actual_cost_usd DOUBLE PRECISION NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS runs_created_at_idx ON runs (created_at);
```

`SpendSince` already relies only on `created_at >= ?`, which the index above
serves efficiently. `Save` maps to `INSERT ... ON CONFLICT (source_hash) DO
UPDATE`, identical in intent to the SQLite upsert.

### Driver choice

- SQLite: keep `modernc.org/sqlite` (pure Go).
- PostgreSQL: `github.com/jackc/pgx/v5` via `database/sql` (or native pool).
  This is a new approved dependency to be ratified in `docs/DEPENDENCIES.md`
  when V2 work starts — not added in V1.

## Implementation Plan

1. (Now) Document parity + decision (this RFC). No code change.
2. (V2) Add `internal/tm/postgres.go` implementing `tm.Store` against pgx.
3. (V2) Add a store factory selecting backend from config
   (`TOMESHIFT_TM_DRIVER=sqlite|postgres`, `..._TM_DSN`).
4. (V2) Add a one-shot migration command exporting SQLite `segments`/`runs`
   into PostgreSQL (idempotent upserts keyed by primary keys).
5. (V2) Extend CI with a Postgres service container to run the same
   `tm.Store` conformance test suite against both backends.

## Rollout Plan

- Local stays SQLite (default); no action for existing users.
- Service mode: provision Postgres → run migration command → switch
  `TM_DRIVER=postgres`. SQLite file is retained as a backup until verified.
- Rollback: switch `TM_DRIVER` back to `sqlite`; the file is untouched by the
  export (read-only migration).

## Impact

### Technical
- Concurrency: PostgreSQL removes the single-writer constraint for workers.
- Maintainability: one extra SQL dialect, mitigated by a shared conformance
  test suite run against both backends.

### Cost
- Local: unchanged (no infra).
- Service: a managed Postgres instance; TM reuse continues to drive the primary
  cost savings (fewer paid NMT/LLM calls).

### Security/Compliance
- Source/target text persists in the central TM; apply encryption-at-rest and
  access controls per `ARCHITECTURE.md` §11 when content is sensitive.

## Fuzzy / Semantic TM Spike — Recommendation

Two candidate approaches were evaluated on a desk basis:

### Lexical fuzzy match (`pg_trgm`)
- Trigram similarity index on `source_text`; query nearest neighbors above a
  similarity threshold, then accept only if placeholder structure matches.
- Pros: simple, cheap, no embeddings/model; good for typo- and punctuation-level
  near-duplicates. Native to PostgreSQL.
- Cons: blind to paraphrase/semantic equivalence.

### Semantic match (`pgvector`)
- Store an embedding per segment; nearest-neighbor by cosine distance.
- Pros: catches paraphrases and reworded sentences.
- Cons: needs an embedding model (cost + latency + a new dependency); risk of
  false-positive reuse that silently changes meaning; threshold tuning needs a
  labeled benchmark set.

**Recommendation:** when fuzzy TM is taken on (M2+, after PostgreSQL lands),
**start with `pg_trgm` lexical fuzzy matching** gated behind a high similarity
threshold **and** an exact placeholder-structure check before reuse. It reuses
the chosen PostgreSQL backend with no new model dependency and a bounded
false-positive risk. Defer `pgvector`/semantic reuse until there is a labeled
benchmark to measure precision of semantic matches — reusing a semantically
"close" but not equivalent translation is a correctness hazard for prose, so it
must be measured, not assumed. Any fuzzy hit should also be recorded with a
lower `quality_score` than an exact hit so QA/reporting can distinguish them.

## Acceptance Criteria

- A `tm.Store` conformance suite passes identically on SQLite and PostgreSQL.
- Migration command moves all `segments`/`runs` rows with no loss and is
  re-runnable (idempotent).
- Fuzzy matching (when implemented) never reuses a candidate whose placeholder
  structure differs from the query segment.

## Risks and Mitigations

- Risk: SQL dialect drift between backends.
  - Mitigation: shared conformance test suite in CI against both.
- Risk: fuzzy reuse degrades translation correctness.
  - Mitigation: high threshold + placeholder-structure gate + lower quality
    score + deferral of semantic reuse until benchmarked.
- Risk: central TM becomes a data-sensitivity concern.
  - Mitigation: encryption at rest, access controls, retention limits (§11).

## Open Questions

- Should fuzzy hits be auto-applied or only surfaced as suggestions for human
  post-edit in early service-mode rollouts?
- What labeled benchmark do we adopt to tune semantic thresholds?

## Decision Log

- 2026-06-15: status set to `draft` (initial proposal; documents the M2 TM
  storage-evolution and fuzzy-TM-spike deliverables; no code change in V1).
