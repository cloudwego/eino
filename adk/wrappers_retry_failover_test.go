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

package adk

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestRetryThenFailover_Generate_RetryExhaustedTriggersFailover tests the combined
// retry + failover path for Generate: m1 always fails, retry exhausted, failover to m2 which succeeds.
func TestRetryThenFailover_Generate_RetryExhaustedTriggersFailover(t *testing.T) {
	modelErr := errors.New("model error")
	var m1Calls int32
	var m2Calls int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, modelErr
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}
	m2 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("ok from m2", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}

	retryCfg := &ModelRetryConfig{
		MaxRetries:  2,
		IsRetryAble: func(_ context.Context, err error) bool { return true },
		BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
	}

	failoverCfg := &ModelFailoverConfig{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
			return err != nil
		},
		GetFailoverModel: func(_ context.Context, fc *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			require.NotNil(t, fc.LastErr)
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers(m1, &modelWrapperConfig{
		retryConfig:    retryCfg,
		failoverConfig: failoverCfg,
	})

	ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.Equal(t, "ok from m2", msg.Content)

	// m1: 1 (lastSuccess) + 2 retries = 3 calls on lastSuccess attempt,
	// then failover to m2 which also goes through retry wrapper: 1 call succeeds.
	require.Equal(t, int32(3), atomic.LoadInt32(&m1Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
}

// TestRetryThenFailover_Generate_AllExhausted tests: m1 retry exhausted → failover to m2 → m2 retry exhausted → final error.
func TestRetryThenFailover_Generate_AllExhausted(t *testing.T) {
	modelErr := errors.New("always fails")
	var m1Calls int32
	var m2Calls int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, modelErr
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}
	m2 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return nil, modelErr
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}

	retryCfg := &ModelRetryConfig{
		MaxRetries:  1,
		IsRetryAble: func(_ context.Context, err error) bool { return true },
		BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
	}

	failoverCfg := &ModelFailoverConfig{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
			return err != nil
		},
		GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers(m1, &modelWrapperConfig{
		retryConfig:    retryCfg,
		failoverConfig: failoverCfg,
	})

	ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	_, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.Error(t, err)

	// Should be RetryExhaustedError from m2's retry wrapper
	var retryErr *RetryExhaustedError
	require.True(t, errors.As(err, &retryErr))

	// m1: 1 initial + 1 retry = 2 calls
	require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
	// m2: 1 initial + 1 retry = 2 calls
	require.Equal(t, int32(2), atomic.LoadInt32(&m2Calls))
}

// TestRetryThenFailover_Stream_RetryExhaustedTriggersFailover tests stream path:
// m1 stream always errors mid-way, retry exhausted, failover to m2 which succeeds.
func TestRetryThenFailover_Stream_RetryExhaustedTriggersFailover(t *testing.T) {
	streamErr := errors.New("stream mid error")
	var m1Calls int32
	var m2Calls int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unused")
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("partial", nil),
			}, streamErr), nil
		},
	}
	m2 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unused")
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok from m2", nil)}), nil
		},
	}

	retryCfg := &ModelRetryConfig{
		MaxRetries:  1,
		IsRetryAble: func(_ context.Context, err error) bool { return true },
		BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
	}

	failoverCfg := &ModelFailoverConfig{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
			return err != nil
		},
		GetFailoverModel: func(_ context.Context, fc *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			require.NotNil(t, fc.LastErr)
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers(m1, &modelWrapperConfig{
		retryConfig:    retryCfg,
		failoverConfig: failoverCfg,
	})

	ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	sr, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	msgs, err := drainMessageStream(sr)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "ok from m2", msgs[0].Content)

	// m1: 1 initial + 1 retry = 2 calls on lastSuccess attempt
	require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
}

// TestRetryThenFailover_Generate_RetrySucceedsNoFailover tests that when retry
// succeeds on the first model, failover is never triggered.
func TestRetryThenFailover_Generate_RetrySucceedsNoFailover(t *testing.T) {
	var m1Calls int32
	var failoverCalled int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			n := atomic.AddInt32(&m1Calls, 1)
			if n == 1 {
				return nil, errors.New("transient error")
			}
			return schema.AssistantMessage("ok on retry", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}

	retryCfg := &ModelRetryConfig{
		MaxRetries:  2,
		IsRetryAble: func(_ context.Context, err error) bool { return true },
		BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
	}

	failoverCfg := &ModelFailoverConfig{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
			atomic.AddInt32(&failoverCalled, 1)
			return true
		},
		GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			t.Fatal("GetFailoverModel should not be called when retry succeeds")
			return nil, nil, nil
		},
	}

	wrapped := buildModelWrappers(m1, &modelWrapperConfig{
		retryConfig:    retryCfg,
		failoverConfig: failoverCfg,
	})

	ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.Equal(t, "ok on retry", msg.Content)

	// 2 calls: first fails, second succeeds via retry
	require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
	// ShouldFailover should never be called
	require.Equal(t, int32(0), atomic.LoadInt32(&failoverCalled))
}

// TestRetryThenFailover_Generate_NonRetryableErrorTriggersFailover tests that a non-retryable
// error skips retry and directly triggers failover.
func TestRetryThenFailover_Generate_NonRetryableErrorTriggersFailover(t *testing.T) {
	nonRetryableErr := errors.New("non-retryable")
	var m1Calls int32
	var m2Calls int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, nonRetryableErr
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}
	m2 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("ok from m2", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}

	retryCfg := &ModelRetryConfig{
		MaxRetries: 3,
		IsRetryAble: func(_ context.Context, err error) bool {
			// Only non-retryable errors
			return !errors.Is(err, nonRetryableErr)
		},
		BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
	}

	failoverCfg := &ModelFailoverConfig{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
			return err != nil
		},
		GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers(m1, &modelWrapperConfig{
		retryConfig:    retryCfg,
		failoverConfig: failoverCfg,
	})

	ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.Equal(t, "ok from m2", msg.Content)

	// m1 called only once — non-retryable error skips retry
	require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
}

// TestRetryThenFailover_Stream_AllExhausted tests stream path when both retry and failover are exhausted.
func TestRetryThenFailover_Stream_AllExhausted(t *testing.T) {
	streamErr := errors.New("always fails mid-stream")
	var m1Calls int32
	var m2Calls int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unused")
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("p", nil),
			}, streamErr), nil
		},
	}
	m2 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unused")
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("p", nil),
			}, streamErr), nil
		},
	}

	retryCfg := &ModelRetryConfig{
		MaxRetries:  1,
		IsRetryAble: func(_ context.Context, err error) bool { return true },
		BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
	}

	failoverCfg := &ModelFailoverConfig{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
			return err != nil
		},
		GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers(m1, &modelWrapperConfig{
		retryConfig:    retryCfg,
		failoverConfig: failoverCfg,
	})

	ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	_, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.Error(t, err)

	var retryErr *RetryExhaustedError
	require.True(t, errors.As(err, &retryErr))

	// m1: 1 initial + 1 retry = 2 calls
	require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
	// m2: 1 initial + 1 retry = 2 calls
	require.Equal(t, int32(2), atomic.LoadInt32(&m2Calls))
}
