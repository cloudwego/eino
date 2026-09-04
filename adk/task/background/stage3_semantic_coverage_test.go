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
)

type semanticBoundaryStore struct {
	LifecycleStore
	getCalls           int64
	startCalls         int64
	requestCancelCalls int64
	waitVersionCalls   int64
	onRequestCancel    func()
}

func (s *semanticBoundaryStore) Get(
	ctx context.Context,
	taskID string,
) (*TaskSnapshot, error) {
	atomic.AddInt64(&s.getCalls, 1)
	return s.LifecycleStore.Get(ctx, taskID)
}

func (s *semanticBoundaryStore) Start(
	ctx context.Context,
	req *StartTaskRequest,
) (*TaskSnapshot, error) {
	atomic.AddInt64(&s.startCalls, 1)
	return s.LifecycleStore.Start(ctx, req)
}

func (s *semanticBoundaryStore) RequestCancel(
	context.Context,
	*RequestCancelRequest,
) (*TaskSnapshot, error) {
	atomic.AddInt64(&s.requestCancelCalls, 1)
	if s.onRequestCancel != nil {
		s.onRequestCancel()
	}
	return nil, ErrVersionConflict
}

func (s *semanticBoundaryStore) WaitForTaskVersion(
	ctx context.Context,
	req *WaitForTaskVersionRequest,
) (*TaskSnapshot, error) {
	atomic.AddInt64(&s.waitVersionCalls, 1)
	return s.LifecycleStore.WaitForTaskVersion(ctx, req)
}

func TestManagerExecuteAdmissionBoundaries(t *testing.T) {
	t.Run("closed manager rejects before reading the store", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		countingStore := &semanticBoundaryStore{LifecycleStore: store}
		manager := mustNewManager(t, context.Background(), &Config{
			Tasks: countingStore, TaskEvents: store,
		})
		require.NoError(t, manager.Close(context.Background()))

		err := manager.Execute(context.Background(), "closed-task")

		require.EqualError(
			t,
			err,
			"the task manager has shut down and is no longer accepting new tasks. "+
				"Do not retry this; finish using any results you already have",
		)
		require.Zero(t, atomic.LoadInt64(&countingStore.getCalls))
		require.Zero(t, atomic.LoadInt64(&countingStore.startCalls))
		_, getErr := store.Get(context.Background(), "closed-task")
		require.ErrorIs(t, getErr, ErrNotFound)
	})

	t.Run("same task cannot execute concurrently", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		var executeCalls int64
		executor := &scriptedExecutor{execute: func(
			ctx context.Context,
			_ *TaskSnapshot,
			_ ExecutionRuntime,
		) (*ExecutionResult, error) {
			atomic.AddInt64(&executeCalls, 1)
			close(started)
			select {
			case <-release:
				return &ExecutionResult{
					Action: ExecutionActionComplete,
					Data:   []byte("done"),
				}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				return nil, context.DeadlineExceeded
			}
		}}
		store := NewInMemoryStore(nil)
		countingStore := &semanticBoundaryStore{LifecycleStore: store}
		manager := managerWithExecutor(t, countingStore, executor, time.Minute)
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(release) })
			closeWithTimeout(manager)
		})
		spec := validSpec("same-task-execute")
		spec.RootSessionID = ""
		spec.NotifySession = false
		submitted, err := manager.Submit(
			context.Background(),
			&SubmitRequest{Spec: spec},
		)
		require.NoError(t, err)

		firstDone := make(chan error, 1)
		go func() {
			firstDone <- manager.Execute(context.Background(), submitted.Spec.ID)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for the first execution to start")
		}

		secondDone := make(chan error, 1)
		go func() {
			secondDone <- manager.Execute(context.Background(), submitted.Spec.ID)
		}()
		select {
		case secondErr := <-secondDone:
			require.EqualError(
				t,
				secondErr,
				"task/background: task is already executing in this manager",
			)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for concurrent execution rejection")
		}
		require.Equal(t, int64(1), atomic.LoadInt64(&executeCalls))
		require.Equal(t, int64(1), atomic.LoadInt64(&countingStore.getCalls))
		require.Equal(t, int64(1), atomic.LoadInt64(&countingStore.startCalls))
		running, err := store.Get(context.Background(), submitted.Spec.ID)
		require.NoError(t, err)
		require.Equal(t, StatusRunning, running.Status)
		require.Equal(t, int64(1), running.Attempt)

		releaseOnce.Do(func() { close(release) })
		select {
		case firstErr := <-firstDone:
			require.NoError(t, firstErr)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for the first execution to finish")
		}
		completed, err := store.Get(context.Background(), submitted.Spec.ID)
		require.NoError(t, err)
		require.Equal(t, StatusCompleted, completed.Status)
		require.Equal(t, []byte("done"), completed.ResultData)
		require.Equal(t, int64(1), atomic.LoadInt64(&executeCalls))
		require.Equal(t, int64(1), atomic.LoadInt64(&countingStore.getCalls))
		require.Equal(t, int64(1), atomic.LoadInt64(&countingStore.startCalls))
	})
}

func TestManagerRequestCancelVersionConflictBoundaries(t *testing.T) {
	newPendingTask := func(t *testing.T, store *InMemoryStore, taskID string) *TaskSnapshot {
		t.Helper()
		snapshot, err := store.Create(context.Background(), &CreateTaskRequest{
			Spec:              validSpec(taskID),
			LeaseExpiryPolicy: LeaseExpiryRetry,
		})
		require.NoError(t, err)
		return snapshot
	}
	assertUnchanged := func(
		t *testing.T,
		store *InMemoryStore,
		before *TaskSnapshot,
	) {
		t.Helper()
		current, err := store.Get(context.Background(), before.Spec.ID)
		require.NoError(t, err)
		require.Equal(t, StatusPending, current.Status)
		require.Equal(t, before.Version, current.Version)
		require.Nil(t, current.CancelRequestedAt)
		require.Empty(t, current.CancelReason)
	}

	t.Run("returns the eighth conflict without mutating state", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		pending := newPendingTask(t, store, "cancel-conflict-exhausted")
		conflictingStore := &semanticBoundaryStore{LifecycleStore: store}
		manager := mustNewManager(t, context.Background(), &Config{
			Tasks: conflictingStore, TaskEvents: store,
		})
		defer closeWithTimeout(manager)

		result, err := manager.RequestCancel(
			context.Background(),
			pending.Spec.ID,
			WithCancellationReason("operator stop"),
		)

		require.Nil(t, result)
		require.ErrorIs(t, err, ErrVersionConflict)
		require.Equal(t, int64(8), atomic.LoadInt64(&conflictingStore.getCalls))
		require.Equal(
			t,
			int64(8),
			atomic.LoadInt64(&conflictingStore.requestCancelCalls),
		)
		assertUnchanged(t, store, pending)
	})

	t.Run("stops after the conflict that cancels context", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		pending := newPendingTask(t, store, "cancel-conflict-context")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conflictingStore := &semanticBoundaryStore{
			LifecycleStore:  store,
			onRequestCancel: cancel,
		}
		manager := mustNewManager(t, context.Background(), &Config{
			Tasks: conflictingStore, TaskEvents: store,
		})
		defer closeWithTimeout(manager)

		result, err := manager.RequestCancel(ctx, pending.Spec.ID)

		require.Nil(t, result)
		require.ErrorIs(t, err, context.Canceled)
		require.NotErrorIs(t, err, ErrVersionConflict)
		require.Equal(t, int64(1), atomic.LoadInt64(&conflictingStore.getCalls))
		require.Equal(
			t,
			int64(1),
			atomic.LoadInt64(&conflictingStore.requestCancelCalls),
		)
		assertUnchanged(t, store, pending)
	})
}

func TestPersistTaskEventEnforcesStickyWriterValidation(t *testing.T) {
	scope := TaskEventScope{TaskID: "task", Attempt: 1, EventID: "event"}

	t.Run("invalid input never reaches the underlying writer", func(t *testing.T) {
		for _, testCase := range []struct {
			name  string
			input *TaskEventPartInput
		}{
			{name: "nil input"},
			{name: "empty part id", input: &TaskEventPartInput{}},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				var underlyingCalls int64
				runtime := taskEventRuntimeStub{
					scope: scope,
					writer: taskEventWriterFunc(func(
						context.Context,
						*TaskEventPartInput,
					) (*AppendTaskEventResult, error) {
						atomic.AddInt64(&underlyingCalls, 1)
						return nil, nil
					}),
				}
				var firstErr error
				result, err := PersistTaskEvent[string, string](
					context.Background(),
					runtime,
					scope.EventID,
					&TaskEventEnvelope[string, string]{Event: "value"},
					TaskEventPersisterFunc[string, string](func(
						ctx context.Context,
						_ TaskEventScope,
						_ *TaskEventEnvelope[string, string],
						writer TaskEventWriter,
					) error {
						_, firstErr = writer.Append(ctx, testCase.input)
						require.EqualError(
							t,
							firstErr,
							"task/background: task event writer and non-empty part id are required",
						)
						_, repeatedErr := writer.Append(ctx, &TaskEventPartInput{
							PartID: "valid",
						})
						require.ErrorIs(t, repeatedErr, firstErr)
						return nil
					}),
				)

				require.NotNil(t, result)
				require.Equal(t, scope, result.Scope)
				require.Empty(t, result.Appends)
				require.ErrorIs(t, err, firstErr)
				require.Zero(t, atomic.LoadInt64(&underlyingCalls))
			})
		}
	})

	t.Run("underlying writer error makes the boundary sticky", func(t *testing.T) {
		writerErr := errors.New("writer failed")
		var underlyingCalls int64
		runtime := taskEventRuntimeStub{
			scope: scope,
			writer: taskEventWriterFunc(func(
				context.Context,
				*TaskEventPartInput,
			) (*AppendTaskEventResult, error) {
				atomic.AddInt64(&underlyingCalls, 1)
				return nil, writerErr
			}),
		}
		var firstErr error
		result, err := PersistTaskEvent[string, string](
			context.Background(),
			runtime,
			scope.EventID,
			&TaskEventEnvelope[string, string]{Event: "value"},
			TaskEventPersisterFunc[string, string](func(
				ctx context.Context,
				_ TaskEventScope,
				_ *TaskEventEnvelope[string, string],
				writer TaskEventWriter,
			) error {
				_, firstErr = writer.Append(ctx, &TaskEventPartInput{
					PartID: "first",
				})
				require.ErrorIs(t, firstErr, writerErr)
				_, repeatedErr := writer.Append(ctx, &TaskEventPartInput{
					PartID: "second",
				})
				require.ErrorIs(t, repeatedErr, firstErr)
				return nil
			}),
		)

		require.NotNil(t, result)
		require.Equal(t, scope, result.Scope)
		require.Empty(t, result.Appends)
		require.ErrorIs(t, err, writerErr)
		require.Equal(t, int64(1), atomic.LoadInt64(&underlyingCalls))
	})

	t.Run("invalid underlying result makes validation sticky", func(t *testing.T) {
		var underlyingCalls int64
		runtime := taskEventRuntimeStub{
			scope: scope,
			writer: taskEventWriterFunc(func(
				context.Context,
				*TaskEventPartInput,
			) (*AppendTaskEventResult, error) {
				atomic.AddInt64(&underlyingCalls, 1)
				return nil, nil
			}),
		}
		var firstErr error
		result, err := PersistTaskEvent[string, string](
			context.Background(),
			runtime,
			scope.EventID,
			&TaskEventEnvelope[string, string]{Event: "value"},
			TaskEventPersisterFunc[string, string](func(
				ctx context.Context,
				_ TaskEventScope,
				_ *TaskEventEnvelope[string, string],
				writer TaskEventWriter,
			) error {
				_, firstErr = writer.Append(ctx, &TaskEventPartInput{
					PartID: "first",
				})
				require.EqualError(
					t,
					firstErr,
					"task/background: task event writer returned an incomplete append result",
				)
				_, repeatedErr := writer.Append(ctx, &TaskEventPartInput{
					PartID: "second",
				})
				require.ErrorIs(t, repeatedErr, firstErr)
				return nil
			}),
		)

		require.NotNil(t, result)
		require.Equal(t, scope, result.Scope)
		require.Empty(t, result.Appends)
		require.ErrorIs(t, err, firstErr)
		require.Equal(t, int64(1), atomic.LoadInt64(&underlyingCalls))
	})
}

func TestManagerWaitForTaskVersionMissingTaskReturnsImmediately(t *testing.T) {
	store := NewInMemoryStore(nil)
	countingStore := &semanticBoundaryStore{LifecycleStore: store}
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: countingStore, TaskEvents: store,
	})
	defer closeWithTimeout(manager)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	type waitResult struct {
		task *TaskSnapshot
		err  error
	}
	done := make(chan waitResult, 1)
	go func() {
		task, err := manager.WaitForTaskVersion(
			ctx,
			&WaitForTaskVersionRequest{
				TaskID: "missing", AfterVersion: 100,
			},
		)
		done <- waitResult{task: task, err: err}
	}()

	var result waitResult
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for missing task lookup")
	}
	require.Nil(t, result.task)
	require.ErrorIs(t, result.err, ErrNotFound)
	require.NotErrorIs(t, result.err, context.Canceled)
	require.Equal(t, int64(1), atomic.LoadInt64(&countingStore.waitVersionCalls))
}
