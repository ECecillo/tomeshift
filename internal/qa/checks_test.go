package qa

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/epub"
	"github.com/ECecillo/tomeshift/internal/segment"
)

func TestEvaluate_HappyPath(t *testing.T) {
	t.Parallel()

	book := &epub.Book{
		Manifest: epub.Manifest{Items: map[string]epub.ManifestItem{
			"chap1": {ID: "chap1", Href: "chapter1.xhtml", MediaType: xhtmlMediaType},
		}},
		Spine:    []epub.SpineItem{{IDRef: "chap1"}},
		Contents: map[string]*epub.XHTMLDocument{"chapter1.xhtml": {}},
	}

	segs := []segment.Segment{
		{ID: "ch_0001_p_0001", Status: segment.StatusTranslated, TargetText: "Bonjour"},
		{
			ID:           "ch_0001_p_0002",
			Status:       segment.StatusTranslated,
			TargetText:   `Du texte <x id="1"/> ici`,
			Placeholders: []segment.Placeholder{{Token: `<x id="1"/>`}},
		},
	}

	report := Evaluate(book, segs)

	require.True(t, report.Passed())
	require.Equal(t, 2, report.TotalSegments)
	require.Equal(t, 1, report.PlaceholdersExpected)
	require.Equal(t, 1, report.PlaceholdersRestored)
	require.InDelta(t, 1.0, report.PlaceholderRestoreRatio, 0.0001)
	require.Equal(t, 0, report.MissingSpineDocs)
	require.Equal(t, 0, report.UntranslatedSegments)
	require.Empty(t, report.Errors)
}

func TestEvaluate_DroppedPlaceholderFails(t *testing.T) {
	t.Parallel()

	segs := []segment.Segment{
		{
			ID:           "s1",
			Status:       segment.StatusTranslated,
			TargetText:   "texte sans token",
			Placeholders: []segment.Placeholder{{Token: `<x id="1"/>`}},
		},
	}

	report := Evaluate(&epub.Book{}, segs)

	require.False(t, report.Passed())
	require.Equal(t, 0, report.PlaceholdersRestored)
	require.InDelta(t, 0.0, report.PlaceholderRestoreRatio, 0.0001)
	require.NotEmpty(t, report.Errors)
}

func TestEvaluate_UntranslatedCounted(t *testing.T) {
	t.Parallel()

	segs := []segment.Segment{
		{ID: "s1", Status: segment.StatusTranslated, TargetText: "ok"},
		{ID: "s2", Status: segment.StatusPending},
	}

	report := Evaluate(&epub.Book{}, segs)

	require.Equal(t, 1, report.UntranslatedSegments)
	require.InDelta(t, 0.5, report.UntranslatedRatio, 0.0001)
	require.False(t, report.Passed())
}

func TestEvaluate_LeftoverSourceDetected(t *testing.T) {
	t.Parallel()

	segs := []segment.Segment{
		{ID: "s1", Status: segment.StatusTranslated, SourceText: "Hello world", TargetText: "Bonjour le monde"},
		{ID: "s2", Status: segment.StatusTranslated, SourceText: "Untranslated.", TargetText: "Untranslated."},
	}

	report := Evaluate(&epub.Book{}, segs)
	require.Equal(t, 1, report.LeftoverSourceSegments)
	require.NotEmpty(t, report.Warnings)
	// Leftover-source is a heuristic warning and does not fail the static gate.
	require.True(t, report.Passed())
}

func TestEvaluate_MissingSpineDoc(t *testing.T) {
	t.Parallel()

	book := &epub.Book{
		Manifest: epub.Manifest{Items: map[string]epub.ManifestItem{
			"chap1": {ID: "chap1", Href: "chapter1.xhtml", MediaType: xhtmlMediaType},
		}},
		Spine:    []epub.SpineItem{{IDRef: "chap1"}},
		Contents: map[string]*epub.XHTMLDocument{}, // chapter1 not parsed
	}

	report := Evaluate(book, nil)

	require.Equal(t, 1, report.MissingSpineDocs)
	require.False(t, report.Passed())
}

func TestEvaluate_NavSpineItemIgnored(t *testing.T) {
	t.Parallel()

	book := &epub.Book{
		Manifest: epub.Manifest{Items: map[string]epub.ManifestItem{
			"nav": {ID: "nav", Href: "nav.xhtml", MediaType: xhtmlMediaType, Properties: []string{"nav"}},
		}},
		Spine:    []epub.SpineItem{{IDRef: "nav"}},
		Contents: map[string]*epub.XHTMLDocument{},
	}

	report := Evaluate(book, nil)
	require.Equal(t, 0, report.MissingSpineDocs)
}
