package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultCometTimeout = 60 * time.Second

var (
	ErrCometEmptyEndpoint = errors.New("eval: COMET endpoint is empty")
	ErrCometStatus        = errors.New("eval: COMET service returned a non-2xx status")
	ErrCometEmptyScores   = errors.New("eval: COMET service returned no scores")
)

// Sample is one scoring item. Ref is empty for reference-free QE (CometKiwi);
// set it for reference-based COMET.
type Sample struct {
	Src string `json:"src"`
	MT  string `json:"mt"`
	Ref string `json:"ref,omitempty"`
}

type cometRequest struct {
	Samples []Sample `json:"samples"`
}

type cometResponse struct {
	Scores []float64 `json:"scores"`
}

// CometClient calls a COMET scoring service. The same client serves reference-
// free QE (via Estimate / samples without Ref) and reference-based COMET (via
// Score with Ref set). Scores are typically in [0,1] (higher = better).
type CometClient struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

// NewCometClient builds a COMET client targeting endpoint.
func NewCometClient(endpoint, apiKey string) *CometClient {
	return &CometClient{
		Endpoint: endpoint,
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: defaultCometTimeout},
	}
}

// Score returns one score per sample in order.
func (c *CometClient) Score(ctx context.Context, samples []Sample) ([]float64, error) {
	if c.Endpoint == "" {
		return nil, ErrCometEmptyEndpoint
	}

	payload, err := json.Marshal(cometRequest{Samples: samples})
	if err != nil {
		return nil, fmt.Errorf("unable to marshal comet request, err: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("unable to build comet request, err: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: defaultCometTimeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unable to call comet service, err: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("unable to score: status %d body %q, err: %w", resp.StatusCode, string(body), ErrCometStatus)
	}

	var decoded cometResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("unable to decode comet response, err: %w", err)
	}
	if len(decoded.Scores) == 0 {
		return nil, ErrCometEmptyScores
	}
	return decoded.Scores, nil
}

// Estimate returns a reference-free QE score for a single (source, candidate)
// pair. It satisfies the pipeline's quality-estimator contract used to drive
// selective LLM escalation.
func (c *CometClient) Estimate(ctx context.Context, source, candidate string) (float64, error) {
	scores, err := c.Score(ctx, []Sample{{Src: source, MT: candidate}})
	if err != nil {
		return 0, err
	}
	return scores[0], nil
}
