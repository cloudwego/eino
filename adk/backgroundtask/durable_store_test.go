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

func validSpec(id string) Spec {
	return Spec{
		ID: id, ExecutorKey: "test", Payload: []byte("payload"),
		SessionID: "session", Notify: &NotificationTarget{
			Kind: "session_inbox", TargetID: "session",
			Metadata: map[string]string{"test/key": "value"},
		},
	}
}

func createAndStart(t *testing.T, store *InMemoryStore, id string) *Task {
	t.Helper()
	created, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: validSpec(id), LeaseExpiryPolicy: LeaseExpiryRetry,
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
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, created.Status)
	assert.Equal(t, LeaseExpiryRetry, created.LeaseExpiryPolicy)
	assert.Empty(t, created.ResultData)
	assert.Empty(t, created.ResultError)
	assert.Nil(t, created.PendingResume)

	spec.Payload[0] = 'X'
	spec.Notify.Metadata["test/key"] = "changed"
	stored, err := store.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(stored.Spec.Payload))
	assert.Equal(t, "value", stored.Spec.Notify.Metadata["test/key"])
}

func TestInMemoryStoreCreateAndStartIsAtomic_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	spec := validSpec("local")
	started, err := store.CreateAndStart(
		context.Background(), &CreateTaskRequest{
			Spec: spec, LeaseExpiryPolicy: LeaseExpiryFail,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, started.Status)
	assert.Equal(t, int64(1), started.Attempt)
	assert.Equal(t, int64(1), started.Version)
	pending, err := store.ListPending(context.Background(), &ListPendingRequest{
		ExecutorKeys: []string{"test"},
	})
	require.NoError(t, err)
	assert.Empty(t, pending.Tasks)
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
			_, mutationErr := store.Complete(context.Background(), &CompleteTaskRequest{
				TaskID: started.Spec.ID, ExpectedVersion: requested.Version, Data: []byte("late"),
			})
			return mutationErr
		}},
		{name: "fail", mutate: func() error {
			_, mutationErr := store.Fail(context.Background(), &FailTaskRequest{
				TaskID: started.Spec.ID, ExpectedVersion: requested.Version, Error: "late",
			})
			return mutationErr
		}},
		{name: "wait input", mutate: func() error {
			_, mutationErr := store.WaitInput(context.Background(), &WaitInputTaskRequest{
				TaskID: started.Spec.ID, ExpectedVersion: requested.Version, Checkpoint: []byte("late"),
			})
			return mutationErr
		}},
		{name: "suspend", mutate: func() error {
			_, mutationErr := store.Suspend(context.Background(), &SuspendTaskRequest{
				TaskID: started.Spec.ID, ExpectedVersion: requested.Version, Checkpoint: []byte("late"),
			})
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

	canceled, err := store.Cancel(context.Background(), &CancelTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: requested.Version,
	})
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, canceled.Status)
}

func TestAttack_TaskEventOrderSpansAttemptsWithoutExposingAttempt(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := NewInMemoryStore(&InMemoryStoreConfig{
		Clock: clock.Now, ActiveAttemptTimeout: time.Second,
	})
	firstAttempt := createAndStart(t, store, "output-retry")
	first, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: firstAttempt.Spec.ID, Attempt: firstAttempt.Attempt,
		EventID: "first", Data: []byte("first"),
	})
	require.NoError(t, err)
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
	require.Equal(t, "second", second.Event.EventID)

	output, err := store.ReadRecentTaskEvents(context.Background(), &ReadRecentTaskEventsRequest{
		TaskID: secondAttempt.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, output.Events, 2)
	require.Equal(t, []string{"first", "second"}, []string{
		output.Events[0].EventID, output.Events[1].EventID,
	})
	require.Equal(t, []string{"first", "second"}, []string{
		string(output.Events[0].Data), string(output.Events[1].Data),
	})
}

func TestInMemoryStoreYieldReturnsRecoverableAttemptToPending_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "yield")
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
		ConsumerID: "test", Limit: 10, VisibilityTime: time.Second,
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
	require.Error(t, err)
	first, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt,
		EventID: "event-1", Data: []byte("payload"),
	})
	require.NoError(t, err)
	require.True(t, first.Inserted)
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
	require.Equal(t, first.Event, replayed.Event)
	require.Equal(t, createdAt, replayed.Event.CreatedAt)

	_, err = store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: restarted.Spec.ID, Attempt: restarted.Attempt,
		EventID: "event-1", Data: []byte("different"),
	})
	require.ErrorIs(t, err, ErrTaskEventConflict)

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
	stored, err := store.ReadRecentTaskEvents(context.Background(), &ReadRecentTaskEventsRequest{
		TaskID: restarted.Spec.ID,
	})
	require.NoError(t, err)
	require.Equal(t, "payload", string(stored.Events[0].Data))
}

func TestInMemoryStoreReadRecentTaskEventsReturnsNewestChronologically_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "recent-output")
	for _, value := range []string{"one", "two", "three"} {
		_, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
			TaskID: started.Spec.ID, Attempt: started.Attempt,
			EventID: value, Data: []byte(value),
		})
		require.NoError(t, err)
	}
	result, err := store.ReadRecentTaskEvents(context.Background(), &ReadRecentTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 2)
	require.Equal(t, "two", string(result.Events[0].Data))
	require.Equal(t, "three", string(result.Events[1].Data))
}

func TestInMemoryStoreHeartbeatSuspensionReleaseAndWait(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "suspension")
	waitDone := make(chan struct {
		task *Task
		err  error
	}, 1)
	go func() {
		task, err := store.Wait(context.Background(), &WaitUpdateRequest{
			TaskID: started.Spec.ID, AfterVersion: started.Version,
		})
		waitDone <- struct {
			task *Task
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

	suspended, err := store.Suspend(context.Background(), &SuspendTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: heartbeat.Version,
		Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)
	require.Equal(t, StatusSuspended, suspended.Status)
	require.Equal(t, "checkpoint", string(suspended.Checkpoint))

	released, err := store.ReleaseSuspension(context.Background(), &ReleaseSuspensionRequest{
		TaskID: started.Spec.ID, ExpectedVersion: suspended.Version,
	})
	require.NoError(t, err)
	require.Equal(t, StatusPending, released.Status)
	require.Equal(t, "checkpoint", string(released.Checkpoint))
}

func TestInMemoryStoreReportOutputFailureIsFencedAndFirstErrorWins_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "output")
	reported, err := store.ReportOutputFailure(context.Background(), &ReportOutputFailureRequest{
		TaskID: "output", ExpectedVersion: started.Version, Error: "write failed",
	})
	require.NoError(t, err)
	assert.Equal(t, "write failed", reported.OutputFileErr)
	assert.Equal(t, started.Version+1, reported.Version)
	assert.Equal(t, StatusRunning, reported.Status)

	repeated, err := store.ReportOutputFailure(context.Background(), &ReportOutputFailureRequest{
		TaskID: "output", ExpectedVersion: reported.Version, Error: "close failed",
	})
	require.NoError(t, err)
	assert.Equal(t, "write failed", repeated.OutputFileErr)
	assert.Equal(t, reported.Version, repeated.Version)

	_, err = store.ReportOutputFailure(context.Background(), &ReportOutputFailureRequest{
		TaskID: "output", ExpectedVersion: started.Version, Error: "stale attempt",
	})
	assert.ErrorIs(t, err, ErrVersionConflict)
}

func TestInMemoryStoreTaskEventFeedSupportsReplayAndAttemptFencing_BitsUT(t *testing.T) {
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

	page, err := store.ReadRecentTaskEvents(context.Background(), &ReadRecentTaskEventsRequest{
		TaskID: started.Spec.ID, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Equal(t, "second", string(page.Events[0].Data))

	_, err = store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt + 1,
		EventID: "first", Data: []byte("first"),
	})
	assert.ErrorIs(t, err, ErrLeaseLost)

	_, err = store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version, Data: []byte("done"),
	})
	require.NoError(t, err)
	page, err = store.ReadRecentTaskEvents(context.Background(), &ReadRecentTaskEventsRequest{
		TaskID: started.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
}

func TestInMemoryStoreCheckpointedPauseHasNoTerminalResult_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "waiting")

	waiting, err := store.WaitInput(context.Background(), &WaitInputTaskRequest{
		TaskID: "waiting", ExpectedVersion: started.Version, Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusWaitingInput, waiting.Status)
	assert.Equal(t, "checkpoint", string(waiting.Checkpoint))
	assert.Empty(t, waiting.ResultData)
	assert.Empty(t, waiting.ResultError)

	started = createAndStart(t, store, "missing-checkpoint")
	_, err = store.WaitInput(context.Background(), &WaitInputTaskRequest{
		TaskID: "missing-checkpoint", ExpectedVersion: started.Version,
	})
	require.Error(t, err)
	stillRunning, getErr := store.Get(context.Background(), "missing-checkpoint")
	require.NoError(t, getErr)
	assert.Equal(t, StatusRunning, stillRunning.Status)
}

func TestInMemoryStoreTerminalResultInvariant_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "terminal")

	completed, err := store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: "terminal", ExpectedVersion: started.Version, Data: []byte("final"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, completed.Status)
	assert.Equal(t, "final", string(completed.ResultData))
	require.NotNil(t, completed.DoneAt)
}

func TestInMemoryStoreExpiredLeaseRedispatchesWithCheckpoint_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := NewInMemoryStore(&InMemoryStoreConfig{
		Clock: clock.Now, ActiveAttemptTimeout: 5 * time.Second,
	})
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
	store := NewInMemoryStore(&InMemoryStoreConfig{
		Clock: clock.Now, ActiveAttemptTimeout: 5 * time.Second,
	})
	spec := validSpec("local-expired")
	_, err := store.CreateAndStart(
		context.Background(), &CreateTaskRequest{
			Spec: spec, LeaseExpiryPolicy: LeaseExpiryFail,
		},
	)
	require.NoError(t, err)
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
			store := NewInMemoryStore(&InMemoryStoreConfig{
				Clock: clock.Now, ActiveAttemptTimeout: 5 * time.Second,
			})
			spec := validSpec("cancel-expired-" + string(policy))
			started, err := store.CreateAndStart(
				context.Background(), &CreateTaskRequest{
					Spec: spec, LeaseExpiryPolicy: policy,
				},
			)
			require.NoError(t, err)
			requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
				TaskID: spec.ID, ExpectedVersion: started.Version,
			})
			require.NoError(t, err)
			assert.Equal(t, StatusRunning, requested.Status)
			require.NotNil(t, requested.CancelRequestedAt)

			clock.Advance(6 * time.Second)
			resolved, err := store.Get(context.Background(), spec.ID)
			require.NoError(t, err)
			if policy == LeaseExpiryRetry {
				assert.Equal(t, StatusPending, resolved.Status)
				assert.NotNil(t, resolved.CancelRequestedAt)
				assert.Nil(t, resolved.DoneAt)
			} else {
				assert.Equal(t, StatusCanceled, resolved.Status)
				assert.Equal(t, canceledError, resolved.ResultError)
				require.NotNil(t, resolved.DoneAt)
			}
		})
	}
}

func TestInMemoryStoreResumePersistsPendingResumeBytes_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "resume")
	waiting, err := store.WaitInput(context.Background(), &WaitInputTaskRequest{
		TaskID: "resume", ExpectedVersion: started.Version, Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)

	resumed, err := store.Resume(context.Background(), &ResumeRequest{
		TaskID: "resume", ExpectedVersion: waiting.Version,
		Data: []byte("answer"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, resumed.Status)
	assert.Equal(t, "checkpoint", string(resumed.Checkpoint))
	require.NotNil(t, resumed.PendingResume)
	assert.Equal(t, "answer", string(resumed.PendingResume))
}

func TestInMemoryStoreResumeRejectsStaleTaskVersion_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "stale-resume")
	waiting, err := store.WaitInput(context.Background(), &WaitInputTaskRequest{
		TaskID: "stale-resume", ExpectedVersion: started.Version, Checkpoint: []byte("checkpoint-1"),
	})
	require.NoError(t, err)

	store.mu.Lock()
	store.tasks["stale-resume"].Checkpoint = []byte("checkpoint-2")
	store.advanceLocked(store.tasks["stale-resume"])
	store.mu.Unlock()

	_, err = store.Resume(context.Background(), &ResumeRequest{
		TaskID: "stale-resume", ExpectedVersion: waiting.Version,
		Data: []byte("answer"),
	})
	assert.ErrorIs(t, err, ErrVersionConflict)
}

func TestInMemoryStoreCancellationIntentReconcilesToCanceled_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	task := createAndStart(t, store, "cancel")

	requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: task.Spec.ID, ExpectedVersion: task.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, requested.Status)
	assert.NotNil(t, requested.CancelRequestedAt)
	assert.Empty(t, requested.ResultData)
	assert.Empty(t, requested.ResultError)
	repeated, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: task.Spec.ID, ExpectedVersion: requested.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, requested.Version, repeated.Version)
	assert.Equal(t, requested.CancelRequestedAt, repeated.CancelRequestedAt)

	_, err = store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: "cancel", ExpectedVersion: requested.Version, Data: []byte("late"),
	})
	assert.ErrorIs(t, err, ErrLeaseLost)
	_, err = store.Fail(context.Background(), &FailTaskRequest{
		TaskID: "cancel", ExpectedVersion: requested.Version, Error: "late failure",
	})
	assert.ErrorIs(t, err, ErrLeaseLost)

	canceled, err := store.Cancel(context.Background(), &CancelTaskRequest{
		TaskID: "cancel", ExpectedVersion: requested.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, canceled.Status)
	assert.Equal(t, "task was canceled", canceled.ResultError)
}

func TestTaskRuntimeCommitReconcilesConcurrentCancellation_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "cancel-before-commit")
	runtime := newTaskRuntime(store, started.Spec.ID, started.Attempt, started.Version)

	requested, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, requested.Status)

	committed, err := runtime.commit(context.Background(), &ExecutionResult{
		Status: StatusCompleted,
		Data:   []byte("late completion"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, committed.Status)
	assert.Empty(t, committed.ResultData)
	assert.Equal(t, canceledError, committed.ResultError)
}

func TestInMemoryStoreRunningAttemptCanCommitCanceled_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	task := createAndStart(t, store, "local-cancel")

	canceled, err := store.Cancel(context.Background(), &CancelTaskRequest{
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
		{name: "incomplete notification", spec: Spec{
			ID: "task", ExecutorKey: "test", Notify: &NotificationTarget{},
		}},
		{name: "unnamespaced metadata", spec: Spec{
			ID: "task", ExecutorKey: "test",
			Notify: &NotificationTarget{
				Kind: "custom", TargetID: "target",
				Metadata: map[string]string{"plain": "value"},
			},
		}},
		{name: "session target mismatch", spec: Spec{
			ID: "task", ExecutorKey: "test", SessionID: "session-a",
			Notify: &NotificationTarget{
				Kind: "session_inbox", TargetID: "session-b",
			},
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
	require.Error(t, validateOutputFailure(""))
	require.Error(t, validateOutputFailure(string(make([]byte, 4097))))

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
