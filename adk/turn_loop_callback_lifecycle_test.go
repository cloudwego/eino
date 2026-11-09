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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

type callbackLifecycleAgent[M MessageType] struct {
	started chan context.Context
	stopped chan struct{}
}

func (a *callbackLifecycleAgent[M]) Name(context.Context) string { return "cooperative" }
func (a *callbackLifecycleAgent[M]) Description(context.Context) string {
	return "wait for cancellation"
}
func (a *callbackLifecycleAgent[M]) Run(ctx context.Context, _ *TypedAgentInput[M], _ ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go func() {
		defer gen.Close()
		defer close(a.stopped)
		a.started <- ctx
		<-ctx.Done()
		gen.Send(&TypedAgentEvent[M]{Err: ctx.Err()})
	}()
	return iter
}

func testTurnLoopCallbackCancelsRun[M MessageType](t *testing.T) {
	t.Helper()
	for _, outcome := range []string{"error", "panic", "stop"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			a := &callbackLifecycleAgent[M]{started: make(chan context.Context, 1), stopped: make(chan struct{})}
			var agentCtx context.Context
			callbackErr := errors.New("delivery failed")
			loop := NewTurnLoop(TurnLoopConfig[string, M]{
				GenInput: func(_ context.Context, _ *TurnLoop[string, M], items []string) (*GenInputResult[string, M], error) {
					return &GenInputResult[string, M]{Input: &TypedAgentInput[M]{}, Consumed: items}, nil
				},
				PrepareAgent: func(context.Context, *TurnLoop[string, M], []string) (TypedAgent[M], error) { return a, nil },
				OnAgentEvents: func(_ context.Context, tc *TurnContext[string, M], _ *AsyncIterator[*TypedAgentEvent[M]]) error {
					agentCtx = <-a.started
					if outcome == "panic" {
						panic("delivery failed")
					}
					if outcome == "stop" {
						tc.Loop.Stop()
						return nil
					}
					return callbackErr
				},
			})
			t.Cleanup(func() {
				cancel()
				select {
				case <-a.stopped:
				case <-time.After(time.Second):
					t.Error("cooperative agent did not terminate")
				}
			})
			loop.Push("request")
			loop.Run(ctx)
			result := loop.Wait()
			if outcome == "panic" {
				require.ErrorContains(t, result.ExitReason, "delivery failed")
			} else if outcome == "stop" {
				require.NoError(t, result.ExitReason)
			} else {
				require.ErrorIs(t, result.ExitReason, callbackErr)
			}
			require.ErrorIs(t, agentCtx.Err(), context.Canceled,
				"Wait returned after the event handler but left its agent running")
			require.NoError(t, ctx.Err(), "the parent context belongs to the caller")
		})
	}
}

func TestTurnLoopCallbackCancelsMessageRun(t *testing.T) {
	testTurnLoopCallbackCancelsRun[*schema.Message](t)
}

func TestTurnLoopCallbackCancelsAgenticRun(t *testing.T) {
	testTurnLoopCallbackCancelsRun[*schema.AgenticMessage](t)
}
