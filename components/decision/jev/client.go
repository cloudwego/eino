/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package jev implements the decision.Decider interface against TypeSafe AI's
// Jev "System One" decision model.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/decision"
)

const (
	defaultBaseURL    = "https://api.typesafe.ai"
	defaultModel      = "jev-latest"
	defaultTimeout    = 10 * time.Second
	defaultMaxRetries = 2

	defaultRetryBackoff = 200 * time.Millisecond
	maxRetryDelay       = 30 * time.Second

	systemOnePath = "/v1/systemone"

	envAPIKey = "TYPESAFE_API_KEY"

	maxErrorBodyLen = 512
)

// Config is the configuration of the Jev client.
type Config struct {
	// APIKey is the TypeSafe API key. Defaults to the TYPESAFE_API_KEY environment variable.
	APIKey string
	// BaseURL is the base URL of the TypeSafe API. Defaults to https://api.typesafe.ai.
	BaseURL string
	// Model is the model name. Defaults to "jev-latest".
	Model string
	// HTTPClient is the HTTP client used to call the API. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Timeout is the per-request timeout. Defaults to 10s.
	Timeout time.Duration
	// MaxRetries is the maximum number of retries for HTTP 429 and 5xx responses. Defaults to 2. Negative values disable retries.
	MaxRetries int
}

// Client is a decision.Decider backed by the TypeSafe Jev model.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	timeout    time.Duration
	maxRetries int
}

// NewClient creates a new Jev client.
func NewClient(ctx context.Context, cfg *Config) (*Client, error) {
	if cfg == nil {
		cfg = &Config{}
	}

	c := &Client{
		apiKey:     cfg.APIKey,
		baseURL:    cfg.BaseURL,
		model:      cfg.Model,
		httpClient: cfg.HTTPClient,
		timeout:    cfg.Timeout,
		maxRetries: cfg.MaxRetries,
	}

	if c.apiKey == "" {
		c.apiKey = os.Getenv(envAPIKey)
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("jev: api key is required, set Config.APIKey or the %s environment variable", envAPIKey)
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	if c.model == "" {
		c.model = defaultModel
	}
	if c.httpClient == nil {
		c.httpClient = http.DefaultClient
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.maxRetries == 0 {
		c.maxRetries = defaultMaxRetries
	}
	if c.maxRetries < 0 {
		c.maxRetries = 0
	}

	return c, nil
}

var (
	_ decision.Decider   = (*Client)(nil)
	_ components.Typer   = (*Client)(nil)
	_ components.Checker = (*Client)(nil)
)

// GetType returns the type name of the component.
func (c *Client) GetType() string {
	return "Jev"
}

// IsCallbacksEnabled indicates that the client invokes callbacks itself.
func (c *Client) IsCallbacksEnabled() bool {
	return true
}

// Decide evaluates all questions against the state with a single Jev request
// and returns one answer per question id.
func (c *Client) Decide(ctx context.Context, state any, questions map[string]decision.Question, opts ...decision.Option) (answers map[string]*decision.Answer, err error) {
	ctx = callbacks.EnsureRunInfo(ctx, c.GetType(), components.ComponentOfDecision)
	ctx = callbacks.OnStart(ctx, &decision.CallbackInput{
		State:     state,
		Questions: questions,
	})
	defer func() {
		if err != nil {
			callbacks.OnError(ctx, err)
		}
	}()

	options := decision.GetCommonOptions(nil, opts...)
	model := c.model
	if options.Model != nil {
		model = *options.Model
	}

	req := wireRequest{
		State:     state,
		Model:     model,
		Questions: make(map[string]wireQuestion, len(questions)),
	}
	for id, q := range questions {
		req.Questions[id] = wireQuestion{
			Type:         string(q.Type),
			Instructions: q.Instructions,
			Criteria:     q.Criteria,
		}
	}

	resp, err := c.do(ctx, &req)
	if err != nil {
		return nil, err
	}

	answers = make(map[string]*decision.Answer, len(resp.Answers))
	for id, wa := range resp.Answers {
		answer, convErr := convAnswer(id, wa)
		if convErr != nil {
			return nil, convErr
		}
		answers[id] = answer
	}

	ctx = callbacks.OnEnd(ctx, &decision.CallbackOutput{
		Answers: answers,
		Extra: map[string]any{
			"model": resp.Model,
			"usage": resp.Usage,
		},
	})

	return answers, nil
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireRequest struct {
	State     any                     `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]wireQuestion `json:"questions"`
}

// Usage is the token usage of a Jev request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type wireAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Score         *float64           `json:"score"`
	Noul          *float64           `json:"noul"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    *float64           `json:"confidence"`
	Legend        []string           `json:"legend"`
}

type wireResponse struct {
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   Usage                 `json:"usage"`
}

func convAnswer(id string, wa wireAnswer) (*decision.Answer, error) {
	a := &decision.Answer{
		Type:          decision.QuestionType(wa.Type),
		Probabilities: wa.Probabilities,
		Confidence:    wa.Confidence,
		Legend:        wa.Legend,
	}

	switch decision.QuestionType(wa.Type) {
	case decision.QuestionTypeChoice:
		a.Choice = wa.Choice
	case decision.QuestionTypeScore:
		if wa.Score == nil {
			return nil, fmt.Errorf("jev: score answer for question %q is missing the score field", id)
		}
		a.Score = *wa.Score
	case decision.QuestionTypeNoul:
		if wa.Noul == nil {
			return nil, fmt.Errorf("jev: noul answer for question %q is missing the noul field", id)
		}
		a.Noul = *wa.Noul
	default:
		return nil, fmt.Errorf("jev: answer for question %q has unknown type %q", id, wa.Type)
	}

	return a, nil
}

func (c *Client) do(ctx context.Context, req *wireRequest) (*wireResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("jev: marshal request: %w", err)
	}

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		// The per-attempt timeout must cover reading the response body, so the
		// cancel runs only after the body has been fully consumed and closed.
		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
		resp, doErr := c.doOnce(reqCtx, body)
		if doErr != nil {
			cancel()
			return nil, doErr
		}

		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			wr, parseErr := parseResponse(resp)
			cancel()
			return wr, parseErr
		}

		retryAfter := resp.Header.Get("Retry-After")
		err = readError(resp)
		cancel()
		if !isRetryable(resp.StatusCode) || attempt == c.maxRetries {
			return nil, err
		}

		if waitErr := sleepWithContext(ctx, retryDelay(retryAfter, attempt)); waitErr != nil {
			return nil, waitErr
		}
	}

	return nil, fmt.Errorf("jev: request failed after %d attempts", c.maxRetries+1)
}

func (c *Client) doOnce(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+systemOnePath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev: do request: %w", err)
	}

	return resp, nil
}

func parseResponse(resp *http.Response) (*wireResponse, error) {
	defer resp.Body.Close()

	var wr wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, fmt.Errorf("jev: decode response: %w", err)
	}

	return &wr, nil
}

func readError(resp *http.Response) error {
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyLen))
	return fmt.Errorf("jev: unexpected status %d: %s", resp.StatusCode, string(body))
}

func isRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func retryDelay(retryAfter string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		d := time.Duration(seconds) * time.Second
		if d <= 0 && seconds > 0 {
			// Overflowed for an absurd Retry-After value; clamp to the cap.
			return maxRetryDelay
		}
		if d > maxRetryDelay {
			return maxRetryDelay
		}
		return d
	}

	// Guard the exponential shift against overflow for very large MaxRetries.
	d := defaultRetryBackoff << attempt
	if d <= 0 || d > maxRetryDelay {
		return maxRetryDelay
	}
	return d
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
