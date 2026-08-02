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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routedRecordingSink struct {
	fail          bool
	targets       []NotificationTarget
	notifications []Notification
}

func (s *routedRecordingSink) Accept(context.Context, Notification) error {
	return errors.New("unexpected non-routed delivery")
}

func (s *routedRecordingSink) AcceptTarget(
	_ context.Context,
	target NotificationTarget,
	notification Notification,
) error {
	s.targets = append(s.targets, target)
	s.notifications = append(s.notifications, notification)
	if s.fail {
		return errors.New("sink unavailable")
	}
	return nil
}

func TestDispatcherRedeliversUntilSinkAccepts_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(400, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, ActiveAttemptTimeout: time.Minute,
	})
	task := createAndStart(t, store, "dispatch")
	completed, err := store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: "dispatch", ExpectedVersion: task.Version, Data: []byte("result"),
	})
	require.NoError(t, err)

	sink := &routedRecordingSink{fail: true}
	registry := NewSinkRegistry()
	require.NoError(t, registry.Register("session_inbox", sink))
	dispatcher := &Dispatcher{
		Outbox: store, Store: store, Sinks: registry, ConsumerID: "dispatcher",
		BatchSize: 10, Visibility: time.Second,
	}
	accepted, err := dispatcher.DispatchOnce(context.Background())
	require.Error(t, err)
	assert.Zero(t, accepted)
	require.Len(t, sink.notifications, 1)
	assert.Equal(t, completed.Spec.ID, sink.notifications[0].TaskID)
	assert.Equal(t, task.Spec.SessionID, sink.targets[0].TargetID)
	require.NotNil(t, sink.notifications[0].Task)
	assert.Equal(t, StateCompleted, sink.notifications[0].Task.Status)

	clock.Advance(time.Second)
	sink.fail = false
	accepted, err = dispatcher.DispatchOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, accepted)
	require.Len(t, sink.notifications, 2)
	assert.Equal(t, sink.notifications[0].ID, sink.notifications[1].ID)

	empty, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		ConsumerID: "verify", Limit: 10, VisibilityTime: time.Second,
	})
	require.NoError(t, err)
	assert.Empty(t, empty.Deliveries)
}

func TestMemoryStoreTerminalCommitCreatesOneTerminalOutbox_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
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
		ConsumerID: "consumer", Limit: 10, VisibilityTime: time.Minute,
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

func TestRouteLessTaskNeverCreatesOutbox_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	spec := validSpec("route-less")
	spec.Notify = nil
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
		ConsumerID: "consumer", Limit: 10, VisibilityTime: time.Minute,
	})
	require.NoError(t, err)
	assert.Empty(t, outbox.Deliveries)
}
