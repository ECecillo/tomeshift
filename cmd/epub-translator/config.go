package main

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

const envPrefix = "EPUB_TRANSLATOR"

type Config struct {
	Env                  string  `split_words:"true" default:"local"`
	LogLevel             string  `split_words:"true" default:"info"`
	RunOutputDir         string  `split_words:"true" default:"./runs"`
	NMTProvider          string  `split_words:"true" default:"http"`
	NMTEndpoint          string  `split_words:"true" required:"true"`
	NMTAPIKey            string  `split_words:"true"`
	TMPath               string  `split_words:"true" default:"./tm.sqlite"`
	NMTMaxAttempts       int     `split_words:"true" default:"3"`
	NMTRetryBaseMS       int     `envconfig:"NMT_RETRY_BASE_MS" default:"200"`
	EPUBCheckBin         string  `envconfig:"EPUBCHECK_BIN"`
	EPUBCheckJar         string  `envconfig:"EPUBCHECK_JAR"`
	RunTimeoutSeconds    int     `split_words:"true" default:"0"`
	LLMEnabled           bool    `split_words:"true" default:"false"`
	LLMProvider          string  `split_words:"true"`
	LLMEndpoint          string  `split_words:"true"`
	LLMAPIKey            string  `split_words:"true"`
	LLMEscalateMinChars  int     `split_words:"true" default:"240"`
	CostLLMPerSegmentUSD float64 `envconfig:"COST_LLM_PER_SEGMENT_USD" default:"0"`
	CometEndpoint        string  `split_words:"true"`
	CometAPIKey          string  `split_words:"true"`
	EscalateBelowScore   float64 `split_words:"true" default:"0"`
	DailyBudgetUSD       float64 `split_words:"true" default:"5.00"`
	MonthlyBudgetUSD     float64 `split_words:"true" default:"50.00"`
	PerBookBudgetUSD     float64 `split_words:"true" default:"2.00"`

	CostPerSegmentUSD   float64 `split_words:"true" default:"0"`
	CostPer1000CharsUSD float64 `envconfig:"COST_PER_1000_CHARS_USD" default:"0"`

	LLMInputPerMTokUSD    float64 `envconfig:"LLM_INPUT_PER_MTOK_USD" default:"0"`
	LLMOutputPerMTokUSD   float64 `envconfig:"LLM_OUTPUT_PER_MTOK_USD" default:"0"`
	LLMOutputExpansion    float64 `split_words:"true" default:"1.15"`
	LLMPromptOverheadToks int     `envconfig:"LLM_PROMPT_OVERHEAD_TOKENS" default:"200"`
}

func LoadConfig() (Config, error) {
	var cfg Config
	if err := envconfig.Process(envPrefix, &cfg); err != nil {
		return Config{}, fmt.Errorf("unable to load config from environment, err: %w", err)
	}
	if cfg.DailyBudgetUSD < 0 || cfg.MonthlyBudgetUSD < 0 || cfg.PerBookBudgetUSD < 0 {
		return Config{}, fmt.Errorf("unable to validate budget config: values must be >= 0, err: %w", ErrNegativeBudget)
	}
	return cfg, nil
}
