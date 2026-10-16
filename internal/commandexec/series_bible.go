package commandexec

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/ECecillo/tomeshift/internal/cli"
	"github.com/ECecillo/tomeshift/internal/config"
	"github.com/ECecillo/tomeshift/internal/cost"
	"github.com/ECecillo/tomeshift/internal/exitcode"
	"github.com/ECecillo/tomeshift/internal/narrative"
)

// volumeSource is one EPUB of a series, in publication order.
type volumeSource struct {
	// ID is the filename stem, used as the summary's volume key and as the
	// suffix of the scoped series ID.
	ID   string
	Path string
	// Number is the parsed publication order, independent of lexical padding.
	Number int
}

// runSeriesBible builds the bible before any translation, from every volume on
// disk. Style, character voices and address rules are seeded from all of them;
// summaries are one synopsis per volume, produced by map-reduce, and scoped to
// volumes 1..N in the bible meant to translate volume N — so the model never
// receives the ending of a story it is rendering the beginning of.
//
// The output is an input to translation runs and is never written to during
// one. That is what makes a run's prompts byte-identical across chapters and a
// re-run free against the LLM cache.
func runSeriesBible(stdout, stderr io.Writer, cmd cli.SeriesBibleCommand, cfg config.Config) int {
	volumes, err := discoverVolumes(cmd.InputDir)
	if err != nil {
		writef(stderr, "unable to discover volumes: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	if len(volumes) == 0 {
		writef(stderr, "no .epub files found in %s\n", cmd.InputDir)
		return exitcode.InvalidInputConfig
	}

	baseCtx, stop := signalContext()
	defer stop()
	ctx, cancel := withRunTimeout(baseCtx, cfg.RunTimeoutSeconds)
	defer cancel()

	// Price the job before opening anything it would write to. Until this
	// existed, `series-bible` was the only command that could spend without a
	// projection or a cap — and the largest: one call per chapter across every
	// volume, which on a 33-volume series is over a thousand.
	if code := projectSeriesBible(ctx, stdout, stderr, cmd, cfg, volumes); code != exitcode.Success || cmd.DryRun {
		return code
	}

	store, err := narrative.NewSQLiteStore(cfg.NarrativePath)
	if err != nil {
		writef(stderr, "unable to open narrative store: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = store.Close() }()

	chat, closeCache, err := newCachedChatClient(cfg)
	if err != nil {
		writef(stderr, "%v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = closeCache() }()

	profiler := narrative.Profiler{Chat: chat, Model: cfg.LLMModel}

	base, err := seedSeriesStyle(ctx, profiler, cmd, volumes)
	if err != nil {
		writef(stderr, "unable to seed series style: %v\n", err)
		return exitCodeForError(err)
	}
	writef(stdout, "style pass: %d volumes, %d characters, %d address rules\n",
		len(volumes), len(base.Characters), len(base.Address))

	synopses := make([]narrative.Summary, 0, len(volumes))
	for _, volume := range volumes {
		synopsis, chapters, err := volumeSynopsis(ctx, stdout, profiler, store, cmd.Series, volume)
		if err != nil {
			writef(stderr, "unable to build synopsis for %s: %v\n", volume.ID, err)
			return exitCodeForError(err)
		}
		writef(stdout, "volume %s: %d chapters summarised, synopsis %d runes\n",
			volume.ID, chapters, len([]rune(synopsis)))
		synopses = append(synopses, narrative.Summary{VolumeID: volume.ID, Summary: synopsis})
	}

	if err := saveScopedBibles(ctx, stdout, stderr, store, cmd, base, synopses, volumes); err != nil {
		writef(stderr, "unable to save series bibles: %v\n", err)
		return exitCodeForError(err)
	}
	return exitcode.Success
}

// seedSeriesStyle runs the single sampling pass that produces style, voices and
// address rules. Sampling is the right method here and the wrong one for plot:
// register and idiolect are recoverable from excerpts, a story is not. The
// summaries the model volunteers are discarded — the map-reduce pass owns them.
func seedSeriesStyle(
	ctx context.Context, profiler narrative.Profiler,
	cmd cli.SeriesBibleCommand, volumes []volumeSource,
) (narrative.Bible, error) {
	var samples []string
	for _, volume := range volumes {
		volumeSamples, err := styleProfileSamples(ctx, volume.Path, cmd.Samples)
		if err != nil {
			return narrative.Bible{}, fmt.Errorf("unable to sample %q, err: %w", volume.ID, err)
		}
		samples = append(samples, volumeSamples...)
	}

	bible, err := profiler.Seed(ctx, samples)
	if err != nil {
		return narrative.Bible{}, err
	}
	bible.SeriesID = cmd.Series
	bible.Summaries = nil
	if err := narrative.ValidateStyleBible(bible); err != nil {
		return narrative.Bible{}, fmt.Errorf("unable to validate seeded series style, err: %w", err)
	}
	return bible, nil
}

// volumeSynopsis is the map-reduce of decision 5: one summary per chapter, then
// one reduction over those summaries. The per-chapter summaries are persisted
// rather than discarded — they are exactly what a per-chapter granularity
// option would need if per-volume synopses ever prove too coarse.
func volumeSynopsis(
	ctx context.Context, stdout io.Writer, profiler narrative.Profiler,
	store narrative.Store, seriesID string, volume volumeSource,
) (string, int, error) {
	book, err := openBook(ctx, volume.Path)
	if err != nil {
		return "", 0, err
	}
	// The same enumeration the translation pipeline segments, so a summary
	// keyed ch_0012 and the translated chapter ch_0012 are the same document.
	chapters := book.Chapters()
	if len(chapters) == 0 {
		return "", 0, fmt.Errorf("unable to read chapters of %q, err: %w", volume.ID, narrative.ErrEmptySource)
	}

	perChapter := make([]narrative.Summary, 0, len(chapters))
	texts := make([]string, 0, len(chapters))
	for _, chapter := range chapters {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		// Blank spine documents (title pages, dividers) stay numbered so the
		// chapter IDs line up with the translation pipeline's, but there is
		// nothing to summarise and no reason to pay for a call.
		text := chapter.Text()
		if text == "" {
			continue
		}

		summary, err := profiler.SummariseChapter(ctx, chapter.ID, text)
		if err != nil {
			return "", 0, err
		}
		perChapter = append(perChapter, narrative.Summary{
			VolumeID:  volume.ID,
			ChapterID: chapter.ID,
			Summary:   summary,
		})
		texts = append(texts, summary)
		writef(stdout, "  %s/%s summarised (%d/%d)\n", volume.ID, chapter.ID, len(texts), len(chapters))
	}

	if err := store.PutChapterSummaries(ctx, seriesID, volume.ID, perChapter); err != nil {
		return "", 0, err
	}

	synopsis, err := profiler.SynthesiseVolume(ctx, volume.ID, texts)
	if err != nil {
		return "", 0, err
	}
	return synopsis, len(perChapter), nil
}

// saveScopedBibles writes one bible per volume N holding the synopses of
// volumes 1..N, then the same content once more under the bare series ID so the
// newest volume is translatable without naming a scope.
func saveScopedBibles(
	ctx context.Context, stdout, stderr io.Writer, store narrative.Store,
	cmd cli.SeriesBibleCommand, base narrative.Bible,
	synopses []narrative.Summary, volumes []volumeSource,
) error {
	bibles := make([]narrative.Bible, 0, len(volumes)+1)
	for n, volume := range volumes {
		scoped := base
		scoped.Summaries = append([]narrative.Summary(nil), synopses[:n+1]...)
		prepared, err := prepareBible(ctx, stderr, store, cmd.Merge,
			narrative.ScopedSeriesID(cmd.Series, volume.ID), scoped)
		if err != nil {
			return err
		}
		if err := validateSynopsisSet(prepared, volumes[:n+1]); err != nil {
			return err
		}
		bibles = append(bibles, prepared)
	}

	latest := base
	latest.Summaries = append([]narrative.Summary(nil), synopses...)
	prepared, err := prepareBible(ctx, stderr, store, cmd.Merge, cmd.Series, latest)
	if err != nil {
		return err
	}
	if err := validateSynopsisSet(prepared, volumes); err != nil {
		return err
	}
	bibles = append(bibles, prepared)

	if err := store.SaveAll(ctx, bibles); err != nil {
		return err
	}
	for _, bible := range bibles {
		saved, found, err := store.Load(ctx, bible.SeriesID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("unable to reload bible %q after save, err: %w", bible.SeriesID, narrative.ErrMissingBible)
		}
		writef(stdout, "bible %s saved: version %d, %d volume synopses\n",
			saved.SeriesID, saved.Version, len(saved.Summaries))
	}
	return nil
}

func prepareBible(
	ctx context.Context, stderr io.Writer, store narrative.Store,
	merge bool, seriesID string, bible narrative.Bible,
) (narrative.Bible, error) {
	bible.SeriesID = seriesID
	bible.Summaries = append([]narrative.Summary(nil), bible.Summaries...)
	generatedSummaries := append([]narrative.Summary(nil), bible.Summaries...)

	if merge {
		existing, found, err := store.Load(ctx, seriesID)
		if err != nil {
			return narrative.Bible{}, fmt.Errorf("unable to load %q for merge, err: %w", seriesID, err)
		}
		if found {
			var stats narrative.MergeStats
			bible, stats = narrative.MergeBibles(existing, bible)
			bible.SeriesID = seriesID
			// Synopsis generation is a complete snapshot. Merge may preserve
			// curated style fields, but it must not resurrect stale/future rows.
			bible.Summaries = generatedSummaries
			if stats.Lossy() {
				writef(stderr, "warning: merge into %s dropped %d summaries already present\n",
					seriesID, stats.DroppedSummaries)
			}
		}
	}
	return bible, nil
}

func validateSynopsisSet(bible narrative.Bible, volumes []volumeSource) error {
	if err := narrative.ValidateBible(bible); err != nil {
		return fmt.Errorf("unable to validate generated bible %q, err: %w", bible.SeriesID, err)
	}
	if len(bible.Summaries) != len(volumes) {
		return fmt.Errorf("unable to validate generated bible %q synopsis count, err: %w", bible.SeriesID, narrative.ErrInvalidBible)
	}
	for i, volume := range volumes {
		if bible.Summaries[i].VolumeID != volume.ID {
			return fmt.Errorf("unable to validate generated bible %q synopsis %d, err: %w", bible.SeriesID, i, narrative.ErrInvalidBible)
		}
	}
	return nil
}

var volumeNumberPattern = regexp.MustCompile(`^(.*?)([0-9]+)$`)

// discoverVolumes derives publication order from the trailing volume number.
// A shared non-numeric prefix and unique numbers make the order unambiguous;
// zero-padding is accepted but no longer required for correctness.
// styleCallTokenWarning is the input size above which the single style call is
// worth flagging. It is the smallest context window among the models this CLI
// is routinely pointed at (Haiku 4.5, 200K), so a style pass beyond it does not
// merely cost a lot — it fails outright on that model.
const styleCallTokenWarning = 200_000

// projectSeriesBible measures what the run is about to send, prints the
// projection, and enforces the per-book cap before the first provider call.
//
// The cap is named per book and this job spans a whole series, which is the
// honest reading: a bible build is one invocation, and the per-book cap is the
// only per-invocation cap the configuration has. The message says so, because a
// 33-volume build refused by a 2 $ default should point at the setting rather
// than look like a bug.
func projectSeriesBible(
	ctx context.Context, stdout, stderr io.Writer,
	cmd cli.SeriesBibleCommand, cfg config.Config, volumes []volumeSource,
) int {
	shape, err := measureBibleShape(ctx, cmd, volumes)
	if err != nil {
		writef(stderr, "unable to measure the series: %v\n", err)
		return exitCodeForError(err)
	}

	projection := cfg.CostModel().ProjectSeriesBible(shape)

	writef(stdout, "series-bible projection for %s: %d volumes, %d chapters, %d calls, samples=%d\n",
		cmd.Series, projection.Volumes, projection.Chapters, projection.Calls, cmd.Samples)
	writef(stdout, "%-8s %8s %12s %12s %12s\n", "stage", "calls", "in_tokens", "out_tokens", "usd")
	for _, stage := range projection.Stages {
		writef(stdout, "%-8s %8d %12d %12d %12.4f\n",
			stage.Name, stage.Calls, stage.InputTokens, stage.OutputTokens, stage.EstimatedUSD)
	}
	writef(stdout, "%-8s %8d %12d %12d %12.4f\n", "total",
		projection.Calls, projection.InputTokens, projection.OutputTokens, projection.EstimatedUSD)

	// The style pass is one request carrying every sampled chapter of every
	// volume, so it grows with the series while --samples looks like a per-book
	// knob. It is the one way this command can fail on its very first call.
	for _, stage := range projection.Stages {
		if stage.Name == "style" && stage.InputTokens > styleCallTokenWarning {
			writef(stderr,
				"warning: the style pass sends %d tokens in a single request (samples=%d x %d volumes); lower --samples or expect it to exceed the model's context window\n",
				stage.InputTokens, cmd.Samples, projection.Volumes)
		}
	}

	if projection.EstimatedUSD == 0 {
		// No rates configured, or a subscription-backed provider. The token
		// counts above are then the whole answer, and the cap has nothing to
		// compare against — say so rather than let silence read as approval.
		writef(stdout, "no LLM rates configured: the token counts above are the projection, and no budget cap applies\n")
		return exitcode.Success
	}

	if cfg.PerBookBudgetUSD > 0 && projection.EstimatedUSD > cfg.PerBookBudgetUSD {
		writef(stderr, "unable to start series-bible (estimated $%.4f > TOMESHIFT_PER_BOOK_BUDGET_USD $%.4f over %d volumes): %v\n",
			projection.EstimatedUSD, cfg.PerBookBudgetUSD, projection.Volumes, cost.ErrBudgetExceeded)
		return exitcode.BudgetExceeded
	}

	return exitcode.Success
}

// measureBibleShape opens every volume once to count what the three stages will
// send. It parses each EPUB a second time — volumeSynopsis opens them again —
// which is the cheap side of the trade: holding every parsed book in memory to
// save one pass costs far more than the seconds it saves before a job that
// makes one provider call per chapter.
func measureBibleShape(
	ctx context.Context, cmd cli.SeriesBibleCommand, volumes []volumeSource,
) (cost.BibleShape, error) {
	shape := cost.BibleShape{Volumes: make([]cost.VolumeShape, 0, len(volumes))}

	for _, volume := range volumes {
		if err := ctx.Err(); err != nil {
			return cost.BibleShape{}, err
		}

		book, err := openBook(ctx, volume.Path)
		if err != nil {
			return cost.BibleShape{}, fmt.Errorf("unable to read %q, err: %w", volume.ID, err)
		}

		measured := cost.VolumeShape{ID: volume.ID}
		for _, chapter := range book.Chapters() {
			text := chapter.Text()
			if text == "" {
				continue // never sent, never billed
			}
			measured.Chapters++
			measured.SourceChars += utf8.RuneCountInString(text)
		}
		shape.Volumes = append(shape.Volumes, measured)

		for _, sample := range styleSamplesFromBook(book, cmd.Samples) {
			shape.StyleSampleChars += utf8.RuneCountInString(sample)
		}
	}

	return shape, nil
}

func discoverVolumes(dir string) ([]volumeSource, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.epub"))
	if err != nil {
		return nil, fmt.Errorf("unable to list volumes in %q, err: %w", dir, err)
	}
	volumes := make([]volumeSource, 0, len(paths))
	numbers := make(map[int]string, len(paths))
	prefix := ""
	for _, path := range paths {
		id := bookIDFromPath(path)
		parts := volumeNumberPattern.FindStringSubmatch(id)
		if parts == nil || parts[1] == "" {
			return nil, fmt.Errorf("unable to derive publication number from volume %q", id)
		}
		if prefix == "" {
			prefix = parts[1]
		} else if parts[1] != prefix {
			return nil, fmt.Errorf("ambiguous volume names %q and %q: prefixes differ", volumes[0].ID, id)
		}
		number, err := strconv.Atoi(parts[2])
		if err != nil || number < 1 {
			return nil, fmt.Errorf("invalid publication number in volume %q", id)
		}
		if previous, exists := numbers[number]; exists {
			return nil, fmt.Errorf("ambiguous publication number %d in volumes %q and %q", number, previous, id)
		}
		numbers[number] = id
		volumes = append(volumes, volumeSource{ID: id, Path: path, Number: number})
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Number < volumes[j].Number })
	return volumes, nil
}
