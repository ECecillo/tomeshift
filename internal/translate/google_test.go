package translate_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/translate"
)

type googleRequest struct {
	Q      []string `json:"q"`
	Source string   `json:"source"`
	Target string   `json:"target"`
	Format string   `json:"format"`
}

// googleServer answers like Cloud Translation v2: one translation per `q`
// entry, in order, with the text content HTML-escaped.
func googleServer(t *testing.T, translate func(string) string, record *[]googleRequest) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req googleRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if record != nil {
			*record = append(*record, req)
		}

		type translation struct {
			TranslatedText string `json:"translatedText"`
		}
		out := make([]translation, 0, len(req.Q))
		for _, q := range req.Q {
			out = append(out, translation{TranslatedText: translate(q)})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"translations": out},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGoogleTranslator_Translate(t *testing.T) {
	t.Parallel()

	var got []googleRequest
	srv := googleServer(t, func(q string) string { return "[fr] " + q }, &got)

	tr := translate.NewGoogleTranslator(srv.URL, "secret")
	resp, err := tr.Translate(t.Context(), translate.Request{
		Text:       `Hello <x id="1"/> world`,
		SourceLang: "en",
		TargetLang: "fr",
	})

	require.NoError(t, err)
	require.Equal(t, `[fr] Hello <x id="1"/> world`, resp.Text)
	require.Equal(t, "nmt-google", resp.Engine)
	require.Equal(t, "nmt-google", tr.Engine())

	require.Len(t, got, 1)
	require.Equal(t, "html", got[0].Format, "format=html is what preserves placeholder tokens")
	require.Equal(t, "en", got[0].Source)
	require.Equal(t, "fr", got[0].Target)
}

func TestGoogleTranslator_SendsAPIKeyInAHeaderNotTheURL(t *testing.T) {
	t.Parallel()

	var header, rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("X-goog-api-key")
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"translations":[{"translatedText":"bonjour"}]}}`))
	}))
	t.Cleanup(srv.Close)

	tr := translate.NewGoogleTranslator(srv.URL, "secret")
	_, err := tr.Translate(t.Context(), translate.Request{Text: "hello"})

	require.NoError(t, err)
	require.Equal(t, "secret", header)
	// net/http puts the full URL in every transport error, which the pipeline
	// wraps into the run log. A key in the query string would land on disk.
	require.NotContains(t, rawQuery, "secret")
}

func TestGoogleTranslator_TransportErrorDoesNotLeakTheKey(t *testing.T) {
	t.Parallel()

	// Nothing is listening, so the client fails at dial time and the error
	// carries the request URL.
	tr := translate.NewGoogleTranslator("http://127.0.0.1:1/language/translate/v2", "AIzaSECRET")
	_, err := tr.Translate(t.Context(), translate.Request{Text: "hello"})

	require.Error(t, err)
	require.NotContains(t, err.Error(), "AIzaSECRET")
}

func TestGoogleTranslator_BatchKeepsIndexAlignment(t *testing.T) {
	t.Parallel()

	srv := googleServer(t, func(q string) string { return "[fr] " + q }, nil)

	tr := translate.NewGoogleTranslator(srv.URL, "")
	resps, err := tr.TranslateBatch(t.Context(), translate.BatchRequest{
		Texts:      []string{"one", "two", "three"},
		SourceLang: "en",
		TargetLang: "fr",
	})

	require.NoError(t, err)
	require.Len(t, resps, 3)
	require.Equal(t, "[fr] one", resps[0].Text)
	require.Equal(t, "[fr] two", resps[1].Text)
	require.Equal(t, "[fr] three", resps[2].Text)
}

func TestGoogleTranslator_ShortResponseArrayIsAnError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Two texts requested, one translation returned: accepting it would
		// shift every following segment onto the wrong translation.
		_, _ = w.Write([]byte(`{"data":{"translations":[{"translatedText":"un"}]}}`))
	}))
	t.Cleanup(srv.Close)

	tr := translate.NewGoogleTranslator(srv.URL, "")
	_, err := tr.TranslateBatch(t.Context(), translate.BatchRequest{Texts: []string{"one", "two"}})

	require.ErrorIs(t, err, translate.ErrShortBatchResponse)
}

func TestGoogleTranslator_UnescapesEntitiesAndKeepsNormalisedTokens(t *testing.T) {
	t.Parallel()

	// Cloud Translation escapes text content and may normalise our
	// self-closing token to the paired form; both must round-trip.
	srv := googleServer(t, func(string) string {
		return `L&#39;aube <x id="1"></x> se l&egrave;ve`
	}, nil)

	tr := translate.NewGoogleTranslator(srv.URL, "")
	resp, err := tr.Translate(t.Context(), translate.Request{Text: `The dawn <x id="1"/> rises`})

	require.NoError(t, err)
	require.Equal(t, `L'aube <x id="1"></x> se lève`, resp.Text)
}

func TestGoogleTranslator_SplitsOversizedBatches(t *testing.T) {
	t.Parallel()

	var got []googleRequest
	srv := googleServer(t, func(q string) string { return q }, &got)

	// 200 texts is over the 128-string ceiling, so the adapter must split.
	texts := make([]string, 200)
	for i := range texts {
		texts[i] = "x"
	}

	tr := translate.NewGoogleTranslator(srv.URL, "")
	resps, err := tr.TranslateBatch(t.Context(), translate.BatchRequest{Texts: texts})

	require.NoError(t, err)
	require.Len(t, resps, 200)
	require.Len(t, got, 2)
	require.Len(t, got[0].Q, 128)
	require.Len(t, got[1].Q, 72)
}

func TestGoogleTranslator_SplitsOnCharacterBudget(t *testing.T) {
	t.Parallel()

	var got []googleRequest
	srv := googleServer(t, func(q string) string { return q }, &got)

	// Three 3000-char texts: 5000 chars per request means one request each for
	// the first two, and the third joins none of them.
	texts := []string{strings.Repeat("a", 3000), strings.Repeat("b", 3000), strings.Repeat("c", 3000)}

	tr := translate.NewGoogleTranslator(srv.URL, "")
	resps, err := tr.TranslateBatch(t.Context(), translate.BatchRequest{Texts: texts})

	require.NoError(t, err)
	require.Len(t, resps, 3)
	require.Len(t, got, 3)
}

func TestGoogleTranslator_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":403}}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	tr := translate.NewGoogleTranslator(srv.URL, "")
	_, err := tr.Translate(t.Context(), translate.Request{Text: "x"})
	require.ErrorIs(t, err, translate.ErrProviderStatus)
}

func TestGoogleTranslator_EmptyTranslation(t *testing.T) {
	t.Parallel()

	srv := googleServer(t, func(string) string { return "" }, nil)

	tr := translate.NewGoogleTranslator(srv.URL, "")
	_, err := tr.Translate(t.Context(), translate.Request{Text: "x"})
	require.ErrorIs(t, err, translate.ErrEmptyTranslation)
}

func TestNewProvider_Google(t *testing.T) {
	t.Parallel()

	tr := translate.NewProvider(translate.ProviderGoogle, "", "key")
	require.Equal(t, "nmt-google", tr.Engine())
}
