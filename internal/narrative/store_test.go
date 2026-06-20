package narrative_test

import (
	"errors"
	"path/filepath"
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
		Summaries: []narrative.ChapterSummary{{
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
	require.ErrorIs(t, store.AppendSummary(t.Context(), "", narrative.ChapterSummary{}), narrative.ErrEmptySeriesID)
	require.ErrorIs(t, store.SetAddress(t.Context(), "", narrative.AddressRule{}), narrative.ErrEmptySeriesID)
}

func TestSQLiteStore_AppendSummaryOrdering(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	require.NoError(t, store.AppendSummary(t.Context(), "series-a", narrative.ChapterSummary{
		ChapterID: "ch_0001",
		Summary:   "First.",
	}))
	require.NoError(t, store.AppendSummary(t.Context(), "series-a", narrative.ChapterSummary{
		ChapterID: "ch_0002",
		Summary:   "Second.",
	}))

	got, found, err := store.Load(t.Context(), "series-a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []narrative.ChapterSummary{
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
