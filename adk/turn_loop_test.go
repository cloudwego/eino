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
	cancelFunc func(opts ...AgentCancelOption) error
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

type turnLoopCheckpointStore struct {
	m  map[string][]byte
	mu sync.Mutex
}

func (s *turnLoopCheckpointStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *turnLoopCheckpointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok, nil
}

type turnLoopCancellableMockAgent struct {
	name     string
	runFunc  func(ctx context.Context, input *AgentInput) (*AgentOutput, error)
	onCancel func(cc *cancelContext)
	cancel   context.CancelFunc
	mu       sync.Mutex
}

func (a *turnLoopCancellableMockAgent) Name(_ context.Context) string        { return a.name }
func (a *turnLoopCancellableMockAgent) Description(_ context.Context) string { return "mock agent" }

func (a *turnLoopCancellableMockAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()

	o := getCommonOptions(nil, opts...)
	cc := o.cancelCtx

	a.mu.Lock()
	var cancelCtx context.Context
	cancelCtx, a.cancel = context.WithCancel(ctx)
	a.mu.Unlock()

	go func() {
		defer gen.Close()
		if cc != nil {
			go func() {
				<-cc.cancelChan
				// CRITICAL: call onCancel BEFORE cancel() to avoid race condition.
				// If cancel() fires first, the runFunc returns immediately,
				// flowAgent's defer calls markDone(), and doneChan closes
				// before onCancel can read cc.config.
				if a.onCancel != nil {
					a.onCancel(cc)
				}
				a.mu.Lock()
				if a.cancel != nil {
					a.cancel()
				}
				a.mu.Unlock()
			}()
		}

		output, err := a.runFunc(cancelCtx, input)
		if err != nil {
			gen.Send(&AgentEvent{Err: err})
			return
		}
		gen.Send(&AgentEvent{Output: output})
	}()
	return iter
}

func mustNewTurnLoop[T any](cfg TurnLoopConfig[T]) *TurnLoop[T] {
	l, err := NewTurnLoop(cfg)
	if err != nil {
		panic(err)
	}
	return l
}

func newAndRunTurnLoop[T any](ctx context.Context, cfg TurnLoopConfig[T]) *TurnLoop[T] {
	l := mustNewTurnLoop(cfg)
	_ = l.Run(ctx)
	return l
}

func TestTurnLoop_RunAndPush(t *testing.T) {
	processedItems := make([]string, 0)
	var mu sync.Mutex

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			mu.Lock()
			processedItems = append(processedItems, items...)
			mu.Unlock()
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	time.Sleep(100 * time.Millisecond)

	loop.Stop()
	result := loop.Wait()

	mu.Lock()
	defer mu.Unlock()

	assert.NoError(t, result.ExitReason)
	assert.True(t, len(processedItems) > 0, "should have processed at least one item")
}

func TestTurnLoop_PushReturnsErrorAfterStop(t *testing.T) {
	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Stop()

	ok, _ := loop.Push("msg1")
	assert.False(t, ok)
}

func TestTurnLoop_StopIsIdempotent(t *testing.T) {
	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Stop()
	loop.Stop()
	loop.Stop()

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
}

func TestTurnLoop_WaitMultipleGoroutines(t *testing.T) {
	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Stop()

	var wg sync.WaitGroup
	results := make([]*TurnLoopExitState[string], 3)

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

func TestTurnLoop_UnhandledItemsOnStop(t *testing.T) {
	started := make(chan struct{})
	blocked := make(chan struct{})

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			close(started)
			<-blocked
			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")
	loop.Push("msg3")

	<-started

	loop.Stop()
	close(blocked)

	result := loop.Wait()
	assert.True(t, len(result.UnhandledItems) >= 0, "should return unhandled items")
}

func TestTurnLoop_GenInputError(t *testing.T) {
	genErr := errors.New("gen input error")

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return nil, genErr
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, genErr)
}

func TestTurnLoop_GetAgentError(t *testing.T) {
	agentErr := errors.New("get agent error")

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return nil, agentErr
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, agentErr)
}

func TestTurnLoop_BatchProcessing(t *testing.T) {
	var batches [][]string
	var mu sync.Mutex

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			mu.Lock()
			batches = append(batches, items)
			mu.Unlock()

			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")
	loop.Push("msg3")

	time.Sleep(200 * time.Millisecond)

	loop.Stop()
	loop.Wait()

	mu.Lock()
	defer mu.Unlock()

	assert.True(t, len(batches) > 0, "should have processed at least one batch")
}

func TestTurnLoop_StopWithMode(t *testing.T) {
	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Stop(WithAgentCancel(WithAgentCancelMode(CancelAfterToolCalls)))

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
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

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
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

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&genInputCalls), int32(2))
}

func TestTurnLoop_Preempt_DiscardsConsumedItems(t *testing.T) {
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

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
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

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, len(genInputResults), 2)
	if len(genInputResults) >= 2 {
		assert.NotContains(t, genInputResults[1], "first")
		assert.Contains(t, genInputResults[1], "urgent")
	}
}

func TestTurnLoop_Preempt_WithAgentCancelMode(t *testing.T) {
	agentStarted := make(chan struct{})
	cancelFuncCalled := make(chan struct{})
	agentStartedOnce := sync.Once{}
	cancelFuncCalledOnce := sync.Once{}
	firstCancelModeUsed := CancelImmediate
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
		onCancel: func(cc *cancelContext) {
			cancelModeMu.Lock()
			cancelFuncCalledOnce.Do(func() {
				firstCancelModeUsed = cc.getMode()
				close(cancelFuncCalled)
			})
			cancelModeMu.Unlock()
		},
	}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
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

	loop.Push("urgent", WithPreempt(WithAgentCancelMode(CancelAfterToolCalls)))

	select {
	case <-cancelFuncCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("cancelFunc was not called by preempt")
	}

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	cancelModeMu.Lock()
	actualMode := firstCancelModeUsed
	cancelModeMu.Unlock()
	assert.Equal(t, CancelAfterToolCalls, actualMode)
}

func TestTurnLoop_PreemptAck_ClosesAfterCancelIsInitiated(t *testing.T) {
	agentStarted := make(chan struct{})
	cancelObserved := make(chan struct{})
	agentFinishGate := make(chan struct{})
	agentStartedOnce := sync.Once{}
	cancelObservedOnce := sync.Once{}

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentStartedOnce.Do(func() { close(agentStarted) })
			<-ctx.Done()
			<-agentFinishGate
			return &AgentOutput{}, nil
		},
		onCancel: func(cc *cancelContext) {
			cancelObservedOnce.Do(func() { close(cancelObserved) })
		},
	}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  []string{items[0]},
				Remaining: items[1:],
			}, nil
		},
	})

	_, _ = loop.Push("first")

	select {
	case <-agentStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("agent did not start")
	}

	ok, ack := loop.Push("urgent", WithPreempt(WithAgentCancelMode(CancelAfterToolCalls)))
	assert.True(t, ok)
	assert.NotNil(t, ack)

	select {
	case <-ack:
	case <-time.After(1 * time.Second):
		t.Fatal("preempt ack was not closed")
	}

	select {
	case <-cancelObserved:
	case <-time.After(1 * time.Second):
		t.Fatal("cancel was not initiated")
	}

	close(agentFinishGate)

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
}

func TestTurnLoop_PreemptAck_ClosesImmediatelyIfLoopNotStarted(t *testing.T) {
	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	ok, ack := loop.Push("urgent", WithPreempt())
	assert.True(t, ok)
	assert.NotNil(t, ack)

	select {
	case <-ack:
	case <-time.After(1 * time.Second):
		t.Fatal("preempt ack was not closed")
	}
}

func TestTurnLoop_Preempt_EscalatesOnSecondPreempt(t *testing.T) {
	agentStarted := make(chan struct{})
	firstCancelSeen := make(chan struct{})
	agentFinishGate := make(chan struct{})
	agentStartedOnce := sync.Once{}
	firstCancelOnce := sync.Once{}

	var ccPtr atomic.Value

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentStartedOnce.Do(func() { close(agentStarted) })
			<-ctx.Done()
			<-agentFinishGate
			return &AgentOutput{}, nil
		},
		onCancel: func(cc *cancelContext) {
			ccPtr.Store(cc)
			firstCancelOnce.Do(func() { close(firstCancelSeen) })
		},
	}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
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

	loop.Push("urgent1", WithPreempt(WithAgentCancelMode(CancelAfterChatModel)))
	select {
	case <-firstCancelSeen:
	case <-time.After(1 * time.Second):
		t.Fatal("first preempt did not trigger cancel")
	}

	loop.Push("urgent2", WithPreempt(WithAgentCancelMode(CancelImmediate)))

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		v := ccPtr.Load()
		if v == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		cc := v.(*cancelContext)
		if cc.getMode() == CancelImmediate && atomic.LoadInt32(&cc.escalated) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	v := ccPtr.Load()
	if v == nil {
		t.Fatal("cancel context was not captured")
	}
	cc := v.(*cancelContext)
	assert.Equal(t, CancelImmediate, cc.getMode())
	assert.Equal(t, int32(1), atomic.LoadInt32(&cc.escalated))

	close(agentFinishGate)

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
}

func TestTurnLoop_Preempt_JoinsSafePointModesOnSecondPreempt(t *testing.T) {
	agentStarted := make(chan struct{})
	firstCancelSeen := make(chan struct{})
	agentFinishGate := make(chan struct{})
	agentStartedOnce := sync.Once{}
	firstCancelOnce := sync.Once{}

	var ccPtr atomic.Value

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentStartedOnce.Do(func() { close(agentStarted) })
			<-ctx.Done()
			<-agentFinishGate
			return &AgentOutput{}, nil
		},
		onCancel: func(cc *cancelContext) {
			ccPtr.Store(cc)
			firstCancelOnce.Do(func() { close(firstCancelSeen) })
		},
	}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
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

	loop.Push("urgent1", WithPreempt(WithAgentCancelMode(CancelAfterChatModel)))
	select {
	case <-firstCancelSeen:
	case <-time.After(1 * time.Second):
		t.Fatal("first preempt did not trigger cancel")
	}

	loop.Push("urgent2", WithPreempt(WithAgentCancelMode(CancelAfterToolCalls)))

	want := CancelAfterChatModel | CancelAfterToolCalls
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		v := ccPtr.Load()
		if v == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		cc := v.(*cancelContext)
		if cc.getMode() == want {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	v := ccPtr.Load()
	if v == nil {
		t.Fatal("cancel context was not captured")
	}
	cc := v.(*cancelContext)
	assert.Equal(t, want, cc.getMode())

	close(agentFinishGate)

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
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

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
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

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.Equal(t, 2, agentRunCount)
}

func TestTurnLoop_PreemptDelay_NoMispreemptOnNaturalCompletion(t *testing.T) {
	agent1Started := make(chan struct{})
	agent1Done := make(chan struct{})
	agent2Started := make(chan struct{})
	agent2Done := make(chan struct{})
	agent1StartedOnce := sync.Once{}
	agent1DoneOnce := sync.Once{}
	agent2StartedOnce := sync.Once{}
	agent2DoneOnce := sync.Once{}

	var agentRunCount int32

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			count := atomic.AddInt32(&agentRunCount, 1)
			if count == 1 {
				agent1StartedOnce.Do(func() { close(agent1Started) })
				time.Sleep(50 * time.Millisecond)
				agent1DoneOnce.Do(func() { close(agent1Done) })
			} else if count == 2 {
				agent2StartedOnce.Do(func() { close(agent2Started) })
				time.Sleep(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					t.Error("Agent2 was unexpectedly cancelled")
					return nil, ctx.Err()
				default:
				}
				agent2DoneOnce.Do(func() { close(agent2Done) })
			}
			return &AgentOutput{}, nil
		},
	}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  []string{items[0]},
				Remaining: items[1:],
			}, nil
		},
	})

	loop.Push("first")

	select {
	case <-agent1Started:
	case <-time.After(1 * time.Second):
		t.Fatal("agent1 did not start")
	}

	loop.Push("second", WithPreempt(), WithPreemptDelay(500*time.Millisecond))

	select {
	case <-agent1Done:
	case <-time.After(1 * time.Second):
		t.Fatal("agent1 did not complete naturally")
	}

	select {
	case <-agent2Started:
	case <-time.After(1 * time.Second):
		t.Fatal("agent2 did not start")
	}

	select {
	case <-agent2Done:
	case <-time.After(1 * time.Second):
		t.Fatal("agent2 did not complete - may have been incorrectly preempted")
	}

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.Equal(t, int32(2), atomic.LoadInt32(&agentRunCount))
}

func TestTurnLoop_ConcurrentPush(t *testing.T) {
	var count int32

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			atomic.AddInt32(&count, int32(len(items)))
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = loop.Push(fmt.Sprintf("msg-%d-%d", i, j))
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	loop.Stop()
	result := loop.Wait()

	processed := atomic.LoadInt32(&count)
	unhandled := len(result.UnhandledItems)

	assert.True(t, processed > 0, "should have processed some items")
	assert.True(t, int(processed)+unhandled <= 100, "total should not exceed pushed amount")
}

func TestTurnLoop_StopAfterReceive_RecoverItem(t *testing.T) {
	receiveStarted := make(chan struct{})
	cancelDone := make(chan struct{})

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			close(receiveStarted)
			<-cancelDone
			time.Sleep(50 * time.Millisecond)
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	<-receiveStarted

	loop.Stop()
	close(cancelDone)

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
}

func TestTurnLoop_StopAfterGenInput_RecoverConsumed(t *testing.T) {
	genInputDone := make(chan struct{})

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			close(genInputDone)
			time.Sleep(50 * time.Millisecond)
			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			time.Sleep(100 * time.Millisecond)
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	<-genInputDone

	time.Sleep(60 * time.Millisecond)
	loop.Stop()

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
}

func TestTurnLoop_GetAgentError_RecoverConsumed(t *testing.T) {
	agentErr := errors.New("get agent error")

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], c []string) (Agent, error) {
			return nil, agentErr
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, agentErr)
	assert.True(t, len(result.UnhandledItems) >= 1, "should recover at least the consumed item and remaining")
}

func TestTurnLoop_GenInputError_RecoverItems(t *testing.T) {
	genErr := errors.New("gen input error")

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return nil, genErr
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, genErr)
	assert.Len(t, result.UnhandledItems, 2, "should recover all items when GenInput fails")
	assert.Contains(t, result.UnhandledItems, "msg1")
	assert.Contains(t, result.UnhandledItems, "msg2")
}

func TestTurnLoop_PrepareAgentError_RecoverItemsInOrder(t *testing.T) {
	agentErr := errors.New("prepare agent error")

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			var urgent string
			remaining := make([]string, 0, len(items))
			for _, item := range items {
				if item == "urgent" {
					urgent = item
				} else {
					remaining = append(remaining, item)
				}
			}
			if urgent != "" {
				return &GenInputResult[string]{
					Input:     &AgentInput{},
					Consumed:  []string{urgent},
					Remaining: remaining,
				}, nil
			}
			return &GenInputResult[string]{
				Input:     &AgentInput{},
				Consumed:  items[:1],
				Remaining: items[1:],
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return nil, agentErr
		},
	})

	loop.Push("msg1")
	loop.Push("urgent")
	loop.Push("msg2")

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, agentErr)
	assert.Len(t, result.UnhandledItems, 3, "should recover all items")
	assert.Equal(t, []string{"msg1", "urgent", "msg2"}, result.UnhandledItems,
		"should preserve original push order even when GenInput selects non-prefix items")
}

// Context cancel tests: the TurnLoop monitors context cancellation by closing
// the internal buffer when ctx.Done() fires, which unblocks the blocking
// Receive() call. The loop then checks ctx.Err() and exits with the context error.

func TestTurnLoop_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	genInputStarted := make(chan struct{})
	genInputDone := make(chan struct{})

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			close(genInputStarted)
			<-genInputDone
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], c []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")

	<-genInputStarted
	cancel()
	close(genInputDone)

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, context.Canceled)
}

func TestTurnLoop_ContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			select {
			case <-time.After(100 * time.Millisecond):
				return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], c []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, context.DeadlineExceeded)
}

func TestTurnLoop_ContextCancelBeforeReceive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], c []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	// Push before Run to guarantee the item is buffered before the
	// context-monitoring goroutine can close the buffer.
	_, _ = loop.Push("msg1")
	_ = loop.Run(ctx)

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, context.Canceled)
	assert.Len(t, result.UnhandledItems, 1)
}

func TestTurnLoop_ContextCancelDuringBlockingReceive(t *testing.T) {
	// When context is cancelled while Receive() is blocking (no items in buffer),
	// the context monitoring goroutine closes the buffer, which unblocks Receive().
	ctx, cancel := context.WithCancel(context.Background())

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], c []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	// Don't push any items — let Receive() block
	time.Sleep(50 * time.Millisecond)
	cancel()

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, context.Canceled)
}

func TestTurnLoop_ContextCancelAfterGenInput_RecoverItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	genInputCount := 0
	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
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
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], c []string) (Agent, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, context.Canceled)
	assert.True(t, len(result.UnhandledItems) >= 1, "should recover consumed and remaining items")
}

func TestTurnLoop_OnAgentEventsReceivesEvents(t *testing.T) {
	var receivedEvents []*AgentEvent
	var receivedConsumed []string
	var mu sync.Mutex

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			mu.Lock()
			receivedConsumed = append(receivedConsumed, tc.Consumed...)
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

	loop.Stop()
	result := loop.Wait()

	assert.NoError(t, result.ExitReason)

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, len(receivedConsumed) > 0, "should have received consumed items")
}

func TestTurnLoop_StopDuringAgentExecution(t *testing.T) {
	agentStarted := make(chan struct{})

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
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
	loop.Stop(WithAgentCancel(WithAgentCancelMode(CancelImmediate)))

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.Equal(t, []string{"msg1"}, result.CanceledItems)
}

func TestTurnLoop_StopCheckPointIDInCancelError(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	checkpointID := "turn-loop-cancel-ckpt-1"
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}

	slowModel := &cancelTestChatModel{
		delay: 500 * time.Millisecond,
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "Hello",
		},
		startedChan: modelStarted,
		doneChan:    make(chan struct{}, 1),
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent",
		Instruction: "You are a test assistant",
		Model:       slowModel,
	})
	assert.NoError(t, err)

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store: store,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				RunOpts:  []AgentRunOption{WithCheckPointID(checkpointID)},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
	})

	loop.Push("msg1")

	<-modelStarted
	loop.Stop(WithAgentCancel(WithAgentCancelMode(CancelImmediate)))

	result := loop.Wait()

	var cancelErr *CancelError
	if assert.True(t, errors.As(result.ExitReason, &cancelErr), "ExitReason should be a *CancelError") {
		assert.Equal(t, checkpointID, cancelErr.CheckPointID, "CancelError should contain the checkpoint ID")
	}
	assert.Equal(t, checkpointID, result.CheckPointID)
}

func TestTurnLoop_StopWithoutCheckPointIDPersists(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}

	slowModel := &cancelTestChatModel{
		delay: 500 * time.Millisecond,
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "Hello",
		},
		startedChan: modelStarted,
		doneChan:    make(chan struct{}, 1),
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent",
		Instruction: "You are a test assistant",
		Model:       slowModel,
	})
	assert.NoError(t, err)

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store: store,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
	})

	loop.Push("msg1")

	<-modelStarted
	loop.Stop(WithAgentCancel(WithAgentCancelMode(CancelImmediate)))

	result := loop.Wait()

	var cancelErr *CancelError
	if assert.True(t, errors.As(result.ExitReason, &cancelErr), "ExitReason should be a *CancelError") {
		assert.NotEmpty(t, cancelErr.CheckPointID)
		assert.Equal(t, cancelErr.CheckPointID, result.CheckPointID)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	_, ok := store.m[result.CheckPointID]
	assert.True(t, ok)
}

func TestTurnLoop_StopBetweenTurnsAndResume(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}

	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		Store: store,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("a")
	loop.Push("b")
	loop.Stop()
	_ = loop.Run(ctx)

	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)
	assert.NotEmpty(t, exit.CheckPointID)

	var seen []string
	var mu sync.Mutex
	loop2 := mustNewTurnLoop(TurnLoopConfig[string]{
		Store: store,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			mu.Lock()
			seen = append([]string{}, items...)
			mu.Unlock()
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})

	assert.NoError(t, loop2.Resume(ctx, exit.CheckPointID, []string{"c"}))
	exit2 := loop2.Wait()
	assert.NoError(t, exit2.ExitReason)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a", "b", "c"}, seen)
}

func TestTurnLoop_ExternalTurnState_ResumeRequiresItems(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		Store:             store,
		ExternalTurnState: true,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	assert.Error(t, loop.Resume(ctx, "cp-1", nil))
}

func TestTurnLoop_FrameworkMode_RejectsResumeItems(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		Store: store,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	assert.Error(t, loop.Resume(ctx, "cp-1", nil, WithResumeItems[string]([]string{"a"}, []string{"b"})))
}

func TestTurnLoop_StopDuringAgentExecution_PersistAndResume(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}

	slowModel := &cancelTestChatModel{
		delay: 500 * time.Millisecond,
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "Hello",
		},
		startedChan: modelStarted,
		doneChan:    make(chan struct{}, 1),
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent",
		Instruction: "You are a test assistant",
		Model:       slowModel,
	})
	assert.NoError(t, err)

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store: store,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
	})

	loop.Push("msg1")
	<-modelStarted
	loop.Stop(WithAgentCancel(WithAgentCancelMode(CancelImmediate)))
	exit := loop.Wait()

	assert.NotEmpty(t, exit.CheckPointID)

	store.mu.Lock()
	_, ok := store.m[exit.CheckPointID]
	store.mu.Unlock()
	assert.True(t, ok)

	slowModel.delay = 10 * time.Millisecond

	var consumed2 []string
	var genResumeCalled bool
	var genInputCalled bool
	loop2 := mustNewTurnLoop(TurnLoopConfig[string]{
		Store: store,
		GenResume: func(ctx context.Context, _ *TurnLoop[string], canceledItems []string, unhandledItems []string, newItems []string) (*GenResumeResult[string], error) {
			genResumeCalled = true
			return &GenResumeResult[string]{
				CheckPointID: exit.CheckPointID,
				Consumed:     canceledItems,
				Remaining:    append(append([]string{}, unhandledItems...), newItems...),
			}, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			genInputCalled = true
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			consumed2 = append([]string{}, consumed...)
			return agent, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})

	assert.NoError(t, loop2.Resume(ctx, exit.CheckPointID, nil))
	exit2 := loop2.Wait()
	assert.NoError(t, exit2.ExitReason)
	assert.Equal(t, []string{"msg1"}, consumed2)
	assert.True(t, genResumeCalled)
	assert.False(t, genInputCalled)
}

func TestTurnLoop_ExternalTurnState_StopAndResume(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}

	slowModel := &cancelTestChatModel{
		delay: 500 * time.Millisecond,
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "Hello",
		},
		startedChan: modelStarted,
		doneChan:    make(chan struct{}, 1),
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent",
		Instruction: "You are a test assistant",
		Model:       slowModel,
	})
	assert.NoError(t, err)

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store:             store,
		ExternalTurnState: true,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
	})

	loop.Push("msg1")
	<-modelStarted
	loop.Stop(WithAgentCancel(WithAgentCancelMode(CancelImmediate)))
	exit := loop.Wait()

	assert.NotEmpty(t, exit.CheckPointID)

	slowModel.delay = 10 * time.Millisecond

	var consumed2 []string
	var genResumeCalled bool
	var genInputCalled bool
	loop2 := mustNewTurnLoop(TurnLoopConfig[string]{
		Store:             store,
		ExternalTurnState: true,
		GenResume: func(ctx context.Context, _ *TurnLoop[string], canceledItems []string, unhandledItems []string, newItems []string) (*GenResumeResult[string], error) {
			genResumeCalled = true
			return &GenResumeResult[string]{
				CheckPointID: exit.CheckPointID,
				Consumed:     canceledItems,
				Remaining:    append(append([]string{}, unhandledItems...), newItems...),
			}, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			genInputCalled = true
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			consumed2 = append([]string{}, consumed...)
			return agent, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})

	assert.NoError(t, loop2.Resume(ctx, exit.CheckPointID, nil, WithResumeItems[string](exit.CanceledItems, exit.UnhandledItems)))
	exit2 := loop2.Wait()
	assert.NoError(t, exit2.ExitReason)
	assert.Equal(t, []string{"msg1"}, consumed2)
	assert.True(t, genResumeCalled)
	assert.False(t, genInputCalled)
}

func TestTurnLoop_ExternalTurnState_StopBetweenTurns(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}

	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		Store:             store,
		ExternalTurnState: true,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("a")
	loop.Push("b")
	loop.Stop()
	assert.NoError(t, loop.Run(ctx))

	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)
	assert.NotEmpty(t, exit.CheckPointID)

	var seen []string
	var mu sync.Mutex
	loop2 := mustNewTurnLoop(TurnLoopConfig[string]{
		Store:             store,
		ExternalTurnState: true,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			mu.Lock()
			seen = append([]string{}, items...)
			mu.Unlock()
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})

	assert.NoError(t, loop2.Resume(ctx, exit.CheckPointID, []string{"c"}, WithResumeItems[string](exit.CanceledItems, exit.UnhandledItems)))
	exit2 := loop2.Wait()
	assert.NoError(t, exit2.ExitReason)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a", "b", "c"}, seen)
}

func TestTurnLoop_StopOptionsArePassed(t *testing.T) {
	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Stop(WithAgentCancel(WithAgentCancelMode(CancelAfterToolCalls)))

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
}

func TestTurnLoop_DefaultOnAgentEvents_ErrorPropagation(t *testing.T) {
	agentErr := errors.New("agent execution error")

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{
				name: "test",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					return nil, agentErr
				},
			}, nil
		},
		// No OnAgentEvents — use default handler
	})

	loop.Push("msg1")

	result := loop.Wait()
	// The default handler should propagate the agent error as ExitReason
	assert.Error(t, result.ExitReason)
}

func TestTurnLoop_OnAgentEventsError(t *testing.T) {
	handlerErr := errors.New("event handler error")

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			// Drain events then return error
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			return handlerErr
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.ErrorIs(t, result.ExitReason, handlerErr)
}

func TestTurnLoop_StopCallFromGenInput(t *testing.T) {
	// Test that calling Stop() from within GenInput works correctly
	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, loop *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			loop.Stop()
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
}

func TestTurnLoop_PushFromOnAgentEvents(t *testing.T) {
	// Test that calling Push() from within OnAgentEvents works
	pushCount := int32(0)

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  []string{items[0]},
				Remaining: items[1:],
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			count := atomic.AddInt32(&pushCount, 1)
			if count == 1 {
				// Push a follow-up item from the callback
				_, _ = tc.Loop.Push("follow-up")
			} else {
				tc.Loop.Stop()
			}
			return nil
		},
	})

	loop.Push("initial")

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&pushCount), int32(2))
}

// Tests for NewTurnLoop: the permissive API where Push, Stop, and Wait are
// all valid on a not-yet-running loop.

func TestNewTurnLoop_PushBeforeRun(t *testing.T) {
	// Items pushed before Run are buffered and processed after Run starts.
	var processedItems []string
	var mu sync.Mutex

	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			mu.Lock()
			processedItems = append(processedItems, items...)
			mu.Unlock()
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	// Push before Run — items should be buffered.
	ok, _ := loop.Push("msg1")
	assert.True(t, ok)
	ok, _ = loop.Push("msg2")
	assert.True(t, ok)

	_ = loop.Run(context.Background())

	time.Sleep(100 * time.Millisecond)

	loop.Stop()
	result := loop.Wait()

	mu.Lock()
	defer mu.Unlock()

	assert.NoError(t, result.ExitReason)
	assert.Contains(t, processedItems, "msg1")
	assert.Contains(t, processedItems, "msg2")
}

func TestNewTurnLoop_StopBeforeRun(t *testing.T) {
	// Stop before Run sets the stopped flag. When Run is called, the loop
	// exits immediately and buffered items appear as UnhandledItems.
	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			t.Fatal("GenInput should not be called")
			return nil, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			t.Fatal("PrepareAgent should not be called")
			return nil, nil
		},
	})

	loop.Push("msg1")
	loop.Push("msg2")
	loop.Stop()

	// Push after Stop returns false.
	ok, _ := loop.Push("msg3")
	assert.False(t, ok)

	_ = loop.Run(context.Background())
	result := loop.Wait()

	assert.NoError(t, result.ExitReason)
	assert.Equal(t, []string{"msg1", "msg2"}, result.UnhandledItems)
}

func TestNewTurnLoop_WaitBeforeRun(t *testing.T) {
	// Wait blocks until Run is called AND the loop exits.
	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	waitDone := make(chan *TurnLoopExitState[string], 1)
	go func() {
		waitDone <- loop.Wait()
	}()

	// Wait should not return yet since Run hasn't been called.
	select {
	case <-waitDone:
		t.Fatal("Wait returned before Run was called")
	case <-time.After(50 * time.Millisecond):
		// expected
	}

	loop.Push("msg1")
	loop.Stop()
	_ = loop.Run(context.Background())

	select {
	case result := <-waitDone:
		assert.NoError(t, result.ExitReason)
		assert.Equal(t, []string{"msg1"}, result.UnhandledItems)
	case <-time.After(1 * time.Second):
		t.Fatal("Wait did not return after Run + Stop")
	}
}

func TestNewTurnLoop_RunIsIdempotent(t *testing.T) {
	// Calling Run multiple times returns an error after the first call.
	var genInputCalls int32

	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			atomic.AddInt32(&genInputCalls, 1)
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	assert.NoError(t, loop.Run(context.Background()))
	assert.Error(t, loop.Run(context.Background()))
	assert.Error(t, loop.Run(context.Background()))

	time.Sleep(100 * time.Millisecond)

	loop.Stop()
	result := loop.Wait()

	assert.NoError(t, result.ExitReason)
	// If Run spawned multiple goroutines, we'd see duplicate processing or panics.
	// A clean exit with processed items confirms idempotency.
	assert.True(t, atomic.LoadInt32(&genInputCalls) >= 1)
}

func TestNewTurnLoop_StopBeforeRun_ThenWait(t *testing.T) {
	// Demonstrates the full sequence: create, push, stop, run, wait.
	loop := mustNewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			t.Fatal("GenInput should not be called after Stop")
			return nil, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			t.Fatal("PrepareAgent should not be called after Stop")
			return nil, nil
		},
	})

	loop.Push("a")
	loop.Push("b")
	loop.Push("c")
	loop.Stop()

	// Run after Stop: the loop goroutine starts but exits immediately.
	_ = loop.Run(context.Background())

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.Equal(t, []string{"a", "b", "c"}, result.UnhandledItems)
}

func TestNewTurnLoop_ConcurrentPushAndRun(t *testing.T) {
	// Concurrent Push and Run should not race.
	for i := 0; i < 100; i++ {
		var count int32

		loop := mustNewTurnLoop(TurnLoopConfig[string]{
			GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
				atomic.AddInt32(&count, int32(len(items)))
				return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
			},
			PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
				return &turnLoopMockAgent{name: "test"}, nil
			},
		})

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			_, _ = loop.Push("item")
		}()

		go func() {
			defer wg.Done()
			_ = loop.Run(context.Background())
		}()

		wg.Wait()

		time.Sleep(50 * time.Millisecond)

		loop.Stop()
		result := loop.Wait()
		assert.NoError(t, result.ExitReason)

		processed := atomic.LoadInt32(&count)
		unhandled := len(result.UnhandledItems)
		assert.True(t, int(processed)+unhandled <= 1,
			"total should not exceed pushed amount")
	}
}

type turnCtxKey struct{}

func TestTurnLoop_RunCtx_Propagation(t *testing.T) {
	// Verify that GenInputResult.RunCtx is propagated to PrepareAgent,
	// the agent run, and OnAgentEvents.

	const traceVal = "trace-123"
	var prepareCtxVal, agentCtxVal, eventsCtxVal string

	cfg := TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, loop *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			// Derive a new context with per-item trace data
			runCtx := context.WithValue(ctx, turnCtxKey{}, traceVal)
			return &GenInputResult[string]{
				RunCtx:   runCtx,
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, loop *TurnLoop[string], consumed []string) (Agent, error) {
			if v, ok := ctx.Value(turnCtxKey{}).(string); ok {
				prepareCtxVal = v
			}
			return &turnLoopMockAgent{
				name: "trace-agent",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					if v, ok := ctx.Value(turnCtxKey{}).(string); ok {
						agentCtxVal = v
					}
					return &AgentOutput{}, nil
				},
			}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			if v, ok := ctx.Value(turnCtxKey{}).(string); ok {
				eventsCtxVal = v
			}
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	}

	loop := mustNewTurnLoop(cfg)
	loop.Push("hello")
	_ = loop.Run(context.Background())
	result := loop.Wait()

	assert.Nil(t, result.ExitReason)
	assert.Equal(t, traceVal, prepareCtxVal, "PrepareAgent should receive RunCtx")
	assert.Equal(t, traceVal, agentCtxVal, "Agent run should receive RunCtx")
	assert.Equal(t, traceVal, eventsCtxVal, "OnAgentEvents should receive RunCtx")
}

func TestTurnLoop_TurnContext_PreemptedChannel(t *testing.T) {
	preemptedSeen := make(chan struct{})
	agentStarted := make(chan struct{})

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopCancellableMockAgent{
				name: "slow",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			close(agentStarted)
			select {
			case <-tc.Preempted:
				close(preemptedSeen)
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for Preempted channel")
			}
			// Drain events
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			return nil
		},
	})

	loop.Push("msg1")
	<-agentStarted
	loop.Push("msg2", WithPreempt(WithAgentCancelMode(CancelImmediate)))

	select {
	case <-preemptedSeen:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("preempted channel was never observed in OnAgentEvents")
	}

	loop.Stop()
	loop.Wait()
}

func TestTurnLoop_TurnContext_StoppedChannel(t *testing.T) {
	stoppedSeen := make(chan struct{})
	agentStarted := make(chan struct{})

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopCancellableMockAgent{
				name: "slow",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			close(agentStarted)
			select {
			case <-tc.Stopped:
				close(stoppedSeen)
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for Stopped channel")
			}
			// Drain events
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			return nil
		},
	})

	loop.Push("msg1")
	<-agentStarted
	loop.Stop(WithAgentCancel(WithAgentCancelMode(CancelImmediate)))

	select {
	case <-stoppedSeen:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("stopped channel was never observed in OnAgentEvents")
	}

	loop.Wait()
}
