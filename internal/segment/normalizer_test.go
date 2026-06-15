package segment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "collapses whitespace runs", input: "Hello    world", want: "Hello world"},
		{name: "trims and collapses newlines/tabs", input: "\n  Hello\tworld \n", want: "Hello world"},
		{name: "folds curly quotes", input: "“Hello” ‘world’", want: `"Hello" 'world'`},
		{name: "folds dashes and ellipsis", input: "a — b … c", want: "a - b ... c"},
		{name: "empty stays empty", input: "   \n\t ", want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Normalize(tt.input))
		})
	}
}

func TestHash_Deterministic(t *testing.T) {
	t.Parallel()

	a := Hash(Normalize("Hello   world"))
	b := Hash(Normalize("Hello world"))
	c := Hash(Normalize("Different text"))

	require.Equal(t, a, b, "normalized-equal inputs must hash equally")
	require.NotEqual(t, a, c)
	require.Len(t, a, 64, "sha256 hex is 64 chars")
}
