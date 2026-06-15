package segment

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// FuzzTokenizeRestore checks the core invariant: tokenizing a paragraph and
// restoring the exact tokenized text (an identity "translation") reproduces the
// original markup. This guards the placeholder round-trip against malformed and
// adversarial inline content (RFC 0004 risk mitigation).
func FuzzTokenizeRestore(f *testing.F) {
	seeds := []string{
		"Hello world",
		"Some <em>emphasized</em> text.",
		`A <strong>b</strong> c <a href="x">d</a> e`,
		"nested <em>a <strong>b</strong> c</em> d",
		"<strong>only</strong>",
		"a &amp; b &lt; c",
		`text with quote " and apostrophe '`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	pm := InlinePlaceholderManager{}

	f.Fuzz(func(t *testing.T, inner string) {
		doc, err := html.Parse(strings.NewReader("<p>" + inner + "</p>"))
		if err != nil {
			return
		}

		p := findParagraph(doc)
		if p == nil {
			return
		}

		before := mustRender(t, p)

		text, placeholders, err := pm.Tokenize(p)
		require.NoError(t, err)

		// Skip the documented pathological case where the visible text literally
		// contains a token-shaped string in addition to issued tokens: ordering
		// is ambiguous and out of scope for V1.
		if len(placeholderTokenPattern.FindAllString(text, -1)) != len(placeholders) {
			t.Skip("literal token collision in source text (documented limitation)")
		}

		require.NoError(t, pm.Restore(text, placeholders, p))
		require.Equal(t, before, mustRender(t, p), "identity round-trip must preserve markup")
	})
}

func findParagraph(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.Data == "p" {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findParagraph(c); found != nil {
			return found
		}
	}
	return nil
}

func mustRender(t *testing.T, n *html.Node) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, html.Render(&sb, n))
	return sb.String()
}
