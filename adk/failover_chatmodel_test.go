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
	"io"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fakeChatModel struct {
	generate         func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error)
	stream           func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
	callbacksEnabled bool
}

func (m *fakeChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.generate(ctx, input, opts...)
}

func (m *fakeChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.stream(ctx, input, opts...)
}

func (m *fakeChatModel) IsCallbacksEnabled() bool {
	return m.callbacksEnabled
}

func drainMessageStream(sr *schema.StreamReader[*schema.Message]) ([]*schema.Message, error) {
	defer sr.Close()
	var out []*schema.Message
	for {
		chunk, err := sr.Recv()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, chunk)
	}
}

func streamWithMidError(chunks []*schema.Message, err error) *schema.StreamReader[*schema.Message] {
	sr, sw := schema.Pipe[*schema.Message](2)
	go func() {
		defer sw.Close()
		for _, c := range chunks {
			sw.Send(c, nil)
		}
		sw.Send(nil, err)
	}()
	return sr
}

func streamWithMidErrorControlled(chunks []*schema.Message, err error, firstSent chan struct{}, release chan struct{}) *schema.StreamReader[*schema.Message] {
	sr, sw := schema.Pipe[*schema.Message](2)
	go func() {
		defer sw.Close()
		for i, c := range chunks {
			sw.Send(c, nil)
			if i == 0 && firstSent != nil {
				close(firstSent)
				if release != nil {
					<-release
				}
			}
		}
		sw.Send(nil, err)
	}()
	return sr
}

func TestFailoverCurrentModelContext(t *testing.T) {
	t.Run("set and get", func(t *testing.T) {
		ctx := context.Background()
		m := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return schema.AssistantMessage("ok", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
			},
		}
		ctx = setFailoverCurrentModel(ctx, m)
		got := getFailoverCurrentModel(ctx)
		require.NotNil(t, got)
		require.Same(t, m, got.model)
	})

	t.Run("wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), failoverCurrentModelKey{}, "bad")
		require.Nil(t, getFailoverCurrentModel(ctx))
	})

	t.Run("missing", func(t *testing.T) {
		require.Nil(t, getFailoverCurrentModel(context.Background()))
	})
}

func TestFailoverProxyModel(t *testing.T) {
	t.Run("generate missing context", func(t *testing.T) {
		p := &failoverProxyModel{}
		_, err := p.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
	})

	t.Run("stream missing context", func(t *testing.T) {
		p := &failoverProxyModel{}
		_, err := p.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
	})

	t.Run("generate routes to current model", func(t *testing.T) {
		var called int32
		target := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				atomic.AddInt32(&called, 1)
				return schema.AssistantMessage("routed", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("routed", nil)}), nil
			},
		}
		ctx := setFailoverCurrentModel(context.Background(), target)
		p := &failoverProxyModel{}
		msg, err := p.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		require.Equal(t, "routed", msg.Content)
		require.Equal(t, int32(1), atomic.LoadInt32(&called))
	})
}

func TestFailoverModelWrapper_Generate(t *testing.T) {
	t.Run("delegates when GetFailoverModel nil", func(t *testing.T) {
		var called int32
		inner := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				atomic.AddInt32(&called, 1)
				return schema.AssistantMessage("inner", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("inner", nil)}), nil
			},
		}
		w := newFailoverModelWrapper(inner, &ModelFailoverConfig{
			MaxRetries:       2,
			ShouldFailover:   func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: nil,
		})
		msg, err := w.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		require.Equal(t, "inner", msg.Content)
		require.Equal(t, int32(1), atomic.LoadInt32(&called))
	})

	t.Run("failover to second model", func(t *testing.T) {
		wantErr := errors.New("first failed")
		var shouldCalls int32
		var m1Calls int32
		var m2Calls int32

		m1 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				atomic.AddInt32(&m1Calls, 1)
				return nil, wantErr
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return nil, errors.New("unused")
			},
		}
		m2 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				atomic.AddInt32(&m2Calls, 1)
				return schema.AssistantMessage("ok", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return nil, errors.New("unused")
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				return errors.Is(err, wantErr)
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				return m2, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		msg, err := w.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		require.Equal(t, "ok", msg.Content)
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
	})

	t.Run("canceled error delegates to ShouldFailover", func(t *testing.T) {
		var shouldCalls int32
		m1 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, context.Canceled
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return nil, errors.New("unused")
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 5,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				// User decides to stop on canceled error
				return !errors.Is(err, context.Canceled)
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := w.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.ErrorIs(t, err, context.Canceled)
		// ShouldFailover is called once and returns false, stopping failover
		require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
	})

	t.Run("stops when GetFailoverModel returns error", func(t *testing.T) {
		wantErr := errors.New("get model failed")
		var called int32
		inner := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				atomic.AddInt32(&called, 1)
				return schema.AssistantMessage("unused", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return nil, errors.New("unused")
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries:     3,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, wantErr
			},
		}

		w := newFailoverModelWrapper(inner, cfg)
		_, err := w.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.ErrorIs(t, err, wantErr)
		require.Equal(t, int32(0), atomic.LoadInt32(&called))
	})

	t.Run("stops when GetFailoverModel returns nil model", func(t *testing.T) {
		cfg := &ModelFailoverConfig{
			MaxRetries:     1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		msg, err := w.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, msg)
		require.Error(t, err)
		require.ErrorContains(t, err, "GetFailoverModel returned nil model")
	})
}

func TestFailoverModelWrapper_Stream(t *testing.T) {
	t.Run("returns stream when first attempt succeeds", func(t *testing.T) {
		var shouldCalls int32
		in := schema.UserMessage("hi")

		m1 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				require.Len(t, input, 1)
				require.Same(t, in, input[0])
				return schema.StreamReaderFromArray([]*schema.Message{
					schema.AssistantMessage("a", nil),
					schema.AssistantMessage("b", nil),
				}), nil
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 0,
			ShouldFailover: func(context.Context, *schema.Message, error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				return false
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := w.Stream(ctx, []*schema.Message{in})
		require.NoError(t, err)
		msgs, err := drainMessageStream(sr)
		require.NoError(t, err)
		require.Len(t, msgs, 2)
		require.Equal(t, "a", msgs[0].Content)
		require.Equal(t, "b", msgs[1].Content)
		require.Equal(t, int32(0), atomic.LoadInt32(&shouldCalls))
	})

	t.Run("failover when Stream returns error immediately", func(t *testing.T) {
		wantErr := errors.New("stream init failed")
		var shouldCalls int32
		var m1Calls int32
		var m2Calls int32

		m1 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				atomic.AddInt32(&m1Calls, 1)
				return nil, wantErr
			},
		}
		m2 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				atomic.AddInt32(&m2Calls, 1)
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				return errors.Is(err, wantErr)
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				return m2, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := w.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		msgs, err := drainMessageStream(sr)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.Equal(t, "ok", msgs[0].Content)
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
	})

	t.Run("failover when stream errors mid-way", func(t *testing.T) {
		streamErr := errors.New("mid error")
		var shouldCalls int32
		var seenOutput atomic.Value
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
					schema.AssistantMessage("p1", nil),
					schema.AssistantMessage("p2", nil),
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
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final", nil)}), nil
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, out *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				if errors.Is(err, streamErr) && out != nil {
					seenOutput.Store(out.Content)
				}
				return errors.Is(err, streamErr)
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				return m2, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := w.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		msgs, err := drainMessageStream(sr)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.Equal(t, "final", msgs[0].Content)
		require.Equal(t, "p1p2", seenOutput.Load())
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
	})

	t.Run("stop when ShouldFailover returns false for mid-way error", func(t *testing.T) {
		streamErr := errors.New("mid error")
		m1 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return streamWithMidError([]*schema.Message{schema.AssistantMessage("p", nil)}, streamErr), nil
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 3,
			ShouldFailover: func(context.Context, *schema.Message, error) bool {
				return false
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := w.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, sr)
		require.ErrorIs(t, err, streamErr)
	})

	t.Run("canceled mid-way error delegates to ShouldFailover", func(t *testing.T) {
		var shouldCalls int32
		m1 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return streamWithMidError([]*schema.Message{schema.AssistantMessage("p", nil)}, context.Canceled), nil
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 3,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				// User decides to stop on canceled error
				return !errors.Is(err, context.Canceled)
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := w.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, sr)
		require.ErrorIs(t, err, context.Canceled)
		// ShouldFailover is called once and returns false, stopping failover
		require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
	})

	t.Run("stop when Stream returns error immediately and ShouldFailover returns false", func(t *testing.T) {
		wantErr := errors.New("stream init failed")
		var shouldCalls int32
		var m1Calls int32

		m1 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				atomic.AddInt32(&m1Calls, 1)
				return nil, wantErr
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 3,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				require.ErrorIs(t, err, wantErr)
				return false
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := w.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, sr)
		require.ErrorIs(t, err, wantErr)
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
	})

	t.Run("stops when GetFailoverModel returns nil model", func(t *testing.T) {
		cfg := &ModelFailoverConfig{
			MaxRetries:     1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		sr, err := w.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, sr)
		require.Error(t, err)
		require.ErrorContains(t, err, "GetFailoverModel returned nil model")
	})

	t.Run("stops when GetFailoverModel returns error", func(t *testing.T) {
		wantErr := errors.New("get model failed")
		var called int32
		inner := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				atomic.AddInt32(&called, 1)
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries:     3,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, wantErr
			},
		}

		w := newFailoverModelWrapper(inner, cfg)
		sr, err := w.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, sr)
		require.ErrorIs(t, err, wantErr)
		require.Equal(t, int32(0), atomic.LoadInt32(&called))
	})

	t.Run("stops when ctx canceled during mid-way error handling", func(t *testing.T) {
		midErr := errors.New("mid error")
		var shouldCalls int32
		var m1Calls int32
		var m2Calls int32
		firstSent := make(chan struct{})
		release := make(chan struct{})

		m1 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				atomic.AddInt32(&m1Calls, 1)
				return streamWithMidErrorControlled(
					[]*schema.Message{schema.AssistantMessage("p", nil)},
					midErr,
					firstSent,
					release,
				), nil
			},
		}
		m2 := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return nil, errors.New("unused")
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				atomic.AddInt32(&m2Calls, 1)
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
			},
		}

		cfg := &ModelFailoverConfig{
			MaxRetries: 1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				return true
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
				require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				return m2, nil, nil
			},
		}

		w := newFailoverModelWrapper(&failoverProxyModel{}, cfg)
		baseCtx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		ctx, cancel := context.WithCancel(baseCtx)
		type result struct {
			sr  *schema.StreamReader[*schema.Message]
			err error
		}
		ch := make(chan result, 1)
		go func() {
			sr, err := w.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
			ch <- result{sr: sr, err: err}
		}()

		<-firstSent
		cancel()
		close(release)

		res := <-ch
		if res.sr != nil {
			res.sr.Close()
		}
		require.Nil(t, res.sr)
		require.ErrorIs(t, res.err, midErr)
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(0), atomic.LoadInt32(&m2Calls))
		require.Equal(t, int32(0), atomic.LoadInt32(&shouldCalls))
	})
}
