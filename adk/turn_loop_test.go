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
	"github.com/stretchr/testify/require"

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

type turnLoopStopModeProbeAgent struct {
	ccCh chan *cancelContext
}

func (a *turnLoopStopModeProbeAgent) Name(_ context.Context) string        { return "probe" }
func (a *turnLoopStopModeProbeAgent) Description(_ context.Context) string { return "probe" }
func (a *turnLoopStopModeProbeAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	o := getCommonOptions(nil, opts...)
	cc := o.cancelCtx
	a.ccCh <- cc
	go func() {
		defer gen.Close()
		<-cc.cancelChan
		for {
			if cc.getMode() == CancelImmediate {
				gen.Send(&AgentEvent{Err: cc.createCancelError()})
				return
			}
			time.Sleep(1 * time.Millisecond)
		}
	}()
	return iter
}

func newAndRunTurnLoop[T any](ctx context.Context, cfg TurnLoopConfig[T]) *TurnLoop[T] {
	l := NewTurnLoop(cfg)
	l.Run(ctx)
	return l
}

func newPreemptTestLoop(t *testing.T, agent *turnLoopCancellableMockAgent) *TurnLoop[string] {
	t.Helper()

	agentStarted := make(chan struct{})
	agentStartedOnce := sync.Once{}

	originalRunFunc := agent.runFunc
	agent.runFunc = func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
		agentStartedOnce.Do(func() { close(agentStarted) })
		return originalRunFunc(ctx, input)
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

	return loop
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
	assert.NotEmpty(t, processedItems, "should have processed at least one item")
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
	assert.NotEmpty(t, result.UnhandledItems, "should return unhandled items")
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

	assert.NotEmpty(t, batches, "should have processed at least one batch")
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

	loop.Stop(WithGraceful())

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

	loop.Push("urgent", WithPreempt[string](AnySafePoint))

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

	loop.Push("urgent", WithPreempt[string](AnySafePoint))

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
	require.GreaterOrEqual(t, len(genInputResults), 2)
	assert.NotContains(t, genInputResults[1], "first")
	assert.Contains(t, genInputResults[1], "urgent")
}

func TestTurnLoop_Preempt_WithAgentCancelMode(t *testing.T) {
	cancelFuncCalled := make(chan struct{})
	cancelFuncCalledOnce := sync.Once{}
	firstCancelModeUsed := CancelImmediate
	var cancelModeMu sync.Mutex

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
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

	loop := newPreemptTestLoop(t, agent)

	loop.Push("urgent", WithPreempt[string](AfterToolCalls))

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
	cancelObserved := make(chan struct{})
	agentFinishGate := make(chan struct{})
	cancelObservedOnce := sync.Once{}

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			<-ctx.Done()
			<-agentFinishGate
			return &AgentOutput{}, nil
		},
		onCancel: func(cc *cancelContext) {
			cancelObservedOnce.Do(func() { close(cancelObserved) })
		},
	}

	loop := newPreemptTestLoop(t, agent)

	ok, ack := loop.Push("urgent", WithPreempt[string](AfterToolCalls))
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
	loop := NewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	ok, ack := loop.Push("urgent", WithPreempt[string](AnySafePoint))
	assert.True(t, ok)
	assert.NotNil(t, ack)

	select {
	case <-ack:
	case <-time.After(1 * time.Second):
		t.Fatal("preempt ack was not closed")
	}
}

func TestTurnLoop_Preempt_EscalatesOnSecondPreempt(t *testing.T) {
	firstCancelSeen := make(chan struct{})
	agentFinishGate := make(chan struct{})
	firstCancelOnce := sync.Once{}

	var ccPtr atomic.Value

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			<-ctx.Done()
			<-agentFinishGate
			return &AgentOutput{}, nil
		},
		onCancel: func(cc *cancelContext) {
			ccPtr.Store(cc)
			firstCancelOnce.Do(func() { close(firstCancelSeen) })
		},
	}

	loop := newPreemptTestLoop(t, agent)

	loop.Push("urgent1", WithPreempt[string](AfterChatModel))
	select {
	case <-firstCancelSeen:
	case <-time.After(1 * time.Second):
		t.Fatal("first preempt did not trigger cancel")
	}

	loop.Push("urgent2", WithPreemptTimeout[string](AnySafePoint, time.Millisecond))

	wantMode := CancelAfterChatModel | CancelAfterToolCalls
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		v := ccPtr.Load()
		if v == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		cc := v.(*cancelContext)
		if cc.getMode() == wantMode && atomic.LoadInt32(&cc.escalated) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	v := ccPtr.Load()
	if v == nil {
		t.Fatal("cancel context was not captured")
	}
	cc := v.(*cancelContext)
	assert.Equal(t, wantMode, cc.getMode())
	assert.Equal(t, int32(1), atomic.LoadInt32(&cc.escalated))

	close(agentFinishGate)

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
}

func TestTurnLoop_Preempt_JoinsSafePointModesOnSecondPreempt(t *testing.T) {
	firstCancelSeen := make(chan struct{})
	agentFinishGate := make(chan struct{})
	firstCancelOnce := sync.Once{}

	var ccPtr atomic.Value

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			<-ctx.Done()
			<-agentFinishGate
			return &AgentOutput{}, nil
		},
		onCancel: func(cc *cancelContext) {
			ccPtr.Store(cc)
			firstCancelOnce.Do(func() { close(firstCancelSeen) })
		},
	}

	loop := newPreemptTestLoop(t, agent)

	loop.Push("urgent1", WithPreempt[string](AfterChatModel))
	select {
	case <-firstCancelSeen:
	case <-time.After(1 * time.Second):
		t.Fatal("first preempt did not trigger cancel")
	}

	loop.Push("urgent2", WithPreempt[string](AfterToolCalls))

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

	loop.Push("second", WithPreempt[string](AnySafePoint), WithPreemptDelay[string](500*time.Millisecond))

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
	assert.NotEmpty(t, result.UnhandledItems, "should recover at least the consumed item and remaining")
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

	loop := NewTurnLoop(TurnLoopConfig[string]{
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
	loop.Run(ctx)

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
	assert.NotEmpty(t, result.UnhandledItems, "should recover consumed and remaining items")
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
	assert.NotEmpty(t, receivedConsumed, "should have received consumed items")
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
	loop.Stop()

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.Empty(t, result.CanceledItems)
}

func TestTurnLoop_StopCheckPointIDInCancelError(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	checkpointID := "turn-loop-cancel-ckpt-1"
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}

	slowModel := &cancelTestChatModel{
		delayNs: int64(500 * time.Millisecond),
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
		Store:        store,
		CheckpointID: checkpointID,
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
	loop.Stop()

	result := loop.Wait()

	var cancelErr *CancelError
	assert.True(t, errors.As(result.ExitReason, &cancelErr), "ExitReason should be a *CancelError")

	store.mu.Lock()
	defer store.mu.Unlock()
	_, ok := store.m[checkpointID]
	assert.True(t, ok, "checkpoint should be saved under the configured CheckpointID")
}

func TestTurnLoop_StopWithoutCheckpointIDDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}

	slowModel := &cancelTestChatModel{
		delayNs: int64(500 * time.Millisecond),
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
	loop.Stop()

	result := loop.Wait()

	var cancelErr *CancelError
	assert.True(t, errors.As(result.ExitReason, &cancelErr), "ExitReason should be a *CancelError")

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Empty(t, store.m, "no checkpoint should be saved when CheckpointID is not configured")
}

func TestTurnLoop_StopWhileIdle_SkipsCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := &deletableCheckpointStore{
		turnLoopCheckpointStore: turnLoopCheckpointStore{m: make(map[string][]byte)},
	}
	cpID := "idle-session"

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Stop()
	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)

	store.mu.Lock()
	defer store.mu.Unlock()
	_, exists := store.m[cpID]
	assert.False(t, exists, "no checkpoint should be saved when TurnLoop is idle")
}

func TestTurnLoop_StopBetweenTurnsAndResume(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "between-turns-session"

	loop := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
	loop.Run(ctx)

	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)

	var seen []string
	var mu sync.Mutex
	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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

	loop2.Push("c")
	loop2.Run(ctx)
	exit2 := loop2.Wait()
	assert.NoError(t, exit2.ExitReason)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a", "b", "c"}, seen)
}

func TestTurnLoop_StopDuringAgentExecution_PersistAndResume(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "mid-turn-session"

	slowModel := &cancelTestChatModel{
		delayNs: int64(500 * time.Millisecond),
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
		Store:        store,
		CheckpointID: cpID,
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
	loop.Stop()
	exit := loop.Wait()

	store.mu.Lock()
	_, ok := store.m[cpID]
	store.mu.Unlock()
	assert.True(t, ok)
	_ = exit

	slowModel.setDelay(10 * time.Millisecond)

	var consumed2 []string
	var genResumeCalled bool
	var genInputCalled bool
	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenResume: func(ctx context.Context, _ *TurnLoop[string], canceledItems []string, unhandledItems []string, newItems []string) (*GenResumeResult[string], error) {
			genResumeCalled = true
			return &GenResumeResult[string]{
				Consumed:  canceledItems,
				Remaining: append(append([]string{}, unhandledItems...), newItems...),
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

	loop2.Run(ctx)
	exit2 := loop2.Wait()
	assert.NoError(t, exit2.ExitReason)
	assert.Equal(t, []string{"msg1"}, consumed2)
	assert.True(t, genResumeCalled)
	assert.False(t, genInputCalled)
}

func TestTurnLoop_CheckpointIDWithoutStore_FreshStart(t *testing.T) {
	ctx := context.Background()
	var genInputCalled bool
	loop := NewTurnLoop(TurnLoopConfig[string]{
		CheckpointID: "some-id",
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			genInputCalled = true
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})
	loop.Push("a")
	loop.Run(ctx)
	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)
	assert.True(t, genInputCalled)
}

func TestTurnLoop_CheckpointNotFound_FreshStart(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	var genInputCalled bool
	loop := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: "nonexistent-id",
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			genInputCalled = true
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})
	loop.Push("a")
	loop.Run(ctx)
	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)
	assert.True(t, genInputCalled)
}

func TestTurnLoop_CheckpointEmptyData_TreatedAsNoCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	store.m["cp-empty"] = nil

	var genInputCalled bool
	loop := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: "cp-empty",
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			genInputCalled = true
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})
	loop.Push("a")
	loop.Run(ctx)
	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)
	assert.True(t, genInputCalled)
}

type errorCheckpointStore struct {
	getErr error
	setErr error
}

func (s *errorCheckpointStore) Get(_ context.Context, _ string) ([]byte, bool, error) {
	return nil, false, s.getErr
}

func (s *errorCheckpointStore) Set(_ context.Context, _ string, _ []byte) error {
	return s.setErr
}

func TestTurnLoop_CheckpointLoadError_ReturnsError(t *testing.T) {
	ctx := context.Background()
	store := &errorCheckpointStore{getErr: fmt.Errorf("store unavailable")}
	loop := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: "cp-1",
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop.Push("a")
	loop.Run(ctx)
	exit := loop.Wait()
	assert.Error(t, exit.ExitReason)
	assert.Contains(t, exit.ExitReason.Error(), "store unavailable")
}

func TestTurnLoop_CheckpointCorruptData_ReturnsError(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	store.m["cp-corrupt"] = []byte("not-valid-gob-data")
	loop := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: "cp-corrupt",
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop.Push("a")
	loop.Run(ctx)
	exit := loop.Wait()
	assert.Error(t, exit.ExitReason)
	assert.Contains(t, exit.ExitReason.Error(), "failed to unmarshal checkpoint")
}

func TestTurnLoop_CheckpointSaveError_ReturnsError(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	saveStore := &errorCheckpointStore{setErr: fmt.Errorf("write failed")}
	slowModel := &cancelTestChatModel{
		delayNs: int64(500 * time.Millisecond),
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
		Store:        saveStore,
		CheckpointID: "cp-1",
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
	loop.Stop()
	exit := loop.Wait()
	assert.Error(t, exit.ExitReason)
	assert.True(t, exit.Checkpointed)
	assert.Error(t, exit.CheckpointErr)
	assert.Contains(t, exit.CheckpointErr.Error(), "write failed")
}

func TestTurnLoop_StaleCheckpointDeletion_OnCleanResume(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "stale-session"

	loop1 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop1.Push("a")
	loop1.Stop()
	loop1.Run(ctx)
	loop1.Wait()

	store.mu.Lock()
	_, exists := store.m[cpID]
	store.mu.Unlock()
	assert.True(t, exists, "checkpoint should exist after first loop saves it")

	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})
	loop2.Push("b")
	loop2.Run(ctx)
	exit2 := loop2.Wait()
	assert.NoError(t, exit2.ExitReason)

	store.mu.Lock()
	_, exists = store.m[cpID]
	store.mu.Unlock()
	assert.True(t, exists, "checkpoint should still exist because loop2 was stopped and saved a new one")
}

func TestTurnLoop_StaleCheckpointDeletion_ContextCancel(t *testing.T) {
	ctx := context.Background()
	store := &deletableCheckpointStore{turnLoopCheckpointStore: turnLoopCheckpointStore{m: make(map[string][]byte)}}
	cpID := "delete-on-cancel"

	loop1 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop1.Push("a")
	loop1.Stop()
	loop1.Run(ctx)
	loop1.Wait()

	store.mu.Lock()
	_, exists := store.m[cpID]
	store.mu.Unlock()
	assert.True(t, exists, "checkpoint saved after loop1")

	ctx2, cancel2 := context.WithCancel(ctx)
	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			cancel2()
			return nil
		},
	})
	loop2.Push("b")
	loop2.Run(ctx2)
	exit2 := loop2.Wait()
	assert.ErrorIs(t, exit2.ExitReason, context.Canceled)

	store.mu.Lock()
	_, exists = store.m[cpID]
	deleteCalled := store.deleteCalled
	store.mu.Unlock()
	assert.True(t, deleteCalled && !exists, "stale checkpoint should be deleted when loop exits via context cancellation")
}

type deletableCheckpointStore struct {
	turnLoopCheckpointStore
	deleteCalled bool
	deletedKey   string
}

func (s *deletableCheckpointStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalled = true
	s.deletedKey = key
	delete(s.m, key)
	return nil
}

func TestTurnLoop_CheckpointDeleter_CalledOnContextCancel(t *testing.T) {
	ctx := context.Background()
	store := &deletableCheckpointStore{
		turnLoopCheckpointStore: turnLoopCheckpointStore{m: make(map[string][]byte)},
	}
	cpID := "deleter-session"

	loop1 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop1.Push("a")
	loop1.Stop()
	loop1.Run(ctx)
	loop1.Wait()

	store.mu.Lock()
	_, exists := store.m[cpID]
	store.mu.Unlock()
	assert.True(t, exists, "checkpoint saved after loop1")

	ctx2, cancel2 := context.WithCancel(ctx)
	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			cancel2()
			return nil
		},
	})
	loop2.Push("b")
	loop2.Run(ctx2)
	exit2 := loop2.Wait()
	assert.ErrorIs(t, exit2.ExitReason, context.Canceled)

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.True(t, store.deleteCalled, "CheckPointDeleter.Delete should be called")
	assert.Equal(t, cpID, store.deletedKey)
	_, exists = store.m[cpID]
	assert.False(t, exists, "checkpoint should be removed from store")
}

func TestTurnLoop_GenResumeNil_Error(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "resume-nil-session"
	modelStarted := make(chan struct{}, 1)

	slowModel := &cancelTestChatModel{
		delayNs: int64(500 * time.Millisecond),
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

	loop1 := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
	loop1.Push("msg1")
	<-modelStarted
	loop1.Stop()
	loop1.Wait()

	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop2.Run(ctx)
	exit2 := loop2.Wait()
	assert.Error(t, exit2.ExitReason)
	assert.Contains(t, exit2.ExitReason.Error(), "GenResume is required")
}

func TestTurnLoop_SameCheckpointID_OverwritePattern(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "overwrite-session"

	loop1 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop1.Push("a")
	loop1.Push("b")
	loop1.Stop()
	loop1.Run(ctx)
	loop1.Wait()

	store.mu.Lock()
	data1 := append([]byte{}, store.m[cpID]...)
	store.mu.Unlock()
	assert.NotEmpty(t, data1)

	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop2.Push("c")
	loop2.Stop()
	loop2.Run(ctx)
	loop2.Wait()

	store.mu.Lock()
	data2 := append([]byte{}, store.m[cpID]...)
	store.mu.Unlock()
	assert.NotEmpty(t, data2)
	assert.NotEqual(t, data1, data2, "checkpoint data should change because items are different")

	var seen []string
	var mu sync.Mutex
	loop3 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
				if _, ok := events.Next(); !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})
	loop3.Push("d")
	loop3.Run(ctx)
	exit3 := loop3.Wait()
	assert.NoError(t, exit3.ExitReason)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a", "b", "c", "d"}, seen, "should see loop2's unhandled items (a,b,c from loop2's checkpoint) plus new d")
}

func TestTurnLoop_CheckpointHasRunnerStateButEmptyBytes(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "empty-runner-bytes"

	cp := &turnLoopCheckpoint[string]{
		HasRunnerState:   true,
		RunnerCheckpoint: nil,
		UnhandledItems:   []string{"x"},
	}
	data, err := marshalTurnLoopCheckpoint(cp)
	assert.NoError(t, err)
	store.m[cpID] = data

	loop := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop.Push("a")
	loop.Run(ctx)
	exit := loop.Wait()
	assert.Error(t, exit.ExitReason)
	assert.Contains(t, exit.ExitReason.Error(), "has runner state but bytes are empty")
}

func TestTurnLoop_GenResumeReturnsError(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "resume-err-session"
	modelStarted := make(chan struct{}, 1)

	slowModel := &cancelTestChatModel{
		delayNs: int64(500 * time.Millisecond),
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

	loop1 := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
	loop1.Push("msg1")
	<-modelStarted
	loop1.Stop()
	loop1.Wait()

	genResumeErr := fmt.Errorf("resume callback failed")
	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GenResume: func(ctx context.Context, _ *TurnLoop[string], canceled, unhandled, newItems []string) (*GenResumeResult[string], error) {
			return nil, genResumeErr
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop2.Run(ctx)
	exit2 := loop2.Wait()
	assert.Error(t, exit2.ExitReason)
	assert.ErrorIs(t, exit2.ExitReason, genResumeErr)
}

func TestTurnLoop_CheckpointSaveError_MergesWithExistingError(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 1)
	saveStore := &errorCheckpointStore{setErr: fmt.Errorf("disk full")}
	slowModel := &cancelTestChatModel{
		delayNs: int64(500 * time.Millisecond),
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
		Store:        saveStore,
		CheckpointID: "cp-merge-err",
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
	loop.Stop()
	exit := loop.Wait()
	assert.Error(t, exit.ExitReason)
	var ce *CancelError
	assert.True(t, errors.As(exit.ExitReason, &ce), "ExitReason should be CancelError, not merged with checkpoint error")
	assert.True(t, exit.Checkpointed)
	assert.Error(t, exit.CheckpointErr)
	assert.Contains(t, exit.CheckpointErr.Error(), "disk full")
}

func TestTurnLoop_ResumeWithParams(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "resume-params-session"
	modelStarted := make(chan struct{}, 1)

	slowModel := &cancelTestChatModel{
		delayNs: int64(500 * time.Millisecond),
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

	loop1 := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
	loop1.Push("msg1")
	<-modelStarted
	loop1.Stop()
	exit1 := loop1.Wait()
	var ce *CancelError
	assert.True(t, errors.As(exit1.ExitReason, &ce))

	var resumeParamsUsed *ResumeParams
	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		GenResume: func(ctx context.Context, _ *TurnLoop[string], canceled, unhandled, newItems []string) (*GenResumeResult[string], error) {
			params := &ResumeParams{
				Targets: map[string]any{"some-address": "user-data"},
			}
			resumeParamsUsed = params
			return &GenResumeResult[string]{
				ResumeParams: params,
				Consumed:     append(append(canceled, unhandled...), newItems...),
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			tc.Loop.Stop()
			return nil
		},
	})
	loop2.Run(ctx)
	exit2 := loop2.Wait()
	assert.NotNil(t, resumeParamsUsed, "GenResume should have been called with ResumeParams")
	assert.Contains(t, resumeParamsUsed.Targets, "some-address")
	_ = exit2
}

func TestTurnLoop_Stop_EscalatesCancelMode(t *testing.T) {
	ctx := context.Background()
	agentStarted := make(chan *cancelContext, 1)
	probe := &turnLoopStopModeProbeAgent{ccCh: agentStarted}
	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return probe, nil
		},
	})

	loop.Push("msg1")
	cc := <-agentStarted

	loop.Stop(WithGracefulTimeout(10 * time.Second))
	loop.Stop(WithImmediate())

	deadline := time.After(1 * time.Second)
	for {
		if cc.getMode() == CancelImmediate {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cancel mode did not escalate to CancelImmediate")
		default:
		}
		time.Sleep(1 * time.Millisecond)
	}

	exit := loop.Wait()
	var ce *CancelError
	assert.True(t, errors.As(exit.ExitReason, &ce))
	assert.Equal(t, CancelImmediate, ce.Info.Mode)
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
	assert.Equal(t, int32(2), atomic.LoadInt32(&pushCount))
}

// Tests for NewTurnLoop: the permissive API where Push, Stop, and Wait are
// all valid on a not-yet-running loop.

func TestNewTurnLoop_PushBeforeRun(t *testing.T) {
	// Items pushed before Run are buffered and processed after Run starts.
	var processedItems []string
	var mu sync.Mutex

	loop := NewTurnLoop(TurnLoopConfig[string]{
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

	loop.Run(context.Background())

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
	loop := NewTurnLoop(TurnLoopConfig[string]{
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

	loop.Run(context.Background())
	result := loop.Wait()

	assert.NoError(t, result.ExitReason)
	assert.Equal(t, []string{"msg1", "msg2"}, result.UnhandledItems)
}

func TestNewTurnLoop_WaitBeforeRun(t *testing.T) {
	// Wait blocks until Run is called AND the loop exits.
	loop := NewTurnLoop(TurnLoopConfig[string]{
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
	loop.Run(context.Background())

	select {
	case result := <-waitDone:
		assert.NoError(t, result.ExitReason)
		assert.Equal(t, []string{"msg1"}, result.UnhandledItems)
	case <-time.After(1 * time.Second):
		t.Fatal("Wait did not return after Run + Stop")
	}
}

func TestNewTurnLoop_RunIsIdempotent(t *testing.T) {
	var genInputCalls int32

	loop := NewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			atomic.AddInt32(&genInputCalls, 1)
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("msg1")
	loop.Run(context.Background())
	loop.Run(context.Background())
	loop.Run(context.Background())

	time.Sleep(100 * time.Millisecond)

	loop.Stop()
	result := loop.Wait()

	assert.NoError(t, result.ExitReason)
	assert.True(t, atomic.LoadInt32(&genInputCalls) >= 1)
}

func TestNewTurnLoop_StopBeforeRun_ThenWait(t *testing.T) {
	// Demonstrates the full sequence: create, push, stop, run, wait.
	loop := NewTurnLoop(TurnLoopConfig[string]{
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
	loop.Run(context.Background())

	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.Equal(t, []string{"a", "b", "c"}, result.UnhandledItems)
}

func TestNewTurnLoop_ConcurrentPushAndRun(t *testing.T) {
	// Concurrent Push and Run should not race.
	for i := 0; i < 100; i++ {
		var count int32

		loop := NewTurnLoop(TurnLoopConfig[string]{
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
			loop.Run(context.Background())
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

	loop := NewTurnLoop(cfg)
	loop.Push("hello")
	loop.Run(context.Background())
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
	loop.Push("msg2", WithPreemptTimeout[string](AnySafePoint, time.Millisecond))

	select {
	case <-preemptedSeen:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("preempted channel was never observed in OnAgentEvents")
	}

	loop.Stop()
	loop.Wait()
}

// =============================================================================
// preemptSignal unit tests (direct testing of the hold/preempt/unhold mechanism)
// =============================================================================

func TestPreemptSignal_HoldCountLifecycle(t *testing.T) {
	s := newPreemptSignal()

	s.holdRunLoop()
	s.holdRunLoop()

	done := make(chan bool)
	go func() {
		preempted, _, _ := s.waitForPreemptOrUnhold()
		done <- preempted
	}()

	select {
	case <-done:
		t.Fatal("waitForPreemptOrUnhold should block while holdCount > 0")
	case <-time.After(50 * time.Millisecond):
	}

	s.unholdRunLoop()

	select {
	case <-done:
		t.Fatal("waitForPreemptOrUnhold should still block (holdCount=1)")
	case <-time.After(50 * time.Millisecond):
	}

	s.unholdRunLoop()

	select {
	case preempted := <-done:
		assert.False(t, preempted, "should return not-preempted when all holds released")
	case <-time.After(1 * time.Second):
		t.Fatal("waitForPreemptOrUnhold should unblock when holdCount reaches 0")
	}
}

func TestPreemptSignal_RequestPreemptWithNoHold(t *testing.T) {
	s := newPreemptSignal()

	ack := make(chan struct{})
	s.requestPreempt(ack)

	select {
	case <-ack:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ack should be closed immediately when holdCount is 0")
	}
}

func TestPreemptSignal_RequestPreemptWakesWaiter(t *testing.T) {
	s := newPreemptSignal()
	s.holdRunLoop()

	done := make(chan struct {
		preempted bool
		ackList   []chan struct{}
	})
	go func() {
		preempted, _, ackList := s.waitForPreemptOrUnhold()
		done <- struct {
			preempted bool
			ackList   []chan struct{}
		}{preempted, ackList}
	}()

	ack := make(chan struct{})
	s.requestPreempt(ack)

	select {
	case result := <-done:
		assert.True(t, result.preempted)
		assert.Len(t, result.ackList, 1)
		close(result.ackList[0])
	case <-time.After(1 * time.Second):
		t.Fatal("waitForPreemptOrUnhold should wake on requestPreempt")
	}
}

func TestPreemptSignal_HoldAndGetTurn(t *testing.T) {
	s := newPreemptSignal()
	s.setTurn(context.Background(), "turn-A")

	ctx, tc := s.holdAndGetTurn()
	assert.NotNil(t, ctx)
	assert.Equal(t, "turn-A", tc)

	s.endTurnAndUnhold()

	_, tc2 := s.holdAndGetTurn()
	assert.Nil(t, tc2, "TC should be nil after endTurnAndUnhold")
	s.unholdRunLoop()
}

func TestPreemptSignal_EndTurnPreservesSignalWhenHoldRemains(t *testing.T) {
	s := newPreemptSignal()

	s.holdRunLoop()
	s.holdRunLoop()

	ack := make(chan struct{})
	s.requestPreempt(ack)

	s.endTurnAndUnhold()

	done := make(chan bool)
	go func() {
		preempted, _, ackList := s.waitForPreemptOrUnhold()
		for _, a := range ackList {
			close(a)
		}
		done <- preempted
	}()

	select {
	case preempted := <-done:
		assert.True(t, preempted, "signal state should be preserved when holdCount > 0 after endTurnAndUnhold")
	case <-time.After(1 * time.Second):
		t.Fatal("waiter should see the preserved preempt signal")
	}

	select {
	case <-ack:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ack should have been closed")
	}
}

func TestPreemptSignal_ConcurrentHoldRequestUnhold(t *testing.T) {
	s := newPreemptSignal()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.holdRunLoop()
			ack := make(chan struct{})
			s.requestPreempt(ack)
			s.unholdRunLoop()
			<-ack
		}()
	}
	wg.Wait()
}

// =============================================================================
// Integration tests for race-prone preempt scenarios
// =============================================================================

func TestTurnLoop_ConcurrentPreemptsDuringTurn(t *testing.T) {
	agentStarted := make(chan struct{})
	agentStartedOnce := sync.Once{}

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentStartedOnce.Do(func() {
				close(agentStarted)
			})
			<-ctx.Done()
			return &AgentOutput{}, nil
		},
	}

	var genInputCount int32

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			atomic.AddInt32(&genInputCount, 1)
			return &GenInputResult[string]{
				Input:    &AgentInput{},
				Consumed: items,
			}, nil
		},
	})

	loop.Push("seed")

	select {
	case <-agentStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("agent did not start")
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, ack := loop.Push(fmt.Sprintf("urgent-%d", i), WithPreemptTimeout[string](AnySafePoint, time.Millisecond))
			if ok && ack != nil {
				select {
				case <-ack:
				case <-time.After(5 * time.Second):
					t.Error("ack channel not closed within timeout")
				}
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.True(t, atomic.LoadInt32(&genInputCount) >= 2, "should have had at least the initial turn + one preempted turn")
}

func TestTurnLoop_PreemptDuringTurnTransition(t *testing.T) {
	turnCount := int32(0)
	firstTurnDone := make(chan struct{})
	firstTurnOnce := sync.Once{}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "fast"}, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			count := atomic.AddInt32(&turnCount, 1)
			if count == 1 {
				firstTurnOnce.Do(func() {
					close(firstTurnDone)
				})
			}
			return &GenInputResult[string]{
				Input:    &AgentInput{},
				Consumed: items,
			}, nil
		},
	})

	loop.Push("first")

	select {
	case <-firstTurnDone:
	case <-time.After(1 * time.Second):
		t.Fatal("first turn did not start")
	}

	time.Sleep(50 * time.Millisecond)

	ok, ack := loop.Push("transitional", WithPreempt[string](AnySafePoint))
	assert.True(t, ok, "push should succeed")
	if ack != nil {
		select {
		case <-ack:
		case <-time.After(2 * time.Second):
			t.Fatal("ack should be closed even if preempt arrived during/after turn transition")
		}
	}

	time.Sleep(100 * time.Millisecond)

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.True(t, atomic.LoadInt32(&turnCount) >= 2, "transitional item should have been processed")
}

func TestTurnLoop_PushStrategy_DuringTurnTransition(t *testing.T) {
	agentStarted := make(chan struct{})
	agentStartedOnce := sync.Once{}
	allowFinish := make(chan struct{})

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentStartedOnce.Do(func() {
				close(agentStarted)
			})
			select {
			case <-allowFinish:
				return &AgentOutput{}, nil
			case <-ctx.Done():
				return &AgentOutput{}, nil
			}
		},
	}

	var genInputCount int32
	secondTurnDone := make(chan struct{})
	secondTurnOnce := sync.Once{}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			count := atomic.AddInt32(&genInputCount, 1)
			if count >= 2 {
				secondTurnOnce.Do(func() {
					close(secondTurnDone)
				})
			}
			return &GenInputResult[string]{
				Input:    &AgentInput{},
				Consumed: items,
			}, nil
		},
	})

	loop.Push("first")

	select {
	case <-agentStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("agent did not start")
	}

	strategyBlocker := make(chan struct{})
	var strategyTCNotNil int32

	go func() {
		loop.Push("strategic-item", WithPushStrategy(func(ctx context.Context, tc *TurnContext[string]) []PushOption[string] {
			if tc != nil {
				atomic.StoreInt32(&strategyTCNotNil, 1)
			}
			<-strategyBlocker
			return []PushOption[string]{WithPreempt[string](AnySafePoint)}
		}))
	}()

	time.Sleep(50 * time.Millisecond)
	close(allowFinish)
	time.Sleep(50 * time.Millisecond)
	close(strategyBlocker)

	select {
	case <-secondTurnDone:
	case <-time.After(3 * time.Second):
		t.Fatal("second turn should eventually run after strategy resolves")
	}

	loop.Stop()
	result := loop.Wait()
	assert.NoError(t, result.ExitReason)
	assert.True(t, atomic.LoadInt32(&genInputCount) >= 2)
}

func TestTurnLoop_ConcurrentPreemptAndStop(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		t.Run(fmt.Sprintf("iter_%d", iter), func(t *testing.T) {
			ctx := context.Background()

			agentStarted := make(chan struct{})
			agentStartedOnce := sync.Once{}

			agent := &turnLoopCancellableMockAgent{
				name: "test",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					agentStartedOnce.Do(func() {
						close(agentStarted)
					})
					<-ctx.Done()
					return &AgentOutput{}, nil
				},
			}

			loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
				PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
					return agent, nil
				},
				GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
					return &GenInputResult[string]{
						Input:    &AgentInput{},
						Consumed: items,
					}, nil
				},
			})

			loop.Push("seed")

			select {
			case <-agentStarted:
			case <-time.After(1 * time.Second):
				t.Fatal("agent did not start")
			}

			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				_, ack := loop.Push("preempt-item", WithPreempt[string](AnySafePoint))
				if ack != nil {
					<-ack
				}
			}()

			go func() {
				defer wg.Done()
				loop.Stop()
			}()

			wg.Wait()
			loop.Wait()
		})
	}
}

func TestTurnLoop_ConcurrentPushStrategyAndStop(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		t.Run(fmt.Sprintf("iter_%d", iter), func(t *testing.T) {
			ctx := context.Background()

			agentStarted := make(chan struct{})
			agentStartedOnce := sync.Once{}

			agent := &turnLoopCancellableMockAgent{
				name: "test",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					agentStartedOnce.Do(func() {
						close(agentStarted)
					})
					<-ctx.Done()
					return &AgentOutput{}, nil
				},
			}

			loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
				PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
					return agent, nil
				},
				GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
					return &GenInputResult[string]{
						Input:    &AgentInput{},
						Consumed: items,
					}, nil
				},
			})

			loop.Push("seed")

			select {
			case <-agentStarted:
			case <-time.After(1 * time.Second):
				t.Fatal("agent did not start")
			}

			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				_, ack := loop.Push("strategic-item", WithPushStrategy(func(ctx context.Context, tc *TurnContext[string]) []PushOption[string] {
					return []PushOption[string]{WithPreempt[string](AnySafePoint)}
				}))
				if ack != nil {
					<-ack
				}
			}()

			go func() {
				defer wg.Done()
				loop.Stop()
			}()

			wg.Wait()
			loop.Wait()
		})
	}
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
	loop.Stop()

	select {
	case <-stoppedSeen:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("stopped channel was never observed in OnAgentEvents")
	}

	loop.Wait()
}

func TestTurnLoop_PushStrategy_DuringTurn(t *testing.T) {
	agentStarted := make(chan struct{})
	agentStartedOnce := sync.Once{}
	agentCancelled := make(chan struct{})
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

	// Strategy inspects TurnContext during a running turn and decides to preempt.
	var strategyCalled int32
	var strategyTC *TurnContext[string]
	loop.Push("urgent", WithPushStrategy(func(ctx context.Context, tc *TurnContext[string]) []PushOption[string] {
		atomic.AddInt32(&strategyCalled, 1)
		strategyTC = tc
		return []PushOption[string]{WithPreempt[string](AnySafePoint)}
	}))

	select {
	case <-agentCancelled:
	case <-time.After(1 * time.Second):
		t.Fatal("agent was not cancelled by strategy-returned preempt")
	}

	select {
	case <-secondGenInputCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("second GenInput was not called after preempt")
	}

	loop.Stop()
	loop.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&strategyCalled))
	assert.NotNil(t, strategyTC, "strategy should receive non-nil TurnContext during a turn")
	assert.Equal(t, []string{"first"}, strategyTC.Consumed)
}

func TestTurnLoop_PushStrategy_BetweenTurns(t *testing.T) {
	// Push with strategy before Run() — TurnContext should be nil.
	var strategyCalled int32
	var strategyTCWasNil bool

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			return &AgentOutput{}, nil
		},
	}

	agentDone := make(chan struct{})
	agentDoneOnce := sync.Once{}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  items,
				Remaining: nil,
			}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			agentDoneOnce.Do(func() {
				close(agentDone)
			})
			return nil
		},
	})

	// Push with strategy — no turn is active yet, so tc should be nil.
	loop.Push("item", WithPushStrategy(func(ctx context.Context, tc *TurnContext[string]) []PushOption[string] {
		atomic.AddInt32(&strategyCalled, 1)
		strategyTCWasNil = (tc == nil)
		return nil // plain push, no preempt
	}))

	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not complete")
	}

	loop.Stop()
	loop.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&strategyCalled))
	assert.True(t, strategyTCWasNil, "strategy should receive nil TurnContext between turns")
}

func TestTurnLoop_PushStrategy_OverridesOtherOptions(t *testing.T) {
	// Push with both WithPreempt and WithPushStrategy — only strategy's result applies.
	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			return &AgentOutput{}, nil
		},
	}

	agentDone := make(chan struct{})
	agentDoneOnce := sync.Once{}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  items,
				Remaining: nil,
			}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			agentDoneOnce.Do(func() {
				close(agentDone)
			})
			return nil
		},
	})

	// Strategy returns nil (no preempt), even though WithPreempt is also passed.
	// The strategy should override — so the agent should NOT be preempted.
	ok, ack := loop.Push("item", WithPreempt[string](AnySafePoint), WithPushStrategy(func(ctx context.Context, tc *TurnContext[string]) []PushOption[string] {
		return nil // no preempt
	}))
	assert.True(t, ok)
	assert.Nil(t, ack, "ack should be nil since strategy returned no preempt")

	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not complete normally")
	}

	loop.Stop()
	loop.Wait()
}

func TestTurnLoop_PushStrategy_NestedStrategyStripped(t *testing.T) {
	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			return &AgentOutput{}, nil
		},
	}

	agentDone := make(chan struct{})
	agentDoneOnce := sync.Once{}

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  items,
				Remaining: nil,
			}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				_, ok := events.Next()
				if !ok {
					break
				}
			}
			agentDoneOnce.Do(func() {
				close(agentDone)
			})
			return nil
		},
	})

	// Strategy returns another WithPushStrategy — the nested one should be stripped.
	innerCalled := int32(0)
	ok, ack := loop.Push("item", WithPushStrategy(func(ctx context.Context, tc *TurnContext[string]) []PushOption[string] {
		return []PushOption[string]{
			WithPushStrategy(func(ctx context.Context, tc *TurnContext[string]) []PushOption[string] {
				atomic.AddInt32(&innerCalled, 1)
				return []PushOption[string]{WithPreempt[string](AnySafePoint)}
			}),
		}
	}))
	assert.True(t, ok)
	assert.Nil(t, ack, "ack should be nil since nested strategy was stripped (no preempt)")

	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not complete normally")
	}

	loop.Stop()
	loop.Wait()

	assert.Equal(t, int32(0), atomic.LoadInt32(&innerCalled), "nested strategy should not be called")
}

func TestTurnLoop_PushStrategy_ConsumedInspection(t *testing.T) {
	// Strategy preempts only when current turn is processing "low-priority" items.
	agentStarted := make(chan struct{})
	agentStartedOnce := sync.Once{}

	agent := &turnLoopCancellableMockAgent{
		name: "test",
		runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
			agentStartedOnce.Do(func() {
				close(agentStarted)
			})
			<-ctx.Done()
			return &AgentOutput{}, nil
		},
	}

	genInputCalls := int32(0)
	secondGenInputItems := make(chan []string, 1)

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return agent, nil
		},
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			count := atomic.AddInt32(&genInputCalls, 1)
			if count >= 2 {
				select {
				case secondGenInputItems <- append([]string{}, items...):
				default:
				}
			}
			return &GenInputResult[string]{
				Input:     &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed:  []string{items[0]},
				Remaining: items[1:],
			}, nil
		},
	})

	loop.Push("low-priority-task")

	select {
	case <-agentStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("agent did not start")
	}

	// Strategy checks Consumed and preempts because current turn has "low-priority" items.
	loop.Push("urgent-task", WithPushStrategy(func(ctx context.Context, tc *TurnContext[string]) []PushOption[string] {
		if tc != nil && len(tc.Consumed) > 0 && tc.Consumed[0] == "low-priority-task" {
			return []PushOption[string]{WithPreempt[string](AnySafePoint)}
		}
		return nil
	}))

	select {
	case items := <-secondGenInputItems:
		assert.Contains(t, items, "urgent-task")
	case <-time.After(2 * time.Second):
		t.Fatal("second GenInput was not called after strategy-driven preempt")
	}

	loop.Stop()
	loop.Wait()
}

func TestTurnLoop_PushAfterStop_BufferedAsLateItems(t *testing.T) {
	ctx := context.Background()
	processed := make(chan string, 10)

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
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
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			processed <- tc.Consumed[0]
			return nil
		},
	})

	loop.Push("msg1")
	<-processed
	loop.Stop()
	result := loop.Wait()

	// Push after stop — should be buffered as late items
	ok1, _ := loop.Push("late1")
	ok2, _ := loop.Push("late2")
	ok3, _ := loop.Push("late3")
	assert.False(t, ok1)
	assert.False(t, ok2)
	assert.False(t, ok3)

	late := result.TakeLateItems()
	assert.Equal(t, []string{"late1", "late2", "late3"}, late)
}

func TestTurnLoop_TakeLateItems_Idempotent(t *testing.T) {
	ctx := context.Background()

	loop := NewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop.Push("a")
	loop.Stop()
	loop.Run(ctx)
	result := loop.Wait()

	loop.Push("late1")

	first := result.TakeLateItems()
	second := result.TakeLateItems()
	third := result.TakeLateItems()

	assert.Equal(t, []string{"late1"}, first)
	assert.Equal(t, first, second, "subsequent calls should return the same slice")
	assert.Equal(t, first, third, "subsequent calls should return the same slice")
}

func TestTurnLoop_PushAfterTakeLateItems_Panics(t *testing.T) {
	ctx := context.Background()

	loop := NewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop.Push("a")
	loop.Stop()
	loop.Run(ctx)
	result := loop.Wait()

	result.TakeLateItems()

	assert.PanicsWithValue(t, "TurnLoop: Push called after TakeLateItems", func() {
		loop.Push("too-late")
	})
}

func TestTurnLoop_TakeLateItems_NeverCalled_NoImpact(t *testing.T) {
	ctx := context.Background()

	loop := NewTurnLoop(TurnLoopConfig[string]{
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
	loop.Run(ctx)
	result := loop.Wait()

	// Don't call TakeLateItems — verify UnhandledItems works normally
	assert.Contains(t, result.UnhandledItems, "b")
	assert.Nil(t, result.ExitReason)
}

func TestTurnLoop_CheckpointErr_SeparateFromExitReason(t *testing.T) {
	ctx := context.Background()
	saveStore := &errorCheckpointStore{setErr: fmt.Errorf("storage unavailable")}

	loop := NewTurnLoop(TurnLoopConfig[string]{
		Store:        saveStore,
		CheckpointID: "cp-separate-err",
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop.Push("a")
	loop.Stop()
	loop.Run(ctx)
	result := loop.Wait()

	// ExitReason should be nil (clean stop), checkpoint error should be separate
	assert.Nil(t, result.ExitReason)
	assert.True(t, result.Checkpointed)
	assert.Error(t, result.CheckpointErr)
	assert.Contains(t, result.CheckpointErr.Error(), "storage unavailable")
}

func TestTurnLoop_Checkpointed_FalseWhenNoStore(t *testing.T) {
	ctx := context.Background()

	loop := NewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop.Push("a")
	loop.Stop()
	loop.Run(ctx)
	result := loop.Wait()

	assert.False(t, result.Checkpointed)
	assert.Nil(t, result.CheckpointErr)
}

func TestTurnLoop_Checkpointed_FalseOnErrorExit(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	genInputErr := errors.New("gen input failed")

	firstTurnDone := make(chan struct{})
	var callCount int32
	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: "cp-err-exit",
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			n := atomic.AddInt32(&callCount, 1)
			if n > 1 {
				return nil, genInputErr
			}
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			close(firstTurnDone)
			return nil
		},
	})
	loop.Push("msg1")
	<-firstTurnDone
	loop.Push("msg2")
	result := loop.Wait()

	// Loop exited from error, not Stop() — checkpoint should not be saved
	assert.ErrorIs(t, result.ExitReason, genInputErr)
	assert.False(t, result.Checkpointed)
	assert.Nil(t, result.CheckpointErr)
}

func TestTurnLoop_StopConcurrentWithCallbackError_NoCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "stop-concurrent-err"

	prepareErr := errors.New("prepare agent failed")
	firstTurnDone := make(chan struct{})
	stopCalled := make(chan struct{})
	var prepareCount int32

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{
				Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			n := atomic.AddInt32(&prepareCount, 1)
			if n > 1 {
				// Wait until Stop() has been called so stopSig.isStopped() is true
				<-stopCalled
				return nil, prepareErr
			}
			return &turnLoopMockAgent{name: "test"}, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *TurnContext[string], events *AsyncIterator[*AgentEvent]) error {
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			close(firstTurnDone)
			return nil
		},
	})

	loop.Push("msg1")
	<-firstTurnDone
	loop.Push("msg2")

	// Call Stop() and signal PrepareAgent to proceed with error
	go func() {
		loop.Stop()
		close(stopCalled)
	}()

	result := loop.Wait()

	// The loop may exit via Stop (clean) or via PrepareAgent error.
	// If it exited via PrepareAgent error with Stop also called:
	// checkpoint should NOT be saved.
	if result.ExitReason != nil && !errors.As(result.ExitReason, new(*CancelError)) {
		assert.ErrorIs(t, result.ExitReason, prepareErr)
		assert.False(t, result.Checkpointed, "should not checkpoint when exit is caused by callback error")
	}
	// If Stop won the race, that's fine — checkpoint may or may not be saved
	// depending on idle state. The test is about the error path.
}

func TestTurnLoop_DeleteWithoutCheckPointDeleter_NoOp(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "no-deleter"

	// First loop: save a checkpoint
	loop1 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop1.Push("a")
	loop1.Stop()
	loop1.Run(ctx)
	loop1.Wait()

	store.mu.Lock()
	_, exists := store.m[cpID]
	store.mu.Unlock()
	assert.True(t, exists, "checkpoint should be saved")

	// Second loop: exit via context cancel — should try to delete but store
	// doesn't implement CheckPointDeleter, so checkpoint persists (no-op)
	ctx2, cancel2 := context.WithCancel(ctx)
	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
			for {
				if _, ok := events.Next(); !ok {
					break
				}
			}
			cancel2()
			return nil
		},
	})
	loop2.Push("b")
	loop2.Run(ctx2)
	loop2.Wait()

	// Without CheckPointDeleter, the stale checkpoint should NOT be deleted
	store.mu.Lock()
	v, exists := store.m[cpID]
	store.mu.Unlock()
	assert.True(t, exists, "checkpoint should still exist without CheckPointDeleter")
	assert.NotNil(t, v, "checkpoint should not be set to nil")
}

func TestTurnLoop_StopWithSkipCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "skip-cp-session"

	loop := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("a")
	loop.Push("b")
	loop.Stop(WithSkipCheckpoint())
	loop.Run(ctx)

	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)
	assert.False(t, exit.Checkpointed, "checkpoint should be skipped when WithSkipCheckpoint is used")

	store.mu.Lock()
	_, exists := store.m[cpID]
	store.mu.Unlock()
	assert.False(t, exists, "no checkpoint should be saved when WithSkipCheckpoint is used")
}

func TestTurnLoop_StopWithSkipCheckpoint_DeletesStaleCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := &deletableCheckpointStore{
		turnLoopCheckpointStore: turnLoopCheckpointStore{m: make(map[string][]byte)},
	}
	cpID := "skip-stale-session"

	loop1 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop1.Push("a")
	loop1.Stop()
	loop1.Run(ctx)
	exit1 := loop1.Wait()
	assert.True(t, exit1.Checkpointed)

	store.mu.Lock()
	_, exists := store.m[cpID]
	store.mu.Unlock()
	assert.True(t, exists, "first loop should save checkpoint")

	loop2 := NewTurnLoop(TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})
	loop2.Push("b")
	loop2.Stop(WithSkipCheckpoint())
	loop2.Run(ctx)
	exit2 := loop2.Wait()
	assert.False(t, exit2.Checkpointed, "second loop should skip checkpoint")

	store.mu.Lock()
	deleteCalled := store.deleteCalled
	store.mu.Unlock()
	assert.True(t, deleteCalled, "stale checkpoint should be deleted when SkipCheckpoint is used")
}

func TestTurnLoop_StopWithStopCause(t *testing.T) {
	ctx := context.Background()
	cause := "user session timeout"

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Push("a")
	loop.Stop(WithStopCause(cause))

	exit := loop.Wait()
	assert.Equal(t, cause, exit.StopCause)
}

func TestTurnLoop_StopCause_EmptyWhenNoStop(t *testing.T) {
	ctx := context.Background()

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return &GenInputResult[string]{Input: &AgentInput{}, Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			return &turnLoopMockAgent{name: "test"}, nil
		},
	})

	loop.Stop()
	exit := loop.Wait()
	assert.Empty(t, exit.StopCause)
}

func TestTurnLoop_StopCause_InTurnContext(t *testing.T) {
	cause := "business shutdown"
	gotCause := make(chan string, 1)
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
				gotCause <- tc.StopCause()
			case <-time.After(5 * time.Second):
				t.Error("timed out waiting for Stopped channel")
			}
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
	loop.Stop(WithStopCause(cause))

	select {
	case c := <-gotCause:
		assert.Equal(t, cause, c)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for StopCause in TurnContext")
	}

	exit := loop.Wait()
	assert.Equal(t, cause, exit.StopCause)
}

func TestTurnLoop_StopCause_FirstNonEmptyWins(t *testing.T) {
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
	loop.Stop(WithGraceful(), WithStopCause("first cause"))
	loop.Stop(WithStopCause("second cause"))

	exit := loop.Wait()
	assert.Equal(t, "first cause", exit.StopCause, "first non-empty StopCause should win")
}

func TestTurnLoop_StopBeforeRun_PushThenStop(t *testing.T) {
	loop := NewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			t.Fatal("GenInput should not be called when Stop is called before Run")
			return nil, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			t.Fatal("PrepareAgent should not be called when Stop is called before Run")
			return nil, nil
		},
	})

	ok, _ := loop.Push("item1")
	assert.True(t, ok)
	ok, _ = loop.Push("item2")
	assert.True(t, ok)

	loop.Stop()
	loop.Run(context.Background())
	result := loop.Wait()

	assert.NoError(t, result.ExitReason)
	assert.Equal(t, []string{"item1", "item2"}, result.UnhandledItems)
	assert.Empty(t, result.CanceledItems)
	assert.Empty(t, result.TakeLateItems())
}

func TestTurnLoop_StopBeforeRun_StopThenPush(t *testing.T) {
	loop := NewTurnLoop(TurnLoopConfig[string]{
		GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			t.Fatal("GenInput should not be called when Stop is called before Run")
			return nil, nil
		},
		PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
			t.Fatal("PrepareAgent should not be called when Stop is called before Run")
			return nil, nil
		},
	})

	loop.Stop()

	ok, _ := loop.Push("item1")
	assert.False(t, ok)
	ok, _ = loop.Push("item2")
	assert.False(t, ok)

	loop.Run(context.Background())
	result := loop.Wait()

	assert.NoError(t, result.ExitReason)
	assert.Empty(t, result.UnhandledItems)
	assert.Empty(t, result.CanceledItems)
	assert.Equal(t, []string{"item1", "item2"}, result.TakeLateItems())
}

func TestTurnLoop_SkipCheckpoint_Sticky(t *testing.T) {
	agentStarted := make(chan struct{})

	store := &turnLoopCheckpointStore{m: make(map[string][]byte)}
	cpID := "sticky-skip-session"

	loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
		Store:        store,
		CheckpointID: cpID,
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
	loop.Stop(WithGraceful(), WithSkipCheckpoint())
	loop.Stop()

	exit := loop.Wait()
	assert.False(t, exit.Checkpointed, "SkipCheckpoint should be sticky across multiple Stop calls")

	store.mu.Lock()
	_, exists := store.m[cpID]
	store.mu.Unlock()
	assert.False(t, exists, "no checkpoint should be saved when SkipCheckpoint was set in any Stop call")
}

func TestWithGracefulTimeout_NonPositive_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "adk: WithGracefulTimeout: gracePeriod must be positive",
		func() { WithGracefulTimeout(0) })
	assert.PanicsWithValue(t, "adk: WithGracefulTimeout: gracePeriod must be positive",
		func() { WithGracefulTimeout(-1 * time.Second) })
}

func TestWithPreempt_ZeroSafePoint_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "adk: SafePoint must not be zero; use AfterToolCalls, AfterChatModel, or AnySafePoint",
		func() { WithPreempt[string](SafePoint(0)) })
}

func TestWithPreemptTimeout_ZeroSafePoint_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "adk: SafePoint must not be zero; use AfterToolCalls, AfterChatModel, or AnySafePoint",
		func() { WithPreemptTimeout[string](SafePoint(0), time.Second) })
}

func TestSafePoint_ToCancelMode(t *testing.T) {
	assert.Equal(t, CancelAfterToolCalls, AfterToolCalls.toCancelMode())
	assert.Equal(t, CancelAfterChatModel, AfterChatModel.toCancelMode())
	assert.Equal(t, CancelAfterToolCalls|CancelAfterChatModel, AnySafePoint.toCancelMode())
}

func TestNewTurnLoop_NilGenInput_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "adk: NewTurnLoop: GenInput is required", func() {
		NewTurnLoop(TurnLoopConfig[string]{PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) { return nil, nil }})
	})
}

func TestNewTurnLoop_NilPrepareAgent_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "adk: NewTurnLoop: PrepareAgent is required", func() {
		NewTurnLoop(TurnLoopConfig[string]{GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
			return nil, nil
		}})
	})
}

func TestDeriveChild_NilParent_ReturnsNil(t *testing.T) {
	var cc *cancelContext
	assert.Nil(t, cc.deriveChild(context.Background()))
}

func TestUntilIdleFor(t *testing.T) {
	t.Run("FiresAfterIdleDuration", func(t *testing.T) {
		turnDone := make(chan struct{})
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
						close(turnDone)
						return &AgentOutput{}, nil
					},
				}, nil
			},
		})

		loop.Push("msg1")
		<-turnDone

		loop.Stop(UntilIdleFor(50 * time.Millisecond))

		done := make(chan struct{})
		go func() {
			loop.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("loop did not exit after idle timeout")
		}
	})

	t.Run("ResetsOnPush", func(t *testing.T) {
		turnCount := int32(0)
		turnDone := make(chan struct{}, 10)
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
						atomic.AddInt32(&turnCount, 1)
						turnDone <- struct{}{}
						return &AgentOutput{}, nil
					},
				}, nil
			},
		})

		loop.Push("msg1")
		<-turnDone

		loop.Stop(UntilIdleFor(200 * time.Millisecond))

		time.Sleep(100 * time.Millisecond)
		loop.Push("msg2")
		<-turnDone

		done := make(chan struct{})
		go func() {
			loop.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("loop did not exit after idle timeout")
		}

		assert.Equal(t, int32(2), atomic.LoadInt32(&turnCount))
	})

	t.Run("EscalatedByStopWithImmediate", func(t *testing.T) {
		agentStarted := make(chan *cancelContext, 1)
		probe := &turnLoopStopModeProbeAgent{ccCh: agentStarted}
		loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
			GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
				return &GenInputResult[string]{
					Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
					Consumed: items,
				}, nil
			},
			PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
				return probe, nil
			},
		})

		loop.Push("msg1")
		cc := <-agentStarted

		loop.Stop(UntilIdleFor(10 * time.Minute))
		loop.Stop(WithImmediate())

		deadline := time.After(2 * time.Second)
		for {
			if cc.getMode() == CancelImmediate {
				break
			}
			select {
			case <-deadline:
				t.Fatal("cancel mode did not escalate to CancelImmediate")
			default:
			}
			time.Sleep(1 * time.Millisecond)
		}

		exit := loop.Wait()
		var ce *CancelError
		assert.True(t, errors.As(exit.ExitReason, &ce))
		assert.Equal(t, CancelImmediate, ce.Info.Mode)
	})

	t.Run("EscalatedByStopWithGraceful", func(t *testing.T) {
		agentStarted := make(chan struct{})
		agentDone := make(chan struct{})
		loop := newAndRunTurnLoop(context.Background(), TurnLoopConfig[string]{
			GenInput: func(ctx context.Context, _ *TurnLoop[string], items []string) (*GenInputResult[string], error) {
				return &GenInputResult[string]{
					Input:    &AgentInput{Messages: []Message{schema.UserMessage(items[0])}},
					Consumed: items,
				}, nil
			},
			PrepareAgent: func(ctx context.Context, _ *TurnLoop[string], consumed []string) (Agent, error) {
				return &turnLoopCancellableMockAgent{
					name: "test",
					runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
						close(agentStarted)
						<-ctx.Done()
						close(agentDone)
						return nil, ctx.Err()
					},
				}, nil
			},
		})

		loop.Push("msg1")
		<-agentStarted

		loop.Stop(UntilIdleFor(10 * time.Minute))
		loop.Stop(WithGracefulTimeout(50 * time.Millisecond))

		select {
		case <-agentDone:
		case <-time.After(2 * time.Second):
			t.Fatal("agent was not cancelled")
		}

		exit := loop.Wait()
		assert.Error(t, exit.ExitReason)
	})
}

func TestUntilIdleFor_NonPositive_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "adk: UntilIdleFor: duration must be positive",
		func() { UntilIdleFor(0) })
	assert.PanicsWithValue(t, "adk: UntilIdleFor: duration must be positive",
		func() { UntilIdleFor(-1 * time.Second) })
}
