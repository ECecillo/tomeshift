package epub

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Chapters is the single source of chapter identity: segmentation, style
// sampling and chapter summarisation all read it, so a summary keyed ch_0002
// and the translated chapter ch_0002 are the same document by construction.
func TestBook_Chapters(t *testing.T) {
	t.Parallel()

	book := &Book{
		Spine: []SpineItem{
			{IDRef: "cover"},    // not xhtml
			{IDRef: "nav"},      // navigation
			{IDRef: "one"},      // chapter 1
			{IDRef: "missing"},  // no manifest item
			{IDRef: "unparsed"}, // manifest item with no parsed document
			{IDRef: "two"},      // chapter 2
		},
		Manifest: Manifest{Items: map[string]ManifestItem{
			"cover":    {Href: "cover.jpg", MediaType: "image/jpeg"},
			"nav":      {Href: "nav.xhtml", MediaType: "application/xhtml+xml", Properties: []string{"nav"}},
			"one":      {Href: "one.xhtml", MediaType: "application/xhtml+xml"},
			"unparsed": {Href: "unparsed.xhtml", MediaType: "application/xhtml+xml"},
			"two":      {Href: "two.xhtml", MediaType: "application/xhtml+xml"},
		}},
		Contents: map[string]*XHTMLDocument{
			"nav.xhtml": {Nodes: []*ContentNode{{Text: "Table of contents"}}},
			"one.xhtml": {Nodes: []*ContentNode{{Text: "Alice arrives."}, {Text: "   "}, {Text: "Bruno waits."}}},
			"two.xhtml": {Nodes: []*ContentNode{{Text: "They leave."}}},
		},
	}

	chapters := book.Chapters()

	require.Len(t, chapters, 2)
	// Numbering is dense over the documents that survive the filter, not over
	// spine position: the skipped items must not leave gaps.
	require.Equal(t, "ch_0001", chapters[0].ID)
	require.Equal(t, "one.xhtml", chapters[0].Href)
	require.Equal(t, "ch_0002", chapters[1].ID)
	require.Equal(t, "two.xhtml", chapters[1].Href)

	// Text drops blank nodes and trims; PlainText keeps every node verbatim,
	// because chrF compares it against a reference line by line.
	require.Equal(t, "Alice arrives.\nBruno waits.", chapters[0].Text())
	require.Equal(t, "Alice arrives.\n   \nBruno waits.\nThey leave.\n", book.PlainText())
}

func TestBook_ChaptersEmpty(t *testing.T) {
	t.Parallel()

	require.Empty(t, (&Book{}).Chapters())
}
