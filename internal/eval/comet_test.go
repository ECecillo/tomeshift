package eval_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/eval"
	"github.com/ECecillo/tomeshift/internal/eval/comettest"
)

func TestCometClient_Score(t *testing.T) {
	t.Parallel()

	srv := comettest.NewServer(comettest.Constant(0.83))
	t.Cleanup(srv.Close)

	c := eval.NewCometClient(srv.URL, "")
	scores, err := c.Score(t.Context(), []eval.Sample{
		{Src: "Hello", MT: "Bonjour"},
		{Src: "World", MT: "Monde"},
	})
	require.NoError(t, err)
	require.Len(t, scores, 2)
	require.InDelta(t, 0.83, scores[0], 1e-9)
}

func TestCometClient_Estimate_QE(t *testing.T) {
	t.Parallel()

	// QE: score reflects mt length proximity to src as a stand-in signal.
	srv := comettest.NewServer(func(src, mt, _ string) float64 {
		if mt == "" {
			return 0
		}
		return 0.42
	})
	t.Cleanup(srv.Close)

	c := eval.NewCometClient(srv.URL, "")
	score, err := c.Estimate(t.Context(), "Hello world", "Bonjour le monde")
	require.NoError(t, err)
	require.InDelta(t, 0.42, score, 1e-9)
}

func TestCometClient_EmptyEndpoint(t *testing.T) {
	t.Parallel()
	_, err := eval.NewCometClient("", "").Estimate(t.Context(), "a", "b")
	require.ErrorIs(t, err, eval.ErrCometEmptyEndpoint)
}

func TestCometClient_NonOKStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	_, err := eval.NewCometClient(srv.URL, "").Estimate(t.Context(), "a", "b")
	require.ErrorIs(t, err, eval.ErrCometStatus)
}
