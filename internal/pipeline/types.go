package pipeline

import (
	"time"

	"github.com/ECecillo/tomeshift/internal/cost"
	"github.com/ECecillo/tomeshift/internal/literary"
	"github.com/ECecillo/tomeshift/internal/qa"
)

const (
	ModeNMT      = "nmt"
	ModeLiterary = "literary"
)

// BookInput is the input contract for a single translation run. It replaces the
// former per-run config struct and is the payload the flow (and, from phase 3,
// the Temporal workflow) is started with: no live objects, only serialisable
// values.
type BookInput struct {
	InputPath  string
	OutputDir  string // run-specific output directory (artifacts are written here)
	SourceLang string
	TargetLang string
	RunID      string
	BookID     string
	Mode       string
	SeriesID   string
	// UpdateBible lets literary mode evolve the bible during the run. Off by
	// default: the bible is frozen so prompts/cache stay stable and re-runs are
	// reproducible.
	UpdateBible bool
	// Resume requests continuation using the persistent TM cache; already
	// translated segments become TM hits rather than new provider calls.
	Resume bool
}

// runMode returns the effective mode for in, defaulting to NMT.
func runMode(in BookInput) string {
	if in.Mode == "" {
		return ModeNMT
	}
	return in.Mode
}

// RunResult is the machine-readable summary emitted as report.json.
type RunResult struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	BookID        string    `json:"book_id"`
	Mode          string    `json:"mode"`
	SeriesID      string    `json:"series_id,omitempty"`
	BibleVersion  int       `json:"bible_version,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	TotalSegments int       `json:"total_segments"`
	TMHits        int       `json:"tm_hits"`
	TMHitRatio    float64   `json:"tm_hit_ratio"`
	NMTSegments   int       `json:"nmt_segments"`
	NMTEngine     string    `json:"nmt_engine,omitempty"` // provider the chars below were sent to
	NMTChars      int       `json:"nmt_chars"`            // source chars sent to it (free-tier accounting)
	LLMAttempts   int       `json:"llm_attempts"`         // post-edit calls made, usable or not — this is what was billed
	LLMChars      int       `json:"llm_chars"`            // source chars those calls carried
	LLMSegments   int       `json:"llm_segments"`
	// LLMPassSegments is how many segments the LLM pass carried, usable result
	// or not — unlike LLMSegments, which counts the ones it improved. It is the
	// denominator that pairs with the token counters below, and the one the
	// ledger keeps: dividing a degraded batch's tokens by the segments it did
	// not deliver would report a model as more verbose the worse it behaved.
	LLMPassSegments        int     `json:"llm_pass_segments,omitempty"`
	LLMProvider            string  `json:"llm_provider,omitempty"`
	LLMModel               string  `json:"llm_model,omitempty"`
	LLMEscalationRatio     float64 `json:"llm_escalation_ratio"`
	LLMInputTokens         int     `json:"llm_input_tokens"`
	LLMOutputTokens        int     `json:"llm_output_tokens"`
	LLMCacheReadTokens     int     `json:"llm_cache_read_tokens"`
	LLMCacheCreationTokens int     `json:"llm_cache_creation_tokens"`
	GlossaryViolations     int     `json:"glossary_violations"`
	AvgQualityScore        float64 `json:"avg_quality_score"` // mean COMET-QE over scored segments (0 if disabled)
	QualityScoredSegments  int     `json:"quality_scored_segments"`
	// EstimatedCostUSD is the whole book priced with NO translation-memory
	// reuse. It is deliberately not the figure the per-book cap is enforced on —
	// that one covers only the segments this run still has to translate — because
	// this is the baseline CostSavingsUSD subtracts from. The two coincide on a
	// fresh book and diverge on a resumed one.
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	ActualCostUSD    float64   `json:"actual_cost_usd"`
	CostSavingsUSD   float64   `json:"cost_savings_usd"` // estimated (no-reuse) minus actual = TM savings
	OutputEPUB       string    `json:"output_epub"`
	QA               qa.Report `json:"qa"`
}

// PlanResult is a dry-run projection: the book is parsed and segmented but not
// translated, and per-strategy cost estimates are returned.
type PlanResult struct {
	BookID           string `json:"book_id"`
	TotalSegments    int    `json:"total_segments"`
	TotalSourceChars int    `json:"total_source_chars"`
	Chapters         int    `json:"chapters"`
	// ReusableSegments is how many of them the translation memory can already
	// serve, and PendingSegments what is left to pay for. The scenarios below
	// price the pending part only — the projection is about this run, not about
	// the book in the abstract.
	ReusableSegments  int                     `json:"reusable_segments"`
	PendingSegments   int                     `json:"pending_segments"`
	PendingSourceChar int                     `json:"pending_source_chars"`
	BatchSize         int                     `json:"batch_size"`
	EscalationRate    float64                 `json:"escalation_rate"`
	Scenarios         []cost.ScenarioEstimate `json:"scenarios"`
}

// ---------------------------------------------------------------------------
// Step DTOs. One small, serialisable struct per activity in the RFC table. No
// *html.Node, no segment.Segment and no epub.Book ever crosses a port: content
// is exchanged through the per-chapter unit files in the workspace.
// ---------------------------------------------------------------------------

// RunRef locates a run's workspace, identity and source without carrying its
// content. InputPath lets the re-parse activities (Commit, Recompose, Finish)
// re-derive placeholders and node trees deterministically.
type RunRef struct {
	RunID     string
	BookID    string
	OutputDir string
	InputPath string
}

// Manifest is what PrepareBook produced: the chapters to translate, in spine
// order, plus the run-scoped identifiers the flow stamps onto RunResult but
// cannot learn without the injected collaborators (the NMT engine name, the LLM
// provider/model recorded for token accounting).
type Manifest struct {
	Chapters    []ChapterInfo
	NMTEngine   string
	LLMProvider string
	LLMModel    string
	// BatchSize is the literary LLM batch size the flow chunks fresh segments
	// by; 0 outside literary mode.
	BatchSize int
}

// ChapterInfo is a chapter's shape: enough for the flow to drive the loop and
// for the cost projection to price it, with none of its content.
type ChapterInfo struct {
	ID       string
	Href     string
	Segments int
	Chars    int
}

// ProjectInput asks for the pre-run budget guardrail: it prices the work still
// to do and refuses it before any provider call if a cap is exceeded.
type ProjectInput struct {
	Ref      RunRef
	Mode     string
	SeriesID string
}

// Projection is what ProjectCost returns: the two figures the two readers want
// (EnforcedUSD, priced on the pending work; NoReuseUSD, the whole book with no
// reuse — the baseline the savings subtract from), the reuse split, the free
// characters already consumed this month, and the loaded bible version.
type Projection struct {
	EnforcedUSD   float64
	NoReuseUSD    float64
	Pending       int
	Reusable      int
	UsedFreeChars int
	BibleVersion  int
}

// ResolveInput asks ResolveChapter to serve a chapter's segments from the TM.
type ResolveInput struct {
	Ref          RunRef
	ChapterID    string
	ReusesDrafts bool
}

// ChapterPlan is the reuse split of one chapter after the TM lookup: how many
// segments the memory served and which ones remain to translate.
type ChapterPlan struct {
	Reused int
	Fresh  []string
}

// DraftInput asks DraftChapter to draft a chapter's TM misses in one batched
// provider call. PersistDrafts is true only in NMT mode, where an unescalated
// draft is the final output for its segment.
type DraftInput struct {
	Ref           RunRef
	ChapterID     string
	Fresh         []string
	PersistDrafts bool
	SourceLang    string
	TargetLang    string
}

// DraftResult reports what the draft consumed: the source characters actually
// put into provider requests (retries included) and the engine that answered.
type DraftResult struct {
	Chars  int
	Engine string
}

// EscalateInput asks EscalateChapter to run the NMT-mode quality-estimation and
// selective LLM post-edit over a chapter's fresh drafts.
type EscalateInput struct {
	Ref        RunRef
	ChapterID  string
	Fresh      []string
	SourceLang string
	TargetLang string
}

// EscalateResult reports the escalation pass: post-edit calls made (Attempts,
// billed whether usable or not), the source characters they carried, how many
// segments were actually improved, and the quality-estimation totals.
type EscalateResult struct {
	Attempts int
	Chars    int
	Improved int
	QESum    float64
	QECount  int
}

// RefineInput asks RefineBatch to rewrite one literary batch. The preceding
// French context is PrevTail (the previous batch's translated tail) for every
// batch but a chapter's first; that one carries PrevTail empty and, in
// PrevChapterID, the id of the previous chapter, whose source tail is the
// context — exactly as the in-process walk read it before the split.
type RefineInput struct {
	Ref           RunRef
	ChapterID     string
	SeriesID      string
	IDs           []string
	PrevTail      string
	PrevChapterID string
	SourceLang    string
	TargetLang    string
}

// RefineResult reports one batch's outcome: the tokens it consumed, how many of
// its segments it refined, the ids it degraded (kept on their draft) and the
// tail of French it produced, fed to the next batch as PrevTail.
type RefineResult struct {
	Usage    literary.Usage
	Refined  int
	Degraded []string
	Tail     string
}

// CommitInput asks CommitChapter to re-derive the chapter's placeholders,
// validate each translation by restoring it, persist accepted ones (Persist is
// true only in literary mode) and write the final units.
type CommitInput struct {
	Ref        RunRef
	ChapterID  string
	Persist    bool
	SourceLang string
	TargetLang string
}

// CommitResult reports what the commit found: glossary violations across the
// chapter and restores the placeholder manager refused (QA counts these).
type CommitResult struct {
	GlossaryViolations int
	RestoreFailures    int
}

// BibleInput asks UpdateBible to evolve the series bible from a committed
// chapter.
type BibleInput struct {
	Ref       RunRef
	SeriesID  string
	ChapterID string
}

// BibleResult reports the bible version after the update.
type BibleResult struct {
	Version int
}

// RecomposeResult reports the produced EPUB path.
type RecomposeResult struct {
	OutputEPUB string
}

// CheckInput asks CheckEPUB to validate the produced EPUB.
type CheckInput struct {
	OutputEPUB string
}

// CheckResult is the epubcheck verdict. A skipped check (binary absent) does
// not block the run.
type CheckResult struct {
	Passed  bool
	Skipped bool
}

// FinishInput asks FinishBook to evaluate QA over the committed units and write
// the run artifacts.
type FinishInput struct {
	Ref    RunRef
	Result RunResult
}

// SpendInput asks RecordSpend to persist what the run consumed to the ledger.
type SpendInput struct {
	Ref    RunRef
	Result RunResult
}

// PriceInput asks PriceRun to price what the result says has been consumed so
// far, through the mode's cost model.
type PriceInput struct {
	Result        RunResult
	UsedFreeChars int
	Mode          string
}

// Progress is the run's advancement, published by the flow and consumed by the
// engine: the in-process runtime drives the terminal bar with it, and from
// phase 3 the Temporal workflow serves it through the progress query.
type Progress struct {
	State        string
	Stage        string
	Chapter      int
	Chapters     int
	Segments     int // book-wide total, so the bar can size itself
	SegmentsDone int
	TMHits       int
	CostUSD      float64
}
