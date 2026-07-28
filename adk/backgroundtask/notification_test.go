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
	notifications []TaskNotification
}

func (s *routedRecordingSink) Accept(context.Context, TaskNotification) error {
	return errors.New("unexpected non-routed delivery")
}

func (s *routedRecordingSink) AcceptTarget(
	_ context.Context,
	target NotificationTarget,
	notification TaskNotification,
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
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	task, lease := createAndClaim(t, store, "dispatch", 10*time.Second)
	committed, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Mutation: mutationForStatus(StatusCompleted),
	})
	require.NoError(t, err)

	// Ack the claim update so the test isolates the terminal record.
	initial, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		ConsumerID: "setup", Limit: 1, VisibilityTime: time.Second,
	})
	require.NoError(t, err)
	require.Len(t, initial.Deliveries, 1)
	require.NoError(t, store.Ack(context.Background(), initial.Deliveries[0].Receipt))

	sink := &routedRecordingSink{fail: true}
	registry := NewSinkRegistry()
	require.NoError(t, registry.Register("session_inbox", sink))
	dispatcher := &Dispatcher{
		Outbox: store, Sinks: registry, ConsumerID: "dispatcher",
		BatchSize: 10, Visibility: time.Second,
	}
	accepted, err := dispatcher.DispatchOnce(context.Background())
	require.Error(t, err)
	assert.Zero(t, accepted)
	require.Len(t, sink.notifications, 1)
	assert.Equal(t, committed.Notification.NotificationID, sink.notifications[0].NotificationID)
	assert.Equal(t, task.Spec.SessionID, sink.targets[0].TargetID)

	clock.Advance(time.Second)
	sink.fail = false
	accepted, err = dispatcher.DispatchOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, accepted)
	require.Len(t, sink.notifications, 2)
	assert.Equal(t, sink.notifications[0].NotificationID, sink.notifications[1].NotificationID)

	empty, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		ConsumerID: "verify", Limit: 10, VisibilityTime: time.Second,
	})
	require.NoError(t, err)
	assert.Empty(t, empty.Deliveries)
}

func TestMemoryStoreTerminalCommitCreatesOneTerminalOutbox_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	_, lease := createAndClaim(t, store, "one-terminal", time.Minute)
	terminal, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Mutation: mutationForStatus(StatusCompleted),
	})
	require.NoError(t, err)
	require.NotNil(t, terminal.Notification)
	assert.Equal(t, NotificationCompleted, terminal.Notification.EventKind)
	assert.Equal(t, terminal.Task.LatestUpdateSequence, terminal.Notification.UpdateSequence)

	_, err = store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Mutation: mutationForStatus(StatusCompleted),
	})
	require.Error(t, err)

	deliveries, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		ConsumerID: "consumer", Limit: 10, VisibilityTime: time.Minute,
	})
	require.NoError(t, err)
	terminalCount := 0
	for _, delivery := range deliveries.Deliveries {
		if delivery.Record.EventKind == NotificationCompleted {
			terminalCount++
		}
	}
	assert.Equal(t, 1, terminalCount)
}

func TestRouteLessTaskNeverCreatesOutbox_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	spec := validSpec("route-less")
	spec.Notify = nil
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "worker", LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	_, err = store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: claim.Lease, Kind: UpdateMessage,
		Payload: &UpdatePayload{Type: "text/plain", Value: validInline("message")},
	})
	require.NoError(t, err)

	outbox, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		ConsumerID: "consumer", Limit: 10, VisibilityTime: time.Minute,
	})
	require.NoError(t, err)
	assert.Empty(t, outbox.Deliveries)
}
