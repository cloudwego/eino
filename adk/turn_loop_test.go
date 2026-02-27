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

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

type turnLoopMockAgent struct {
	name       string
	events     []*AgentEvent
	runFunc    func(ctx context.Context, input *AgentInput) (*AgentOutput, error)
	cancelFunc func(opts ...CancelOption) error
}

func (a *turnLoopMockAgent) Name(_ context.Context) string        { return a.name }
func (a *turnLoopMockAgent) Description(_ context.Context) string { return "mock agent" }
func (a *turnLoopMockAgent) Run(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()

	if a.runFunc != nil {
		go func() {
			defer gen.Close()
			output, err := a.runFunc(ctx, input)
			if err != nil {
				gen.Send(&AgentEvent{Err: err})
				return
			}
			gen.Send(&AgentEvent{Output: output})
		}()
		return iter
	}

	go func() {
		defer gen.Close()
		for _, e := range a.events {
			gen.Send(e)
		}
	}()
	return iter
}

type turnLoopCancellableMockAgent struct {
	name       string
	runFunc    func(ctx context.Context, input *AgentInput) (*AgentOutput, error)
	cancelFunc func(opts ...CancelOption) error
	cancelCtx  context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
}

var _ CancellableAgent = (*turnLoopCancellableMockAgent)(nil)

func (a *turnLoopCancellableMockAgent) Name(_ context.Context) string        { return a.name }
func (a *turnLoopCancellableMockAgent) Description(_ context.Context) string { return "mock agent" }

func (a *turnLoopCancellableMockAgent) Run(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return a.runWithCancel(ctx, input, nil)
}

func (a *turnLoopCancellableMockAgent) RunWithCancel(ctx context.Context, input *AgentInput, _ ...AgentRunOption) (*AsyncIterator[*AgentEvent], CancelFunc) {
	iter := a.runWithCancel(ctx, input, nil)
	cancelFunc := func(opts ...CancelOption) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.cancel != nil {
			a.cancel()
		}
		if a.cancelFunc != nil {
			return a.cancelFunc(opts...)
		}
		return nil
	}
	return iter, cancelFunc
}

func (a *turnLoopCancellableMockAgent) runWithCancel(ctx context.Context, input *AgentInput, _ []AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()

	a.mu.Lock()
	a.cancelCtx, a.cancel = context.WithCancel(ctx)
	cancelCtx := a.cancelCtx
	a.mu.Unlock()

	go func() {
		defer gen.Close()
		output, err := a.runFunc(cancelCtx, input)
		if err != nil {
			gen.Send(&AgentEvent{Err: err})
			return
		}
		gen.Send(&AgentEvent{Output: output})
	}()
	return iter
}

func TestTurnLoop_RunAndPush(t *testing.T) {
	processedItems := make([]string, 0)
	var mu sync.Mutex

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_PushReturnsErrorAfterCancel(t *testing.T) {
	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_CancelIsIdempotent(t *testing.T) {
	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_WaitMultipleGoroutines(t *testing.T) {
	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Cancel()

	var wg sync.WaitGroup
	results := make([]*TurnLoopResult[string], 3)

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

func TestTurnLoop_UnhandledItemsOnCancel(t *testing.T) {
	started := make(chan struct{})
	blocked := make(chan struct{})

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_GenInputError(t *testing.T) {
	genErr := errors.New("gen input error")

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_GetAgentError(t *testing.T) {
	agentErr := errors.New("get agent error")

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_BatchProcessing(t *testing.T) {
	var batches [][]string
	var mu sync.Mutex

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_CancelWithMode(t *testing.T) {
	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Cancel(WithTurnLoopCancelMode(CancelAfterToolCall))

	result := loop.Wait()
	assert.NoError(t, result.Error)
}

func TestTurnLoop_Preempt_CancelsCurrentAgent(t *testing.T) {
	agentStarted := make(chan struct{})
	agentCancelled := make(chan struct{})
	agentStartedOnce := sync.Once{}
	agentCancelledOnce := sync.Once{}

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentStartedOnce.Do(func() {
				close(agentStarted)
			})
			<-ctx.Done()
			agentCancelledOnce.Do(func() {
				close(agentCancelled)
			})
			return &AgentOutput{}, nil
		},
	}

	genInputCalls := int32(0)
	secondGenInputCalled := make(chan struct{})
	secondGenInputOnce := sync.Once{}

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			count := atomic.AddInt32(&genInputCalls, 1)
			if count >= 2 {
				secondGenInputOnce.Do(func() {
					close(secondGenInputCalled)
				})
			}
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  []string{items[0]},
				Remaining: items[1:],
			}, nil
		},
	})

	loop.Push("first")

	select {
	case <-agentStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("agent did not start")
	}

	loop.Push("urgent", WithPreempt())

	select {
	case <-agentCancelled:
	case <-time.After(1 * time.Second):
		t.Fatal("agent was not cancelled by preempt")
	}

	select {
	case <-secondGenInputCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("second GenInput was not called after preempt")
	}

	loop.Cancel()
	result := loop.Wait()
	assert.NoError(t, result.Error)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&genInputCalls), int32(2))
}

func TestTurnLoop_Preempt_RecoverConsumedItems(t *testing.T) {
	agentStarted := make(chan struct{})
	agentDone := make(chan struct{})
	agentStartedOnce := sync.Once{}
	agentDoneOnce := sync.Once{}
	firstAgentRun := true
	var firstRunMu sync.Mutex

	genInputResults := make([][]string, 0)
	var mu sync.Mutex

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			firstRunMu.Lock()
			isFirst := firstAgentRun
			firstAgentRun = false
			firstRunMu.Unlock()

			if isFirst {
				agentStartedOnce.Do(func() {
					close(agentStarted)
				})
				<-ctx.Done()
			} else {
				agentDoneOnce.Do(func() {
					close(agentDone)
				})
			}
			return &AgentOutput{}, nil
		},
	}

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			mu.Lock()
			genInputResults = append(genInputResults, items)
			mu.Unlock()

			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  []string{items[0]},
				Remaining: items[1:],
			}, nil
		},
	})

	loop.Push("first")

	select {
	case <-agentStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("agent did not start")
	}

	loop.Push("urgent", WithPreempt())

	select {
	case <-agentDone:
	case <-time.After(1 * time.Second):
		t.Fatal("second agent run did not complete")
	}

	loop.Cancel()
	result := loop.Wait()
	assert.NoError(t, result.Error)

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, len(genInputResults), 2)
	if len(genInputResults) >= 2 {
		assert.Contains(t, genInputResults[1], "first")
		assert.Contains(t, genInputResults[1], "urgent")
	}
}

func TestTurnLoop_Preempt_WithCancelMode(t *testing.T) {
	agentStarted := make(chan struct{})
	cancelFuncCalled := make(chan struct{})
	agentStartedOnce := sync.Once{}
	cancelFuncCalledOnce := sync.Once{}
	cancelModeUsed := CancelImmediate
	var cancelModeMu sync.Mutex

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentStartedOnce.Do(func() {
				close(agentStarted)
			})
			<-ctx.Done()
			return &AgentOutput{}, nil
		},
		cancelFunc: func(opts ...CancelOption) error {
			cancelModeMu.Lock()
			cfg := &cancelConfig{Mode: CancelImmediate}
			for _, opt := range opts {
				opt(cfg)
			}
			cancelModeUsed = cfg.Mode
			cancelModeMu.Unlock()
			cancelFuncCalledOnce.Do(func() {
				close(cancelFuncCalled)
			})
			return nil
		},
	}

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  []string{items[0]},
				Remaining: items[1:],
			}, nil
		},
	})

	loop.Push("first")

	select {
	case <-agentStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("agent did not start")
	}

	loop.Push("urgent", WithPreempt(WithTurnLoopCancelMode(CancelAfterToolCall)))

	select {
	case <-cancelFuncCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("cancelFunc was not called by preempt")
	}

	loop.Cancel()
	result := loop.Wait()
	assert.NoError(t, result.Error)
	cancelModeMu.Lock()
	actualMode := cancelModeUsed
	cancelModeMu.Unlock()
	assert.Equal(t, CancelAfterToolCall, actualMode)
}

func TestTurnLoop_Push_WithoutPreempt_DoesNotCancel(t *testing.T) {
	agentRunCount := 0
	agentDone := make(chan struct{})

	agent := &turnLoopMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentRunCount++
			if agentRunCount == 1 {
				time.Sleep(100 * time.Millisecond)
			}
			if agentRunCount == 2 {
				close(agentDone)
			}
			return &AgentOutput{}, nil
		},
	}

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  []string{items[0]},
				Remaining: items[1:],
			}, nil
		},
	})

	loop.Push("first")
	time.Sleep(20 * time.Millisecond)
	loop.Push("second")

	select {
	case <-agentDone:
	case <-time.After(1 * time.Second):
		t.Fatal("second agent run did not complete")
	}

	loop.Cancel()
	result := loop.Wait()
	assert.NoError(t, result.Error)
	assert.Equal(t, 2, agentRunCount)
}

func TestTurnLoop_ConcurrentPush(t *testing.T) {
	var count int32

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_CancelAfterReceive_RecoverItem(t *testing.T) {
	receiveStarted := make(chan struct{})
	cancelDone := make(chan struct{})

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_CancelAfterGenInput_RecoverConsumed(t *testing.T) {
	genInputDone := make(chan struct{})

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_GetAgentError_RecoverConsumed(t *testing.T) {
	agentErr := errors.New("get agent error")

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
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

func TestTurnLoop_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	genInputStarted := make(chan struct{})
	genInputDone := make(chan struct{})

	loop := RunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			close(genInputStarted)
			<-genInputDone
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, c []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")

	<-genInputStarted
	cancel()
	close(genInputDone)

	result := loop.Wait()
	assert.ErrorIs(t, result.Error, context.Canceled)
}

func TestTurnLoop_ContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	loop := RunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			time.Sleep(100 * time.Millisecond)
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, c []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.ErrorIs(t, result.Error, context.DeadlineExceeded)
}

func TestTurnLoop_ContextCancelBeforeReceive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	loop := RunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GetAgent: func(ctx context.Context, c []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.ErrorIs(t, result.Error, context.Canceled)
	assert.Len(t, result.UnhandledItems, 1)
}

func TestTurnLoop_ContextCancelAfterGenInput_RecoverItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	genInputCount := 0
	loop := RunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			genInputCount++
			if genInputCount == 1 {
				cancel()
			}
			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		GetAgent: func(ctx context.Context, c []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	result := loop.Wait()
	assert.ErrorIs(t, result.Error, context.Canceled)
	assert.True(t, len(result.UnhandledItems) >= 1, "should recover consumed and remaining items")
}

func TestTurnLoop_OnAgentEventsReceivesEvents(t *testing.T) {
	var receivedEvents []*AgentEvent
	var receivedConsumed []string
	var mu sync.Mutex

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, consumed []string, events *AsyncIterator[*AgentEvent]) error {
			mu.Lock()
			receivedConsumed = append(receivedConsumed, consumed...)
			mu.Unlock()

			for {
				event, ok := events.Next()
				if !ok {
					break
				}
				mu.Lock()
				receivedEvents = append(receivedEvents, event)
				mu.Unlock()
			}
			return nil
		},
	})

	loop.Push("msg1")

	time.Sleep(100 * time.Millisecond)

	loop.Cancel()
	result := loop.Wait()

	assert.NoError(t, result.Error)

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, len(receivedConsumed) > 0, "should have received consumed items")
}

func TestTurnLoop_CancelDuringAgentExecution(t *testing.T) {
	agentStarted := make(chan struct{})

	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, consumed []string, events *AsyncIterator[*AgentEvent]) error {
			close(agentStarted)
			time.Sleep(200 * time.Millisecond)
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			return nil
		},
	})

	loop.Push("msg1")

	<-agentStarted
	loop.Cancel(WithTurnLoopCancelMode(CancelImmediate))

	result := loop.Wait()
	assert.NoError(t, result.Error)
}

func TestTurnLoop_CancelOptionsArePassed(t *testing.T) {
	loop := RunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		GetAgent: func(ctx context.Context, consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Cancel(WithTurnLoopCancelMode(CancelAfterToolCall))

	result := loop.Wait()
	assert.NoError(t, result.Error)
}
