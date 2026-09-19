package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// SystemOneQuestion defines a single evaluation question for TypeSafe System One.
type SystemOneQuestion struct {
	Type         string      `json:"type"`               // "noul", "choice", or "score"
	Instructions interface{} `json:"instructions"`       // string | object | array
	Criteria     interface{} `json:"criteria,omitempty"` // object for noul, map for choice, array for score
}

// SystemOneRequest represents the payload sent to POST /v1/systemone.
type SystemOneRequest struct {
	Model     string                        `json:"model"`
	State     interface{}                   `json:"state"`
	Questions map[string]SystemOneQuestion `json:"questions"`
}

// SystemOneAnswer represents the model's answer to a single question.
type SystemOneAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        *string            `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// SystemOneUsage represents token usage returned by TypeSafe API.
type SystemOneUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SystemOneResponse represents the top-level response from POST /v1/systemone.
type SystemOneResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]SystemOneAnswer `json:"answers"`
	Usage   SystemOneUsage             `json:"usage"`
}

// TypeSafeClient interacts with TypeSafe AI System One decision endpoints.
type TypeSafeClient struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	client  *http.Client
}

// NewTypeSafeClient creates a new client for TypeSafe AI System One.
func NewTypeSafeClient(baseURL, apiKey, model string, timeout time.Duration) *TypeSafeClient {
	if baseURL == "" {
		baseURL = "https://api.typesafe.ai/v1/systemone"
	}
	if model == "" || strings.EqualFold(model, "jev") {
		model = "jev-latest"
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	return &TypeSafeClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		Timeout: timeout,
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
		},
	}
}

// resolveEndpoint ensures the URL points to /v1/systemone
func (c *TypeSafeClient) resolveEndpoint() string {
	u := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(u, "/v1/systemone") {
		return u
	}
	if strings.HasSuffix(u, "/v1") {
		return u + "/systemone"
	}
	return u + "/v1/systemone"
}

// Evaluate sends a System One evaluation request and returns structured answers.
func (c *TypeSafeClient) Evaluate(ctx context.Context, state interface{}, questions map[string]SystemOneQuestion) (*SystemOneResponse, error) {
	reqBody := SystemOneRequest{
		Model:     c.Model,
		State:     state,
		Questions: questions,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("typesafe marshal error: %w", err)
	}

	endpoint := c.resolveEndpoint()

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("typesafe create request error: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if c.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
		}

		resp, err := c.client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("typesafe request attempt %d failed: %w", attempt, err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
				continue
			}
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("typesafe read response error: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
			// Rate limit or overloaded: back off and retry
			lastErr = fmt.Errorf("typesafe status %d: %s", resp.StatusCode, string(bodyBytes))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*2) * time.Second):
				continue
			}
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("typesafe API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		}

		var sysOneResp SystemOneResponse
		if err := json.Unmarshal(bodyBytes, &sysOneResp); err != nil {
			return nil, fmt.Errorf("typesafe unmarshal response error: %w (body: %s)", err, string(bodyBytes))
		}

		return &sysOneResp, nil
	}

	return nil, lastErr
}

// EvaluateNoul is a helper to evaluate a single yes/no question on a state.
func (c *TypeSafeClient) EvaluateNoul(ctx context.Context, state interface{}, instructions string, criteria map[string]string) (float64, error) {
	questions := map[string]SystemOneQuestion{
		"q": {
			Type:         "noul",
			Instructions: instructions,
		},
	}
	if len(criteria) > 0 {
		q := questions["q"]
		q.Criteria = criteria
		questions["q"] = q
	}

	resp, err := c.Evaluate(ctx, state, questions)
	if err != nil {
		return 0, err
	}

	ans, ok := resp.Answers["q"]
	if !ok || ans.Noul == nil {
		return 0, fmt.Errorf("typesafe response missing noul answer")
	}

	return *ans.Noul, nil
}

// EvaluateChoice is a helper to evaluate a choice question from a map of criteria options.
func (c *TypeSafeClient) EvaluateChoice(ctx context.Context, state interface{}, instructions string, criteria map[string]string) (string, map[string]float64, float64, error) {
	questions := map[string]SystemOneQuestion{
		"q": {
			Type:         "choice",
			Instructions: instructions,
			Criteria:     criteria,
		},
	}

	resp, err := c.Evaluate(ctx, state, questions)
	if err != nil {
		return "", nil, 0, err
	}

	ans, ok := resp.Answers["q"]
	if !ok || ans.Choice == nil {
		return "", nil, 0, fmt.Errorf("typesafe response missing choice answer")
	}

	conf := 0.0
	if ans.Confidence != nil {
		conf = *ans.Confidence
	}

	return *ans.Choice, ans.Probabilities, conf, nil
}

// EvaluateScore is a helper to evaluate a score question against ordered levels.
func (c *TypeSafeClient) EvaluateScore(ctx context.Context, state interface{}, instructions string, criteria []string) (float64, map[string]float64, float64, error) {
	questions := map[string]SystemOneQuestion{
		"q": {
			Type:         "score",
			Instructions: instructions,
			Criteria:     criteria,
		},
	}

	resp, err := c.Evaluate(ctx, state, questions)
	if err != nil {
		return 0, nil, 0, err
	}

	ans, ok := resp.Answers["q"]
	if !ok || ans.Score == nil {
		return 0, nil, 0, fmt.Errorf("typesafe response missing score answer")
	}

	conf := 0.0
	if ans.Confidence != nil {
		conf = *ans.Confidence
	}

	return *ans.Score, ans.Probabilities, conf, nil
}
