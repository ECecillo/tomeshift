package epub

import (
	"slices"

	"golang.org/x/net/html"
)

type RawEPUB struct {
	Mimetype   []byte
	Container  []byte // META-INF/container.xml
	OPFPath    string
	OPFContent []byte
	Files      map[string][]byte // All files by path
}

type Book struct {
	Path      string
	Manifest  Manifest
	Spine     []SpineItem
	Contents  map[string]*XHTMLDocument // href -> parsed doc
	Resources map[string][]byte         // Non-XHTML files
}

type Manifest struct {
	Items map[string]ManifestItem
}

type ManifestItem struct {
	ID         string
	Href       string
	MediaType  string
	Properties []string
}

type SpineItem struct {
	IDRef string
}

// XHTMLDocument holds a parsed spine document. Root is the live html.Node tree
// used for both content extraction and recomposition: mutating a ContentNode's
// Node and re-rendering Root yields the translated document.
type XHTMLDocument struct {
	Raw   []byte
	Root  *html.Node
	Nodes []*ContentNode
}

// ContentNode is a translatable element extracted from an XHTML document. Node
// points into the document's live tree so translated text can be written back
// in place before recomposition.
type ContentNode struct {
	NodePath string     // XPath-like stable path, e.g. "/html[1]/body[1]/p[2]"
	Text     string     // Inner text content, whitespace-trimmed
	Node     *html.Node // Reference into XHTMLDocument.Root for recomposition
}

// IsNav reports whether the item is the EPUB navigation document. Nav is
// structural rather than prose: it is never segmented, translated or scored,
// and every stage of the pipeline has to skip it.
func (i ManifestItem) IsNav() bool {
	return slices.Contains(i.Properties, "nav")
}
