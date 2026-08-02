package segment

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// firstElement parses an XHTML fragment and returns the first element with the
// given tag name, used to obtain a live node for tokenize/restore tests.
func firstElement(t *testing.T, fragment, tag string) *html.Node {
	t.Helper()

	doc, err := html.Parse(strings.NewReader(fragment))
	require.NoError(t, err)

	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == tag {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	require.NotNil(t, found, "fragment must contain a <%s>", tag)
	return found
}

func renderNode(t *testing.T, n *html.Node) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, html.Render(&sb, n))
	return sb.String()
}

func TestTokenize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		fragment         string
		wantText         string
		wantPlaceholders []string // expected Original markup, in order
	}{
		{
			name:             "plain text has no placeholders",
			fragment:         "<p>Hello world</p>",
			wantText:         "Hello world",
			wantPlaceholders: nil,
		},
		{
			name:             "simple inline becomes an open/close token pair",
			fragment:         "<p>Some <em>emphasized</em> text.</p>",
			wantText:         `Some <x id="1"/>emphasized<x id="2"/> text.`,
			wantPlaceholders: []string{"<em>emphasized</em>", ""},
		},
		{
			name:             "multiple inline elements get sequential ids",
			fragment:         `<p>A <strong>b</strong> c <a href="x">d</a> e</p>`,
			wantText:         `A <x id="1"/>b<x id="2"/> c <x id="3"/>d<x id="4"/> e`,
			wantPlaceholders: []string{"<strong>b</strong>", "", `<a href="x">d</a>`, ""},
		},
		{
			name:             "inline with nested markup stays opaque",
			fragment:         `<p>x <span>a <em>b</em></span> y</p>`,
			wantText:         `x <x id="1"/> y`,
			wantPlaceholders: []string{`<span>a <em>b</em></span>`},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			node := firstElement(t, tt.fragment, "p")
			text, placeholders, err := InlinePlaceholderManager{}.Tokenize(node)

			require.NoError(t, err)
			require.Equal(t, tt.wantText, text)
			require.Len(t, placeholders, len(tt.wantPlaceholders))
			for i, want := range tt.wantPlaceholders {
				require.Equal(t, want, placeholders[i].Original)
				require.Equal(t, PlaceholderInline, placeholders[i].Type)
			}
		})
	}
}

func TestTokenizeRestore_RoundTrip(t *testing.T) {
	t.Parallel()

	fragments := []string{
		"<p>Hello world</p>",
		"<p>Some <em>emphasized</em> text.</p>",
		`<p>A <strong>bold</strong> and a <a href="x">link</a>.</p>`,
	}

	for _, frag := range fragments {
		frag := frag
		t.Run(frag, func(t *testing.T) {
			t.Parallel()

			pm := InlinePlaceholderManager{}
			node := firstElement(t, frag, "p")
			original := renderNode(t, node)

			text, placeholders, err := pm.Tokenize(node)
			require.NoError(t, err)

			// Simulate a translator that preserves placeholder tokens verbatim.
			require.NoError(t, pm.Restore(text, placeholders, node))
			require.Equal(t, original, renderNode(t, node), "round-trip must reproduce original markup")
		})
	}
}

func TestRestore_TranslatedText(t *testing.T) {
	t.Parallel()

	pm := InlinePlaceholderManager{}
	node := firstElement(t, "<p>Some <em>emphasized</em> text.</p>", "p")

	_, placeholders, err := pm.Tokenize(node)
	require.NoError(t, err)

	// Translator output: French text keeping the open/close token pair, with the
	// inner text now translated ("emphasized" -> "souligné").
	require.NoError(t, pm.Restore(`Du texte <x id="1"/>souligné<x id="2"/> ici.`, placeholders, node))
	require.Equal(t, "<p>Du texte <em>souligné</em> ici.</p>", renderNode(t, node))
}

func TestRestore_AcceptsPairedTokenForm(t *testing.T) {
	t.Parallel()

	// Some NMT providers (e.g. LibreTranslate with format=html) normalize the
	// self-closing token <x id="1"/> into the paired form <x id="1"></x>.
	// Restore must accept both.
	pm := InlinePlaceholderManager{}
	node := firstElement(t, "<p>Some <em>emphasized</em> text.</p>", "p")

	_, placeholders, err := pm.Tokenize(node)
	require.NoError(t, err)

	require.NoError(t, pm.Restore(`Du texte <x id="1"></x>souligné<x id="2"></x> ici.`, placeholders, node))
	require.Equal(t, "<p>Du texte <em>souligné</em> ici.</p>", renderNode(t, node))
}

func TestRestore_Errors(t *testing.T) {
	t.Parallel()

	pm := InlinePlaceholderManager{}

	t.Run("non-issued token kept as literal, real placeholder missing", func(t *testing.T) {
		t.Parallel()
		node := firstElement(t, "<p>a <em>b</em></p>", "p")
		_, placeholders, err := pm.Tokenize(node)
		require.NoError(t, err)
		// id 9 was never issued; the real id 1 is absent -> missing placeholder.
		err = pm.Restore(`a <x id="9"/>`, placeholders, node)
		require.ErrorIs(t, err, ErrMissingPlaceholder)
	})

	t.Run("literal token in source round-trips without inline elements", func(t *testing.T) {
		t.Parallel()
		// A paragraph whose visible text literally contains a token-shaped
		// string and has no inline elements must survive tokenize+restore.
		node := firstElement(t, `<p>see &lt;x id="1"/&gt; here</p>`, "p")
		text, placeholders, err := pm.Tokenize(node)
		require.NoError(t, err)
		require.Empty(t, placeholders)
		require.NoError(t, pm.Restore(text, placeholders, node))
		require.Equal(t, `<p>see &lt;x id=&#34;1&#34;/&gt; here</p>`, renderNode(t, node))
	})

	t.Run("dropped placeholder", func(t *testing.T) {
		t.Parallel()
		node := firstElement(t, "<p>a <em>b</em></p>", "p")
		_, placeholders, err := pm.Tokenize(node)
		require.NoError(t, err)
		err = pm.Restore(`a translated without token`, placeholders, node)
		require.ErrorIs(t, err, ErrMissingPlaceholder)
	})

	t.Run("dropped placeholder leaves the node untouched", func(t *testing.T) {
		t.Parallel()
		// The callers document a restore failure as "the segment keeps its
		// source markup". That is only true if Restore validates before it
		// mutates: a partial rebuild would drop <em>b</em> outright.
		node := firstElement(t, "<p>a <em>b</em> c</p>", "p")
		before := renderNode(t, node)

		_, placeholders, err := pm.Tokenize(node)
		require.NoError(t, err)

		err = pm.Restore(`a b c`, placeholders, node)
		require.ErrorIs(t, err, ErrMissingPlaceholder)
		require.Equal(t, before, renderNode(t, node))
	})

	t.Run("partially dropped placeholder leaves the node untouched", func(t *testing.T) {
		t.Parallel()
		// The close token survives, the open token does not. Before the
		// pre-flight check this rebuilt the node and lost the <em> element.
		node := firstElement(t, "<p>a <em>b</em> c</p>", "p")
		before := renderNode(t, node)

		_, placeholders, err := pm.Tokenize(node)
		require.NoError(t, err)

		err = pm.Restore(`a b<x id="2"/> c`, placeholders, node)
		require.ErrorIs(t, err, ErrMissingPlaceholder)
		require.Equal(t, before, renderNode(t, node))
	})
}
