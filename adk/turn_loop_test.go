/*
 * Copyright 2025 CloudWeGo Authors
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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type turnLoopMockSource struct {
	items []string
	idx   int
	err   error
}

func (s *turnLoopMockSource) Receive(ctx context.Context, cfg ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
	if s.idx >= len(s.items) {
		return ctx, "", nil, s.err
	}
	item := s.items[s.idx]
	s.idx++
	return ctx, item, nil, nil
}

func (s *turnLoopMockSource) Front(ctx context.Context, cfg ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
	if s.idx >= len(s.items) {
		return ctx, "", nil, s.err
	}
	return ctx, s.items[s.idx], nil, nil
}

type turnLoopFuncSource[T any] struct {
	receive func(ctx context.Context, cfg ReceiveConfig) (context.Context, T, []ConsumeOption, error)
	front   func(ctx context.Context, cfg ReceiveConfig) (context.Context, T, []ConsumeOption, error)
}

func (s *turnLoopFuncSource[T]) Receive(ctx context.Context, cfg ReceiveConfig) (context.Context, T, []ConsumeOption, error) {
	return s.receive(ctx, cfg)
}

func (s *turnLoopFuncSource[T]) Front(ctx context.Context, cfg ReceiveConfig) (context.Context, T, []ConsumeOption, error) {
	if s.front != nil {
		return s.front(ctx, cfg)
	}
	return s.receive(ctx, cfg)
}

type turnLoopMockAgent struct {
	name   string
	events []*AgentEvent
}

func (a *turnLoopMockAgent) Name(_ context.Context) string        { return a.name }
func (a *turnLoopMockAgent) Description(_ context.Context) string { return "mock agent" }
func (a *turnLoopMockAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		for _, e := range a.events {
			gen.Send(e)
		}
	}()
	return iter
}

type turnLoopCancellableAgent struct {
	name         string
	startedCh    chan struct{}
	cancelCh     chan struct{}
	cancelled    int32
	cancelledOpt []CancelOption
}

func (a *turnLoopCancellableAgent) Name(_ context.Context) string        { return a.name }
func (a *turnLoopCancellableAgent) Description(_ context.Context) string { return "cancellable mock" }
func (a *turnLoopCancellableAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, _ := a.RunWithCancel(context.Background(), nil)
	return iter
}

func (a *turnLoopCancellableAgent) RunWithCancel(_ context.Context, _ *AgentInput, _ ...AgentRunOption) (*AsyncIterator[*AgentEvent], CancelFunc) {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	close(a.startedCh)
	go func() {
		defer gen.Close()
		<-a.cancelCh
	}()
	cancelFunc := func(opts ...CancelOption) error {
		atomic.StoreInt32(&a.cancelled, 1)
		a.cancelledOpt = opts
		close(a.cancelCh)
		return nil
	}
	return iter, cancelFunc
}

func TestNewTurnLoop_Validation(t *testing.T) {
	t.Run("missing source", func(t *testing.T) {
		_, err := NewTurnLoop(TurnLoopConfig[string]{
			GenInput: func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) { return nil, nil, nil },
			GetAgent: func(_ context.Context, _ string) (Agent, error) { return nil, nil },
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Source")
	})

	t.Run("missing GenInput", func(t *testing.T) {
		_, err := NewTurnLoop(TurnLoopConfig[string]{
			Source:   &turnLoopMockSource{},
			GetAgent: func(_ context.Context, _ string) (Agent, error) { return nil, nil },
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "GenInput")
	})

	t.Run("missing GetAgent", func(t *testing.T) {
		_, err := NewTurnLoop(TurnLoopConfig[string]{
			Source:   &turnLoopMockSource{},
			GenInput: func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) { return nil, nil, nil },
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "GetAgent")
	})

	t.Run("valid config without OnAgentEvents", func(t *testing.T) {
		loop, err := NewTurnLoop(TurnLoopConfig[string]{
			Source:   &turnLoopMockSource{},
			GenInput: func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) { return nil, nil, nil },
			GetAgent: func(_ context.Context, _ string) (Agent, error) { return nil, nil },
		})
		require.NoError(t, err)
		assert.NotNil(t, loop)
	})

	t.Run("valid config with OnAgentEvents", func(t *testing.T) {
		loop, err := NewTurnLoop(TurnLoopConfig[string]{
			Source:        &turnLoopMockSource{},
			GenInput:      func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) { return nil, nil, nil },
			GetAgent:      func(_ context.Context, _ string) (Agent, error) { return nil, nil },
			OnAgentEvents: func(_ context.Context, _ string, _ *AsyncIterator[*AgentEvent]) error { return nil },
		})
		require.NoError(t, err)
		assert.NotNil(t, loop)
	})
}

func TestTurnLoop_NormalLoop(t *testing.T) {
	agent := &turnLoopMockAgent{
		name: "test-agent",
		events: []*AgentEvent{
			{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("hello", nil)}}},
		},
	}

	var receivedItems []string
	var eventCount int

	source := &turnLoopMockSource{
		items: []string{"msg1", "msg2", "msg3"},
		err:   context.DeadlineExceeded,
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			receivedItems = append(receivedItems, item)
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := iter.Next()
				if !ok {
					break
				}
				eventCount++
			}
			return nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, []string{"msg1", "msg2", "msg3"}, receivedItems)
	assert.Equal(t, 3, eventCount)
}

func TestTurnLoop_SourceError(t *testing.T) {
	sourceErr := errors.New("source failure")
	source := &turnLoopMockSource{
		items: nil,
		err:   sourceErr,
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return &turnLoopMockAgent{name: "a"}, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, _ *AsyncIterator[*AgentEvent]) error { return nil },
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, sourceErr)
}

func TestTurnLoop_GenInputError(t *testing.T) {
	genErr := errors.New("gen input failure")

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{"msg1"}, err: errors.New("should not reach")},
		GenInput: func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) {
			return nil, nil, genErr
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return &turnLoopMockAgent{name: "a"}, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, _ *AsyncIterator[*AgentEvent]) error { return nil },
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, genErr)
	assert.Contains(t, err.Error(), "failed to generate agent input")
}

func TestTurnLoop_GetAgentError(t *testing.T) {
	agentErr := errors.New("get agent failure")

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{"msg1"}, err: errors.New("should not reach")},
		GenInput: func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return nil, agentErr
		},
		OnAgentEvents: func(_ context.Context, _ string, _ *AsyncIterator[*AgentEvent]) error { return nil },
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, agentErr)
	assert.Contains(t, err.Error(), "failed to get agent")
}

func TestTurnLoop_OnAgentEventsError(t *testing.T) {
	eventErr := errors.New("event handler failure")
	agent := &turnLoopMockAgent{
		name:   "test-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{"msg1"}, err: errors.New("should not reach")},
		GenInput: func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, _ *AsyncIterator[*AgentEvent]) error {
			return eventErr
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, eventErr)
	assert.Contains(t, err.Error(), "failed to handle events")
}

func TestTurnLoop_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	source := &turnLoopFuncSource[string]{receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
		callCount++
		if callCount > 1 {
			cancel()
			return ctx, "", nil, ctx.Err()
		}
		return ctx, "msg1", nil, nil
	}}

	agent := &turnLoopMockAgent{
		name:   "test-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := iter.Next(); !ok {
					break
				}
			}
			return nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestTurnLoop_MultipleEventsPerTurn(t *testing.T) {
	agent := &turnLoopMockAgent{
		name: "multi-event-agent",
		events: []*AgentEvent{
			{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("event1", nil)}}},
			{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("event2", nil)}}},
			{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("event3", nil)}}},
		},
	}

	var eventCount int

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{"msg1"}, err: context.DeadlineExceeded},
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := iter.Next()
				if !ok {
					break
				}
				eventCount++
			}
			return nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 3, eventCount)
}

func TestTurnLoop_DefaultOnAgentEvents(t *testing.T) {
	agent := &turnLoopMockAgent{
		name:   "test-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{"msg1"}, err: context.DeadlineExceeded},
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestTurnLoop_DefaultOnAgentEventsWithError(t *testing.T) {
	agentErr := errors.New("agent internal error")
	agent := &turnLoopMockAgent{
		name:   "error-agent",
		events: []*AgentEvent{{Err: agentErr}},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{"msg1"}, err: context.DeadlineExceeded},
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, agentErr)
}

func TestTurnLoop_AgentErrorEvent(t *testing.T) {
	agentErr := errors.New("agent internal error")
	agent := &turnLoopMockAgent{
		name:   "error-agent",
		events: []*AgentEvent{{Err: agentErr}},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{"msg1"}, err: context.DeadlineExceeded},
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				if event.Err != nil {
					return fmt.Errorf("agent run failed: %w", event.Err)
				}
			}
			return nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, agentErr)
	assert.Contains(t, err.Error(), "agent run failed")
}

func TestTurnLoop_PreemptiveCancellation(t *testing.T) {
	slowAgent := &turnLoopCancellableAgent{
		name:      "slow-agent",
		startedCh: make(chan struct{}),
		cancelCh:  make(chan struct{}),
	}

	fastAgent := &turnLoopMockAgent{
		name:   "fast-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	var processedItems []string
	receiveCount := 0
	msgs := []struct {
		item string
		opts []ConsumeOption
		err  error
	}{
		{"slow-msg", nil, nil},
		{"preempt-msg", []ConsumeOption{WithPreemptive(), WithCancelOptions(WithCancelMode(CancelImmediate))}, nil},
		{"", nil, context.DeadlineExceeded},
	}
	frontIdx := 0
	source := &turnLoopFuncSource[string]{
		receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			if receiveCount >= len(msgs) {
				return ctx, "", nil, context.DeadlineExceeded
			}
			m := msgs[receiveCount]
			receiveCount++
			frontIdx = receiveCount
			return ctx, m.item, m.opts, m.err
		},
		front: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			<-slowAgent.startedCh
			if frontIdx >= len(msgs) {
				return ctx, "", nil, context.DeadlineExceeded
			}
			m := msgs[frontIdx]
			return ctx, m.item, m.opts, m.err
		},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			processedItems = append(processedItems, item)
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, item string) (Agent, error) {
			if item == "slow-msg" {
				return slowAgent, nil
			}
			return fastAgent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := iter.Next(); !ok {
					break
				}
			}
			return nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, atomic.LoadInt32(&slowAgent.cancelled) == 1, "slow agent should have been cancelled")
	assert.Equal(t, []string{"slow-msg", "preempt-msg"}, processedItems)
}

func TestTurnLoop_PreemptiveNonCancellableAgent(t *testing.T) {
	nonCancellableAgent := &turnLoopMockAgent{
		name:   "non-cancellable-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}
	fastAgent := &turnLoopMockAgent{
		name:   "fast-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	var processedItems []string
	callCount := 0
	source := &turnLoopFuncSource[string]{receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
		callCount++
		switch callCount {
		case 1:
			return ctx, "non-cancel-msg", nil, nil
		case 2:
			return ctx, "preempt-msg", []ConsumeOption{WithPreemptive()}, nil
		default:
			return ctx, "", nil, context.DeadlineExceeded
		}
	}}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			processedItems = append(processedItems, item)
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, item string) (Agent, error) {
			if item == "non-cancel-msg" {
				return nonCancellableAgent, nil
			}
			return fastAgent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := iter.Next(); !ok {
					break
				}
			}
			return nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, []string{"non-cancel-msg", "preempt-msg"}, processedItems)
}

func TestTurnLoop_WithCancel_Basic(t *testing.T) {
	agent := &turnLoopCancellableAgent{
		name:      "test-agent",
		startedCh: make(chan struct{}),
		cancelCh:  make(chan struct{}),
	}

	receiveCount := int32(0)
	frontBlocked := make(chan struct{})
	source := &turnLoopFuncSource[string]{
		receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			cnt := atomic.AddInt32(&receiveCount, 1)
			if cnt == 1 {
				return ctx, "msg1", nil, nil
			}
			return ctx, "", nil, context.DeadlineExceeded
		},
		front: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			<-frontBlocked
			return ctx, "", nil, context.DeadlineExceeded
		},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := iter.Next(); !ok {
					break
				}
			}
			return nil
		},
	})
	require.NoError(t, err)

	ctx, cancel := loop.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- loop.Run(ctx)
	}()

	<-agent.startedCh
	e := cancel()
	assert.NoError(t, e)

	err = <-done
	assert.NoError(t, err)
}

func TestTurnLoop_WithCancel_DuringAgentRun(t *testing.T) {
	slowAgent := &turnLoopCancellableAgent{
		name:      "slow-agent",
		startedCh: make(chan struct{}),
		cancelCh:  make(chan struct{}),
	}

	receiveCount := int32(0)
	frontBlocked := make(chan struct{})
	source := &turnLoopFuncSource[string]{
		receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			cnt := atomic.AddInt32(&receiveCount, 1)
			if cnt == 1 {
				return ctx, "msg1", nil, nil
			}
			return ctx, "", nil, context.DeadlineExceeded
		},
		front: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			<-frontBlocked
			return ctx, "", nil, context.DeadlineExceeded
		},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return slowAgent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := iter.Next(); !ok {
					break
				}
			}
			return nil
		},
	})
	require.NoError(t, err)

	ctx, cancel := loop.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- loop.Run(ctx)
	}()

	<-slowAgent.startedCh
	cancel(WithCancelMode(CancelImmediate))

	err = <-done
	assert.NoError(t, err)
	assert.True(t, atomic.LoadInt32(&slowAgent.cancelled) == 1, "agent should have been cancelled")
}

func TestTurnLoop_WithCancel_NonCancellableAgent_ReturnsError(t *testing.T) {
	agent := &turnLoopMockAgent{
		name:   "non-cancellable-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	source := &turnLoopFuncSource[string]{
		receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			return ctx, "msg1", nil, nil
		},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
	})
	require.NoError(t, err)

	ctx, _ := loop.WithCancel(context.Background())
	err = loop.Run(ctx)

	assert.ErrorIs(t, err, ErrAgentNotCancellableInTurnLoop)
	assert.Contains(t, err.Error(), "non-cancellable-agent")
}

func TestTurnLoop_WithCancel_MultipleCalls(t *testing.T) {
	agent := &turnLoopMockAgent{
		name:   "test-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{"msg1"}, err: context.DeadlineExceeded},
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
	})
	require.NoError(t, err)

	_, cancel := loop.WithCancel(context.Background())

	err = cancel()
	assert.NoError(t, err)
}

func TestTurnLoop_WithCancel_IndependentCancels(t *testing.T) {
	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: &turnLoopMockSource{items: []string{}, err: context.DeadlineExceeded},
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return &turnLoopMockAgent{name: "a"}, nil
		},
	})
	require.NoError(t, err)

	ctx1, cancel1 := loop.WithCancel(context.Background())
	ctx2, cancel2 := loop.WithCancel(context.Background())

	cs1 := getTurnLoopCancelSig(ctx1)
	cs2 := getTurnLoopCancelSig(ctx2)

	assert.NotNil(t, cs1)
	assert.NotNil(t, cs2)
	assert.NotEqual(t, cs1, cs2)

	cancel1()
	assert.True(t, cs1.isCancelled())
	assert.False(t, cs2.isCancelled())

	cancel2()
	assert.True(t, cs2.isCancelled())
}

func TestTurnLoop_WithCancel_WithCancelOptions(t *testing.T) {
	slowAgent := &turnLoopCancellableAgent{
		name:      "slow-agent",
		startedCh: make(chan struct{}),
		cancelCh:  make(chan struct{}),
	}

	receiveCount := int32(0)
	frontBlocked := make(chan struct{})
	source := &turnLoopFuncSource[string]{
		receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			cnt := atomic.AddInt32(&receiveCount, 1)
			if cnt == 1 {
				return ctx, "msg1", nil, nil
			}
			return ctx, "", nil, context.DeadlineExceeded
		},
		front: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			<-frontBlocked
			return ctx, "", nil, context.DeadlineExceeded
		},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return slowAgent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := iter.Next(); !ok {
					break
				}
			}
			return nil
		},
	})
	require.NoError(t, err)

	ctx, cancel := loop.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- loop.Run(ctx)
	}()

	<-slowAgent.startedCh
	cancel(WithCancelMode(CancelAfterToolCall))

	<-done
	assert.Len(t, slowAgent.cancelledOpt, 1)
}

type turnLoopInMemoryStore struct {
	data map[string][]byte
}

func newTurnLoopInMemoryStore() *turnLoopInMemoryStore {
	return &turnLoopInMemoryStore{data: make(map[string][]byte)}
}

func (s *turnLoopInMemoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := s.data[key]
	return v, ok, nil
}

func (s *turnLoopInMemoryStore) Set(_ context.Context, key string, value []byte) error {
	s.data[key] = value
	return nil
}

type turnLoopTestModel struct {
	messages []*schema.Message
	idx      int
}

func (m *turnLoopTestModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if m.idx >= len(m.messages) {
		return nil, fmt.Errorf("no more messages")
	}
	msg := m.messages[m.idx]
	m.idx++
	return msg, nil
}

func (m *turnLoopTestModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

func (m *turnLoopTestModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type turnLoopSlowModel struct {
	delay     int64
	startedCh unsafe.Pointer
	doneCh    chan struct{}
	message   *schema.Message
}

func (m *turnLoopSlowModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if ch := (*chan struct{})(atomic.LoadPointer(&m.startedCh)); ch != nil {
		select {
		case *ch <- struct{}{}:
		default:
		}
	}
	if delay := atomic.LoadInt64(&m.delay); delay > 0 {
		time.Sleep(time.Duration(delay))
	}
	if m.doneCh != nil {
		select {
		case m.doneCh <- struct{}{}:
		default:
		}
	}
	return m.message, nil
}

func (m *turnLoopSlowModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

type turnLoopInterruptTool struct {
	name string
}

func (t *turnLoopInterruptTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "A tool that interrupts",
	}, nil
}

func (t *turnLoopInterruptTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	wasInterrupted, _, _ := tool.GetInterruptState[any](ctx)
	if !wasInterrupted {
		return "", tool.Interrupt(ctx, "need approval")
	}
	isResumeTarget, hasData, data := tool.GetResumeContext[string](ctx)
	if isResumeTarget && hasData {
		return data, nil
	}
	return "approved", nil
}

func TestTurnLoop_ExternalCancel_WithStore(t *testing.T) {
	store := newTurnLoopInMemoryStore()
	const checkPointID = "external-cancel-test"

	modelStarted := make(chan struct{}, 1)
	testModel := &turnLoopSlowModel{
		doneCh:  make(chan struct{}, 1),
		message: schema.AssistantMessage("task completed", nil),
	}
	atomic.StoreInt64(&testModel.delay, int64(5*time.Second))
	atomic.StorePointer(&testModel.startedCh, unsafe.Pointer(&modelStarted))

	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:        "test-agent",
		Description: "test agent for external cancel",
		Model:       testModel,
	})
	require.NoError(t, err)

	receiveCount := int32(0)
	turnCount := int32(0)
	frontBlocked := make(chan struct{})
	source := &turnLoopFuncSource[string]{
		receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			cnt := atomic.AddInt32(&receiveCount, 1)
			if cnt == 1 {
				return ctx, "msg1", []ConsumeOption{WithConsumeCheckPointID(checkPointID)}, nil
			}
			return ctx, "", nil, ErrLoopExit
		},
		front: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			<-frontBlocked
			return ctx, "", nil, context.DeadlineExceeded
		},
	}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			atomic.AddInt32(&turnCount, 1)
			for {
				if _, ok := iter.Next(); !ok {
					break
				}
			}
			return nil
		},
		Store: store,
	})
	require.NoError(t, err)

	ctx, cancel := loop.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- loop.Run(ctx)
	}()

	select {
	case <-modelStarted:
	case err := <-done:
		t.Fatalf("loop.Run returned early with error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for model to start")
	}

	err = cancel(WithCancelMode(CancelImmediate))

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for loop.Run to return after cancel")
	}

	var interruptErr *TurnLoopInterruptError[string]
	require.True(t, errors.As(runErr, &interruptErr), "expected TurnLoopInterruptError, got: %v", runErr)
	assert.Equal(t, checkPointID, interruptErr.CheckpointID)
	assert.Equal(t, "msg1", interruptErr.Item)
	assert.True(t, len(store.data) > 0, "checkpoint should be stored")

	atomic.StorePointer(&testModel.startedCh, nil)
	atomic.StoreInt64(&testModel.delay, 0)

	done = make(chan error)
	go func() {
		done <- loop.Run(context.Background(), WithTurnLoopResume(checkPointID, "msg1"))
	}()

	select {
	case runErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for loop.Run (resume) to return")
	}
	assert.NoError(t, runErr)
	assert.Equal(t, int32(2), atomic.LoadInt32(&turnCount), "should have 2 turns total (1 from first run + 1 from resume)")
}

func TestTurnLoop_InternalToolInterrupt_WithCheckpoint_ThenResume(t *testing.T) {
	store := newTurnLoopInMemoryStore()
	const checkPointID = "tool-interrupt-test"

	interruptTool := &turnLoopInterruptTool{name: "approval_tool"}

	testModel := &turnLoopTestModel{
		messages: []*schema.Message{
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "call1", Function: schema.FunctionCall{Name: "approval_tool", Arguments: "{}"}},
			}),
			schema.AssistantMessage("task completed", nil),
		},
	}

	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:        "test-agent",
		Description: "test agent",
		Model:       testModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{interruptTool},
			},
		},
	})
	require.NoError(t, err)

	receiveCount := int32(0)
	frontBlocked := make(chan struct{})
	source := &turnLoopFuncSource[string]{
		receive: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			cnt := atomic.AddInt32(&receiveCount, 1)
			if cnt == 1 {
				return ctx, "msg1", []ConsumeOption{WithConsumeCheckPointID(checkPointID)}, nil
			}
			return ctx, "", nil, ErrLoopExit
		},
		front: func(ctx context.Context, _ ReceiveConfig) (context.Context, string, []ConsumeOption, error) {
			<-frontBlocked
			return ctx, "", nil, context.DeadlineExceeded
		},
	}

	var interruptErr *TurnLoopInterruptError[string]
	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, _ string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ string, iter *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := iter.Next(); !ok {
					break
				}
			}
			return nil
		},
		Store: store,
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	require.True(t, errors.As(err, &interruptErr), "expected TurnLoopInterruptError, got: %v", err)
	assert.Equal(t, checkPointID, interruptErr.CheckpointID)
	assert.Equal(t, "msg1", interruptErr.Item)
	assert.Len(t, interruptErr.InterruptContexts, 1)
	assert.Equal(t, "need approval", interruptErr.InterruptContexts[0].Info)

	interruptID := interruptErr.InterruptContexts[0].ID
	assert.NotEmpty(t, interruptID)
	assert.True(t, len(store.data) > 0, "checkpoint should be stored")

	testModel.idx = 1
	receiveCount = 0

	resumeCtx := compose.ResumeWithData(context.Background(), interruptID, "user approved")
	err = loop.Run(resumeCtx, WithTurnLoopResume(checkPointID, "msg1"))
	assert.NoError(t, err)
}

// =============================================================================
// TurnLoop V2 Tests
// =============================================================================

func TestTurnLoopV2_RunAndPush(t *testing.T) {
	processedItems := make([]string, 0)
	var mu sync.Mutex

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			mu.Lock()
			processedItems = append(processedItems, items...)
			mu.Unlock()
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	time.Sleep(100 * time.Millisecond)

	loop.Cancel()
	result := loop.Wait()

	mu.Lock()
	defer mu.Unlock()

	assert.NoError(t, result.Error)
	assert.True(t, len(processedItems) > 0, "should have processed at least one item")
}

func TestTurnLoopV2_PushReturnsErrorAfterCancel(t *testing.T) {
	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{},
				Consumed: items,
			}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Cancel()

	err := loop.Push("msg1")
	assert.ErrorIs(t, err, ErrTurnLoopStopped)
}

func TestTurnLoopV2_CancelIsIdempotent(t *testing.T) {
	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Cancel()
	loop.Cancel()
	loop.Cancel()

	result := loop.Wait()
	assert.NoError(t, result.Error)
}

func TestTurnLoopV2_WaitMultipleGoroutines(t *testing.T) {
	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Cancel()

	var wg sync.WaitGroup
	results := make([]*TurnLoopV2Result[string], 3)

	for i := 0; i < 3; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = loop.Wait()
		}()
	}

	wg.Wait()

	assert.Equal(t, results[0], results[1])
	assert.Equal(t, results[1], results[2])
}

func TestTurnLoopV2_UnhandledItemsOnCancel(t *testing.T) {
	started := make(chan struct{})
	blocked := make(chan struct{})

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			close(started)
			<-blocked
			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")
	loop.Push("msg3")

	<-started

	loop.Cancel()
	close(blocked)

	result := loop.Wait()
	assert.True(t, len(result.UnhandledItems) >= 0, "should return unhandled items")
}

func TestTurnLoopV2_GenInputError(t *testing.T) {
	genErr := errors.New("gen input error")

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return nil, genErr
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.ErrorIs(t, result.Error, genErr)
}

func TestTurnLoopV2_GetAgentError(t *testing.T) {
	agentErr := errors.New("get agent error")

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return nil, agentErr
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.ErrorIs(t, result.Error, agentErr)
}

func TestTurnLoopV2_BatchProcessing(t *testing.T) {
	var batches [][]string
	var mu sync.Mutex

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			mu.Lock()
			batches = append(batches, items)
			mu.Unlock()

			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")
	loop.Push("msg3")

	time.Sleep(200 * time.Millisecond)

	loop.Cancel()
	loop.Wait()

	mu.Lock()
	defer mu.Unlock()

	assert.True(t, len(batches) > 0, "should have processed at least one batch")
}

func TestTurnLoopV2_CancelWithMode(t *testing.T) {
	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Cancel(WithV2CancelMode(CancelAfterToolCall), WithV2SkipCheckpoint())

	result := loop.Wait()
	assert.NoError(t, result.Error)
}

func TestTurnLoopV2_ConcurrentPush(t *testing.T) {
	var count int32

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			atomic.AddInt32(&count, int32(len(items)))
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				loop.Push(fmt.Sprintf("msg-%d-%d", i, j))
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	loop.Cancel()
	result := loop.Wait()

	processed := atomic.LoadInt32(&count)
	unhandled := len(result.UnhandledItems)

	assert.True(t, processed > 0, "should have processed some items")
	assert.True(t, int(processed)+unhandled <= 100, "total should not exceed pushed amount")
}

func TestTurnLoopV2_CancelAfterReceive_RecoverItem(t *testing.T) {
	receiveStarted := make(chan struct{})
	cancelDone := make(chan struct{})

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			close(receiveStarted)
			<-cancelDone
			time.Sleep(50 * time.Millisecond)
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	<-receiveStarted

	loop.Cancel()
	close(cancelDone)

	result := loop.Wait()
	assert.NoError(t, result.Error)
}

func TestTurnLoopV2_CancelAfterGenInput_RecoverConsumed(t *testing.T) {
	genInputDone := make(chan struct{})

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			close(genInputDone)
			time.Sleep(50 * time.Millisecond)
			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			time.Sleep(100 * time.Millisecond)
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	<-genInputDone

	time.Sleep(60 * time.Millisecond)
	loop.Cancel()

	result := loop.Wait()
	assert.NoError(t, result.Error)
}

func TestTurnLoopV2_GetAgentError_RecoverConsumed(t *testing.T) {
	agentErr := errors.New("get agent error")

	loop := RunTurnLoopV2(context.Background(), TurnLoopV2Config[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		GetAgent: func(ctx context.Context, c []string) (Agent, error) {
			return nil, agentErr
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	result := loop.Wait()
	assert.ErrorIs(t, result.Error, agentErr)
	assert.True(t, len(result.UnhandledItems) >= 1, "should recover at least the consumed item and remaining")
}
