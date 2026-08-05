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

// Package storetest provides reusable conformance suites for background-task
// persistence providers.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

// TaskStoreConfig configures TaskStore conformance. New returns an isolated
// provider for each subtest. ExpireActiveAttempt must wait for or advance the
// provider until the supplied running attempt's lease has expired.
type TaskStoreConfig struct {
	New                 func(testing.TB) backgroundtask.TaskStore
	ExpireActiveAttempt func(testing.TB, backgroundtask.TaskStore, *backgroundtask.Task)
}

// TaskEventStoreConfig configures TaskEventStore conformance. New returns
// lifecycle and event capabilities sharing one task namespace.
type TaskEventStoreConfig struct {
	New func(testing.TB) (backgroundtask.TaskStore, backgroundtask.TaskEventStore)
}

// NotificationOutboxConfig configures NotificationOutbox conformance. New
// returns lifecycle and outbox capabilities sharing one task namespace.
// ExpireLease must wait for or advance the provider past the requested lease.
type NotificationOutboxConfig struct {
	New         func(testing.TB) (backgroundtask.TaskStore, backgroundtask.NotificationOutbox)
	ExpireLease func(testing.TB, backgroundtask.NotificationOutbox, time.Duration)
}

// RunTaskStoreConformance checks lifecycle transitions, CAS, cancellation,
// pagination, ownership, and lease-expiry recovery.
func RunTaskStoreConformance(t *testing.T, config TaskStoreConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	require.NotNil(t, config.ExpireActiveAttempt)

	t.Run("create_owns_timestamp_and_snapshot", func(t *testing.T) {
		store := config.New(t)
		spec := testSpec("create")
		created := create(t, store, spec, backgroundtask.LeaseExpiryRetry)
		require.False(t, created.CreatedAt.IsZero())
		require.Equal(t, created.CreatedAt, created.UpdatedAt)
		require.Equal(t, backgroundtask.StatusPending, created.Status)
		spec.Payload[0] = 'X'
		created.Spec.Payload[0] = 'Y'
		stored, err := store.Get(context.Background(), spec.ID)
		require.NoError(t, err)
		require.Equal(t, "payload", string(stored.Spec.Payload))
	})

	t.Run("transitions_and_cas", func(t *testing.T) {
		store := config.New(t)
		started := createAndStart(t, store, "transitions", backgroundtask.LeaseExpiryRetry)
		_, err := store.Heartbeat(context.Background(), &backgroundtask.HeartbeatRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version - 1,
		})
		require.ErrorIs(t, err, backgroundtask.ErrVersionConflict)
		heartbeat, err := store.Heartbeat(context.Background(), &backgroundtask.HeartbeatRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		completed, err := store.Complete(context.Background(), &backgroundtask.CompleteTaskRequest{
			TaskID: heartbeat.Spec.ID, ExpectedVersion: heartbeat.Version, Data: []byte("done"),
		})
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusCompleted, completed.Status)
		require.Equal(t, "done", string(completed.ResultData))
		require.NotNil(t, completed.DoneAt)
		_, err = store.Fail(context.Background(), &backgroundtask.FailTaskRequest{
			TaskID: completed.Spec.ID, ExpectedVersion: completed.Version, Error: "late",
		})
		require.Error(t, err)
		require.True(t,
			errors.Is(err, backgroundtask.ErrAlreadyTerminal) ||
				errors.Is(err, backgroundtask.ErrLeaseLost),
		)
	})

	t.Run("waiting_resume_suspend_release_and_yield", func(t *testing.T) {
		store := config.New(t)
		started := createAndStart(t, store, "waiting", backgroundtask.LeaseExpiryRetry)
		waiting, err := store.WaitInput(context.Background(), &backgroundtask.WaitInputTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version, Checkpoint: []byte("cp"),
		})
		require.NoError(t, err)
		resumed, err := store.Resume(context.Background(), &backgroundtask.ResumeRequest{
			TaskID: waiting.Spec.ID, ExpectedVersion: waiting.Version, Data: []byte("input"),
		})
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusPending, resumed.Status)
		require.Equal(t, "input", string(resumed.PendingResume))
		started, err = store.Start(context.Background(), &backgroundtask.StartTaskRequest{
			TaskID: resumed.Spec.ID, ExpectedVersion: resumed.Version,
		})
		require.NoError(t, err)
		suspended, err := store.Suspend(context.Background(), &backgroundtask.SuspendTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version, Checkpoint: []byte("safe"),
		})
		require.NoError(t, err)
		released, err := store.ReleaseSuspension(context.Background(), &backgroundtask.ReleaseSuspensionRequest{
			TaskID: suspended.Spec.ID, ExpectedVersion: suspended.Version,
		})
		require.NoError(t, err)
		started, err = store.Start(context.Background(), &backgroundtask.StartTaskRequest{
			TaskID: released.Spec.ID, ExpectedVersion: released.Version,
		})
		require.NoError(t, err)
		yielded, err := store.Yield(context.Background(), &backgroundtask.YieldTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusPending, yielded.Status)
		require.Equal(t, "safe", string(yielded.Checkpoint))
	})

	t.Run("cancellation_is_first_write_and_fences", func(t *testing.T) {
		store := config.New(t)
		started := createAndStart(t, store, "cancel", backgroundtask.LeaseExpiryRetry)
		requested, err := store.RequestCancel(context.Background(), &backgroundtask.RequestCancelRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version, Reason: "first",
		})
		require.NoError(t, err)
		repeated, err := store.RequestCancel(context.Background(), &backgroundtask.RequestCancelRequest{
			TaskID: requested.Spec.ID, ExpectedVersion: requested.Version, Reason: "second",
		})
		require.NoError(t, err)
		require.Equal(t, "first", repeated.CancelReason)
		_, err = store.Complete(context.Background(), &backgroundtask.CompleteTaskRequest{
			TaskID: repeated.Spec.ID, ExpectedVersion: repeated.Version,
		})
		require.ErrorIs(t, err, backgroundtask.ErrLeaseLost)
		canceled, err := store.AckCancel(context.Background(), &backgroundtask.AckCancelRequest{
			TaskID: repeated.Spec.ID, ExpectedVersion: repeated.Version,
		})
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusCanceled, canceled.Status)
		require.Equal(t, "first", canceled.ResultError)
	})

	t.Run("listing_and_cursor", func(t *testing.T) {
		store := config.New(t)
		for _, id := range []string{"b", "a", "c"} {
			create(t, store, testSpec(id), backgroundtask.LeaseExpiryRetry)
		}
		first, err := store.ListPending(context.Background(), &backgroundtask.ListPendingRequest{
			ExecutorKeys: []string{"test"}, Limit: 2,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}, taskIDs(first.Tasks))
		require.NotEmpty(t, first.NextCursor)
		second, err := store.ListPending(context.Background(), &backgroundtask.ListPendingRequest{
			ExecutorKeys: []string{"test"}, Cursor: first.NextCursor, Limit: 2,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"c"}, taskIDs(second.Tasks))
		require.Empty(t, second.NextCursor)
	})

	for _, policy := range []backgroundtask.LeaseExpiryPolicy{
		backgroundtask.LeaseExpiryRetry, backgroundtask.LeaseExpiryFail,
	} {
		t.Run("lease_expiry_"+string(policy), func(t *testing.T) {
			store := config.New(t)
			started := createAndStart(t, store, "lease-"+string(policy), policy)
			config.ExpireActiveAttempt(t, store, started)
			expired, err := store.Get(context.Background(), started.Spec.ID)
			require.NoError(t, err)
			if policy == backgroundtask.LeaseExpiryRetry {
				require.Equal(t, backgroundtask.StatusPending, expired.Status)
			} else {
				require.Equal(t, backgroundtask.StatusFailed, expired.Status)
			}
		})
	}
}

// RunTaskEventStoreConformance checks attempt fencing, replay identity,
// append ordering, cursor validation, and snapshot-stable pagination.
func RunTaskEventStoreConformance(t *testing.T, config TaskEventStoreConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	tasks, events := config.New(t)
	started := createAndStart(t, tasks, "events", backgroundtask.LeaseExpiryRetry)
	appendEvent(t, events, started, "one", "one")
	replay, err := events.AppendTaskEvent(context.Background(), &backgroundtask.AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("one"),
	})
	require.NoError(t, err)
	require.False(t, replay.Inserted)
	_, err = events.AppendTaskEvent(context.Background(), &backgroundtask.AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("different"),
	})
	require.ErrorIs(t, err, backgroundtask.ErrTaskEventIDConflict)
	appendEvent(t, events, started, "two", "two")
	appendEvent(t, events, started, "three", "three")
	first, err := events.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, eventIDs(first.Events))
	appendEvent(t, events, started, "four", "four")
	second, err := events.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: started.Spec.ID, Cursor: first.NextCursor, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"three"}, eventIDs(second.Events))
	recent, err := events.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2, NewestFirst: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"four", "three"}, eventIDs(recent.Events))

	yielded, err := tasks.Yield(context.Background(), &backgroundtask.YieldTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	restarted, err := tasks.Start(context.Background(), &backgroundtask.StartTaskRequest{
		TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
	})
	require.NoError(t, err)
	_, err = events.AppendTaskEvent(context.Background(), &backgroundtask.AppendTaskEventRequest{
		TaskID: restarted.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("one"),
	})
	require.ErrorIs(t, err, backgroundtask.ErrLeaseLost)

	other := create(t, tasks, testSpec("other"), backgroundtask.LeaseExpiryRetry)
	_, err = events.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: other.Spec.ID, Cursor: recent.NextCursor, NewestFirst: true,
	})
	require.ErrorIs(t, err, backgroundtask.ErrInvalidCursor)
}

// RunNotificationOutboxConformance checks lease exclusion, expiry, redelivery,
// stale-receipt rejection, and acknowledgement.
func RunNotificationOutboxConformance(t *testing.T, config NotificationOutboxConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	require.NotNil(t, config.ExpireLease)
	tasks, outbox := config.New(t)
	spec := testSpec("notification")
	spec.SessionID = "session"
	create(t, tasks, spec, backgroundtask.LeaseExpiryRetry)
	lease := 20 * time.Millisecond
	first, err := outbox.Receive(context.Background(), &backgroundtask.ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Len(t, first.Deliveries, 1)
	require.Equal(t, backgroundtask.NotificationTaskCreated, first.Deliveries[0].Record.Kind)
	require.Equal(t, spec.SessionID, first.Deliveries[0].Record.SessionID)
	concurrent, err := outbox.Receive(context.Background(), &backgroundtask.ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Empty(t, concurrent.Deliveries)
	config.ExpireLease(t, outbox, lease)
	require.ErrorIs(t, outbox.Ack(context.Background(), first.Deliveries[0].Receipt), backgroundtask.ErrLeaseLost)
	second, err := outbox.Receive(context.Background(), &backgroundtask.ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Len(t, second.Deliveries, 1)
	require.NotEqual(t, first.Deliveries[0].Receipt, second.Deliveries[0].Receipt)
	require.Error(t, outbox.Ack(context.Background(), first.Deliveries[0].Receipt))
	require.NoError(t, outbox.Ack(context.Background(), second.Deliveries[0].Receipt))
}

func testSpec(id string) backgroundtask.Spec {
	return backgroundtask.Spec{
		ID: id, ExecutorKey: "test", Payload: []byte("payload"),
	}
}

func create(
	t testing.TB,
	store backgroundtask.TaskStore,
	spec backgroundtask.Spec,
	policy backgroundtask.LeaseExpiryPolicy,
) *backgroundtask.Task {
	t.Helper()
	task, err := store.Create(context.Background(), &backgroundtask.CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: policy,
	})
	require.NoError(t, err)
	return task
}

func createAndStart(
	t testing.TB,
	store backgroundtask.TaskStore,
	id string,
	policy backgroundtask.LeaseExpiryPolicy,
) *backgroundtask.Task {
	t.Helper()
	created := create(t, store, testSpec(id), policy)
	started, err := store.Start(context.Background(), &backgroundtask.StartTaskRequest{
		TaskID: id, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	return started
}

func appendEvent(
	t testing.TB,
	store backgroundtask.TaskEventStore,
	task *backgroundtask.Task,
	id string,
	data string,
) {
	t.Helper()
	result, err := store.AppendTaskEvent(context.Background(), &backgroundtask.AppendTaskEventRequest{
		TaskID: task.Spec.ID, Attempt: task.Attempt, EventID: id, Data: []byte(data),
	})
	require.NoError(t, err)
	require.True(t, result.Inserted)
}

func taskIDs(tasks []*backgroundtask.Task) []string {
	result := make([]string, len(tasks))
	for i, task := range tasks {
		result[i] = task.Spec.ID
	}
	return result
}

func eventIDs(events []*backgroundtask.TaskEvent) []string {
	result := make([]string, len(events))
	for i, event := range events {
		result[i] = event.EventID
	}
	return result
}
