package segment

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/ECecillo/tomeshift/internal/epub"
)

// renderNodes renders every translatable node of doc in order and joins the
// results, giving a stable byte image of the live tree to compare before and
// after re-application.
func renderNodes(t *testing.T, doc *epub.XHTMLDocument) string {
	t.Helper()

	var sb strings.Builder
	for _, cn := range doc.Nodes {
		require.NoError(t, html.Render(&sb, cn.Node))
	}
	return sb.String()
}

func TestApplyTranslations(t *testing.T) {
	t.Parallel()

	seg := NewParagraphSegmenter()
	pm := InlinePlaceholderManager{}

	type expected struct {
		applied   int
		missing   []string
		failed    []string // ids expected in Failed
		failedErr error    // sentinel wrapped inside each Failed value
		err       error    // wrapped top-level error, if any
		// renderEqualsSource asserts the applied tree is byte-identical to the
		// pristine source (nothing was rewritten).
		renderEqualsSource bool
		// contains lists substrings the applied render must include.
		contains []string
	}

	cases := []struct {
		name        string
		fragment    string
		makeTargets func(segs []Segment) map[string]string
		expected    expected
	}{
		{
			name:     "identity round trip feeds source back unchanged",
			fragment: `<body><p>First paragraph.</p><p>Second one.</p></body>`,
			makeTargets: func(segs []Segment) map[string]string {
				targets := map[string]string{}
				for _, s := range segs {
					targets[s.ID] = s.SourceText
				}
				return targets
			},
			expected: expected{applied: 2, renderEqualsSource: true},
		},
		{
			name:     "inline tokens restore em and strong",
			fragment: `<body><p>Hello <em>world</em> and <strong>more</strong>.</p></body>`,
			makeTargets: func(segs []Segment) map[string]string {
				src := segs[0].SourceText
				fr := strings.ReplaceAll(src, "world", "monde")
				fr = strings.ReplaceAll(fr, "more", "plus")
				fr = strings.ReplaceAll(fr, "Hello", "Bonjour")
				fr = strings.ReplaceAll(fr, " and ", " et ")
				return map[string]string{segs[0].ID: fr}
			},
			expected: expected{
				applied:  1,
				contains: []string{"<em>monde</em>", "<strong>plus</strong>"},
			},
		},
		{
			name:     "unknown target id yields ErrUnknownSegment",
			fragment: `<body><p>Only paragraph.</p></body>`,
			makeTargets: func(segs []Segment) map[string]string {
				return map[string]string{
					segs[0].ID:       "Seul paragraphe.",
					"ch_0001_p_9999": "ghost",
				}
			},
			expected: expected{applied: 1, err: ErrUnknownSegment},
		},
		{
			name:     "dropped token lands in Failed and leaves the node as source",
			fragment: `<body><p>Keep <em>this</em> intact.</p></body>`,
			makeTargets: func(segs []Segment) map[string]string {
				// Drop the closing token of the inline pair: Restore refuses and
				// the node keeps its original markup.
				broken := strings.Replace(segs[0].SourceText, `<x id="2"/>`, "", 1)
				return map[string]string{segs[0].ID: broken}
			},
			expected: expected{
				applied:            0,
				failed:             []string{"ch_0001_p_0001"},
				failedErr:          ErrMissingPlaceholder,
				renderEqualsSource: true,
			},
		},
		{
			name:     "segment without a target is reported Missing",
			fragment: `<body><p>Translated.</p><p>Left alone.</p></body>`,
			makeTargets: func(segs []Segment) map[string]string {
				return map[string]string{segs[0].ID: segs[0].SourceText}
			},
			expected: expected{
				applied:            1,
				missing:            []string{"ch_0001_p_0002"},
				renderEqualsSource: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pristine := docFromParagraphs(t, tc.fragment)
			sourceRender := renderNodes(t, pristine)

			apply := docFromParagraphs(t, tc.fragment)
			segs, err := seg.Segment(t.Context(), apply, "ch_0001")
			require.NoError(t, err)
			targets := tc.makeTargets(segs)

			report, err := ApplyTranslations(t.Context(), seg, pm, apply, "ch_0001", targets)

			if tc.expected.err != nil {
				require.ErrorIs(t, err, tc.expected.err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.expected.applied, report.Applied)
			require.ElementsMatch(t, tc.expected.missing, report.Missing)

			require.Len(t, report.Failed, len(tc.expected.failed))
			for _, id := range tc.expected.failed {
				require.Contains(t, report.Failed, id)
				if tc.expected.failedErr != nil {
					require.ErrorIs(t, report.Failed[id], tc.expected.failedErr)
				}
			}

			applied := renderNodes(t, apply)
			if tc.expected.renderEqualsSource {
				require.Equal(t, sourceRender, applied)
			}
			for _, want := range tc.expected.contains {
				require.Contains(t, applied, want)
			}
		})
	}
}

// TestApplyTranslations_ResegmentationIsDeterministic pins the invariant
// ApplyTranslations relies on: segmenting the same document twice yields the
// same ids and hashes, so targets keyed on the first segmentation address the
// segments the second one restores.
func TestApplyTranslations_ResegmentationIsDeterministic(t *testing.T) {
	t.Parallel()

	fragment := `<body><p>Alpha <em>beta</em>.</p><p>Gamma delta.</p></body>`
	seg := NewParagraphSegmenter()

	first, err := seg.Segment(t.Context(), docFromParagraphs(t, fragment), "ch_0001")
	require.NoError(t, err)
	second, err := seg.Segment(t.Context(), docFromParagraphs(t, fragment), "ch_0001")
	require.NoError(t, err)

	require.Len(t, second, len(first))
	for i := range first {
		require.Equal(t, first[i].ID, second[i].ID)
		require.Equal(t, first[i].Hash, second[i].Hash)
	}
}

// TestSegment_Unit_JSONSchema proves the serialisable projection carries exactly
// the schema §16.1 field names (minus schema_version/run/book ids), so a unit
// written to work/<chapter>.jsonl round-trips against the documented contract.
func TestSegment_Unit_JSONSchema(t *testing.T) {
	t.Parallel()

	s := Segment{
		ID:             "ch_0001_p_0012",
		ChapterID:      "ch_0001",
		NodePath:       "/html[1]/body[1]/p[12]",
		SourceText:     `Source <x id="1"/>text`,
		NormalizedText: `source <x id="1"/>text`,
		Hash:           "deadbeef",
		Placeholders:   []Placeholder{{Token: `<x id="1"/>`}},
		TargetText:     `Texte <x id="1"/>traduit`,
		Status:         StatusTranslated,
	}

	raw, err := json.Marshal(s.Unit())
	require.NoError(t, err)

	var got map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &got))

	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	expected := []string{
		"chapter_id", "node_path", "normalized_source_text", "placeholders",
		"segment_hash", "segment_id", "source_text", "status", "target_text",
	}
	sort.Strings(expected)
	require.Equal(t, expected, keys)

	// The single placeholder is flattened to its token.
	require.JSONEq(t, `["<x id=\"1\"/>"]`, string(got["placeholders"]))
}
