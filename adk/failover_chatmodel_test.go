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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func (m *fakeChatModel) GetType() string {
	return "fake_chat_model"
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
		ctx = typedSetFailoverCurrentModel[*schema.Message](ctx, m)
		got, ok := typedGetFailoverCurrentModel[*schema.Message](ctx)
		require.True(t, ok)
		require.Same(t, m, got)
	})

	t.Run("wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), failoverCurrentModelKey{}, "bad")
		_, ok := typedGetFailoverCurrentModel[*schema.Message](ctx)
		require.False(t, ok)
	})

	t.Run("missing", func(t *testing.T) {
		_, ok := typedGetFailoverCurrentModel[*schema.Message](context.Background())
		require.False(t, ok)
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
		ctx := typedSetFailoverCurrentModel[*schema.Message](context.Background(), target)
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
		w := newFailoverModelWrapper[*schema.Message](inner, &ModelFailoverConfig[*schema.Message]{
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				return errors.Is(err, wantErr)
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				return m2, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 5,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				// User decides to stop on canceled error
				return !errors.Is(err, context.Canceled)
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries:     3,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, wantErr
			},
		}

		w := newFailoverModelWrapper[*schema.Message](inner, cfg)
		_, err := w.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.ErrorIs(t, err, wantErr)
		require.Equal(t, int32(0), atomic.LoadInt32(&called))
	})

	t.Run("stops when GetFailoverModel returns nil model", func(t *testing.T) {
		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries:     1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 0,
			ShouldFailover: func(context.Context, *schema.Message, error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				return false
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				return errors.Is(err, wantErr)
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				return m2, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, out *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				if errors.Is(err, streamErr) && out != nil {
					seenOutput.Store(out.Content)
				}
				return errors.Is(err, streamErr)
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				return m2, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 3,
			ShouldFailover: func(context.Context, *schema.Message, error) bool {
				return false
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 3,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				// User decides to stop on canceled error
				return !errors.Is(err, context.Canceled)
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 3,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				require.ErrorIs(t, err, wantErr)
				return false
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m1, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := w.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, sr)
		require.ErrorIs(t, err, wantErr)
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
	})

	t.Run("stops when GetFailoverModel returns nil model", func(t *testing.T) {
		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries:     1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries:     3,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, wantErr
			},
		}

		w := newFailoverModelWrapper[*schema.Message](inner, cfg)
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

		cfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool {
				atomic.AddInt32(&shouldCalls, 1)
				return true
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				return m2, nil, nil
			},
		}

		w := newFailoverModelWrapper[*schema.Message](&failoverProxyModel{}, cfg)
		baseCtx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
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

func TestTypedConsumeStream_EmptyAgenticStream(t *testing.T) {
	sr, sw := schema.Pipe[*schema.AgenticMessage](1)
	sw.Close()

	msg, err := typedConsumeStream(sr)
	assert.Nil(t, err, "empty stream should not return error")
	assert.NotNil(t, msg, "empty stream should return non-nil message from ConcatAgenticMessages")
}

func TestTypedConsumeStream_AgenticMidStreamError(t *testing.T) {
	midErr := errors.New("mid-stream failure")
	sr := streamWithMidErrorAgentic(
		[]*schema.AgenticMessage{agenticChunk("chunk1"), agenticChunk("chunk2")},
		midErr,
	)

	msg, err := typedConsumeStream(sr)
	assert.ErrorIs(t, err, midErr, "should return the mid-stream error")
	assert.NotNil(t, msg, "should return concatenated partial message from received chunks")
}

func streamWithMidErrorAgentic(chunks []*schema.AgenticMessage, err error) *schema.StreamReader[*schema.AgenticMessage] {
	sr, sw := schema.Pipe[*schema.AgenticMessage](len(chunks) + 1)
	go func() {
		defer sw.Close()
		for _, c := range chunks {
			sw.Send(c, nil)
		}
		sw.Send(nil, err)
	}()
	return sr
}

func agenticChunk(text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: text}),
		},
	}
}

// agenticUserText reads the text of a user message, which is what these tests
// assert on: agenticTextContent only reads assistant output.
func agenticUserText(msg *schema.AgenticMessage) string {
	for _, b := range msg.ContentBlocks {
		if b.UserInputText != nil {
			return b.UserInputText.Text
		}
	}
	return ""
}

// failoverInputProbe records, per model, the input each call was made with.
type failoverInputProbe struct {
	mu     sync.Mutex
	inputs map[string][][]*schema.AgenticMessage
}

func newFailoverInputProbe() *failoverInputProbe {
	return &failoverInputProbe{inputs: map[string][][]*schema.AgenticMessage{}}
}

func (p *failoverInputProbe) record(name string, input []*schema.AgenticMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs[name] = append(p.inputs[name], append([]*schema.AgenticMessage(nil), input...))
}

func (p *failoverInputProbe) calls(name string) [][]*schema.AgenticMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inputs[name]
}

// A retry that rewrote its input and asked for the rewrite to be persisted has
// concluded the input itself was at fault. Everything after it must therefore
// be sent the rewrite -- including the model failed over to, which is otherwise
// handed the very payload the retry just faulted, and spends the rest of the
// failover budget re-sending it.
func TestFailoverUsesRetryPersistedInput(t *testing.T) {
	ctx := context.Background()

	rejected := errors.New("provider rejected the request payload")
	const repairedText = "hello (attachment omitted)"
	probe := newFailoverInputProbe()

	m1 := &mockAgenticModel{
		generateFn: func(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			probe.record("m1", input)
			return nil, rejected
		},
	}
	m2 := &mockAgenticModel{
		generateFn: func(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			probe.record("m2", input)
			return agenticMsg("failover ok"), nil
		},
	}

	var failoverInput []*schema.AgenticMessage
	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "failover-persisted-input-agent",
		Description: "failover reads the input a retry persisted",
		Model:       m1,
		ModelRetryConfig: &TypedModelRetryConfig[*schema.AgenticMessage]{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *TypedRetryContext[*schema.AgenticMessage]) *TypedRetryDecision[*schema.AgenticMessage] {
				if !errors.Is(retryCtx.Err, rejected) {
					return &TypedRetryDecision[*schema.AgenticMessage]{Retry: false}
				}
				return &TypedRetryDecision[*schema.AgenticMessage]{
					Retry:                        true,
					ModifiedInputMessages:        []*schema.AgenticMessage{schema.UserAgenticMessage(repairedText)},
					PersistModifiedInputMessages: true,
				}
			},
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
		ModelFailoverConfig: &ModelFailoverConfig[*schema.AgenticMessage]{
			MaxRetries:     1,
			ShouldFailover: func(_ context.Context, _ *schema.AgenticMessage, err error) bool { return err != nil },
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext[*schema.AgenticMessage]) (
				model.BaseModel[*schema.AgenticMessage], []*schema.AgenticMessage, error) {
				failoverInput = failoverCtx.InputMessages
				return m2, nil, nil
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
	msg := drainTypedAgenticEvents(t, runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")}))
	require.NotNil(t, msg)
	assert.Equal(t, "failover ok", agenticTextContent(msg))

	// The retry itself behaves as before: original input, then the rewrite.
	m1Calls := probe.calls("m1")
	require.Len(t, m1Calls, 2)
	assert.Equal(t, "hello", agenticUserText(m1Calls[0][0]))
	assert.Equal(t, repairedText, agenticUserText(m1Calls[1][0]))

	// What this test exists for: the failover target sees the rewrite, both in
	// the context it selects from and in the call it is actually made with.
	require.Len(t, failoverInput, 1)
	assert.Equal(t, repairedText, agenticUserText(failoverInput[0]))
	m2Calls := probe.calls("m2")
	require.Len(t, m2Calls, 1)
	require.Len(t, m2Calls[0], 1)
	assert.Equal(t, repairedText, agenticUserText(m2Calls[0][0]))
}

// Same, in streaming mode: both attempt loops read the state, not just the one
// Generate happens to use.
func TestFailoverUsesRetryPersistedInput_Stream(t *testing.T) {
	ctx := context.Background()

	rejected := errors.New("provider rejected the request payload")
	const repairedText = "hello (attachment omitted)"
	probe := newFailoverInputProbe()

	m1 := &mockAgenticModel{
		streamFn: func(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (
			*schema.StreamReader[*schema.AgenticMessage], error) {
			probe.record("m1", input)
			return nil, rejected
		},
	}
	m2 := &mockAgenticModel{
		streamFn: func(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (
			*schema.StreamReader[*schema.AgenticMessage], error) {
			probe.record("m2", input)
			return agenticStreamOK([]*schema.AgenticMessage{agenticMsg("failover stream ok")}), nil
		},
	}

	var failoverInput []*schema.AgenticMessage
	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "failover-persisted-input-stream-agent",
		Description: "failover reads the input a retry persisted, in stream mode",
		Model:       m1,
		ModelRetryConfig: &TypedModelRetryConfig[*schema.AgenticMessage]{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *TypedRetryContext[*schema.AgenticMessage]) *TypedRetryDecision[*schema.AgenticMessage] {
				if !errors.Is(retryCtx.Err, rejected) {
					return &TypedRetryDecision[*schema.AgenticMessage]{Retry: false}
				}
				return &TypedRetryDecision[*schema.AgenticMessage]{
					Retry:                        true,
					ModifiedInputMessages:        []*schema.AgenticMessage{schema.UserAgenticMessage(repairedText)},
					PersistModifiedInputMessages: true,
				}
			},
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
		ModelFailoverConfig: &ModelFailoverConfig[*schema.AgenticMessage]{
			MaxRetries:     1,
			ShouldFailover: func(_ context.Context, _ *schema.AgenticMessage, err error) bool { return err != nil },
			GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext[*schema.AgenticMessage]) (
				model.BaseModel[*schema.AgenticMessage], []*schema.AgenticMessage, error) {
				failoverInput = failoverCtx.InputMessages
				return m2, nil, nil
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: true})
	msg := drainTypedAgenticEvents(t, runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")}))
	require.NotNil(t, msg)
	assert.Contains(t, agenticTextContent(msg), "failover stream ok")

	require.Len(t, failoverInput, 1)
	assert.Equal(t, repairedText, agenticUserText(failoverInput[0]))
	m2Calls := probe.calls("m2")
	require.Len(t, m2Calls, 1)
	require.Len(t, m2Calls[0], 1)
	assert.Equal(t, repairedText, agenticUserText(m2Calls[0][0]))
}

// The other side of the flag: a rewrite the retry kept to itself stays scoped
// to the model that made it, and the failover target is sent the input the call
// was made with. Reading the state must not turn every rewrite into a global
// one -- that decision stays with PersistModifiedInputMessages.
func TestFailoverKeepsCallInputWhenRetryDoesNotPersist(t *testing.T) {
	ctx := context.Background()

	rejected := errors.New("provider rejected the request payload")
	probe := newFailoverInputProbe()

	m1 := &mockAgenticModel{
		generateFn: func(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			probe.record("m1", input)
			return nil, rejected
		},
	}
	m2 := &mockAgenticModel{
		generateFn: func(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			probe.record("m2", input)
			return agenticMsg("failover ok"), nil
		},
	}

	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "failover-unpersisted-input-agent",
		Description: "failover keeps the call input when the retry did not persist",
		Model:       m1,
		ModelRetryConfig: &TypedModelRetryConfig[*schema.AgenticMessage]{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *TypedRetryContext[*schema.AgenticMessage]) *TypedRetryDecision[*schema.AgenticMessage] {
				if !errors.Is(retryCtx.Err, rejected) {
					return &TypedRetryDecision[*schema.AgenticMessage]{Retry: false}
				}
				return &TypedRetryDecision[*schema.AgenticMessage]{
					Retry:                 true,
					ModifiedInputMessages: []*schema.AgenticMessage{schema.UserAgenticMessage("attempt-local rewrite")},
				}
			},
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
		ModelFailoverConfig: &ModelFailoverConfig[*schema.AgenticMessage]{
			MaxRetries:     1,
			ShouldFailover: func(_ context.Context, _ *schema.AgenticMessage, err error) bool { return err != nil },
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.AgenticMessage]) (
				model.BaseModel[*schema.AgenticMessage], []*schema.AgenticMessage, error) {
				return m2, nil, nil
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
	msg := drainTypedAgenticEvents(t, runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")}))
	require.NotNil(t, msg)

	m2Calls := probe.calls("m2")
	require.Len(t, m2Calls, 1)
	require.Len(t, m2Calls[0], 1)
	assert.Equal(t, "hello", agenticUserText(m2Calls[0][0]))
}
