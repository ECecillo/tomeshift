package llm_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/ebooks-translator/internal/llm"
	"github.com/ECecillo/ebooks-translator/internal/llm/llmtest"
)

func TestHTTPPostEditor_RefinesAndPreservesPlaceholders(t *testing.T) {
	t.Parallel()

	srv := llmtest.NewServer(func(nmt string) string {
		// Improve fluency while keeping the placeholder token.
		return strings.ReplaceAll(nmt, "[fr] ", "")
	})
	t.Cleanup(srv.Close)

	pe := llm.NewHTTPPostEditor(srv.URL, "")
	resp, err := pe.PostEdit(t.Context(), llm.PostEditRequest{
		SourceText: `A <x id="1"/> claim.`,
		NMTText:    `[fr] A <x id="1"/> claim.`,
		SourceLang: "en", TargetLang: "fr",
	})

	require.NoError(t, err)
	require.Equal(t, `A <x id="1"/> claim.`, resp.TranslatedText)
	require.Equal(t, "post-edited", resp.Notes)
}

func TestHTTPPostEditor_RejectsPlaceholderChange(t *testing.T) {
	t.Parallel()

	srv := llmtest.NewServer(func(string) string {
		return "translation without the token" // dropped <x id="1"/>
	})
	t.Cleanup(srv.Close)

	pe := llm.NewHTTPPostEditor(srv.URL, "")
	_, err := pe.PostEdit(t.Context(), llm.PostEditRequest{
		NMTText: `A <x id="1"/> claim.`,
	})
	require.ErrorIs(t, err, llm.ErrPlaceholderChange)
}

func TestHTTPPostEditor_EmptyEndpoint(t *testing.T) {
	t.Parallel()

	pe := llm.NewHTTPPostEditor("", "")
	_, err := pe.PostEdit(t.Context(), llm.PostEditRequest{NMTText: "x"})
	require.ErrorIs(t, err, llm.ErrEmptyEndpoint)
}

func TestHTTPPostEditor_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	pe := llm.NewHTTPPostEditor(srv.URL, "")
	_, err := pe.PostEdit(t.Context(), llm.PostEditRequest{NMTText: "x"})
	require.ErrorIs(t, err, llm.ErrProviderStatus)
}

func TestSafePostEdit_FallsBackOnError(t *testing.T) {
	t.Parallel()

	// Editor that drops the placeholder -> SafePostEdit must keep the NMT draft.
	srv := llmtest.NewServer(func(string) string { return "broken" })
	t.Cleanup(srv.Close)

	pe := llm.NewHTTPPostEditor(srv.URL, "")
	text, edited := llm.SafePostEdit(t.Context(), pe, llm.PostEditRequest{
		NMTText: `keep <x id="1"/> intact`,
	})

	require.False(t, edited)
	require.Equal(t, `keep <x id="1"/> intact`, text)
}

func TestSafePostEdit_NilEditor(t *testing.T) {
	t.Parallel()

	text, edited := llm.SafePostEdit(t.Context(), nil, llm.PostEditRequest{NMTText: "draft"})
	require.False(t, edited)
	require.Equal(t, "draft", text)
}
