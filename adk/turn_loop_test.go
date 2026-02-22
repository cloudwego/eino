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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	cancelFunc := func(_ context.Context, opts ...CancelOption) error {
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

	err1 := cancel()
	err2 := cancel()

	assert.NoError(t, err1)
	assert.ErrorIs(t, err2, ErrAgentFinished)
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

func TestTurnLoop_ExternalCancel_WithCheckpoint_ThenResume(t *testing.T) {
	t.Skip("TODO: Implement after verifying TurnLoop checkpoint mechanism")
}

func TestTurnLoop_InternalToolInterrupt_WithCheckpoint_ThenResume(t *testing.T) {
	t.Skip("TODO: Implement after verifying TurnLoop checkpoint mechanism")
}
