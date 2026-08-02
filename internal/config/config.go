// Package config is the typed, environment-backed configuration for the CLI.
//
// It is one struct, not two. The process entrypoint loads it and the command
// layer consumes it; keeping a separate "runtime" mirror of the same fields
// only bought a field-by-field copy that had to be edited in three places every
// time a setting was added.
package config

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"

	"github.com/ECecillo/ebooks-translator/internal/cost"
	"github.com/ECecillo/ebooks-translator/internal/translate"
)

const envPrefix = "EPUB_TRANSLATOR"

// Config is the full runtime configuration, read from EPUB_TRANSLATOR_* vars.
type Config struct {
	// LogLevel controls the structured run log verbosity.
	LogLevel string `split_words:"true" default:"info"`

	// NMTProvider selects the draft engine: "http" (bundled dev server),
	// "libretranslate" (self-hosted Argos) or "google" (Cloud Translation v2).
	NMTProvider string `split_words:"true" default:"http"`
	// NMTEndpoint is the NMT endpoint the provider targets. It is not required
	// here: `estimate`, `eval` and `glossary-suggest` never translate, and
	// `google` supplies its own public host. The commands that do translate
	// check it when they build the provider, so the failure names the setting
	// instead of surfacing as a refusal to start every command.
	NMTEndpoint string `split_words:"true"`
	// NMTAPIKey authenticates the NMT provider. Required for "google".
	NMTAPIKey string `envconfig:"NMT_API_KEY"`
	// NMTFreeCharsPerMonth is the provider's monthly free character allowance,
	// shared across the billing project. 500000 for Google Cloud Translation.
	NMTFreeCharsPerMonth int `envconfig:"NMT_FREE_CHARS_PER_MONTH" default:"0"`
	// NMTMaxAttempts and NMTRetryBaseMS configure transient-failure retries.
	NMTMaxAttempts int `split_words:"true" default:"3"`
	NMTRetryBaseMS int `envconfig:"NMT_RETRY_BASE_MS" default:"200"`

	// TMPath is the SQLite translation-memory and spend-ledger path.
	TMPath string `split_words:"true" default:"./tm.sqlite"`
	// NarrativePath is the SQLite series-bible path.
	NarrativePath string `split_words:"true" default:"./narrative.sqlite"`

	// EPUBCheckBin / EPUBCheckJar select how the QA gate runs epubcheck.
	EPUBCheckBin string `envconfig:"EPUBCHECK_BIN"`
	EPUBCheckJar string `envconfig:"EPUBCHECK_JAR"`
	// RunTimeoutSeconds bounds a single book's run; 0 disables.
	RunTimeoutSeconds int `split_words:"true" default:"0"`

	// LLM post-edit (NMT mode); disabled unless LLMEnabled and LLMEndpoint set.
	LLMEnabled  bool   `split_words:"true" default:"false"`
	LLMEndpoint string `split_words:"true"`
	LLMAPIKey   string `envconfig:"LLM_API_KEY"`
	// Chat client for literary mode and style-profile.
	LLMChatProvider string `split_words:"true" default:"openai"`
	LLMBaseURL      string `envconfig:"LLM_BASE_URL"`
	LLMModel        string `split_words:"true"`
	// LLMBatchSize is the number of segments per literary LLM call; 0 uses the
	// literary package default. Raise it for large models that finish long
	// structured outputs in one pass, to spend fewer calls per chapter.
	LLMBatchSize int `split_words:"true" default:"0"`
	// LLMCachePath is the SQLite completion cache: re-running over the same
	// chapter reuses prior completions at no provider cost.
	LLMCachePath string `split_words:"true" default:"./llm_cache.sqlite"`
	// LLMEscalateMinChars escalates NMT segments with at least this many source
	// chars to the post-editor; 0 escalates all of them.
	LLMEscalateMinChars int `split_words:"true" default:"240"`

	// LLM token pricing. One model serves both `estimate` and the run-time
	// budget guardrail, so what the former reports is what the latter enforces.
	LLMInputPerMTokUSD     float64 `envconfig:"LLM_INPUT_PER_MTOK_USD" default:"0"`
	LLMOutputPerMTokUSD    float64 `envconfig:"LLM_OUTPUT_PER_MTOK_USD" default:"0"`
	LLMCacheReadPerMTokUSD float64 `envconfig:"LLM_CACHE_READ_PER_MTOK_USD" default:"0"`
	LLMCacheWriteMult      float64 `envconfig:"LLM_CACHE_WRITE_MULTIPLIER" default:"1.25"`
	LLMOutputExpansion     float64 `split_words:"true" default:"1.15"`
	LLMPromptOverheadToks  int     `envconfig:"LLM_PROMPT_OVERHEAD_TOKENS" default:"200"`
	// LLMTokenMultiplier corrects the character→token heuristic for models with
	// a denser tokenizer: 1.30 for claude-sonnet-5 and the Opus 4.7 family,
	// whose sticker price matches Sonnet 4.6 but which emit ~30% more tokens
	// for the same text. Without it the guardrail under-projects by that much.
	LLMTokenMultiplier float64 `envconfig:"LLM_TOKEN_MULTIPLIER" default:"1.0"`

	// COMET-QE quality estimation (optional) for quality-driven escalation.
	CometEndpoint      string  `split_words:"true"`
	CometAPIKey        string  `envconfig:"COMET_API_KEY"`
	EscalateBelowScore float64 `split_words:"true" default:"0"`

	// NMT draft pricing. Google Cloud Translation is 20 $ per million
	// characters, i.e. 0.02 $ per 1000.
	CostPerSegmentUSD   float64 `split_words:"true" default:"0"`
	CostPer1000CharsUSD float64 `envconfig:"COST_PER_1000_CHARS_USD" default:"0"`

	// Spend caps; <= 0 disables the check.
	DailyBudgetUSD   float64 `split_words:"true" default:"5.00"`
	MonthlyBudgetUSD float64 `split_words:"true" default:"50.00"`
	PerBookBudgetUSD float64 `split_words:"true" default:"2.00"`
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	var cfg Config
	if err := envconfig.Process(envPrefix, &cfg); err != nil {
		return Config{}, fmt.Errorf("unable to load config from environment, err: %w", err)
	}
	if cfg.DailyBudgetUSD < 0 || cfg.MonthlyBudgetUSD < 0 || cfg.PerBookBudgetUSD < 0 {
		return Config{}, fmt.Errorf("unable to validate budget config: values must be >= 0, err: %w", ErrNegativeBudget)
	}
	if err := cfg.applyProviderDefaults(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Google Cloud Translation's published rate and free allowance, as of
// 2026-08. They are defaults, not constants: an explicit setting always wins,
// and they should be re-checked against Google's pricing page rather than
// trusted indefinitely.
const (
	googlePer1000CharsUSD   = 0.02 // 20 $ per million characters
	googleFreeCharsPerMonth = 500_000
)

// applyProviderDefaults keeps a paid provider from being selected with a
// guardrail that projects nothing. The whole point of RFC 0007's cost work is
// that the per-book cap has something true to compare against; defaulting to
// zero for the one provider that actually bills would reinstate exactly the
// defect it set out to remove.
func (c *Config) applyProviderDefaults() error {
	if c.NMTProvider != translate.ProviderGoogle {
		return nil
	}
	if c.NMTAPIKey == "" {
		return fmt.Errorf("unable to use the google NMT provider: set EPUB_TRANSLATOR_NMT_API_KEY, err: %w",
			ErrMissingAPIKey)
	}
	if c.CostPer1000CharsUSD == 0 {
		c.CostPer1000CharsUSD = googlePer1000CharsUSD
	}
	if c.NMTFreeCharsPerMonth == 0 {
		c.NMTFreeCharsPerMonth = googleFreeCharsPerMonth
	}
	return nil
}

// CostModel builds the single cost model shared by `estimate`, `run` and
// `batch`. Keeping it in one place is what makes the projection the guardrail
// enforces identical to the one `estimate` prints.
func (c Config) CostModel() cost.Model {
	return cost.Model{
		PerSegmentUSD:     c.CostPerSegmentUSD,
		Per1000CharsUSD:   c.CostPer1000CharsUSD,
		FreeCharsPerMonth: c.NMTFreeCharsPerMonth,
		Tokens: cost.TokenRates{
			InputPerMTokUSD:      c.LLMInputPerMTokUSD,
			OutputPerMTokUSD:     c.LLMOutputPerMTokUSD,
			CacheReadPerMTokUSD:  c.LLMCacheReadPerMTokUSD,
			CacheWriteMultiplier: c.LLMCacheWriteMult,
			OutputExpansion:      c.LLMOutputExpansion,
			PromptOverheadTokens: c.LLMPromptOverheadToks,
			TokenMultiplier:      c.LLMTokenMultiplier,
		},
	}
}
