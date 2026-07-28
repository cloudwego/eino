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
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func digestFor(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func float64Pointer(value float64) *float64 { return &value }
func statusPointer(status Status) *Status   { return &status }
func validInline(payload string) ArtifactValue {
	value := []byte(payload)
	return ArtifactValue{Payload: value, Encoding: "utf-8", Digest: digestFor(value), Size: int64(len(value))}
}

func validSpec(id string) Spec {
	return Spec{
		ID: id, ExecutorKey: "test", PayloadVersion: "v1",
		Payload:   []byte(`{"work":"test"}`),
		SessionID: "session-1",
		Notify:    &NotificationTarget{Kind: "session_inbox", TargetID: "session-1", Metadata: map[string]string{"test/route": "one"}},
		Recovery: RecoveryPolicy{
			OnLeaseExpired: RecoveryResumeCheckpoint, OnMissingCheckpoint: RecoveryRestartFromSpec, MaxAttempts: 3,
		},
		Result: ResultPolicy{ResultFormat: "text/plain"},
	}
}

func createAndClaim(t *testing.T, store *MemoryStore, id string, lease time.Duration) (*Task, LeaseToken) {
	t.Helper()
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: validSpec(id)})
	require.NoError(t, err)
	claim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: id, ExpectedVersion: created.TransitionVersion, WorkerID: "worker-1", LeaseDuration: lease,
	})
	require.NoError(t, err)
	return claim.Task, claim.Lease
}

func TestMemoryStoreCreateDeepCopiesAndStartsUpdateSequence_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	spec := validSpec("create")

	task, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	spec.Payload[0] = 'X'
	spec.Notify.Metadata["test/route"] = "mutated"
	task.Spec.Payload[0] = 'Y'

	got, err := store.Get(context.Background(), "create")
	require.NoError(t, err)
	assert.Equal(t, StatusPending, got.Status)
	assert.Equal(t, int64(1), got.TransitionVersion)
	assert.Equal(t, byte('{'), got.Spec.Payload[0])
	assert.Equal(t, "one", got.Spec.Notify.Metadata["test/route"])

	updates, err := store.ListUpdates(context.Background(), &ListTaskUpdatesRequest{TaskID: "create", Limit: 10})
	require.NoError(t, err)
	require.Len(t, updates.Updates, 1)
	assert.Equal(t, UpdateStatus, updates.Updates[0].Kind)
	assert.Equal(t, statusPointer(StatusPending), updates.Updates[0].Status)

	outbox, err := store.Receive(context.Background(), &ReceiveNotificationsRequest{
		ConsumerID: "consumer", Limit: 10, VisibilityTime: time.Second,
	})
	require.NoError(t, err)
	assert.Empty(t, outbox.Deliveries, "Create must not emit an outbox record")
}

func TestMemoryStoreLeaseFencingAndUpdatePagination_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	task, lease := createAndClaim(t, store, "updates", time.Minute)

	first, err := store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: lease, Kind: UpdateProgress,
		Progress: &Progress{Current: float64Pointer(1), Total: float64Pointer(2), Unit: "steps"},
	})
	require.NoError(t, err)
	lease.ExpectedVersion = first.Task.TransitionVersion
	second, err := store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: lease, Kind: UpdateMessage,
		Payload: &UpdatePayload{Type: "text/plain", Value: validInline("halfway")},
	})
	require.NoError(t, err)

	_, err = store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: LeaseToken{TaskID: task.Spec.ID, ExpectedVersion: task.TransitionVersion, WorkerID: "worker-1", Generation: lease.Generation},
		Kind:  UpdateMessage, Payload: &UpdatePayload{Type: "text/plain", Value: validInline("stale")},
	})
	assert.ErrorIs(t, err, ErrVersionConflict)

	page1, err := store.ListUpdates(context.Background(), &ListTaskUpdatesRequest{TaskID: task.Spec.ID, AfterSequence: 2, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page1.Updates, 1)
	assert.Equal(t, first.Update.Sequence, page1.NextSequence)
	page2, err := store.ListUpdates(context.Background(), &ListTaskUpdatesRequest{
		TaskID: task.Spec.ID, AfterSequence: page1.NextSequence, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page2.Updates, 1)
	assert.Equal(t, second.Update.Sequence, page2.Updates[0].Sequence)
}

func TestMemoryStoreWaitingInputIsAtomic_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	_, lease := createAndClaim(t, store, "interrupt", time.Minute)
	checkpoint := &CheckpointRef{
		ExecutorKey: "test", Format: "runner", Version: "v1", Sequence: 1,
		State: validInline("checkpoint"),
	}
	result, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease,
		Mutation: TaskMutation{
			ToStatus: StatusWaitingInput, Checkpoint: checkpoint,
			InputRequest: &UpdatePayload{Type: "application/json", Value: validInline(`{"prompt":"approve"}`)},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusWaitingInput, result.Task.Status)
	require.Len(t, result.Updates, 2)
	assert.Equal(t, UpdateStatus, result.Updates[0].Kind)
	assert.Equal(t, UpdateInputRequired, result.Updates[1].Kind)
	require.NotNil(t, result.Notification)
	assert.Equal(t, NotificationWaitingInput, result.Notification.EventKind)
	assert.Equal(t, result.Updates[1].Sequence, result.Notification.UpdateSequence)

	_, err = store.Resume(context.Background(), &ResumeTaskRequest{
		TaskID: "interrupt", ExpectedVersion: result.Task.TransitionVersion,
		ResumeData: []byte("yes"), ResumeEncoding: "text/plain",
	})
	require.NoError(t, err)
}

func TestMemoryStoreListClaimableAcceptsRegistryWildcardCapability_DefectProbing(t *testing.T) {
	store := NewMemoryStore(nil)
	_, err := store.Create(context.Background(), &CreateTaskRequest{Spec: validSpec("claimable")})
	require.NoError(t, err)

	result, err := store.ListClaimable(context.Background(), &ListClaimableRequest{
		Capabilities: []ExecutorCapability{{ExecutorKey: "test", PayloadVersion: "*"}},
	})
	require.NoError(t, err)
	require.Len(t, result.Tasks, 1, "[defect-probing] registry wildcard must advertise compatible task versions")
}

func TestMemoryStoreRejectsInvalidInlineDigest_DefectProbing(t *testing.T) {
	store := NewMemoryStore(nil)
	_, lease := createAndClaim(t, store, "digest", time.Minute)
	value := validInline("authoritative")
	value.Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	_, err := store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: lease, Kind: UpdateMessage, Payload: &UpdatePayload{Type: "text/plain", Value: value},
	})
	assert.ErrorIs(t, err, ErrInvalidArtifact, "[defect-probing] corrupt inline bytes must not become authoritative")
}

func TestMemoryStoreCancelExpiredRunningTaskReturnsCanceled_DefectProbing(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	task, _ := createAndClaim(t, store, "expired-cancel", 10*time.Second)
	clock.Advance(11 * time.Second)

	result, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: task.Spec.ID, ExpectedVersion: task.TransitionVersion,
	})
	require.NoError(t, err, "[defect-probing] stop must return the Store-owned expired-lease cancellation")
	assert.Equal(t, StatusCanceled, result.Task.Status)
}

func TestMemoryStoreRejectsStaleGenerationAfterRecovery_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(200, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	firstTask, firstLease := createAndClaim(t, store, "recovery", 5*time.Second)
	clock.Advance(6 * time.Second)
	recovered, err := store.Get(context.Background(), firstTask.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, recovered.Status)
	second, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: recovered.Spec.ID, ExpectedVersion: recovered.TransitionVersion,
		WorkerID: "worker-2", LeaseDuration: 5 * time.Second,
	})
	require.NoError(t, err)

	_, err = store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: firstLease, Kind: UpdateMessage,
		Payload: &UpdatePayload{Type: "text/plain", Value: validInline("late")},
	})
	assert.True(t, errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrLeaseLost))
	assert.Greater(t, second.Lease.Generation, firstLease.Generation)
}

func TestMemoryStoreUpdateSequenceAndCopiesSurviveRecovery_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(250, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	task, lease := createAndClaim(t, store, "update-recovery", 5*time.Second)
	current := 1.0
	payloadBytes := []byte("first")
	first, err := store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: lease, Kind: UpdateProgress,
		Progress: &Progress{Current: &current, Total: float64Pointer(2), Unit: "steps"},
		Payload: &UpdatePayload{
			Type: "text/plain",
			Value: ArtifactValue{
				Payload: payloadBytes, Encoding: "utf-8",
				Digest: digestFor(payloadBytes), Size: int64(len(payloadBytes)),
			},
		},
	})
	require.NoError(t, err)
	current = 99
	payloadBytes[0] = 'X'
	*first.Update.Progress.Current = 88
	first.Update.Payload.Value.Payload[0] = 'Y'

	clock.Advance(6 * time.Second)
	pending, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	secondClaim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: task.Spec.ID, ExpectedVersion: pending.TransitionVersion,
		WorkerID: "worker-2", LeaseDuration: 5 * time.Second,
	})
	require.NoError(t, err)
	second, err := store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: secondClaim.Lease, Kind: UpdateMessage,
		Payload: &UpdatePayload{Type: "text/plain", Value: validInline("second")},
	})
	require.NoError(t, err)

	all, err := store.ListUpdates(context.Background(), &ListTaskUpdatesRequest{
		TaskID: task.Spec.ID, Limit: 100,
	})
	require.NoError(t, err)
	for i := 1; i < len(all.Updates); i++ {
		assert.Equal(t, all.Updates[i-1].Sequence+1, all.Updates[i].Sequence)
	}
	var firstStored *Update
	for _, update := range all.Updates {
		if update.Sequence == first.Update.Sequence {
			firstStored = update
		}
	}
	require.NotNil(t, firstStored)
	assert.Equal(t, 1.0, *firstStored.Progress.Current)
	assert.Equal(t, "first", string(firstStored.Payload.Value.Payload))
	assert.Equal(t, int64(1), firstStored.Attempt)
	assert.Equal(t, int64(2), second.Update.Attempt)
	assert.Greater(t, second.Update.Sequence, first.Update.Sequence)
}

func TestMemoryStoreWaitUpdatesClosesListWaitRace_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	task, lease := createAndClaim(t, store, "wait-update-race", time.Minute)
	before, err := store.ListUpdates(context.Background(), &ListTaskUpdatesRequest{
		TaskID: task.Spec.ID, AfterSequence: task.LatestUpdateSequence, Limit: 10,
	})
	require.NoError(t, err)
	assert.Empty(t, before.Updates)

	appended, err := store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: lease, Kind: UpdateMessage,
		Payload: &UpdatePayload{Type: "text/plain", Value: validInline("raced")},
	})
	require.NoError(t, err)
	waited, err := store.WaitUpdates(context.Background(), &WaitTaskUpdatesRequest{
		TaskID: task.Spec.ID, AfterSequence: task.LatestUpdateSequence, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, waited.Updates, 1)
	assert.Equal(t, appended.Update.Sequence, waited.Updates[0].Sequence)
	assert.Equal(t, appended.Update.Sequence, waited.NextSequence)
}

func TestMemoryStoreVerifiesExternalCheckpointAndUpdateArtifacts_BitsUT(t *testing.T) {
	verified := make(map[string]int)
	verifier := artifactVerifierFunc(func(_ context.Context, key, _ string, _ int64) error {
		verified[key]++
		return nil
	})
	store := NewMemoryStore(&MemoryStoreConfig{
		ArtifactVerifiers: map[string]ArtifactVerifier{"artifacts": verifier},
	})
	_, lease := createAndClaim(t, store, "external-values", time.Minute)
	checkpointValue := ArtifactValue{
		Ref:    &ArtifactRef{StoreKey: "artifacts", Key: "checkpoint"},
		Digest: digestFor([]byte("checkpoint")), Size: int64(len("checkpoint")),
	}
	checkpointed, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease,
		Mutation: TaskMutation{
			ToStatus: StatusRunning,
			Checkpoint: &CheckpointRef{
				ExecutorKey: "test", Format: "test/checkpoint", Version: "v1",
				Sequence: 1, State: checkpointValue,
			},
		},
	})
	require.NoError(t, err)
	lease.ExpectedVersion = checkpointed.Task.TransitionVersion
	updateValue := ArtifactValue{
		Ref:    &ArtifactRef{StoreKey: "artifacts", Key: "update"},
		Digest: digestFor([]byte("update")), Size: int64(len("update")),
	}
	appended, err := store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: lease, Kind: UpdateMessage,
		Payload: &UpdatePayload{Type: "text/plain", Value: updateValue},
	})
	require.NoError(t, err)
	checkpointValue.Ref.Key = "mutated"
	updateValue.Ref.Key = "mutated"

	stored, err := store.Get(context.Background(), "external-values")
	require.NoError(t, err)
	assert.Equal(t, "checkpoint", stored.Checkpoint.State.Ref.Key)
	assert.Equal(t, "update", appended.Update.Payload.Value.Ref.Key)
	assert.Equal(t, 1, verified["checkpoint"])
	assert.Equal(t, 1, verified["update"])
}
