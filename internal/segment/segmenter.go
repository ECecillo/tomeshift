package segment

import (
	"context"
	"fmt"

	"github.com/ECecillo/tomeshift/internal/epub"
)

// Segmenter turns a parsed XHTML document into ordered translatable segments.
type Segmenter interface {
	Segment(ctx context.Context, doc *epub.XHTMLDocument, chapterID string) ([]Segment, error)
}

// ParagraphSegmenter produces one segment per translatable node (paragraph-level
// segmentation for V1), tokenizing inline markup as it goes.
type ParagraphSegmenter struct {
	Placeholders PlaceholderManager
}

// NewParagraphSegmenter returns a segmenter using the default inline-placeholder
// manager.
func NewParagraphSegmenter() *ParagraphSegmenter {
	return &ParagraphSegmenter{Placeholders: InlinePlaceholderManager{}}
}

// Segment walks the document's translatable nodes in order, producing a stable
// segment per node with a normalized hash and tokenized placeholders.
func (s *ParagraphSegmenter) Segment(_ context.Context, doc *epub.XHTMLDocument, chapterID string) ([]Segment, error) {
	if doc == nil {
		return nil, ErrNilDocument
	}

	pm := s.Placeholders
	if pm == nil {
		pm = InlinePlaceholderManager{}
	}

	segments := make([]Segment, 0, len(doc.Nodes))
	for i, cn := range doc.Nodes {
		text, placeholders, err := pm.Tokenize(cn.Node)
		if err != nil {
			return nil, fmt.Errorf("unable to tokenize node %s, err: %w", cn.NodePath, err)
		}

		normalized := Normalize(text)
		if normalized == "" {
			continue
		}

		segments = append(segments, Segment{
			ID:             fmt.Sprintf("%s_p_%04d", chapterID, i+1),
			ChapterID:      chapterID,
			NodePath:       cn.NodePath,
			SourceText:     text,
			NormalizedText: normalized,
			Hash:           Hash(normalized),
			Placeholders:   placeholders,
			Status:         StatusPending,
			Node:           cn.Node,
		})
	}

	return segments, nil
}

var _ Segmenter = (*ParagraphSegmenter)(nil)
