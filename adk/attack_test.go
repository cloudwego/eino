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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestAttack_UntilIdleFor_ConcurrentPushDuringIdleTimer(t *testing.T) {
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

	for i := 0; i < 5; i++ {
		time.Sleep(50 * time.Millisecond)
		loop.Push("concurrent-" + string(rune('a'+i)))
		<-turnDone
	}

	done := make(chan struct{})
	go func() {
		loop.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("loop did not exit after idle timeout — Push did not reset timer correctly")
	}

	finalCount := atomic.LoadInt32(&turnCount)
	assert.Equal(t, int32(6), finalCount, "all 6 pushes should have been processed")
}

func TestAttack_UntilIdleFor_MultipleStopCallsFirstWins(t *testing.T) {
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

	loop.Stop(UntilIdleFor(100 * time.Millisecond))
	loop.Stop(UntilIdleFor(10 * time.Minute))

	done := make(chan struct{})
	go func() {
		loop.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second UntilIdleFor should have been ignored; loop should have exited with 100ms timer")
	}
}

func TestAttack_BareStopOverridesUntilIdleFor(t *testing.T) {
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
			return &turnLoopMockAgent{
				name: "test",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					close(agentStarted)
					<-agentDone
					return &AgentOutput{}, nil
				},
			}, nil
		},
	})

	loop.Push("msg1")
	<-agentStarted

	loop.Stop(UntilIdleFor(10 * time.Minute))

	loop.Stop()
	close(agentDone)

	done := make(chan struct{})
	go func() {
		loop.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bare Stop should override UntilIdleFor and cause immediate shutdown")
	}

	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason, "bare Stop should exit cleanly")
}

func TestAttack_StopSignal_NilCancelOptsDoNotDeescalate(t *testing.T) {
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

	loop.Stop(WithImmediate())

	time.Sleep(20 * time.Millisecond)

	loop.Stop()

	time.Sleep(20 * time.Millisecond)
	mode := cc.getMode()
	assert.Equal(t, CancelImmediate, mode, "bare Stop after WithImmediate must not de-escalate cancel mode")

	exit := loop.Wait()
	var ce *CancelError
	require.True(t, errors.As(exit.ExitReason, &ce))
	assert.Equal(t, CancelImmediate, ce.Info.Mode)
}

func TestAttack_CanceledItems_EmptyWhenAgentFinishesNormally(t *testing.T) {
	agentStarted := make(chan struct{})
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
					close(agentStarted)
					return &AgentOutput{}, nil
				},
			}, nil
		},
	})

	loop.Push("msg1")
	<-agentStarted
	time.Sleep(50 * time.Millisecond)
	loop.Stop()

	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)
	assert.Empty(t, exit.CanceledItems, "CanceledItems must be empty when agent finished normally")
}

func TestAttack_TurnBuffer_WakeupDoesNotLoseItems(t *testing.T) {
	tb := newTurnBuffer[string]()

	tb.Send("a")
	tb.Send("b")
	tb.Wakeup()
	tb.Send("c")

	var got []string
	for i := 0; i < 3; i++ {
		val, ok := tb.Receive()
		require.True(t, ok)
		got = append(got, val)
	}

	assert.Equal(t, []string{"a", "b", "c"}, got, "Wakeup must not cause items to be lost")
}

func TestAttack_TurnBuffer_ClearWakeupPreventsSpuriousReturn(t *testing.T) {
	tb := newTurnBuffer[string]()

	tb.Wakeup()
	tb.ClearWakeup()

	received := make(chan string, 1)
	go func() {
		val, ok := tb.Receive()
		if ok {
			received <- val
		}
	}()

	time.Sleep(50 * time.Millisecond)
	tb.Send("real")

	select {
	case val := <-received:
		assert.Equal(t, "real", val, "ClearWakeup should prevent spurious empty return")
	case <-time.After(2 * time.Second):
		t.Fatal("Receive blocked forever despite Send")
	}
}

func TestAttack_StopBeforeRun_UntilIdleFor_ExitsImmediately(t *testing.T) {
	loop := NewTurnLoop(TurnLoopConfig[string]{
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

	loop.Stop(UntilIdleFor(10 * time.Minute))
	loop.Stop()

	loop.Run(context.Background())

	done := make(chan struct{})
	go func() {
		loop.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop should exit immediately when Stop() called before Run()")
	}
}

func TestAttack_PushAfterStop_UntilIdleFor_RoutedToLateItems(t *testing.T) {
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
	exit := loop.Wait()
	assert.NoError(t, exit.ExitReason)

	ok, _ := loop.Push("after-stop")
	assert.False(t, ok, "Push after loop exited should return false")

	late := exit.TakeLateItems()
	assert.Equal(t, []string{"after-stop"}, late)
}

func TestAttack_ConcurrentStopEscalation_RaceDetector(t *testing.T) {
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
				name: "test",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					close(agentStarted)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}, nil
		},
	})

	loop.Push("msg1")
	<-agentStarted

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0:
				loop.Stop()
			case 1:
				loop.Stop(WithImmediate())
			case 2:
				loop.Stop(WithGracefulTimeout(100 * time.Millisecond))
			case 3:
				loop.Stop(UntilIdleFor(50 * time.Millisecond))
			}
		}(i)
	}

	wg.Wait()
	exit := loop.Wait()
	t.Log("ExitReason:", exit.ExitReason)
}

func TestAttack_StopCause_FirstNonEmptyWins_ConcurrentCallers(t *testing.T) {
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

	loop.Stop(WithStopCause("first-cause"))
	loop.Stop(WithStopCause("second-cause"))

	exit := loop.Wait()
	assert.Equal(t, "first-cause", exit.StopCause, "first non-empty StopCause should win")
}

func TestAttack_SkipCheckpoint_Sticky(t *testing.T) {
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
				name: "test",
				runFunc: func(ctx context.Context, input *AgentInput) (*AgentOutput, error) {
					close(agentStarted)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}, nil
		},
		Store:        &turnLoopCheckpointStore{m: make(map[string][]byte)},
		CheckpointID: "test-sticky",
	})

	loop.Push("msg1")
	<-agentStarted

	loop.Stop(WithSkipCheckpoint())
	loop.Stop(WithImmediate())

	exit := loop.Wait()
	assert.False(t, exit.Checkpointed, "SkipCheckpoint is sticky; checkpoint should be skipped")
}
