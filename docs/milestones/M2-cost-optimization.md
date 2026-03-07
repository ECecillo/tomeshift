# M2 - Cost Optimization

- Status: `planned`
- Target window: TBD
- Owner: TBD

## Goal
Reduce average translation cost and improve predictability without quality regressions.

## In Scope
- TM performance improvements.
- TM storage evolution plan for service mode.
- Advanced glossary enforcement and conflict reporting.
- Cost reporting and budget guardrails.
- Run resume and idempotency hardening.

## Out of Scope
- LLM post-edit activation by default.
- Multi-node service orchestration.

## What We Will Implement
- Better TM indexing and cache-hit observability.
- PostgreSQL TM schema proposal and migration plan from SQLite.
- Fuzzy/semantic TM feasibility spike (`pgvector` vs dedicated vector store).
- Advanced glossary import, lock levels, and conflict reporting in pipeline.
- Cost projection before run start.
- Per-book and daily spend caps with hard-stop behavior.
- Resume behavior for interrupted runs.

## Implementation Plan
1. Add glossary schema and loader.
2. Enforce glossary mappings pre/post translation.
3. Add pre-run segment count and projected-cost estimator.
4. Implement budget cap checks and abort paths.
5. Add resume checkpoints by `segment_hash`/status.
6. Extend report with estimated vs actual cost deltas.
7. Define PostgreSQL migration plan and compatibility checks.
8. Run fuzzy TM storage spike and publish recommendation.

## Deliverables
- Updated CLI/config options for budget controls.
- Enhanced report with cost KPIs.
- Regression suite covering cap-trigger and resume paths.
- TM migration decision note (SQLite -> PostgreSQL) and fuzzy-store recommendation.

## Acceptance Criteria
- Demonstrable cost reduction on repeated runs via TM hits.
- Correct hard-stop when projected/actual cost crosses cap.
- Resume completes without duplicate translated segments.

## Dependencies
- Stable pricing config per provider.
- Historical run data for comparison.

## Risks
- Incorrect cost projection assumptions.
- Glossary over-enforcement harming readability.

## Exit Decision
Milestone is complete when cost guardrails and resume flows are validated on fixture and real-book runs.
