package narrative

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeBibles_PreservesBaseAndAddsIncoming(t *testing.T) {
	t.Parallel()

	base := Bible{
		SeriesID:   "lumen",
		StyleGuide: "First-person, casual, sarcastic.",
		Characters: []CharacterVoice{
			{Name: "Kai", Register: "familier", Notes: "auto-dérision", SampleFR: []string{"Sérieux ?"}},
		},
		Address: []AddressRule{
			{From: "Elena", To: "Kai", Form: "vous"},
		},
		Summaries: []Summary{{ChapterID: "ch_0001", Summary: "Arrivée."}},
	}
	incoming := Bible{
		SeriesID:   "lumen",
		StyleGuide: "Should not clobber base.",
		Characters: []CharacterVoice{
			{Name: "Kai", SampleFR: []string{"Mince."}},    // enrich existing
			{Name: "Mira", Register: "poli", Notes: "dévouée"}, // new
		},
		Address: []AddressRule{
			{From: "Elena", To: "Kai", Form: "tu", History: []AddressChange{{AtChapter: "ch_0010", Note: "rapprochés"}}}, // evolves
			{From: "Mira", To: "Kai", Form: "vous"}, // new
		},
		Summaries: []Summary{
			{ChapterID: "ch_0001", Summary: "DUP — ignored."}, // dup chapter
			{ChapterID: "ch_0002", Summary: "Suite."},         // new
		},
	}

	got, stats := MergeBibles(base, incoming)

	// The duplicate ch_0001 is a genuine conflict, and it is reported.
	require.Equal(t, 1, stats.DroppedSummaries)
	require.True(t, stats.Lossy())

	// Style guide kept from base.
	require.Equal(t, "First-person, casual, sarcastic.", got.StyleGuide)

	// Kai enriched (samples unioned), Mira added.
	require.Len(t, got.Characters, 2)
	require.Equal(t, []string{"Sérieux ?", "Mince."}, got.Characters[0].SampleFR)
	require.Equal(t, "familier", got.Characters[0].Register) // unchanged (incoming empty)
	require.Equal(t, "Mira", got.Characters[1].Name)

	// Address: Elena→Kai evolved vous→tu with history; Mira→Kai added.
	require.Len(t, got.Address, 2)
	require.Equal(t, "tu", got.Address[0].Form)
	require.Len(t, got.Address[0].History, 1)
	require.Equal(t, "ch_0010", got.Address[0].History[0].AtChapter)

	// Summaries: ch_0001 kept (base wins), ch_0002 added.
	require.Len(t, got.Summaries, 2)
	require.Equal(t, "Arrivée.", got.Summaries[0].Summary)
	require.Equal(t, "ch_0002", got.Summaries[1].ChapterID)
}

func TestMergeBibles_EmptyBaseTakesIncoming(t *testing.T) {
	t.Parallel()

	got, stats := MergeBibles(Bible{SeriesID: "s"}, Bible{StyleGuide: "from incoming"})
	require.Equal(t, "from incoming", got.StyleGuide)
	require.False(t, stats.Lossy())
}

// Chapter IDs are minted per book, so volume 2's opening is ch_0001 exactly
// like volume 1's. Before the volume dimension, merging volume 2 into a bible
// holding volume 1 discarded every chapter of volume 2 and said nothing.
func TestMergeBibles_CollidingChapterIDsAcrossVolumesArePreserved(t *testing.T) {
	t.Parallel()

	base := Bible{
		SeriesID: "lumen",
		Summaries: []Summary{
			{VolumeID: "volume-01", ChapterID: "ch_0001", Summary: "Kai arrive à Valoria."},
			{VolumeID: "volume-01", ChapterID: "ch_0002", Summary: "Il rencontre Elena."},
		},
	}
	incoming := Bible{
		SeriesID: "lumen",
		Summaries: []Summary{
			{VolumeID: "volume-02", ChapterID: "ch_0001", Summary: "Kai se réveille au manoir."},
			{VolumeID: "volume-02", ChapterID: "ch_0002", Summary: "Mira et Rina le testent."},
		},
	}

	got, stats := MergeBibles(base, incoming)

	require.Equal(t, 0, stats.DroppedSummaries)
	require.False(t, stats.Lossy())
	require.Len(t, got.Summaries, 4)
	require.Equal(t, "Kai se réveille au manoir.", got.Summaries[2].Summary)
	require.Equal(t, "Mira et Rina le testent.", got.Summaries[3].Summary)
}

func TestSummaryLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary Summary
		want    string
	}{
		{"volume synopsis", Summary{VolumeID: "volume-01"}, "volume-01"},
		{"legacy chapter row", Summary{ChapterID: "prologue"}, "prologue"},
		{"scoped chapter", Summary{VolumeID: "volume-02", ChapterID: "ch_0001"}, "volume-02/ch_0001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.summary.Label())
		})
	}
}
