package main

import (
	"os"

	"github.com/ECecillo/ebooks-translator/internal/commandexec"
)

func main() {
	// Execute run entrypoint method with given go flags and exit program with
	// a defined exit code.
	os.Exit(commandexec.Execute(os.Args[1:], os.Stdout, os.Stderr, loadRuntimeConfig))
}

// loadRuntimeConfig adapts app config into the command execution contract so
// command orchestration stays independent from concrete config types.
func loadRuntimeConfig() (commandexec.RuntimeConfig, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return commandexec.RuntimeConfig{}, err
	}
	return commandexec.RuntimeConfig{
		Env:                   cfg.Env,
		NMTProvider:           cfg.NMTProvider,
		NMTEndpoint:           cfg.NMTEndpoint,
		NMTAPIKey:             cfg.NMTAPIKey,
		LogLevel:              cfg.LogLevel,
		TMPath:                cfg.TMPath,
		NarrativePath:         cfg.NarrativePath,
		PerBookBudgetUSD:      cfg.PerBookBudgetUSD,
		DailyBudgetUSD:        cfg.DailyBudgetUSD,
		MonthlyBudgetUSD:      cfg.MonthlyBudgetUSD,
		CostPerSegmentUSD:     cfg.CostPerSegmentUSD,
		CostPer1000CharsUSD:   cfg.CostPer1000CharsUSD,
		NMTMaxAttempts:        cfg.NMTMaxAttempts,
		NMTRetryBaseMS:        cfg.NMTRetryBaseMS,
		EPUBCheckBin:          cfg.EPUBCheckBin,
		EPUBCheckJar:          cfg.EPUBCheckJar,
		RunTimeoutSeconds:     cfg.RunTimeoutSeconds,
		LLMInputPerMTokUSD:    cfg.LLMInputPerMTokUSD,
		LLMOutputPerMTokUSD:   cfg.LLMOutputPerMTokUSD,
		LLMOutputExpansion:    cfg.LLMOutputExpansion,
		LLMPromptOverheadToks: cfg.LLMPromptOverheadToks,
		LLMEnabled:            cfg.LLMEnabled,
		LLMEndpoint:           cfg.LLMEndpoint,
		LLMAPIKey:             cfg.LLMAPIKey,
		LLMChatProvider:       cfg.LLMChatProvider,
		LLMBaseURL:            cfg.LLMBaseURL,
		LLMModel:              cfg.LLMModel,
		LLMBatchSize:          cfg.LLMBatchSize,
		LLMCachePath:          cfg.LLMCachePath,
		LLMEscalateMinChars:   cfg.LLMEscalateMinChars,
		CostLLMPerSegmentUSD:  cfg.CostLLMPerSegmentUSD,
		CometEndpoint:         cfg.CometEndpoint,
		CometAPIKey:           cfg.CometAPIKey,
		EscalateBelowScore:    cfg.EscalateBelowScore,
	}, nil
}
