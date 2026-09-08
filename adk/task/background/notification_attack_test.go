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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	taskcore "github.com/cloudwego/eino/adk/task"
)

func TestAttack_DisabledLifecycleStillAllowsExplicitParentNotification(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(
			ctx context.Context,
			_ *TaskSnapshot,
			_ ExecutionRuntime,
		) (*ExecutionResult, error) {
			err := NotifyParent(ctx, &NotifyParentRequest{
				EventID: "explicit", Kind: "application.audit",
				Data: []byte("explicit data"),
			})
			require.NoError(t, err)
			return &ExecutionResult{
				Action: ExecutionActionComplete, Data: []byte("result"),
			}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	spec := validSpec("explicit-without-lifecycle")
	spec.NotifySession = false
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))

	result, err := store.Receive(
		context.Background(),
		&ReceiveNotificationsRequest{Limit: 100, LeaseDuration: time.Second},
	)
	require.NoError(t, err)
	kinds := make([]NotificationKind, len(result.Deliveries))
	for i, delivery := range result.Deliveries {
		kinds[i] = delivery.Record.Kind
	}
	t.Logf("delivered kinds with lifecycle disabled: %v", kinds)
	require.Equal(t, []NotificationKind{
		NotificationTaskCreated,
		"application.audit",
	}, kinds)
	require.Equal(t, "explicit data", string(result.Deliveries[1].Record.Data))
}

func TestAttack_CustomNotificationIdentityIsOutsideLifecycleNamespace(t *testing.T) {
	taskID := string([]byte{0xff, ':', 0x00, '1'})
	eventID := string([]byte{0xfe, ':', 0x00, 'e'})
	custom := customNotificationID(taskID, eventID)
	require.Equal(t, custom, customNotificationID(taskID, eventID))
	require.True(t, strings.HasSuffix(custom, ":application-event"))

	for _, kind := range []NotificationKind{
		NotificationTaskCreated,
		NotificationTaskBackgrounded,
		NotificationWaitingInput,
		NotificationCompleted,
		NotificationFailed,
		NotificationCanceled,
		"eino.future",
	} {
		for _, version := range []int64{1, 9, 1<<62 - 1} {
			lifecycle := fmt.Sprintf("%s:%d:%s", taskID, version, kind)
			require.NotEqual(t, lifecycle, custom)
		}
	}
	t.Logf("custom identity %q uses a fixed non-lifecycle terminal domain", custom)
}

func TestAttack_NestedNotificationFailureDoesNotPoisonReplay(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	parent, err := store.Register(ctx, &taskcore.RegisterMailboxRequest{
		CandidateTaskID: "parent", InvocationID: "parent",
		RootSessionID: "root",
	})
	require.NoError(t, err)
	child, err := store.Create(ctx, &CreateTaskRequest{
		Spec: Spec{
			ID: "child", ExecutorKey: "executor", Kind: "tool",
			ParentTaskID: parent.Mailbox.TaskID, RootSessionID: "root",
			NotifySession: true,
		},
		LeaseExpiryPolicy: LeaseExpiryRetry,
		ParentExecution: &taskcore.ExecutionContext{
			TaskID: parent.Mailbox.TaskID, Owner: taskcore.OwnerParent,
			Generation: parent.Mailbox.Generation,
		},
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: child.Spec.ID, ExpectedVersion: child.Version,
	})
	require.NoError(t, err)
	parentInputs, err := store.ListInputs(ctx, &taskcore.ListInputsRequest{
		TaskID: parent.Mailbox.TaskID,
	})
	require.NoError(t, err)
	require.NoError(t, store.AdvanceCursor(ctx, &taskcore.AdvanceCursorRequest{
		TaskID: parent.Mailbox.TaskID, ExpectedCursor: 0,
		Cursor:             parentInputs.LatestSequence,
		ExpectedGeneration: parent.Mailbox.Generation,
	}))
	_, err = store.SealIfIdle(ctx, &taskcore.SealMailboxRequest{
		TaskID:             parent.Mailbox.TaskID,
		ExpectedCursor:     parentInputs.LatestSequence,
		ExpectedGeneration: parent.Mailbox.Generation,
	})
	require.NoError(t, err)

	request := &NotifyParentRequest{
		EventID: "event", Kind: "application.event", Data: []byte("data"),
	}
	require.ErrorIs(
		t,
		store.EnqueueTaskNotification(ctx, child.Spec.ID, started.Attempt, request),
		taskcore.ErrMailboxSealed,
	)
	require.ErrorIs(
		t,
		store.EnqueueTaskNotification(ctx, child.Spec.ID, started.Attempt, request),
		taskcore.ErrMailboxSealed,
	)
	completed, err := store.CompleteIfNoInputs(ctx, &CompleteIfNoInputsRequest{
		TaskID: child.Spec.ID, ExpectedVersion: started.Version,
		Attempt: started.Attempt, InputCursor: 0, ResultData: []byte("done"),
	})
	require.NoError(t, err)
	require.Equal(t, taskcore.ErrMailboxSealed.Error(), completed.ParentNotificationError)
}

func TestAttack_NotificationReplayDetectsDeliveryConflict(t *testing.T) {
	store := NewInMemoryStore(nil)
	ctx := context.Background()
	parent, err := store.Register(ctx, &taskcore.RegisterMailboxRequest{
		CandidateTaskID: "parent", InvocationID: "parent",
	})
	require.NoError(t, err)
	child, err := store.Create(ctx, &CreateTaskRequest{
		Spec: Spec{
			ID: "child", ExecutorKey: "executor", Kind: "tool",
			ParentTaskID: parent.Mailbox.TaskID,
		},
		LeaseExpiryPolicy: LeaseExpiryRetry,
		ParentExecution: &taskcore.ExecutionContext{
			TaskID: parent.Mailbox.TaskID, Owner: taskcore.OwnerParent,
			Generation: parent.Mailbox.Generation,
		},
	})
	require.NoError(t, err)
	started, err := store.Start(ctx, &StartTaskRequest{
		TaskID: child.Spec.ID, ExpectedVersion: child.Version,
	})
	require.NoError(t, err)
	require.NoError(t, store.EnqueueTaskNotification(
		ctx,
		child.Spec.ID,
		started.Attempt,
		&NotifyParentRequest{
			EventID: "event", Kind: "application.event",
			Delivery: taskcore.InputQueued,
		},
	))
	err = store.EnqueueTaskNotification(
		ctx,
		child.Spec.ID,
		started.Attempt,
		&NotifyParentRequest{
			EventID: "event", Kind: "application.event",
			Delivery: taskcore.InputPreempt,
		},
	)
	require.ErrorIs(t, err, ErrNotificationEventIDConflict)
}
