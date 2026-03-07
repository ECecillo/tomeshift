# RFC 0001: V1 Implementation Plan and Delivery Contract

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
The architecture is defined in `docs/ARCHITECTURE.md`. This RFC defines how V1 will be delivered and validated.

## Proposed Scope (V1)
- Translation path: `TM exact match -> NMT -> EPUB rebuild`.
- LLM post-edit path disabled.
- Paragraph-level segmentation.
- Minimal glossary enforcement in V1 for locked proper nouns.
- Implementation policy: Go stdlib-first (`flag`, stdlib IO/HTTP); approved exceptions include `go.uber.org/zap` for logging and required DB/parser dependencies.
- Configuration policy: `envconfig` loads typed config from env; `.env` overrides are applied by sourcing env files in shell before `just`/CLI commands.

## CLI and Run Contract
Command:
```bash
epub-translator run \
  --input ./books/source.epub \
  --output-dir ./runs \
  --source-lang en \
  --target-lang fr
```

Required flags:
- `--input`
- `--output-dir`
- `--source-lang` (`en` in V1)
- `--target-lang` (`fr` in V1)

Optional flags:
- `--glossary`
- `--run-id`
- `--resume`

Environment loading model:
- Source environment file before running:
```bash
set -a && source .env.local && set +a
just run ./books/source.epub ./runs
```
- CLI flags remain explicit inputs for run-specific paths/IDs; provider/runtime settings come from env-backed config structs.
- `.env.example` defines baseline variables and naming conventions.
- `Justfile` recipes are the default entrypoint for local run/test flows.
- `.env` files are trusted local files and must only contain `KEY=VALUE` assignments.

Expected run artifacts:
- `<output-dir>/<run-id>/translated.epub`
- `<output-dir>/<run-id>/report.json`
- `<output-dir>/<run-id>/segments.jsonl`
- `<output-dir>/<run-id>/logs.ndjson`

Exit codes:
- `0` success
- `2` invalid input/config
- `3` parse/segmentation failure
- `4` translation provider failure
- `5` QA gate failure
- `6` EPUB rebuild/validation failure

## Milestone Mapping
- M1: core end-to-end pipeline and QA gate.
- M2: cost optimization and resume behavior.
- M3: quality optimization and selective LLM routing.
- M4: production hardening and service-mode reliability.

Source of truth for milestone tasks:
- `docs/milestones/README.md`
- `docs/milestones/M1-core-mvp.md`
- `docs/milestones/M2-cost-optimization.md`
- `docs/milestones/M3-quality-optimization.md`
- `docs/milestones/M4-production-hardening.md`

## Acceptance Criteria for V1
- `epubcheck` passes for fixture outputs.
- Placeholder restoration ratio `>= 99.5%`.
- Missing spine docs `= 0`.
- Untranslated segment ratio `<= 0.5%` (excluding no-translate blocks).
- Locked glossary term compliance `= 100%`.

## Open Questions
- Should `--resume` be enabled at first release or in M2?
- What minimum fixture corpus size should gate release?

## Fixed Decisions
- V1 config source is env-only via `envconfig` (no runtime YAML config file).
- V1 default NMT integration is HTTP adapter (`EPUB_TRANSLATOR_NMT_PROVIDER=http`, endpoint from env).
- V1 test strategy includes a deterministic NMT mock server for integration tests.
- V1 includes baseline GitHub Actions CI (`fmt`, `lint`, `test`) and a pull request template.
- V1 includes Docker local workflow (app container).

## Decision Log
- 2026-03-07: status set to `draft` (initial proposal).
