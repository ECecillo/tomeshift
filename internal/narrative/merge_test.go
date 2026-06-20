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
		Summaries: []ChapterSummary{{ChapterID: "ch_0001", Summary: "Arrivée."}},
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
		Summaries: []ChapterSummary{
			{ChapterID: "ch_0001", Summary: "DUP — ignored."}, // dup chapter
			{ChapterID: "ch_0002", Summary: "Suite."},         // new
		},
	}

	got := MergeBibles(base, incoming)

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

	got := MergeBibles(Bible{SeriesID: "s"}, Bible{StyleGuide: "from incoming"})
	require.Equal(t, "from incoming", got.StyleGuide)
}
