package segment

import "golang.org/x/net/html"

// SegmentStatus tracks where a segment's translation came from.
type SegmentStatus string

const (
	StatusPending    SegmentStatus = "pending"
	StatusTranslated SegmentStatus = "translated"  // produced by the NMT path
	StatusReused     SegmentStatus = "reused"      // exact TM hit
	StatusPostEdited SegmentStatus = "post_edited" // NMT draft refined by the LLM
)

// PlaceholderType classifies the markup a placeholder protects.
type PlaceholderType string

const (
	// PlaceholderInline protects inline elements (<em>, <strong>, <a>, ruby...).
	PlaceholderInline PlaceholderType = "inline"
)

// Placeholder represents a piece of inline markup removed from a segment's text
// before translation and restored afterwards. The whole inline element is kept
// opaque so its subtree can be spliced back verbatim, guaranteeing valid markup.
type Placeholder struct {
	Token    string          // stable token, e.g. `<x id="1"/>`
	Original string          // original outer HTML, used for reporting/QA
	Type     PlaceholderType //
	node     *html.Node      // original node, re-attached on Restore
}

// Segment is a paragraph-level translatable unit.
type Segment struct {
	ID             string        // e.g. "ch_0001_p_0012"
	ChapterID      string        // e.g. "ch_0001"
	NodePath       string        // XPath-like location within the document
	SourceText     string        // source text with placeholder tokens inlined
	NormalizedText string        // normalized form used for hashing/TM lookup
	Hash           string        // sha256 hex of NormalizedText
	Placeholders   []Placeholder //
	TargetText     string        // translated text with placeholder tokens
	Status         SegmentStatus //
	Node           *html.Node    // reference into the live tree for recomposition
}
