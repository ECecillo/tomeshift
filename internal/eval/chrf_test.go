package eval

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChrF_Identical(t *testing.T) {
	t.Parallel()
	require.InDelta(t, 1.0, ChrF("Bonjour le monde", "Bonjour le monde", 0, 0), 1e-9)
}

func TestChrF_BothEmpty(t *testing.T) {
	t.Parallel()
	require.InDelta(t, 1.0, ChrF("", "", 0, 0), 1e-9)
}

func TestChrF_Disjoint(t *testing.T) {
	t.Parallel()
	// No shared characters at all -> 0.
	require.InDelta(t, 0.0, ChrF("aaaa", "bbbb", 0, 0), 1e-9)
}

func TestChrF_WhitespaceInsensitive(t *testing.T) {
	t.Parallel()
	require.InDelta(t,
		ChrF("Bonjour le monde", "Bonjour le monde", 0, 0),
		ChrF("Bonjour   le\tmonde", "Bonjour le monde", 0, 0),
		1e-9)
}

func TestChrF_PartialBetweenZeroAndOne(t *testing.T) {
	t.Parallel()
	score := ChrF("Bonjour le monde cruel", "Bonjour le monde", 0, 0)
	require.Greater(t, score, 0.0)
	require.Less(t, score, 1.0)
}

func TestChrF_CloserIsHigher(t *testing.T) {
	t.Parallel()

	ref := "Le chat dort sur le canapé"
	near := "Le chat dort sur le sofa"         // small lexical change
	far := "Une voiture roule sur l'autoroute" // unrelated

	require.Greater(t, ChrF(near, ref, 0, 0), ChrF(far, ref, 0, 0))
}

func TestChrF_HandlesAccentsAsRunes(t *testing.T) {
	t.Parallel()
	// Must not panic or mis-split multibyte runes.
	require.InDelta(t, 1.0, ChrF("éàçùœ", "éàçùœ", 0, 0), 1e-9)
}
