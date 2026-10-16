package commandexec

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/narrative"
)

// summaryVolumeIDs lists a bible's volume synopses in order.
func summaryVolumeIDs(summaries []narrative.Summary) []string {
	ids := make([]string, len(summaries))
	for i, s := range summaries {
		ids[i] = s.VolumeID
	}
	return ids
}

func summaryFor(b narrative.Bible, volumeID string) string {
	for _, s := range b.Summaries {
		if s.VolumeID == volumeID {
			return s.Summary
		}
	}
	return ""
}

func seededStore(t *testing.T, summaries []narrative.Summary) *narrative.SQLiteStore {
	t.Helper()
	store, err := narrative.NewSQLiteStore(filepath.Join(t.TempDir(), "narrative.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Save(t.Context(), narrative.Bible{
		SeriesID:   "lumen",
		StyleGuide: "style contemporain",
		Characters: []narrative.CharacterVoice{{Name: "Kai", Register: "familier"}},
		Summaries:  summaries,
	}))
	return store
}

// TestAppendVolumeSynopses_AddsWithoutRebuilding is the --from-volume contract:
// new volume synopses extend STORY_SO_FAR while the earlier ones, the curated
// style guide and the character voices stay exactly as they were.
func TestAppendVolumeSynopses_AddsWithoutRebuilding(t *testing.T) {
	t.Parallel()

	store := seededStore(t, []narrative.Summary{
		{VolumeID: "vol1", Summary: "one"},
		{VolumeID: "vol2", Summary: "two"},
	})
	before, _, err := store.Load(t.Context(), "lumen")
	require.NoError(t, err)

	require.NoError(t, appendVolumeSynopses(t.Context(), io.Discard, io.Discard, store, "lumen",
		[]narrative.Summary{{VolumeID: "vol3", Summary: "three"}}))

	got, found, err := store.Load(t.Context(), "lumen")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []string{"vol1", "vol2", "vol3"}, summaryVolumeIDs(got.Summaries))
	require.Equal(t, before.Characters, got.Characters, "voices must stay untouched")
	require.Equal(t, before.StyleGuide, got.StyleGuide, "curated style guide must stay untouched")
	require.Greater(t, got.Version, before.Version, "the store bumps the version")
}

// TestAppendVolumeSynopses_ExistingWinsOnConflict pins idempotency: re-adding a
// volume already present keeps the stored synopsis, so a re-run never rewrites
// an earlier volume.
func TestAppendVolumeSynopses_ExistingWinsOnConflict(t *testing.T) {
	t.Parallel()

	store := seededStore(t, []narrative.Summary{
		{VolumeID: "vol1", Summary: "one"},
		{VolumeID: "vol2", Summary: "two"},
	})

	require.NoError(t, appendVolumeSynopses(t.Context(), io.Discard, io.Discard, store, "lumen",
		[]narrative.Summary{
			{VolumeID: "vol2", Summary: "REWRITE — must be ignored"},
			{VolumeID: "vol3", Summary: "three"},
		}))

	got, _, err := store.Load(t.Context(), "lumen")
	require.NoError(t, err)
	require.Equal(t, []string{"vol1", "vol2", "vol3"}, summaryVolumeIDs(got.Summaries))
	require.Equal(t, "two", summaryFor(got, "vol2"), "existing synopsis wins a conflict")
}

// TestAppendVolumeSynopses_NoBaseErrors: append has nothing to extend when no
// bible exists yet — it must say so rather than silently create a partial one.
func TestAppendVolumeSynopses_NoBaseErrors(t *testing.T) {
	t.Parallel()

	store, err := narrative.NewSQLiteStore(filepath.Join(t.TempDir(), "narrative.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	err = appendVolumeSynopses(t.Context(), io.Discard, io.Discard, store, "lumen",
		[]narrative.Summary{{VolumeID: "vol1", Summary: "one"}})
	require.ErrorIs(t, err, narrative.ErrMissingBible)
}

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
