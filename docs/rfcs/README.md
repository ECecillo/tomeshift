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
