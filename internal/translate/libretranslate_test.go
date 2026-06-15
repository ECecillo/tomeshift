package translate_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/translate"
)

// libreServer mimics LibreTranslate's /translate contract and records the body.
func libreServer(t *testing.T, translatedText string, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if captured != nil {
			_ = json.Unmarshal(body, captured)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"translatedText":` + mustJSON(t, translatedText) + `}`))
	}))
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

func TestLibreTranslator_Translate(t *testing.T) {
	t.Parallel()

	var got map[string]any
	srv := libreServer(t, `Bonjour <x id="1"/> monde`, &got)
	t.Cleanup(srv.Close)

	tr := translate.NewLibreTranslator(srv.URL, "")
	resp, err := tr.Translate(t.Context(), translate.Request{
		Text: `Hello <x id="1"/> world`, SourceLang: "en", TargetLang: "fr",
	})

	require.NoError(t, err)
	require.Equal(t, `Bonjour <x id="1"/> monde`, resp.Text)
	require.Equal(t, "nmt-libretranslate", resp.Engine)

	// Sends LibreTranslate's field names and requests HTML format so tokens survive.
	require.Equal(t, `Hello <x id="1"/> world`, got["q"])
	require.Equal(t, "en", got["source"])
	require.Equal(t, "fr", got["target"])
	require.Equal(t, "html", got["format"])
}

func TestLibreTranslator_EmptyEndpoint(t *testing.T) {
	t.Parallel()

	_, err := translate.NewLibreTranslator("", "").Translate(t.Context(), translate.Request{Text: "x"})
	require.ErrorIs(t, err, translate.ErrEmptyEndpoint)
}

func TestLibreTranslator_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	_, err := translate.NewLibreTranslator(srv.URL, "").Translate(t.Context(), translate.Request{Text: "x"})
	require.ErrorIs(t, err, translate.ErrProviderStatus)
}

func TestNewProvider_Selection(t *testing.T) {
	t.Parallel()

	require.Equal(t, "nmt-libretranslate",
		translate.NewProvider(translate.ProviderLibreTranslate, "http://x", "").Engine())
	require.Equal(t, "nmt-http",
		translate.NewProvider(translate.ProviderHTTP, "http://x", "").Engine())
	// Unknown provider falls back to the default HTTP adapter.
	require.Equal(t, "nmt-http",
		translate.NewProvider("typo", "http://x", "").Engine())
}
