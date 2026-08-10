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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/internal/taskcontrol"
)

func closeWithTimeout(manager *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = manager.Close(ctx)
}

type firstReleaseConflictStore struct {
	TaskStore
	once sync.Once
}

func (s *firstReleaseConflictStore) ReleaseSuspension(
	ctx context.Context,
	req *ReleaseSuspensionRequest,
) (*Task, error) {
	conflict := false
	s.once.Do(func() { conflict = true })
	if conflict {
		return nil, ErrVersionConflict
	}
	return s.TaskStore.ReleaseSuspension(ctx, req)
}

func TestManagerListTaskEventsDelegatesToStore_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: lifecycleStoreOnly{TaskStore: store}, TaskEvents: store,
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
	require.Len(t, result.Events, 1)
	require.Equal(t, "record", string(result.Events[0].Data))
}

func TestManagerExecuteBindsTimeoutController_BitsUT(t *testing.T) {
	observed := make(chan ControlRequest, 1)
	executor := &scriptedExecutor{
		execute: func(
			_ context.Context,
			_ *Task,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{Status: StatusFailed, Error: control.Reason}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	task, err := manager.Submit(context.Background(), validSpec("timeout"))
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
	require.EqualError(t, err, "backgroundtask: execute task id is required")
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
			_ *Task,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(started)
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{
				Status: StatusSuspended, Checkpoint: []byte("checkpoint"),
			}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), validSpec("close-drain"))
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
		TaskStore: baseStore, entered: make(chan struct{}), release: make(chan struct{}),
	}
	observed := make(chan ControlRequest, 1)
	executor := &scriptedExecutor{
		execute: func(
			_ context.Context,
			_ *Task,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{
				Status: StatusSuspended, Checkpoint: []byte("checkpoint"),
			}, nil
		},
	}
	manager := managerWithExecutor(t, store, executor, time.Minute)
	spec := validSpec("close-initializing")
	spec.SessionID = ""
	spec.NotifySession = false
	task, err := manager.Submit(context.Background(), spec)
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
	suspended, err := store.Suspend(context.Background(), &SuspendTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)

	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: &firstReleaseConflictStore{TaskStore: store}, TaskEvents: store,
	})
	defer closeWithTimeout(manager)
	released, err := manager.ReleaseSuspension(context.Background(), suspended.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, released.Status)
	require.Equal(t, "checkpoint", string(released.Checkpoint))
}

func TestManagerReleaseSuspensionRejectsInvalidTargets_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, &scriptedExecutor{}, time.Minute)
	defer closeWithTimeout(manager)

	released, err := manager.ReleaseSuspension(context.Background(), "")
	require.Nil(t, released)
	require.EqualError(t, err, "backgroundtask: release suspension task id is required")

	task, err := manager.Submit(context.Background(), validSpec("not-suspended"))
	require.NoError(t, err)
	released, err = manager.ReleaseSuspension(context.Background(), task.Spec.ID)
	require.Nil(t, released)
	require.ErrorIs(t, err, ErrIllegalTransition)
}

func TestManagerExecutorPanicFailsTask_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error) {
			panic("executor panic")
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), validSpec("executor-panic"))
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
			_ *Task,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(started)
			control := <-runtime.Controls()
			observed <- control.Kind
			return &ExecutionResult{Status: StatusCanceled}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), validSpec("close-cancel"))
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
			_ *Task,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(started)
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{Status: StatusCanceled}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	task, err := manager.Submit(context.Background(), validSpec("local-cancel"))
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
	executor := &scriptedExecutor{execute: func(
		_ context.Context,
		_ *Task,
		runtime ExecutionRuntime,
	) (*ExecutionResult, error) {
		control := <-runtime.Controls()
		observed <- control
		return &ExecutionResult{Status: StatusCanceled}, nil
	}}
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	require.NoError(t, manager.Execute(context.Background(), started.Spec.ID))
	require.Equal(t, ControlRequest{
		Kind: ControlStop, Reason: "operator request",
	}, <-observed)
	canceled, err := manager.Get(context.Background(), started.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, "operator request", canceled.ResultError)
}

func TestManagerDispatchReadBoundaries(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, &scriptedExecutor{}, time.Minute)
	defer closeWithTimeout(manager)
	submitted, err := manager.Submit(context.Background(), validSpec("dispatch"))
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

	output, err := runtime.EmitProgress(context.Background(), "", []byte("output"))
	require.NoError(t, err)
	require.NotEmpty(t, output.EventID)
	require.True(t, output.FirstEmission)
	supplied, err := runtime.EmitProgress(context.Background(), "caller-event", []byte("supplied"))
	require.NoError(t, err)
	require.Equal(t, "caller-event", supplied.EventID)
	require.True(t, supplied.FirstEmission)
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
	TaskStore
	err error
}

func (s heartbeatErrorStore) Heartbeat(context.Context, *HeartbeatRequest) (*Task, error) {
	return nil, s.err
}

func TestManagerHeartbeatStopsAndCancelsOnLeaseError(t *testing.T) {
	manager := mustNewManager(t, context.Background(), nil)
	manager.heartbeatEvery = time.Nanosecond
	events := NewInMemoryStore(nil)
	runtime := newTaskRuntime(
		heartbeatErrorStore{TaskStore: NewInMemoryStore(nil), err: ErrLeaseLost},
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

func TestExecutorRegistryRegistrationBoundaries(t *testing.T) {
	registry := NewExecutorRegistry()
	require.Error(t, registry.Register(nil))
	executor := &scriptedExecutor{}
	require.NoError(t, registry.Register(executor))
	require.ErrorIs(t, registry.Register(executor), ErrAlreadyExists)
	resolved, ok := registry.Resolve(executor.Key())
	require.True(t, ok)
	require.Same(t, executor, resolved)
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

func TestManagerResumeLifecycleBoundaries(t *testing.T) {
	store := NewInMemoryStore(nil)
	executor := &scriptedExecutor{}
	manager := managerWithExecutor(t, store, executor, time.Minute)
	require.Error(t, func() error {
		_, err := manager.Resume(context.Background(), nil)
		return err
	}())
	_, err := manager.Resume(context.Background(), &ResumeRequest{TaskID: "missing"})
	require.ErrorIs(t, err, ErrNotFound)

	pending, err := manager.Submit(context.Background(), validSpec("pending-resume"))
	require.NoError(t, err)
	_, err = manager.Resume(context.Background(), &ResumeRequest{
		TaskID: pending.Spec.ID, ExpectedVersion: pending.Version,
	})
	require.ErrorIs(t, err, ErrIllegalTransition)

	started := createAndStart(t, store, "waiting-resume")
	waiting, err := store.WaitInput(context.Background(), &WaitInputTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)
	_, err = manager.Resume(context.Background(), &ResumeRequest{
		TaskID: waiting.Spec.ID, ExpectedVersion: waiting.Version + 1,
	})
	require.ErrorIs(t, err, ErrVersionConflict)
	resumed, err := manager.Resume(context.Background(), &ResumeRequest{
		TaskID: waiting.Spec.ID, ExpectedVersion: waiting.Version,
		Data: []byte("opaque"),
	})
	require.NoError(t, err)
	require.Equal(t, []byte("opaque"), resumed.PendingResume)

	missingExecutorStore := NewInMemoryStore(nil)
	started = createAndStart(t, missingExecutorStore, "missing-executor")
	waiting, err = missingExecutorStore.WaitInput(
		context.Background(), &WaitInputTaskRequest{
			TaskID: started.Spec.ID, ExpectedVersion: started.Version,
			Checkpoint: []byte("checkpoint"),
		},
	)
	require.NoError(t, err)
	missingExecutorManager := mustNewManager(t, context.Background(), &Config{Tasks: missingExecutorStore})
	resumed, err = missingExecutorManager.Resume(context.Background(), &ResumeRequest{
		TaskID: waiting.Spec.ID, ExpectedVersion: waiting.Version,
	})
	require.NoError(t, err)
	require.Equal(t, StatusPending, resumed.Status)
}

type firstGetBlockingStore struct {
	TaskStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *firstGetBlockingStore) Get(ctx context.Context, taskID string) (*Task, error) {
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
	return s.TaskStore.Get(ctx, taskID)
}

func TestManagerCancelPendingLocalTaskAfterExecuteValidationFailure(t *testing.T) {
	baseStore := NewInMemoryStore(nil)
	store := &firstGetBlockingStore{
		TaskStore: baseStore, entered: make(chan struct{}), release: make(chan struct{}),
	}
	executor := &scriptedExecutor{leaseExpiryPolicy: LeaseExpiryFail}
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	spec := validSpec("early-failure-cancel")
	spec.NotifySession = false
	spec.SessionID = ""
	task, err := manager.Submit(context.Background(), spec)
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
		task *Task
		err  error
	}, 1)
	go func() {
		canceled, cancelErr := manager.RequestCancel(cancelCtx, task.Spec.ID)
		cancelDone <- struct {
			task *Task
			err  error
		}{task: canceled, err: cancelErr}
	}()
	close(store.release)

	require.ErrorContains(t, <-executeDone, "worker validation failed")
	result := <-cancelDone
	require.NoError(t, result.err)
	require.Equal(t, StatusCanceled, result.task.Status)
}

func TestExecutorRegistryLoadOrRegisterIsAtomic_BitsUT(t *testing.T) {
	registry := NewExecutorRegistry()
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
			actual, loaded, err := registry.LoadOrRegister(
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
	TaskStore
}

func TestNewRejectsMissingTaskEventStore_BitsUT(t *testing.T) {
	_, err := New(context.Background(), &Config{
		Tasks: lifecycleStoreOnly{TaskStore: NewInMemoryStore(nil)},
	})
	require.EqualError(
		t,
		err,
		"backgroundtask: task event store is required when task store does not implement TaskEventStore",
	)
}

func TestManagerSubmitParentSessionRequiresOutbox_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(
		t,
		lifecycleStoreOnly{TaskStore: store},
		&scriptedExecutor{},
		time.Minute,
	)
	defer closeWithTimeout(manager)
	spec := validSpec("notification-without-outbox")
	spec.NotifySession = true

	_, err := manager.Submit(context.Background(), spec)
	require.EqualError(
		t,
		err,
		"backgroundtask: task store must implement NotificationOutbox for parent-session tasks",
	)
	_, getErr := store.Get(context.Background(), spec.ID)
	require.ErrorIs(t, getErr, ErrNotFound)
}
