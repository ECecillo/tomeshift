// Package cost provides a configurable translation cost model used for pre-run
// projection and budget guardrails.
//
// V1's default NMT path is typically local/cheap, so the default model is zero
// cost. When provider rates are configured, the pipeline projects spend before
// translating and hard-stops if the projection exceeds the per-book cap.
package cost
