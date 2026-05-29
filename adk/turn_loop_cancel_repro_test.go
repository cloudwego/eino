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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type turnLoopAgenticToolCallModel struct {
	callCount int32
}

func (m *turnLoopAgenticToolCallModel) Generate(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
	if atomic.AddInt32(&m.callCount, 1) == 1 {
		return agenticToolCallMsg("turn_loop_slow_tool", "call-1", `{"input":"x"}`), nil
	}
	return agenticMsg("done"), nil
}

func (m *turnLoopAgenticToolCallModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{msg}), nil
}

func TestTurnLoop_StopGracefulThenImmediate_AgenticStreamableToolCheckpoint(t *testing.T) {
	ctx := context.Background()

	gate := make(chan struct{})
	slowTool := &slowStreamingTool{
		name:          "turn_loop_slow_tool",
		chunkInterval: time.Hour,
		chunks:        []string{"chunk"},
		started:       make(chan struct{}, 1),
		gate:          gate,
	}
	t.Cleanup(func() {
		close(gate)
	})

	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TurnLoopAgenticRepro",
		Description: "repro agent",
		Model:       &turnLoopAgenticToolCallModel{},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{slowTool}},
		},
	})
	require.NoError(t, err)

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string, *schema.AgenticMessage]{
		Store: newTestStore(),
		GenInput: func(_ context.Context, _ *TurnLoop[string, *schema.AgenticMessage], items []string) (*GenInputResult[string, *schema.AgenticMessage], error) {
			return &GenInputResult[string, *schema.AgenticMessage]{
				Input: &TypedAgentInput[*schema.AgenticMessage]{
					Messages: []*schema.AgenticMessage{schema.UserAgenticMessage(items[0])},
				},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(_ context.Context, _ *TurnLoop[string, *schema.AgenticMessage], _ []string) (TypedAgent[*schema.AgenticMessage], error) {
			return agent, nil
		},
	})

	loop.Push("trigger")
	select {
	case <-slowTool.started:
	case <-time.After(5 * time.Second):
		t.Fatal("streamable tool did not start")
	}

	loop.Stop(WithGraceful())
	time.Sleep(50 * time.Millisecond)
	loop.Stop(WithImmediate())

	exit := loop.Wait()

	var cancelErr *CancelError
	require.True(t, errors.As(exit.ExitReason, &cancelErr), "ExitReason should be a *CancelError, got %v", exit.ExitReason)
	assert.NoError(t, exit.CheckpointErr)
}

func TestTurnLoop_PreemptAfterToolCallsTimeout_AgenticStreamableToolCheckpoint(t *testing.T) {
	ctx := context.Background()

	gate := make(chan struct{})
	slowTool := &slowStreamingTool{
		name:          "turn_loop_slow_tool",
		chunkInterval: time.Millisecond,
		chunks:        []string{"chunk-1", "chunk-2", "chunk-3"},
		started:       make(chan struct{}, 1),
		gate:          gate,
	}
	t.Cleanup(func() {
		close(gate)
	})

	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TurnLoopAgenticPreemptRepro",
		Description: "repro agent",
		Model:       &turnLoopAgenticToolCallModel{},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{slowTool}},
		},
	})
	require.NoError(t, err)

	errCh := make(chan error, 16)
	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string, *schema.AgenticMessage]{
		Store: newTestStore(),
		GenInput: func(_ context.Context, _ *TurnLoop[string, *schema.AgenticMessage], items []string) (*GenInputResult[string, *schema.AgenticMessage], error) {
			return &GenInputResult[string, *schema.AgenticMessage]{
				Input: &TypedAgentInput[*schema.AgenticMessage]{
					Messages: []*schema.AgenticMessage{schema.UserAgenticMessage(items[0])},
				},
				Consumed: []string{items[0]},
				Remaining: func() []string {
					if len(items) <= 1 {
						return nil
					}
					return append([]string(nil), items[1:]...)
				}(),
			}, nil
		},
		PrepareAgent: func(_ context.Context, _ *TurnLoop[string, *schema.AgenticMessage], _ []string) (TypedAgent[*schema.AgenticMessage], error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, _ *TurnContext[string, *schema.AgenticMessage], events *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]]) error {
			for {
				ev, ok := events.Next()
				if !ok {
					return nil
				}
				if ev.Err != nil {
					errCh <- ev.Err
				}
			}
		},
	})

	loop.Push("trigger")
	select {
	case <-slowTool.started:
	case <-time.After(5 * time.Second):
		t.Fatal("streamable tool did not start")
	}
	time.Sleep(20 * time.Millisecond)

	ok, ack := loop.Push("preempt", WithPreemptTimeout[string, *schema.AgenticMessage](AfterToolCalls, 20*time.Millisecond))
	require.True(t, ok)
	select {
	case <-ack:
	case <-time.After(5 * time.Second):
		t.Fatal("preempt was not acknowledged")
	}

	loop.Stop()
	exit := loop.Wait()

	for {
		select {
		case err := <-errCh:
			assert.NotContains(t, err.Error(), "gob marshal error")
		default:
			assert.NoError(t, exit.CheckpointErr)
			return
		}
	}
}

func TestTurnLoop_ManagedInterruptEarlyResumeWaitsForCheckpoint(t *testing.T) {
	ctx := context.Background()
	streamTool := &cancelInterruptThenHangingStreamTool{
		name:        "turn_loop_slow_tool",
		interrupted: make(chan struct{}),
		resumed:     make(chan struct{}),
		gate:        make(chan struct{}),
	}
	var closeGateOnce sync.Once
	closeGate := func() {
		closeGateOnce.Do(func() { close(streamTool.gate) })
	}
	t.Cleanup(func() {
		closeGate()
	})
	var interruptTargetID string

	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TurnLoopManagedEarlyResume",
		Description: "repro agent",
		Model:       &turnLoopAgenticToolCallModel{},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{streamTool}},
		},
	})
	require.NoError(t, err)

	loop := NewTurnLoop(TurnLoopConfig[string, *schema.AgenticMessage]{
		InterruptMode: TurnLoopInterruptWaitsForExplicitResume,
		GenInput: func(_ context.Context, _ *TurnLoop[string, *schema.AgenticMessage], items []string) (*GenInputResult[string, *schema.AgenticMessage], error) {
			return &GenInputResult[string, *schema.AgenticMessage]{
				Input: &TypedAgentInput[*schema.AgenticMessage]{
					Messages:        []*schema.AgenticMessage{schema.UserAgenticMessage(items[0])},
					EnableStreaming: true,
				},
				Consumed: []string{items[0]},
			}, nil
		},
		GenResume: func(_ context.Context, _ *TurnLoop[string, *schema.AgenticMessage], interruptedItems, unhandledItems, resumeItems []string) (*GenResumeResult[string, *schema.AgenticMessage], error) {
			return &GenResumeResult[string, *schema.AgenticMessage]{
				Decision: TurnLoopResumeDecisionResume,
				ResumeParams: &ResumeParams{
					Targets: map[string]any{interruptTargetID: "approved"},
				},
				Consumed:  append(append([]string{}, interruptedItems...), resumeItems...),
				Remaining: unhandledItems,
			}, nil
		},
		PrepareAgent: func(_ context.Context, _ *TurnLoop[string, *schema.AgenticMessage], _ []string) (TypedAgent[*schema.AgenticMessage], error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, tc *TurnContext[string, *schema.AgenticMessage], events *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]]) error {
			for {
				event, ok := events.Next()
				if !ok {
					return nil
				}
				if event.Err != nil {
					return event.Err
				}
				if event.Action == nil || event.Action.Interrupted == nil {
					continue
				}
				for _, ictx := range event.Action.Interrupted.InterruptContexts {
					if ictx.IsRootCause {
						interruptTargetID = ictx.ID
						break
					}
				}
				if interruptTargetID != "" {
					return tc.Loop.Resume("approved")
				}
			}
		},
	})
	loop.Push("trigger")
	loop.Run(ctx)

	select {
	case <-streamTool.interrupted:
	case <-time.After(5 * time.Second):
		t.Fatal("streamable tool did not interrupt")
	}
	select {
	case <-streamTool.resumed:
	case <-time.After(5 * time.Second):
		t.Fatal("streamable tool did not resume")
	}

	closeGate()
	loop.Stop()
	exit := loop.Wait()
	require.NoError(t, exit.ExitReason)
	require.NoError(t, exit.CheckpointErr)
}
