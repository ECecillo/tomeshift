package epub

import (
	"archive/zip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

func TestEPUBRecomposer_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := newRawEPUBFixture(t, cloneEntries(validEPUBEntries))

	book, err := OPFParser{}.Parse(ctx, raw)
	require.NoError(t, err)
	require.Contains(t, book.Contents, "chapter1.xhtml")

	// Translate the first paragraph in place.
	p := book.Contents["chapter1.xhtml"].Nodes[0].Node
	for c := p.FirstChild; c != nil; {
		next := c.NextSibling
		p.RemoveChild(c)
		c = next
	}
	p.AppendChild(&html.Node{Type: html.TextNode, Data: "Bonjour le monde"})

	outPath := filepath.Join(t.TempDir(), "translated.epub")
	require.NoError(t, EPUBRecomposer{}.Recompose(ctx, raw, book, outPath))

	// mimetype must be the first entry and stored uncompressed.
	zr, err := zip.OpenReader(outPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = zr.Close() })

	require.NotEmpty(t, zr.File)
	require.Equal(t, "mimetype", zr.File[0].Name)
	require.Equal(t, uint16(zip.Store), zr.File[0].Method)

	// Re-extract and re-parse: the translation must survive the round trip.
	reExtracted, err := ZipExtractor{}.Extract(ctx, outPath)
	require.NoError(t, err)

	reBook, err := OPFParser{}.Parse(ctx, reExtracted)
	require.NoError(t, err)
	require.Contains(t, reBook.Contents, "chapter1.xhtml")
	require.Equal(t, "Bonjour le monde", reBook.Contents["chapter1.xhtml"].Nodes[0].Text)
}

func TestEPUBRecomposer_NilInput(t *testing.T) {
	t.Parallel()

	err := EPUBRecomposer{}.Recompose(t.Context(), nil, nil, filepath.Join(t.TempDir(), "x.epub"))
	require.ErrorIs(t, err, ErrNilRecomposeInput)
}

func TestEPUBRecomposer_PreservesResources(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	entries := cloneEntries(validEPUBEntries)
	entries["OEBPS/style.css"] = "body { margin: 0; }"
	raw := newRawEPUBFixture(t, entries)

	book, err := OPFParser{}.Parse(ctx, raw)
	require.NoError(t, err)

	outPath := filepath.Join(t.TempDir(), "out.epub")
	require.NoError(t, EPUBRecomposer{}.Recompose(ctx, raw, book, outPath))

	reExtracted, err := ZipExtractor{}.Extract(ctx, outPath)
	require.NoError(t, err)
	require.Contains(t, reExtracted.Files, "OEBPS/style.css")
	require.Equal(t, "body { margin: 0; }", string(reExtracted.Files["OEBPS/style.css"]))
}

// ensure rendered output stays parseable as a sanity check on serialization.
func TestEPUBRecomposer_RendersParseableXHTML(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := newRawEPUBFixture(t, cloneEntries(validEPUBEntries))
	book, err := OPFParser{}.Parse(ctx, raw)
	require.NoError(t, err)

	outPath := filepath.Join(t.TempDir(), "out.epub")
	require.NoError(t, EPUBRecomposer{}.Recompose(ctx, raw, book, outPath))

	reExtracted, err := ZipExtractor{}.Extract(ctx, outPath)
	require.NoError(t, err)

	_, err = html.Parse(strings.NewReader(string(reExtracted.Files["OEBPS/chapter1.xhtml"])))
	require.NoError(t, err)
}

func TestEPUBRecomposer_PreservesXMLDeclaration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := newRawEPUBFixture(t, cloneEntries(validEPUBEntries))
	book, err := OPFParser{}.Parse(ctx, raw)
	require.NoError(t, err)

	outPath := filepath.Join(t.TempDir(), "out.epub")
	require.NoError(t, EPUBRecomposer{}.Recompose(ctx, raw, book, outPath))

	reExtracted, err := ZipExtractor{}.Extract(ctx, outPath)
	require.NoError(t, err)

	chapter := string(reExtracted.Files["OEBPS/chapter1.xhtml"])
	require.True(t, strings.HasPrefix(chapter, `<?xml version="1.0" encoding="UTF-8"?>`),
		"XML declaration must be restored, got prefix: %.60q", chapter)
	require.NotContains(t, chapter, "<!--?xml", "declaration must not be a bogus comment")
}
