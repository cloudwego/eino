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

package tool

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	componenttool "github.com/cloudwego/eino/components/tool"
)

func TestAttack_RecoverableContextCancellationAlwaysSuspends(t *testing.T) {
	checkpoint, err := encodeManagedCheckpoint(nil, []byte("current"))
	require.NoError(t, err)
	payload := encodedPayload(t, "external", `{"value":"recover"}`)

	for i := 0; i < 100; i++ {
		waitStarted := make(chan struct{})
		registry := NewRegistry()
		require.NoError(t, registry.Register(&Registration{
			Info: toolInfo("external"),
			Tool: &fakeTool{
				recover: func(context.Context, *RecoverRequest) (Run, error) {
					return &fakeRun{
						wait: func(ctx context.Context) (*Outcome, error) {
							close(waitStarted)
							<-ctx.Done()
							runtime.Gosched()
							return nil, ctx.Err()
						},
					}, nil
				},
			},
		}))
		ctx, cancel := context.WithCancel(context.Background())
		type executeResult struct {
			result *backgroundtask.ExecutionResult
			err    error
		}
		done := make(chan executeResult, 1)
		go func() {
			result, executeErr := (&executor{
				registry: registry, recoverable: true,
			}).Execute(
				ctx,
				&backgroundtask.Task{
					Spec: backgroundtask.Spec{
						ID: "recover-task", ExecutorKey: RecoverableExecutorKey,
						Kind: "background_tool", Payload: payload,
					},
					Status: backgroundtask.StatusRunning, Attempt: 2,
					Checkpoint: checkpoint,
				},
				&replayRuntimeStub{},
			)
			done <- executeResult{result: result, err: executeErr}
		}()
		<-waitStarted
		cancel()
		executed := <-done
		require.NoError(t, executed.err, "iteration %d", i)
		require.Equal(t, backgroundtask.StatusSuspended, executed.result.Status, "iteration %d", i)
	}
}

func TestAttack_StreamDispatchRejectionMatchesInvoke(t *testing.T) {
	dispatchErr := errors.New("worker is closing")
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return nil, errors.New("unexpected execution")
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	managed := wrapped.(*managedTool)
	managed.runInBackground = func(context.Context, string) bool { return true }
	managed.dispatchPending = func(context.Context, *backgroundtask.Task) error {
		return dispatchErr
	}

	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(),
		toolArgument(`{"value":"x"}`),
	)
	require.Nil(t, stream)
	require.ErrorIs(t, err, dispatchErr)
	task, getErr := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, getErr)
	require.Equal(t, backgroundtask.StatusPending, task.Status)
	require.EqualError(
		t,
		err,
		fmt.Sprintf(
			"backgroundtask/tool: dispatch pending task %q: %s",
			task.Spec.ID,
			dispatchErr,
		),
	)
}

func TestAttack_StreamAutoBackgroundDispatchRejectionRetainsRun(t *testing.T) {
	dispatchErr := errors.New("worker is closing")
	release := make(chan struct{})
	stopCalls := 0
	implementation := &handoffFakeTool{fakeTool: &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(context.Context) (*Outcome, error) {
					<-release
					return &Outcome{Status: backgroundtask.StatusCompleted}, nil
				},
				stop: func(context.Context) error {
					stopCalls++
					return nil
				},
			}, nil
		},
	}}
	manager, wrapped := newTestManagedTool(t, implementation, time.Millisecond)
	managed := wrapped.(*managedTool)
	managed.dispatchPending = func(context.Context, *backgroundtask.Task) error {
		return dispatchErr
	}

	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(),
		toolArgument(`{"value":"x"}`),
	)
	require.NoError(t, err)
	result, err := stream.Recv()
	require.Nil(t, result)
	require.ErrorIs(t, err, dispatchErr)
	task, getErr := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, getErr)
	require.Equal(t, backgroundtask.StatusPending, task.Status)
	require.Zero(t, stopCalls)

	close(release)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	require.Equal(t, backgroundtask.StatusCompleted, waitTaskTerminal(
		t,
		manager,
		task.Spec.ID,
	).Status)
}
