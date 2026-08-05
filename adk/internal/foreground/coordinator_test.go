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

package foreground

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *backgroundtask.Config,
) *backgroundtask.Manager {
	t.Helper()
	if config == nil {
		config = &backgroundtask.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *backgroundtask.Task) error { return nil }
	}
	manager, err := backgroundtask.New(ctx, config)
	require.NoError(t, err)
	return manager
}

type coordinatorExecutor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*coordinatorExecutor) Key() string { return "coordinator-test" }

func (*coordinatorExecutor) LeaseExpiryPolicy() backgroundtask.LeaseExpiryPolicy {
	return backgroundtask.LeaseExpiryRetry
}

func (*coordinatorExecutor) ValidateSpec(backgroundtask.Spec) error { return nil }

func (*coordinatorExecutor) ValidateExecution(context.Context, *backgroundtask.Task) error {
	return nil
}

func (*coordinatorExecutor) SupportsDrain() bool { return false }

func (e *coordinatorExecutor) Execute(
	_ context.Context,
	_ *backgroundtask.Task,
	runtime backgroundtask.ExecutionRuntime,
) (*backgroundtask.ExecutionResult, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return &backgroundtask.ExecutionResult{
			Status: backgroundtask.StatusCompleted, Data: []byte("done"),
		}, nil
	case control := <-runtime.Controls():
		switch control.Kind {
		case backgroundtask.ControlStop:
			return &backgroundtask.ExecutionResult{Status: backgroundtask.StatusCanceled}, nil
		case backgroundtask.ControlTimeout:
			return &backgroundtask.ExecutionResult{
				Status: backgroundtask.StatusFailed, Error: control.Reason,
			}, nil
		default:
			return nil, backgroundtask.ErrDrainCheckpointUnavailable
		}
	}
}

func newCoordinatorTask(t *testing.T) (*backgroundtask.Manager, *coordinatorExecutor, *backgroundtask.Task) {
	t.Helper()
	executor := &coordinatorExecutor{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
	})
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(closeCtx)
	})
	task, err := manager.Submit(context.Background(), backgroundtask.Spec{
		ID: "coordinator-task", ExecutorKey: executor.Key(),
	})
	require.NoError(t, err)
	return manager, executor, task
}

func waitForTerminal(
	t *testing.T,
	manager *backgroundtask.Manager,
	task *backgroundtask.Task,
) *backgroundtask.Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	current := task
	for current.Status == backgroundtask.StatusPending ||
		current.Status == backgroundtask.StatusRunning {
		next, err := manager.WaitForTaskVersion(ctx, &backgroundtask.WaitForTaskVersionRequest{
			TaskID: current.Spec.ID, AfterVersion: current.Version,
		})
		require.NoError(t, err)
		current = next
	}
	return current
}

func TestProjectionDetachedFollowsForegroundLifetime_BitsUT(t *testing.T) {
	type contextKey struct{}
	parent := context.WithValue(context.Background(), contextKey{}, "value")
	detached := detachedCtx{parent: parent}
	_, hasDeadline := detached.Deadline()
	require.False(t, hasDeadline)
	require.Nil(t, detached.Done())
	require.NoError(t, detached.Err())
	ctx, detach := withProjection(detached)
	signal := ProjectionDetached(ctx)
	require.NotNil(t, signal)
	assert.Equal(t, "value", ctx.Value(contextKey{}))

	select {
	case <-signal:
		t.Fatal("projection detached before foreground coordination ended")
	default:
	}

	detach()
	detach()
	select {
	case <-signal:
	default:
		t.Fatal("projection signal remained open after detach")
	}
	assert.Nil(t, ProjectionDetached(context.Background()))
}

func TestRunCoordinatesForegroundLifecycle(t *testing.T) {
	t.Run("completion", func(t *testing.T) {
		manager, executor, task := newCoordinatorTask(t)
		close(executor.release)
		result, err := Run(context.Background(), manager, Policy{}, &Request{
			TaskID: task.Spec.ID,
		})
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusCompleted, result.Status)
		require.Equal(t, "done", string(result.ResultData))
	})

	t.Run("explicit background", func(t *testing.T) {
		manager, executor, task := newCoordinatorTask(t)
		result, err := Run(context.Background(), manager, Policy{}, &Request{
			TaskID: task.Spec.ID, RunInBackground: true,
		})
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusRunning, result.Status)
		require.NoError(t, requestStop(manager, result))
		terminal := waitForTerminal(t, manager, result)
		require.Equal(t, backgroundtask.StatusCanceled, terminal.Status)
		select {
		case <-executor.started:
		default:
			t.Fatal("executor did not start")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		manager, _, task := newCoordinatorTask(t)
		result, err := Run(context.Background(), manager, Policy{TimeoutMs: 1}, &Request{
			TaskID: task.Spec.ID,
		})
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusFailed, result.Status)
		require.Equal(t, "timed out after 1ms", result.ResultError)
	})

	t.Run("auto background", func(t *testing.T) {
		manager, _, task := newCoordinatorTask(t)
		result, err := Run(context.Background(), manager, Policy{
			TimeoutMs: 1,
			ShouldAutoBackground: func(context.Context, *backgroundtask.Task) bool {
				return true
			},
		}, &Request{TaskID: task.Spec.ID})
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusRunning, result.Status)
		require.NoError(t, requestStop(manager, result))
		require.Equal(t, backgroundtask.StatusCanceled, waitForTerminal(t, manager, result).Status)
	})

	t.Run("caller cancellation", func(t *testing.T) {
		manager, executor, task := newCoordinatorTask(t)
		done := make(chan error, 1)
		go func() {
			done <- manager.Execute(context.Background(), task.Spec.ID)
		}()
		<-executor.started
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := waitForeground(
			ctx, manager, Policy{}, &Request{TaskID: task.Spec.ID}, nil, done,
		)
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusCanceled, result.Status)
	})
}

func TestRunRejectsInvalidRequestAndState(t *testing.T) {
	_, err := Run(context.Background(), nil, Policy{}, &Request{TaskID: "task"})
	require.Error(t, err)

	manager, _, task := newCoordinatorTask(t)
	_, err = manager.RequestCancel(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	_, err = Run(context.Background(), manager, Policy{}, &Request{TaskID: task.Spec.ID})
	require.ErrorIs(t, err, backgroundtask.ErrIllegalTransition)

	store := backgroundtask.NewInMemoryStore(nil)
	created, err := store.Create(context.Background(), &backgroundtask.CreateTaskRequest{
		Spec: backgroundtask.Spec{
			ID: "suspended", ExecutorKey: "coordinator-test",
		},
		LeaseExpiryPolicy: backgroundtask.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(context.Background(), &backgroundtask.StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	_, err = store.Suspend(context.Background(), &backgroundtask.SuspendTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
		Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)
	suspendedManager := mustNewBackgroundManager(t,
		context.Background(), &backgroundtask.Config{Tasks: store},
	)
	_, err = Run(context.Background(), suspendedManager, Policy{}, &Request{
		TaskID: created.Spec.ID,
	})
	require.ErrorIs(t, err, backgroundtask.ErrIllegalTransition)
}

func TestForegroundWaitBoundaryErrors(t *testing.T) {
	t.Run("startup context canceled", func(t *testing.T) {
		manager, _, task := newCoordinatorTask(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := waitStarted(ctx, manager, task, make(chan error))
		require.ErrorIs(t, err, context.Canceled)
		canceled, getErr := manager.Get(context.Background(), task.Spec.ID)
		require.NoError(t, getErr)
		require.Equal(t, backgroundtask.StatusCanceled, canceled.Status)
	})

	t.Run("projection context canceled", func(t *testing.T) {
		manager, _, task := newCoordinatorTask(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := waitForeground(
			ctx, manager, Policy{}, &Request{
				TaskID: task.Spec.ID, ProjectionReady: make(chan struct{}),
			}, nil, make(chan error),
		)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("execution finishes before projection", func(t *testing.T) {
		manager, _, task := newCoordinatorTask(t)
		done := make(chan error, 1)
		done <- nil
		result, err := waitForeground(
			context.Background(), manager, Policy{}, &Request{
				TaskID: task.Spec.ID, ProjectionReady: make(chan struct{}),
			}, nil, done,
		)
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusPending, result.Status)
	})

	t.Run("unexpected pending status at timeout", func(t *testing.T) {
		manager, _, task := newCoordinatorTask(t)
		_, err := waitForeground(
			context.Background(), manager, Policy{TimeoutMs: 1},
			&Request{TaskID: task.Spec.ID}, nil, make(chan error),
		)
		require.ErrorContains(t, err, "unexpected task status")
	})
}

func TestWaitStartedReconcilesWorkerClaimRace(t *testing.T) {
	manager, _, task := newCoordinatorTask(t)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	current := task
	deadline := time.Now().Add(time.Second)
	for current.Status != backgroundtask.StatusRunning && time.Now().Before(deadline) {
		var err error
		current, err = manager.Get(context.Background(), task.Spec.ID)
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
	}
	require.Equal(t, backgroundtask.StatusRunning, current.Status)

	done := make(chan error, 1)
	done <- backgroundtask.ErrVersionConflict
	reconciled, err := waitStarted(context.Background(), manager, task, done)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusRunning, reconciled.Status)
	_, err = manager.RequestCancel(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.NoError(t, <-executeDone)
}

func TestAuthoritativeClaimStatus(t *testing.T) {
	for _, status := range []backgroundtask.Status{
		backgroundtask.StatusRunning,
		backgroundtask.StatusCompleted,
		backgroundtask.StatusFailed,
		backgroundtask.StatusCanceled,
	} {
		require.True(t, authoritativeClaimStatus(status))
	}
	for _, status := range []backgroundtask.Status{
		backgroundtask.StatusPending,
		backgroundtask.StatusWaitingInput,
		backgroundtask.StatusSuspended,
	} {
		require.False(t, authoritativeClaimStatus(status))
	}
}

func requestStop(manager *backgroundtask.Manager, task *backgroundtask.Task) error {
	_, err := manager.RequestCancel(context.Background(), task.Spec.ID)
	return err
}
