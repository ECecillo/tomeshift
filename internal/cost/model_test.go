package cost

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/segment"
)

func TestModel_ZeroIsFree(t *testing.T) {
	t.Parallel()

	var m Model
	require.True(t, m.IsZero())

	proj := m.Project([]segment.Segment{{SourceText: "hello"}, {SourceText: "world"}})
	require.Equal(t, 2, proj.Segments)
	require.Equal(t, 10, proj.Chars)
	require.InDelta(t, 0.0, proj.EstimatedUSD, 0.0001)
	require.InDelta(t, 0.0, m.Actual(2, 10, 0), 0.0001)
}

func TestModel_Project(t *testing.T) {
	t.Parallel()

	m := Model{PerSegmentUSD: 0.01, Per1000CharsUSD: 2.0}

	// 2 segments, 10 chars total: 2*0.01 + (10/1000)*2.0 = 0.02 + 0.02 = 0.04
	proj := m.Project([]segment.Segment{{SourceText: "hello"}, {SourceText: "world"}})
	require.InDelta(t, 0.04, proj.EstimatedUSD, 0.0001)
}

func TestModel_Actual(t *testing.T) {
	t.Parallel()

	m := Model{PerSegmentUSD: 0.01, Per1000CharsUSD: 2.0, LLMPerSegmentUSD: 0.05}
	require.InDelta(t, 0.04, m.Actual(2, 10, 0), 0.0001)
	require.InDelta(t, 0.0, m.Actual(0, 0, 0), 0.0001)
	// 2 NMT segments + 1 LLM post-edit: 0.04 + 0.05 = 0.09
	require.InDelta(t, 0.09, m.Actual(2, 10, 1), 0.0001)
}

func TestModel_CountsRunesNotBytes(t *testing.T) {
	t.Parallel()

	var m Model
	// "café" is 4 runes but 5 bytes.
	proj := m.Project([]segment.Segment{{SourceText: "café"}})
	require.Equal(t, 4, proj.Chars)
}
