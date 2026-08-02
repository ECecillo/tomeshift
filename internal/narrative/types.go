package narrative

import "context"

// CharacterVoice captures stable voice/register instructions for one character.
type CharacterVoice struct {
	Name     string   `json:"name"`
	Register string   `json:"register"`
	Notes    string   `json:"notes"`
	SampleFR []string `json:"sample_fr"`
}

// AddressChange records when and why a character-pair address form changed.
type AddressChange struct {
	AtChapter string `json:"at_chapter"`
	Note      string `json:"note"`
}

// AddressRule is an ordered tu/vous rule for one speaker/listener pair.
type AddressRule struct {
	From    string          `json:"from"`
	To      string          `json:"to"`
	Form    string          `json:"form"`
	History []AddressChange `json:"history"`
}

// Summary is one unit of narrative memory in a bible.
//
// A volume synopsis — what a bible carries since RFC 0008 — has VolumeID set
// and ChapterID empty. A per-chapter entry (the map step's by-product, and
// every pre-0008 row) carries both, or ChapterID alone when it predates the
// volume dimension.
//
// Identity is the (VolumeID, ChapterID) pair, never ChapterID alone: chapter
// IDs are minted per book (`ch_0001` in every volume), so keying on the chapter
// made volume 2's opening indistinguishable from volume 1's.
type Summary struct {
	VolumeID  string `json:"volume_id,omitempty"`
	ChapterID string `json:"chapter_id,omitempty"`
	Summary   string `json:"summary"`
}

// Key is the merge identity of a summary entry.
func (s Summary) Key() [2]string { return [2]string{s.VolumeID, s.ChapterID} }

// Label names the entry for prompt rendering: the volume for a synopsis, the
// chapter for a legacy per-chapter row, both when both are known.
func (s Summary) Label() string {
	switch {
	case s.ChapterID == "":
		return s.VolumeID
	case s.VolumeID == "":
		return s.ChapterID
	default:
		return s.VolumeID + "/" + s.ChapterID
	}
}

// ScopedSeriesID names the bible used to translate one volume of a series.
//
// Summaries are bounded to volumes 1..N so the model never receives the ending
// of a story it is rendering the beginning of, which means a series needs one
// bible per volume rather than one overall. Scoping by identifier is what keeps
// that out of the store schema and out of the run's flags.
func ScopedSeriesID(series, volumeID string) string {
	return series + "@" + volumeID
}

// Bible is the persisted series-scoped narrative state used by literary mode.
type Bible struct {
	SeriesID   string           `json:"series_id"`
	StyleGuide string           `json:"style_guide"`
	Characters []CharacterVoice `json:"characters"`
	Address    []AddressRule    `json:"address"`
	Summaries  []Summary        `json:"summaries"`
	Version    int              `json:"version"`
}

// Store persists and retrieves series bibles.
type Store interface {
	Load(ctx context.Context, seriesID string) (Bible, bool, error)
	Save(ctx context.Context, bible Bible) error
	// SaveIfVersion persists bible only when the stored version still matches
	// bible.Version. saved is false when another writer won the race.
	SaveIfVersion(ctx context.Context, bible Bible) (saved bool, err error)
	// SaveAll publishes a complete generation atomically: either every bible is
	// visible or none is. Versions are assigned by the store at commit time.
	SaveAll(ctx context.Context, bibles []Bible) error
	AppendSummary(ctx context.Context, seriesID string, summary Summary) error
	SetAddress(ctx context.Context, seriesID string, rule AddressRule) error
	// PutChapterSummaries replaces one volume's map-step summaries. They are
	// kept out of Bible.Summaries on purpose: they are a by-product worth
	// keeping, but rendering 68 of them per volume into the cacheable prefix is
	// exactly the unbounded growth RFC 0008 exists to remove.
	//
	// Reading them back is deliberately not on this interface — nothing in the
	// pipeline consumes them yet, and an interface should describe what callers
	// need rather than what the backing table can do. SQLiteStore exposes
	// ChapterSummaries for inspection.
	PutChapterSummaries(ctx context.Context, seriesID, volumeID string, summaries []Summary) error
	Close() error
}

// Consistent reports whether bible contains exactly the expected address form
// for an ordered character pair.
func Consistent(bible Bible, from, to, form string) bool {
	for _, rule := range bible.Address {
		if rule.From == from && rule.To == to {
			return rule.Form == form
		}
	}
	return false
}
