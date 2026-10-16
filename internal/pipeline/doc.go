// Package pipeline orchestrates the EPUB translation workflow behind two small
// ports. The work — extract, parse, segment, project, translate, recompose,
// QA, artifacts — lives in Stages methods (the activity bodies); the control
// flow — accounting, the budget guardrail, cleanup — lives in RunBook and
// RunSeries, written once against the Steps and Runtime ports. The in-process
// implementations of those ports run the CLI; a Temporal implementation will
// run the same flow unchanged.
//
// Content never crosses a port: a chapter's segments are exchanged as units in
// the workspace (units.jsonl per chapter), and re-derived deterministically —
// ids by position, hash by normalised text — whenever a stage needs the live
// node tree. Individual processing responsibilities stay in their dedicated
// packages (epub, segment, tm, translate, literary, qa, cost, narrative).
package pipeline
