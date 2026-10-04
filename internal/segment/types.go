package segment

import (
	"unicode/utf8"

	"golang.org/x/net/html"
)

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

// placeholderRole distinguishes how a placeholder token is spliced back during
// Restore. Opaque elements (the default) are re-attached as a whole subtree.
// A "simple" inline element whose children are all text is split into an open
// token and a close token so its inner text stays translatable: the open token
// re-creates the (childless) element and the close token closes it, with the
// translated inner text appended in between.
type placeholderRole uint8

const (
	roleOpaque placeholderRole = iota // whole subtree re-attached verbatim
	roleOpen                          // opening tag of a simple inline element
	roleClose                         // closing tag of a simple inline element
)

// Placeholder represents a piece of inline markup removed from a segment's text
// before translation and restored afterwards. Opaque inline elements are kept
// whole so their subtree splices back verbatim; simple text-only inline elements
// are kept as an open/close token pair so their inner text can be translated.
type Placeholder struct {
	Token    string          // stable token, e.g. `<x id="1"/>`
	Original string          // original outer HTML, used for reporting/QA
	Type     PlaceholderType //
	role     placeholderRole // how to splice back on Restore
	node     *html.Node      // node re-attached on Restore (nil for roleClose)
	// closeID is the id of the token that closes this one, set on roleOpen.
	// Restore needs the pairing, not just the count: open-1 open-3 close-2
	// close-4 is depth-balanced but closes the wrong elements.
	closeID string
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

// SourceChars totals the source characters of segs, counting runes rather than
// bytes so accented text is not over-counted. It is the unit the NMT providers
// bill in, and the basis of every token heuristic.
func SourceChars(segs []Segment) int {
	chars := 0
	for i := range segs {
		chars += utf8.RuneCountInString(segs[i].SourceText)
	}
	return chars
}

// Unit is the serialisable projection of a Segment: the schema §16.1 content
// fields minus schema_version, the run/book ids and translation_engine (the
// engine is recorded on the TM entry, not the unit). It is what crosses process
// boundaries; a Segment (which references a live *html.Node) never does.
// Placeholders are reduced to their opaque tokens because the *html.Node subtree
// they carry cannot be serialised — re-application re-segments the source
// document to recover it.
type Unit struct {
	ID             string        `json:"segment_id"`
	ChapterID      string        `json:"chapter_id"`
	NodePath       string        `json:"node_path"`
	SourceText     string        `json:"source_text"`
	NormalizedText string        `json:"normalized_source_text"`
	Hash           string        `json:"segment_hash"`
	Placeholders   []string      `json:"placeholders"` // tokens, e.g. `<x id="1"/>`
	TargetText     string        `json:"target_text"`
	Status         SegmentStatus `json:"status"`
}

// Unit projects the segment onto its serialisable form, dropping the live
// *html.Node and flattening each placeholder to its token.
func (s Segment) Unit() Unit {
	tokens := make([]string, len(s.Placeholders))
	for i := range s.Placeholders {
		tokens[i] = s.Placeholders[i].Token
	}
	return Unit{
		ID:             s.ID,
		ChapterID:      s.ChapterID,
		NodePath:       s.NodePath,
		SourceText:     s.SourceText,
		NormalizedText: s.NormalizedText,
		Hash:           s.Hash,
		Placeholders:   tokens,
		TargetText:     s.TargetText,
		Status:         s.Status,
	}
}

// SourceCharsOfUnits totals the source characters of units, counting runes
// rather than bytes — the same billing rule as SourceChars, applied to the
// serialisable projection that crosses process boundaries.
func SourceCharsOfUnits(units []Unit) int {
	chars := 0
	for i := range units {
		chars += utf8.RuneCountInString(units[i].SourceText)
	}
	return chars
}
