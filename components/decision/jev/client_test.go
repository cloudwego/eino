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

package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/decision"
)

const testAPIKey = "test-api-key"

func newTestQuestions(t *testing.T) map[string]decision.Question {
	t.Helper()

	choice, err := decision.NewChoiceQuestion("which team should handle this", map[string]string{
		"billing":  "questions about invoices and payments",
		"shipping": "questions about delivery",
	})
	require.NoError(t, err)

	score, err := decision.NewScoreQuestion("rate the urgency", []string{"not urgent", "somewhat urgent", "very urgent"})
	require.NoError(t, err)

	return map[string]decision.Question{
		"route":   choice,
		"urgency": score,
		"unclear": decision.NewNoulQuestion("how uncertain is the intent"),
	}
}

const testResponseBody = `{
	"model": "jev-1.13.0",
	"answers": {
		"route": {"type":"choice","choice":"billing","probabilities":{"billing":1},"confidence":1},
		"urgency": {"type":"score","score":1.43,"probabilities":{"0":0.1,"1":0.57,"2":0.43},"confidence":0.9,"legend":["not urgent","somewhat urgent","very urgent"]},
		"unclear": {"type":"noul","noul":0.97}
	},
	"usage": {"input_tokens":360,"output_tokens":39}
}`

func TestDecideRequestSerialization(t *testing.T) {
	var reqBody []byte
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, systemOnePath, r.URL.Path)
		authHeader = r.Header.Get("Authorization")
		reqBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testResponseBody))
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	questions := newTestQuestions(t)
	_, err = cli.Decide(context.Background(), map[string]any{"text": "where is my invoice"}, questions)
	require.NoError(t, err)

	assert.Equal(t, "Bearer "+testAPIKey, authHeader)

	var req map[string]any
	require.NoError(t, json.Unmarshal(reqBody, &req))
	assert.Equal(t, map[string]any{"text": "where is my invoice"}, req["state"])
	assert.Equal(t, defaultModel, req["model"])

	qs, ok := req["questions"].(map[string]any)
	require.True(t, ok)
	require.Len(t, qs, 3)

	route := qs["route"].(map[string]any)
	assert.Equal(t, "choice", route["type"])
	assert.Equal(t, "which team should handle this", route["instructions"])
	assert.Equal(t, map[string]any{
		"billing":  "questions about invoices and payments",
		"shipping": "questions about delivery",
	}, route["criteria"])

	urgency := qs["urgency"].(map[string]any)
	assert.Equal(t, "score", urgency["type"])
	assert.Equal(t, []any{"not urgent", "somewhat urgent", "very urgent"}, urgency["criteria"])

	unclear := qs["unclear"].(map[string]any)
	assert.Equal(t, "noul", unclear["type"])
	_, hasCriteria := unclear["criteria"]
	assert.False(t, hasCriteria)
}

func TestDecideResponseParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testResponseBody))
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	answers, err := cli.Decide(context.Background(), "some state", newTestQuestions(t))
	require.NoError(t, err)
	require.Len(t, answers, 3)

	route := answers["route"]
	assert.Equal(t, decision.QuestionTypeChoice, route.Type)
	assert.Equal(t, "billing", route.Choice)
	assert.Equal(t, map[string]float64{"billing": 1}, route.Probabilities)
	require.NotNil(t, route.Confidence)
	assert.Equal(t, 1.0, *route.Confidence)

	urgency := answers["urgency"]
	assert.Equal(t, decision.QuestionTypeScore, urgency.Type)
	assert.Equal(t, 1.43, urgency.Score)
	assert.Equal(t, map[string]float64{"0": 0.1, "1": 0.57, "2": 0.43}, urgency.Probabilities)
	require.NotNil(t, urgency.Confidence)
	assert.Equal(t, 0.9, *urgency.Confidence)
	assert.Equal(t, []string{"not urgent", "somewhat urgent", "very urgent"}, urgency.Legend)

	unclear := answers["unclear"]
	assert.Equal(t, decision.QuestionTypeNoul, unclear.Type)
	assert.Equal(t, 0.97, unclear.Noul)
	assert.Nil(t, unclear.Confidence)
	assert.False(t, unclear.IsConfident(0))
}

func TestDecideModelOptionOverride(t *testing.T) {
	var reqBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testResponseBody))
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = cli.Decide(context.Background(), "state", newTestQuestions(t), decision.WithModel("jev-1.13.0"))
	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(reqBody, &req))
	assert.Equal(t, "jev-1.13.0", req["model"])
}

func TestDecideNonRetryableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad question"}`))
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = cli.Decide(context.Background(), "state", newTestQuestions(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	assert.Contains(t, err.Error(), "bad question")
}

func TestDecideRetryOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testResponseBody))
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	answers, err := cli.Decide(context.Background(), "state", newTestQuestions(t))
	require.NoError(t, err)
	assert.Len(t, answers, 3)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestDecideRetryExhausted(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL, MaxRetries: 1})
	require.NoError(t, err)

	_, err = cli.Decide(context.Background(), "state", newTestQuestions(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestDecideDefaultMaxRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = cli.Decide(context.Background(), "state", newTestQuestions(t))
	require.Error(t, err)
	assert.Equal(t, int32(1+defaultMaxRetries), atomic.LoadInt32(&calls))
}

func TestDecideRetryDisabledWithNegativeMaxRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL, MaxRetries: -1})
	require.NoError(t, err)

	_, err = cli.Decide(context.Background(), "state", newTestQuestions(t))
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestDecideContextCancelDuringRetryBackoff(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = cli.Decide(ctx, "state", newTestQuestions(t))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestDecideRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testResponseBody))
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL, Timeout: 50 * time.Millisecond})
	require.NoError(t, err)

	_, err = cli.Decide(context.Background(), "state", newTestQuestions(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jev: do request")
}

func TestDecideAnswerParseErrors(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "unknown type",
			body:    `{"answers":{"q":{"type":"mystery"}}}`,
			wantErr: `unknown type "mystery"`,
		},
		{
			name:    "missing score",
			body:    `{"answers":{"q":{"type":"score"}}}`,
			wantErr: "missing the score field",
		},
		{
			name:    "missing noul",
			body:    `{"answers":{"q":{"type":"noul"}}}`,
			wantErr: "missing the noul field",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
			require.NoError(t, err)

			_, err = cli.Decide(context.Background(), "state", newTestQuestions(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestDecideOnErrorCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	var started, ended, errored int32
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			atomic.AddInt32(&started, 1)
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			atomic.AddInt32(&ended, 1)
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			assert.Equal(t, components.ComponentOfDecision, info.Component)
			atomic.AddInt32(&errored, 1)
			return ctx
		}).
		Build()

	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{
		Type:      cli.GetType(),
		Component: components.ComponentOfDecision,
	}, handler)

	_, err = cli.Decide(ctx, "state", newTestQuestions(t))
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&started))
	assert.Equal(t, int32(0), atomic.LoadInt32(&ended))
	assert.Equal(t, int32(1), atomic.LoadInt32(&errored))
}

func TestDecideCallbacks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testResponseBody))
	}))
	defer srv.Close()

	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey, BaseURL: srv.URL})
	require.NoError(t, err)

	var started, ended int32
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			assert.Equal(t, components.ComponentOfDecision, info.Component)
			in, ok := input.(*decision.CallbackInput)
			require.True(t, ok)
			assert.NotEmpty(t, in.Questions)
			atomic.AddInt32(&started, 1)
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			assert.Equal(t, components.ComponentOfDecision, info.Component)
			out, ok := output.(*decision.CallbackOutput)
			require.True(t, ok)
			assert.Len(t, out.Answers, 3)
			assert.Equal(t, "jev-1.13.0", out.Extra["model"])
			assert.Equal(t, Usage{InputTokens: 360, OutputTokens: 39}, out.Extra["usage"])
			atomic.AddInt32(&ended, 1)
			return ctx
		}).
		Build()

	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{
		Type:      cli.GetType(),
		Component: components.ComponentOfDecision,
	}, handler)

	_, err = cli.Decide(ctx, "state", newTestQuestions(t))
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&started))
	assert.Equal(t, int32(1), atomic.LoadInt32(&ended))
}

func TestNewClientMissingAPIKey(t *testing.T) {
	t.Setenv(envAPIKey, "")

	_, err := NewClient(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), envAPIKey)
}

func TestClientMeta(t *testing.T) {
	cli, err := NewClient(context.Background(), &Config{APIKey: testAPIKey})
	require.NoError(t, err)

	assert.Equal(t, "Jev", cli.GetType())
	assert.True(t, cli.IsCallbacksEnabled())
}
