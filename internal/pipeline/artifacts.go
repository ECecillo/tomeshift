package pipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ECecillo/ebooks-translator/internal/segment"
)

const (
	reportFileName   = "report.json"
	segmentsFileName = "segments.jsonl"
	metricsFileName  = "metrics.json"
)

// segmentRecord is the per-segment artifact written to segments.jsonl. It mirrors
// the translation-unit schema in docs/ARCHITECTURE.md §16.1.
type segmentRecord struct {
	SchemaVersion  int      `json:"schema_version"`
	RunID          string   `json:"run_id"`
	BookID         string   `json:"book_id"`
	ChapterID      string   `json:"chapter_id"`
	SegmentID      string   `json:"segment_id"`
	NodePath       string   `json:"node_path"`
	SourceText     string   `json:"source_text"`
	NormalizedText string   `json:"normalized_source_text"`
	SegmentHash    string   `json:"segment_hash"`
	Placeholders   []string `json:"placeholders"`
	TargetText     string   `json:"target_text"`
	Status         string   `json:"status"`
}

// WriteArtifacts writes report.json, metrics.json and segments.jsonl into the
// run output dir.
func WriteArtifacts(ref RunRef, result *RunResult, segments []segment.Segment) error {
	if err := os.MkdirAll(ref.OutputDir, 0o755); err != nil {
		return fmt.Errorf("unable to create run output dir %q, err: %w", ref.OutputDir, err)
	}
	if err := writeReport(filepath.Join(ref.OutputDir, reportFileName), result); err != nil {
		return err
	}
	if err := writeMetrics(filepath.Join(ref.OutputDir, metricsFileName), result); err != nil {
		return err
	}
	return writeSegments(filepath.Join(ref.OutputDir, segmentsFileName), ref, segments)
}

// writeMetrics emits a flat metrics snapshot using the canonical metric names
// from docs/ARCHITECTURE.md §10, ready for a metrics scraper in service mode.
func writeMetrics(path string, result *RunResult) error {
	var validationFailures int
	if !result.QA.EPUBCheckSkipped && !result.QA.EPUBCheckPassed {
		validationFailures = 1
	}

	metrics := map[string]float64{
		"translation_segments_total": float64(result.TotalSegments),
		// The provider's free tier is denominated in characters per calendar
		// month, not in dollars, so a run inside it costs nothing while still
		// consuming the allowance the next one is billed against. Cost alone
		// cannot show that burn.
		"translation_nmt_chars_total":           float64(result.NMTChars),
		"translation_cache_hit_ratio":           result.TMHitRatio,
		"translation_llm_escalation_ratio":      result.LLMEscalationRatio,
		"translation_cost_usd_total":            result.ActualCostUSD,
		"epub_validation_failures_total":        float64(validationFailures),
		"translation_placeholder_restore_ratio": result.QA.PlaceholderRestoreRatio,
	}
	if result.QualityScoredSegments > 0 {
		metrics["translation_quality_score"] = result.AvgQualityScore
	}
	if result.LLMInputTokens+result.LLMCacheReadTokens+result.LLMCacheCreationTokens > 0 {
		metrics["translation_llm_input_tokens"] = float64(result.LLMInputTokens)
		metrics["translation_llm_output_tokens"] = float64(result.LLMOutputTokens)
		metrics["translation_llm_cache_read_tokens"] = float64(result.LLMCacheReadTokens)
		metrics["translation_llm_cache_creation_tokens"] = float64(result.LLMCacheCreationTokens)
	}

	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return fmt.Errorf("unable to marshal metrics, err: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("unable to write metrics %q, err: %w", path, err)
	}
	return nil
}

func writeReport(path string, result *RunResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("unable to marshal run report, err: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("unable to write run report %q, err: %w", path, err)
	}
	return nil
}

func writeSegments(path string, ref RunRef, segments []segment.Segment) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("unable to create segments artifact %q, err: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)

	for i := range segments {
		seg := &segments[i]

		placeholders := make([]string, 0, len(seg.Placeholders))
		for _, ph := range seg.Placeholders {
			placeholders = append(placeholders, ph.Original)
		}

		rec := segmentRecord{
			SchemaVersion:  1,
			RunID:          ref.RunID,
			BookID:         ref.BookID,
			ChapterID:      seg.ChapterID,
			SegmentID:      seg.ID,
			NodePath:       seg.NodePath,
			SourceText:     seg.SourceText,
			NormalizedText: seg.NormalizedText,
			SegmentHash:    "sha256:" + seg.Hash,
			Placeholders:   placeholders,
			TargetText:     seg.TargetText,
			Status:         string(seg.Status),
		}
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("unable to encode segment %s, err: %w", seg.ID, err)
		}
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("unable to flush segments artifact, err: %w", err)
	}
	return nil
}
