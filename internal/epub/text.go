package epub

import (
	"slices"
	"strings"
)

// PlainText returns the book's visible translatable text in spine order, one
// content node per line. Navigation documents and non-XHTML items are skipped.
// It is used for reference-based evaluation (e.g. chrF) where only the visible
// prose matters.
func (b *Book) PlainText() string {
	var sb strings.Builder
	for _, item := range b.Spine {
		mi, ok := b.Manifest.Items[item.IDRef]
		if !ok || mi.MediaType != "application/xhtml+xml" {
			continue
		}
		if slices.Contains(mi.Properties, "nav") {
			continue
		}
		doc, ok := b.Contents[mi.Href]
		if !ok || doc == nil {
			continue
		}
		for _, n := range doc.Nodes {
			sb.WriteString(n.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
