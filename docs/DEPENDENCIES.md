# Dependency Policy

## Purpose
Define how this project selects and maintains third-party dependencies.

## Principles
- Prefer Go standard library by default.
- Add external dependencies only when they provide required capabilities not covered by stdlib, or materially reduce operational risk.
- Keep dependency count low and justify each addition.

## Approved External Dependencies (Current)
- `golang.org/x/net/html`
  - Reason: robust HTML/XHTML parsing beyond stdlib `encoding/xml`.
- `modernc.org/sqlite`
  - Reason: SQLite driver required for DB access in Go.
- `github.com/kelseyhightower/envconfig`
  - Reason: typed env-to-struct config loading with defaults and required field tags.
- `go.uber.org/zap`
  - Reason: structured, high-performance logging for production-grade runs.

- `go.temporal.io/sdk` *(approved; lands in go.mod in RFC 0009 phase 3, not yet vendored)*
  - Why stdlib is insufficient: a run is a long, multi-stage, externally
    rate-limited job that must survive worker death and provider outages without
    repaying LLM work. Durable execution with replay, per-activity timeouts and
    retries, durable timers, signals, queries and child workflows is a workflow
    engine — the stdlib offers none of it, and hand-rolling it is exactly M4's
    plan items 1 and 5.
  - Capability gained: the Go SDK for a self-hosted Temporal dev server. The run
    becomes a workflow, each stage an activity with its own retry policy, the 429
    pause a timer or signal, progress a query, and a series a parent workflow —
    all replayable and observable in the Temporal UI.
  - Maintenance and security: Temporal is actively maintained and Go is a
    first-class SDK. It pulls in a heavy transitive tree (gRPC, protobuf); it is
    confined to `internal/temporal`, the only package allowed to import it,
    enforced by a `depguard` rule in `.golangci.yml`. Local dev server only — no
    Temporal Cloud, no credentials.
  - Rollback/removal plan: the business (stages and control flow) stays
    Temporal-agnostic behind the `pipeline.Steps`/`pipeline.Runtime` ports.
    Leaving Temporal means deleting `internal/temporal` and running the flow on
    the in-process `Steps`/`Runtime` pair that ships as the portability proof.
    See RFC 0009 (`docs/rfcs/0009-temporal-execution-engine.md`).

## Observability Stack (Service Mode)
- `VictoriaMetrics` for metrics storage/query.
- `Grafana` for dashboards.
- Log backend is deployment-dependent (for example Loki/OpenSearch), while app log emission format remains structured JSON.

## Tooling (Non-Go Module Dependencies)
- `just` command runner:
  - Reason: simple, repeatable local workflows (`run`, `test`, env sourcing).
- `golangci-lint`:
  - Reason: consistent linting and static analysis in local and CI validation.
- Docker + Docker Compose:
  - Reason: reproducible local runtime and optional local DB profile for service-mode preparation.
- Temporal dev server (`temporalio/temporal`, pinned tag in `docker-compose.yml`):
  - Reason: local, single-node durable execution engine (`server start-dev`,
    embedded SQLite history, Web UI on :8233) for RFC 0009. Started by `just up`
    / `just temporal-up`. No PostgreSQL, no Temporal Cloud.
- GitHub Actions:
  - Reason: automated lint/test/format validation on pull requests and main branch updates.

## Adding a New Dependency
Required in the RFC/PR:
1. Why stdlib is insufficient.
2. What capability is gained.
3. Maintenance and security considerations.
4. Rollback/removal plan if dependency becomes a risk.

## Constraints
- Pin versions in `go.mod`.
- Prefer actively maintained packages.
- Avoid overlapping libraries for the same purpose.
