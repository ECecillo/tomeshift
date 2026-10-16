package pipeline_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/pipeline"
	"github.com/ECecillo/tomeshift/internal/segment"
)

func TestWorkspace_ChapterRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		units []segment.Unit
	}{
		{name: "empty chapter", units: nil},
		{
			name: "units with placeholder tokens survive verbatim",
			units: []segment.Unit{
				{
					ID: "ch_0001_p_0001", ChapterID: "ch_0001", NodePath: "/html[1]/body[1]/p[1]",
					SourceText: `A <x id="1"/>bold<x id="2"/> claim.`, NormalizedText: "a bold claim.",
					Hash: "deadbeef", Placeholders: []string{`<x id="1"/>`, `<x id="2"/>`},
					TargetText: `Une <x id="1"/>affirmation<x id="2"/>.`, Status: segment.StatusPostEdited,
				},
				{
					ID: "ch_0001_p_0002", ChapterID: "ch_0001", NodePath: "/html[1]/body[1]/p[2]",
					SourceText: "Plain.", NormalizedText: "plain.", Hash: "cafe",
					TargetText: "Simple.", Status: segment.StatusReused,
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ws := pipeline.NewWorkspace(t.TempDir())
			require.NoError(t, ws.WriteChapter("ch_0001", tt.units))

			got, err := ws.ReadChapter("ch_0001")
			require.NoError(t, err)
			require.Equal(t, tt.units, got)
		})
	}
}

func TestWorkspace_ReadCorruptLineErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ws := pipeline.NewWorkspace(dir)
	require.NoError(t, ws.WriteChapter("ch_0001", []segment.Unit{{ID: "s1"}}))

	// A half-written unit file must not be silently half-applied.
	path := filepath.Join(dir, "work", "ch_0001.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{not json}\n"), 0o644))

	_, err := ws.ReadChapter("ch_0001")
	require.Error(t, err)
}

func TestWorkspace_WriteIsAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ws := pipeline.NewWorkspace(dir)

	require.NoError(t, ws.WriteChapter("ch_0001", []segment.Unit{{ID: "old", SourceText: "old"}}))
	require.NoError(t, ws.WriteChapter("ch_0001", []segment.Unit{{ID: "new", SourceText: "new"}}))

	// The second write replaces the first wholesale, and leaves no temp file
	// behind in the work dir.
	got, err := ws.ReadChapter("ch_0001")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "new", got[0].ID)

	entries, err := os.ReadDir(filepath.Join(dir, "work"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp file left behind")
	require.Equal(t, "ch_0001.jsonl", entries[0].Name())
}
