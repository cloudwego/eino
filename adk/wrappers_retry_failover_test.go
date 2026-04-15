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

func newFakeChatModel(
	gen func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error),
	stream func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error),
) *fakeChatModel {
	if gen == nil {
		gen = func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unused")
		}
	}
	if stream == nil {
		stream = func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		}
	}
	return &fakeChatModel{callbacksEnabled: true, generate: gen, stream: stream}
}

func TestRetryThenFailover(t *testing.T) {
	t.Run("Generate_RetryExhaustedTriggersFailover", func(t *testing.T) {
		modelErr := errors.New("model error")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, modelErr
		}, nil)
		m2 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("ok from m2", nil), nil
		}, nil)

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
	})

	t.Run("Generate_AllExhausted", func(t *testing.T) {
		modelErr := errors.New("always fails")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, modelErr
		}, nil)
		m2 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return nil, modelErr
		}, nil)

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
	})

	t.Run("Generate_RetrySucceedsNoFailover", func(t *testing.T) {
		var m1Calls int32
		var failoverCalled int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			n := atomic.AddInt32(&m1Calls, 1)
			if n == 1 {
				return nil, errors.New("transient error")
			}
			return schema.AssistantMessage("ok on retry", nil), nil
		}, nil)

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
	})

	t.Run("Generate_NonRetryableErrorTriggersFailover", func(t *testing.T) {
		nonRetryableErr := errors.New("non-retryable")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, nonRetryableErr
		}, nil)
		m2 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("ok from m2", nil), nil
		}, nil)

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
	})

	t.Run("Stream_RetryExhaustedTriggersFailover", func(t *testing.T) {
		streamErr := errors.New("stream mid error")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("partial", nil),
			}, streamErr), nil
		})
		m2 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok from m2", nil)}), nil
		})

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
	})

	t.Run("Stream_AllExhausted", func(t *testing.T) {
		streamErr := errors.New("always fails mid-stream")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("p", nil),
			}, streamErr), nil
		})
		m2 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("p", nil),
			}, streamErr), nil
		})

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
	})

	t.Run("ShouldRetry_Stream_TriggersFailover", func(t *testing.T) {
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("bad from m1", nil)}), nil
		})
		m2 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("good from m2", nil)}), nil
		})

		retryCfg := &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad from m1" {
					return &RetryDecision{Retry: true}
				}
				return &RetryDecision{Retry: false}
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
		sr, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		msgs, err := drainMessageStream(sr)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.Equal(t, "good from m2", msgs[0].Content)
		require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	})

	t.Run("ShouldRetry_Generate_TriggersFailover", func(t *testing.T) {
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return schema.AssistantMessage("bad from m1", nil), nil
		}, nil)
		m2 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("good from m2", nil), nil
		}, nil)

		retryCfg := &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad from m1" {
					return &RetryDecision{Retry: true}
				}
				return &RetryDecision{Retry: false}
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
		require.Equal(t, "good from m2", msg.Content)
		require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	})

	t.Run("Stream_GetFailoverModelReturnsNilModel", func(t *testing.T) {
		streamErr := errors.New("m1 always fails")
		var m1Calls int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, streamErr
		})

		retryCfg := &ModelRetryConfig{
			MaxRetries:  0,
			IsRetryAble: func(_ context.Context, err error) bool { return false },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
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
		_, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
		require.Contains(t, err.Error(), "returned nil model at attempt")
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	})

	t.Run("Stream_ContextCanceledDuringFailover", func(t *testing.T) {
		streamErr := errors.New("m1 fails")
		var m1Calls int32
		var failoverModelCalled int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, streamErr
		})

		ctx, cancel := context.WithCancel(context.Background())

		retryCfg := &ModelRetryConfig{
			MaxRetries:  0,
			IsRetryAble: func(_ context.Context, err error) bool { return false },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig{
			MaxRetries: 3,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				cancel()
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				atomic.AddInt32(&failoverModelCalled, 1)
				return nil, nil, nil
			},
		}

		wrapped := buildModelWrappers(m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx = withChatModelAgentExecCtx(ctx, &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(0), atomic.LoadInt32(&failoverModelCalled))
	})
}

func TestErrStreamCanceled_Failover(t *testing.T) {
	t.Run("Stream_NeverFailedOver", func(t *testing.T) {
		var m1Calls int32
		var failoverCalled int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("partial", nil),
			}, ErrStreamCanceled), nil
		})

		failoverCfg := &ModelFailoverConfig{
			MaxRetries: 2,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&failoverCalled, 1)
				return true
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				t.Fatal("GetFailoverModel should not be called for ErrStreamCanceled")
				return nil, nil, nil
			},
		}

		wrapped := buildModelWrappers(m1, &modelWrapperConfig{
			failoverConfig: failoverCfg,
		})

		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrStreamCanceled))
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(0), atomic.LoadInt32(&failoverCalled))
	})

	t.Run("Generate_NeverFailedOver", func(t *testing.T) {
		var m1Calls int32
		var failoverCalled int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, ErrStreamCanceled
		}, nil)

		failoverCfg := &ModelFailoverConfig{
			MaxRetries: 2,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&failoverCalled, 1)
				return true
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				t.Fatal("GetFailoverModel should not be called for ErrStreamCanceled")
				return nil, nil, nil
			},
		}

		wrapped := buildModelWrappers(m1, &modelWrapperConfig{
			failoverConfig: failoverCfg,
		})

		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrStreamCanceled))
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(0), atomic.LoadInt32(&failoverCalled))
	})
}
