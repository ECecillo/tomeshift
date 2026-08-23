package commandexec

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/ECecillo/ebooks-translator/internal/cli"
	"github.com/ECecillo/ebooks-translator/internal/config"
	"github.com/ECecillo/ebooks-translator/internal/epub"
	"github.com/ECecillo/ebooks-translator/internal/exitcode"
	"github.com/ECecillo/ebooks-translator/internal/glossary"
	"github.com/ECecillo/ebooks-translator/internal/narrative"
)

// runStyleProfile seeds or refreshes a series bible from an EPUB sample set.
func runStyleProfile(stdout, stderr io.Writer, cmd cli.StyleProfileCommand, cfg config.Config) int {
	store, err := narrative.NewSQLiteStore(cfg.NarrativePath)
	if err != nil {
		writef(stderr, "unable to open narrative store: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = store.Close() }()

	baseCtx, stop := signalContext()
	defer stop()
	ctx, cancel := withRunTimeout(baseCtx, cfg.RunTimeoutSeconds)
	defer cancel()

	samples, err := styleProfileSamples(ctx, cmd.Input, cmd.Samples)
	if err != nil {
		writef(stderr, "unable to collect style samples: %v\n", err)
		return exitCodeForError(err)
	}

	cachedChatClient, closeCache, err := newCachedChatClient(cfg)
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = closeCache() }()

	profiler := narrative.Profiler{
		Chat:  cachedChatClient,
		Model: cfg.LLMModel,
	}
	bible, err := profiler.Seed(ctx, samples)
	if err != nil {
		writef(stderr, "unable to seed style profile: %v\n", err)
		return exitCodeForError(err)
	}
	bible.SeriesID = cmd.Series

	// --merge enriches the existing series bible (e.g. seeding from a later
	// volume) instead of overwriting it.
	if cmd.Merge {
		existing, found, loadErr := store.Load(ctx, cmd.Series)
		if loadErr != nil {
			writef(stderr, "unable to load existing bible for merge: %v\n", loadErr)
			return exitcode.InvalidInputConfig
		}
		if found {
			var stats narrative.MergeStats
			bible, stats = narrative.MergeBibles(existing, bible)
			if stats.Lossy() {
				writef(stderr, "warning: merge dropped %d summaries already present in %s\n",
					stats.DroppedSummaries, cmd.Series)
			}
		}
	}

	if err := store.Save(ctx, bible); err != nil {
		writef(stderr, "unable to save style profile: %v\n", err)
		return exitcode.InvalidInputConfig
	}

	saved, found, err := store.Load(ctx, cmd.Series)
	if err != nil {
		writef(stderr, "unable to reload style profile: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	if !found {
		writef(stderr, "unable to reload style profile: series %q not found\n", cmd.Series)
		return exitcode.InvalidInputConfig
	}

	writef(stdout, "style-profile %s saved: version %d, %d samples, %d characters, %d address rules\n",
		cmd.Series, saved.Version, len(samples), len(saved.Characters), len(saved.Address))
	return exitcode.Success
}

// styleProfileSamples extracts up to limit spine-document samples in order.
func styleProfileSamples(ctx context.Context, path string, limit int) ([]string, error) {
	book, err := openBook(ctx, path)
	if err != nil {
		return nil, err
	}
	return styleSamplesFromBook(book, limit), nil
}

// styleSamplesFromBook is the sampling rule itself, split out so the cost
// projection can measure exactly what the style pass will send from a book it
// has already opened. Two copies of "the first N non-empty chapters" would be
// free to drift, and a projection that samples differently from the run is a
// projection of nothing.
func styleSamplesFromBook(book *epub.Book, limit int) []string {
	if limit < 1 {
		limit = 1
	}

	samples := make([]string, 0, limit)
	for _, chapter := range book.Chapters() {
		sample := chapter.Text()
		if sample == "" {
			continue
		}
		samples = append(samples, sample)
		if len(samples) >= limit {
			break
		}
	}

	return samples
}

// runSuggest scans an EPUB for recurring proper-noun candidates and prints them
// (and optionally writes a glossary JSON skeleton) to bootstrap a series glossary.
func runSuggest(stdout, stderr io.Writer, cmd cli.SuggestCommand) int {
	text, err := bookPlainText(context.Background(), cmd.Input)
	if err != nil {
		writef(stderr, "unable to read epub: %v\n", err)
		return exitCodeForError(err)
	}

	candidates := glossary.Suggest(text, cmd.MinCount)

	if cmd.Out != "" {
		data, err := json.MarshalIndent(glossary.SuggestTerms(candidates), "", "  ")
		if err != nil {
			writef(stderr, "unable to marshal glossary skeleton: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		if err := os.WriteFile(cmd.Out, append(data, '\n'), 0o644); err != nil {
			writef(stderr, "unable to write glossary skeleton: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		writef(stdout, "wrote %d candidates to %s (edit term_fr, then pass via --glossary)\n", len(candidates), cmd.Out)
	}

	limit := len(candidates)
	if limit > 30 {
		limit = 30
	}
	writef(stdout, "%d proper-noun candidates (min-count %d); top %d:\n", len(candidates), cmd.MinCount, limit)
	for _, c := range candidates[:limit] {
		writef(stdout, "  %5d  %s\n", c.Count, c.Term)
	}
	return exitcode.Success
}
