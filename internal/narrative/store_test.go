package narrative_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/narrative"
)

func newStore(t *testing.T) *narrative.SQLiteStore {
	t.Helper()

	store, err := narrative.NewSQLiteStore(filepath.Join(t.TempDir(), "narrative.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSQLiteStore_SaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	want := narrative.Bible{
		SeriesID:   "series-a",
		StyleGuide: "Neutral literary French.",
		Characters: []narrative.CharacterVoice{{
			Name:     "Alice",
			Register: "plainspoken",
			Notes:    "Short sentences.",
			SampleFR: []string{"Je viens."},
		}},
		Address: []narrative.AddressRule{{
			From: "Alice",
			To:   "Bruno",
			Form: "tu",
			History: []narrative.AddressChange{{
				AtChapter: "ch_0001",
				Note:      "Childhood friends.",
			}},
		}},
		Summaries: []narrative.Summary{{
			ChapterID: "ch_0001",
			Summary:   "Alice leaves home.",
		}},
	}

	require.NoError(t, store.Save(t.Context(), want))

	got, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1, got.Version)
	require.Equal(t, want.SeriesID, got.SeriesID)
	require.Equal(t, want.StyleGuide, got.StyleGuide)
	require.Equal(t, want.Characters, got.Characters)
	require.Equal(t, want.Address, got.Address)
	require.Equal(t, want.Summaries, got.Summaries)
}

func TestSQLiteStore_SaveBumpsVersion(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	require.NoError(t, store.Save(t.Context(), narrative.Bible{SeriesID: "series-a", StyleGuide: "v1"}))
	first, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1, first.Version)

	first.StyleGuide = "v2"
	require.NoError(t, store.Save(t.Context(), first))
	second, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 2, second.Version)
	require.Equal(t, "v2", second.StyleGuide)
}

func TestSQLiteStore_SaveAllRollsBackWholeGeneration(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	err := store.SaveAll(t.Context(), []narrative.Bible{
		{SeriesID: "series-a", StyleGuide: "new"},
		{},
	})
	require.ErrorIs(t, err, narrative.ErrEmptySeriesID)
	_, found, loadErr := store.Load(t.Context(), "series-a")
	require.NoError(t, loadErr)
	require.False(t, found)
}

func TestSQLiteStore_ConcurrentSavesGetDistinctVersions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "narrative.sqlite")
	first, err := narrative.NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := narrative.NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, item := range []struct {
		store *narrative.SQLiteStore
		style string
	}{{first, "first"}, {second, "second"}} {
		go func() {
			ready.Done()
			<-start
			errs <- item.store.Save(context.Background(), narrative.Bible{SeriesID: "series-a", StyleGuide: item.style})
		}()
	}
	ready.Wait()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	got, found, err := first.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 2, got.Version)
}

func TestSQLiteStore_SaveIfVersionRejectsStaleWriter(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	require.NoError(t, store.Save(t.Context(), narrative.Bible{SeriesID: "series-a", StyleGuide: "v1"}))
	stale, _, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)

	require.NoError(t, store.Save(t.Context(), narrative.Bible{SeriesID: "series-a", StyleGuide: "v2"}))
	stale.StyleGuide = "stale"
	saved, err := store.SaveIfVersion(t.Context(), stale)
	require.NoError(t, err)
	require.False(t, saved)

	got, _, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.Equal(t, "v2", got.StyleGuide)
	require.Equal(t, 2, got.Version)
}

func TestSQLiteStore_ConcurrentAppendsDoNotLoseUpdates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "narrative.sqlite")
	first, err := narrative.NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := narrative.NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	require.NoError(t, first.Save(t.Context(), narrative.Bible{SeriesID: "series-a", StyleGuide: "base"}))

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, item := range []struct {
		store   *narrative.SQLiteStore
		summary narrative.Summary
	}{
		{first, narrative.Summary{ChapterID: "ch_0001", Summary: "one"}},
		{second, narrative.Summary{ChapterID: "ch_0002", Summary: "two"}},
	} {
		go func() {
			<-start
			errs <- item.store.AppendSummary(context.Background(), "series-a", item.summary)
		}()
	}
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	got, found, err := first.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.Summaries, 2)
	require.Equal(t, 3, got.Version)
}

func TestSQLiteStore_LoadMiss(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	got, found, err := store.Load(t.Context(), "missing")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, got.SeriesID)
}

func TestSQLiteStore_EmptySeriesID(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	_, _, err := store.Load(t.Context(), "")
	require.True(t, errors.Is(err, narrative.ErrEmptySeriesID))
	require.ErrorIs(t, store.Save(t.Context(), narrative.Bible{}), narrative.ErrEmptySeriesID)
	require.ErrorIs(t, store.AppendSummary(t.Context(), "", narrative.Summary{}), narrative.ErrEmptySeriesID)
	require.ErrorIs(t, store.SetAddress(t.Context(), "", narrative.AddressRule{}), narrative.ErrEmptySeriesID)
}

func TestSQLiteStore_AppendSummaryOrdering(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	require.NoError(t, store.AppendSummary(t.Context(), "series-a", narrative.Summary{
		ChapterID: "ch_0001",
		Summary:   "First.",
	}))
	require.NoError(t, store.AppendSummary(t.Context(), "series-a", narrative.Summary{
		ChapterID: "ch_0002",
		Summary:   "Second.",
	}))

	got, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []narrative.Summary{
		{ChapterID: "ch_0001", Summary: "First."},
		{ChapterID: "ch_0002", Summary: "Second."},
	}, got.Summaries)
	require.Equal(t, 2, got.Version)
}

func TestAddressMatrix_EvolutionLogged(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	initial := narrative.AddressRule{
		From: "Alice",
		To:   "Bruno",
		Form: "vous",
		History: []narrative.AddressChange{{
			AtChapter: "ch_0001",
			Note:      "Formal first meeting.",
		}},
	}
	require.NoError(t, store.SetAddress(t.Context(), "series-a", initial))

	changed := narrative.AddressRule{
		From: "Alice",
		To:   "Bruno",
		Form: "tu",
		History: []narrative.AddressChange{{
			AtChapter: "ch_0003",
			Note:      "They become allies.",
		}},
	}
	require.NoError(t, store.SetAddress(t.Context(), "series-a", changed))

	got, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.Address, 1)
	require.Equal(t, "tu", got.Address[0].Form)
	require.Equal(t, []narrative.AddressChange{
		{AtChapter: "ch_0001", Note: "Formal first meeting."},
		{AtChapter: "ch_0003", Note: "They become allies."},
	}, got.Address[0].History)
	require.Equal(t, 2, got.Version)
}

func TestSQLiteStore_SetAddressNoopWhenUnchanged(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	rule := narrative.AddressRule{
		From: "Alice",
		To:   "Bruno",
		Form: "tu",
		History: []narrative.AddressChange{{
			AtChapter: "ch_0001",
			Note:      "Friends.",
		}},
	}

	require.NoError(t, store.SetAddress(t.Context(), "series-a", rule))
	require.NoError(t, store.SetAddress(t.Context(), "series-a", narrative.AddressRule{
		From: "Alice",
		To:   "Bruno",
		Form: "tu",
		History: []narrative.AddressChange{{
			AtChapter: "ch_0002",
			Note:      "Should not be appended.",
		}},
	}))

	got, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1, got.Version)
	require.Len(t, got.Address[0].History, 1)
}

func TestTuVousConsistency(t *testing.T) {
	t.Parallel()

	bible := narrative.Bible{
		Address: []narrative.AddressRule{{
			From: "Alice",
			To:   "Bruno",
			Form: "vous",
		}},
	}

	require.True(t, narrative.Consistent(bible, "Alice", "Bruno", "vous"))
	require.False(t, narrative.Consistent(bible, "Alice", "Bruno", "tu"))
	require.False(t, narrative.Consistent(bible, "Bruno", "Alice", "vous"))
}

// The version identifies the content, so `just bible` reporting v3 means the v3
// content rather than "saved three times".
func TestSQLiteStore_VersionTracksContent(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	bible := narrative.Bible{
		SeriesID:   "series-a",
		StyleGuide: "Neutral literary French.",
		Summaries:  []narrative.Summary{{VolumeID: "volume-01", Summary: "Tome 1."}},
	}

	require.NoError(t, store.Save(t.Context(), bible))
	first, _, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.Equal(t, 1, first.Version)

	// Saving the same content again must not move the version.
	require.NoError(t, store.Save(t.Context(), first))
	unchanged, _, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.Equal(t, 1, unchanged.Version)

	// Any content change does.
	unchanged.Summaries = append(unchanged.Summaries,
		narrative.Summary{VolumeID: "volume-02", Summary: "Tome 2."})
	require.NoError(t, store.Save(t.Context(), unchanged))
	changed, _, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.Equal(t, 2, changed.Version)

	// A caller holding a stale version cannot rewind it.
	stale := changed
	stale.Version = 0
	stale.StyleGuide = "Something else."
	require.NoError(t, store.Save(t.Context(), stale))
	bumped, _, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.Equal(t, 3, bumped.Version)
}

func TestSQLiteStore_ChapterSummariesRoundTrip(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	summaries := []narrative.Summary{
		{VolumeID: "volume-01", ChapterID: "ch_0001", Summary: "Arrivée."},
		{VolumeID: "volume-01", ChapterID: "ch_0002", Summary: "Rencontre."},
		// Same chapter ID, different volume: both must survive.
		{VolumeID: "volume-02", ChapterID: "ch_0001", Summary: "Réveil au manoir."},
	}
	require.NoError(t, store.PutChapterSummaries(t.Context(), "series-a", "volume-01", summaries[:2]))
	require.NoError(t, store.PutChapterSummaries(t.Context(), "series-a", "volume-02", summaries[2:]))

	all, err := store.ChapterSummaries(t.Context(), "series-a", "")
	require.NoError(t, err)
	require.Equal(t, summaries, all)

	scoped, err := store.ChapterSummaries(t.Context(), "series-a", "volume-02")
	require.NoError(t, err)
	require.Equal(t, summaries[2:], scoped)

	// Re-running the map step replaces rather than duplicates.
	require.NoError(t, store.PutChapterSummaries(t.Context(), "series-a", "volume-01", []narrative.Summary{
		{VolumeID: "volume-01", ChapterID: "ch_0001", Summary: "Arrivée, révisée."},
	}))
	again, err := store.ChapterSummaries(t.Context(), "series-a", "volume-01")
	require.NoError(t, err)
	require.Len(t, again, 1)
	require.Equal(t, "Arrivée, révisée.", again[0].Summary)

	// An empty current snapshot removes every stale chapter for the volume.
	require.NoError(t, store.PutChapterSummaries(t.Context(), "series-a", "volume-01", nil))
	again, err = store.ChapterSummaries(t.Context(), "series-a", "volume-01")
	require.NoError(t, err)
	require.Empty(t, again)
}

// The map step's output stays out of the bible: rendering 68 summaries per
// volume into the cacheable prefix is the unbounded growth RFC 0008 removes.
func TestSQLiteStore_ChapterSummariesAreNotPartOfTheBible(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	require.NoError(t, store.Save(t.Context(), narrative.Bible{
		SeriesID:  "series-a",
		Summaries: []narrative.Summary{{VolumeID: "volume-01", Summary: "Synopsis du tome 1."}},
	}))
	require.NoError(t, store.PutChapterSummaries(t.Context(), "series-a", "volume-01", []narrative.Summary{
		{VolumeID: "volume-01", ChapterID: "ch_0001", Summary: "Arrivée."},
	}))

	got, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.Summaries, 1)
	require.Equal(t, "Synopsis du tome 1.", got.Summaries[0].Summary)
}

func TestSQLiteStore_ChapterSummariesValidatesSeriesID(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	require.ErrorIs(t, store.PutChapterSummaries(t.Context(), "", "volume-01", []narrative.Summary{{}}), narrative.ErrEmptySeriesID)
	_, err := store.ChapterSummaries(t.Context(), "", "")
	require.ErrorIs(t, err, narrative.ErrEmptySeriesID)
}

// Pre-0008 rows carry a chapter ID and no volume. They must keep loading, and
// keep rendering under their own label.
func TestSQLiteStore_LegacySummariesLoadWithoutVolume(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	require.NoError(t, store.Save(t.Context(), narrative.Bible{
		SeriesID:  "series-a",
		Summaries: []narrative.Summary{{ChapterID: "prologue", Summary: "Avant l'arrivée."}},
	}))

	got, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "", got.Summaries[0].VolumeID)
	require.Equal(t, "prologue", got.Summaries[0].Label())
}
