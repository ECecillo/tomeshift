package literary

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeResponse_Tolerant(t *testing.T) {
	t.Parallel()

	const want = "Bonjour"
	tests := []struct {
		name string
		raw  string
	}{
		{"plain", `{"segments":[{"id":"s1","target_text":"Bonjour"}]}`},
		{"code fence", "```json\n{\"segments\":[{\"id\":\"s1\",\"target_text\":\"Bonjour\"}]}\n```"},
		{"leading prose", "Here is the translation:\n{\"segments\":[{\"id\":\"s1\",\"target_text\":\"Bonjour\"}]}"},
		{"trailing prose", `{"segments":[{"id":"s1","target_text":"Bonjour"}]} Preserve existing facts.`},
		{"unknown extra field ignored", `{"notes":"x","segments":[{"id":"s1","target_text":"Bonjour"}]}`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := decodeResponse(tt.raw)
			require.NoError(t, err)
			require.Len(t, out.Segments, 1)
			require.Equal(t, want, out.Segments[0].TargetText)
		})
	}
}

func TestDecodeResponse_TruncatedIsError(t *testing.T) {
	t.Parallel()

	// A response cut off mid-array (e.g. hit max_tokens) must error, not silently
	// drop segments.
	_, err := decodeResponse(`{"segments":[{"id":"s1","target_text":"Bonjour"},{"id":"s2",`)
	require.Error(t, err)
}

func TestExtractJSONObject(t *testing.T) {
	t.Parallel()

	// Braces inside string values must not end the object early.
	got := extractJSONObject("noise {\"a\":\"has } brace\"} trailing {ignored}")
	require.Equal(t, `{"a":"has } brace"}`, got)
}
