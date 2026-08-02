package translate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/translate"
)

func TestRequiresEndpoint(t *testing.T) {
	t.Parallel()

	// Cloud Translation has one public host, so the adapter supplies it.
	require.False(t, translate.RequiresEndpoint(translate.ProviderGoogle))
	require.Equal(t,
		"https://translation.googleapis.com/language/translate/v2",
		translate.NewGoogleTranslator("", "key").Endpoint)

	// The self-hosted engines have nowhere to default to.
	require.True(t, translate.RequiresEndpoint(translate.ProviderHTTP))
	require.True(t, translate.RequiresEndpoint(translate.ProviderLibreTranslate))
	require.True(t, translate.RequiresEndpoint(""))
}
