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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// taskStoreConformanceConfig configures TaskStore conformance. New returns an isolated
// provider for each subtest. ExpireActiveAttempt must wait for or advance the
// provider until the supplied running attempt's lease has expired.
type taskStoreConformanceConfig struct {
	New                 func(testing.TB) TaskStore
	ExpireActiveAttempt func(testing.TB, TaskStore, *Task)
}

// taskEventStoreConformanceConfig configures TaskEventStore conformance. New returns
// lifecycle and event capabilities sharing one task namespace.
type taskEventStoreConformanceConfig struct {
	New func(testing.TB) (TaskStore, TaskEventStore)
}

// notificationOutboxConformanceConfig configures NotificationOutbox conformance. New
// returns lifecycle and outbox capabilities sharing one task namespace.
// ExpireLease must wait for or advance the provider past the requested lease.
type notificationOutboxConformanceConfig struct {
	New         func(testing.TB) (TaskStore, NotificationOutbox)
	ExpireLease func(testing.TB, NotificationOutbox, time.Duration)
}

// notificationWriterConformanceConfig configures NotificationWriter conformance. New
// returns lifecycle and outbox capabilities sharing one task namespace; the
// returned TaskStore must also implement NotificationWriter.
type notificationWriterConformanceConfig struct {
	New                 func(testing.TB) (TaskStore, NotificationOutbox)
	ExpireActiveAttempt func(testing.TB, TaskStore, *Task)
}

// runTaskStoreConformance checks lifecycle transitions, CAS, cancellation,
// pagination, ownership, and lease-expiry recovery.
func runTaskStoreConformance(t *testing.T, config taskStoreConformanceConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	require.NotNil(t, config.ExpireActiveAttempt)

	t.Run("create_owns_timestamp_and_snapshot", func(t *testing.T) {
		runCreateSnapshotConformance(t, config.New(t))
	})

	t.Run("create_copies_initial_checkpoint", func(t *testing.T) {
		runCreateInitialCheckpointConformance(t, config.New(t))
	})

	t.Run("transitions_and_cas", func(t *testing.T) {
		store := config.New(t)
		started := createAndStartConformance(t, store, "transitions", LeaseExpiryRetry)
		_, err := store.Heartbeat(context.Background(), &HeartbeatRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version - 1,
		})
		require.ErrorIs(t, err, ErrVersionConflict)
		heartbeat, err := store.Heartbeat(context.Background(), &HeartbeatRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		completed, err := store.Complete(context.Background(), &CompleteTaskRequest{
			TaskID: heartbeat.Spec.ID, ExpectedVersion: heartbeat.Version, Data: []byte("done"),
		})
		require.NoError(t, err)
		require.Equal(t, StatusCompleted, completed.Status)
		require.Equal(t, "done", string(completed.ResultData))
		require.NotNil(t, completed.DoneAt)
		_, err = store.Fail(context.Background(), &FailTaskRequest{
			TaskID: completed.Spec.ID, ExpectedVersion: completed.Version, Error: "late",
		})
		require.Error(t, err)
		require.True(t,
			errors.Is(err, ErrAlreadyTerminal) ||
				errors.Is(err, ErrLeaseLost),
		)
	})

	t.Run("start_commit_is_owned_and_retained", func(t *testing.T) {
		store := config.New(t)
		started := createAndStartConformance(
			t,
			store,
			"running-checkpoint",
			LeaseExpiryRetry,
		)
		checkpoint := []byte("recovery")
		saved, err := store.CommitStart(
			context.Background(),
			&CommitStartRequest{
				TaskID: started.Spec.ID, ExpectedVersion: started.Version,
				Checkpoint: checkpoint,
			},
		)
		require.NoError(t, err)
		checkpoint[0] = 'X'
		require.Equal(t, StatusRunning, saved.Status)
		require.Equal(t, started.Version+1, saved.Version)
		require.Equal(t, "recovery", string(saved.Checkpoint))
		_, err = store.CommitStart(
			context.Background(),
			&CommitStartRequest{
				TaskID: saved.Spec.ID, ExpectedVersion: started.Version,
				Checkpoint: []byte("stale"),
			},
		)
		require.ErrorIs(t, err, ErrVersionConflict)
		_, err = store.CommitStart(
			context.Background(),
			&CommitStartRequest{
				TaskID: saved.Spec.ID, ExpectedVersion: saved.Version,
				Checkpoint: []byte("duplicate"),
			},
		)
		require.ErrorIs(t, err, ErrIllegalTransition)
		yielded, err := store.Yield(
			context.Background(),
			&YieldTaskRequest{
				TaskID: saved.Spec.ID, ExpectedVersion: saved.Version,
			},
		)
		require.NoError(t, err)
		require.Equal(t, "recovery", string(yielded.Checkpoint))
	})

	t.Run("waiting_resume_suspend_release_and_yield", func(t *testing.T) {
		store := config.New(t)
		started := createAndStartConformance(t, store, "waiting", LeaseExpiryRetry)
		waiting, err := store.WaitInput(context.Background(), &WaitInputTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version, Checkpoint: []byte("cp"),
		})
		require.NoError(t, err)
		resumed, err := store.Resume(context.Background(), &ResumeRequest{
			TaskID: waiting.Spec.ID, ExpectedVersion: waiting.Version, Data: []byte("input"),
		})
		require.NoError(t, err)
		require.Equal(t, StatusPending, resumed.Status)
		require.Equal(t, "input", string(resumed.PendingResume))
		started, err = store.Start(context.Background(), &StartTaskRequest{
			TaskID: resumed.Spec.ID, ExpectedVersion: resumed.Version,
		})
		require.NoError(t, err)
		yielded, err := store.Yield(context.Background(), &YieldTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		require.Equal(t, "input", string(yielded.PendingResume))
		started, err = store.Start(context.Background(), &StartTaskRequest{
			TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
		})
		require.NoError(t, err)
		suspended, err := store.Suspend(context.Background(), &SuspendTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version, Checkpoint: []byte("safe"),
		})
		require.NoError(t, err)
		released, err := store.ReleaseSuspension(context.Background(), &ReleaseSuspensionRequest{
			TaskID: suspended.Spec.ID, ExpectedVersion: suspended.Version,
		})
		require.NoError(t, err)
		started, err = store.Start(context.Background(), &StartTaskRequest{
			TaskID: released.Spec.ID, ExpectedVersion: released.Version,
		})
		require.NoError(t, err)
		yielded, err = store.Yield(context.Background(), &YieldTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		require.Equal(t, StatusPending, yielded.Status)
		require.Equal(t, "safe", string(yielded.Checkpoint))
	})

	t.Run("cancellation_is_first_write_and_fences", func(t *testing.T) {
		store := config.New(t)
		started := createAndStartConformance(t, store, "cancel", LeaseExpiryRetry)
		requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version, Reason: "first",
		})
		require.NoError(t, err)
		repeated, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
			TaskID: requested.Spec.ID, ExpectedVersion: requested.Version, Reason: "second",
		})
		require.NoError(t, err)
		require.Equal(t, "first", repeated.CancelReason)
		_, err = store.Complete(context.Background(), &CompleteTaskRequest{
			TaskID: repeated.Spec.ID, ExpectedVersion: repeated.Version,
		})
		require.ErrorIs(t, err, ErrLeaseLost)
		canceled, err := store.AckCancel(context.Background(), &AckCancelRequest{
			TaskID: repeated.Spec.ID, ExpectedVersion: repeated.Version,
		})
		require.NoError(t, err)
		require.Equal(t, StatusCanceled, canceled.Status)
		require.Equal(t, "first", canceled.ResultError)
	})

	t.Run("listing_and_cursor", func(t *testing.T) {
		store := config.New(t)
		for _, id := range []string{"b", "a", "c"} {
			create(t, store, testSpec(id), LeaseExpiryRetry)
		}
		first, err := store.ListPending(context.Background(), &ListPendingRequest{
			ExecutorKeys: []string{"test"}, Limit: 2,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}, taskIDs(first.Tasks))
		require.NotEmpty(t, first.NextCursor)
		second, err := store.ListPending(context.Background(), &ListPendingRequest{
			ExecutorKeys: []string{"test"}, Cursor: first.NextCursor, Limit: 2,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"c"}, taskIDs(second.Tasks))
		require.Empty(t, second.NextCursor)
	})

	for _, policy := range []LeaseExpiryPolicy{
		LeaseExpiryRetry, LeaseExpiryFail,
	} {
		t.Run("lease_expiry_"+string(policy), func(t *testing.T) {
			store := config.New(t)
			started := createAndStartConformance(t, store, "lease-"+string(policy), policy)
			config.ExpireActiveAttempt(t, store, started)
			expired, err := store.Get(context.Background(), started.Spec.ID)
			require.NoError(t, err)
			if policy == LeaseExpiryRetry {
				require.Equal(t, StatusPending, expired.Status)
			} else {
				require.Equal(t, StatusFailed, expired.Status)
			}
		})
	}
}

// runTaskEventStoreConformance validates append ordering, cursor validation,
// and snapshot-stable pagination.
func runTaskEventStoreConformance(t *testing.T, config taskEventStoreConformanceConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	tasks, events := config.New(t)
	started := createAndStartConformance(t, tasks, "events", LeaseExpiryRetry)
	appendEvent(t, events, started, "one", "one")
	replay, err := events.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("one"),
	})
	require.NoError(t, err)
	require.False(t, replay.Inserted)
	_, err = events.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("different"),
	})
	require.ErrorIs(t, err, ErrTaskEventIDConflict)
	appendEvent(t, events, started, "two", "two")
	appendEvent(t, events, started, "three", "three")
	first, err := events.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, eventIDs(first.Events))
	appendEvent(t, events, started, "four", "four")
	second, err := events.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Cursor: first.NextCursor, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"three"}, eventIDs(second.Events))
	recent, err := events.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2, NewestFirst: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"four", "three"}, eventIDs(recent.Events))

	yielded, err := tasks.Yield(context.Background(), &YieldTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	restarted, err := tasks.Start(context.Background(), &StartTaskRequest{
		TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
	})
	require.NoError(t, err)
	_, err = events.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: restarted.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("one"),
	})
	require.ErrorIs(t, err, ErrLeaseLost)

	other := create(t, tasks, testSpec("other"), LeaseExpiryRetry)
	_, err = events.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: other.Spec.ID, Cursor: recent.NextCursor, NewestFirst: true,
	})
	require.ErrorIs(t, err, ErrInvalidCursor)
}

// runNotificationOutboxConformance checks lease exclusion, expiry, redelivery,
// stale-receipt rejection, and acknowledgement.
func runNotificationOutboxConformance(t *testing.T, config notificationOutboxConformanceConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	require.NotNil(t, config.ExpireLease)
	tasks, outbox := config.New(t)
	spec := testSpec("notification")
	spec.SessionID = "session"
	create(t, tasks, spec, LeaseExpiryRetry)
	lease := 20 * time.Millisecond
	first, err := outbox.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Len(t, first.Deliveries, 1)
	require.Equal(t, NotificationTaskCreated, first.Deliveries[0].Record.Kind)
	require.Equal(t, spec.SessionID, first.Deliveries[0].Record.SessionID)
	concurrent, err := outbox.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Empty(t, concurrent.Deliveries)
	config.ExpireLease(t, outbox, lease)
	require.ErrorIs(t, outbox.Ack(context.Background(), first.Deliveries[0].Receipt), ErrLeaseLost)
	second, err := outbox.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Len(t, second.Deliveries, 1)
	require.NotEqual(t, first.Deliveries[0].Receipt, second.Deliveries[0].Receipt)
	require.Error(t, outbox.Ack(context.Background(), first.Deliveries[0].Receipt))
	require.NoError(t, outbox.Ack(context.Background(), second.Deliveries[0].Receipt))
}

// runNotificationWriterConformance checks authorization-before-replay,
// idempotency, bounds, identity, state preservation, and copy ownership.
func runNotificationWriterConformance(t *testing.T, config notificationWriterConformanceConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	require.NotNil(t, config.ExpireActiveAttempt)

	t.Run("replay_follows_authorization_and_survives_ack", func(t *testing.T) {
		tasks, outbox := config.New(t)
		writer := notificationWriter(t, tasks)
		started := createParentAndStart(t, tasks, "notify-replay")
		req := &NotifyParentRequest{
			EventID: "event", Kind: "application.update", Data: []byte("original"),
		}
		require.NoError(t, writer.EnqueueTaskNotification(
			context.Background(), started.Spec.ID, started.Attempt, req,
		))
		req.Data[0] = 'X'
		custom, lifecycle := receiveNotificationKinds(t, outbox)
		require.Equal(t, "original", string(custom.Record.Data))
		require.NotEqual(t, lifecycle.Record.ID, custom.Record.ID)
		require.NoError(t, outbox.Ack(context.Background(), custom.Receipt))

		yielded, err := tasks.Yield(context.Background(), &YieldTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		restarted, err := tasks.Start(context.Background(), &StartTaskRequest{
			TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
		})
		require.NoError(t, err)
		original := &NotifyParentRequest{
			EventID: "event", Kind: "application.update", Data: []byte("original"),
		}
		require.ErrorIs(t, writer.EnqueueTaskNotification(
			context.Background(),
			started.Spec.ID,
			started.Attempt,
			&NotifyParentRequest{
				EventID: "event", Kind: "application.changed", Data: []byte("changed"),
			},
		), ErrLeaseLost)
		require.NoError(t, writer.EnqueueTaskNotification(
			context.Background(), restarted.Spec.ID, restarted.Attempt, original,
		))
		require.ErrorIs(t, writer.EnqueueTaskNotification(
			context.Background(),
			restarted.Spec.ID,
			restarted.Attempt,
			&NotifyParentRequest{
				EventID: "event", Kind: "application.changed", Data: []byte("changed"),
			},
		), ErrNotificationEventIDConflict)
		afterReplay, err := outbox.Receive(
			context.Background(),
			&ReceiveNotificationsRequest{
				Limit: 100, LeaseDuration: time.Second,
			},
		)
		require.NoError(t, err)
		for _, delivery := range afterReplay.Deliveries {
			require.NotEqual(t, "application.update", string(delivery.Record.Kind))
		}

		config.ExpireActiveAttempt(t, tasks, restarted)
		require.ErrorIs(t, writer.EnqueueTaskNotification(
			context.Background(), restarted.Spec.ID, restarted.Attempt, original,
		), ErrLeaseLost)
		pending, err := tasks.Get(context.Background(), restarted.Spec.ID)
		require.NoError(t, err)
		current, err := tasks.Start(context.Background(), &StartTaskRequest{
			TaskID: pending.Spec.ID, ExpectedVersion: pending.Version,
		})
		require.NoError(t, err)
		canceled, err := tasks.RequestCancel(
			context.Background(),
			&RequestCancelRequest{
				TaskID: current.Spec.ID, ExpectedVersion: current.Version,
			},
		)
		require.NoError(t, err)
		require.ErrorIs(t, writer.EnqueueTaskNotification(
			context.Background(), canceled.Spec.ID, current.Attempt, original,
		), ErrLeaseLost)
	})

	t.Run("identity_state_version_and_copy_ownership", func(t *testing.T) {
		tasks, outbox := config.New(t)
		writer := notificationWriter(t, tasks)
		started := createParentAndStart(t, tasks, "notify-state")
		before, err := tasks.Get(context.Background(), started.Spec.ID)
		require.NoError(t, err)
		req := &NotifyParentRequest{
			EventID: "state-event", Kind: "application.state", Data: []byte("data"),
		}
		require.NoError(t, writer.EnqueueTaskNotification(
			context.Background(), started.Spec.ID, started.Attempt, req,
		))
		after, err := tasks.Get(context.Background(), started.Spec.ID)
		require.NoError(t, err)
		require.Equal(t, before, after)

		custom, _ := receiveNotificationKindsWithLease(t, outbox, time.Millisecond)
		require.Equal(t, started.Spec.ID, custom.Record.TaskID)
		require.Equal(t, "parent-session", custom.Record.SessionID)
		require.Equal(t, started.Version, custom.Record.Version)
		require.Equal(t, NotificationKind("application.state"), custom.Record.Kind)
		require.Equal(t, "data", string(custom.Record.Data))
		firstID := custom.Record.ID
		custom.Record.Data[0] = 'X'
		time.Sleep(2 * time.Millisecond)
		redelivered, _ := receiveNotificationKinds(t, outbox)
		require.Equal(t, firstID, redelivered.Record.ID)
		require.Equal(t, "data", string(redelivered.Record.Data))
		require.NoError(t, writer.EnqueueTaskNotification(
			context.Background(),
			started.Spec.ID,
			started.Attempt,
			&NotifyParentRequest{
				EventID: "state-event", Kind: "application.state", Data: []byte("data"),
			},
		))

		otherTasks, otherOutbox := config.New(t)
		otherWriter := notificationWriter(t, otherTasks)
		otherStarted := createParentAndStart(t, otherTasks, "notify-state")
		require.NoError(t, otherWriter.EnqueueTaskNotification(
			context.Background(), otherStarted.Spec.ID, otherStarted.Attempt,
			&NotifyParentRequest{
				EventID: "state-event", Kind: "application.state", Data: []byte("data"),
			},
		))
		otherCustom, _ := receiveNotificationKinds(t, otherOutbox)
		require.Equal(t, firstID, otherCustom.Record.ID)
	})

	t.Run("validation_bounds_and_nil_empty_replay", func(t *testing.T) {
		tasks, _ := config.New(t)
		writer := notificationWriter(t, tasks)
		started := createParentAndStart(t, tasks, "notify-validation")
		write := func(req *NotifyParentRequest) error {
			return writer.EnqueueTaskNotification(
				context.Background(), started.Spec.ID, started.Attempt, req,
			)
		}
		for _, req := range []*NotifyParentRequest{
			nil,
			{Kind: "application.valid"},
			{EventID: strings.Repeat("e", 1025), Kind: "application.valid"},
			{EventID: "empty-kind"},
			{EventID: "long-kind", Kind: NotificationKind(strings.Repeat("k", 65))},
			{EventID: "lifecycle", Kind: NotificationCompleted},
			{EventID: "reserved", Kind: "eino.application"},
			{EventID: "large-data", Kind: "application.valid", Data: make([]byte, (256<<10)+1)},
		} {
			require.Error(t, write(req))
		}
		require.NoError(t, write(&NotifyParentRequest{
			EventID: strings.Repeat("e", 1024),
			Kind:    NotificationKind(strings.Repeat("k", 64)),
			Data:    make([]byte, 256<<10),
		}))
		require.NoError(t, write(&NotifyParentRequest{
			EventID: "nil-empty", Kind: "application.empty",
		}))
		require.NoError(t, write(&NotifyParentRequest{
			EventID: "nil-empty", Kind: "application.empty", Data: []byte{},
		}))
	})
}

func notificationWriter(t testing.TB, tasks TaskStore) NotificationWriter {
	t.Helper()
	writer, ok := tasks.(NotificationWriter)
	require.True(t, ok)
	return writer
}

func createParentAndStart(t testing.TB, tasks TaskStore, id string) *Task {
	t.Helper()
	spec := testSpec(id)
	spec.SessionID = "parent-session"
	created := create(t, tasks, spec, LeaseExpiryRetry)
	started, err := tasks.Start(context.Background(), &StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	return started
}

func receiveNotificationKinds(t testing.TB, outbox NotificationOutbox) (NotificationDelivery, NotificationDelivery) {
	t.Helper()
	return receiveNotificationKindsWithLease(t, outbox, time.Second)
}

func receiveNotificationKindsWithLease(t testing.TB, outbox NotificationOutbox, lease time.Duration) (NotificationDelivery, NotificationDelivery) {
	t.Helper()
	result, err := outbox.Receive(
		context.Background(),
		&ReceiveNotificationsRequest{
			Limit: 100, LeaseDuration: lease,
		},
	)
	require.NoError(t, err)
	var custom NotificationDelivery
	var lifecycle NotificationDelivery
	for _, delivery := range result.Deliveries {
		if delivery.Record.Kind == NotificationTaskCreated {
			lifecycle = delivery
		} else {
			custom = delivery
		}
	}
	require.NotEmpty(t, custom.Record.ID)
	require.NotEmpty(t, lifecycle.Record.ID)
	return custom, lifecycle
}

func testSpec(id string) Spec {
	return Spec{
		ID: id, ExecutorKey: "test", Payload: []byte("payload"),
	}
}

func runCreateSnapshotConformance(t testing.TB, store TaskStore) {
	t.Helper()
	spec := testSpec("create")
	created := create(t, store, spec, LeaseExpiryRetry)
	require.False(t, created.CreatedAt.IsZero())
	require.Equal(t, created.CreatedAt, created.UpdatedAt)
	require.Equal(t, StatusPending, created.Status)
	spec.Payload[0] = 'X'
	created.Spec.Payload[0] = 'Y'
	stored, err := store.Get(context.Background(), spec.ID)
	require.NoError(t, err)
	require.Equal(t, "payload", string(stored.Spec.Payload))
}

func runCreateInitialCheckpointConformance(t testing.TB, store TaskStore) {
	t.Helper()
	spec := testSpec("initial-checkpoint")
	checkpoint := []byte("checkpoint")
	created, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: LeaseExpiryRetry,
		Checkpoint: checkpoint,
	})
	require.NoError(t, err)
	require.Equal(t, "checkpoint", string(created.Checkpoint))
	checkpoint[0] = 'X'
	created.Checkpoint[0] = 'Y'
	stored, err := store.Get(context.Background(), spec.ID)
	require.NoError(t, err)
	require.Equal(t, "checkpoint", string(stored.Checkpoint))
}

func create(t testing.TB, store TaskStore, spec Spec, policy LeaseExpiryPolicy) *Task {
	t.Helper()
	task, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: policy,
	})
	require.NoError(t, err)
	return task
}

func createAndStartConformance(t testing.TB, store TaskStore, id string, policy LeaseExpiryPolicy) *Task {
	t.Helper()
	created := create(t, store, testSpec(id), policy)
	started, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: id, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	return started
}

func appendEvent(t testing.TB, store TaskEventStore, task *Task, id string, data string) {
	t.Helper()
	result, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: task.Spec.ID, Attempt: task.Attempt, EventID: id, Data: []byte(data),
	})
	require.NoError(t, err)
	require.True(t, result.Inserted)
}

func taskIDs(tasks []*Task) []string {
	result := make([]string, len(tasks))
	for i, task := range tasks {
		result[i] = task.Spec.ID
	}
	return result
}

func eventIDs(events []*TaskEvent) []string {
	result := make([]string, len(events))
	for i, event := range events {
		result[i] = event.EventID
	}
	return result
}
