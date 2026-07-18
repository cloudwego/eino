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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// deltaThenStuckChatModel mimics an HTTP SSE provider that emits one delta,
// flushes it, and then blocks as if waiting on request-context cancellation
// that never actually arrives (ADK's root, non-abort-only cancel scope does
// not cancel the Go context of the model call). It never sends or closes
// again on its own; the test unblocks all outstanding goroutines on cleanup.
type deltaThenStuckChatModel struct {
	delta   *schema.Message
	started chan struct{}
	release chan struct{}
}

func newDeltaThenStuckChatModel(delta *schema.Message) *deltaThenStuckChatModel {
	return &deltaThenStuckChatModel{
		delta:   delta,
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (m *deltaThenStuckChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.delta, nil
}

func (m *deltaThenStuckChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		writer.Send(m.delta, nil)
		select {
		case m.started <- struct{}{}:
		default:
		}
		// Simulate the provider transport hanging until the connection is
		// forcibly torn down, which never happens for a root cancel scope.
		<-m.release
		writer.Close()
	}()
	return reader, nil
}

func (m *deltaThenStuckChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

// TestWithCancel_CancelImmediate_MidStream_AlwaysCheckpoints reproduces
// https://github.com/cloudwego/eino/issues/1148: a CancelImmediate raced
// against a streaming ChatModel that has already emitted a chunk and is
// blocked on the next Recv() must deterministically surface *adk.CancelError
// (never a raw StreamCanceledError) and must always persist a checkpoint.
//
// Before the receiveWithListening fix in compose/graph_manager.go, this
// occasionally failed because a zero-timeout graph interrupt raced a
// time.After(0) timer against the task's own completion (triggered by the
// same cancel signal via wrapStreamWithCancelMonitoring's injected
// ErrStreamCanceled), non-deterministically skipping the checkpoint/interrupt
// path. Run repeatedly to catch the race with reasonable confidence.
func TestWithCancel_CancelImmediate_MidStream_AlwaysCheckpoints(t *testing.T) {
	const iterations = 300

	for i := 0; i < iterations; i++ {
		ctx := context.Background()

		blk := newDeltaThenStuckChatModel(schema.AssistantMessage("hello", nil))
		t.Cleanup(func() { close(blk.release) })

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "test",
			Model:       blk,
		})
		require.NoError(t, err)

		store := newCancelTestStore()
		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: true,
			CheckPointStore: store,
		})

		checkpointID := "mid-stream-race"
		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("hi")}, cancelOpt, WithCheckPointID(checkpointID))

		select {
		case <-blk.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: model did not start streaming", i)
		}

		handle, _ := cancelFn()
		cancelErr := handle.Wait()
		require.NoError(t, cancelErr, "iteration %d", i)

		var foundCancelError bool
		var foundRawStreamCanceled bool
		for {
			e, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if e.Err != nil && errors.As(e.Err, &ce) {
				foundCancelError = true
			} else if e.Err != nil && errors.Is(e.Err, ErrStreamCanceled) {
				foundRawStreamCanceled = true
			}
		}

		assert.False(t, foundRawStreamCanceled, "iteration %d: raw ErrStreamCanceled leaked to the event stream instead of being converted to CancelError", i)
		assert.True(t, foundCancelError, "iteration %d: expected *adk.CancelError in event stream", i)

		store.mu.Lock()
		_, checkpointSaved := store.m[checkpointID]
		store.mu.Unlock()
		assert.True(t, checkpointSaved, "iteration %d: expected checkpoint %q to be saved after accepted CancelImmediate", i, checkpointID)
	}
}
