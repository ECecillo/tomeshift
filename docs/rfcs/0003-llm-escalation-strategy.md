# RFC 0003: LLM Escalation Strategy

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
LLM post-edit is planned for Phase 3. This RFC will define when and how segments are escalated from NMT to LLM.

## Scope
- Escalation triggers and thresholds.
- Response schema contract and validation.
- Cost and rate-limit guardrails.
- Fallback behavior on LLM failure.

## Initial Direction
- Keep LLM escalation ratio configurable and capped.
- Preserve placeholders exactly.
- Require structured output for deterministic pipeline behavior.

## Open Questions
- Which heuristics should be weighted most (idioms, entity density, length, confidence)?
- How to evaluate quality improvement against baseline NMT?
- Should escalation be model-specific or provider-agnostic?

## Decision Log
- 2026-03-07: status set to `draft` (initial proposal).
