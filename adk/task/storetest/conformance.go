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

// Package storetest provides reusable conformance suites for task
// persistence providers.
package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task/background"
)

// LifecycleStoreConfig configures LifecycleStore conformance. New returns an isolated
// provider for each subtest. ExpireActiveAttempt must wait for or advance the
// provider until the supplied running attempt's lease has expired.
type LifecycleStoreConfig struct {
	New                 func(testing.TB) background.LifecycleStore
	ExpireActiveAttempt func(testing.TB, background.LifecycleStore, *background.TaskSnapshot)
}

// TaskEventStoreConfig configures TaskEventStore conformance. New returns
// lifecycle and event capabilities sharing one task namespace.
type TaskEventStoreConfig struct {
	New func(testing.TB) (background.TaskStore, background.TaskEventStore)
}

// NotificationOutboxConfig configures NotificationOutbox conformance. New
// returns lifecycle and outbox capabilities sharing one task namespace.
// ExpireLease must wait for or advance the provider past the requested lease.
type NotificationOutboxConfig struct {
	New         func(testing.TB) (background.TaskStore, background.NotificationOutbox)
	ExpireLease func(testing.TB, background.NotificationOutbox, time.Duration)
}

// NotificationWriterConfig configures NotificationWriter conformance. New
// returns lifecycle and outbox capabilities sharing one task namespace; the
// returned TaskStore must also implement background.NotificationWriter.
type NotificationWriterConfig struct {
	New func(testing.TB) (
		background.TaskStore,
		background.NotificationOutbox,
	)
	ExpireActiveAttempt func(
		testing.TB,
		background.TaskStore,
		*background.TaskSnapshot,
	)
}

// RunLifecycleStoreConformance checks lifecycle transitions, CAS, cancellation,
// pagination, ownership, and lease-expiry recovery.
func RunLifecycleStoreConformance(t *testing.T, config LifecycleStoreConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	require.NotNil(t, config.ExpireActiveAttempt)

	t.Run("create_owns_timestamp_and_snapshot", func(t *testing.T) {
		runCreateSnapshotConformance(t, config.New(t))
	})

	t.Run("create_copies_initial_checkpoint", func(t *testing.T) {
		runCreateInitialCheckpointConformance(t, config.New(t))
	})

	runPublicationConformance(t, config.New)

	t.Run("transitions_and_cas", func(t *testing.T) {
		store := config.New(t)
		started := createAndStart(t, store, "transitions", background.LeaseExpiryRetry)
		_, err := store.Heartbeat(context.Background(), &background.HeartbeatRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version - 1,
		})
		require.ErrorIs(t, err, background.ErrVersionConflict)
		heartbeat, err := store.Heartbeat(context.Background(), &background.HeartbeatRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		completed, err := store.CompleteIfNoInputs(
			context.Background(),
			&background.CompleteIfNoInputsRequest{
				TaskID: heartbeat.Spec.ID, ExpectedVersion: heartbeat.Version,
				Attempt: heartbeat.Attempt, InputCursor: 0, ResultData: []byte("done"),
			},
		)
		require.NoError(t, err)
		require.Equal(t, background.StatusCompleted, completed.Status)
		require.Equal(t, "done", string(completed.ResultData))
		require.NotNil(t, completed.DoneAt)
		_, err = store.Fail(context.Background(), &background.FailTaskRequest{
			TaskID: completed.Spec.ID, ExpectedVersion: completed.Version, Error: "late",
		})
		require.Error(t, err)
		require.True(t,
			errors.Is(err, background.ErrAlreadyTerminal) ||
				errors.Is(err, background.ErrLeaseLost),
		)
	})

	t.Run("start_commit_is_owned_and_retained", func(t *testing.T) {
		store := config.New(t)
		started := createAndStart(
			t,
			store,
			"running-checkpoint",
			background.LeaseExpiryRetry,
		)
		checkpoint := []byte("recovery")
		saved, err := store.CommitStart(
			context.Background(),
			&background.CommitStartRequest{
				TaskID: started.Spec.ID, ExpectedVersion: started.Version,
				Checkpoint: checkpoint,
			},
		)
		require.NoError(t, err)
		checkpoint[0] = 'X'
		require.Equal(t, background.StatusRunning, saved.Status)
		require.Equal(t, started.Version+1, saved.Version)
		require.Equal(t, "recovery", string(saved.Checkpoint))
		_, err = store.CommitStart(
			context.Background(),
			&background.CommitStartRequest{
				TaskID: saved.Spec.ID, ExpectedVersion: started.Version,
				Checkpoint: []byte("stale"),
			},
		)
		require.ErrorIs(t, err, background.ErrVersionConflict)
		_, err = store.CommitStart(
			context.Background(),
			&background.CommitStartRequest{
				TaskID: saved.Spec.ID, ExpectedVersion: saved.Version,
				Checkpoint: []byte("duplicate"),
			},
		)
		require.ErrorIs(t, err, background.ErrIllegalTransition)
		yielded, err := store.Yield(
			context.Background(),
			&background.YieldTaskRequest{
				TaskID: saved.Spec.ID, ExpectedVersion: saved.Version,
			},
		)
		require.NoError(t, err)
		require.Equal(t, "recovery", string(yielded.Checkpoint))
	})

	t.Run("suspend_release_and_yield", func(t *testing.T) {
		store := config.New(t)
		started := createAndStart(t, store, "waiting", background.LeaseExpiryRetry)
		suspended, err := store.SuspendIfNoInputs(
			context.Background(),
			&background.SuspendIfNoInputsRequest{
				TaskID: started.Spec.ID, ExpectedVersion: started.Version,
				Attempt: started.Attempt, InputCursor: 0, Checkpoint: []byte("safe"),
			},
		)
		require.NoError(t, err)
		released, err := store.ReleaseSuspension(context.Background(), &background.ReleaseSuspensionRequest{
			TaskID: suspended.Spec.ID, ExpectedVersion: suspended.Version,
		})
		require.NoError(t, err)
		started, err = store.Start(context.Background(), &background.StartTaskRequest{
			TaskID: released.Spec.ID, ExpectedVersion: released.Version,
		})
		require.NoError(t, err)
		yielded, err := store.Yield(context.Background(), &background.YieldTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		require.Equal(t, background.StatusPending, yielded.Status)
		require.Equal(t, "safe", string(yielded.Checkpoint))
	})

	t.Run("cancellation_is_first_write_and_fences", func(t *testing.T) {
		store := config.New(t)
		started := createAndStart(t, store, "cancel", background.LeaseExpiryRetry)
		requested, err := store.RequestCancel(context.Background(), &background.RequestCancelRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version, Reason: "first",
		})
		require.NoError(t, err)
		repeated, err := store.RequestCancel(context.Background(), &background.RequestCancelRequest{
			TaskID: requested.Spec.ID, ExpectedVersion: requested.Version, Reason: "second",
		})
		require.NoError(t, err)
		require.Equal(t, "first", repeated.CancelReason)
		_, err = store.CompleteIfNoInputs(
			context.Background(),
			&background.CompleteIfNoInputsRequest{
				TaskID: repeated.Spec.ID, ExpectedVersion: repeated.Version,
				Attempt: repeated.Attempt, InputCursor: 0,
			},
		)
		require.ErrorIs(t, err, background.ErrLeaseLost)
		canceled, err := store.AckCancel(context.Background(), &background.AckCancelRequest{
			TaskID: repeated.Spec.ID, ExpectedVersion: repeated.Version,
		})
		require.NoError(t, err)
		require.Equal(t, background.StatusCanceled, canceled.Status)
		require.Equal(t, "first", canceled.ResultError)
	})

	t.Run("listing_and_cursor", func(t *testing.T) {
		store := config.New(t)
		for _, id := range []string{"b", "a", "c"} {
			create(t, store, testSpec(id), background.LeaseExpiryRetry)
		}
		first, err := store.ListPending(context.Background(), &background.ListPendingRequest{
			ExecutorKeys: []string{"test"}, Limit: 2,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}, taskIDs(first.Tasks))
		require.NotEmpty(t, first.NextCursor)
		second, err := store.ListPending(context.Background(), &background.ListPendingRequest{
			ExecutorKeys: []string{"test"}, Cursor: first.NextCursor, Limit: 2,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"c"}, taskIDs(second.Tasks))
		require.Empty(t, second.NextCursor)
	})

	for _, policy := range []background.LeaseExpiryPolicy{
		background.LeaseExpiryRetry, background.LeaseExpiryFail,
	} {
		t.Run("lease_expiry_"+string(policy), func(t *testing.T) {
			store := config.New(t)
			started := createAndStart(t, store, "lease-"+string(policy), policy)
			config.ExpireActiveAttempt(t, store, started)
			expired, err := store.Get(context.Background(), started.Spec.ID)
			require.NoError(t, err)
			if policy == background.LeaseExpiryRetry {
				require.Equal(t, background.StatusPending, expired.Status)
			} else {
				require.Equal(t, background.StatusFailed, expired.Status)
			}
		})
	}
}

// RunTaskEventStoreConformance validates append ordering, cursor validation,
// and snapshot-stable pagination.
func RunTaskEventStoreConformance(t *testing.T, config TaskEventStoreConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	tasks, events := config.New(t)
	started := createAndStart(t, tasks, "events", background.LeaseExpiryRetry)
	appendEvent(t, events, started, "one", "one")
	replay, err := events.AppendTaskEvent(context.Background(), &background.AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("one"),
	})
	require.NoError(t, err)
	require.False(t, replay.Inserted)
	_, err = events.AppendTaskEvent(context.Background(), &background.AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("different"),
	})
	require.ErrorIs(t, err, background.ErrTaskEventPartConflict)
	appendEvent(t, events, started, "two", "two")
	appendEvent(t, events, started, "three", "three")
	firstPart, err := events.AppendTaskEvent(
		context.Background(),
		&background.AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: "stream", PartID: "chunk-0", Data: []byte("a"),
		},
	)
	require.NoError(t, err)
	require.True(t, firstPart.Inserted)
	finalPart, err := events.AppendTaskEvent(
		context.Background(),
		&background.AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: "stream", PartID: "end", Data: []byte("done"),
			Final: true,
		},
	)
	require.NoError(t, err)
	require.True(t, finalPart.Inserted)
	replayedFinal, err := events.AppendTaskEvent(
		context.Background(),
		&background.AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: "stream", PartID: "end", Data: []byte("done"),
			Final: true,
		},
	)
	require.NoError(t, err)
	require.False(t, replayedFinal.Inserted)
	_, err = events.AppendTaskEvent(
		context.Background(),
		&background.AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: "stream", PartID: "late", Data: []byte("late"),
		},
	)
	require.ErrorIs(t, err, background.ErrTaskEventClosed)
	first, err := events.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, eventIDs(first.Events))
	appendEvent(t, events, started, "four", "four")
	second, err := events.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: started.Spec.ID, Cursor: first.NextCursor, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"three", "stream"}, eventIDs(second.Events))
	third, err := events.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: started.Spec.ID, Cursor: second.NextCursor, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"stream"}, eventIDs(third.Events))
	recent, err := events.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2, NewestFirst: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"four", "stream"}, eventIDs(recent.Events))

	yielded, err := tasks.Yield(context.Background(), &background.YieldTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	restarted, err := tasks.Start(context.Background(), &background.StartTaskRequest{
		TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
	})
	require.NoError(t, err)
	_, err = events.AppendTaskEvent(context.Background(), &background.AppendTaskEventRequest{
		TaskID: restarted.Spec.ID, Attempt: started.Attempt, EventID: "one", Data: []byte("one"),
	})
	require.ErrorIs(t, err, background.ErrLeaseLost)

	other := create(t, tasks, testSpec("other"), background.LeaseExpiryRetry)
	_, err = events.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: other.Spec.ID, Cursor: recent.NextCursor, NewestFirst: true,
	})
	require.ErrorIs(t, err, background.ErrInvalidCursor)
}

// RunNotificationOutboxConformance checks lease exclusion, expiry, redelivery,
// stale-receipt rejection, and acknowledgement.
func RunNotificationOutboxConformance(t *testing.T, config NotificationOutboxConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	require.NotNil(t, config.ExpireLease)
	tasks, outbox := config.New(t)
	spec := testSpec("notification")
	spec.RootSessionID = "session"
	create(t, tasks, spec, background.LeaseExpiryRetry)
	lease := 20 * time.Millisecond
	first, err := outbox.Receive(context.Background(), &background.ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Len(t, first.Deliveries, 1)
	require.Equal(t, background.NotificationTaskCreated, first.Deliveries[0].Record.Kind)
	require.Equal(t, spec.RootSessionID, first.Deliveries[0].Record.SessionID)
	concurrent, err := outbox.Receive(context.Background(), &background.ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Empty(t, concurrent.Deliveries)
	config.ExpireLease(t, outbox, lease)
	require.ErrorIs(t, outbox.Ack(context.Background(), first.Deliveries[0].Receipt), background.ErrLeaseLost)
	second, err := outbox.Receive(context.Background(), &background.ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: lease,
	})
	require.NoError(t, err)
	require.Len(t, second.Deliveries, 1)
	require.NotEqual(t, first.Deliveries[0].Receipt, second.Deliveries[0].Receipt)
	require.Error(t, outbox.Ack(context.Background(), first.Deliveries[0].Receipt))
	require.NoError(t, outbox.Ack(context.Background(), second.Deliveries[0].Receipt))

	deferredSpec := testSpec("deferred-notification")
	deferredSpec.RootSessionID = "session"
	deferredSpec.NotifySession = true
	deferred, err := tasks.Create(
		context.Background(),
		&background.CreateTaskRequest{
			Spec: deferredSpec, Publication: background.PublicationDeferred,
			LeaseExpiryPolicy: background.LeaseExpiryRetry,
		},
	)
	require.NoError(t, err)
	hidden, err := outbox.Receive(
		context.Background(),
		&background.ReceiveNotificationsRequest{
			Limit: 10, LeaseDuration: lease,
		},
	)
	require.NoError(t, err)
	require.Empty(t, hidden.Deliveries)
	published, err := tasks.Publish(
		context.Background(),
		&background.PublishTaskRequest{
			TaskID: deferred.Spec.ID, ExpectedVersion: deferred.Version,
		},
	)
	require.NoError(t, err)
	require.Equal(t, background.PublicationOnBackground, published.Publication)
	backgrounded, err := outbox.Receive(
		context.Background(),
		&background.ReceiveNotificationsRequest{
			Limit: 10, LeaseDuration: lease,
		},
	)
	require.NoError(t, err)
	require.Len(t, backgrounded.Deliveries, 1)
	require.Equal(
		t,
		background.NotificationTaskBackgrounded,
		backgrounded.Deliveries[0].Record.Kind,
	)
	require.NoError(t, outbox.Ack(
		context.Background(),
		backgrounded.Deliveries[0].Receipt,
	))
	replayed, err := tasks.Publish(
		context.Background(),
		&background.PublishTaskRequest{
			TaskID: published.Spec.ID, ExpectedVersion: published.Version,
		},
	)
	require.NoError(t, err)
	require.Equal(t, published.Version, replayed.Version)
	duplicate, err := outbox.Receive(
		context.Background(),
		&background.ReceiveNotificationsRequest{
			Limit: 10, LeaseDuration: lease,
		},
	)
	require.NoError(t, err)
	require.Empty(t, duplicate.Deliveries)
}

// RunNotificationWriterConformance checks authorization-before-replay,
// idempotency, bounds, identity, state preservation, and copy ownership.
func RunNotificationWriterConformance(t *testing.T, config NotificationWriterConfig) {
	t.Helper()
	require.NotNil(t, config.New)
	require.NotNil(t, config.ExpireActiveAttempt)

	t.Run("replay_follows_authorization_and_survives_ack", func(t *testing.T) {
		tasks, outbox := config.New(t)
		writer := notificationWriter(t, tasks)
		started := createParentAndStart(t, tasks, "notify-replay")
		req := &background.NotifyParentRequest{
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

		yielded, err := tasks.Yield(context.Background(), &background.YieldTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		})
		require.NoError(t, err)
		restarted, err := tasks.Start(context.Background(), &background.StartTaskRequest{
			TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
		})
		require.NoError(t, err)
		original := &background.NotifyParentRequest{
			EventID: "event", Kind: "application.update", Data: []byte("original"),
		}
		require.ErrorIs(t, writer.EnqueueTaskNotification(
			context.Background(),
			started.Spec.ID,
			started.Attempt,
			&background.NotifyParentRequest{
				EventID: "event", Kind: "application.changed", Data: []byte("changed"),
			},
		), background.ErrLeaseLost)
		require.NoError(t, writer.EnqueueTaskNotification(
			context.Background(), restarted.Spec.ID, restarted.Attempt, original,
		))
		require.ErrorIs(t, writer.EnqueueTaskNotification(
			context.Background(),
			restarted.Spec.ID,
			restarted.Attempt,
			&background.NotifyParentRequest{
				EventID: "event", Kind: "application.changed", Data: []byte("changed"),
			},
		), background.ErrNotificationEventIDConflict)
		afterReplay, err := outbox.Receive(
			context.Background(),
			&background.ReceiveNotificationsRequest{
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
		), background.ErrLeaseLost)
		pending, err := tasks.Get(context.Background(), restarted.Spec.ID)
		require.NoError(t, err)
		current, err := tasks.Start(context.Background(), &background.StartTaskRequest{
			TaskID: pending.Spec.ID, ExpectedVersion: pending.Version,
		})
		require.NoError(t, err)
		canceled, err := tasks.RequestCancel(
			context.Background(),
			&background.RequestCancelRequest{
				TaskID: current.Spec.ID, ExpectedVersion: current.Version,
			},
		)
		require.NoError(t, err)
		require.ErrorIs(t, writer.EnqueueTaskNotification(
			context.Background(), canceled.Spec.ID, current.Attempt, original,
		), background.ErrLeaseLost)
	})

	t.Run("identity_state_version_and_copy_ownership", func(t *testing.T) {
		tasks, outbox := config.New(t)
		writer := notificationWriter(t, tasks)
		started := createParentAndStart(t, tasks, "notify-state")
		before, err := tasks.Get(context.Background(), started.Spec.ID)
		require.NoError(t, err)
		req := &background.NotifyParentRequest{
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
		require.Equal(t, background.NotificationKind("application.state"), custom.Record.Kind)
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
			&background.NotifyParentRequest{
				EventID: "state-event", Kind: "application.state", Data: []byte("data"),
			},
		))

		otherTasks, otherOutbox := config.New(t)
		otherWriter := notificationWriter(t, otherTasks)
		otherStarted := createParentAndStart(t, otherTasks, "notify-state")
		require.NoError(t, otherWriter.EnqueueTaskNotification(
			context.Background(), otherStarted.Spec.ID, otherStarted.Attempt,
			&background.NotifyParentRequest{
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
		write := func(req *background.NotifyParentRequest) error {
			return writer.EnqueueTaskNotification(
				context.Background(), started.Spec.ID, started.Attempt, req,
			)
		}
		for _, req := range []*background.NotifyParentRequest{
			nil,
			{Kind: "application.valid"},
			{EventID: strings.Repeat("e", 1025), Kind: "application.valid"},
			{EventID: "empty-kind"},
			{EventID: "long-kind", Kind: background.NotificationKind(strings.Repeat("k", 65))},
			{EventID: "lifecycle", Kind: background.NotificationCompleted},
			{EventID: "reserved", Kind: "eino.application"},
			{EventID: "large-data", Kind: "application.valid", Data: make([]byte, (256<<10)+1)},
		} {
			require.Error(t, write(req))
		}
		require.NoError(t, write(&background.NotifyParentRequest{
			EventID: strings.Repeat("e", 1024),
			Kind:    background.NotificationKind(strings.Repeat("k", 64)),
			Data:    make([]byte, 256<<10),
		}))
		require.NoError(t, write(&background.NotifyParentRequest{
			EventID: "nil-empty", Kind: "application.empty",
		}))
		require.NoError(t, write(&background.NotifyParentRequest{
			EventID: "nil-empty", Kind: "application.empty", Data: []byte{},
		}))
	})
}

func notificationWriter(
	t testing.TB,
	tasks background.TaskStore,
) background.NotificationWriter {
	t.Helper()
	writer, ok := tasks.(background.NotificationWriter)
	require.True(t, ok)
	return writer
}

func createParentAndStart(
	t testing.TB,
	tasks background.TaskStore,
	id string,
) *background.TaskSnapshot {
	t.Helper()
	spec := testSpec(id)
	spec.RootSessionID = "parent-session"
	created := create(t, tasks, spec, background.LeaseExpiryRetry)
	started, err := tasks.Start(context.Background(), &background.StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	return started
}

func receiveNotificationKinds(
	t testing.TB,
	outbox background.NotificationOutbox,
) (background.NotificationDelivery, background.NotificationDelivery) {
	t.Helper()
	return receiveNotificationKindsWithLease(t, outbox, time.Second)
}

func receiveNotificationKindsWithLease(
	t testing.TB,
	outbox background.NotificationOutbox,
	lease time.Duration,
) (background.NotificationDelivery, background.NotificationDelivery) {
	t.Helper()
	result, err := outbox.Receive(
		context.Background(),
		&background.ReceiveNotificationsRequest{
			Limit: 100, LeaseDuration: lease,
		},
	)
	require.NoError(t, err)
	var custom background.NotificationDelivery
	var lifecycle background.NotificationDelivery
	for _, delivery := range result.Deliveries {
		if delivery.Record.Kind == background.NotificationTaskCreated {
			lifecycle = delivery
		} else {
			custom = delivery
		}
	}
	require.NotEmpty(t, custom.Record.ID)
	require.NotEmpty(t, lifecycle.Record.ID)
	return custom, lifecycle
}

func testSpec(id string) background.Spec {
	return background.Spec{
		ID: id, ExecutorKey: "test", Payload: []byte("payload"),
	}
}

func runCreateSnapshotConformance(t testing.TB, store background.TaskStore) {
	t.Helper()
	spec := testSpec("create")
	created := create(t, store, spec, background.LeaseExpiryRetry)
	require.False(t, created.CreatedAt.IsZero())
	require.Equal(t, created.CreatedAt, created.UpdatedAt)
	require.Equal(t, background.StatusPending, created.Status)
	require.Equal(t, background.PublicationOnCreate, created.Publication)
	spec.Payload[0] = 'X'
	created.Spec.Payload[0] = 'Y'
	stored, err := store.Get(context.Background(), spec.ID)
	require.NoError(t, err)
	require.Equal(t, "payload", string(stored.Spec.Payload))
}

func runCreateInitialCheckpointConformance(
	t testing.TB,
	store background.TaskStore,
) {
	t.Helper()
	spec := testSpec("initial-checkpoint")
	checkpoint := []byte("checkpoint")
	created, err := store.Create(context.Background(), &background.CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: background.LeaseExpiryRetry,
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

func runPublicationConformance(
	t *testing.T,
	newStore func(testing.TB) background.LifecycleStore,
) {
	t.Helper()
	t.Run("publication_transition_and_replay", func(t *testing.T) {
		store := newStore(t)
		created, err := store.Create(
			context.Background(),
			&background.CreateTaskRequest{
				Spec:              testSpec("deferred-publication"),
				Publication:       background.PublicationDeferred,
				LeaseExpiryPolicy: background.LeaseExpiryRetry,
			},
		)
		require.NoError(t, err)
		require.Equal(t, background.PublicationDeferred, created.Publication)
		published, err := store.Publish(
			context.Background(),
			&background.PublishTaskRequest{
				TaskID: created.Spec.ID, ExpectedVersion: created.Version,
			},
		)
		require.NoError(t, err)
		require.Equal(t, background.PublicationOnBackground, published.Publication)
		require.Equal(t, created.Version, published.Version)
		replayed, err := store.Publish(
			context.Background(),
			&background.PublishTaskRequest{
				TaskID: published.Spec.ID, ExpectedVersion: published.Version,
			},
		)
		require.NoError(t, err)
		require.Equal(t, published.Version, replayed.Version)

		created = create(
			t,
			store,
			testSpec("created-publication"),
			background.LeaseExpiryRetry,
		)
		require.Equal(t, background.PublicationOnCreate, created.Publication)
		_, err = store.Publish(
			context.Background(),
			&background.PublishTaskRequest{
				TaskID: created.Spec.ID, ExpectedVersion: created.Version,
			},
		)
		require.ErrorIs(t, err, background.ErrIllegalTransition)
	})

	t.Run("terminal_deferred_task_cannot_publish", func(t *testing.T) {
		store := newStore(t)
		created, err := store.Create(
			context.Background(),
			&background.CreateTaskRequest{
				Spec:              testSpec("terminal-deferred"),
				Publication:       background.PublicationDeferred,
				LeaseExpiryPolicy: background.LeaseExpiryRetry,
			},
		)
		require.NoError(t, err)
		canceled, err := store.RequestCancel(
			context.Background(),
			&background.RequestCancelRequest{
				TaskID: created.Spec.ID, ExpectedVersion: created.Version,
				Reason: "foreground timeout",
			},
		)
		require.NoError(t, err)
		require.Equal(t, background.StatusCanceled, canceled.Status)
		_, err = store.Publish(
			context.Background(),
			&background.PublishTaskRequest{
				TaskID: canceled.Spec.ID, ExpectedVersion: canceled.Version,
			},
		)
		require.ErrorIs(t, err, background.ErrAlreadyTerminal)
	})
}

func create(
	t testing.TB,
	store background.TaskStore,
	spec background.Spec,
	policy background.LeaseExpiryPolicy,
) *background.TaskSnapshot {
	t.Helper()
	task, err := store.Create(context.Background(), &background.CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: policy,
	})
	require.NoError(t, err)
	return task
}

func createAndStart(
	t testing.TB,
	store background.TaskStore,
	id string,
	policy background.LeaseExpiryPolicy,
) *background.TaskSnapshot {
	t.Helper()
	created := create(t, store, testSpec(id), policy)
	started, err := store.Start(context.Background(), &background.StartTaskRequest{
		TaskID: id, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	return started
}

func appendEvent(
	t testing.TB,
	store background.TaskEventStore,
	task *background.TaskSnapshot,
	id string,
	data string,
) {
	t.Helper()
	result, err := store.AppendTaskEvent(context.Background(), &background.AppendTaskEventRequest{
		TaskID: task.Spec.ID, Attempt: task.Attempt, EventID: id, Data: []byte(data),
	})
	require.NoError(t, err)
	require.True(t, result.Inserted)
}

func taskIDs(tasks []*background.TaskSnapshot) []string {
	result := make([]string, len(tasks))
	for i, task := range tasks {
		result[i] = task.Spec.ID
	}
	return result
}

func eventIDs(events []*background.TaskEvent) []string {
	result := make([]string, len(events))
	for i, event := range events {
		result[i] = event.EventID
	}
	return result
}
