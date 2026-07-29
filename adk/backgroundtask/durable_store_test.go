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

func createAndStart(t *testing.T, store *MemoryStore, id string) *Task {
	t.Helper()
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: validSpec(id)})
	require.NoError(t, err)
	started, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: id, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	return started
}

func TestMemoryStoreCreatePersistsPendingSnapshot_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	spec := validSpec("create")

	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, created.Status)
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

func TestMemoryStoreCheckpointedPauseHasNoTerminalResult_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
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

func TestMemoryStoreTerminalResultInvariant_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	started := createAndStart(t, store, "terminal")

	completed, err := store.Complete(context.Background(), &CompleteTaskRequest{
		TaskID: "terminal", ExpectedVersion: started.Version, Data: []byte("final"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, completed.Status)
	assert.Equal(t, "final", string(completed.ResultData))
	require.NotNil(t, completed.DoneAt)
}

func TestMemoryStoreExpiredLeaseRedispatchesWithCheckpoint_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
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

func TestMemoryStoreResumePersistsPendingResumeBytes_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	started := createAndStart(t, store, "resume")
	waiting, err := store.WaitInput(context.Background(), &WaitInputTaskRequest{
		TaskID: "resume", ExpectedVersion: started.Version, Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)

	resumed, err := store.Resume(context.Background(), &ResumeTaskRequest{
		TaskID: "resume", ExpectedVersion: waiting.Version,
		Data: []byte("answer"),
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, resumed.Status)
	assert.Equal(t, "checkpoint", string(resumed.Checkpoint))
	require.NotNil(t, resumed.PendingResume)
	assert.Equal(t, "answer", string(resumed.PendingResume))
}

func TestMemoryStoreResumeRejectsStaleTaskVersion_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	started := createAndStart(t, store, "stale-resume")
	waiting, err := store.WaitInput(context.Background(), &WaitInputTaskRequest{
		TaskID: "stale-resume", ExpectedVersion: started.Version, Checkpoint: []byte("checkpoint-1"),
	})
	require.NoError(t, err)

	store.mu.Lock()
	store.tasks["stale-resume"].Checkpoint = []byte("checkpoint-2")
	store.advanceLocked(store.tasks["stale-resume"])
	store.mu.Unlock()

	_, err = store.Resume(context.Background(), &ResumeTaskRequest{
		TaskID: "stale-resume", ExpectedVersion: waiting.Version,
		Data: []byte("answer"),
	})
	assert.ErrorIs(t, err, ErrVersionConflict)
}

func TestMemoryStoreCancelingReconcilesToCanceled_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	task := createAndStart(t, store, "cancel")

	canceling, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: task.Spec.ID, ExpectedVersion: task.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceling, canceling.Status)
	assert.Empty(t, canceling.ResultData)
	assert.Empty(t, canceling.ResultError)

	canceled, err := store.Cancel(context.Background(), &CancelTaskRequest{
		TaskID: "cancel", ExpectedVersion: canceling.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, canceled.Status)
	assert.Equal(t, "task was canceled", canceled.ResultError)
}
