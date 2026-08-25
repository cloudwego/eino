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

package background

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	taskcore "github.com/cloudwego/eino/adk/task"
)

func TestHandleImplementsOwnerNeutralTaskOperations(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, &scriptedExecutor{}, 0)
	created, err := manager.Submit(ctx, &SubmitRequest{
		Spec: validSpec("handled"),
	})
	require.NoError(t, err)
	handle, err := manager.Handle(created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, created.Spec.ID, handle.ID())
	require.NoError(t, handle.SendInput(ctx, &taskcore.Input{
		EventID: "event", Kind: "input", Data: []byte("value"),
	}))
	inputs, err := manager.ListInputs(ctx, &taskcore.ListInputsRequest{
		TaskID: created.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, 1)

	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	require.NoError(t, store.AdvanceCursor(ctx, &taskcore.AdvanceCursorRequest{
		TaskID: created.Spec.ID, ExpectedCursor: 0,
		Cursor: inputs.LatestSequence, ExpectedGeneration: inputs.Generation,
		Attempt: started.Attempt,
	}))
	completed, err := store.CompleteIfNoInputs(ctx, &CompleteIfNoInputsRequest{
		TaskID: created.Spec.ID, ExpectedVersion: started.Version,
		Attempt: started.Attempt, InputCursor: inputs.LatestSequence,
		ResultData: []byte("done"),
	})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, completed.Status)
	outcome, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, taskcore.OutcomeCompleted, outcome.Status)
	require.Equal(t, []byte("done"), outcome.Data)
	require.NoError(t, handle.Cancel(ctx, "late cancel"))
}

func TestHandleReportsWaitingInputAndValidation(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, &scriptedExecutor{}, 0)
	created, err := manager.Submit(ctx, &SubmitRequest{
		Spec: validSpec("waiting-handle"),
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	_, err = store.WaitInputIfNoInputs(ctx, &WaitInputIfNoInputsRequest{
		TaskID: created.Spec.ID, ExpectedVersion: started.Version,
		Attempt: started.Attempt, InputCursor: 0, Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)
	handle, err := manager.Handle(created.Spec.ID)
	require.NoError(t, err)
	outcome, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, taskcore.OutcomeInterrupted, outcome.Status)
	require.ErrorIs(t, handle.SendInput(ctx, nil), taskcore.ErrInputRequired)
	_, err = manager.Handle("")
	require.Error(t, err)
}

func TestHandleReportsTerminalFailuresAndWaitCancellation(t *testing.T) {
	tests := []struct {
		name       string
		transition func(*InMemoryStore, *TaskSnapshot) error
		status     taskcore.OutcomeStatus
		reason     string
	}{
		{
			name: "failed",
			transition: func(store *InMemoryStore, started *TaskSnapshot) error {
				_, err := store.Fail(context.Background(), &FailTaskRequest{
					TaskID: started.Spec.ID, ExpectedVersion: started.Version,
					Error: "execution failed",
				})
				return err
			},
			status: taskcore.OutcomeFailed,
			reason: "execution failed",
		},
		{
			name: "canceled",
			transition: func(store *InMemoryStore, started *TaskSnapshot) error {
				requested, err := store.RequestCancel(
					context.Background(),
					&RequestCancelRequest{
						TaskID: started.Spec.ID, ExpectedVersion: started.Version,
						Reason: "operator canceled",
					},
				)
				if err != nil {
					return err
				}
				_, err = store.AckCancel(context.Background(), &AckCancelRequest{
					TaskID: started.Spec.ID, ExpectedVersion: requested.Version,
				})
				return err
			},
			status: taskcore.OutcomeCanceled,
			reason: "operator canceled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewInMemoryStore(nil)
			manager := managerWithExecutor(t, store, &scriptedExecutor{}, 0)
			created, err := manager.Submit(ctx, &SubmitRequest{
				Spec: validSpec("handle-" + test.name),
			})
			require.NoError(t, err)
			started, err := store.Start(ctx, &StartTaskRequest{
				TaskID: created.Spec.ID, ExpectedVersion: created.Version,
			})
			require.NoError(t, err)
			require.NoError(t, test.transition(store, started))

			handle, err := manager.Handle(created.Spec.ID)
			require.NoError(t, err)
			outcome, err := handle.Wait(ctx)
			require.NoError(t, err)
			require.Equal(t, test.status, outcome.Status)
			require.Equal(t, test.reason, outcome.Error)
		})
	}

	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, &scriptedExecutor{}, 0)
	created, err := manager.Submit(context.Background(), &SubmitRequest{
		Spec: validSpec("handle-context"),
	})
	require.NoError(t, err)
	started, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	suspended, err := store.SuspendIfNoInputs(
		context.Background(),
		&SuspendIfNoInputsRequest{
			TaskID: created.Spec.ID, ExpectedVersion: started.Version,
			Attempt: started.Attempt, Checkpoint: []byte("suspended"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, StatusSuspended, suspended.Status)
	handle, err := manager.Handle(created.Spec.ID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = handle.Wait(ctx)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
}
