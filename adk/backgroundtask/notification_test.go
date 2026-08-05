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

package backgroundtask

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
	terminal, err := store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: "one-terminal", ExpectedVersion: started.Version, Data: []byte("result"),
	})
	require.NoError(t, err)

	_, err = store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: "one-terminal", ExpectedVersion: started.Version, Data: []byte("result"),
	})
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

func TestTaskWithoutSessionNotificationNeverCreatesOutbox_BitsUT(t *testing.T) {
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
	_, err = store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: spec.ID, ExpectedVersion: started.Version, Data: []byte("done"),
	})
	require.NoError(t, err)

	outbox, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 10, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	assert.Empty(t, outbox.Deliveries)
}

func TestInMemoryStoreAckRequiresCurrentUnexpiredLease_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(500, 0)}
	store := newInMemoryStoreWithClock(nil, clock.Now)
	started := createAndStart(t, store, "lease-validation")
	_, err := store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
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
