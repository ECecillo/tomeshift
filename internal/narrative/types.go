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

// ChapterSummary stores compact continuity context for one translated chapter.
type ChapterSummary struct {
	ChapterID string `json:"chapter_id"`
	Summary   string `json:"summary"`
}

// Bible is the persisted series-scoped narrative state used by literary mode.
type Bible struct {
	SeriesID   string           `json:"series_id"`
	StyleGuide string           `json:"style_guide"`
	Characters []CharacterVoice `json:"characters"`
	Address    []AddressRule    `json:"address"`
	Summaries  []ChapterSummary `json:"summaries"`
	Version    int              `json:"version"`
}

// Store persists and retrieves series bibles.
type Store interface {
	Load(ctx context.Context, seriesID string) (Bible, bool, error)
	Save(ctx context.Context, bible Bible) error
	AppendSummary(ctx context.Context, seriesID string, summary ChapterSummary) error
	SetAddress(ctx context.Context, seriesID string, rule AddressRule) error
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
