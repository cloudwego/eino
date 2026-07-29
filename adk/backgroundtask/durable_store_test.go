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

func createAndClaim(t *testing.T, store *MemoryStore, id string, lease time.Duration) (*Task, LeaseToken) {
	t.Helper()
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: validSpec(id)})
	require.NoError(t, err)
	claim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: id, ExpectedVersion: created.TransitionVersion,
		LeaseOwnerID: "worker", LeaseDuration: lease,
	})
	require.NoError(t, err)
	return claim.Task, claim.Lease
}

func TestMemoryStoreCreatePersistsPendingSnapshot_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	spec := validSpec("create")

	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, created.Status)
	assert.Nil(t, created.Result)
	assert.Nil(t, created.PendingResume)

	spec.Payload[0] = 'X'
	spec.Notify.Metadata["test/key"] = "changed"
	stored, err := store.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(stored.Spec.Payload))
	assert.Equal(t, "value", stored.Spec.Notify.Metadata["test/key"])
}

func TestMemoryStoreCheckpointedPauseHasNoTerminalResult_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	_, lease := createAndClaim(t, store, "waiting", time.Minute)

	committed, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Status: StatusWaitingInput, Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusWaitingInput, committed.Task.Status)
	assert.Equal(t, "checkpoint", string(committed.Task.Checkpoint))
	assert.Equal(t, int64(1), committed.Task.CheckpointVersion)
	assert.Nil(t, committed.Task.Result)
	require.NotNil(t, committed.Notification)
	assert.Equal(t, NotificationWaitingInput, committed.Notification.EventKind)

	_, lease = createAndClaim(t, store, "missing-checkpoint", time.Minute)
	_, err = store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Status: StatusWaitingInput,
	})
	require.Error(t, err)
	stillRunning, getErr := store.Get(context.Background(), "missing-checkpoint")
	require.NoError(t, getErr)
	assert.Equal(t, StatusRunning, stillRunning.Status)
}

func TestMemoryStoreTerminalResultInvariant_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	_, lease := createAndClaim(t, store, "terminal", time.Minute)

	_, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Status: StatusCompleted,
	})
	assert.ErrorIs(t, err, ErrInvalidResult)

	completed, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Status: StatusCompleted, Result: &Result{Data: []byte("final")},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, completed.Task.Status)
	assert.Equal(t, "final", string(completed.Task.Result.Data))
	require.NotNil(t, completed.Task.DoneAt)
}

func TestMemoryStoreExpiredLeaseRedispatchesWithCheckpoint_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	_, lease := createAndClaim(t, store, "recovery", 5*time.Second)
	_, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Status: StatusRunning, Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)

	clock.Advance(6 * time.Second)
	recovered, err := store.Get(context.Background(), "recovery")
	require.NoError(t, err)
	assert.Equal(t, StatusPending, recovered.Status)
	assert.Equal(t, "checkpoint", string(recovered.Checkpoint))
	assert.Empty(t, recovered.LeaseOwner)

	reclaimed, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: "recovery", ExpectedVersion: recovered.TransitionVersion,
		LeaseOwnerID: "worker-2", LeaseDuration: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), reclaimed.Task.Attempt)
	assert.Equal(t, StatusRunning, reclaimed.Task.Status)
	assert.Equal(t, "checkpoint", string(reclaimed.Task.Checkpoint))
}

func TestMemoryStoreResumePersistsStandalonePendingResume_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	_, lease := createAndClaim(t, store, "resume", time.Minute)
	waiting, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Status: StatusWaitingInput, Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)

	resumed, err := store.Resume(context.Background(), &ResumeTaskRequest{
		TaskID: "resume", ExpectedVersion: waiting.Task.TransitionVersion,
		Data: []byte("answer"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, resumed.Status)
	assert.Equal(t, "checkpoint", string(resumed.Checkpoint))
	require.NotNil(t, resumed.PendingResume)
	assert.Equal(t, resumed.CheckpointVersion, resumed.PendingResume.CheckpointVersion)
	assert.Equal(t, "answer", string(resumed.PendingResume.Data))
}

func TestMemoryStoreClaimDropsStalePendingResume_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	_, lease := createAndClaim(t, store, "stale-resume", time.Minute)
	waiting, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Status: StatusWaitingInput, Checkpoint: []byte("checkpoint-1"),
	})
	require.NoError(t, err)
	resumed, err := store.Resume(context.Background(), &ResumeTaskRequest{
		TaskID: "stale-resume", ExpectedVersion: waiting.Task.TransitionVersion,
		Data: []byte("answer"),
	})
	require.NoError(t, err)

	store.mu.Lock()
	store.tasks["stale-resume"].Checkpoint = []byte("checkpoint-2")
	store.tasks["stale-resume"].CheckpointVersion++
	store.mu.Unlock()

	claimed, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: "stale-resume", ExpectedVersion: resumed.TransitionVersion,
		LeaseOwnerID: "worker-2", LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	assert.Nil(t, claimed.Task.PendingResume)
	assert.Equal(t, "checkpoint-2", string(claimed.Task.Checkpoint))
}

func TestMemoryStoreCancelingReconcilesToCanceled_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	task, _ := createAndClaim(t, store, "cancel", time.Minute)

	canceling, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: task.Spec.ID, ExpectedVersion: task.TransitionVersion,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceling, canceling.Task.Status)
	assert.Nil(t, canceling.Task.Result)

	canceled, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: LeaseToken{
			TaskID: "cancel", ExpectedVersion: canceling.Task.TransitionVersion,
			LeaseOwnerID: "worker", Generation: task.LeaseGeneration,
		},
		Status: StatusCanceled, Result: &Result{Error: "canceled"},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, canceled.Task.Status)
	assert.Equal(t, "canceled", canceled.Task.Result.Error)
}
