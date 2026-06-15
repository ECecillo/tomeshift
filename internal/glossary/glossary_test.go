package glossary

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNew_KeepsOnlyLocked(t *testing.T) {
	t.Parallel()

	g, err := New([]Term{
		{EN: "Gandalf", FR: "Gandalf", Locked: true},
		{EN: "house", FR: "maison", Locked: false},
	})
	require.NoError(t, err)
	require.Len(t, g.locked, 1)
	require.Equal(t, "Gandalf", g.locked[0].EN)
}

func TestNew_RejectsEmptyTerms(t *testing.T) {
	t.Parallel()

	_, err := New([]Term{{EN: "", FR: "x", Locked: true}})
	require.ErrorIs(t, err, ErrEmptyTermEN)

	_, err = New([]Term{{EN: "x", FR: "", Locked: true}})
	require.ErrorIs(t, err, ErrEmptyTermFR)
}

func TestNew_DetectsConflictingTerms(t *testing.T) {
	t.Parallel()

	_, err := New([]Term{
		{EN: "Strider", FR: "Grands-Pas", Locked: true},
		{EN: "Strider", FR: "Arpenteur", Locked: true},
	})
	require.ErrorIs(t, err, ErrConflictingTerm)
}

func TestNew_AllowsExactDuplicates(t *testing.T) {
	t.Parallel()

	g, err := New([]Term{
		{EN: "Frodo", FR: "Frodon", Locked: true},
		{EN: "Frodo", FR: "Frodon", Locked: true},
	})
	require.NoError(t, err)
	require.Len(t, g.locked, 1)
}

func TestProtectApply_RoundTrip(t *testing.T) {
	t.Parallel()

	g, err := New([]Term{
		{EN: "Shire", FR: "Comté", Locked: true},
		{EN: "Bilbo", FR: "Bilbon", Locked: true},
	})
	require.NoError(t, err)

	protected, subs := g.Protect("Bilbo left the Shire.")
	require.NotContains(t, protected, "Bilbo")
	require.NotContains(t, protected, "Shire")
	require.Len(t, subs, 2)

	// A translator that preserves tokens; then enforce canonical terms.
	applied := g.Apply(protected, subs)
	require.Contains(t, applied, "Bilbon")
	require.Contains(t, applied, "Comté")
	require.False(t, HasGlossaryTokens(applied))
}

func TestProtect_LongestFirst(t *testing.T) {
	t.Parallel()

	g, err := New([]Term{
		{EN: "New York", FR: "New York", Locked: true},
		{EN: "York", FR: "Yorkville", Locked: true},
	})
	require.NoError(t, err)

	protected, subs := g.Protect("New York")
	applied := g.Apply(protected, subs)
	// "New York" must win over the shorter "York".
	require.Equal(t, "New York", applied)
}

func TestViolations(t *testing.T) {
	t.Parallel()

	g, err := New([]Term{{EN: "Gandalf", FR: "Gandalf", Locked: true}})
	require.NoError(t, err)

	require.Equal(t, 0, g.Violations("Gandalf spoke", "Gandalf a parlé"))
	require.Equal(t, 1, g.Violations("Gandalf spoke", "Le magicien a parlé"))
	require.Equal(t, 0, g.Violations("nobody here", "personne ici"))
}

func TestLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "glossary.json")
	require.NoError(t, os.WriteFile(path, []byte(`[
	  {"term_en":"Frodo","term_fr":"Frodon","locked":true},
	  {"term_en":"ring","term_fr":"anneau","locked":false}
	]`), 0o600))

	g, err := Load(path)
	require.NoError(t, err)
	require.Len(t, g.locked, 1)

	protected, subs := g.Protect("Frodo carried the ring")
	applied := g.Apply(protected, subs)
	require.Contains(t, applied, "Frodon")
	require.Contains(t, applied, "ring", "unlocked terms are not enforced")
}

func TestNilGlossary_NoOp(t *testing.T) {
	t.Parallel()

	var g *Glossary
	protected, subs := g.Protect("anything")
	require.Equal(t, "anything", protected)
	require.Empty(t, subs)
	require.Equal(t, 0, g.Violations("a", "b"))
}
