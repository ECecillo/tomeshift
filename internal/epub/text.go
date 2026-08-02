package epub

import (
	"strings"
)

// PlainText returns the book's visible translatable text in spine order, one
// content node per line. Navigation documents and non-XHTML items are skipped.
// It is used for reference-based evaluation (e.g. chrF) where only the visible
// prose matters.
func (b *Book) PlainText() string {
	var sb strings.Builder
	for _, chapter := range b.Chapters() {
		// Blank nodes are kept here, unlike Chapter.Text: chrF compares this
		// output against a reference line by line.
		for _, n := range chapter.Doc.Nodes {
			sb.WriteString(n.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
