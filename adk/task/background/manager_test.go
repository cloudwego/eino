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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/internal/taskcontrol"
	taskcore "github.com/cloudwego/eino/adk/task"
)

func closeWithTimeout(manager *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = manager.Close(ctx)
}

type managerContextSnapshotKey struct{}

type testContextSnapshotter struct{}

func (testContextSnapshotter) CaptureContext(ctx context.Context) ([]byte, error) {
	value, _ := ctx.Value(managerContextSnapshotKey{}).(string)
	if value == "" {
		return nil, nil
	}
	return []byte(value), nil
}

func (testContextSnapshotter) RestoreContext(ctx context.Context, snapshot []byte) (context.Context, error) {
	return context.WithValue(ctx, managerContextSnapshotKey{}, string(snapshot)), nil
}

type failingRestoreSnapshotter struct {
	err error
}

type cancellationAcknowledgingExecutor struct {
	*scriptedExecutor
	acknowledge func(context.Context, *TaskSnapshot, string) error
}

func (e *cancellationAcknowledgingExecutor) AcknowledgeCancellation(
	ctx context.Context,
	task *TaskSnapshot,
	reason string,
) error {
	return e.acknowledge(ctx, task, reason)
}

func (f failingRestoreSnapshotter) CaptureContext(context.Context) ([]byte, error) {
	return nil, nil
}

func (f failingRestoreSnapshotter) RestoreContext(context.Context, []byte) (context.Context, error) {
	return nil, f.err
}

type firstReleaseConflictStore struct {
	LifecycleStore
	once sync.Once
}

type publishConflictStore struct {
	LifecycleStore
	conflicts  int
	calls      int
	onConflict func()
}

func (s *publishConflictStore) Publish(
	ctx context.Context,
	req *PublishTaskRequest,
) (*TaskSnapshot, error) {
	s.calls++
	if s.calls <= s.conflicts {
		if s.onConflict != nil {
			s.onConflict()
		}
		return nil, ErrVersionConflict
	}
	return s.LifecycleStore.Publish(ctx, req)
}

func (s *firstReleaseConflictStore) ReleaseSuspension(
	ctx context.Context,
	req *ReleaseSuspensionRequest,
) (*TaskSnapshot, error) {
	conflict := false
	s.once.Do(func() { conflict = true })
	if conflict {
		return nil, ErrVersionConflict
	}
	return s.LifecycleStore.ReleaseSuspension(ctx, req)
}

func TestManagerListTaskEventsDelegatesToStore_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: lifecycleStoreOnly{LifecycleStore: store}, TaskEvents: store,
	})
	defer closeWithTimeout(manager)
	started := createAndStart(t, store, "manager-output")
	_, err := store.AppendTaskEvent(context.Background(), &AppendTaskEventRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt,
		EventID: "record", Data: []byte("record"),
	})
	require.NoError(t, err)

	result, err := manager.ListTaskEvents(context.Background(), &ListTaskEventsRequest{
		TaskID: started.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, result.Parts, 1)
	require.Equal(t, "record", string(result.Parts[0].Data))
}

func TestManagerExecuteBindsTimeoutController_BitsUT(t *testing.T) {
	observed := make(chan ControlRequest, 1)
	executor := &scriptedExecutor{
		execute: func(
			_ context.Context,
			_ *TaskSnapshot,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{
				Action: ExecutionActionFail,
				Error:  control.Reason,
			}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("timeout")})
	require.NoError(t, err)

	executeCtx, timeoutController := taskcontrol.WithTimeoutController(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Execute(executeCtx, task.Spec.ID)
	}()

	err = timeoutController.RequestTimeout(context.Background(), "")
	require.EqualError(t, err, "taskcontrol: timeout reason is required")
	require.NoError(t, timeoutController.RequestTimeout(
		context.Background(), "timed out after 10ms",
	))
	require.NoError(t, <-done)
	control := <-observed
	require.Equal(t, ControlTimeout, control.Kind)
	require.Equal(t, "timed out after 10ms", control.Reason)

	failed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, failed.Status)
	require.Equal(t, "timed out after 10ms", failed.ResultError)
	require.Nil(t, failed.CancelRequestedAt)
	require.ErrorIs(
		t,
		timeoutController.RequestTimeout(context.Background(), "too late"),
		taskcontrol.ErrClosed,
	)
}

func TestManagerExecuteClosesTimeoutControllerOnEarlyFailure_BitsUT(t *testing.T) {
	manager := mustNewManager(t, context.Background(), nil)
	defer closeWithTimeout(manager)
	executeCtx, timeoutController := taskcontrol.WithTimeoutController(context.Background())

	err := manager.Execute(executeCtx, "")
	require.EqualError(t, err, "task/background: execute task id is required")
	require.ErrorIs(
		t,
		timeoutController.RequestTimeout(context.Background(), "too late"),
		taskcontrol.ErrClosed,
	)
}

func TestManagerCloseDrainsActiveAttempt(t *testing.T) {
	started := make(chan struct{})
	observed := make(chan ControlRequest, 1)
	executor := &scriptedExecutor{
		execute: func(
			_ context.Context,
			_ *TaskSnapshot,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(started)
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{
				Action: ExecutionActionSuspend, Checkpoint: []byte("checkpoint"),
			}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("close-drain")})
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	<-started

	require.ErrorIs(t, manager.Close(context.Background()), ErrCloseDeadlineRequired)
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Close(closeCtx, WithDrainReason("worker maintenance")))
	require.NoError(t, <-executeDone)
	require.Equal(t, ControlRequest{
		Kind: ControlDrain, Reason: "worker maintenance",
	}, <-observed)
	suspended, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSuspended, suspended.Status)
	require.Equal(t, "checkpoint", string(suspended.Checkpoint))

	discovered, err := manager.ListSuspended(
		context.Background(),
		&ListSuspendedRequest{ExecutorKeys: []string{"test"}},
	)
	require.NoError(t, err)
	require.Len(t, discovered.Tasks, 1)
	require.Equal(t, task.Spec.ID, discovered.Tasks[0].Spec.ID)

	released, err := manager.ReleaseSuspension(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, released.Status)
	require.Equal(t, "checkpoint", string(released.Checkpoint))

	pending, err := manager.ListPending(
		context.Background(),
		&ListPendingRequest{ExecutorKeys: []string{"test"}},
	)
	require.NoError(t, err)
	require.Len(t, pending.Tasks, 1)
	require.Equal(t, task.Spec.ID, pending.Tasks[0].Spec.ID)
}

func TestManagerCloseDrainsAttemptInitializedDuringClose(t *testing.T) {
	baseStore := NewInMemoryStore(nil)
	store := &firstGetBlockingStore{
		LifecycleStore: baseStore, TaskEventStore: baseStore,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	observed := make(chan ControlRequest, 1)
	executor := &scriptedExecutor{
		execute: func(
			_ context.Context,
			_ *TaskSnapshot,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{
				Action: ExecutionActionSuspend, Checkpoint: []byte("checkpoint"),
			}, nil
		},
	}
	manager := managerWithExecutor(t, store, executor, time.Minute)
	spec := validSpec("close-initializing")
	spec.RootSessionID = ""
	spec.NotifySession = false
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	<-store.entered

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- manager.Close(closeCtx, WithDrainReason("worker maintenance"))
	}()
	require.Eventually(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return manager.closed
	}, time.Second, time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	close(store.release)

	require.NoError(t, <-closeDone)
	require.NoError(t, <-executeDone)
	require.Equal(t, ControlRequest{
		Kind: ControlDrain, Reason: "worker maintenance",
	}, <-observed)
	suspended, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSuspended, suspended.Status)
	require.Equal(t, "checkpoint", string(suspended.Checkpoint))
}

func TestManagerReleaseSuspensionRetriesVersionConflict_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "release-retry")
	suspended, err := store.SuspendIfNoInputs(
		context.Background(),
		&SuspendIfNoInputsRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
			Attempt: started.Attempt, InputCursor: 0,
			Checkpoint: []byte("checkpoint"),
		},
	)
	require.NoError(t, err)

	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: &firstReleaseConflictStore{LifecycleStore: store}, TaskEvents: store,
	})
	defer closeWithTimeout(manager)
	released, err := manager.ReleaseSuspension(context.Background(), suspended.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, released.Status)
	require.Equal(t, "checkpoint", string(released.Checkpoint))
}

func TestManagerPublishHandlesVersionConflicts(t *testing.T) {
	newDeferredTask := func(
		t *testing.T,
		store *InMemoryStore,
		id string,
	) *TaskSnapshot {
		t.Helper()
		created, err := store.Create(
			context.Background(),
			&CreateTaskRequest{
				Spec:              validSpec(id),
				Publication:       PublicationDeferred,
				LeaseExpiryPolicy: LeaseExpiryRetry,
			},
		)
		require.NoError(t, err)
		return created
	}

	t.Run("requires task id", func(t *testing.T) {
		manager := mustNewManager(t, context.Background(), nil)
		defer closeWithTimeout(manager)

		published, err := manager.Publish(context.Background(), "")
		require.Nil(t, published)
		require.EqualError(t, err, "task/background: publish task id is required")
	})

	t.Run("retries conflict", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		created := newDeferredTask(t, store, "publish-retry")
		conflicts := &publishConflictStore{
			LifecycleStore: store,
			conflicts:      1,
		}
		manager := mustNewManager(t, context.Background(), &Config{
			Tasks: conflicts, TaskEvents: store,
		})
		defer closeWithTimeout(manager)

		published, err := manager.Publish(context.Background(), created.Spec.ID)
		require.NoError(t, err)
		require.Equal(t, 2, conflicts.calls)
		require.Equal(t, PublicationOnBackground, published.Publication)
	})

	t.Run("returns the eighth conflict", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		created := newDeferredTask(t, store, "publish-exhaust")
		conflicts := &publishConflictStore{
			LifecycleStore: store,
			conflicts:      8,
		}
		manager := mustNewManager(t, context.Background(), &Config{
			Tasks: conflicts, TaskEvents: store,
		})
		defer closeWithTimeout(manager)

		published, err := manager.Publish(context.Background(), created.Spec.ID)
		require.Nil(t, published)
		require.ErrorIs(t, err, ErrVersionConflict)
		require.Equal(t, 8, conflicts.calls)
		current, getErr := store.Get(context.Background(), created.Spec.ID)
		require.NoError(t, getErr)
		require.Equal(t, PublicationDeferred, current.Publication)
	})

	t.Run("stops retrying when context is canceled", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		created := newDeferredTask(t, store, "publish-context")
		ctx, cancel := context.WithCancel(context.Background())
		conflicts := &publishConflictStore{
			LifecycleStore: store,
			conflicts:      8,
			onConflict:     cancel,
		}
		manager := mustNewManager(t, context.Background(), &Config{
			Tasks: conflicts, TaskEvents: store,
		})
		defer closeWithTimeout(manager)

		published, err := manager.Publish(ctx, created.Spec.ID)
		require.Nil(t, published)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, conflicts.calls)
		current, getErr := store.Get(context.Background(), created.Spec.ID)
		require.NoError(t, getErr)
		require.Equal(t, PublicationDeferred, current.Publication)
	})
}

func TestManagerReleaseSuspensionRejectsInvalidTargets_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, &scriptedExecutor{}, time.Minute)
	defer closeWithTimeout(manager)

	released, err := manager.ReleaseSuspension(context.Background(), "")
	require.Nil(t, released)
	require.EqualError(t, err, "task/background: release suspension task id is required")

	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("not-suspended")})
	require.NoError(t, err)
	released, err = manager.ReleaseSuspension(context.Background(), task.Spec.ID)
	require.Nil(t, released)
	require.ErrorIs(t, err, ErrIllegalTransition)
}

func TestManagerExecutorPanicFailsTask_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(context.Context, *TaskSnapshot, ExecutionRuntime) (*ExecutionResult, error) {
			panic("executor panic")
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("executor-panic")})
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	failed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, failed.Status)
	require.Contains(t, failed.ResultError, "executor panic")
}

func TestManagerCloseCancelsNonDrainableAttemptAtDeadline(t *testing.T) {
	started := make(chan struct{})
	observed := make(chan ControlKind, 1)
	executor := &scriptedExecutor{
		disableDrain: true,
		execute: func(
			_ context.Context,
			_ *TaskSnapshot,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(started)
			control := <-runtime.Controls()
			observed <- control.Kind
			return &ExecutionResult{Action: ExecutionActionCancel}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("close-cancel")})
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	<-started

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	require.NoError(t, manager.Close(closeCtx))
	require.NoError(t, <-executeDone)
	require.Equal(t, ControlStop, <-observed)
	canceled, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, canceled.Status)
}

func TestManagerCancelNonRecoverableAttempt(t *testing.T) {
	started := make(chan struct{})
	observed := make(chan ControlRequest, 1)
	executor := &scriptedExecutor{
		leaseExpiryPolicy: LeaseExpiryFail,
		execute: func(
			_ context.Context,
			_ *TaskSnapshot,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(started)
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{Action: ExecutionActionCancel}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("local-cancel")})
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	<-started

	canceled, err := manager.RequestCancel(
		context.Background(), task.Spec.ID,
		WithCancellationReason("stopped by operator"),
	)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, canceled.Status)
	require.Equal(t, "stopped by operator", canceled.CancelReason)
	require.Equal(t, "stopped by operator", canceled.ResultError)
	require.NoError(t, <-executeDone)
	require.Equal(t, ControlRequest{
		Kind: ControlStop, Reason: "stopped by operator",
	}, <-observed)
}

func TestManagerAcknowledgesActiveCancellationOnceAndRetriesFailures(t *testing.T) {
	for _, test := range []struct {
		name             string
		taskID           string
		failFirst        bool
		requests         int
		wantCalls        int64
		wantFirstFailure bool
	}{
		{
			name:     "repeated request after success",
			taskID:   "acknowledge-success",
			requests: 2, wantCalls: 1,
		},
		{
			name:      "failed acknowledgement is retried",
			taskID:    "acknowledge-retry",
			failFirst: true, requests: 2, wantCalls: 2, wantFirstFailure: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			controlReceived := make(chan ControlRequest, 1)
			finish := make(chan struct{})
			var acknowledgeCalls int64
			ackErr := errors.New("acknowledgement failed")
			executor := &cancellationAcknowledgingExecutor{
				scriptedExecutor: &scriptedExecutor{execute: func(
					_ context.Context,
					_ *TaskSnapshot,
					runtime ExecutionRuntime,
				) (*ExecutionResult, error) {
					close(started)
					controlReceived <- <-runtime.Controls()
					<-finish
					return &ExecutionResult{Action: ExecutionActionCancel}, nil
				}},
				acknowledge: func(
					context.Context,
					*TaskSnapshot,
					string,
				) error {
					call := atomic.AddInt64(&acknowledgeCalls, 1)
					if test.failFirst && call == 1 {
						return ackErr
					}
					return nil
				},
			}
			store := NewInMemoryStore(nil)
			manager := managerWithExecutor(t, store, executor, time.Minute)
			defer closeWithTimeout(manager)
			task, err := manager.Submit(
				context.Background(),
				&SubmitRequest{Spec: validSpec(test.taskID)},
			)
			require.NoError(t, err)
			executeDone := make(chan error, 1)
			go func() {
				executeDone <- manager.Execute(context.Background(), task.Spec.ID)
			}()
			<-started

			for request := 0; request < test.requests; request++ {
				_, err = manager.RequestCancel(context.Background(), task.Spec.ID)
				if request == 0 && test.wantFirstFailure {
					require.ErrorIs(t, err, ackErr)
				} else {
					require.NoError(t, err)
				}
			}
			require.Equal(t, test.wantCalls, atomic.LoadInt64(&acknowledgeCalls))
			require.Equal(t, ControlStop, (<-controlReceived).Kind)
			close(finish)
			require.NoError(t, <-executeDone)
			require.Equal(t, test.wantCalls, atomic.LoadInt64(&acknowledgeCalls))
		})
	}
}

func TestManagerCancellationAcknowledgerPanicKeepsDurableIntent(t *testing.T) {
	started := make(chan struct{})
	observedControl := make(chan ControlRequest, 1)
	var acknowledgeCalls int64
	executor := &cancellationAcknowledgingExecutor{
		scriptedExecutor: &scriptedExecutor{execute: func(
			_ context.Context,
			_ *TaskSnapshot,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(started)
			select {
			case control := <-runtime.Controls():
				observedControl <- control
			case <-time.After(time.Second):
				return nil, errors.New("timed out waiting for cancellation control")
			}
			return &ExecutionResult{Action: ExecutionActionCancel}, nil
		}},
		acknowledge: func(context.Context, *TaskSnapshot, string) error {
			if atomic.AddInt64(&acknowledgeCalls, 1) == 1 {
				panic("acknowledger panic")
			}
			return nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	submitted, err := manager.Submit(
		context.Background(),
		&SubmitRequest{Spec: validSpec("cancel-hook-panic")},
	)
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), submitted.Spec.ID)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task execution to start")
	}

	requested, err := manager.RequestCancel(
		context.Background(),
		submitted.Spec.ID,
		WithCancellationReason("operator stop"),
	)
	require.ErrorContains(t, err, "acknowledger panic")
	require.NotNil(t, requested)
	require.Equal(t, StatusRunning, requested.Status)
	require.NotNil(t, requested.CancelRequestedAt)
	require.Equal(t, "operator stop", requested.CancelReason)
	stored, getErr := manager.Get(context.Background(), submitted.Spec.ID)
	require.NoError(t, getErr)
	require.NotNil(t, stored.CancelRequestedAt)
	require.Equal(t, "operator stop", stored.CancelReason)
	select {
	case control := <-observedControl:
		t.Fatalf("stop control arrived before cancellation hook succeeded: %+v", control)
	default:
	}

	retried, err := manager.RequestCancel(context.Background(), submitted.Spec.ID)
	require.NoError(t, err)
	require.NotNil(t, retried.CancelRequestedAt)
	require.Equal(t, int64(2), atomic.LoadInt64(&acknowledgeCalls))
	var control ControlRequest
	select {
	case control = <-observedControl:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancellation control")
	}
	require.Equal(t, ControlRequest{
		Kind: ControlStop, Reason: "operator stop",
	}, control)
	select {
	case executeErr := <-executeDone:
		require.NoError(t, executeErr)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task execution to finish")
	}
	terminal, err := manager.Get(context.Background(), submitted.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, terminal.Status)
	require.Equal(t, "operator stop", terminal.ResultError)
}

func TestManagerReplaysCancellationReasonToRecoveryAttempt(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := newInMemoryStoreWithClock(
		&InMemoryStoreConfig{ActiveAttemptTimeout: time.Second},
		clock.Now,
	)
	started := createAndStart(t, store, "recover-cancel-reason")
	_, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		Reason: "operator request",
	})
	require.NoError(t, err)
	clock.Advance(2 * time.Second)
	pending, err := store.Get(context.Background(), started.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, pending.Status)

	observed := make(chan ControlRequest, 1)
	var acknowledgeCalls int64
	executor := &cancellationAcknowledgingExecutor{
		scriptedExecutor: &scriptedExecutor{execute: func(
			_ context.Context,
			_ *TaskSnapshot,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{Action: ExecutionActionCancel}, nil
		}},
		acknowledge: func(
			context.Context,
			*TaskSnapshot,
			string,
		) error {
			atomic.AddInt64(&acknowledgeCalls, 1)
			return nil
		},
	}
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	require.NoError(t, manager.Execute(context.Background(), started.Spec.ID))
	require.Equal(t, ControlRequest{
		Kind: ControlStop, Reason: "operator request",
	}, <-observed)
	canceled, err := manager.Get(context.Background(), started.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, "operator request", canceled.ResultError)
	require.Equal(t, int64(1), atomic.LoadInt64(&acknowledgeCalls))
}

func TestManagerPreservesContextSnapshotAcrossMailboxWake(t *testing.T) {
	store := NewInMemoryStore(nil)
	observed := make(chan string, 2)
	attempt := 0
	executor := &scriptedExecutor{execute: func(
		ctx context.Context,
		_ *TaskSnapshot,
		runtime ExecutionRuntime,
	) (*ExecutionResult, error) {
		attempt++
		value, _ := ctx.Value(managerContextSnapshotKey{}).(string)
		observed <- value
		if attempt == 1 {
			return &ExecutionResult{
				Action:     ExecutionActionWaitInput,
				Checkpoint: []byte("checkpoint"),
			}, nil
		}
		inputs, err := runtime.ListInputs(ctx, 0, 10)
		if err != nil {
			return nil, err
		}
		if err = runtime.AdvanceInputCursor(
			ctx,
			inputs.ConsumedCursor,
			inputs.LatestSequence,
		); err != nil {
			return nil, err
		}
		return &ExecutionResult{
			Action: ExecutionActionComplete, Data: []byte("done"),
			InputCursor: inputs.LatestSequence,
		}, nil
	}}
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks:              store,
		ContextSnapshotter: testContextSnapshotter{},
	})
	_, _, err := manager.LoadOrRegisterExecutor(executor)
	require.NoError(t, err)
	defer closeWithTimeout(manager)

	submitCtx := context.WithValue(
		context.Background(), managerContextSnapshotKey{}, "submit-trace",
	)
	task, err := manager.Submit(submitCtx, &SubmitRequest{
		Spec: validSpec("ctx-snapshot"),
	})
	require.NoError(t, err)
	require.Equal(t, "submit-trace", string(task.ContextSnapshot))
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	require.Equal(t, "submit-trace", <-observed)

	waiting, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusWaitingInput, waiting.Status)
	_, err = manager.SendInput(context.Background(), &taskcore.SendInputRequest{
		TaskID: task.Spec.ID,
		Input: taskcore.Input{
			EventID: "approval", Kind: "approval",
			Data: []byte(`{"approval":true}`),
		},
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	require.Equal(t, "submit-trace", <-observed)

	completed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, completed.Status)
}

func TestManagerExecuteRequiresContextSnapshotterForPersistedSnapshot(t *testing.T) {
	store := NewInMemoryStore(nil)
	created, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: validSpec("missing-restorer"), LeaseExpiryPolicy: LeaseExpiryRetry,
		ContextSnapshot: []byte("trace"),
	})
	require.NoError(t, err)
	manager := managerWithExecutor(t, store, &scriptedExecutor{}, time.Minute)
	defer closeWithTimeout(manager)

	err = manager.Execute(context.Background(), created.Spec.ID)
	require.EqualError(
		t,
		err,
		"task/background: context snapshotter is required to restore task context",
	)
	current, getErr := manager.Get(context.Background(), created.Spec.ID)
	require.NoError(t, getErr)
	require.Equal(t, StatusPending, current.Status)
}

func TestAttack_ContextSnapshotRestoreFailureDoesNotStartTask(t *testing.T) {
	restoreErr := errors.New("restore failed")
	store := NewInMemoryStore(nil)
	created, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: validSpec("restore-failure"), LeaseExpiryPolicy: LeaseExpiryRetry,
		ContextSnapshot: []byte("trace"),
	})
	require.NoError(t, err)
	executeCalled := false
	executor := &scriptedExecutor{execute: func(
		context.Context,
		*TaskSnapshot,
		ExecutionRuntime,
	) (*ExecutionResult, error) {
		executeCalled = true
		return &ExecutionResult{
			Action: ExecutionActionComplete,
			Data:   []byte("unexpected"),
		}, nil
	}}
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks:              store,
		ContextSnapshotter: failingRestoreSnapshotter{err: restoreErr},
	})
	_, _, err = manager.LoadOrRegisterExecutor(executor)
	require.NoError(t, err)
	defer closeWithTimeout(manager)

	err = manager.Execute(context.Background(), created.Spec.ID)
	require.ErrorIs(t, err, restoreErr)
	require.False(t, executeCalled)
	current, getErr := manager.Get(context.Background(), created.Spec.ID)
	require.NoError(t, getErr)
	require.Equal(t, StatusPending, current.Status)
	require.Equal(t, int64(0), current.Attempt)
}

func TestManagerDispatchReadBoundaries(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, &scriptedExecutor{}, time.Minute)
	defer closeWithTimeout(manager)
	submitted, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("dispatch")})
	require.NoError(t, err)
	pending, err := manager.ListPending(context.Background(), &ListPendingRequest{
		ExecutorKeys: []string{"test"},
	})
	require.NoError(t, err)
	require.Len(t, pending.Tasks, 1)
	require.Equal(t, submitted.Spec.ID, pending.Tasks[0].Spec.ID)

	started, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: submitted.Spec.ID, ExpectedVersion: submitted.Version,
	})
	require.NoError(t, err)
	updated, err := manager.WaitForTaskVersion(context.Background(), &WaitForTaskVersionRequest{
		TaskID: submitted.Spec.ID, AfterVersion: submitted.Version,
	})
	require.NoError(t, err)
	require.Equal(t, started.Version, updated.Version)
}

func TestTimeoutControlPrecedence(t *testing.T) {
	t.Run("timeout always has a reason", func(t *testing.T) {
		runtime := newTaskRuntime(nil, nil, "task", 1, 1, nil)
		require.True(t, runtime.requestControlWithReason(ControlTimeout, ""))
		require.Equal(t, ControlRequest{
			Kind: ControlTimeout, Reason: defaultTimeoutReason,
		}, <-runtime.Controls())
	})

	t.Run("timeout supersedes drain", func(t *testing.T) {
		runtime := newTaskRuntime(nil, nil, "task", 1, 1, nil)
		require.True(t, runtime.requestControl(ControlDrain))
		_, controller := taskcontrol.WithTimeoutController(context.Background())
		stop := make(chan struct{})
		done := make(chan struct{})
		go serveTimeoutRequests(runtime, controller, stop, done)

		require.NoError(t, controller.RequestTimeout(context.Background(), "deadline"))
		require.Equal(t, ControlRequest{
			Kind: ControlTimeout, Reason: "deadline",
		}, <-runtime.Controls())
		close(stop)
		<-done
	})

	t.Run("stop rejects timeout", func(t *testing.T) {
		runtime := newTaskRuntime(nil, nil, "task", 1, 1, nil)
		require.True(t, runtime.requestControl(ControlStop))
		_, controller := taskcontrol.WithTimeoutController(context.Background())
		stop := make(chan struct{})
		done := make(chan struct{})
		go serveTimeoutRequests(runtime, controller, stop, done)

		require.ErrorIs(
			t,
			controller.RequestTimeout(context.Background(), "deadline"),
			taskcontrol.ErrClosed,
		)
		require.Equal(t, ControlStop, (<-runtime.Controls()).Kind)
		close(stop)
		<-done
	})
}

func TestTaskRuntimeTranscriptFailureAndHeartbeat(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "runtime")
	runtime := newTaskRuntime(
		store, store, started.Spec.ID, started.Attempt, started.Version, nil,
	)

	outputScope, outputWriter := runtime.NewTaskEventWriter("")
	output, err := outputWriter.Append(
		context.Background(),
		&TaskEventPartInput{PartID: "event", Data: []byte("output"), Final: true},
	)
	require.NoError(t, err)
	require.NotEmpty(t, outputScope.EventID)
	require.True(t, output.Inserted)
	suppliedScope, suppliedWriter := runtime.NewTaskEventWriter("caller-event")
	supplied, err := suppliedWriter.Append(
		context.Background(),
		&TaskEventPartInput{PartID: "event", Data: []byte("supplied"), Final: true},
	)
	require.NoError(t, err)
	require.Equal(t, "caller-event", suppliedScope.EventID)
	require.True(t, supplied.Inserted)
	require.NoError(t, runtime.ReportTranscriptFailure(
		context.Background(), errors.New("file failed"),
	))
	require.NoError(t, runtime.heartbeat(context.Background()))

	current, err := store.Get(context.Background(), started.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, "file failed", current.OutputFileErr)
	require.Equal(t, started.Version+2, current.Version)
}

func TestTaskRuntimeReconcilesCancellationOnHeartbeat(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "heartbeat-cancel")
	runtime := newTaskRuntime(
		store, store, started.Spec.ID, started.Attempt, started.Version, nil,
	)
	_, err := store.RequestCancel(context.Background(), &RequestCancelRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	require.ErrorIs(t, runtime.heartbeat(context.Background()), errHeartbeatStopped)
	require.True(t, runtime.cancelRequested)
	require.Equal(t, ControlStop, (<-runtime.Controls()).Kind)

	runtime.poison = ErrLeaseLost
	require.ErrorIs(t, runtime.heartbeat(context.Background()), ErrLeaseLost)
}

type heartbeatErrorStore struct {
	LifecycleStore
	err error
}

func (s heartbeatErrorStore) Heartbeat(context.Context, *HeartbeatRequest) (*TaskSnapshot, error) {
	return nil, s.err
}

type cursorReadErrorStore struct {
	LifecycleStore
	err                error
	getMailboxCalls    int
	advanceCursorCalls int
}

func (s *cursorReadErrorStore) GetMailbox(
	context.Context,
	string,
) (*taskcore.Mailbox, error) {
	s.getMailboxCalls++
	return nil, s.err
}

func (s *cursorReadErrorStore) AdvanceCursor(
	context.Context,
	*taskcore.AdvanceCursorRequest,
) error {
	s.advanceCursorCalls++
	return nil
}

func TestTaskRuntimeAdvanceInputCursorErrors(t *testing.T) {
	getMailboxErr := errors.New("mailbox unavailable")
	store := &cursorReadErrorStore{
		LifecycleStore: NewInMemoryStore(nil),
		err:            getMailboxErr,
	}
	tests := []struct {
		name    string
		runtime *taskRuntime
		wantErr error
	}{
		{
			name:    "requires mailbox store",
			runtime: newTaskRuntime(nil, nil, "task", 1, 1, nil),
			wantErr: taskcore.ErrMailboxStoreRequired,
		},
		{
			name: "propagates mailbox read error",
			runtime: newTaskRuntime(
				store, nil, "task", 1, 1, nil,
			),
			wantErr: getMailboxErr,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.runtime.AdvanceInputCursor(
				context.Background(), 2, 3,
			)
			require.ErrorIs(t, err, testCase.wantErr)
		})
	}
	require.Equal(t, 1, store.getMailboxCalls)
	require.Zero(t, store.advanceCursorCalls)
}

func TestManagerHeartbeatStopsAndCancelsOnLeaseError(t *testing.T) {
	manager := mustNewManager(t, context.Background(), nil)
	manager.heartbeatEvery = time.Nanosecond
	events := NewInMemoryStore(nil)
	runtime := newTaskRuntime(
		heartbeatErrorStore{LifecycleStore: NewInMemoryStore(nil), err: ErrLeaseLost},
		events,
		"task", 1, 1,
		nil,
	)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	stop := make(chan struct{})
	go manager.heartbeat(runCtx, cancel, runtime, stop, done)
	<-done
	require.ErrorIs(t, runCtx.Err(), context.Canceled)

	runCtx, cancel = context.WithCancel(context.Background())
	defer cancel()
	runtime = newTaskRuntime(NewInMemoryStore(nil), events, "task", 1, 1, nil)
	runtime.cancelRequested = true
	done = make(chan struct{})
	go manager.heartbeat(runCtx, cancel, runtime, stop, done)
	<-done
	require.NoError(t, runCtx.Err())
}

func TestDetachedExecutionContext(t *testing.T) {
	type key struct{}
	ctx := detachedCtx{parent: context.WithValue(context.Background(), key{}, "value")}
	_, hasDeadline := ctx.Deadline()
	require.False(t, hasDeadline)
	require.Nil(t, ctx.Done())
	require.NoError(t, ctx.Err())
	require.Equal(t, "value", ctx.Value(key{}))
}

func TestManagerExecutorRegistrationBoundaries(t *testing.T) {
	manager := mustNewManager(t, context.Background(), nil)
	_, _, err := manager.LoadOrRegisterExecutor(nil)
	require.Error(t, err)
	executor := &scriptedExecutor{}
	actual, loaded, err := manager.LoadOrRegisterExecutor(executor)
	require.NoError(t, err)
	require.False(t, loaded)
	require.Same(t, executor, actual)
	actual, loaded, err = manager.LoadOrRegisterExecutor(executor)
	require.NoError(t, err)
	require.True(t, loaded)
	require.Same(t, executor, actual)

	var nilManager *Manager
	_, _, err = nilManager.LoadOrRegisterExecutor(executor)
	require.Error(t, err)
}

func TestRuntimeCancellationReconciliationRejectsInvalidState(t *testing.T) {
	store := NewInMemoryStore(nil)
	runtime := newTaskRuntime(store, store, "missing", 1, 1, nil)
	require.ErrorIs(
		t, runtime.reconcileCancellationLocked(context.Background()), ErrNotFound,
	)
	require.ErrorIs(t, runtime.poison, ErrNotFound)

	started := createAndStart(t, store, "not-canceled")
	runtime = newTaskRuntime(
		store, store, started.Spec.ID, started.Attempt, started.Version, nil,
	)
	require.ErrorIs(
		t, runtime.reconcileCancellationLocked(context.Background()), ErrLeaseLost,
	)
	require.ErrorIs(t, runtime.poison, ErrLeaseLost)
}

type firstGetBlockingStore struct {
	LifecycleStore
	TaskEventStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *firstGetBlockingStore) Get(ctx context.Context, taskID string) (*TaskSnapshot, error) {
	block := false
	s.once.Do(func() {
		block = true
		close(s.entered)
	})
	if block {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.LifecycleStore.Get(ctx, taskID)
}

func TestManagerCancelPendingLocalTaskAfterExecuteValidationFailure(t *testing.T) {
	baseStore := NewInMemoryStore(nil)
	store := &firstGetBlockingStore{
		LifecycleStore: baseStore, TaskEventStore: baseStore,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	executor := &scriptedExecutor{leaseExpiryPolicy: LeaseExpiryFail}
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	spec := validSpec("early-failure-cancel")
	spec.NotifySession = false
	spec.RootSessionID = ""
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.NoError(t, err)
	executor.validateErr = errors.New("worker validation failed")

	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	<-store.entered

	cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cancelDone := make(chan struct {
		task *TaskSnapshot
		err  error
	}, 1)
	go func() {
		canceled, cancelErr := manager.RequestCancel(cancelCtx, task.Spec.ID)
		cancelDone <- struct {
			task *TaskSnapshot
			err  error
		}{task: canceled, err: cancelErr}
	}()
	close(store.release)

	require.ErrorContains(t, <-executeDone, "worker validation failed")
	result := <-cancelDone
	require.NoError(t, result.err)
	require.Equal(t, StatusCanceled, result.task.Status)
}

func TestManagerLoadOrRegisterExecutorIsAtomic_BitsUT(t *testing.T) {
	manager := mustNewManager(t, context.Background(), nil)
	const callers = 32
	type result struct {
		actual Executor
		loaded bool
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, callers)
	var group sync.WaitGroup
	for i := 0; i < callers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			actual, loaded, err := manager.LoadOrRegisterExecutor(
				&scriptedExecutor{key: "atomic"},
			)
			results <- result{actual: actual, loaded: loaded, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var registered *scriptedExecutor
	var registrations int
	for result := range results {
		require.NoError(t, result.err)
		actual, ok := result.actual.(*scriptedExecutor)
		require.True(t, ok)
		if registered == nil {
			registered = actual
		} else {
			require.Same(t, registered, actual)
		}
		if !result.loaded {
			registrations++
		}
	}
	require.Equal(t, 1, registrations)

}

type lifecycleStoreOnly struct {
	LifecycleStore
}

func TestNewRejectsMissingTaskEventStore_BitsUT(t *testing.T) {
	_, err := New(context.Background(), &Config{
		Tasks: lifecycleStoreOnly{LifecycleStore: NewInMemoryStore(nil)},
	})
	require.EqualError(
		t,
		err,
		"task/background: task event store is required when task store does not implement TaskEventStore",
	)
}

func TestManagerSubmitParentSessionRequiresOutbox_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(
		t,
		lifecycleStoreOnly{LifecycleStore: store},
		&scriptedExecutor{},
		time.Minute,
	)
	defer closeWithTimeout(manager)
	spec := validSpec("notification-without-outbox")
	spec.NotifySession = true

	_, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.EqualError(
		t,
		err,
		"task/background: task store must implement NotificationOutbox for parent-session tasks",
	)
	_, getErr := store.Get(context.Background(), spec.ID)
	require.ErrorIs(t, getErr, ErrNotFound)
}
