# RFC Index

RFCs are proposal documents used to discuss options and implementation details before they become accepted architecture decisions.

## Workflow
- `draft`: initial write-up, incomplete, not ready for decision.
- `proposed`: ready for review and decision meeting.
- `accepted`: approved; decisions should be reflected in `docs/ARCHITECTURE.md` and milestone plans.
- `rejected`: not approved; document kept for historical context.
- `superseded`: replaced by a newer RFC.

## Process
1. Create a new RFC from `docs/rfcs/TEMPLATE.md`.
2. Set status to `draft` and capture open questions.
3. Move to `proposed` when the tradeoffs and recommendation are clear.
4. Record decision and set `accepted` / `rejected` / `superseded`.
5. If `accepted`, update architecture/milestones and link back to this RFC.

- [0001 - V1 Implementation Plan and Delivery Contract](./0001-v1-implementation-plan.md)
- [0002 - Infrastructure and Cost Model](./0002-infrastructure-and-cost-model.md)
- [0003 - LLM Escalation Strategy](./0003-llm-escalation-strategy.md)
- [0004 - EPUB Parse/Segment/Recompose Pipeline](./0004-epub-pipeline.md)
- [0005 - TM Storage Evolution (SQLite → PostgreSQL) and Fuzzy TM Spike](./0005-tm-storage-evolution.md)
- [0006 - Faithful Multi-Volume Literary Translation via Persistent Narrative Memory](./0006-faithful-multivolume-translation.md)
- [0007 - Google Cloud Translation as the Draft Engine, and a Token-Based Budget Guardrail](./0007-google-translate-draft-and-token-cost-model.md)
- [0008 - Pre-Generated Series Bible as a Job Separate from Translation](./0008-pre-generated-series-bible.md)
