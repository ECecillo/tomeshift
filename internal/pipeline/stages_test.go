package pipeline_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/pipeline"
	"github.com/ECecillo/ebooks-translator/internal/qa"
	"github.com/ECecillo/ebooks-translator/internal/segment"
	"github.com/ECecillo/ebooks-translator/internal/tm"
	"github.com/ECecillo/ebooks-translator/internal/translate"
)

// The stage bodies own the rules the flow trusts by construction: which mode
// persists a draft, when a draft is a reusable hit, and that glossary terms are
// protected around the provider call. These test them directly against real
// collaborators, where the old orchestrator_test could only reach them through
// a whole run.

// mangleServer is an NMT mock that rewrites a term, so the glossary's protection
// is the only thing that can keep the canonical French out of the provider's
// reach.
func mangleServer(t *testing.T, from, to string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := req.Text
		if from != "" {
			out = strings.ReplaceAll(out, from, to)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"translated_text": "[fr] " + out})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func newTestStages(t *testing.T, store tm.Store, endpoint string, gloss *glossary.Glossary) *pipeline.Stages {
	t.Helper()
	return &pipeline.Stages{
		Extractor:  epub.ZipExtractor{},
		Parser:     epub.OPFParser{},
		Segmenter:  segment.NewParagraphSegmenter(),
		TM:         store,
		Translator: translate.NewHTTPTranslator(endpoint, ""),
		Recomposer: epub.EPUBRecomposer{},
		EPUBCheck:  qa.EPUBChecker{Binary: "epubcheck-not-installed-xyz"},
		Glossary:   gloss,
	}
}

// writeChapterFixture writes a one-chapter EPUB whose body is bodyHTML.
func writeChapterFixture(t *testing.T, bodyHTML string) string {
	t.Helper()
	entries := map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"OEBPS/content.opf":      `<?xml version="1.0"?><package version="3.0" xmlns="http://www.idpf.org/2007/opf"><manifest><item id="c1" href="c1.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c1"/></spine></package>`,
		"OEBPS/c1.xhtml":         `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body>` + bodyHTML + `</body></html>`,
	}

	path := filepath.Join(t.TempDir(), "source.epub")
	f, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	require.NoError(t, err)
	_, err = w.Write([]byte(entries["mimetype"]))
	require.NoError(t, err)
	for name, content := range entries {
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

func newTestStore(t *testing.T) *tm.SQLiteStore {
	t.Helper()
	store, err := tm.NewSQLiteStore(filepath.Join(t.TempDir(), "tm.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStages_DraftPersistenceFollowsTheMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		persistDrafts bool
		expectStored  bool
	}{
		{name: "nmt persists its drafts", persistDrafts: true, expectStored: true},
		{name: "literary does not persist drafts", persistDrafts: false, expectStored: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t)
			input := writeChapterFixture(t, `<p>Only sentence.</p>`)
			out := filepath.Join(t.TempDir(), "out")
			s := newTestStages(t, store, mangleServer(t, "", ""), nil)
			ref := pipeline.RunRef{RunID: "r", BookID: "b", OutputDir: out, InputPath: input}

			ctx := context.Background()
			manifest, err := s.PrepareBook(ctx, pipeline.BookInput{InputPath: input, OutputDir: out, RunID: "r", BookID: "b"})
			require.NoError(t, err)
			chID := manifest.Chapters[0].ID

			plan, err := s.ResolveChapter(ctx, pipeline.ResolveInput{Ref: ref, ChapterID: chID, ReusesDrafts: true})
			require.NoError(t, err)
			require.NotEmpty(t, plan.Fresh)

			_, err = s.DraftChapter(ctx, pipeline.DraftInput{
				Ref: ref, ChapterID: chID, Fresh: plan.Fresh, PersistDrafts: tt.persistDrafts,
				SourceLang: "en", TargetLang: "fr",
			})
			require.NoError(t, err)

			_, found, err := store.Lookup(ctx, segment.Hash(segment.Normalize("Only sentence.")))
			require.NoError(t, err)
			require.Equal(t, tt.expectStored, found)
		})
	}
}

func TestStages_ResolveReusesADraftOnlyForTheModeThatOutputsIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		reusesDrafts bool
		wantReused   int
	}{
		{name: "nmt reuses the draft", reusesDrafts: true, wantReused: 1},
		{name: "literary redrafts it", reusesDrafts: false, wantReused: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t)
			input := writeChapterFixture(t, `<p>Only sentence.</p>`)
			out := filepath.Join(t.TempDir(), "out")
			s := newTestStages(t, store, mangleServer(t, "", ""), nil)
			ref := pipeline.RunRef{RunID: "r", BookID: "b", OutputDir: out, InputPath: input}
			ctx := context.Background()

			manifest, err := s.PrepareBook(ctx, pipeline.BookInput{InputPath: input, OutputDir: out, RunID: "r", BookID: "b"})
			require.NoError(t, err)
			chID := manifest.Chapters[0].ID

			// Seed a raw NMT draft (a draft-engine entry) for the segment.
			plan, err := s.ResolveChapter(ctx, pipeline.ResolveInput{Ref: ref, ChapterID: chID, ReusesDrafts: true})
			require.NoError(t, err)
			_, err = s.DraftChapter(ctx, pipeline.DraftInput{
				Ref: ref, ChapterID: chID, Fresh: plan.Fresh, PersistDrafts: true, SourceLang: "en", TargetLang: "fr",
			})
			require.NoError(t, err)

			// A fresh run resolves the same chapter under the mode's rule.
			out2 := filepath.Join(t.TempDir(), "out2")
			ref2 := pipeline.RunRef{RunID: "r2", BookID: "b", OutputDir: out2, InputPath: input}
			_, err = s.PrepareBook(ctx, pipeline.BookInput{InputPath: input, OutputDir: out2, RunID: "r2", BookID: "b"})
			require.NoError(t, err)

			got, err := s.ResolveChapter(ctx, pipeline.ResolveInput{Ref: ref2, ChapterID: chID, ReusesDrafts: tt.reusesDrafts})
			require.NoError(t, err)
			require.Equal(t, tt.wantReused, got.Reused)
		})
	}
}

func TestStages_DraftProtectsLockedGlossaryTerms(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	input := writeChapterFixture(t, `<p>Bilbo went home.</p>`)
	out := filepath.Join(t.TempDir(), "out")

	glossPath := filepath.Join(t.TempDir(), "glossary.json")
	require.NoError(t, os.WriteFile(glossPath, []byte(`[{"term_en":"Bilbo","term_fr":"Bilbon","locked":true}]`), 0o600))
	gloss, err := glossary.Load(glossPath)
	require.NoError(t, err)

	// The provider rewrites "Bilbo" to "WRONG" — but glossary protection removes
	// it before the request and substitutes the canonical term after.
	s := newTestStages(t, store, mangleServer(t, "Bilbo", "WRONG"), gloss)
	ref := pipeline.RunRef{RunID: "r", BookID: "b", OutputDir: out, InputPath: input}
	ctx := context.Background()

	manifest, err := s.PrepareBook(ctx, pipeline.BookInput{InputPath: input, OutputDir: out, RunID: "r", BookID: "b"})
	require.NoError(t, err)
	chID := manifest.Chapters[0].ID

	plan, err := s.ResolveChapter(ctx, pipeline.ResolveInput{Ref: ref, ChapterID: chID, ReusesDrafts: true})
	require.NoError(t, err)
	_, err = s.DraftChapter(ctx, pipeline.DraftInput{
		Ref: ref, ChapterID: chID, Fresh: plan.Fresh, PersistDrafts: true, SourceLang: "en", TargetLang: "fr",
	})
	require.NoError(t, err)

	units, err := pipeline.NewWorkspace(out).ReadChapter(chID)
	require.NoError(t, err)
	require.Len(t, units, 1)
	require.Contains(t, units[0].TargetText, "Bilbon", "the canonical term must be substituted back")
	require.NotContains(t, units[0].TargetText, "WRONG", "the provider must never have seen the term")
}

func TestStages_CommitPersistsOnlyRefinedLiteraryTranslations(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	input := writeChapterFixture(t, `<p>First.</p><p>Second.</p>`)
	out := filepath.Join(t.TempDir(), "out")
	s := newTestStages(t, store, mangleServer(t, "", ""), nil)
	ref := pipeline.RunRef{RunID: "r", BookID: "b", OutputDir: out, InputPath: input}
	ctx := context.Background()

	manifest, err := s.PrepareBook(ctx, pipeline.BookInput{InputPath: input, OutputDir: out, RunID: "r", BookID: "b"})
	require.NoError(t, err)
	chID := manifest.Chapters[0].ID

	// Hand-write the units: one refined (post-edited), one still a draft.
	ws := pipeline.NewWorkspace(out)
	units, err := ws.ReadChapter(chID)
	require.NoError(t, err)
	require.Len(t, units, 2)
	units[0].TargetText, units[0].Status = "Première.", segment.StatusPostEdited
	units[1].TargetText, units[1].Status = "brouillon", segment.StatusTranslated
	require.NoError(t, ws.WriteChapter(chID, units))

	res, err := s.CommitChapter(ctx, pipeline.CommitInput{
		Ref: ref, ChapterID: chID, Persist: true, SourceLang: "en", TargetLang: "fr",
	})
	require.NoError(t, err)
	require.Zero(t, res.RestoreFailures)

	// Only the refined segment reaches the TM, as a finished literary entry.
	refined, found, err := store.Lookup(ctx, segment.Hash(segment.Normalize("First.")))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, tm.EngineLiterary, refined.Engine)

	_, found, err = store.Lookup(ctx, segment.Hash(segment.Normalize("Second.")))
	require.NoError(t, err)
	require.False(t, found, "a draft segment must never be persisted in literary mode")
}
