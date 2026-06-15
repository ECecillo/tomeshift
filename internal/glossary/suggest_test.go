package glossary

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func candidateMap(cs []Candidate) map[string]int {
	m := make(map[string]int, len(cs))
	for _, c := range cs {
		m[c.Term] = c.Count
	}
	return m
}

func TestSuggest_FindsMidSentenceProperNouns(t *testing.T) {
	t.Parallel()

	text := "The hobbit met Gandalf. Later Gandalf spoke to Gandalf again. " +
		"Frodo saw Gandalf near Frodo's home. Frodo and Frodo."

	got := candidateMap(Suggest(text, 2))
	// "Gandalf" appears mid-sentence several times -> suggested.
	require.GreaterOrEqual(t, got["Gandalf"], 2)
	require.Contains(t, got, "Frodo")
	// "The" / "Later" are only sentence-initial -> not suggested.
	require.NotContains(t, got, "The")
	require.NotContains(t, got, "Later")
}

func TestSuggest_FindsMultiWordPhrases(t *testing.T) {
	t.Parallel()

	text := "They left for New York. In New York it rained. Back to New York."
	got := candidateMap(Suggest(text, 2))
	require.Contains(t, got, "New York")
	require.GreaterOrEqual(t, got["New York"], 2)
}

func TestSuggest_RespectsMinCount(t *testing.T) {
	t.Parallel()

	text := "Alice saw Bob. Bob saw Alice. Carol waved once."
	got := candidateMap(Suggest(text, 2))
	// Carol appears once mid-sentence -> below min-count.
	require.NotContains(t, got, "Carol")
}

func TestSuggestTerms_Skeleton(t *testing.T) {
	t.Parallel()

	terms := SuggestTerms([]Candidate{{Term: "Gandalf", Count: 9}})
	require.Len(t, terms, 1)
	require.Equal(t, "Gandalf", terms[0].EN)
	require.Equal(t, "Gandalf", terms[0].FR)
	require.True(t, terms[0].Locked)
}

func TestSuggest_RankedByFrequency(t *testing.T) {
	t.Parallel()

	text := "x Alpha. y Alpha. z Alpha. p Beta. q Beta."
	got := Suggest(text, 2)
	require.NotEmpty(t, got)
	require.Equal(t, "Alpha", got[0].Term, "most frequent first")
}
