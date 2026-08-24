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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
)

func TestInMemoryStoreAdoptForeground(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	registered, err := store.Register(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "task", InvocationID: "invocation",
		Identity: []byte("identity"), RootSessionID: "session",
	})
	require.NoError(t, err)
	request := &AdoptForegroundStoreRequest{
		AdoptForegroundRequest: AdoptForegroundRequest{
			Spec: Spec{
				ID: "task", ExecutorKey: "executor", Kind: "subagent",
				SessionID: "session",
			},
			ExpectedGeneration: registered.Mailbox.Generation,
			InitialCheckpoint:  []byte("checkpoint"),
		},
		LeaseExpiryPolicy: LeaseExpiryRetry,
	}
	adopted, err := store.AdoptForeground(ctx, request)
	require.NoError(t, err)
	require.Equal(t, StatusSuspended, adopted.Status)
	require.Equal(t, "checkpoint", string(adopted.Checkpoint))
	mailbox, err := store.GetMailbox(ctx, "task")
	require.NoError(t, err)
	require.Equal(t, task.MailboxBackground, mailbox.State)
	require.Equal(t, registered.Mailbox.Generation+1, mailbox.Generation)
	notifications, err := store.Receive(ctx, &ReceiveNotificationsRequest{
		Limit: 10, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Len(t, notifications.Deliveries, 1)
	require.Equal(
		t,
		NotificationTaskBackgrounded,
		notifications.Deliveries[0].Record.Kind,
	)
	replayed, err := store.AdoptForeground(ctx, request)
	require.NoError(t, err)
	require.Equal(t, adopted.Spec, replayed.Spec)
	conflict := *request
	conflict.InitialCheckpoint = []byte("changed")
	_, err = store.AdoptForeground(ctx, &conflict)
	require.ErrorIs(t, err, ErrAlreadyExists)
	require.ErrorIs(t, store.AdvanceCursor(ctx, &task.AdvanceCursorRequest{
		TaskID: "task", ExpectedGeneration: registered.Mailbox.Generation,
	}), task.ErrOwnershipLost)
}

func TestAttack_AdoptForegroundDoesNotLoseRacingInput(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	registered, err := store.Register(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "task", InvocationID: "invocation",
	})
	require.NoError(t, err)
	_, err = store.SendInput(ctx, &task.SendInputRequest{
		TaskID: "task", EventID: "late", Kind: "event",
	})
	require.NoError(t, err)
	_, err = store.AdoptForeground(ctx, &AdoptForegroundStoreRequest{
		AdoptForegroundRequest: AdoptForegroundRequest{
			Spec:               Spec{ID: "task", ExecutorKey: "executor", Kind: "subagent"},
			ExpectedGeneration: registered.Mailbox.Generation,
		},
		LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.ErrorIs(t, err, task.ErrInputsPending)
	_, err = store.Get(ctx, "task")
	require.ErrorIs(t, err, ErrNotFound)
	mailbox, err := store.GetMailbox(ctx, "task")
	require.NoError(t, err)
	require.Equal(t, task.MailboxForeground, mailbox.State)
}

func TestInMemoryStoreDirectBackgroundCreateOwnsMailbox(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	created, err := store.Create(ctx, &CreateTaskRequest{
		Spec:              Spec{ID: "task", ExecutorKey: "executor", Kind: "tool"},
		LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	require.Equal(t, StatusPending, created.Status)
	mailbox, err := store.GetMailbox(ctx, "task")
	require.NoError(t, err)
	require.Equal(t, task.MailboxBackground, mailbox.State)
}

func TestBackgroundChildLifecycleNotifiesForegroundParent(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	parent, err := store.Register(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "parent", InvocationID: "parent-invocation",
		RootSessionID: "root-session",
	})
	require.NoError(t, err)
	child, err := store.Create(ctx, &CreateTaskRequest{
		Spec: Spec{
			ID: "child", ExecutorKey: "executor", Kind: "tool",
			ParentTaskID: parent.Mailbox.TaskID, SessionID: "root-session",
			NotifySession: true,
		},
		LeaseExpiryPolicy: LeaseExpiryRetry,
		ParentExecution: &task.ExecutionContext{
			TaskID: parent.Mailbox.TaskID, Mode: task.ModeForeground,
			OwnerEpoch: parent.Mailbox.Generation, RootSessionID: "root-session",
		},
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: child.Spec.ID, ExpectedVersion: child.Version,
	})
	require.NoError(t, err)
	_, err = store.CompleteIfNoInputs(ctx, &CompleteIfNoInputsRequest{
		TaskID: child.Spec.ID, ExpectedVersion: started.Version,
		Attempt: started.Attempt, InputCursor: 0, ResultData: []byte("done"),
	})
	require.NoError(t, err)
	inputs, err := store.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: parent.Mailbox.TaskID,
	})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, 2)
	require.Equal(t, string(NotificationTaskCreated), inputs.Inputs[0].Kind)
	require.Equal(t, string(NotificationCompleted), inputs.Inputs[1].Kind)
	require.Equal(t, task.MailboxForeground, inputs.MailboxState)
	childMailbox, err := store.GetMailbox(ctx, child.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, "root-session", childMailbox.RootSessionID)
}

func TestAttack_NestedSubmitDoesNotEmitRootSessionEvent(t *testing.T) {
	store := NewInMemoryStore(nil)
	rootEvents := 0
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *TaskSnapshot) error {
			rootEvents++
			return nil
		},
	})
	_, _, err := manager.LoadOrRegisterExecutor(&scriptedExecutor{})
	require.NoError(t, err)
	parent, err := manager.RegisterMailbox(
		context.Background(),
		&task.RegisterMailboxRequest{
			CandidateTaskID: "parent", InvocationID: "parent",
			RootSessionID: "root-session",
		},
	)
	require.NoError(t, err)
	ctx := task.WithExecutionContext(context.Background(), task.ExecutionContext{
		TaskID: parent.Mailbox.TaskID, Mode: task.ModeForeground,
		OwnerEpoch: parent.Mailbox.Generation, RootSessionID: "root-session",
	})
	child, err := manager.Submit(ctx, &SubmitRequest{Spec: Spec{
		ID: "child", ExecutorKey: "test", Kind: "test",
		SessionID: "immediate-parent-session", NotifySession: true,
	}})
	require.NoError(t, err)
	require.Equal(t, parent.Mailbox.TaskID, child.Spec.ParentTaskID)
	require.Equal(t, "root-session", child.Spec.SessionID)
	require.Zero(t, rootEvents)
	inputs, err := manager.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: parent.Mailbox.TaskID,
	})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, 1)
	require.Equal(t, string(NotificationTaskCreated), inputs.Inputs[0].Kind)
}

func TestBackgroundParentOwnershipFencesNestedCreation(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	parent, err := store.Create(ctx, &CreateTaskRequest{
		Spec:              Spec{ID: "parent", ExecutorKey: "executor"},
		LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: parent.Spec.ID, ExpectedVersion: parent.Version,
	})
	require.NoError(t, err)
	parentMailbox, err := store.GetMailbox(ctx, parent.Spec.ID)
	require.NoError(t, err)
	_, err = store.Register(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "bad-child", InvocationID: "bad-child",
		ParentTaskID: parent.Spec.ID,
		ParentExecution: &task.ExecutionContext{
			TaskID: parent.Spec.ID, Mode: task.ModeBackground,
			OwnerEpoch: parentMailbox.Generation, Attempt: started.Attempt + 1,
		},
	})
	require.ErrorIs(t, err, ErrLeaseLost)
	child, err := store.Register(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "child", InvocationID: "child",
		ParentTaskID: parent.Spec.ID,
		ParentExecution: &task.ExecutionContext{
			TaskID: parent.Spec.ID, Mode: task.ModeBackground,
			OwnerEpoch: parentMailbox.Generation, Attempt: started.Attempt,
		},
	})
	require.NoError(t, err)
	require.Equal(t, parent.Spec.ID, child.Mailbox.ParentTaskID)
}

func TestListChildrenPaginationAndCursorValidation(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	parent := registerMailboxForTest(t, store, "parent")
	for _, childID := range []string{"child-c", "child-a", "child-b"} {
		_, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: childID, InvocationID: childID,
			ParentTaskID: parent.TaskID,
			ParentExecution: &task.ExecutionContext{
				TaskID: parent.TaskID, Mode: task.ModeForeground,
				OwnerEpoch: parent.Generation,
			},
		})
		require.NoError(t, err)
	}
	first, err := store.ListChildren(ctx, &task.ListChildrenRequest{
		ParentTaskID: parent.TaskID, Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, first.Mailboxes, 2)
	require.Equal(t, "child-a", first.Mailboxes[0].TaskID)
	require.NotEmpty(t, first.NextCursor)
	second, err := store.ListChildren(ctx, &task.ListChildrenRequest{
		ParentTaskID: parent.TaskID, Cursor: first.NextCursor, Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, second.Mailboxes, 1)
	require.Equal(t, "child-c", second.Mailboxes[0].TaskID)
	_, err = store.ListChildren(ctx, &task.ListChildrenRequest{
		ParentTaskID: "other", Cursor: first.NextCursor,
	})
	require.ErrorIs(t, err, ErrInvalidCursor)
}

func registerMailboxForTest(
	t *testing.T,
	store *InMemoryStore,
	id string,
) *task.Mailbox {
	t.Helper()
	registered, err := store.Register(context.Background(), &task.RegisterMailboxRequest{
		CandidateTaskID: id, InvocationID: id,
	})
	require.NoError(t, err)
	return registered.Mailbox
}

func TestAttack_CompleteIfNoInputsReturnsTaskToPendingOnRace(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	created, err := store.Create(ctx, &CreateTaskRequest{
		Spec:              Spec{ID: "task", ExecutorKey: "executor", Kind: "subagent"},
		LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	_, err = store.SendInput(ctx, &task.SendInputRequest{
		TaskID: created.Spec.ID, EventID: "late", Kind: "event",
	})
	require.NoError(t, err)
	result, err := store.CompleteIfNoInputs(ctx, &CompleteIfNoInputsRequest{
		TaskID: created.Spec.ID, ExpectedVersion: started.Version,
		Attempt: started.Attempt, InputCursor: 0,
	})
	require.ErrorIs(t, err, task.ErrInputsPending)
	require.Equal(t, StatusPending, result.Status)
}

func TestAttack_ManagerRedispatchesInputRace(t *testing.T) {
	store := NewInMemoryStore(nil)
	var attempts atomic.Int64
	executor := &scriptedExecutor{execute: func(
		ctx context.Context,
		backgroundTask *TaskSnapshot,
		runtime ExecutionRuntime,
	) (*ExecutionResult, error) {
		if attempts.Add(1) == 1 {
			_, err := store.SendInput(ctx, &task.SendInputRequest{
				TaskID: backgroundTask.Spec.ID, EventID: "late", Kind: "event",
			})
			require.NoError(t, err)
			return &ExecutionResult{
				Action:      ExecutionActionComplete,
				InputCursor: 0,
			}, nil
		}
		inputs, err := runtime.ListInputs(ctx, 0, 10)
		if err != nil {
			return nil, err
		}
		if err = runtime.AdvanceInputCursor(
			ctx,
			inputs.ConsumedCursor,
			inputs.LatestSequence,
		); err != nil {
			return nil, err
		}
		return &ExecutionResult{
			Action: ExecutionActionComplete, Data: []byte("done"),
			InputCursor: inputs.LatestSequence,
		}, nil
	}}
	manager := managerWithExecutor(t, store, executor, time.Minute)
	backgroundTask, err := manager.Submit(
		context.Background(),
		&SubmitRequest{Spec: validSpec("redispatch")},
	)
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), backgroundTask.Spec.ID))
	require.Eventually(t, func() bool {
		current, getErr := manager.Get(context.Background(), backgroundTask.Spec.ID)
		return getErr == nil && current.Status == StatusCompleted
	}, time.Second, time.Millisecond)
	require.Equal(t, int64(2), attempts.Load())
}

func TestAttack_TerminalFailureSealsMailboxWithPendingInput(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	created, err := store.Create(ctx, &CreateTaskRequest{
		Spec:              Spec{ID: "task", ExecutorKey: "executor", Kind: "tool"},
		LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	_, err = store.SendInput(ctx, &task.SendInputRequest{
		TaskID: created.Spec.ID, EventID: "late", Kind: "event",
	})
	require.NoError(t, err)
	_, err = store.Fail(ctx, &FailTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: started.Version,
		Error: "failed",
	})
	require.NoError(t, err)
	mailbox, err := store.GetMailbox(ctx, created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, mailbox.State)
	_, err = store.SendInput(ctx, &task.SendInputRequest{
		TaskID: created.Spec.ID, EventID: "after", Kind: "event",
	})
	require.ErrorIs(t, err, task.ErrMailboxSealed)
}

func TestCommitInputAndWaitAreAtomicWithMailboxCursor(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	created, err := store.Create(ctx, &CreateTaskRequest{
		Spec:              Spec{ID: "task", ExecutorKey: "executor", Kind: "tool"},
		LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	_, err = store.SendInput(ctx, &task.SendInputRequest{
		TaskID: created.Spec.ID, EventID: "resume", Kind: "resume",
	})
	require.NoError(t, err)
	committed, err := store.CommitInput(ctx, &CommitInputRequest{
		TaskID: created.Spec.ID, ExpectedVersion: started.Version,
		Attempt: started.Attempt, ExpectedCursor: 0, InputCursor: 1,
		Checkpoint: []byte("resumed"),
	})
	require.NoError(t, err)
	require.Equal(t, []byte("resumed"), committed.Checkpoint)
	mailbox, err := store.GetMailbox(ctx, created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), mailbox.ConsumedCursor)
	waiting, err := store.WaitInputIfNoInputs(ctx, &WaitInputIfNoInputsRequest{
		TaskID: created.Spec.ID, ExpectedVersion: committed.Version,
		Attempt: started.Attempt, InputCursor: 1, Checkpoint: []byte("waiting"),
	})
	require.NoError(t, err)
	require.Equal(t, StatusWaitingInput, waiting.Status)
}

func TestSuspendIfNoInputsSuccessAndRace(t *testing.T) {
	ctx := context.Background()
	for _, withInput := range []bool{false, true} {
		store := NewInMemoryStore(nil)
		id := "idle"
		if withInput {
			id = "racing"
		}
		created, err := store.Create(ctx, &CreateTaskRequest{
			Spec:              Spec{ID: id, ExecutorKey: "executor", Kind: "tool"},
			LeaseExpiryPolicy: LeaseExpiryRetry,
		})
		require.NoError(t, err)
		started, err := store.Start(ctx, &StartTaskRequest{
			TaskID: id, ExpectedVersion: created.Version,
		})
		require.NoError(t, err)
		if withInput {
			_, err = store.SendInput(ctx, &task.SendInputRequest{
				TaskID: id, EventID: "late", Kind: "event",
			})
			require.NoError(t, err)
		}
		result, err := store.SuspendIfNoInputs(ctx, &SuspendIfNoInputsRequest{
			TaskID: id, ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0,
			Checkpoint: []byte("checkpoint"),
		})
		if withInput {
			require.ErrorIs(t, err, task.ErrInputsPending)
			require.Equal(t, StatusPending, result.Status)
			require.Equal(t, []byte("checkpoint"), result.Checkpoint)
		} else {
			require.NoError(t, err)
			require.Equal(t, StatusSuspended, result.Status)
		}
	}
}

func TestWaitInputIfNoInputsPersistsCheckpointOnInputRace(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	created, err := store.Create(ctx, &CreateTaskRequest{
		Spec:              Spec{ID: "task", ExecutorKey: "executor", Kind: "tool"},
		LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	for _, eventID := range []string{"consumed", "late"} {
		_, err = store.SendInput(ctx, &task.SendInputRequest{
			TaskID: created.Spec.ID, EventID: eventID, Kind: "event",
		})
		require.NoError(t, err)
	}
	result, err := store.WaitInputIfNoInputs(ctx, &WaitInputIfNoInputsRequest{
		TaskID: created.Spec.ID, ExpectedVersion: started.Version,
		Attempt: started.Attempt, InputCursor: 1,
		Checkpoint: []byte("new-checkpoint"),
	})
	require.ErrorIs(t, err, task.ErrInputsPending)
	require.Equal(t, StatusPending, result.Status)
	require.Equal(t, []byte("new-checkpoint"), result.Checkpoint)
	mailbox, err := store.GetMailbox(ctx, created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), mailbox.ConsumedCursor)
}

func TestMailboxValidationBoundaries(t *testing.T) {
	store := NewInMemoryStore(&InMemoryStoreConfig{MaxValueBytes: 4})
	ctx := context.Background()
	_, err := store.Register(ctx, nil)
	require.Error(t, err)
	_, err = store.Register(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "large", InvocationID: "large",
		Identity: []byte("large"),
	})
	require.Error(t, err)
	_, err = store.GetMailbox(ctx, "missing")
	require.ErrorIs(t, err, task.ErrMailboxNotFound)
	_, err = store.GetActiveMailboxBySession(ctx, "")
	require.Error(t, err)
	_, err = store.SendInput(ctx, nil)
	require.Error(t, err)
	_, err = store.SendInput(ctx, &task.SendInputRequest{
		TaskID: "missing", EventID: "event", Kind: "kind",
	})
	require.ErrorIs(t, err, task.ErrMailboxNotFound)
	_, err = store.SendInput(ctx, &task.SendInputRequest{
		TaskID: "missing", EventID: "event", Kind: "kind",
		Delivery: task.InputDelivery(99),
	})
	require.Error(t, err)
	_, err = store.ListInputs(ctx, nil)
	require.Error(t, err)
	_, err = store.ListInputs(ctx, &task.ListInputsRequest{TaskID: "missing"})
	require.ErrorIs(t, err, task.ErrMailboxNotFound)
	waitCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.WaitInputs(waitCtx, &task.WaitInputsRequest{
		TaskID: "missing",
	})
	require.ErrorIs(t, err, task.ErrMailboxNotFound)
	require.Error(t, store.AdvanceCursor(ctx, nil))
	require.ErrorIs(t, store.AdvanceCursor(ctx, &task.AdvanceCursorRequest{
		TaskID: "missing",
	}), task.ErrMailboxNotFound)
	_, err = store.SealIfIdle(ctx, nil)
	require.Error(t, err)
	_, err = store.Abandon(ctx, nil)
	require.Error(t, err)
	_, err = store.ListChildren(ctx, nil)
	require.Error(t, err)

	mailbox := registerMailboxForTest(t, store, "mailbox")
	_, err = store.SendInput(ctx, &task.SendInputRequest{
		TaskID: mailbox.TaskID, EventID: "event", Kind: "kind",
	})
	require.NoError(t, err)
	require.ErrorIs(t, store.AdvanceCursor(ctx, &task.AdvanceCursorRequest{
		TaskID: mailbox.TaskID, ExpectedCursor: 1, Cursor: 1,
		ExpectedGeneration: mailbox.Generation,
	}), task.ErrCursorConflict)
	require.ErrorIs(t, store.AdvanceCursor(ctx, &task.AdvanceCursorRequest{
		TaskID: mailbox.TaskID, ExpectedCursor: 0, Cursor: 2,
		ExpectedGeneration: mailbox.Generation,
	}), ErrInvalidCursor)
	_, err = store.SealIfIdle(ctx, &task.SealMailboxRequest{
		TaskID: mailbox.TaskID, ExpectedGeneration: mailbox.Generation + 1,
	})
	require.ErrorIs(t, err, task.ErrOwnershipLost)
	_, err = store.Abandon(ctx, &task.AbandonMailboxRequest{
		TaskID: mailbox.TaskID, ExpectedGeneration: mailbox.Generation + 1,
	})
	require.ErrorIs(t, err, task.ErrOwnershipLost)
}
