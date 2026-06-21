package segment

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/ECecillo/ebooks-translator/internal/epub"
)

// allElements returns every element with the given tag in document order.
func allElements(t *testing.T, fragment, tag string) []*html.Node {
	t.Helper()

	doc, err := html.Parse(strings.NewReader(fragment))
	require.NoError(t, err)

	var found []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == tag {
			found = append(found, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

func docFromParagraphs(t *testing.T, fragment string) *epub.XHTMLDocument {
	t.Helper()

	ps := allElements(t, fragment, "p")
	nodes := make([]*epub.ContentNode, 0, len(ps))
	for i, p := range ps {
		nodes = append(nodes, &epub.ContentNode{
			NodePath: "/html[1]/body[1]/p[" + string(rune('1'+i)) + "]",
			Node:     p,
		})
	}
	return &epub.XHTMLDocument{Nodes: nodes}
}

func TestParagraphSegmenter_Segment(t *testing.T) {
	t.Parallel()

	doc := docFromParagraphs(t, `<body>
		<p>First paragraph.</p>
		<p>Second with <em>markup</em> inside.</p>
	</body>`)

	segs, err := NewParagraphSegmenter().Segment(t.Context(), doc, "ch_0001")
	require.NoError(t, err)
	require.Len(t, segs, 2)

	require.Equal(t, "ch_0001_p_0001", segs[0].ID)
	require.Equal(t, "ch_0001", segs[0].ChapterID)
	require.Equal(t, "First paragraph.", segs[0].SourceText)
	require.Equal(t, StatusPending, segs[0].Status)
	require.NotEmpty(t, segs[0].Hash)
	require.Empty(t, segs[0].Placeholders)

	require.Equal(t, "ch_0001_p_0002", segs[1].ID)
	// A text-only inline element is split into an open/close token pair so its
	// inner text ("markup") stays translatable.
	require.Equal(t, `Second with <x id="1"/>markup<x id="2"/> inside.`, segs[1].SourceText)
	require.Len(t, segs[1].Placeholders, 2)
	require.Equal(t, `Second with <x id="1"/>markup<x id="2"/> inside.`, segs[1].NormalizedText)
}

func TestParagraphSegmenter_NilDocument(t *testing.T) {
	t.Parallel()

	_, err := NewParagraphSegmenter().Segment(t.Context(), nil, "ch_0001")
	require.ErrorIs(t, err, ErrNilDocument)
}

func TestParagraphSegmenter_SkipsEmpty(t *testing.T) {
	t.Parallel()

	doc := docFromParagraphs(t, "<body><p>   </p><p>Real.</p></body>")

	segs, err := NewParagraphSegmenter().Segment(t.Context(), doc, "ch_0001")
	require.NoError(t, err)
	require.Len(t, segs, 1)
	require.Equal(t, "Real.", segs[0].SourceText)
}
