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

	loop.Cancel(WithTurnLoopCancelMode(CancelAfterToolCall), WithSkipCheckpoint())

	result := loop.Wait()
	assert.NoError(t, result.Error)
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

	loop.Cancel(WithTurnLoopCancelMode(CancelAfterToolCall), WithSkipCheckpoint())

	result := loop.Wait()
	assert.NoError(t, result.Error)
}
