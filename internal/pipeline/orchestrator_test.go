package pipeline_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/cost"
	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/llm/llmtest"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/qa"
	"github.com/ECecillo/ebooks-translator/internal/segment"
	"github.com/ECecillo/ebooks-translator/internal/tm"
	"github.com/ECecillo/ebooks-translator/internal/translate"
	"github.com/ECecillo/ebooks-translator/internal/translate/nmttest"
)

var fixtureEntries = map[string]string{
	"mimetype": "application/epub+zip",
	"META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
	"OEBPS/content.opf": `<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf">
  <manifest>
    <item id="chap1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="chap1"/>
  </spine>
</package>`,
	"OEBPS/chapter1.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <body>
    <h1>Chapter One</h1>
    <p>Hello world.</p>
    <p>A <em>bold</em> claim.</p>
  </body>
</html>`,
}

func writeFixtureEPUB(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "source.epub")
	f, err := os.Create(path)
	require.NoError(t, err)

	zw := zip.NewWriter(f)

	w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	require.NoError(t, err)
	_, err = w.Write([]byte(fixtureEntries["mimetype"]))
	require.NoError(t, err)

	for name, content := range fixtureEntries {
		if name == "mimetype" {
			continue
		}
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return path
}

func newOrchestrator(t *testing.T, tr translate.Translator) *pipeline.Orchestrator {
	t.Helper()

	store, err := tm.NewSQLiteStore(filepath.Join(t.TempDir(), "tm.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	return &pipeline.Orchestrator{
		Extractor:  epub.ZipExtractor{},
		Parser:     epub.OPFParser{},
		Segmenter:  segment.NewParagraphSegmenter(),
		TM:         store,
		Translator: tr,
		Recomposer: epub.EPUBRecomposer{},
		EPUBCheck:  qa.EPUBChecker{Binary: "epubcheck-not-installed-xyz"}, // force skip
	}
}

func TestOrchestrator_Run_EndToEnd(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil) // prefixes "[fr] "
	t.Cleanup(srv.Close)

	orch := newOrchestrator(t, translate.NewHTTPTranslator(srv.URL, ""))

	outputDir := filepath.Join(t.TempDir(), "run")
	cfg := pipeline.RunConfig{
		InputPath:  writeFixtureEPUB(t),
		OutputDir:  outputDir,
		SourceLang: "en",
		TargetLang: "fr",
		RunID:      "test-run",
		BookID:     "fixture",
	}

	result, err := orch.Run(t.Context(), cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, 3, result.TotalSegments) // h1 + 2 paragraphs
	require.Equal(t, 3, result.NMTSegments)
	require.Equal(t, 0, result.TMHits)
	require.True(t, result.QA.Passed())
	require.True(t, result.QA.EPUBCheckSkipped)
	require.InDelta(t, 1.0, result.QA.PlaceholderRestoreRatio, 0.0001)

	// Artifacts exist.
	require.FileExists(t, filepath.Join(outputDir, "translated.epub"))
	require.FileExists(t, filepath.Join(outputDir, "report.json"))
	require.FileExists(t, filepath.Join(outputDir, "segments.jsonl"))

	// report.json round-trips.
	reportBytes, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
	require.NoError(t, err)
	var roundTrip pipeline.RunResult
	require.NoError(t, json.Unmarshal(reportBytes, &roundTrip))
	require.Equal(t, "test-run", roundTrip.RunID)

	// Translated EPUB contains the French-marked text with markup intact.
	chapter := readZipEntry(t, filepath.Join(outputDir, "translated.epub"), "OEBPS/chapter1.xhtml")
	require.Contains(t, chapter, "[fr] Hello world.")
	require.Contains(t, chapter, "<em>bold</em>", "inline markup must be preserved")
}

func TestOrchestrator_Run_TMReuse(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	store, err := tm.NewSQLiteStore(filepath.Join(t.TempDir(), "tm.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	mk := func() *pipeline.Orchestrator {
		return &pipeline.Orchestrator{
			Extractor:  epub.ZipExtractor{},
			Parser:     epub.OPFParser{},
			Segmenter:  segment.NewParagraphSegmenter(),
			TM:         store,
			Translator: translate.NewHTTPTranslator(srv.URL, ""),
			Recomposer: epub.EPUBRecomposer{},
			EPUBCheck:  qa.EPUBChecker{Binary: "epubcheck-not-installed-xyz"},
			CostModel:  cost.Model{PerSegmentUSD: 0.01}, // make reuse savings observable
		}
	}

	input := writeFixtureEPUB(t)

	first, err := mk().Run(t.Context(), pipeline.RunConfig{
		InputPath: input, OutputDir: filepath.Join(t.TempDir(), "r1"),
		SourceLang: "en", TargetLang: "fr", RunID: "r1", BookID: "b",
	})
	require.NoError(t, err)
	require.Equal(t, 3, first.NMTSegments)
	require.Equal(t, 0, first.TMHits)

	// Second run over the same book: every segment should now be a TM hit.
	second, err := mk().Run(t.Context(), pipeline.RunConfig{
		InputPath: input, OutputDir: filepath.Join(t.TempDir(), "r2"),
		SourceLang: "en", TargetLang: "fr", RunID: "r2", BookID: "b",
	})
	require.NoError(t, err)
	require.Equal(t, 0, second.NMTSegments)
	require.Equal(t, 3, second.TMHits)
	require.InDelta(t, 1.0, second.TMHitRatio, 0.0001)

	// All 3 segments reused -> actual cost 0, full projected cost saved.
	require.InDelta(t, 0.0, second.ActualCostUSD, 0.0001)
	require.Greater(t, second.CostSavingsUSD, 0.0)
	require.InDelta(t, second.EstimatedCostUSD, second.CostSavingsUSD, 0.0001)
}

func TestOrchestrator_Run_BudgetAbort(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	orch := newOrchestrator(t, translate.NewHTTPTranslator(srv.URL, ""))
	orch.CostModel = cost.Model{PerSegmentUSD: 1.0} // 3 segments -> ~$3
	orch.BudgetUSD = 0.5                            // way under projection

	result, err := orch.Run(t.Context(), pipeline.RunConfig{
		InputPath:  writeFixtureEPUB(t),
		OutputDir:  filepath.Join(t.TempDir(), "run"),
		SourceLang: "en", TargetLang: "fr", RunID: "budget", BookID: "b",
	})

	require.ErrorIs(t, err, cost.ErrBudgetExceeded)
	require.NotNil(t, result)
	require.Greater(t, result.EstimatedCostUSD, orch.BudgetUSD)
	require.Equal(t, 0, result.NMTSegments, "no provider calls when aborted pre-translation")
}

func TestOrchestrator_Run_DailyCapAbort(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	store, err := tm.NewSQLiteStore(filepath.Join(t.TempDir(), "tm.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// Pre-seed today's ledger close to the daily cap.
	now := time.Now().UTC()
	require.NoError(t, store.RecordRun(t.Context(), tm.RunSpend{
		RunID: "earlier", BookID: "b", StartedAt: now, EndedAt: now, ActualCostUSD: 9.5,
	}))

	orch := &pipeline.Orchestrator{
		Extractor:  epub.ZipExtractor{},
		Parser:     epub.OPFParser{},
		Segmenter:  segment.NewParagraphSegmenter(),
		TM:         store,
		Translator: translate.NewHTTPTranslator(srv.URL, ""),
		Recomposer: epub.EPUBRecomposer{},
		EPUBCheck:  qa.EPUBChecker{Binary: "epubcheck-not-installed-xyz"},
		CostModel:  cost.Model{PerSegmentUSD: 1.0}, // ~$3 projected
		DailyUSD:   10.0,                           // 9.5 + 3 > 10 -> abort
	}

	_, err = orch.Run(t.Context(), pipeline.RunConfig{
		InputPath:  writeFixtureEPUB(t),
		OutputDir:  filepath.Join(t.TempDir(), "run"),
		SourceLang: "en", TargetLang: "fr", RunID: "daily", BookID: "b",
	})
	require.ErrorIs(t, err, cost.ErrBudgetExceeded)
}

func TestOrchestrator_Run_LLMEscalation(t *testing.T) {
	t.Parallel()

	nmt := nmttest.NewServer(nil) // "[fr] " prefix
	t.Cleanup(nmt.Close)

	// Post-editor uppercases the draft while preserving placeholder tokens.
	post := llmtest.NewServer(func(draft string) string {
		return strings.ReplaceAll(draft, "[fr] ", "[FR] ")
	})
	t.Cleanup(post.Close)

	orch := newOrchestrator(t, translate.NewHTTPTranslator(nmt.URL, ""))
	orch.PostEditor = llm.NewHTTPPostEditor(post.URL, "")
	orch.EscalateMinChars = 0 // escalate every fresh segment

	outputDir := filepath.Join(t.TempDir(), "run")
	result, err := orch.Run(t.Context(), pipeline.RunConfig{
		InputPath:  writeFixtureEPUB(t),
		OutputDir:  outputDir,
		SourceLang: "en", TargetLang: "fr", RunID: "llm", BookID: "b",
	})

	require.NoError(t, err)
	require.Equal(t, 3, result.LLMSegments, "all fresh segments post-edited")
	require.True(t, result.QA.Passed())

	chapter := readZipEntry(t, filepath.Join(outputDir, "translated.epub"), "OEBPS/chapter1.xhtml")
	require.Contains(t, chapter, "[FR] Hello world.", "post-edited text must reach the output")
	require.Contains(t, chapter, "<em>bold</em>", "markup preserved through post-edit")
}

// fakeEstimator returns a fixed QE score for every segment.
type fakeEstimator struct{ score float64 }

func (f fakeEstimator) Estimate(_ context.Context, _, _ string) (float64, error) {
	return f.score, nil
}

func TestOrchestrator_Run_QualityDrivenEscalation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		qeScore    float64
		wantLLMSeg int
	}{
		{"low quality escalates all", 0.30, 3},
		{"high quality escalates none", 0.95, 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nmt := nmttest.NewServer(nil)
			t.Cleanup(nmt.Close)
			post := llmtest.NewServer(nil) // identity, preserves placeholders
			t.Cleanup(post.Close)

			orch := newOrchestrator(t, translate.NewHTTPTranslator(nmt.URL, ""))
			orch.PostEditor = llm.NewHTTPPostEditor(post.URL, "")
			orch.EscalateMinChars = 1 << 30 // length rule would never escalate
			orch.QualityEstimator = fakeEstimator{score: tt.qeScore}
			orch.EscalateBelowScore = 0.5

			result, err := orch.Run(t.Context(), pipeline.RunConfig{
				InputPath:  writeFixtureEPUB(t),
				OutputDir:  filepath.Join(t.TempDir(), "run"),
				SourceLang: "en", TargetLang: "fr", RunID: "qe", BookID: "b",
			})
			require.NoError(t, err)
			require.Equal(t, tt.wantLLMSeg, result.LLMSegments, "QE score must drive escalation, not length")
			require.Equal(t, 3, result.QualityScoredSegments)
			require.InDelta(t, tt.qeScore, result.AvgQualityScore, 1e-9)
		})
	}
}

func TestOrchestrator_Run_CancelledContextAborts(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	orch := newOrchestrator(t, translate.NewHTTPTranslator(srv.URL, ""))

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-cancel: the translate loop must bail out

	_, err := orch.Run(ctx, pipeline.RunConfig{
		InputPath:  writeFixtureEPUB(t),
		OutputDir:  filepath.Join(t.TempDir(), "run"),
		SourceLang: "en", TargetLang: "fr", RunID: "cancel", BookID: "b",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func readZipEntry(t *testing.T, zipPath, entry string) string {
	t.Helper()

	zr, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = zr.Close() })

	for _, f := range zr.File {
		if f.Name == entry {
			rc, err := f.Open()
			require.NoError(t, err)
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(rc)
			require.NoError(t, err)
			return string(data)
		}
	}
	t.Fatalf("entry %q not found in %q", entry, zipPath)
	return ""
}
