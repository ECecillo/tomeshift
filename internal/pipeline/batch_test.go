package pipeline_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/pipeline"
)

func TestNewBatchSummary(t *testing.T) {
	t.Parallel()

	outcomes := []pipeline.BookOutcome{
		{BookID: "a", OK: true, Result: &pipeline.RunResult{TotalSegments: 10, TMHits: 2, NMTSegments: 8, LLMSegments: 1, ActualCostUSD: 0.10, CostSavingsUSD: 0.05}},
		{BookID: "b", OK: true, Result: &pipeline.RunResult{TotalSegments: 6, TMHits: 6, NMTSegments: 0, ActualCostUSD: 0, CostSavingsUSD: 0.20}},
		{BookID: "c", OK: false, Error: "boom"},
	}

	s := pipeline.NewBatchSummary(outcomes)
	require.Equal(t, 3, s.Books)
	require.Equal(t, 2, s.Succeeded)
	require.Equal(t, 1, s.Failed)
	require.Equal(t, 16, s.TotalSegments)
	require.Equal(t, 8, s.TMHits)
	require.Equal(t, 8, s.NMTSegments)
	require.Equal(t, 1, s.LLMSegments)
	require.InDelta(t, 0.5, s.TMHitRatio, 0.0001)
	require.InDelta(t, 0.10, s.TotalActualCostUSD, 0.0001)
	require.InDelta(t, 0.25, s.TotalSavingsUSD, 0.0001)
}

func TestWriteBatchReport(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	summary := pipeline.NewBatchSummary([]pipeline.BookOutcome{{BookID: "a", OK: true, Result: &pipeline.RunResult{TotalSegments: 3}}})
	require.NoError(t, pipeline.WriteBatchReport(dir, summary))

	data, err := os.ReadFile(filepath.Join(dir, "batch-report.json"))
	require.NoError(t, err)
	var got pipeline.BatchSummary
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, 1, got.Books)
	require.Equal(t, 3, got.TotalSegments)
}
