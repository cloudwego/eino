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

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestBuildModelWrappers_FailoverProxyInner(t *testing.T) {
	base := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("ok", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
		},
	}

	failoverCfg := &ModelFailoverConfig{
		MaxRetries:     0,
		ShouldFailover: func(context.Context, *schema.Message, error) bool { return false },
		GetFailoverModel: func(_ context.Context, _ *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			return base, nil, nil
		},
	}

	wrapped := buildModelWrappers(base, &modelWrapperConfig{
		failoverConfig: failoverCfg,
	})

	smw, ok := wrapped.(*stateModelWrapper)
	require.True(t, ok)
	_, ok = smw.inner.(*failoverProxyModel)
	require.True(t, ok)
	require.Same(t, base, smw.original)
	require.Same(t, failoverCfg, smw.modelFailoverConfig)
}

func TestStateModelWrapper_Generate_WithFailover(t *testing.T) {
	wantErr := errors.New("first failed")
	var shouldCalls int32
	var m1Calls int32
	var m2Calls int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return schema.AssistantMessage("partial", nil), wantErr
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

	failoverCfg := &ModelFailoverConfig{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, out *schema.Message, err error) bool {
			atomic.AddInt32(&shouldCalls, 1)
			require.ErrorIs(t, err, wantErr)
			require.NotNil(t, out)
			require.Equal(t, "partial", out.Content)
			return true
		},
		GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers(m1, &modelWrapperConfig{
		failoverConfig: failoverCfg,
	})

	ctx := withChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	got, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "ok", got.Content)
	require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
}

func TestStateModelWrapper_Stream_WithFailover(t *testing.T) {
	streamErr := errors.New("mid error")
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

	failoverCfg := &ModelFailoverConfig{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, out *schema.Message, err error) bool {
			atomic.AddInt32(&shouldCalls, 1)
			require.ErrorIs(t, err, streamErr)
			require.NotNil(t, out)
			require.Equal(t, "p1p2", out.Content)
			return true
		},
		GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
			require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers(m1, &modelWrapperConfig{
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
	require.Equal(t, "final", msgs[0].Content)
	require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
}
