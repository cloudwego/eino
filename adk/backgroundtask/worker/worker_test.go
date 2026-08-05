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

package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

type testExecutor struct {
	execute     func(context.Context, *backgroundtask.Task) (*backgroundtask.ExecutionResult, error)
	validateErr error
}

func (*testExecutor) Key() string { return "worker-test" }
func (*testExecutor) LeaseExpiryPolicy() backgroundtask.LeaseExpiryPolicy {
	return backgroundtask.LeaseExpiryRetry
}
func (*testExecutor) ValidateSpec(backgroundtask.Spec) error { return nil }
func (e *testExecutor) ValidateExecution(context.Context, *backgroundtask.Task) error {
	return e.validateErr
}
func (*testExecutor) SupportsDrain() bool { return true }
func (e *testExecutor) Execute(
	ctx context.Context,
	task *backgroundtask.Task,
	_ backgroundtask.ExecutionRuntime,
) (*backgroundtask.ExecutionResult, error) {
	if e.execute != nil {
		return e.execute(ctx, task)
	}
	return &backgroundtask.ExecutionResult{
		Status: backgroundtask.StatusCompleted, Data: []byte("done"),
	}, nil
}

func newWorkerManager(t *testing.T, executor backgroundtask.Executor) *backgroundtask.Manager {
	t.Helper()
	registry := backgroundtask.NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	return backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: registry})
}

func submitTask(t *testing.T, manager *backgroundtask.Manager, id string) *backgroundtask.Task {
	t.Helper()
	task, err := manager.Submit(context.Background(), backgroundtask.Spec{
		ID: id, ExecutorKey: "worker-test", Kind: "test", Payload: []byte("{}"),
	})
	require.NoError(t, err)
	return task
}

func waitTaskStatus(
	t *testing.T,
	manager *backgroundtask.Manager,
	taskID string,
	status backgroundtask.Status,
) *backgroundtask.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.Get(context.Background(), taskID)
		require.NoError(t, err)
		if task.Status == status {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not reach %s", taskID, status)
	return nil
}

func TestWorkerPicksUpPendingAndYieldedTasks(t *testing.T) {
	executor := &testExecutor{
		execute: func(_ context.Context, task *backgroundtask.Task) (*backgroundtask.ExecutionResult, error) {
			if task.Attempt == 1 {
				return &backgroundtask.ExecutionResult{
					Directive:  backgroundtask.ExecutionDirectiveYield,
					Checkpoint: []byte("ref"),
				}, nil
			}
			return &backgroundtask.ExecutionResult{
				Status: backgroundtask.StatusCompleted, Data: []byte("done"),
			}, nil
		},
	}
	manager := newWorkerManager(t, executor)
	task := submitTask(t, manager, "yielded")
	worker, err := NewWorker(WorkerConfig{
		Manager: manager, ExecutorKeys: []string{"worker-test"},
		PollInterval: time.Millisecond, MaxConcurrent: 1,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	completed := waitTaskStatus(t, manager, task.Spec.ID, backgroundtask.StatusCompleted)
	require.Equal(t, int64(2), completed.Attempt)
	require.Equal(t, "ref", string(completed.Checkpoint))
	cancel()
	require.NoError(t, <-done)
}

func TestWorkerDelaysOnlyAttemptZeroTasks(t *testing.T) {
	manager := newWorkerManager(t, &testExecutor{})
	task := submitTask(t, manager, "delayed")
	worker, err := NewWorker(WorkerConfig{
		Manager: manager, ExecutorKeys: []string{"worker-test"},
		PollInterval: time.Millisecond, InitialPickupDelay: 80 * time.Millisecond,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)
	current, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusPending, current.Status)
	waitTaskStatus(t, manager, task.Spec.ID, backgroundtask.StatusCompleted)
	cancel()
	require.NoError(t, <-done)
}

func TestWorkerBoundsConcurrentDispatch(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 3)
	var mu sync.Mutex
	active, maximum := 0, 0
	executor := &testExecutor{
		execute: func(ctx context.Context, _ *backgroundtask.Task) (*backgroundtask.ExecutionResult, error) {
			mu.Lock()
			active++
			if active > maximum {
				maximum = active
			}
			mu.Unlock()
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			mu.Lock()
			active--
			mu.Unlock()
			return &backgroundtask.ExecutionResult{Status: backgroundtask.StatusCompleted}, nil
		},
	}
	manager := newWorkerManager(t, executor)
	for _, id := range []string{"one", "two", "three"} {
		submitTask(t, manager, id)
	}
	worker, err := NewWorker(WorkerConfig{
		Manager: manager, ExecutorKeys: []string{"worker-test"},
		PollInterval: time.Millisecond, MaxConcurrent: 2,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	<-started
	<-started
	select {
	case <-started:
		t.Fatal("third task started before a concurrency slot was available")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	waitTaskStatus(t, manager, "three", backgroundtask.StatusCompleted)
	cancel()
	require.NoError(t, <-done)
	mu.Lock()
	require.Equal(t, 2, maximum)
	mu.Unlock()
}

func TestWorkerReturnsPermanentDispatchError(t *testing.T) {
	wantErr := errors.New("worker dependency unavailable")
	manager := newWorkerManager(t, &testExecutor{validateErr: wantErr})
	submitTask(t, manager, "invalid")
	worker, err := NewWorker(WorkerConfig{
		Manager: manager, ExecutorKeys: []string{"worker-test"},
		PollInterval: time.Millisecond,
	})
	require.NoError(t, err)
	runCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.ErrorContains(t, worker.Run(runCtx), wantErr.Error())
	task, err := manager.Get(context.Background(), "invalid")
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusPending, task.Status)
}

func TestNewWorkerValidationAndDefaults(t *testing.T) {
	_, err := NewWorker(WorkerConfig{})
	require.ErrorContains(t, err, "manager is required")
	manager := newWorkerManager(t, &testExecutor{})
	_, err = NewWorker(WorkerConfig{Manager: manager})
	require.ErrorContains(t, err, "executor keys")
	_, err = NewWorker(WorkerConfig{
		Manager: manager, ExecutorKeys: []string{""},
	})
	require.ErrorContains(t, err, "executor key is required")
	_, err = NewWorker(WorkerConfig{
		Manager: manager, ExecutorKeys: []string{"worker-test"},
		InitialPickupDelay: -time.Second,
	})
	require.ErrorContains(t, err, "cannot be negative")

	worker, err := NewWorker(WorkerConfig{
		Manager: manager, ExecutorKeys: []string{"worker-test"},
	})
	require.NoError(t, err)
	require.Equal(t, time.Second, worker.pollInterval)
	require.Equal(t, 1, worker.maxConcurrent)
}

func TestBenignDispatchErrorClassification(t *testing.T) {
	activeCtx := context.Background()
	for _, err := range []error{
		nil,
		backgroundtask.ErrVersionConflict,
		backgroundtask.ErrIllegalTransition,
		backgroundtask.ErrAlreadyTerminal,
		backgroundtask.ErrNotFound,
		errors.New("backgroundtask: task is already executing in this manager"),
	} {
		require.True(t, benignDispatchError(activeCtx, err))
	}
	require.False(t, benignDispatchError(activeCtx, errors.New("dependency unavailable")))
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	require.True(t, benignDispatchError(canceledCtx, errors.New("any error")))
}

type listErrorStore struct {
	backgroundtask.Store
}

func (listErrorStore) ListPending(
	context.Context,
	*backgroundtask.ListPendingRequest,
) (*backgroundtask.ListPendingResult, error) {
	return nil, errors.New("list failed")
}

func TestWorkerRunValidationAndListFailure(t *testing.T) {
	var nilWorker *Worker
	require.ErrorContains(t, nilWorker.Run(context.Background()), "worker is required")

	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{
		Store: listErrorStore{Store: backgroundtask.NewInMemoryStore(nil)},
	})
	worker, err := NewWorker(WorkerConfig{
		Manager: manager, ExecutorKeys: []string{"worker-test"},
	})
	require.NoError(t, err)
	require.ErrorContains(t, worker.Run(context.Background()), "list failed")
}
