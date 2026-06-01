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
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestModelTimeoutGenerateCallTimeout(t *testing.T) {
	release := make(chan struct{})
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			<-release
			return schema.AssistantMessage("late", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
		},
	}
	defer close(release)

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond})
	started := time.Now()
	_, err := wrapped.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.Error(t, err)
	require.Less(t, time.Since(started), 200*time.Millisecond)

	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, ModelTimeoutPhaseCall, timeoutErr.Phase)
	require.Equal(t, 0, timeoutErr.ChunksReceived)
	require.True(t, errors.Is(err, ErrModelTimeout))
	require.True(t, IsModelTimeoutBeforeOutput(err))
}

func TestModelTimeoutGenerateParentCancellation(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
		},
	}
	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{CallTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, errors.Is(err, ErrModelTimeout))
}

func TestModelTimeoutStreamFirstChunkTimeout(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			reader, _ := schema.Pipe[*schema.Message](1)
			return reader, nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{FirstChunkTimeout: 10 * time.Millisecond})
	stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	defer stream.Close()

	_, err = stream.Recv()
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, ModelTimeoutPhaseFirstChunk, timeoutErr.Phase)
	require.Equal(t, 0, timeoutErr.ChunksReceived)
	require.True(t, defaultIsRetryAble(context.Background(), err))
}

func TestModelTimeoutStreamOpenTimeoutClosesLateReader(t *testing.T) {
	release := make(chan struct{})
	lateReader, lateWriter := schema.Pipe[*schema.Message](0)
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			<-release
			return lateReader, nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond})
	_, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, ModelTimeoutPhaseCall, timeoutErr.Phase)

	close(release)
	closed := make(chan bool, 1)
	go func() {
		closed <- lateWriter.Send(schema.AssistantMessage("late", nil), nil)
	}()
	select {
	case got := <-closed:
		require.True(t, got, "late stream reader should be closed by timeout wrapper")
	case <-time.After(time.Second):
		t.Fatal("late stream reader was not closed")
	}
}

func TestModelTimeoutStreamOpenCooperativeTimeout(t *testing.T) {
	cooperated := make(chan struct{})
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			<-ctx.Done()
			close(cooperated)
			return nil, ctx.Err()
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond})
	_, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, ModelTimeoutPhaseCall, timeoutErr.Phase)
	select {
	case <-cooperated:
	case <-time.After(time.Second):
		t.Fatal("stream-open context was not canceled on timeout")
	}
}

func TestModelTimeoutStreamIdleTimeoutAfterOutput(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			reader, writer := schema.Pipe[*schema.Message](1)
			writer.Send(schema.AssistantMessage("first", nil), nil)
			return reader, nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{StreamIdleTimeout: 10 * time.Millisecond})
	stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	defer stream.Close()

	msg, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "first", msg.Content)

	_, err = stream.Recv()
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, ModelTimeoutPhaseStreamIdle, timeoutErr.Phase)
	require.Equal(t, 1, timeoutErr.ChunksReceived)
	require.False(t, defaultIsRetryAble(context.Background(), err))
	require.False(t, IsModelTimeoutBeforeOutput(err))
}

func TestModelTimeoutStreamTotalTimeoutAfterOutput(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			reader, writer := schema.Pipe[*schema.Message](1)
			writer.Send(schema.AssistantMessage("first", nil), nil)
			return reader, nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{TotalTimeout: 20 * time.Millisecond})
	stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	defer stream.Close()

	msg, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "first", msg.Content)

	_, err = stream.Recv()
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, ModelTimeoutPhaseTotal, timeoutErr.Phase)
	require.Equal(t, 1, timeoutErr.ChunksReceived)
}

func TestModelTimeoutGenerateTotalBeatsCallTimeout(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{
		CallTimeout:  time.Second,
		TotalTimeout: 10 * time.Millisecond,
	})
	_, err := wrapped.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, ModelTimeoutPhaseTotal, timeoutErr.Phase)
}

func TestModelTimeoutStreamDownstreamCloseClosesUpstream(t *testing.T) {
	upstreamReader, upstreamWriter := schema.Pipe[*schema.Message](0)
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return upstreamReader, nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{StreamIdleTimeout: time.Second})
	stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	stream.Close()

	secondSent := make(chan bool, 1)
	go func() {
		secondSent <- upstreamWriter.Send(schema.AssistantMessage("second", nil), nil)
	}()
	select {
	case <-secondSent:
	case <-time.After(time.Second):
		t.Fatal("wrapper did not receive the post-close upstream chunk")
	}

	closed := make(chan bool, 1)
	go func() {
		closed <- upstreamWriter.Send(schema.AssistantMessage("third", nil), nil)
	}()
	select {
	case got := <-closed:
		require.True(t, got, "upstream reader should be closed after downstream close is observed")
	case <-time.After(time.Second):
		t.Fatal("upstream reader was not closed after downstream close")
	}
}

func TestModelTimeoutRetryDefaultRetriesOnlyBeforeOutput(t *testing.T) {
	t.Run("first chunk timeout retries", func(t *testing.T) {
		var calls int32
		m := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return schema.AssistantMessage("unused", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				if atomic.AddInt32(&calls, 1) == 1 {
					reader, _ := schema.Pipe[*schema.Message](1)
					return reader, nil
				}
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
			},
		}
		timeout := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{FirstChunkTimeout: 10 * time.Millisecond})
		retry := newTypedRetryModelWrapper[*schema.Message](timeout, &ModelRetryConfig{MaxRetries: 1, BackoffFunc: instantBackoff})

		stream, err := retry.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		defer stream.Close()
		msg, err := stream.Recv()
		require.NoError(t, err)
		require.Equal(t, "ok", msg.Content)
		_, err = stream.Recv()
		require.ErrorIs(t, err, io.EOF)
		require.Equal(t, int32(2), atomic.LoadInt32(&calls))
	})

	t.Run("idle timeout after output does not retry", func(t *testing.T) {
		var calls int32
		m := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return schema.AssistantMessage("unused", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				atomic.AddInt32(&calls, 1)
				reader, writer := schema.Pipe[*schema.Message](1)
				writer.Send(schema.AssistantMessage("partial", nil), nil)
				return reader, nil
			},
		}
		timeout := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{StreamIdleTimeout: 10 * time.Millisecond})
		retry := newTypedRetryModelWrapper[*schema.Message](timeout, &ModelRetryConfig{MaxRetries: 1, BackoffFunc: instantBackoff})

		_, err := retry.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		timeoutErr, ok := AsModelTimeout(err)
		require.True(t, ok)
		require.Equal(t, 1, timeoutErr.ChunksReceived)
		require.Equal(t, int32(1), atomic.LoadInt32(&calls))
	})
}

func TestModelTimeoutCustomRetryCanReplayAfterPartialOutput(t *testing.T) {
	var calls int32
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				reader, writer := schema.Pipe[*schema.Message](1)
				writer.Send(schema.AssistantMessage("partial", nil), nil)
				return reader, nil
			}
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("replayed", nil)}), nil
		},
	}
	timeout := newTypedTimeoutModelWrapper[*schema.Message](m, &ModelTimeoutConfig{StreamIdleTimeout: 10 * time.Millisecond})
	retry := newTypedRetryModelWrapper[*schema.Message](timeout, &ModelRetryConfig{
		MaxRetries:  1,
		BackoffFunc: instantBackoff,
		ShouldRetry: func(_ context.Context, rc *RetryContext) *RetryDecision {
			timeoutErr, ok := AsModelTimeout(rc.Err)
			return &RetryDecision{Retry: ok && timeoutErr.ChunksReceived > 0}
		},
	})

	stream, err := retry.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	defer stream.Close()
	msg, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "replayed", msg.Content)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestModelTimeoutChatModelAgentRetryIntegration(t *testing.T) {
	var calls int32
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return schema.AssistantMessage("success", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
		},
	}
	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:               "timeout-retry",
		Description:        "timeout retry",
		Model:              m,
		ModelTimeoutConfig: &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond},
		ModelRetryConfig:   &ModelRetryConfig{MaxRetries: 1, BackoffFunc: instantBackoff},
	})
	require.NoError(t, err)

	events := drainAgentEvents(t, agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
	require.Len(t, events, 1)
	require.NoError(t, events[0].Err)
	require.Equal(t, "success", events[0].Output.MessageOutput.Message.Content)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestModelTimeoutFailoverCanInspectTimeout(t *testing.T) {
	slow := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
		},
	}
	fast := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("failover", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("failover", nil)}), nil
		},
	}

	var inspected bool
	proxy := &typedFailoverProxyModel[*schema.Message]{}
	timeout := newTypedTimeoutModelWrapper[*schema.Message](proxy, &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond})
	failover := newFailoverModelWrapper[*schema.Message](timeout, &ModelFailoverConfig[*schema.Message]{
		MaxRetries: 2,
		ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
			timeoutErr, ok := AsModelTimeout(err)
			inspected = ok && timeoutErr.ChunksReceived == 0
			return inspected
		},
		GetFailoverModel: func(_ context.Context, fc *FailoverContext[*schema.Message]) (model.BaseModel[*schema.Message], []*schema.Message, error) {
			if fc.FailoverAttempt == 1 {
				return slow, nil, nil
			}
			return fast, nil, nil
		},
	})

	msg, err := failover.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.True(t, inspected)
	require.Equal(t, "failover", msg.Content)
}

func TestModelTimeoutFailoverCanInspectRetryExhaustedLastErr(t *testing.T) {
	slow := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
		},
	}
	fast := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("after-exhausted", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("after-exhausted", nil)}), nil
		},
	}

	var inspected bool
	proxy := &typedFailoverProxyModel[*schema.Message]{}
	timeout := newTypedTimeoutModelWrapper[*schema.Message](proxy, &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond})
	retry := newTypedRetryModelWrapper[*schema.Message](timeout, &ModelRetryConfig{MaxRetries: 0, BackoffFunc: instantBackoff})
	failover := newFailoverModelWrapper[*schema.Message](retry, &ModelFailoverConfig[*schema.Message]{
		MaxRetries: 2,
		ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
			var exhausted *RetryExhaustedError
			if !errors.As(err, &exhausted) {
				return false
			}
			timeoutErr, ok := AsModelTimeout(exhausted.LastErr)
			inspected = ok && timeoutErr.ChunksReceived == 0
			return inspected
		},
		GetFailoverModel: func(_ context.Context, fc *FailoverContext[*schema.Message]) (model.BaseModel[*schema.Message], []*schema.Message, error) {
			if fc.FailoverAttempt == 1 {
				return slow, nil, nil
			}
			return fast, nil, nil
		},
	})

	msg, err := failover.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.True(t, inspected)
	require.Equal(t, "after-exhausted", msg.Content)
}

func TestModelTimeoutAgenticMessageGenerate(t *testing.T) {
	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	wrapped := newTypedTimeoutModelWrapper[*schema.AgenticMessage](m, &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond})

	_, err := wrapped.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hi")})
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, ModelTimeoutPhaseCall, timeoutErr.Phase)
}

func TestModelTimeoutSpanMeta(t *testing.T) {
	timeoutErr := &ModelTimeoutError{
		Phase:          ModelTimeoutPhaseTotal,
		Timeout:        20 * time.Millisecond,
		Elapsed:        25 * time.Millisecond,
		ChunksReceived: 2,
	}
	event := newModelSpanEndEvent(context.Background(), modelSpanEndEventInput[*schema.Message]{
		spanID:       "span",
		startEventID: "start",
		started:      time.Now().Add(-25 * time.Millisecond),
		ended:        time.Now(),
		err:          timeoutErr,
	})
	require.NotNil(t, event.Span.Model.Timeout)
	require.Equal(t, string(ModelTimeoutPhaseTotal), event.Span.Model.Timeout.Phase)
	require.Equal(t, int64(20), event.Span.Model.Timeout.TimeoutMS)
	require.Equal(t, int64(25), event.Span.Model.Timeout.ElapsedMS)
	require.Equal(t, 2, event.Span.Model.Timeout.ChunksReceived)
}

func TestModelTimeoutTimelineEventContainsTimeoutMeta(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
		},
	}
	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:               "timeout-timeline",
		Description:        "timeout timeline",
		Model:              m,
		ModelTimeoutConfig: &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond},
	})
	require.NoError(t, err)

	var endEvent *SessionEvent[*schema.Message]
	iter := agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, WithTimelineEvents())
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.SessionEvent != nil && event.SessionEvent.Kind == SessionEventSpanModelRequestEnd {
			endEvent = event.SessionEvent
		}
	}
	require.NotNil(t, endEvent)
	require.Equal(t, "error", endEvent.Span.Status)
	require.Contains(t, endEvent.Span.Err, "model timeout")
	require.NotNil(t, endEvent.Span.Model.Timeout)
	require.Equal(t, string(ModelTimeoutPhaseCall), endEvent.Span.Model.Timeout.Phase)

	encoded, err := json.Marshal(newModelSpanEndEvent(context.Background(), modelSpanEndEventInput[*schema.Message]{
		spanID:       "span",
		startEventID: "start",
		started:      time.Now(),
		ended:        time.Now(),
		msg:          schema.AssistantMessage("ok", nil),
		accepted:     true,
	}).Span.Model)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "timeout")
}

func TestAttack_ModelTimeoutRetryExhaustionKeepsTimelineTimeoutMeta(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("unused", nil)}), nil
		},
	}
	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:               "timeout-retry-exhausted-timeline",
		Description:        "timeout retry exhausted timeline",
		Model:              m,
		ModelTimeoutConfig: &ModelTimeoutConfig{CallTimeout: 10 * time.Millisecond},
		ModelRetryConfig:   &ModelRetryConfig{MaxRetries: 0, BackoffFunc: instantBackoff},
	})
	require.NoError(t, err)

	var endEvent *SessionEvent[*schema.Message]
	iter := agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, WithTimelineEvents())
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.SessionEvent != nil && event.SessionEvent.Kind == SessionEventSpanModelRequestEnd {
			endEvent = event.SessionEvent
		}
	}
	require.NotNil(t, endEvent)
	require.Contains(t, endEvent.Span.Err, "model timeout")
	require.NotNil(t, endEvent.Span.Model.Timeout)
	require.Equal(t, string(ModelTimeoutPhaseCall), endEvent.Span.Model.Timeout.Phase)
}
