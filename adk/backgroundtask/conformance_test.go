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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkpoint(sequence int64) *CheckpointRef {
	return &CheckpointRef{
		ExecutorKey: "test", Format: "test/checkpoint", Version: "v1",
		Sequence: sequence, State: validInline("checkpoint"),
	}
}

func successfulResult() *ResultRef {
	return &ResultRef{Format: "text/plain", Value: validInline("result")}
}

func mutationForStatus(status Status) TaskMutation {
	switch status {
	case StatusRunning:
		return TaskMutation{ToStatus: status, Checkpoint: checkpoint(1)}
	case StatusWaitingInput:
		return TaskMutation{
			ToStatus: status, Checkpoint: checkpoint(1),
			InputRequest: &UpdatePayload{Type: "test/input", Value: validInline("input")},
		}
	case StatusSuspended:
		return TaskMutation{ToStatus: status, Checkpoint: checkpoint(1)}
	case StatusCompleted:
		return TaskMutation{ToStatus: status, Result: successfulResult()}
	case StatusFailed, StatusCanceled:
		return TaskMutation{ToStatus: status, TerminalReason: string(status)}
	default:
		return TaskMutation{ToStatus: status}
	}
}

type cancelAwareExecutor struct {
	started chan struct{}
	finish  chan struct{}
}

type artifactVerifierFunc func(context.Context, string, string, int64) error

func (f artifactVerifierFunc) Verify(ctx context.Context, key, digest string, size int64) error {
	return f(ctx, key, digest, size)
}

type checkpointRejectingExecutor struct {
	checkpoints chan *CheckpointRef
}

func (e *checkpointRejectingExecutor) Key() string         { return "test" }
func (e *checkpointRejectingExecutor) Validate(Spec) error { return nil }
func (e *checkpointRejectingExecutor) ValidateResume(
	context.Context,
	*ValidateResumeRequest,
) (*ValidateResumeResult, error) {
	return nil, errors.New("unexpected resume")
}
func (e *checkpointRejectingExecutor) ValidateCheckpoint(Spec, *CheckpointRef) error {
	return errors.New("incompatible checkpoint")
}
func (e *checkpointRejectingExecutor) Execute(
	_ context.Context,
	req ExecutionRequest,
	_ Runtime,
) Outcome {
	e.checkpoints <- req.Checkpoint
	return Outcome{Kind: OutcomeCompleted, Result: successfulResult()}
}

func (e *cancelAwareExecutor) Key() string         { return "test" }
func (e *cancelAwareExecutor) Validate(Spec) error { return nil }
func (e *cancelAwareExecutor) ValidateResume(
	context.Context,
	*ValidateResumeRequest,
) (*ValidateResumeResult, error) {
	return nil, errors.New("unexpected resume")
}
func (e *cancelAwareExecutor) Execute(
	ctx context.Context,
	_ ExecutionRequest,
	runtime Runtime,
) Outcome {
	close(e.started)
	select {
	case control := <-runtime.Controls():
		if control.Kind == ControlStop {
			<-e.finish
			return Outcome{Kind: OutcomeCanceled, TerminalReason: "canceled"}
		}
		return Outcome{Kind: OutcomeFailed, TerminalReason: "unexpected_control"}
	case <-ctx.Done():
		return Outcome{Kind: OutcomeFailed, Err: ctx.Err()}
	}
}

func TestMemoryStoreWorkerTransitionConformance_BitsUT(t *testing.T) {
	t.Run("running transitions", func(t *testing.T) {
		for _, target := range []Status{
			StatusRunning, StatusWaitingInput, StatusSuspended, StatusCompleted, StatusFailed,
		} {
			t.Run(string(target), func(t *testing.T) {
				store := NewMemoryStore(nil)
				_, lease := createAndClaim(t, store, "task-"+string(target), time.Minute)
				result, err := store.Commit(context.Background(), &CommitTaskRequest{
					Lease: lease, Mutation: mutationForStatus(target),
				})
				require.NoError(t, err)
				assert.Equal(t, target, result.Task.Status)
				require.NotEmpty(t, result.Updates)
				assert.Equal(t, target, *result.Updates[0].Status)
				require.NotNil(t, result.Notification)
			})
		}
	})

	t.Run("running cannot commit canceled", func(t *testing.T) {
		store := NewMemoryStore(nil)
		_, lease := createAndClaim(t, store, "illegal-canceled", time.Minute)
		_, err := store.Commit(context.Background(), &CommitTaskRequest{
			Lease: lease, Mutation: mutationForStatus(StatusCanceled),
		})
		assert.ErrorIs(t, err, ErrIllegalTransition)
	})

	t.Run("canceling transitions", func(t *testing.T) {
		for _, target := range []Status{StatusCanceled, StatusCompleted, StatusFailed} {
			t.Run(string(target), func(t *testing.T) {
				store := NewMemoryStore(nil)
				task, lease := createAndClaim(t, store, "canceling-"+string(target), time.Minute)
				canceled, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
					TaskID: task.Spec.ID, ExpectedVersion: task.TransitionVersion,
				})
				require.NoError(t, err)
				lease.ExpectedVersion = canceled.Task.TransitionVersion
				result, err := store.Commit(context.Background(), &CommitTaskRequest{
					Lease: lease, Mutation: mutationForStatus(target),
				})
				require.NoError(t, err)
				assert.Equal(t, target, result.Task.Status)
			})
		}
	})
}

func TestMemoryStoreRejectsInvalidMutationShapes_BitsUT(t *testing.T) {
	tests := []struct {
		name     string
		mutation TaskMutation
	}{
		{name: "running without checkpoint", mutation: TaskMutation{ToStatus: StatusRunning}},
		{name: "failed without reason", mutation: TaskMutation{ToStatus: StatusFailed}},
		{name: "completed with reason", mutation: TaskMutation{
			ToStatus: StatusCompleted, Result: successfulResult(), TerminalReason: "unexpected",
		}},
		{name: "terminal with checkpoint", mutation: TaskMutation{
			ToStatus: StatusFailed, Checkpoint: checkpoint(1), TerminalReason: "failed",
		}},
		{name: "result on failed", mutation: TaskMutation{
			ToStatus: StatusFailed, Result: successfulResult(), TerminalReason: "failed",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore(nil)
			_, lease := createAndClaim(t, store, "shape-"+test.name, time.Minute)
			_, err := store.Commit(context.Background(), &CommitTaskRequest{
				Lease: lease, Mutation: test.mutation,
			})
			require.Error(t, err)
		})
	}
}

func TestMemoryStoreRestartFromSpecClearsCheckpointAndResume_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	spec := validSpec("restart")
	spec.Recovery.OnLeaseExpired = RecoveryRestartFromSpec
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "worker", LeaseDuration: 5 * time.Second,
	})
	require.NoError(t, err)
	reported, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: claim.Lease, Mutation: TaskMutation{ToStatus: StatusRunning, Checkpoint: checkpoint(1)},
	})
	require.NoError(t, err)
	assert.NotNil(t, reported.Task.Checkpoint)

	clock.Advance(6 * time.Second)
	recovered, err := store.Get(context.Background(), spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPending, recovered.Status)
	assert.Nil(t, recovered.Checkpoint)
	assert.Empty(t, recovered.ResumeData)
	assert.Empty(t, recovered.ResumeEncoding)
}

func TestManagerTreatsIncompatibleCheckpointAsMissing_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(150, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	spec := validSpec("incompatible-checkpoint")
	spec.Recovery.OnMissingCheckpoint = RecoveryRestartFromSpec
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "lost-worker", LeaseDuration: 5 * time.Second,
	})
	require.NoError(t, err)
	checkpointed, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: claim.Lease,
		Mutation: TaskMutation{
			ToStatus: StatusRunning, Checkpoint: checkpoint(1),
		},
	})
	require.NoError(t, err)
	assert.NotNil(t, checkpointed.Task.Checkpoint)
	clock.Advance(6 * time.Second)
	pending, err := store.Get(context.Background(), spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, pending.Status)

	executor := &checkpointRejectingExecutor{checkpoints: make(chan *CheckpointRef, 1)}
	executors := NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := New(context.Background(), &Config{
		Store: store, Executors: executors, WorkerID: "recovery-worker",
		LeaseDuration: 5 * time.Second,
	})
	defer manager.Close(context.Background())
	require.NoError(t, manager.Execute(context.Background(), spec.ID))
	assert.Nil(t, <-executor.checkpoints)
	completed, err := manager.GetTask(context.Background(), spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, completed.Status)
}

func TestMemoryStoreWaitsAreLevelTriggeredAndResolveExpiry_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(200, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	spec := validSpec("wait")
	spec.Recovery.OnLeaseExpired = RecoveryFail
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "worker", LeaseDuration: 5 * time.Second,
	})
	require.NoError(t, err)

	waited, err := store.Wait(context.Background(), &WaitTaskRequest{
		TaskID: spec.ID, AfterVersion: created.TransitionVersion,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, waited.Status)

	clock.Advance(6 * time.Second)
	updates, err := store.WaitUpdates(context.Background(), &WaitTaskUpdatesRequest{
		TaskID: spec.ID, AfterSequence: claim.Task.LatestUpdateSequence, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, updates.Updates, 1)
	assert.Equal(t, StatusFailed, *updates.Updates[0].Status)
}

func TestMemoryStoreResumeAndReleaseAreDistinct_BitsUT(t *testing.T) {
	t.Run("waiting input resumes with opaque bytes", func(t *testing.T) {
		store := NewMemoryStore(nil)
		_, lease := createAndClaim(t, store, "resume", time.Minute)
		waiting, err := store.Commit(context.Background(), &CommitTaskRequest{
			Lease: lease, Mutation: mutationForStatus(StatusWaitingInput),
		})
		require.NoError(t, err)
		resumed, err := store.Resume(context.Background(), &ResumeTaskRequest{
			TaskID: "resume", ExpectedVersion: waiting.Task.TransitionVersion,
			ResumeData: []byte("yes"), ResumeEncoding: "text/plain",
		})
		require.NoError(t, err)
		assert.Equal(t, StatusPending, resumed.Status)
		assert.Equal(t, []byte("yes"), resumed.ResumeData)
		_, err = store.ReleaseSuspension(context.Background(), &ReleaseSuspensionRequest{
			TaskID: "resume", ExpectedVersion: resumed.TransitionVersion,
		})
		assert.ErrorIs(t, err, ErrIllegalTransition)
	})

	t.Run("suspension release preserves checkpoint", func(t *testing.T) {
		store := NewMemoryStore(nil)
		_, lease := createAndClaim(t, store, "release", time.Minute)
		suspended, err := store.Commit(context.Background(), &CommitTaskRequest{
			Lease: lease, Mutation: mutationForStatus(StatusSuspended),
		})
		require.NoError(t, err)
		released, err := store.ReleaseSuspension(context.Background(), &ReleaseSuspensionRequest{
			TaskID: "release", ExpectedVersion: suspended.Task.TransitionVersion,
		})
		require.NoError(t, err)
		assert.Equal(t, StatusPending, released.Status)
		assert.Equal(t, suspended.Task.Checkpoint.Sequence, released.Checkpoint.Sequence)
		_, err = store.Resume(context.Background(), &ResumeTaskRequest{
			TaskID: "release", ExpectedVersion: released.TransitionVersion,
		})
		assert.ErrorIs(t, err, ErrIllegalTransition)
	})
}

func TestMemoryStoreStopStateConformance_BitsUT(t *testing.T) {
	t.Run("pending waiting and suspended cancel directly", func(t *testing.T) {
		for _, source := range []Status{StatusPending, StatusWaitingInput, StatusSuspended} {
			t.Run(string(source), func(t *testing.T) {
				store := NewMemoryStore(nil)
				spec := validSpec("stop-" + string(source))
				task, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
				require.NoError(t, err)
				if source != StatusPending {
					claim, claimErr := store.Claim(context.Background(), &ClaimTaskRequest{
						TaskID: spec.ID, ExpectedVersion: task.TransitionVersion,
						WorkerID: "worker", LeaseDuration: time.Minute,
					})
					require.NoError(t, claimErr)
					committed, commitErr := store.Commit(context.Background(), &CommitTaskRequest{
						Lease: claim.Lease, Mutation: mutationForStatus(source),
					})
					require.NoError(t, commitErr)
					task = committed.Task
				}
				result, cancelErr := store.RequestCancel(context.Background(), &RequestCancelRequest{
					TaskID: spec.ID, ExpectedVersion: task.TransitionVersion,
				})
				require.NoError(t, cancelErr)
				assert.Equal(t, StatusCanceled, result.Task.Status)
				require.NotNil(t, result.Notification)
				assert.Equal(t, NotificationCanceled, result.Notification.EventKind)
			})
		}
	})

	t.Run("canceling is idempotent", func(t *testing.T) {
		store := NewMemoryStore(nil)
		task, _ := createAndClaim(t, store, "stop-canceling", time.Minute)
		first, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
			TaskID: task.Spec.ID, ExpectedVersion: task.TransitionVersion,
		})
		require.NoError(t, err)
		second, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
			TaskID: task.Spec.ID, ExpectedVersion: first.Task.TransitionVersion,
		})
		require.NoError(t, err)
		assert.Equal(t, first.Task.TransitionVersion, second.Task.TransitionVersion)
		assert.Nil(t, second.Update)
		assert.Nil(t, second.Notification)
	})

	t.Run("terminal states reject stop", func(t *testing.T) {
		for _, terminal := range []Status{StatusCompleted, StatusFailed, StatusCanceled} {
			t.Run(string(terminal), func(t *testing.T) {
				store := NewMemoryStore(nil)
				task, lease := createAndClaim(t, store, "stop-"+string(terminal), time.Minute)
				if terminal == StatusCanceled {
					canceling, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
						TaskID: task.Spec.ID, ExpectedVersion: task.TransitionVersion,
					})
					require.NoError(t, err)
					lease.ExpectedVersion = canceling.Task.TransitionVersion
				}
				committed, err := store.Commit(context.Background(), &CommitTaskRequest{
					Lease: lease, Mutation: mutationForStatus(terminal),
				})
				require.NoError(t, err)
				_, err = store.RequestCancel(context.Background(), &RequestCancelRequest{
					TaskID: task.Spec.ID, ExpectedVersion: committed.Task.TransitionVersion,
				})
				assert.ErrorIs(t, err, ErrAlreadyTerminal)
			})
		}
	})
}

func TestMemoryStoreExternalArtifactValidation_BitsUT(t *testing.T) {
	content := []byte("external")
	verifier := artifactVerifierFunc(func(_ context.Context, key, digest string, size int64) error {
		if key != "task/result" || digest != digestFor(content) || size != int64(len(content)) {
			return errors.New("artifact metadata mismatch")
		}
		return nil
	})
	store := NewMemoryStore(&MemoryStoreConfig{
		ArtifactVerifiers: map[string]ArtifactVerifier{"results": verifier},
	})
	spec := validSpec("external")
	spec.Result.ResultStoreKey = "results"
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "worker", LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	lease := claim.Lease
	external := ArtifactValue{
		Ref:    &ArtifactRef{StoreKey: "results", Key: "task/result"},
		Digest: digestFor([]byte("external")), Size: int64(len("external")),
	}
	result, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Mutation: TaskMutation{
			ToStatus: StatusCompleted,
			Result:   &ResultRef{Format: "text/plain", Value: external},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, external.Ref, result.Task.ResultRef.Value.Ref)

	store = NewMemoryStore(&MemoryStoreConfig{
		ArtifactVerifiers: map[string]ArtifactVerifier{"results": verifier},
	})
	spec = validSpec("unknown-external")
	spec.Result.ResultStoreKey = "unknown"
	created, err = store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claim, err = store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "worker", LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	lease = claim.Lease
	external.Ref.StoreKey = "unknown"
	_, err = store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Mutation: TaskMutation{
			ToStatus: StatusCompleted,
			Result:   &ResultRef{Format: "text/plain", Value: external},
		},
	})
	assert.ErrorIs(t, err, ErrInvalidArtifact)

	store = NewMemoryStore(&MemoryStoreConfig{
		ArtifactVerifiers: map[string]ArtifactVerifier{"results": verifier},
	})
	spec = validSpec("mismatched-external")
	spec.Result.ResultStoreKey = "results"
	created, err = store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claim, err = store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "worker", LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	external.Ref.StoreKey = "results"
	external.Digest = digestFor([]byte("different"))
	_, err = store.Commit(context.Background(), &CommitTaskRequest{
		Lease: claim.Lease, Mutation: TaskMutation{
			ToStatus: StatusCompleted,
			Result:   &ResultRef{Format: "text/plain", Value: external},
		},
	})
	assert.ErrorIs(t, err, ErrInvalidArtifact)
}

func TestMemoryStorePreservesEmptyInlineResult_BitsUT(t *testing.T) {
	store := NewMemoryStore(nil)
	_, lease := createAndClaim(t, store, "empty-result", time.Minute)
	empty := []byte{}
	result, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease,
		Mutation: TaskMutation{
			ToStatus: StatusCompleted,
			Result: &ResultRef{
				Format: "text/plain",
				Value: ArtifactValue{
					Payload: empty, Encoding: "utf-8",
					Digest: digestFor(empty), Size: 0,
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Task.ResultRef.Value.Payload)
	assert.Empty(t, result.Task.ResultRef.Value.Payload)
	require.NotNil(t, result.Notification)
	require.NotNil(t, result.Notification.Result.Value.Payload)

	stored, err := store.Get(context.Background(), "empty-result")
	require.NoError(t, err)
	require.NotNil(t, stored.ResultRef.Value.Payload)
	assert.Nil(t, stored.ResultRef.Value.Ref)
}

func TestMemoryStoreDeadlineUsesStoreClockAndFencesWrites_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(300, 0)}
	store := NewMemoryStore(&MemoryStoreConfig{
		Clock: clock.Now, MinLeaseDuration: time.Second, MaxLeaseDuration: time.Minute,
	})
	deadline := clock.now.Add(5 * time.Second)
	spec := validSpec("deadline")
	spec.Deadline = &deadline
	created, err := store.Create(context.Background(), &CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claim, err := store.Claim(context.Background(), &ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "worker", LeaseDuration: 10 * time.Second,
	})
	require.NoError(t, err)

	clock.Advance(6 * time.Second)
	_, err = store.Renew(context.Background(), &RenewLeaseRequest{
		Lease: claim.Lease, LeaseDuration: 10 * time.Second,
	})
	assert.ErrorIs(t, err, ErrLeaseLost)
	task, err := store.Get(context.Background(), spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, task.Status)
	assert.Equal(t, "deadline_exceeded", task.TerminalReason)
	_, err = store.AppendUpdate(context.Background(), &AppendTaskUpdateRequest{
		Lease: claim.Lease, Kind: UpdateMessage,
		Payload: &UpdatePayload{Type: "text/plain", Value: validInline("late")},
	})
	require.Error(t, err)
}

func TestTaskRuntimeAdoptsOnlyAdjacentCancellation_BitsUT(t *testing.T) {
	t.Run("adjacent cancellation", func(t *testing.T) {
		store := NewMemoryStore(nil)
		task, lease := createAndClaim(t, store, "adjacent", time.Minute)
		runtime := newTaskRuntime(store, lease)
		_, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
			TaskID: task.Spec.ID, ExpectedVersion: task.TransitionVersion,
		})
		require.NoError(t, err)
		require.ErrorIs(t, runtime.renew(context.Background(), time.Minute), errLeaseRenewalStopped)
		select {
		case control := <-runtime.Controls():
			assert.Equal(t, ControlStop, control.Kind)
		default:
			t.Fatal("adjacent cancellation did not produce a stop control")
		}
		require.ErrorIs(t, runtime.renew(context.Background(), time.Minute), errLeaseRenewalStopped)
		committed, err := runtime.commit(context.Background(), mutationForStatus(StatusCanceled))
		require.NoError(t, err)
		assert.Equal(t, StatusCanceled, committed.Status)
	})

	t.Run("skipped version poisons runtime", func(t *testing.T) {
		store := NewMemoryStore(nil)
		task, lease := createAndClaim(t, store, "skipped", time.Minute)
		runtime := newTaskRuntime(store, lease)
		canceled, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
			TaskID: task.Spec.ID, ExpectedVersion: task.TransitionVersion,
		})
		require.NoError(t, err)
		newLease := lease
		newLease.ExpectedVersion = canceled.Task.TransitionVersion
		_, err = store.Commit(context.Background(), &CommitTaskRequest{
			Lease: newLease, Mutation: mutationForStatus(StatusFailed),
		})
		require.NoError(t, err)
		assert.ErrorIs(t, runtime.renew(context.Background(), time.Minute), ErrLeaseLost)
		assert.ErrorIs(t, runtime.ReportUpdate(context.Background(), &ReportUpdateRequest{
			Kind: UpdateMessage, Payload: &UpdatePayload{Type: "text/plain", Value: validInline("late")},
		}), ErrLeaseLost)
	})
}

func TestTaskRuntimeStopOverridesQueuedDrain_BitsUT(t *testing.T) {
	runtime := newTaskRuntime(NewMemoryStore(nil), LeaseToken{})
	runtime.requestControl(ControlDrain)
	runtime.requestControl(ControlStop)
	select {
	case control := <-runtime.Controls():
		assert.Equal(t, ControlStop, control.Kind)
	default:
		t.Fatal("terminal stop was not queued")
	}
}

func TestManagerSerializesCancellationWithRuntime_BitsUT(t *testing.T) {
	executor := &cancelAwareExecutor{started: make(chan struct{}), finish: make(chan struct{})}
	executors := NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := New(context.Background(), &Config{
		Executors: executors, WorkerID: "manager-worker", LeaseDuration: time.Second,
	})
	defer manager.Close(context.Background())

	spec := validSpec("manager-cancel")
	task, err := manager.Submit(context.Background(), spec)
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}

	running, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, "manager-worker", running.LeaseOwner)
	canceling, err := manager.RequestCancel(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCanceling, canceling.Status)
	repeated, err := manager.RequestCancel(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, canceling.TransitionVersion, repeated.TransitionVersion)
	close(executor.finish)

	select {
	case err = <-executeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("executor did not acknowledge cancellation")
	}
	terminal, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, terminal.Status)
}

func TestManagerRejectsSubmitAndExecuteAfterClose_BitsUT(t *testing.T) {
	manager := New(context.Background(), nil)
	require.NoError(t, manager.Close(context.Background()))
	_, err := manager.Submit(context.Background(), validSpec("closed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shut down")
	err = manager.Execute(context.Background(), "closed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shut down")
}

func TestMemoryStoreResumePayloadIsBounded_BitsUT(t *testing.T) {
	store := NewMemoryStore(&MemoryStoreConfig{MaxUpdatePayload: 16})
	_, lease := createAndClaim(t, store, "bounded-resume", time.Minute)
	waiting, err := store.Commit(context.Background(), &CommitTaskRequest{
		Lease: lease, Mutation: mutationForStatus(StatusWaitingInput),
	})
	require.NoError(t, err)
	_, err = store.Resume(context.Background(), &ResumeTaskRequest{
		TaskID: "bounded-resume", ExpectedVersion: waiting.Task.TransitionVersion,
		ResumeData: []byte("12345678901234567"), ResumeEncoding: "text/plain",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidArtifact) || strings.Contains(err.Error(), "exceeds"))
}

func TestRuntimeUpdateQuotaFailureCannotBeHiddenByCompletion_BitsUT(t *testing.T) {
	store := NewMemoryStore(&MemoryStoreConfig{MaxReportedUpdates: 1})
	_, lease := createAndClaim(t, store, "update-quota", time.Minute)
	runtime := newTaskRuntime(store, lease)
	update := &ReportUpdateRequest{
		Kind: UpdateMessage,
		Payload: &UpdatePayload{
			Type: "text/plain", Value: validInline("message"),
		},
	}
	require.NoError(t, runtime.ReportUpdate(context.Background(), update))
	err := runtime.ReportUpdate(context.Background(), update)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota")

	_, commitErr := runtime.commit(context.Background(), TaskMutation{
		ToStatus: StatusCompleted, Result: successfulResult(),
	})
	assert.ErrorIs(t, commitErr, err)
	task, getErr := store.Get(context.Background(), "update-quota")
	require.NoError(t, getErr)
	assert.Equal(t, StatusRunning, task.Status)
	assert.Nil(t, task.ResultRef)
}
