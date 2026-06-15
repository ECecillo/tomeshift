# M1 - Core MVP

- Status: `done` (real end-to-end run validated by epubcheck v5.1.0: 0 errors / 0 warnings, 2026-06-15)
- Target window: End of march 2026
- Owner: ECecillo

## Goal
Deliver a reliable end-to-end CLI pipeline that translates EPUB EN->FR with valid EPUB output and deterministic run artifacts.

## In Scope
- EPUB parse and spine traversal.
- Paragraph-level segmentation.
- Exact-match TM cache in SQLite.
- Default NMT translation path.
- Minimal glossary enforcement for locked proper nouns.
- EPUB rebuild and validation gates.
- Run report artifacts.
- CI baseline for lint/test/format checks.
- Docker local setup for app runtime.

## Out of Scope
- LLM post-editing.
- Fuzzy TM matching.
- Confidence-based routing.
- Service mode and queue workers.

## What We Will Implement
- CLI command `epub-translator run` with stdlib `flag` in V1.
- Typed config package using `github.com/kelseyhightower/envconfig` with defaults/required tags.
- `internal/epub` for extraction, manifest/spine parsing, and recomposition.
- `internal/segment` for paragraph extraction, normalization, and hashing.
- `internal/tm` for SQLite exact lookup/store.
- `internal/translate` interface and one default HTTP NMT provider adapter.
- Test-only NMT mock server for deterministic integration tests.
- `internal/qa` checks: placeholders, spine integrity, `epubcheck` integration.
- Run outputs: `translated.epub`, `report.json`, `segments.jsonl`, `logs.ndjson`.
- Structured logging with `go.uber.org/zap`.
- Root `Justfile` for run/test command orchestration.
- GitHub Actions CI workflow and pull request template.
- Docker image definition and compose-based local workflow.

## Implementation Plan
1. Bootstrap Go module, config loader, and CLI entrypoint.
2. Implement env-backed config structs with `envconfig` tags and validation.
3. Implement EPUB unzip + OPF/container parsing.
4. Implement XHTML paragraph extraction with stable `node_path` IDs.
5. Add placeholder tokenization/restoration.
6. Add NMT adapter and pipeline integration.
7. Add minimal locked-term glossary checks (exact proper noun mapping).
8. Add SQLite TM lookup/write path.
9. Implement recomposition and packaging.
10. Enforce QA gates and exit codes.
11. Emit run artifacts and structured logs.
12. Add test-only NMT mock server and wire integration tests against it.
13. Add fixture integration tests.
14. Document TM storage decision record (SQLite for V1, PostgreSQL target for V2).
15. Add `Justfile` recipes and `.env` sourcing conventions for local workflows.
16. Add CI workflow (`fmt`, `lint`, `test`) and PR template.
17. Add Dockerfile and compose definitions for local app execution.

## Deliverables
- Runnable CLI for local translation runs.
- Passing integration tests on at least 3 fixture EPUBs.
- Documented config schema and operational runbook.
- Documented TM storage baseline and migration intent.
- Root `Justfile` and `.env.example` for reproducible local workflows.
- Deterministic integration test mode backed by NMT mock server.
- CI workflow and PR template active in repository.
- Docker local workflow documented and runnable.

## Acceptance Criteria
- `epubcheck` passes for all fixture outputs.
- Placeholder restoration ratio `>= 99.5%`.
- Missing spine docs `= 0`.
- Untranslated segment ratio `<= 0.5%` (excluding configured no-translate blocks).
- Locked glossary term compliance `= 100%`.
- Pull requests use template and CI status checks for validation.

## Dependencies
- NMT HTTP endpoint availability.
- Java runtime for `epubcheck`.
- Representative fixture EPUBs.
- Docker runtime available for container-based local setup.

## Risks
- XHTML edge cases breaking placeholder restore.
- Unexpected NMT endpoint latency/errors.

## Exit Decision
Milestone is complete when all acceptance criteria pass on CI and one real book run succeeds locally.
