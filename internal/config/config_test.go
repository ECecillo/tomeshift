package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// unset and all stand in for "the environment mentioned nothing" and "the
// environment mentioned everything".
func unset(string) bool { return false }
func all(string) bool   { return true }

func TestApplyProviderDefaults_GoogleCannotProjectZero(t *testing.T) {
	t.Parallel()

	// Selecting the one provider that actually bills, with the cost settings
	// left at their defaults, used to project $0 — the exact defect RFC 0007's
	// cost work exists to remove.
	cfg := Config{NMTProvider: "google", NMTAPIKey: "k"}
	require.NoError(t, cfg.applyProviderDefaults(unset))

	require.InDelta(t, googlePer1000CharsUSD, cfg.CostPer1000CharsUSD, 1e-9)
	require.Equal(t, googleFreeCharsPerMonth, cfg.NMTFreeCharsPerMonth)
	require.False(t, cfg.CostModel().IsZero())
}

func TestApplyProviderDefaults_ExplicitSettingsWin(t *testing.T) {
	t.Parallel()

	cfg := Config{NMTProvider: "google", NMTAPIKey: "k", CostPer1000CharsUSD: 0.05, NMTFreeCharsPerMonth: 1}
	require.NoError(t, cfg.applyProviderDefaults(all))

	require.InDelta(t, 0.05, cfg.CostPer1000CharsUSD, 1e-9)
	require.Equal(t, 1, cfg.NMTFreeCharsPerMonth)
}

func TestApplyProviderDefaults_PricesGoogleWithoutCredentials(t *testing.T) {
	t.Parallel()

	// Loading the configuration must not require an API key: `estimate` prices a
	// Google draft without ever calling Google, and the commands that do call it
	// check the key where they build the provider. Requiring it here made a
	// zero-call dry run refuse to start — and, once secrets moved behind a
	// password manager, made arithmetic prompt for a credential.
	cfg := Config{NMTProvider: "google"}
	require.NoError(t, cfg.applyProviderDefaults(unset))

	require.InDelta(t, googlePer1000CharsUSD, cfg.CostPer1000CharsUSD, 1e-9)
	require.Equal(t, googleFreeCharsPerMonth, cfg.NMTFreeCharsPerMonth)
}

func TestApplyProviderDefaults_AnExplicitZeroIsNotUnset(t *testing.T) {
	t.Parallel()

	// "This project's allowance is gone" is a meaningful thing to say, and
	// treating it as unset would silently hand back 500,000 free characters
	// the operator knows they do not have.
	onlyAllowance := func(name string) bool { return name == "NMT_FREE_CHARS_PER_MONTH" }

	cfg := Config{NMTProvider: "google", NMTAPIKey: "k"}
	require.NoError(t, cfg.applyProviderDefaults(onlyAllowance))
	require.Zero(t, cfg.NMTFreeCharsPerMonth)

	// The rate was not mentioned, so it still gets Google's published one.
	require.InDelta(t, googlePer1000CharsUSD, cfg.CostPer1000CharsUSD, 1e-9)
}

func TestValidate_RejectsNegativeRates(t *testing.T) {
	t.Parallel()

	// A negative rate yields a negative projection, which passes every cap —
	// disabling the guardrail rather than tightening it.
	require.ErrorIs(t, Config{LLMInputPerMTokUSD: -1}.validate(), ErrNegativeValue)
	require.ErrorIs(t, Config{NMTFreeCharsPerMonth: -1}.validate(), ErrNegativeValue)
	require.ErrorIs(t, Config{PerBookBudgetUSD: -1}.validate(), ErrNegativeValue)
	require.NoError(t, Config{}.validate())
}

func TestApplyProviderDefaults_LeavesOtherProvidersAlone(t *testing.T) {
	t.Parallel()

	// The self-hosted engines are free; defaulting them to Google's rate would
	// invent spend that never happens.
	cfg := Config{NMTProvider: "libretranslate"}
	require.NoError(t, cfg.applyProviderDefaults(unset))
	require.True(t, cfg.CostModel().IsZero())
}

func TestCostModel_ReadsThePrefixTransportFromTheChatProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{"the CLI reopens a session per call", "claude-cli", true},
		{"the HTTP adapter marks the prefix once", "anthropic", false},
		{"an OpenAI-compatible host has no cache to reopen", "deepseek", false},
		{"unset means the OpenAI-compatible default", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// It is deliberately not an TOMESHIFT_* variable: an operator
			// choosing a chat provider should not also have to know how that
			// transport handles a prompt cache.
			cfg := Config{LLMChatProvider: tt.provider}
			require.Equal(t, tt.want, cfg.CostModel().Tokens.PrefixWrittenPerCall)
		})
	}
}

func TestLoad_TemporalDefaults(t *testing.T) {
	// Not parallel: it reads the process environment, so asserting the built-in
	// defaults means no TOMESHIFT_TEMPORAL_* is set for this case.
	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, "localhost:7233", cfg.TemporalAddress)
	require.Equal(t, "default", cfg.TemporalNamespace)
	require.Equal(t, "tomeshift", cfg.TemporalTaskQueue)
}

func TestLoad_TemporalOverrides(t *testing.T) {
	// t.Setenv forbids t.Parallel; the override must win over the default.
	t.Setenv("TOMESHIFT_TEMPORAL_ADDRESS", "temporal.internal:7233")
	t.Setenv("TOMESHIFT_TEMPORAL_NAMESPACE", "epub")
	t.Setenv("TOMESHIFT_TEMPORAL_TASK_QUEUE", "tomeshift-heavy")

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, "temporal.internal:7233", cfg.TemporalAddress)
	require.Equal(t, "epub", cfg.TemporalNamespace)
	require.Equal(t, "tomeshift-heavy", cfg.TemporalTaskQueue)
}
