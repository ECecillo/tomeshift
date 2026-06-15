package qa

import (
	"slices"
	"strings"

	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/segment"
)

// Definition-of-done thresholds (see docs/ARCHITECTURE.md §15).
const (
	PlaceholderRestoreMin = 0.995
	UntranslatedMax       = 0.005
)

const xhtmlMediaType = "application/xhtml+xml"

// Report aggregates the V1 QA results for a run.
type Report struct {
	TotalSegments           int      `json:"total_segments"`
	PlaceholdersExpected    int      `json:"placeholders_expected"`
	PlaceholdersRestored    int      `json:"placeholders_restored"`
	PlaceholderRestoreRatio float64  `json:"placeholder_restore_ratio"`
	MissingSpineDocs        int      `json:"missing_spine_docs"`
	UntranslatedSegments    int      `json:"untranslated_segments"`
	UntranslatedRatio       float64  `json:"untranslated_ratio"`
	LeftoverSourceSegments  int      `json:"leftover_source_segments"`
	EPUBCheckPassed         bool     `json:"epubcheck_passed"`
	EPUBCheckSkipped        bool     `json:"epubcheck_skipped"`
	Warnings                []string `json:"warnings"`
	Errors                  []string `json:"errors"`
}

// Passed reports whether the static QA thresholds hold. epubcheck status is
// evaluated separately by the pipeline since it depends on the environment.
func (r Report) Passed() bool {
	return r.MissingSpineDocs == 0 &&
		r.PlaceholderRestoreRatio >= PlaceholderRestoreMin &&
		r.UntranslatedRatio <= UntranslatedMax &&
		len(r.Errors) == 0
}

// Evaluate runs the static QA checks over the translated segments and book.
func Evaluate(book *epub.Book, segments []segment.Segment) Report {
	report := Report{TotalSegments: len(segments)}

	checkPlaceholders(&report, segments)
	checkUntranslated(&report, segments)
	checkLeftoverSource(&report, segments)
	checkSpine(&report, book)

	return report
}

// checkLeftoverSource flags segments whose translated text is identical to the
// source after normalization — a strong signal the provider passed the text
// through untranslated. It is a heuristic warning, not a hard gate.
func checkLeftoverSource(report *Report, segments []segment.Segment) {
	for i := range segments {
		seg := &segments[i]
		if seg.TargetText == "" {
			continue
		}
		if segment.Normalize(seg.TargetText) == segment.Normalize(seg.SourceText) {
			report.LeftoverSourceSegments++
			report.Warnings = append(report.Warnings,
				"segment "+seg.ID+" appears untranslated (target equals source)")
		}
	}
}

// checkPlaceholders measures how many placeholder tokens survived into each
// segment's translated text.
func checkPlaceholders(report *Report, segments []segment.Segment) {
	for _, seg := range segments {
		for _, ph := range seg.Placeholders {
			report.PlaceholdersExpected++
			if strings.Contains(seg.TargetText, ph.Token) {
				report.PlaceholdersRestored++
			} else {
				report.Errors = append(report.Errors,
					"segment "+seg.ID+" dropped placeholder "+ph.Token)
			}
		}
	}

	if report.PlaceholdersExpected == 0 {
		report.PlaceholderRestoreRatio = 1
		return
	}
	report.PlaceholderRestoreRatio = float64(report.PlaceholdersRestored) / float64(report.PlaceholdersExpected)
}

// checkUntranslated counts segments that never received target text.
func checkUntranslated(report *Report, segments []segment.Segment) {
	for _, seg := range segments {
		if seg.Status == segment.StatusPending || seg.TargetText == "" {
			report.UntranslatedSegments++
		}
	}
	if len(segments) == 0 {
		return
	}
	report.UntranslatedRatio = float64(report.UntranslatedSegments) / float64(len(segments))
}

// checkSpine verifies every spine document resolves to a parsed content doc.
func checkSpine(report *Report, book *epub.Book) {
	if book == nil {
		return
	}

	for _, item := range book.Spine {
		manifestItem, ok := book.Manifest.Items[item.IDRef]
		if !ok {
			report.MissingSpineDocs++
			report.Errors = append(report.Errors, "spine idref "+item.IDRef+" has no manifest item")
			continue
		}

		// Only content documents are expected in Contents; nav is structural.
		if manifestItem.MediaType != xhtmlMediaType || slices.Contains(manifestItem.Properties, "nav") {
			continue
		}

		if _, ok := book.Contents[manifestItem.Href]; !ok {
			report.MissingSpineDocs++
			report.Errors = append(report.Errors, "spine document "+manifestItem.Href+" was not parsed")
		}
	}
}
