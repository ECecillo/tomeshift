package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyProviderDefaults_GoogleCannotProjectZero(t *testing.T) {
	t.Parallel()

	// Selecting the one provider that actually bills, with the cost settings
	// left at their defaults, used to project $0 — the exact defect RFC 0007's
	// cost work exists to remove.
	cfg := Config{NMTProvider: "google", NMTAPIKey: "k"}
	require.NoError(t, cfg.applyProviderDefaults())

	require.InDelta(t, googlePer1000CharsUSD, cfg.CostPer1000CharsUSD, 1e-9)
	require.Equal(t, googleFreeCharsPerMonth, cfg.NMTFreeCharsPerMonth)
	require.False(t, cfg.CostModel().IsZero())
}

func TestApplyProviderDefaults_ExplicitSettingsWin(t *testing.T) {
	t.Parallel()

	cfg := Config{NMTProvider: "google", NMTAPIKey: "k", CostPer1000CharsUSD: 0.05, NMTFreeCharsPerMonth: 1}
	require.NoError(t, cfg.applyProviderDefaults())

	require.InDelta(t, 0.05, cfg.CostPer1000CharsUSD, 1e-9)
	require.Equal(t, 1, cfg.NMTFreeCharsPerMonth)
}

func TestApplyProviderDefaults_GoogleNeedsAKey(t *testing.T) {
	t.Parallel()

	cfg := Config{NMTProvider: "google"}
	require.ErrorIs(t, cfg.applyProviderDefaults(), ErrMissingAPIKey)
}

func TestApplyProviderDefaults_LeavesOtherProvidersAlone(t *testing.T) {
	t.Parallel()

	// The self-hosted engines are free; defaulting them to Google's rate would
	// invent spend that never happens.
	cfg := Config{NMTProvider: "libretranslate"}
	require.NoError(t, cfg.applyProviderDefaults())
	require.True(t, cfg.CostModel().IsZero())
}
