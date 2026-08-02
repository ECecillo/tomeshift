package epub

import (
	"fmt"
	"strings"
)

// xhtmlMediaType is the only spine media type carrying translatable prose.
const xhtmlMediaType = "application/xhtml+xml"

// Chapter is one spine document treated as a unit of work: segmentation,
// summarisation and style sampling all walk the same list, in the same order,
// under the same identifiers.
type Chapter struct {
	// ID is minted per book in spine order (ch_0001, ch_0002, …). It is the
	// only chapter identity in the codebase — a summary keyed ch_0012 and the
	// translated chapter ch_0012 refer to the same document because they come
	// from this one function, not because two packages agree by coincidence.
	ID   string
	Href string
	Doc  *XHTMLDocument
}

// Chapters returns the book's content documents in spine order, skipping
// navigation documents, non-XHTML items, and anything the parser did not
// produce a document for.
func (b *Book) Chapters() []Chapter {
	var chapters []Chapter
	for _, item := range b.Spine {
		manifestItem, ok := b.Manifest.Items[item.IDRef]
		if !ok || manifestItem.MediaType != xhtmlMediaType || manifestItem.IsNav() {
			continue
		}
		doc, ok := b.Contents[manifestItem.Href]
		if !ok || doc == nil {
			continue
		}

		chapters = append(chapters, Chapter{
			ID:   fmt.Sprintf("ch_%04d", len(chapters)+1),
			Href: manifestItem.Href,
			Doc:  doc,
		})
	}
	return chapters
}

// Text returns the chapter's visible prose, one content node per line, with
// blank nodes dropped. This is the form prompts want; PlainText keeps its own
// blank-preserving join because chrF scores it positionally.
func (c Chapter) Text() string {
	var sb strings.Builder
	for _, node := range c.Doc.Nodes {
		if strings.TrimSpace(node.Text) == "" {
			continue
		}
		sb.WriteString(node.Text)
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}
