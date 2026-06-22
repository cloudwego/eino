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

package modeltimeout

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	. "github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fakeChatModel struct {
	callbacksEnabled bool
	generate         func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
	stream           func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

func (m *fakeChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.generate(ctx, input, opts...)
}

func (m *fakeChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.stream(ctx, input, opts...)
}

func (m *fakeChatModel) BindTools([]*schema.ToolInfo) error {
	return nil
}

func (m *fakeChatModel) IsCallbacksEnabled() bool {
	return m.callbacksEnabled
}

type mockAgenticModel struct {
	generateFn func(context.Context, []*schema.AgenticMessage, ...model.Option) (*schema.AgenticMessage, error)
	streamFn   func(context.Context, []*schema.AgenticMessage, ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error)
}

func (m *mockAgenticModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	return m.generateFn(ctx, input, opts...)
}

func (m *mockAgenticModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	if m.streamFn != nil {
		return m.streamFn(ctx, input, opts...)
	}
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{msg}), nil
}

func instantBackoff(context.Context, int) time.Duration {
	return 0
}

func drainTimeoutAgentEvents(iter *AsyncIterator[*AgentEvent]) []*AgentEvent {
	var events []*AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			return events
		}
		events = append(events, event)
	}
}

func contextAwareMessageStream(ctx context.Context, chunks ...*schema.Message) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](len(chunks) + 1)
	for _, chunk := range chunks {
		writer.Send(chunk, nil)
	}
	go func() {
		<-ctx.Done()
		writer.Send(nil, ctx.Err())
	}()
	return reader
}

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

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{CallTimeout: 10 * time.Millisecond})
	started := time.Now()
	_, err := wrapped.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.Error(t, err)
	require.Less(t, time.Since(started), 200*time.Millisecond)

	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, PhaseCall, timeoutErr.Phase)
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
	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{CallTimeout: time.Second})
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
		stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return contextAwareMessageStream(ctx), nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{FirstChunkTimeout: 10 * time.Millisecond})
	stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	defer stream.Close()

	_, err = stream.Recv()
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, PhaseFirstChunk, timeoutErr.Phase)
	require.Equal(t, 0, timeoutErr.ChunksReceived)
	require.True(t, IsModelTimeoutBeforeOutput(err))
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

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{CallTimeout: 10 * time.Millisecond})
	_, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, PhaseCall, timeoutErr.Phase)
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
		stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return contextAwareMessageStream(ctx, schema.AssistantMessage("first", nil)), nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{StreamIdleTimeout: 10 * time.Millisecond})
	stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	defer stream.Close()

	msg, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "first", msg.Content)

	_, err = stream.Recv()
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, PhaseStreamIdle, timeoutErr.Phase)
	require.Equal(t, 1, timeoutErr.ChunksReceived)
	require.False(t, IsModelTimeoutBeforeOutput(err))
}

func TestModelTimeoutStreamTotalTimeoutAfterOutput(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("unused", nil), nil
		},
		stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return contextAwareMessageStream(ctx, schema.AssistantMessage("first", nil)), nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{TotalTimeout: 20 * time.Millisecond})
	stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	defer stream.Close()

	msg, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "first", msg.Content)

	_, err = stream.Recv()
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, PhaseTotal, timeoutErr.Phase)
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

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{
		CallTimeout:  time.Second,
		TotalTimeout: 10 * time.Millisecond,
	})
	_, err := wrapped.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, PhaseTotal, timeoutErr.Phase)
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

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{StreamIdleTimeout: time.Second})
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
		Name:             "timeout-retry",
		Description:      "timeout retry",
		Model:            m,
		Handlers:         []ChatModelAgentMiddleware{New(&Config{CallTimeout: 10 * time.Millisecond})},
		ModelRetryConfig: &ModelRetryConfig{MaxRetries: 1, BackoffFunc: instantBackoff},
	})
	require.NoError(t, err)

	events := drainTimeoutAgentEvents(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
	require.Len(t, events, 1)
	require.NoError(t, events[0].Err)
	require.Equal(t, "success", events[0].Output.MessageOutput.Message.Content)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestModelTimeoutAgenticMessageGenerate(t *testing.T) {
	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	wrapped := newTypedTimeoutModelWrapper[*schema.AgenticMessage](m, &Config{CallTimeout: 10 * time.Millisecond})

	_, err := wrapped.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hi")})
	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, PhaseCall, timeoutErr.Phase)
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
		Name:        "timeout-timeline",
		Description: "timeout timeline",
		Model:       m,
		Handlers:    []ChatModelAgentMiddleware{New(&Config{CallTimeout: 10 * time.Millisecond})},
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
	require.Equal(t, string(PhaseCall), endEvent.Span.Model.Timeout.Phase)

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
		Name:             "timeout-retry-exhausted-timeline",
		Description:      "timeout retry exhausted timeline",
		Model:            m,
		Handlers:         []ChatModelAgentMiddleware{New(&Config{CallTimeout: 10 * time.Millisecond})},
		ModelRetryConfig: &ModelRetryConfig{MaxRetries: 0, BackoffFunc: instantBackoff},
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
	require.Equal(t, string(PhaseCall), endEvent.Span.Model.Timeout.Phase)
}

func TestModelTimeoutHelperContracts(t *testing.T) {
	var nilTimeout *Error
	require.Equal(t, ErrModelTimeout.Error(), nilTimeout.Error())

	timeoutErr := &Error{
		Phase:          PhaseStreamIdle,
		Timeout:        time.Second,
		Elapsed:        time.Millisecond,
		ChunksReceived: 2,
	}
	require.ErrorIs(t, timeoutErr, ErrModelTimeout)
	require.Contains(t, timeoutErr.Error(), "chunks_received=2")

	extracted, ok := AsModelTimeout(timeoutErr)
	require.True(t, ok)
	require.Same(t, timeoutErr, extracted)
	require.False(t, IsModelTimeoutBeforeOutput(timeoutErr))
	require.True(t, IsModelTimeoutBeforeOutput(&Error{ChunksReceived: 0}))

	_, ok = AsModelTimeout(io.EOF)
	require.False(t, ok)
	require.False(t, isConfigActive(nil))
	require.False(t, isConfigActive(&Config{}))
	require.True(t, isConfigActive(&Config{StreamIdleTimeout: time.Second}))

	timeout, phase, ok := minPositiveTimeout(0, 0)
	require.False(t, ok)
	require.Zero(t, timeout)
	require.Empty(t, phase)
}

func TestModelTimeoutInactiveConfigDelegates(t *testing.T) {
	m := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("generated", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("streamed", nil)}), nil
		},
	}

	wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{})
	msg, err := wrapped.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.Equal(t, "generated", msg.Content)

	stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	defer stream.Close()

	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "streamed", chunk.Content)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestModelTimeoutStreamOpenErrorPaths(t *testing.T) {
	t.Run("nil reader without error", func(t *testing.T) {
		m := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return schema.AssistantMessage("unused", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return nil, nil
			},
		}
		wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{FirstChunkTimeout: time.Second})

		stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, stream)
		require.Error(t, err)
		require.Contains(t, err.Error(), "nil reader")
	})

	t.Run("provider error passes through", func(t *testing.T) {
		providerErr := errors.New("provider stream failed")
		m := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return schema.AssistantMessage("unused", nil), nil
			},
			stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return nil, providerErr
			},
		}
		wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{CallTimeout: time.Second})

		stream, err := wrapped.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, stream)
		require.ErrorIs(t, err, providerErr)
	})

	t.Run("parent cancellation wins open", func(t *testing.T) {
		m := &fakeChatModel{
			callbacksEnabled: true,
			generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				return schema.AssistantMessage("unused", nil), nil
			},
			stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		wrapped := newTypedTimeoutModelWrapper[*schema.Message](m, &Config{CallTimeout: time.Second})

		stream, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Nil(t, stream)
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, errors.Is(err, ErrModelTimeout))
	})
}
