package pipeline

import (
	"time"

	"github.com/ECecillo/ebooks-translator/internal/qa"
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
	SchemaVersion          int       `json:"schema_version"`
	RunID                  string    `json:"run_id"`
	BookID                 string    `json:"book_id"`
	Mode                   string    `json:"mode"`
	SeriesID               string    `json:"series_id,omitempty"`
	BibleVersion           int       `json:"bible_version,omitempty"`
	StartedAt              time.Time `json:"started_at"`
	EndedAt                time.Time `json:"ended_at"`
	TotalSegments          int       `json:"total_segments"`
	TMHits                 int       `json:"tm_hits"`
	TMHitRatio             float64   `json:"tm_hit_ratio"`
	NMTSegments            int       `json:"nmt_segments"`
	LLMSegments            int       `json:"llm_segments"`
	LLMEscalationRatio     float64   `json:"llm_escalation_ratio"`
	LLMInputTokens         int       `json:"llm_input_tokens"`
	LLMOutputTokens        int       `json:"llm_output_tokens"`
	LLMCacheReadTokens     int       `json:"llm_cache_read_tokens"`
	LLMCacheCreationTokens int       `json:"llm_cache_creation_tokens"`
	GlossaryViolations     int       `json:"glossary_violations"`
	AvgQualityScore        float64   `json:"avg_quality_score"` // mean COMET-QE over scored segments (0 if disabled)
	QualityScoredSegments  int       `json:"quality_scored_segments"`
	EstimatedCostUSD       float64   `json:"estimated_cost_usd"`
	ActualCostUSD          float64   `json:"actual_cost_usd"`
	CostSavingsUSD         float64   `json:"cost_savings_usd"` // estimated (no-reuse) minus actual = TM savings
	OutputEPUB             string    `json:"output_epub"`
	QA                     qa.Report `json:"qa"`
}
