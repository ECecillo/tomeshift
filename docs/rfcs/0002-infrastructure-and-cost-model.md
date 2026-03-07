# RFC 0002: Infrastructure and Cost Model

- Status: `proposed`
- Authors: ECecillo, Codex
- Created: 2026-03-07
- Updated: 2026-03-07
- Related:
  - Architecture: `docs/ARCHITECTURE.md`
  - Milestones: `docs/milestones/`
  - Supersedes: n/a
  - Superseded by: n/a

## Context
We need a practical infrastructure path from local CLI (V1) to service mode (V2+) and clear budgeting behavior.

Implementation policy:
- Go stdlib-first for core app code.
- External packages/tools are introduced only for requirements not covered by stdlib (for example SQL drivers, metrics backend, or approved structured logging via `go.uber.org/zap`).

## Runtime Topologies
- Local-only CLI (V1): SQLite + local artifacts.
- Single-server service (early V2): API + worker + PostgreSQL + object storage.
- Split service (scale): API nodes + worker pool + managed queue + centralized observability.

## LLM/NMT Execution Options
Local inference:
- Predictable fixed infra spend.
- Better privacy.
- Higher ops/setup burden.

API inference:
- Lower ops burden.
- Variable spend tied to usage.
- Stronger need for spend controls and provider fallbacks.

Planning formula:
- `total_cost ~= (nmt_unit_cost * nmt_segments) + (llm_unit_cost * llm_segments) + infra_prorated_cost`

## Translation Memory (TM) Storage Options and Tradeoffs
### Option A: SQLite (embedded relational DB)
- Pros:
  - Zero-ops and fast local setup.
  - Good fit for single-user CLI workflow.
  - Strong enough for exact-hash TM lookups in V1.
- Cons:
  - Limited multi-writer concurrency.
  - Less suitable for shared service workloads.
- Best fit:
  - V1 local CLI.

### Option B: PostgreSQL (managed or self-hosted)
- Pros:
  - Strong consistency and mature indexing.
  - Good operational fit for multi-user/service mode.
  - Supports richer reporting and schema evolution.
- Cons:
  - Higher ops complexity and cost than SQLite.
- Best fit:
  - V2+ central TM and run metadata store.

### Option C: MySQL/MariaDB
- Pros:
  - Mature ecosystem and hosting availability.
  - Predictable performance for standard relational workloads.
- Cons:
  - Less flexibility than PostgreSQL for advanced text/fuzzy extensions.
- Best fit:
  - Teams with existing MySQL platform standards.

### Option D: Redis (cache layer)
- Pros:
  - Very low-latency lookups for hot segments.
  - Useful for reducing DB load at scale.
- Cons:
  - Not ideal as sole source of truth for TM history.
  - Memory-heavy; persistence/backup needs explicit design.
- Best fit:
  - Optional cache in front of PostgreSQL in V2+.

### Option E: DuckDB (embedded analytics DB)
- Pros:
  - Strong local analytics for run/cost reporting.
  - Simple embedded usage.
- Cons:
  - Not a strong default for concurrent service writes.
- Best fit:
  - Offline analysis workflows, not primary TM store.

### Option F: Embedded KV (Badger/Bbolt/Pebble)
- Pros:
  - Very fast exact-hash key lookups.
  - Small runtime footprint.
- Cons:
  - Weak query flexibility and reporting ergonomics.
  - More custom tooling burden for migrations/inspection.
- Best fit:
  - Specialized exact-match-only engines.

### Option G: Vector/Search store (`pgvector`, Qdrant, OpenSearch)
- Pros:
  - Enables fuzzy/semantic TM reuse.
  - Useful for near-duplicate recovery in later phases.
- Cons:
  - Additional infra and relevance tuning complexity.
  - Should not replace transactional run-state DB.
- Best fit:
  - Phase 2+ fuzzy matching alongside PostgreSQL.

## Recommended TM Storage Path
1. V1: SQLite as default TM store.
2. V2: Migrate TM + run metadata to PostgreSQL.
3. V2 scale: add Redis hot-cache if lookup latency becomes bottleneck.
4. Phase 2+/3: add `pgvector` (or external vector DB) only when fuzzy/semantic match is enabled.

## Indicative Monthly Cost Envelopes (Planning)
Notes:
- Planning ranges only; not provider quotes.
- Actual values depend on book volume, TM hit ratio, segment length, and escalation ratio.

Local-first V1 (no LLM post-edit):
- `~$0-$30/month` incremental infra.

API-first V1 (NMT API, no LLM post-edit):
- Small usage: `~$20-$150/month`
- Moderate usage: `~$150-$600/month`

Hybrid Phase 3 (NMT + selective LLM):
- Small usage: `~$100-$500/month`
- Moderate usage: `~$500-$2,000+/month`

Single-server V2 baseline (infra only):
- `~$80-$300/month`

## Monitoring and Alerting Baseline
Metrics:
- `translation_segments_total`
- `translation_cache_hit_ratio`
- `translation_llm_escalation_ratio`
- `translation_cost_usd_total`
- `epub_validation_failures_total`
- `translation_provider_errors_total`
- `translation_run_duration_seconds`

Alerts:
- QA failure spike
- Provider error spike
- Cost/day budget breach
- Queue lag/SLA breach (V2+)

Suggested stack:
- Metrics: VictoriaMetrics + Grafana (or Prometheus-compatible equivalent).
- Logs: structured stdout/ndjson by default; centralized backend (Loki/ELK/OpenSearch) in service mode.
- App logger: `go.uber.org/zap` for structured application logging.
- Optional traces: OpenTelemetry.

## Guardrails by Environment
- Local dev: `llm_enabled=false`, strict request caps.
- Staging: low budget, fixture books only.
- Production: per-book + daily + monthly hard caps with auto-abort.

## Open Questions
- Managed vs self-hosted observability for early V2?
- PostgreSQL managed service tier and backup RPO/RTO targets?
- Trigger threshold for adding Redis cache (latency/qps/error budget)?
- Whether to use `pgvector` in PostgreSQL or a dedicated vector store for fuzzy TM?
- Default retention period for raw EPUB artifacts?

## Decision Log
- 2026-03-07: status set to `draft` (initial proposal).
