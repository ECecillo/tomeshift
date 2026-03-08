package main

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

const envPrefix = "EPUB_TRANSLATOR"

type Config struct {
	Env              string  `split_words:"true" default:"local"`
	LogLevel         string  `split_words:"true" default:"info"`
	RunOutputDir     string  `split_words:"true" default:"./runs"`
	NMTProvider      string  `split_words:"true" default:"http"`
	NMTEndpoint      string  `split_words:"true" required:"true"`
	NMTAPIKey        string  `split_words:"true"`
	LLMEnabled       bool    `split_words:"true" default:"false"`
	LLMProvider      string  `split_words:"true"`
	LLMAPIKey        string  `split_words:"true"`
	DailyBudgetUSD   float64 `split_words:"true" default:"5.00"`
	MonthlyBudgetUSD float64 `split_words:"true" default:"50.00"`
	PerBookBudgetUSD float64 `split_words:"true" default:"2.00"`
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
