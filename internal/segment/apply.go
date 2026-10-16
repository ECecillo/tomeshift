package segment

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ECecillo/tomeshift/internal/epub"
)

// ApplyReport is what a consumer observes after ApplyTranslations. It accounts
// for every segment the document produced: Applied were restored into the live
// tree, Missing had no target (left holding their source markup), and Failed
// had a target the placeholder manager refused (also left as source). QA counts
// the Failed entries — a refused restore ships untranslated, not broken.
type ApplyReport struct {
	Applied int              // targets restored into the tree
	Missing []string         // segment ids present in doc but absent from targets (left as source)
	Failed  map[string]error // restore refused (node kept as source) — QA counts these
}

// ApplyTranslations re-segments doc with seg — deterministically, so the ids
// match the segmentation that produced targets — then splices each target back
// into the live tree through pm. A target for an id the document did not
// produce is a mismatched translation set: the restores done so far stand and
// the report is returned alongside a wrapped ErrUnknownSegment listing the
// offending ids. A restore that pm refuses never aborts the pass: it lands in
// Failed and its node keeps the source markup pm left untouched.
func ApplyTranslations(ctx context.Context, seg Segmenter, pm PlaceholderManager,
	doc *epub.XHTMLDocument, chapterID string, targets map[string]string) (ApplyReport, error) {
	segments, err := seg.Segment(ctx, doc, chapterID)
	if err != nil {
		return ApplyReport{}, fmt.Errorf("unable to re-segment chapter %s for re-application, err: %w", chapterID, err)
	}

	report := ApplyReport{Failed: map[string]error{}}
	produced := make(map[string]struct{}, len(segments))
	for i := range segments {
		s := segments[i]
		produced[s.ID] = struct{}{}

		target, ok := targets[s.ID]
		if !ok {
			report.Missing = append(report.Missing, s.ID)
			continue
		}
		if err := pm.Restore(target, s.Placeholders, s.Node); err != nil {
			report.Failed[s.ID] = fmt.Errorf("unable to restore target for segment %s, err: %w", s.ID, err)
			continue
		}
		report.Applied++
	}

	var unknown []string
	for id := range targets {
		if _, ok := produced[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return report, fmt.Errorf("unable to apply targets [%s], err: %w",
			strings.Join(unknown, ", "), ErrUnknownSegment)
	}

	return report, nil
}
