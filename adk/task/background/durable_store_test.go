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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newInMemoryStoreWithClock(
	config *InMemoryStoreConfig,
	now func() time.Time,
) *InMemoryStore {
	store := NewInMemoryStore(config)
	store.now = now
	return store
}

func validSpec(id string) Spec {
	return Spec{
		ID: id, ExecutorKey: "test", Payload: []byte("payload"),
		SessionID: "session", NotifySession: true,
	}
}

func createAndStart(t *testing.T, store *InMemoryStore, id string) *TaskSnapshot {
	return createAndStartWithPolicy(t, store, id, LeaseExpiryRetry)
}

func createAndStartWithPolicy(
	t *testing.T,
	store *InMemoryStore,
	id string,
	policy LeaseExpiryPolicy,
) *TaskSnapshot {
	t.Helper()
	created, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: validSpec(id), LeaseExpiryPolicy: policy,
	})
	require.NoError(t, err)
	started, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: id, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	return started
}

func TestInMemoryStoreCreatePersistsPendingSnapshot_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	spec := validSpec("create")

	created, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: LeaseExpiryRetry,
		ContextSnapshot: []byte("ctx-snapshot"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, created.Status)
	assert.Equal(t, LeaseExpiryRetry, created.LeaseExpiryPolicy)
	assert.Empty(t, created.ResultData)
	assert.Empty(t, created.ResultError)
	assert.Equal(t, "ctx-snapshot", string(created.ContextSnapshot))

	spec.Payload[0] = 'X'
	created.ContextSnapshot[0] = 'X'
	stored, err := store.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(stored.Spec.Payload))
	assert.Equal(t, "ctx-snapshot", string(stored.ContextSnapshot))
	assert.True(t, stored.Spec.NotifySession)
}

func TestAttack_ListPendingCursorSurvivesEarlierInsertion(t *testing.T) {
	store := NewInMemoryStore(nil)
	for _, id := range []string{"task-b", "task-c"} {
		_, err := store.Create(context.Background(), &CreateTaskRequest{
			Spec: validSpec(id), LeaseExpiryPolicy: LeaseExpiryRetry,
		})
		require.NoError(t, err)
	}
	first, err := store.ListPending(context.Background(), &ListPendingRequest{
		ExecutorKeys: []string{"test"}, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, first.Tasks, 1)
	require.Equal(t, "task-b", first.Tasks[0].Spec.ID)
	require.NotEmpty(t, first.NextCursor)

	_, err = store.Create(context.Background(), &CreateTaskRequest{
		Spec: validSpec("task-a"), LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	second, err := store.ListPending(context.Background(), &ListPendingRequest{
		ExecutorKeys: []string{"test"}, Cursor: first.NextCursor, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, second.Tasks, 1)
	t.Logf("cursor %q returned %q after an earlier task was inserted",
		first.NextCursor, second.Tasks[0].Spec.ID)
	require.Equal(t, "task-c", second.Tasks[0].Spec.ID)
}

func TestAttack_ListCursorBindsQueryAndSignalsExactExhaustion(t *testing.T) {
	exhaustedStore := NewInMemoryStore(nil)
	_, err := exhaustedStore.Create(context.Background(), &CreateTaskRequest{
		Spec: validSpec("only"), LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	exhausted, err := exhaustedStore.ListPending(
		context.Background(),
		&ListPendingRequest{ExecutorKeys: []string{"test"}, Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, exhausted.Tasks, 1)
	require.Empty(t, exhausted.NextCursor)

	store := NewInMemoryStore(nil)
	for _, item := range []struct {
		id          string
		executorKey string
	}{
		{id: "a-test", executorKey: "test"},
		{id: "b-other", executorKey: "other"},
		{id: "c-test", executorKey: "test"},
	} {
		spec := validSpec(item.id)
		spec.ExecutorKey = item.executorKey
		_, err = store.Create(context.Background(), &CreateTaskRequest{
			Spec: spec, LeaseExpiryPolicy: LeaseExpiryRetry,
		})
		require.NoError(t, err)
	}
	first, err := store.ListPending(context.Background(), &ListPendingRequest{
		ExecutorKeys: []string{"test", "other"}, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, first.Tasks, 1)
	require.NotEmpty(t, first.NextCursor)

	reordered, err := store.ListPending(context.Background(), &ListPendingRequest{
		ExecutorKeys: []string{"other", "test"},
		Cursor:       first.NextCursor,
		Limit:        1,
	})
	require.NoError(t, err)
	require.Len(t, reordered.Tasks, 1)

	_, err = store.ListPending(context.Background(), &ListPendingRequest{
		ExecutorKeys: []string{"test"}, Cursor: first.NextCursor,
	})
	require.ErrorIs(t, err, ErrInvalidCursor)
	_, err = store.ListSuspended(context.Background(), &ListSuspendedRequest{
		ExecutorKeys: []string{"test", "other"}, Cursor: first.NextCursor,
	})
	require.ErrorIs(t, err, ErrInvalidCursor)

	forged, err := encodeTaskListCursor(taskListCursor{
		Version: 1, Status: StatusPending,
		ExecutorKeys: []string{"other", "test"}, LastID: "missing",
	})
	require.NoError(t, err)
	_, err = store.ListPending(context.Background(), &ListPendingRequest{
		ExecutorKeys: []string{"test", "other"}, Cursor: forged,
	})
	require.ErrorIs(t, err, ErrInvalidCursor)
	t.Log("list cursors are query-bound and empty at exact exhaustion")
}

func TestAttack_CancellationFencesAllNonCancelMutations(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "cancel-fence")
	requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	require.NotNil(t, requested.CancelRequestedAt)

	mutations := []struct {
		name   string
		mutate func() error
	}{
		{name: "heartbeat", mutate: func() error {
			_, mutationErr := store.Heartbeat(context.Background(), &HeartbeatRequest{
				TaskID: started.Spec.ID, ExpectedVersion: requested.Version,
			})
			return mutationErr
		}},
		{name: "append output", mutate: func() error {
			_, mutationErr := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
				TaskID: started.Spec.ID, Attempt: started.Attempt,
				EventID: "late", Data: []byte("late"),
			})
			return mutationErr
		}},
		{name: "complete", mutate: func() error {
			_, mutationErr := store.CompleteIfNoInputs(
				context.Background(),
				&CompleteIfNoInputsRequest{
					TaskID: started.Spec.ID, ExpectedVersion: requested.Version,
					Attempt: started.Attempt, InputCursor: 0, ResultData: []byte("late"),
				},
			)
			return mutationErr
		}},
		{name: "fail", mutate: func() error {
			_, mutationErr := store.Fail(context.Background(), &FailTaskRequest{
				TaskID: started.Spec.ID, ExpectedVersion: requested.Version, Error: "late",
			})
			return mutationErr
		}},
		{name: "wait input", mutate: func() error {
			_, mutationErr := store.WaitInputIfNoInputs(
				context.Background(),
				&WaitInputIfNoInputsRequest{
					TaskID: started.Spec.ID, ExpectedVersion: requested.Version,
					Attempt: started.Attempt, InputCursor: 0, Checkpoint: []byte("late"),
				},
			)
			return mutationErr
		}},
		{name: "suspend", mutate: func() error {
			_, mutationErr := store.SuspendIfNoInputs(
				context.Background(),
				&SuspendIfNoInputsRequest{
					TaskID: started.Spec.ID, ExpectedVersion: requested.Version,
					Attempt: started.Attempt, InputCursor: 0, Checkpoint: []byte("late"),
				},
			)
			return mutationErr
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			mutationErr := mutation.mutate()
			t.Logf("mutation rejected with %v", mutationErr)
			require.ErrorIs(t, mutationErr, ErrLeaseLost)
		})
	}

	canceled, err := store.AckCancel(context.Background(), &AckCancelRequest{
		TaskID: started.Spec.ID, ExpectedVersion: requested.Version,
	})
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, canceled.Status)
}

func TestAttack_TaskEventOrderSpansAttemptsWithoutExposingAttempt(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newInMemoryStoreWithClock(
		&InMemoryStoreConfig{ActiveAttemptTimeout: time.Second},
		clock.Now,
	)
	firstAttempt := createAndStart(t, store, "output-retry")
	first, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: firstAttempt.Spec.ID, Attempt: firstAttempt.Attempt,
		EventID: "first", Data: []byte("first"),
	})
	require.NoError(t, err)
	require.NotNil(t, first.Event)
	require.Equal(t, "first", first.Event.EventID)

	clock.Advance(2 * time.Second)
	pending, err := store.Get(context.Background(), firstAttempt.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, pending.Status)
	secondAttempt, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: pending.Spec.ID, ExpectedVersion: pending.Version,
	})
	require.NoError(t, err)
	second, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: secondAttempt.Spec.ID, Attempt: secondAttempt.Attempt,
		EventID: "second", Data: []byte("second"),
	})
	require.NoError(t, err)
	require.NotNil(t, second.Event)
	require.Equal(t, "second", second.Event.EventID)

	output, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: secondAttempt.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, output.Events, 2)
	require.NotNil(t, output.Events[0])
	require.NotNil(t, output.Events[1])
	require.Equal(t, []string{"first", "second"}, []string{
		output.Events[0].EventID, output.Events[1].EventID,
	})
	require.Equal(t, []string{"first", "second"}, []string{
		string(output.Events[0].Data), string(output.Events[1].Data),
	})
}

func TestAttack_EventIDIsTaskLocal(t *testing.T) {
	store := NewInMemoryStore(nil)
	first := createAndStart(t, store, "event-id-task-one")
	second := createAndStart(t, store, "event-id-task-two")

	firstResult, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: first.Spec.ID, Attempt: first.Attempt,
		EventID: "shared", Data: []byte("first"),
	})
	require.NoError(t, err)
	require.True(t, firstResult.Inserted)
	require.NotNil(t, firstResult.Event)
	secondResult, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: second.Spec.ID, Attempt: second.Attempt,
		EventID: "shared", Data: []byte("second"),
	})
	require.NoError(t, err)
	require.True(t, secondResult.Inserted)
	require.NotNil(t, secondResult.Event)
	require.Equal(t, first.Spec.ID, firstResult.Event.TaskID)
	require.Equal(t, second.Spec.ID, secondResult.Event.TaskID)
}

func TestInMemoryStoreYieldReturnsRecoverableAttemptToPending_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "yield")
	createdDelivery, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 1, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Len(t, createdDelivery.Deliveries, 1)
	require.Equal(t, NotificationTaskCreated, createdDelivery.Deliveries[0].Record.Kind)
	require.NoError(t, store.Ack(context.Background(), createdDelivery.Deliveries[0].Receipt))
	yielded, err := store.Yield(context.Background(), &YieldTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		Checkpoint: []byte("recovery-ref"),
	})
	require.NoError(t, err)
	require.Equal(t, StatusPending, yielded.Status)
	require.Equal(t, "recovery-ref", string(yielded.Checkpoint))
	require.Equal(t, started.Attempt, yielded.Attempt)
	require.Nil(t, yielded.DoneAt)

	deliveries, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		Limit: 10, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Empty(t, deliveries.Deliveries)

	restarted, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), restarted.Attempt)
	yieldedAgain, err := store.Yield(context.Background(), &YieldTaskRequest{
		TaskID: restarted.Spec.ID, ExpectedVersion: restarted.Version,
	})
	require.NoError(t, err)
	require.Equal(t, "recovery-ref", string(yieldedAgain.Checkpoint))

	_, err = store.Yield(context.Background(), &YieldTaskRequest{
		TaskID: yieldedAgain.Spec.ID, ExpectedVersion: started.Version,
	})
	require.ErrorIs(t, err, ErrVersionConflict)
}

func TestInMemoryStoreYieldRejectsCancellation_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "yield-canceled")
	requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	_, err = store.Yield(context.Background(), &YieldTaskRequest{
		TaskID: requested.Spec.ID, ExpectedVersion: requested.Version,
	})
	require.ErrorIs(t, err, ErrLeaseLost)
}

func TestInMemoryStoreTaskEventDeduplicatesAcrossAttempts_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "keyed-output")
	version := started.Version
	_, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, Data: []byte("missing-id"),
	})
	require.ErrorContains(t, err, "event id are required")
	first, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt,
		EventID: "event-1", Data: []byte("payload"),
	})
	require.NoError(t, err)
	require.True(t, first.Inserted)
	require.NotNil(t, first.Event)
	require.Equal(t, "event-1", first.Event.EventID)
	createdAt := first.Event.CreatedAt

	yielded, err := store.Yield(context.Background(), &YieldTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	restarted, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
	})
	require.NoError(t, err)
	replayed, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: restarted.Spec.ID, Attempt: restarted.Attempt,
		EventID: "event-1", Data: []byte("payload"),
	})
	require.NoError(t, err)
	require.False(t, replayed.Inserted)
	require.NotNil(t, replayed.Event)
	require.Equal(t, first.Event, replayed.Event)
	require.Equal(t, createdAt, replayed.Event.CreatedAt)

	_, err = store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: restarted.Spec.ID, Attempt: restarted.Attempt,
		EventID: "event-1", Data: []byte("different"),
	})
	require.ErrorIs(t, err, ErrTaskEventIDConflict)

	current, err := store.Get(context.Background(), restarted.Spec.ID)
	require.NoError(t, err)
	require.NotEqual(t, version, current.Version)
	versionBeforeOutput := current.Version
	_, err = store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: restarted.Spec.ID, Attempt: restarted.Attempt,
		EventID: "event-2", Data: []byte("second"),
	})
	require.NoError(t, err)
	current, err = store.Get(context.Background(), restarted.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, versionBeforeOutput, current.Version)

	first.Event.Data[0] = 'X'
	stored, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: restarted.Spec.ID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, stored.Events)
	require.NotNil(t, stored.Events[0])
	require.Equal(t, "payload", string(stored.Events[0].Data))
}

func TestAttack_NewestTaskEventsIgnoreEventIDLexicalOrder(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "recent-output")
	for _, event := range []struct {
		id   string
		data string
	}{
		{id: "z-event", data: "one"},
		{id: "a-event", data: "two"},
		{id: "m-event", data: "three"},
	} {
		_, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: event.id, Data: []byte(event.data),
		})
		require.NoError(t, err)
	}
	result, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2, NewestFirst: true,
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 2)
	require.NotNil(t, result.Events[0])
	require.NotNil(t, result.Events[1])
	require.Equal(t, "three", string(result.Events[0].Data))
	require.Equal(t, "two", string(result.Events[1].Data))
}

func TestInMemoryStoreListTaskEventsExhaustsStableSnapshots_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "paginated-events")
	appendEvent := func(id string) {
		t.Helper()
		_, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: id, Data: []byte(id),
		})
		require.NoError(t, err)
	}
	eventIDs := func(events []*TaskEvent) []string {
		t.Helper()
		ids := make([]string, len(events))
		for i, event := range events {
			require.NotNil(t, event)
			ids[i] = event.EventID
		}
		return ids
	}
	for _, id := range []string{"one", "two", "three", "four", "five"} {
		appendEvent(id)
	}

	first, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, eventIDs(first.Events))
	require.NotEmpty(t, first.NextCursor)
	appendEvent("six")

	second, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Cursor: first.NextCursor, Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"three", "four"}, eventIDs(second.Events))
	require.NotEmpty(t, second.NextCursor)
	third, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Cursor: second.NextCursor, Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"five"}, eventIDs(third.Events))
	assert.Empty(t, third.NextCursor)

	newest, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2, NewestFirst: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"six", "five"}, eventIDs(newest.Events))
	require.NotEmpty(t, newest.NextCursor)
	appendEvent("seven")

	var newestSnapshot []string
	cursor := newest.NextCursor
	for cursor != "" {
		page, listErr := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
			TaskID: started.Spec.ID, Cursor: cursor, Limit: 2, NewestFirst: true,
		})
		require.NoError(t, listErr)
		newestSnapshot = append(newestSnapshot, eventIDs(page.Events)...)
		cursor = page.NextCursor
	}
	assert.Equal(t, []string{"four", "three", "two", "one"}, newestSnapshot)

	fresh, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"one", "two", "three", "four", "five", "six", "seven"},
		eventIDs(fresh.Events),
	)
	assert.Empty(t, fresh.NextCursor)
}

func TestInMemoryStoreListTaskEventsRejectsInvalidCursors_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "cursor-events")
	for _, id := range []string{"one", "two"} {
		_, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: id, Data: []byte(id),
		})
		require.NoError(t, err)
	}
	first, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)
	other := createAndStart(t, store, "other-cursor-events")

	for _, testCase := range []struct {
		name    string
		request *ListTaskEventsRequest
	}{
		{
			name: "malformed",
			request: &ListTaskEventsRequest{
				TaskID: started.Spec.ID, Cursor: "not-a-cursor",
			},
		},
		{
			name: "other task",
			request: &ListTaskEventsRequest{
				TaskID: other.Spec.ID, Cursor: first.NextCursor,
			},
		},
		{
			name: "other direction",
			request: &ListTaskEventsRequest{
				TaskID: started.Spec.ID, Cursor: first.NextCursor, NewestFirst: true,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, listErr := store.ListTaskEvents(context.Background(), testCase.request)
			require.ErrorIs(t, listErr, ErrInvalidCursor)
		})
	}

	for _, testCase := range []struct {
		name   string
		cursor taskEventCursor
	}{
		{
			name: "unsupported version",
			cursor: taskEventCursor{
				Version: 2, TaskID: started.Spec.ID, SnapshotEnd: 2, Position: 1,
			},
		},
		{
			name: "future snapshot",
			cursor: taskEventCursor{
				Version: 1, TaskID: started.Spec.ID, SnapshotEnd: 3, Position: 1,
			},
		},
		{
			name: "position outside snapshot",
			cursor: taskEventCursor{
				Version: 1, TaskID: started.Spec.ID, SnapshotEnd: 2, Position: 3,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, encodeErr := encodeTaskEventCursor(testCase.cursor)
			require.NoError(t, encodeErr)
			_, listErr := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
				TaskID: started.Spec.ID, Cursor: encoded,
			})
			require.ErrorIs(t, listErr, ErrInvalidCursor)
		})
	}
}

func TestInMemoryStoreListTaskEventsNormalizesLimit_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "event-limits")
	for i := 0; i < maxTaskEventPageSize+1; i++ {
		id := "event-" + strconv.Itoa(i)
		_, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: id, Data: []byte(id),
		})
		require.NoError(t, err)
	}

	defaultPage, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, defaultPage.Events, defaultTaskEventPageSize)
	require.NotEmpty(t, defaultPage.NextCursor)

	cappedPage, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: maxTaskEventPageSize + 1,
	})
	require.NoError(t, err)
	require.Len(t, cappedPage.Events, maxTaskEventPageSize)
	require.NotEmpty(t, cappedPage.NextCursor)
}

func TestInMemoryStoreHeartbeatSuspensionReleaseAndWait(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "suspension")
	waitDone := make(chan struct {
		task *TaskSnapshot
		err  error
	}, 1)
	go func() {
		task, err := store.WaitForTaskVersion(context.Background(), &WaitForTaskVersionRequest{
			TaskID: started.Spec.ID, AfterVersion: started.Version,
		})
		waitDone <- struct {
			task *TaskSnapshot
			err  error
		}{task: task, err: err}
	}()

	heartbeat, err := store.Heartbeat(context.Background(), &HeartbeatRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	require.Equal(t, started.Version+1, heartbeat.Version)
	waited := <-waitDone
	require.NoError(t, waited.err)
	require.Equal(t, heartbeat.Version, waited.task.Version)

	suspended, err := store.SuspendIfNoInputs(
		context.Background(),
		&SuspendIfNoInputsRequest{
			TaskID: started.Spec.ID, ExpectedVersion: heartbeat.Version,
			Attempt: started.Attempt, InputCursor: 0,
			Checkpoint: []byte("checkpoint"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, StatusSuspended, suspended.Status)
	require.Equal(t, "checkpoint", string(suspended.Checkpoint))

	listed, err := store.ListSuspended(context.Background(), &ListSuspendedRequest{
		ExecutorKeys: []string{"test"},
	})
	require.NoError(t, err)
	require.Len(t, listed.Tasks, 1)
	require.Equal(t, suspended.Spec.ID, listed.Tasks[0].Spec.ID)

	released, err := store.ReleaseSuspension(context.Background(), &ReleaseSuspensionRequest{
		TaskID: started.Spec.ID, ExpectedVersion: suspended.Version,
	})
	require.NoError(t, err)
	require.Equal(t, StatusPending, released.Status)
	require.Equal(t, "checkpoint", string(released.Checkpoint))
}

func TestInMemoryStoreReportTranscriptFailureIsFencedAndFirstErrorWins_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "output")
	reported, err := store.ReportTranscriptFailure(context.Background(), &ReportTranscriptFailureRequest{
		TaskID: "output", ExpectedVersion: started.Version, Error: "write failed",
	})
	require.NoError(t, err)
	assert.Equal(t, "write failed", reported.OutputFileErr)
	assert.Equal(t, started.Version+1, reported.Version)
	assert.Equal(t, StatusRunning, reported.Status)

	repeated, err := store.ReportTranscriptFailure(context.Background(), &ReportTranscriptFailureRequest{
		TaskID: "output", ExpectedVersion: reported.Version, Error: "close failed",
	})
	require.NoError(t, err)
	assert.Equal(t, "write failed", repeated.OutputFileErr)
	assert.Equal(t, reported.Version, repeated.Version)

	_, err = store.ReportTranscriptFailure(context.Background(), &ReportTranscriptFailureRequest{
		TaskID: "output", ExpectedVersion: started.Version, Error: "stale attempt",
	})
	assert.ErrorIs(t, err, ErrVersionConflict)
}

func TestAttack_TaskEventReplayFencesStaleAttempt(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "output-feed")
	first, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt,
		EventID: "first", Data: []byte("first"),
	})
	require.NoError(t, err)
	assert.True(t, first.Inserted)
	second, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt,
		EventID: "second", Data: []byte("second"),
	})
	require.NoError(t, err)
	assert.True(t, second.Inserted)

	page, err := store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 1, NewestFirst: true,
	})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.NotNil(t, page.Events[0])
	assert.Equal(t, "second", string(page.Events[0].Data))

	_, err = store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt + 1,
		EventID: "first", Data: []byte("first"),
	})
	assert.ErrorIs(t, err, ErrLeaseLost)

	_, err = store.CompleteIfNoInputs(
		context.Background(),
		&CompleteIfNoInputsRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0, ResultData: []byte("done"),
		},
	)
	require.NoError(t, err)
	page, err = store.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
}

func TestInMemoryStoreCheckpointedPauseHasNoTerminalResult_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "waiting")

	waiting, err := store.WaitInputIfNoInputs(
		context.Background(),
		&WaitInputIfNoInputsRequest{
			TaskID: "waiting", ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0, Checkpoint: []byte("checkpoint"),
		},
	)
	require.NoError(t, err)
	assert.Equal(t, StatusWaitingInput, waiting.Status)
	assert.Equal(t, "checkpoint", string(waiting.Checkpoint))
	assert.Empty(t, waiting.ResultData)
	assert.Empty(t, waiting.ResultError)

	started = createAndStart(t, store, "missing-checkpoint")
	_, err = store.WaitInputIfNoInputs(
		context.Background(),
		&WaitInputIfNoInputsRequest{
			TaskID: "missing-checkpoint", ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0,
		},
	)
	require.Error(t, err)
	stillRunning, getErr := store.Get(context.Background(), "missing-checkpoint")
	require.NoError(t, getErr)
	assert.Equal(t, StatusRunning, stillRunning.Status)
}

func TestInMemoryStoreTerminalResultInvariant_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "terminal")

	completed, err := store.CompleteIfNoInputs(
		context.Background(),
		&CompleteIfNoInputsRequest{
			TaskID: "terminal", ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0, ResultData: []byte("final"),
		},
	)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, completed.Status)
	assert.Equal(t, "final", string(completed.ResultData))
	require.NotNil(t, completed.DoneAt)
}

func TestInMemoryStoreExpiredLeaseRedispatchesWithCheckpoint_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newInMemoryStoreWithClock(
		&InMemoryStoreConfig{ActiveAttemptTimeout: 5 * time.Second},
		clock.Now,
	)
	started := createAndStart(t, store, "recovery")
	store.mu.Lock()
	store.tasks["recovery"].Checkpoint = []byte("checkpoint")
	store.mu.Unlock()
	require.Equal(t, StatusRunning, started.Status)

	clock.Advance(6 * time.Second)
	recovered, err := store.Get(context.Background(), "recovery")
	require.NoError(t, err)
	assert.Equal(t, StatusPending, recovered.Status)
	assert.Equal(t, "checkpoint", string(recovered.Checkpoint))

	reclaimed, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: "recovery", ExpectedVersion: recovered.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), reclaimed.Attempt)
	assert.Equal(t, StatusRunning, reclaimed.Status)
	assert.Equal(t, "checkpoint", string(reclaimed.Checkpoint))
}

func TestInMemoryStoreExpiredNonRetryableLeaseFails_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newInMemoryStoreWithClock(
		&InMemoryStoreConfig{ActiveAttemptTimeout: 5 * time.Second},
		clock.Now,
	)
	spec := validSpec("local-expired")
	createAndStartWithPolicy(t, store, spec.ID, LeaseExpiryFail)
	clock.Advance(6 * time.Second)
	failed, err := store.Get(context.Background(), spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, failed.Status)
	assert.Equal(t, "execution lease expired and retry is disabled", failed.ResultError)
	require.NotNil(t, failed.DoneAt)
}

func TestInMemoryStoreExpiredCanceledLeasePreservesRecoverableStop_BitsUT(t *testing.T) {
	for _, policy := range []LeaseExpiryPolicy{LeaseExpiryRetry, LeaseExpiryFail} {
		t.Run(string(policy), func(t *testing.T) {
			clock := &testClock{now: time.Unix(100, 0)}
			store := newInMemoryStoreWithClock(
				&InMemoryStoreConfig{ActiveAttemptTimeout: 5 * time.Second},
				clock.Now,
			)
			spec := validSpec("cancel-expired-" + string(policy))
			started := createAndStartWithPolicy(t, store, spec.ID, policy)
			requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
				TaskID: spec.ID, ExpectedVersion: started.Version,
				Reason: "deployment shutdown",
			})
			require.NoError(t, err)
			assert.Equal(t, StatusRunning, requested.Status)
			require.NotNil(t, requested.CancelRequestedAt)
			assert.Equal(t, "deployment shutdown", requested.CancelReason)

			clock.Advance(6 * time.Second)
			resolved, err := store.Get(context.Background(), spec.ID)
			require.NoError(t, err)
			if policy == LeaseExpiryRetry {
				assert.Equal(t, StatusPending, resolved.Status)
				assert.NotNil(t, resolved.CancelRequestedAt)
				assert.Equal(t, "deployment shutdown", resolved.CancelReason)
				assert.Nil(t, resolved.DoneAt)
			} else {
				assert.Equal(t, StatusCanceled, resolved.Status)
				assert.Equal(t, "deployment shutdown", resolved.ResultError)
				require.NotNil(t, resolved.DoneAt)
			}
		})
	}
}

func TestInMemoryStoreCancellationIntentReconcilesToCanceled_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	task := createAndStart(t, store, "cancel")

	requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: task.Spec.ID, ExpectedVersion: task.Version, Reason: "operator request",
	})
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, requested.Status)
	assert.NotNil(t, requested.CancelRequestedAt)
	assert.Equal(t, "operator request", requested.CancelReason)
	assert.Empty(t, requested.ResultData)
	assert.Empty(t, requested.ResultError)
	repeated, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: task.Spec.ID, ExpectedVersion: requested.Version, Reason: "changed reason",
	})
	require.NoError(t, err)
	assert.Equal(t, requested.Version, repeated.Version)
	assert.Equal(t, requested.CancelRequestedAt, repeated.CancelRequestedAt)
	assert.Equal(t, "operator request", repeated.CancelReason)

	_, err = store.CompleteIfNoInputs(
		context.Background(),
		&CompleteIfNoInputsRequest{
			TaskID: "cancel", ExpectedVersion: requested.Version,
			Attempt: task.Attempt, InputCursor: 0, ResultData: []byte("late"),
		},
	)
	assert.ErrorIs(t, err, ErrLeaseLost)
	_, err = store.Fail(context.Background(), &FailTaskRequest{
		TaskID: "cancel", ExpectedVersion: requested.Version, Error: "late failure",
	})
	assert.ErrorIs(t, err, ErrLeaseLost)

	canceled, err := store.AckCancel(context.Background(), &AckCancelRequest{
		TaskID: "cancel", ExpectedVersion: requested.Version, Reason: "executor reason",
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, canceled.Status)
	assert.Equal(t, "operator request", canceled.ResultError)
}

func TestTaskRuntimeCommitReconcilesConcurrentCancellation_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "cancel-before-commit")
	runtime := newTaskRuntime(
		store, store, started.Spec.ID, started.Attempt, started.Version, nil,
	)

	requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version, Reason: "operator request",
	})
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, requested.Status)

	committed, err := runtime.commit(context.Background(), &ExecutionResult{
		Action: ExecutionActionComplete,
		Data:   []byte("late completion"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, committed.Status)
	assert.Empty(t, committed.ResultData)
	assert.Equal(t, "operator request", committed.ResultError)
}

func TestInMemoryStoreRunningAttemptCanCommitCanceled_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	task := createAndStart(t, store, "local-cancel")

	canceled, err := store.AckCancel(context.Background(), &AckCancelRequest{
		TaskID: task.Spec.ID, ExpectedVersion: task.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, canceled.Status)
	assert.Equal(t, "task was canceled", canceled.ResultError)
	assert.Nil(t, canceled.CancelRequestedAt)
}

func TestStoreValidationBoundaries(t *testing.T) {
	specCases := []struct {
		name string
		spec Spec
	}{
		{name: "missing identity", spec: Spec{}},
		{name: "notification without session", spec: Spec{
			ID: "task", ExecutorKey: "test", NotifySession: true,
		}},
	}
	for _, testCase := range specCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Error(t, validateSpec(testCase.spec))
		})
	}

	require.Error(t, validateCreateTaskRequest(&CreateTaskRequest{
		Spec: validSpec("policy"), LeaseExpiryPolicy: LeaseExpiryPolicy("unknown"),
	}))
	require.Error(t, validateTranscriptFailure(""))
	require.Error(t, validateTranscriptFailure(string(make([]byte, 4097))))

	snapshotCases := []struct {
		name        string
		status      Status
		data        []byte
		resultError string
	}{
		{name: "pending result", status: StatusPending, data: []byte("result")},
		{name: "unsupported status", status: Status("unknown")},
		{name: "completed error", status: StatusCompleted, resultError: "error"},
		{name: "failed without error", status: StatusFailed},
		{name: "canceled data", status: StatusCanceled, data: []byte("result")},
		{name: "oversized error", status: StatusFailed, resultError: string(make([]byte, 4097))},
	}
	for _, testCase := range snapshotCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Error(t, validateTaskSnapshot(
				testCase.status, testCase.data, testCase.resultError,
			))
		})
	}
}
