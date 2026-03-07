# M4 - Production Hardening

- Status: `planned`
- Target window: TBD
- Owner: TBD

## Goal
Operate the translator reliably in multi-book workloads with observability and recovery capabilities.

## In Scope
- Service mode with queue-based workers.
- Centralized database and artifact storage.
- Monitoring dashboards and alerting.
- Failure recovery and resumable runs at scale.

## Out of Scope
- Full multi-tenant SaaS feature set beyond required auth/access controls.

## What We Will Implement
- API + worker separation.
- Job queue with retry and dead-letter handling.
- PostgreSQL migration for TM/run metadata.
- Docker Compose profile for local V2 stack simulation (API/worker + PostgreSQL).
- Optional Redis cache layer for hot TM lookup path when needed.
- Optional `pgvector` integration if fuzzy TM is enabled by policy.
- Object storage integration for EPUB artifacts.
- Metrics/log pipelines and alert rules.

## Implementation Plan
1. Introduce queue contract and worker runtime.
2. Move persistence from SQLite to PostgreSQL.
3. Add Docker Compose profile for local V2 stack verification.
4. Add artifact storage abstraction and remote backend.
5. Add distributed idempotency and retry controls.
6. Benchmark TM lookup latency and add Redis if threshold breached.
7. Integrate vector-backed fuzzy TM path only if approved from M2 spike.
8. Deploy baseline dashboards and alerts.
9. Run load and failure-injection tests.

## Deliverables
- Deployable service topology.
- Ops runbook and incident playbooks.
- Load test and reliability report.

## Acceptance Criteria
- Stable multi-book throughput within defined SLA.
- Automatic recovery for transient failures.
- Alerting coverage for provider, queue, and QA failure modes.
- TM lookup latency remains within target SLO under load.

## Dependencies
- Cloud environment and IAM setup.
- Managed DB/storage/monitoring choices.

## Risks
- Queue backlog under burst traffic.
- Data consistency issues during retries.

## Exit Decision
Milestone is complete when reliability, throughput, and observability targets are met in staging and production pilot.
