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
