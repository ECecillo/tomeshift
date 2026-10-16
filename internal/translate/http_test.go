package translate_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/translate"
	"github.com/ECecillo/tomeshift/internal/translate/nmttest"
)

func TestHTTPTranslator_Translate(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(nil)
	t.Cleanup(srv.Close)

	tr := translate.NewHTTPTranslator(srv.URL, "")
	resp, err := tr.Translate(t.Context(), translate.Request{
		Text:       `Hello <x id="1"/> world`,
		SourceLang: "en",
		TargetLang: "fr",
	})

	require.NoError(t, err)
	require.Equal(t, `[fr] Hello <x id="1"/> world`, resp.Text)
	require.Equal(t, "nmt-http", resp.Engine)
	require.Equal(t, "nmt-http", tr.Engine())
}

func TestHTTPTranslator_PreservesPlaceholders(t *testing.T) {
	t.Parallel()

	srv := nmttest.NewServer(func(text string) string { return text })
	t.Cleanup(srv.Close)

	tr := translate.NewHTTPTranslator(srv.URL, "")
	resp, err := tr.Translate(t.Context(), translate.Request{Text: `a <x id="1"/> b <x id="2"/>`})

	require.NoError(t, err)
	require.Equal(t, `a <x id="1"/> b <x id="2"/>`, resp.Text)
}

func TestHTTPTranslator_EmptyEndpoint(t *testing.T) {
	t.Parallel()

	tr := translate.NewHTTPTranslator("", "")
	_, err := tr.Translate(t.Context(), translate.Request{Text: "x"})
	require.ErrorIs(t, err, translate.ErrEmptyEndpoint)
}

func TestHTTPTranslator_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	tr := translate.NewHTTPTranslator(srv.URL, "")
	_, err := tr.Translate(t.Context(), translate.Request{Text: "x"})
	require.ErrorIs(t, err, translate.ErrProviderStatus)
}

func TestHTTPTranslator_SendsBearerToken(t *testing.T) {
	t.Parallel()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"translated_text":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	tr := translate.NewHTTPTranslator(srv.URL, "secret-key")
	_, err := tr.Translate(t.Context(), translate.Request{Text: "x"})

	require.NoError(t, err)
	require.Equal(t, "Bearer secret-key", gotAuth)
}
