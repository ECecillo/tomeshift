package pipeline_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/cost"
	"github.com/ECecillo/tomeshift/internal/epub"
	"github.com/ECecillo/tomeshift/internal/llm"
	"github.com/ECecillo/tomeshift/internal/llm/llmtest"
	"github.com/ECecillo/tomeshift/internal/narrative"
	"github.com/ECecillo/tomeshift/internal/pipeline"
	"github.com/ECecillo/tomeshift/internal/qa"
	"github.com/ECecillo/tomeshift/internal/segment"
	"github.com/ECecillo/tomeshift/internal/tm"
	"github.com/ECecillo/tomeshift/internal/translate"
	"github.com/ECecillo/tomeshift/internal/translate/nmttest"
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

func TestOrchestrator_LiteraryModeRequiresExistingValidBible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		store func(t *testing.T) narrative.Store
		want  error
	}{
		{name: "store absent", want: narrative.ErrMissingBible},
		{
			name: "scope missing",
			store: func(t *testing.T) narrative.Store {
				return newNarrativeStore(t)
			},
			want: narrative.ErrMissingBible,
		},
		{
			name: "invalid bible",
			store: func(t *testing.T) narrative.Store {
				store := newNarrativeStore(t)
				require.NoError(t, store.Save(t.Context(), narrative.Bible{SeriesID: "series-a"}))
				return store
			},
			want: narrative.ErrInvalidBible,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orch := newOrchestrator(t, nil)
			if tt.store != nil {
				orch.NarrativeStore = tt.store(t)
			}
			_, err := orch.Run(t.Context(), pipeline.RunConfig{
				InputPath: writeFixtureEPUB(t), Mode: pipeline.ModeLiterary, SeriesID: "series-a",
			})
			require.Error(t, err)
			require.True(t, errors.Is(err, tt.want), "error: %v", err)
		})
	}
}

func newNarrativeStore(t *testing.T) *narrative.SQLiteStore {
	t.Helper()
	store, err := narrative.NewSQLiteStore(filepath.Join(t.TempDir(), "narrative.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
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

func TestOrchestrator_Run_FailedRunStillRecordsWhatItSpent(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	store, err := tm.NewSQLiteStore(filepath.Join(t.TempDir(), "tm.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	orch := newOrchestrator(t, translate.NewHTTPTranslator(srv.URL, ""))
	orch.TM = store
	orch.CostModel = cost.Model{Per1000CharsUSD: 2.0, FreeCharsPerMonth: 500_000}
	// Recompose into a path that cannot exist, so the run fails *after* every
	// segment has already been drafted and paid for.
	outputDir := filepath.Join(t.TempDir(), "run")
	cfg := pipeline.RunConfig{
		InputPath:  writeFixtureEPUB(t),
		OutputDir:  filepath.Join(outputDir, "no", "such", "dir", "\x00"),
		SourceLang: "en", TargetLang: "fr", RunID: "died", BookID: "b",
	}

	_, err = orch.Run(t.Context(), cfg)
	// Pin the stage: if this ever started failing earlier the test would still
	// go red, but for the wrong reason — nothing would have been drafted.
	var stageErr *pipeline.StageError
	require.ErrorAs(t, err, &stageErr)
	require.Equal(t, pipeline.StageRecompose, stageErr.Stage)

	// The provider does not refund a run that died downstream. Losing this
	// would leave the next run's free-tier projection short by the same amount.
	used, err := store.CharsSince(t.Context(), time.Now().UTC().Add(-time.Hour), "nmt-http")
	require.NoError(t, err)
	require.Positive(t, used, "characters already sent must survive the failure")
}

func TestOrchestrator_Run_RetriedCharsAreNotCarriedBetweenRuns(t *testing.T) {
	t.Parallel()

	// One transient failure per book, then success.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls%2 == 1 {
			http.Error(w, "flaky", http.StatusInternalServerError)
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"translated_text": "[fr] " + req.Text})
	}))
	t.Cleanup(srv.Close)

	store, err := tm.NewSQLiteStore(filepath.Join(t.TempDir(), "tm.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// `batch` reuses one orchestrator — and therefore one RetryingTranslator,
	// whose retried-character counter runs for its whole lifetime — across
	// every book.
	orch := newOrchestrator(t, translate.NewRetryingTranslator(
		translate.NewHTTPTranslator(srv.URL, ""), 3, time.Millisecond))
	orch.TM = store

	run := func(id string) int {
		result, err := orch.Run(t.Context(), pipeline.RunConfig{
			InputPath:  writeFixtureEPUB(t),
			OutputDir:  filepath.Join(t.TempDir(), id),
			SourceLang: "en", TargetLang: "fr", RunID: id, BookID: id,
		})
		require.NoError(t, err)
		return result.NMTChars
	}

	first := run("book-1")
	require.Positive(t, first, "the first run translated and retried")

	// The second run is the same book, so every segment is a TM hit: it calls
	// the provider zero times. It must therefore record zero characters, not
	// inherit the first run's retries from a counter that lives as long as the
	// translator — which is exactly how `batch` shares one.
	require.Zero(t, run("book-2"),
		"retried characters must be scoped to the run that re-sent them")
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

// recordingReporter captures what a run reported, so the assertions can be made
// on the sequence a watcher would have seen rather than on rendered text.
type recordingReporter struct {
	stages   []string
	total    int
	chapters int
	fresh    int
	reused   int
	costs    []float64
	finished int
}

func (r *recordingReporter) Stage(name string) { r.stages = append(r.stages, name) }
func (r *recordingReporter) Total(segments, chapters int) {
	r.total, r.chapters = segments, chapters
}
func (r *recordingReporter) Chapter(int) {}
func (r *recordingReporter) Advance(fresh, reused int) {
	r.fresh += fresh
	r.reused += reused
}
func (r *recordingReporter) Cost(usd float64) {
	r.costs = append(r.costs, usd)
}
func (r *recordingReporter) Finish() { r.finished++ }

// lastCost is the figure a watcher was left looking at.
func (r *recordingReporter) lastCost() float64 {
	if len(r.costs) == 0 {
		return 0
	}
	return r.costs[len(r.costs)-1]
}

func TestOrchestrator_Run_ReportsProgress(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	orch := newOrchestrator(t, translate.NewHTTPTranslator(srv.URL, ""))
	rep := &recordingReporter{}
	orch.Progress = rep

	cfg := pipeline.RunConfig{
		InputPath:  writeFixtureEPUB(t),
		OutputDir:  filepath.Join(t.TempDir(), "run"),
		SourceLang: "en",
		TargetLang: "fr",
		RunID:      "progress-run",
		BookID:     "fixture",
	}

	result, err := orch.Run(t.Context(), cfg)
	require.NoError(t, err)

	require.Equal(t, []string{"extract", "parse", "segment", "translate", "recompose", "qa"}, rep.stages)
	require.Equal(t, result.TotalSegments, rep.total)
	require.Equal(t, 1, rep.chapters)
	// Every segment is accounted for exactly once, and the split matches the
	// report — which is what makes the bar's percentage mean the same thing as
	// report.json.
	require.Equal(t, result.NMTSegments, rep.fresh)
	require.Equal(t, result.TMHits, rep.reused)
	require.Equal(t, result.TotalSegments, rep.fresh+rep.reused)
	require.Equal(t, 1, rep.finished)
}

func TestOrchestrator_Run_ReportsProgress_TMReuseOnRerun(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	orch := newOrchestrator(t, translate.NewHTTPTranslator(srv.URL, ""))
	input := writeFixtureEPUB(t)
	cfg := pipeline.RunConfig{
		InputPath:  input,
		OutputDir:  filepath.Join(t.TempDir(), "first"),
		SourceLang: "en",
		TargetLang: "fr",
		RunID:      "first",
		BookID:     "fixture",
	}
	_, err := orch.Run(t.Context(), cfg)
	require.NoError(t, err)

	rep := &recordingReporter{}
	orch.Progress = rep
	cfg.OutputDir = filepath.Join(t.TempDir(), "second")
	cfg.RunID = "second"
	result, err := orch.Run(t.Context(), cfg)
	require.NoError(t, err)

	require.Equal(t, result.TotalSegments, rep.reused, "a re-run is all TM reuse")
	require.Zero(t, rep.fresh)
}

func TestOrchestrator_Run_ReportsFinishOnFailure(t *testing.T) {
	t.Parallel()

	orch := newOrchestrator(t, nil)
	rep := &recordingReporter{}
	orch.Progress = rep

	_, err := orch.Run(t.Context(), pipeline.RunConfig{
		InputPath:  filepath.Join(t.TempDir(), "absent.epub"),
		OutputDir:  filepath.Join(t.TempDir(), "run"),
		SourceLang: "en",
		TargetLang: "fr",
		RunID:      "failed",
		BookID:     "absent",
	})
	require.Error(t, err)

	// The caller is about to write the error to the same stream, so the line
	// must have been closed first.
	require.Equal(t, 1, rep.finished)
	require.Equal(t, []string{"extract"}, rep.stages)
	require.Zero(t, rep.total)
}

func TestOrchestrator_Run_ReportsRunningCost(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	orch := newOrchestrator(t, translate.NewHTTPTranslator(srv.URL, ""))
	// A priced provider with no free allowance, so every character costs.
	orch.CostModel = cost.Model{Per1000CharsUSD: 20}
	rep := &recordingReporter{}
	orch.Progress = rep

	result, err := orch.Run(t.Context(), pipeline.RunConfig{
		InputPath:  writeFixtureEPUB(t),
		OutputDir:  filepath.Join(t.TempDir(), "run"),
		SourceLang: "en",
		TargetLang: "fr",
		RunID:      "cost-run",
		BookID:     "fixture",
	})
	require.NoError(t, err)

	// One repricing per fresh segment, monotonically rising, and landing exactly
	// on the figure report.json carries — the bar and the report are the same
	// arithmetic over the same counters.
	require.Len(t, rep.costs, result.NMTSegments)
	require.Positive(t, rep.lastCost())
	require.InDelta(t, result.ActualCostUSD, rep.lastCost(), 1e-9)
	for i := 1; i < len(rep.costs); i++ {
		require.GreaterOrEqual(t, rep.costs[i], rep.costs[i-1], "spend cannot go down")
	}
}

func TestOrchestrator_Run_TMHitsCostNothing(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	orch := newOrchestrator(t, translate.NewHTTPTranslator(srv.URL, ""))
	orch.CostModel = cost.Model{Per1000CharsUSD: 20}
	input := writeFixtureEPUB(t)
	cfg := pipeline.RunConfig{
		InputPath:  input,
		OutputDir:  filepath.Join(t.TempDir(), "first"),
		SourceLang: "en",
		TargetLang: "fr",
		RunID:      "first",
		BookID:     "fixture",
	}
	_, err := orch.Run(t.Context(), cfg)
	require.NoError(t, err)

	rep := &recordingReporter{}
	orch.Progress = rep
	cfg.OutputDir, cfg.RunID = filepath.Join(t.TempDir(), "second"), "second"
	result, err := orch.Run(t.Context(), cfg)
	require.NoError(t, err)

	// Nothing was sent to the provider, so nothing was repriced and the run is
	// free — which is the whole point of the TM.
	require.Empty(t, rep.costs)
	require.Zero(t, result.ActualCostUSD)
}

func TestOrchestrator_Run_GuardrailPricesOnlyTheRemainingWork(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	store, err := tm.NewSQLiteStore(filepath.Join(t.TempDir(), "tm.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	mk := func(budget float64) *pipeline.Orchestrator {
		return &pipeline.Orchestrator{
			Extractor:  epub.ZipExtractor{},
			Parser:     epub.OPFParser{},
			Segmenter:  segment.NewParagraphSegmenter(),
			TM:         store,
			Translator: translate.NewHTTPTranslator(srv.URL, ""),
			Recomposer: epub.EPUBRecomposer{},
			EPUBCheck:  qa.EPUBChecker{Binary: "epubcheck-not-installed-xyz"},
			CostModel:  cost.Model{PerSegmentUSD: 1},
			BudgetUSD:  budget,
		}
	}
	input := writeFixtureEPUB(t)
	cfg := func(id string) pipeline.RunConfig {
		return pipeline.RunConfig{
			InputPath: input, OutputDir: filepath.Join(t.TempDir(), id),
			SourceLang: "en", TargetLang: "fr", RunID: id, BookID: "b",
		}
	}

	// The book is 3 segments at $1 each. A $2 cap refuses it outright.
	_, err = mk(2).Run(t.Context(), cfg("refused"))
	require.ErrorIs(t, err, cost.ErrBudgetExceeded)

	// Translate it under a cap that allows it, filling the memory.
	first, err := mk(10).Run(t.Context(), cfg("first"))
	require.NoError(t, err)
	require.Equal(t, 3, first.NMTSegments)

	// Now the same $2 cap passes: nothing is left to pay for. Before the
	// projection consulted the memory, this run was refused for a bill of $3 it
	// would never have been sent — which is what a resume after a rate limit
	// looks like.
	second, err := mk(2).Run(t.Context(), cfg("second"))
	require.NoError(t, err)
	require.Equal(t, 3, second.TMHits)
	require.Zero(t, second.NMTSegments)
	require.InDelta(t, 0.0, second.ActualCostUSD, 1e-9)

	// And the report still knows what the book would have cost without the
	// memory, which is the only way the savings figure means anything.
	require.InDelta(t, 3.0, second.EstimatedCostUSD, 1e-9)
	require.InDelta(t, 3.0, second.CostSavingsUSD, 1e-9)
}

func TestOrchestrator_Run_ADraftIsReusableOnlyForTheModeThatOutputsIt(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	store, err := tm.NewSQLiteStore(filepath.Join(t.TempDir(), "tm.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	orch := &pipeline.Orchestrator{
		Extractor:  epub.ZipExtractor{},
		Parser:     epub.OPFParser{},
		Segmenter:  segment.NewParagraphSegmenter(),
		TM:         store,
		Translator: translate.NewHTTPTranslator(srv.URL, ""),
		Recomposer: epub.EPUBRecomposer{},
		EPUBCheck:  qa.EPUBChecker{Binary: "epubcheck-not-installed-xyz"},
		CostModel:  cost.Model{PerSegmentUSD: 1},
		BudgetUSD:  2, // the whole book is $3, so only reuse can let it start
	}
	input := writeFixtureEPUB(t)

	// An NMT run fills the memory with drafts.
	_, err = orch.Run(t.Context(), pipeline.RunConfig{
		InputPath: input, OutputDir: filepath.Join(t.TempDir(), "nmt"),
		SourceLang: "en", TargetLang: "fr", RunID: "nmt", BookID: "b",
		Mode: pipeline.ModeNMT,
	})
	require.ErrorIs(t, err, cost.ErrBudgetExceeded, "nothing cached yet: refused")

	orch.BudgetUSD = 10
	_, err = orch.Run(t.Context(), pipeline.RunConfig{
		InputPath: input, OutputDir: filepath.Join(t.TempDir(), "nmt2"),
		SourceLang: "en", TargetLang: "fr", RunID: "nmt2", BookID: "b",
	})
	require.NoError(t, err)

	// NMT mode reuses those drafts, so the tight cap now passes.
	orch.BudgetUSD = 2
	reused, err := orch.Run(t.Context(), pipeline.RunConfig{
		InputPath: input, OutputDir: filepath.Join(t.TempDir(), "nmt3"),
		SourceLang: "en", TargetLang: "fr", RunID: "nmt3", BookID: "b",
	})
	require.NoError(t, err)
	require.Equal(t, 3, reused.TMHits)

	// Literary mode does not: a draft is an intermediate it has to redo, so the
	// same cached book is projected at full price and the cap refuses it. The
	// projection agreeing with lookupTranslation on this is the whole point —
	// a projection that counted drafts would wave through a run that then pays
	// for every segment.
	orch.NarrativeStore = newNarrativeStore(t)
	require.NoError(t, orch.NarrativeStore.Save(t.Context(), narrative.Bible{
		SeriesID:   "series-a",
		StyleGuide: "Français littéraire souple.",
		Summaries:  []narrative.Summary{{VolumeID: "volume-01", Summary: "Synopsis."}},
	}))
	_, err = orch.Run(t.Context(), pipeline.RunConfig{
		InputPath: input, OutputDir: filepath.Join(t.TempDir(), "lit"),
		SourceLang: "en", TargetLang: "fr", RunID: "lit", BookID: "b",
		Mode: pipeline.ModeLiterary, SeriesID: "series-a",
	})
	require.ErrorIs(t, err, cost.ErrBudgetExceeded)
}
