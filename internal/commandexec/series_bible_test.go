package commandexec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/narrative"
)

func TestDiscoverVolumesSortsByPublicationNumber(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"volume-10.epub", "volume-2.epub", "volume-1.epub"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}

	volumes, err := discoverVolumes(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"volume-1", "volume-2", "volume-10"}, []string{
		volumes[0].ID, volumes[1].ID, volumes[2].ID,
	})
}

func TestValidateSynopsisSetRequiresExactOrderedVolumes(t *testing.T) {
	t.Parallel()

	bible := narrative.Bible{
		SeriesID:   "series-a@volume-02",
		StyleGuide: "style",
		Summaries: []narrative.Summary{
			{VolumeID: "volume-01", Summary: "one"},
			{VolumeID: "volume-10", Summary: "future event"},
		},
	}
	err := validateSynopsisSet(bible, []volumeSource{{ID: "volume-01"}, {ID: "volume-02"}})
	require.ErrorIs(t, err, narrative.ErrInvalidBible)
}

func TestDiscoverVolumesRejectsAmbiguousNames(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"missing number":   {"volume.epub"},
		"mixed prefixes":   {"book-1.epub", "volume-2.epub"},
		"duplicate number": {"volume-1.epub", "volume-01.epub"},
	}
	for name, filenames := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, filename := range filenames {
				require.NoError(t, os.WriteFile(filepath.Join(dir, filename), nil, 0o600))
			}
			_, err := discoverVolumes(dir)
			require.Error(t, err)
		})
	}
}
