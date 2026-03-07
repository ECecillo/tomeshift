# M3 - Quality Optimization

- Status: `planned`
- Target window: TBD
- Owner: TBD

## Goal
Improve translation fluency and consistency while keeping cost bounded.

## In Scope
- Selective LLM post-edit path.
- Confidence/heuristic-based escalation.
- Expanded QA consistency checks.

## Out of Scope
- Full-book LLM rewriting.
- Human review UI.

## What We Will Implement
- Escalation classifier/rules for difficult segments.
- LLM post-edit adapter with strict placeholder-preservation contract.
- LLM rate limits and budget caps.
- Additional QA checks for consistency and leftover source language.

## Implementation Plan
1. Define escalation heuristics and thresholds in config.
2. Implement router branch for selective LLM post-edit.
3. Add strict response schema validation.
4. Add failure fallback from LLM to NMT output.
5. Add quality metrics to run reports.
6. Tune thresholds using benchmark fixture books.

## Deliverables
- Configurable selective LLM path.
- Quality/cost tradeoff dashboard fields in report output.
- Evaluation notes with threshold recommendations.

## Acceptance Criteria
- Quality metrics improve versus M2 baseline.
- LLM escalation ratio remains within configured cap.
- No regression in EPUB structural validation.

## Dependencies
- LLM provider credentials and quotas.
- Benchmark dataset and evaluation rubric.

## Risks
- Over-escalation increasing cost.
- Schema drift in provider responses.

## Exit Decision
Milestone is complete when quality improves with bounded cost increase on benchmark runs.
