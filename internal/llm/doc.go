// Package llm provides the selective LLM post-edit adapter used in M3.
//
// Post-editing improves fluency/style of an NMT draft while preserving meaning
// and placeholder tokens. The adapter enforces a strict JSON response contract
// and falls back to the NMT draft when the model drops placeholder tokens, so a
// post-edit can never produce broken markup. It is disabled by default in V1
// and is wired into routing only once escalation thresholds are tuned.
package llm
