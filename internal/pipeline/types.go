package pipeline

import (
	"time"

	"github.com/ECecillo/tomeshift/internal/qa"
)

const (
	ModeNMT      = "nmt"
	ModeLiterary = "literary"
)

// RunConfig is the input contract for a single translation run.
type RunConfig struct {
	InputPath  string
	OutputDir  string // run-specific output directory (artifacts are written here)
	SourceLang string
	TargetLang string
	RunID      string
	BookID     string
	Mode       string
	SeriesID   string
	// UpdateBible lets literary mode evolve the bible during the run. Off by
	// default: the bible is frozen so prompts/cache stay stable and re-runs are
	// reproducible.
	UpdateBible bool
	// Resume requests continuation using the persistent TM cache; already
	// translated segments become TM hits rather than new provider calls.
	Resume bool
}

// RunResult is the machine-readable summary emitted as report.json.
type RunResult struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	BookID        string    `json:"book_id"`
	Mode          string    `json:"mode"`
	SeriesID      string    `json:"series_id,omitempty"`
	BibleVersion  int       `json:"bible_version,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	TotalSegments int       `json:"total_segments"`
	TMHits        int       `json:"tm_hits"`
	TMHitRatio    float64   `json:"tm_hit_ratio"`
	NMTSegments   int       `json:"nmt_segments"`
	NMTEngine     string    `json:"nmt_engine,omitempty"` // provider the chars below were sent to
	NMTChars      int       `json:"nmt_chars"`            // source chars sent to it (free-tier accounting)
	LLMAttempts   int       `json:"llm_attempts"`         // post-edit calls made, usable or not — this is what was billed
	LLMChars      int       `json:"llm_chars"`            // source chars those calls carried
	LLMSegments   int       `json:"llm_segments"`
	// LLMPassSegments is how many segments the LLM pass carried, usable result
	// or not — unlike LLMSegments, which counts the ones it improved. It is the
	// denominator that pairs with the token counters below, and the one the
	// ledger keeps: dividing a degraded batch's tokens by the segments it did
	// not deliver would report a model as more verbose the worse it behaved.
	LLMPassSegments        int     `json:"llm_pass_segments,omitempty"`
	LLMProvider            string  `json:"llm_provider,omitempty"`
	LLMModel               string  `json:"llm_model,omitempty"`
	LLMEscalationRatio     float64 `json:"llm_escalation_ratio"`
	LLMInputTokens         int     `json:"llm_input_tokens"`
	LLMOutputTokens        int     `json:"llm_output_tokens"`
	LLMCacheReadTokens     int     `json:"llm_cache_read_tokens"`
	LLMCacheCreationTokens int     `json:"llm_cache_creation_tokens"`
	GlossaryViolations     int     `json:"glossary_violations"`
	AvgQualityScore        float64 `json:"avg_quality_score"` // mean COMET-QE over scored segments (0 if disabled)
	QualityScoredSegments  int     `json:"quality_scored_segments"`
	// EstimatedCostUSD is the whole book priced with NO translation-memory
	// reuse. It is deliberately not the figure the per-book cap is enforced on —
	// that one covers only the segments this run still has to translate — because
	// this is the baseline CostSavingsUSD subtracts from. The two coincide on a
	// fresh book and diverge on a resumed one.
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	ActualCostUSD    float64   `json:"actual_cost_usd"`
	CostSavingsUSD   float64   `json:"cost_savings_usd"` // estimated (no-reuse) minus actual = TM savings
	OutputEPUB       string    `json:"output_epub"`
	QA               qa.Report `json:"qa"`
}
