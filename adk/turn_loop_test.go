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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Test mocks
// ---------------------------------------------------------------------------

// turnLoopMockSource returns items from a slice (all NonPreemptive), then an error.
type turnLoopMockSource struct {
	items []string
	idx   int
	err   error
}

func (s *turnLoopMockSource) Receive(_ context.Context, _ time.Duration) (string, ConsumeOption, error) {
	if s.idx >= len(s.items) {
		return "", NonPreemptiveConsumeOption, s.err
	}
	item := s.items[s.idx]
	s.idx++
	return item, NonPreemptiveConsumeOption, nil
}

// turnLoopFuncSource delegates Receive to a user-supplied function.
type turnLoopFuncSource[T any] struct {
	fn func(ctx context.Context, timeout time.Duration) (T, ConsumeOption, error)
}

func (s *turnLoopFuncSource[T]) Receive(ctx context.Context, timeout time.Duration) (T, ConsumeOption, error) {
	return s.fn(ctx, timeout)
}

// turnLoopMockAgent emits a fixed list of events per Run call.
type turnLoopMockAgent struct {
	name   string
	events []*AgentEvent
}

func (a *turnLoopMockAgent) Name(_ context.Context) string        { return a.name }
func (a *turnLoopMockAgent) Description(_ context.Context) string  { return "mock agent" }
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

// turnLoopCancellableAgent blocks until Cancel is called, then closes its iterator.
// It implements both Agent and Cancellable.
type turnLoopCancellableAgent struct {
	name           string
	startedCh      chan struct{} // closed when Run is entered
	cancelCh       chan struct{} // closed by Cancel
	cancelled      atomic.Bool
	cancelledOpt   CancelOption // records the CancelOption passed to Cancel
}

func (a *turnLoopCancellableAgent) Name(_ context.Context) string        { return a.name }
func (a *turnLoopCancellableAgent) Description(_ context.Context) string  { return "cancellable mock" }
func (a *turnLoopCancellableAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	close(a.startedCh)
	go func() {
		defer gen.Close()
		<-a.cancelCh
	}()
	return iter
}

func (a *turnLoopCancellableAgent) Cancel(_ context.Context, opt CancelOption) error {
	a.cancelled.Store(true)
	a.cancelledOpt = opt
	close(a.cancelCh)
	return nil
}

// turnLoopBlockingAgent blocks until blockCh is closed, then emits its events.
// It does NOT implement Cancellable.
type turnLoopBlockingAgent struct {
	name      string
	startedCh chan struct{}
	blockCh   chan struct{}
	events    []*AgentEvent
}

func (a *turnLoopBlockingAgent) Name(_ context.Context) string        { return a.name }
func (a *turnLoopBlockingAgent) Description(_ context.Context) string  { return "blocking mock" }
func (a *turnLoopBlockingAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	close(a.startedCh)
	go func() {
		defer gen.Close()
		<-a.blockCh
		for _, e := range a.events {
			gen.Send(e)
		}
	}()
	return iter
}

// ---------------------------------------------------------------------------
// Tests — validation
// ---------------------------------------------------------------------------

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

	t.Run("valid config", func(t *testing.T) {
		loop, err := NewTurnLoop(TurnLoopConfig[string]{
			Source:   &turnLoopMockSource{},
			GenInput: func(_ context.Context, _ string) (*AgentInput, []AgentRunOption, error) { return nil, nil, nil },
			GetAgent: func(_ context.Context, _ string) (Agent, error) { return nil, nil },
		})
		require.NoError(t, err)
		assert.NotNil(t, loop)
	})
}

// ---------------------------------------------------------------------------
// Tests — non-preemptive (queued) behavior
// ---------------------------------------------------------------------------

func TestTurnLoop_NormalLoop(t *testing.T) {
	agent := &turnLoopMockAgent{
		name: "test-agent",
		events: []*AgentEvent{
			{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("hello", nil)}}},
		},
	}

	var receivedEvents []*AgentEvent
	var receivedItems []string

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
		OnAgentEvent: func(_ context.Context, _ string, event *AgentEvent) error {
			receivedEvents = append(receivedEvents, event)
			return nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, []string{"msg1", "msg2", "msg3"}, receivedItems)
	assert.Len(t, receivedEvents, 3)
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
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, agentErr)
	assert.Contains(t, err.Error(), "failed to get agent")
}

func TestTurnLoop_OnAgentEventError(t *testing.T) {
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
		OnAgentEvent: func(_ context.Context, _ string, _ *AgentEvent) error {
			return eventErr
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, eventErr)
	assert.Contains(t, err.Error(), "OnAgentEvent failed")
}

func TestTurnLoop_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	source := &turnLoopFuncSource[string]{fn: func(ctx context.Context, _ time.Duration) (string, ConsumeOption, error) {
		callCount++
		if callCount > 1 {
			cancel()
			return "", NonPreemptiveConsumeOption, ctx.Err()
		}
		return "msg1", NonPreemptiveConsumeOption, nil
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
		OnAgentEvent: func(_ context.Context, _ string, _ *AgentEvent) error {
			eventCount++
			return nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 3, eventCount)
}

func TestTurnLoop_NoOnAgentEvent(t *testing.T) {
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
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, agentErr)
	assert.Contains(t, err.Error(), "agent run failed")
}

// ---------------------------------------------------------------------------
// Tests — preemptive behavior
// ---------------------------------------------------------------------------

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
	callCount := 0
	source := &turnLoopFuncSource[string]{fn: func(_ context.Context, _ time.Duration) (string, ConsumeOption, error) {
		callCount++
		switch callCount {
		case 1:
			return "slow-msg", NonPreemptiveConsumeOption, nil
		case 2:
			<-slowAgent.startedCh
			return "preempt-msg", ConsumeOption{Mode: ConsumePreemptive, CancelOption: CancelOption{Mode: CancelImmediate}}, nil
		default:
			return "", NonPreemptiveConsumeOption, context.DeadlineExceeded
		}
	}}

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
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, slowAgent.cancelled.Load(), "slow agent should have been cancelled")
	assert.Equal(t, CancelImmediate, slowAgent.cancelledOpt.Mode)
	assert.Equal(t, []string{"slow-msg", "preempt-msg"}, processedItems)
}

func TestTurnLoop_PreemptiveWithCancelMode(t *testing.T) {
	slowAgent := &turnLoopCancellableAgent{
		name:      "slow-agent",
		startedCh: make(chan struct{}),
		cancelCh:  make(chan struct{}),
	}

	fastAgent := &turnLoopMockAgent{
		name:   "fast-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	callCount := 0
	source := &turnLoopFuncSource[string]{fn: func(_ context.Context, _ time.Duration) (string, ConsumeOption, error) {
		callCount++
		switch callCount {
		case 1:
			return "slow-msg", NonPreemptiveConsumeOption, nil
		case 2:
			<-slowAgent.startedCh
			return "preempt-msg", ConsumeOption{
				Mode:         ConsumePreemptive,
				CancelOption: CancelOption{Mode: CancelAfterChatModel | CancelAfterToolCall},
			}, nil
		default:
			return "", NonPreemptiveConsumeOption, context.DeadlineExceeded
		}
	}}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, item string) (Agent, error) {
			if item == "slow-msg" {
				return slowAgent, nil
			}
			return fastAgent, nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, slowAgent.cancelled.Load())
	assert.Equal(t, CancelAfterChatModel|CancelAfterToolCall, slowAgent.cancelledOpt.Mode)
}

func TestTurnLoop_PreemptiveNonCancellableAgent(t *testing.T) {
	agentStarted := make(chan struct{})
	agentContinue := make(chan struct{})

	blockingAgent := &turnLoopBlockingAgent{
		name:      "blocking-agent",
		startedCh: agentStarted,
		blockCh:   agentContinue,
		events:    []*AgentEvent{{Output: &AgentOutput{}}},
	}
	fastAgent := &turnLoopMockAgent{
		name:   "fast-agent",
		events: []*AgentEvent{{Output: &AgentOutput{}}},
	}

	var processedItems []string
	callCount := 0
	source := &turnLoopFuncSource[string]{fn: func(_ context.Context, _ time.Duration) (string, ConsumeOption, error) {
		callCount++
		switch callCount {
		case 1:
			return "blocking-msg", NonPreemptiveConsumeOption, nil
		case 2:
			<-agentStarted
			close(agentContinue)
			return "preempt-msg", ConsumeOption{Mode: ConsumePreemptive}, nil
		default:
			return "", NonPreemptiveConsumeOption, context.DeadlineExceeded
		}
	}}

	loop, err := NewTurnLoop(TurnLoopConfig[string]{
		Source: source,
		GenInput: func(_ context.Context, item string) (*AgentInput, []AgentRunOption, error) {
			processedItems = append(processedItems, item)
			return &AgentInput{Messages: []Message{schema.UserMessage(item)}}, nil, nil
		},
		GetAgent: func(_ context.Context, item string) (Agent, error) {
			if item == "blocking-msg" {
				return blockingAgent, nil
			}
			return fastAgent, nil
		},
	})
	require.NoError(t, err)

	err = loop.Run(context.Background())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, []string{"blocking-msg", "preempt-msg"}, processedItems)
}
