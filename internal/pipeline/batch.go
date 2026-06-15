package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const batchReportFileName = "batch-report.json"

// BookOutcome is the per-book result within a batch run.
type BookOutcome struct {
	BookID string     `json:"book_id"`
	RunID  string     `json:"run_id"`
	OK     bool       `json:"ok"`
	Error  string     `json:"error,omitempty"`
	Result *RunResult `json:"result,omitempty"`
	// ChrF is the reference-based score when a reference was supplied; nil
	// otherwise.
	ChrF *float64 `json:"chrf,omitempty"`
}

// BatchSummary aggregates a multi-book run for the series-level report.
type BatchSummary struct {
	SchemaVersion      int           `json:"schema_version"`
	Books              int           `json:"books"`
	Succeeded          int           `json:"succeeded"`
	Failed             int           `json:"failed"`
	TotalSegments      int           `json:"total_segments"`
	TMHits             int           `json:"tm_hits"`
	NMTSegments        int           `json:"nmt_segments"`
	LLMSegments        int           `json:"llm_segments"`
	TMHitRatio         float64       `json:"tm_hit_ratio"`
	TotalActualCostUSD float64       `json:"total_actual_cost_usd"`
	TotalSavingsUSD    float64       `json:"total_savings_usd"`
	EvaluatedBooks     int           `json:"evaluated_books"`
	AvgChrF            float64       `json:"avg_chrf"`
	Outcomes           []BookOutcome `json:"outcomes"`
}

// NewBatchSummary aggregates per-book outcomes into a series summary.
func NewBatchSummary(outcomes []BookOutcome) BatchSummary {
	s := BatchSummary{SchemaVersion: 1, Books: len(outcomes), Outcomes: outcomes}
	for i := range outcomes {
		o := &outcomes[i]
		if o.OK {
			s.Succeeded++
		} else {
			s.Failed++
		}
		if o.Result == nil {
			continue
		}
		s.TotalSegments += o.Result.TotalSegments
		s.TMHits += o.Result.TMHits
		s.NMTSegments += o.Result.NMTSegments
		s.LLMSegments += o.Result.LLMSegments
		s.TotalActualCostUSD += o.Result.ActualCostUSD
		s.TotalSavingsUSD += o.Result.CostSavingsUSD
		if o.ChrF != nil {
			s.EvaluatedBooks++
			s.AvgChrF += *o.ChrF
		}
	}
	if s.TotalSegments > 0 {
		s.TMHitRatio = float64(s.TMHits) / float64(s.TotalSegments)
	}
	if s.EvaluatedBooks > 0 {
		s.AvgChrF /= float64(s.EvaluatedBooks)
	}
	return s
}

// WriteBatchReport writes the series summary to batch-report.json in dir.
func WriteBatchReport(dir string, summary BatchSummary) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("unable to create batch output dir %q, err: %w", dir, err)
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("unable to marshal batch report, err: %w", err)
	}
	path := filepath.Join(dir, batchReportFileName)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("unable to write batch report %q, err: %w", path, err)
	}
	return nil
}
