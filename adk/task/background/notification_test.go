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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryStoreTerminalCommitCreatesOneTerminalOutbox_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "one-terminal")
	terminal, err := store.CompleteIfNoInputs(
		context.Background(),
		&CompleteIfNoInputsRequest{
			TaskID: "one-terminal", ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0, ResultData: []byte("result"),
		},
	)
	require.NoError(t, err)

	_, err = store.CompleteIfNoInputs(
		context.Background(),
		&CompleteIfNoInputsRequest{
			TaskID: "one-terminal", ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0, ResultData: []byte("result"),
		},
	)
	require.Error(t, err)

	deliveries, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 10, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	terminalCount := 0
	for _, delivery := range deliveries.Deliveries {
		if delivery.Record.Kind == NotificationCompleted {
			assert.Equal(t, terminal.Version, delivery.Record.Version)
			terminalCount++
		}
	}
	assert.Equal(t, 1, terminalCount)
}

func TestTaskWithoutLifecycleNotificationsStillCreatesRecoveryOutbox_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	spec := validSpec("route-less")
	spec.NotifySession = false
	created, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	_, err = store.CompleteIfNoInputs(
		context.Background(),
		&CompleteIfNoInputsRequest{
			TaskID: spec.ID, ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0, ResultData: []byte("done"),
		},
	)
	require.NoError(t, err)

	outbox, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 10, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, outbox.Deliveries, 1)
	assert.Equal(t, NotificationTaskCreated, outbox.Deliveries[0].Record.Kind)
}

func TestInMemoryStoreAckRequiresCurrentUnexpiredLease_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(500, 0)}
	store := newInMemoryStoreWithClock(nil, clock.Now)
	spec := validSpec("lease-validation")
	spec.NotifySession = false
	_, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)

	first, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Len(t, first.Deliveries, 1)
	firstReceipt := first.Deliveries[0].Receipt

	concurrent, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Empty(t, concurrent.Deliveries)

	clock.Advance(time.Second)
	require.ErrorIs(t, store.Ack(context.Background(), firstReceipt), ErrLeaseLost)

	second, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Len(t, second.Deliveries, 1)
	secondReceipt := second.Deliveries[0].Receipt
	require.NotEqual(t, firstReceipt, secondReceipt)

	require.Error(t, store.Ack(context.Background(), firstReceipt))
	require.NoError(t, store.Ack(context.Background(), secondReceipt))
}

func TestExpiredNotificationWriteDoesNotResolveTaskLease_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(600, 0)}
	store := newInMemoryStoreWithClock(&InMemoryStoreConfig{
		ActiveAttemptTimeout: time.Second,
	}, clock.Now)
	started := createAndStart(t, store, "expired-notification")
	clock.Advance(time.Second)

	err := store.EnqueueTaskNotification(
		context.Background(),
		started.Spec.ID,
		started.Attempt,
		&NotifyParentRequest{EventID: "event", Kind: "application.progress"},
	)
	require.ErrorIs(t, err, ErrLeaseLost)

	store.mu.Lock()
	stored := cloneTask(store.tasks[started.Spec.ID])
	active := store.active[started.Spec.ID]
	store.mu.Unlock()
	require.Equal(t, started, stored)
	require.Equal(t, clock.Now(), active.expiresAt)
}

func TestInMemoryNotificationWriterRejectsInvalidAuthority_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	req := &NotifyParentRequest{
		EventID: "event", Kind: "application.progress",
	}
	require.EqualError(
		t,
		store.EnqueueTaskNotification(context.Background(), "", 1, req),
		"task/background: notification task id and attempt are required",
	)
	require.EqualError(
		t,
		store.EnqueueTaskNotification(context.Background(), "task", 0, req),
		"task/background: notification task id and attempt are required",
	)
	require.ErrorIs(
		t,
		store.EnqueueTaskNotification(context.Background(), "missing", 1, req),
		ErrNotFound,
	)

	spec := validSpec("no-parent")
	spec.SessionID = ""
	spec.NotifySession = false
	created, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	require.ErrorIs(
		t,
		store.EnqueueTaskNotification(
			context.Background(), started.Spec.ID, started.Attempt, req,
		),
		ErrNotificationUnavailable,
	)
}
