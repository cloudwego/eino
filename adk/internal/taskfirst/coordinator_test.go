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

package taskfirst

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/foreground"
)

type controlledExecutor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type competingClaimStore struct {
	*background.InMemoryStore
	firstStart   chan struct{}
	releaseFirst chan struct{}
	startCalls   int32
}

func (s *competingClaimStore) Start(
	ctx context.Context,
	req *background.StartTaskRequest,
) (*background.TaskSnapshot, error) {
	call := atomic.AddInt32(&s.startCalls, 1)
	if call == 1 {
		close(s.firstStart)
		<-s.releaseFirst
	}
	task, err := s.InMemoryStore.Start(ctx, req)
	if call == 2 {
		close(s.releaseFirst)
	}
	return task, err
}

func (*controlledExecutor) Key() string { return "taskfirst-test" }

func (*controlledExecutor) LeaseExpiryPolicy() background.LeaseExpiryPolicy {
	return background.LeaseExpiryFail
}

func (*controlledExecutor) ValidateSpec(background.Spec) error { return nil }

func (*controlledExecutor) ValidateExecution(
	context.Context,
	*background.TaskSnapshot,
) error {
	return nil
}

func (*controlledExecutor) SupportsDrain() bool { return false }

func (e *controlledExecutor) Execute(
	_ context.Context,
	_ *background.TaskSnapshot,
	runtime background.ExecutionRuntime,
) (*background.ExecutionResult, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return &background.ExecutionResult{
			Action: background.ExecutionActionComplete,
			Data:   []byte("done"),
		}, nil
	case control := <-runtime.Controls():
		return &background.ExecutionResult{
			Action: background.ExecutionActionCancel,
			Error:  control.Reason,
		}, nil
	}
}

func newControlledExecution(
	t *testing.T,
	policy *Policy,
) (*Execution, *background.Manager, *controlledExecutor) {
	t.Helper()
	manager, err := background.New(context.Background(), nil)
	require.NoError(t, err)
	executor := &controlledExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	_, loaded, err := manager.LoadOrRegisterExecutor(executor)
	require.NoError(t, err)
	require.False(t, loaded)
	execution, err := Start(
		context.Background(),
		manager,
		policy,
		&StartRequest{
			Spec: background.Spec{
				ID: "task", ExecutorKey: executor.Key(), Kind: "test",
			},
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		executor.once.Do(func() { close(executor.started) })
		select {
		case <-executor.release:
		default:
			close(executor.release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	return execution, manager, executor
}

func TestExecutionChannelsAreStableAndTimeoutDoesNotReset(t *testing.T) {
	execution, _, _ := newControlledExecution(t, &Policy{TimeoutMs: 20})
	timeout := execution.Timeout()
	terminal := execution.Terminal()
	require.Equal(t, timeout, execution.Timeout())
	require.Equal(t, terminal, execution.Terminal())
	for i := 0; i < 100; i++ {
		require.Equal(t, timeout, execution.Timeout())
	}
	select {
	case <-timeout:
	case <-time.After(time.Second):
		t.Fatal("foreground timeout was reset")
	}
}

func TestAwaitPublishesOnTimeoutAndLeavesTaskRunning(t *testing.T) {
	type contextKey struct{}
	callerCtx := context.WithValue(
		context.Background(),
		contextKey{},
		"caller-value",
	)
	execution, manager, executor := newControlledExecution(t, &Policy{
		TimeoutMs: 1,
		ShouldAutoBackground: func(
			ctx context.Context,
			_ *foreground.CandidateInfo,
		) bool {
			require.Equal(t, "caller-value", ctx.Value(contextKey{}))
			require.NoError(t, ctx.Err())
			return true
		},
	})
	outcome, err := execution.Await(callerCtx)
	require.NoError(t, err)
	require.True(t, outcome.Backgrounded)
	require.Equal(t, background.PublicationOnBackground, outcome.Task.Publication)
	require.Equal(t, background.StatusRunning, outcome.Task.Status)

	close(executor.release)
	terminal, err := execution.WaitTerminal(context.Background())
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, terminal.Status)
	stored, err := manager.Get(context.Background(), execution.TaskID())
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, stored.Status)
}

func TestExecutionObservesAttemptClaimedByAnotherManager(t *testing.T) {
	store := &competingClaimStore{
		InMemoryStore: background.NewInMemoryStore(nil),
		firstStart:    make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	managerOne, err := background.New(context.Background(), &background.Config{
		Tasks: store, TaskEvents: store,
	})
	require.NoError(t, err)
	managerTwo, err := background.New(context.Background(), &background.Config{
		Tasks: store, TaskEvents: store,
	})
	require.NoError(t, err)
	executor := &controlledExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	for _, manager := range []*background.Manager{managerOne, managerTwo} {
		_, loaded, registerErr := manager.LoadOrRegisterExecutor(executor)
		require.NoError(t, registerErr)
		require.False(t, loaded)
	}
	execution, err := Start(
		context.Background(),
		managerOne,
		&Policy{},
		&StartRequest{Spec: background.Spec{
			ID: "competing-claim", ExecutorKey: executor.Key(), Kind: "test",
		}},
	)
	require.NoError(t, err)
	select {
	case <-store.firstStart:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not reach Start")
	}
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- managerTwo.Execute(
			context.Background(),
			execution.TaskID(),
		)
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("competing manager did not claim the task")
	}

	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, err := execution.Await(callerCtx)
	require.NoError(t, err)
	require.True(t, outcome.Backgrounded)
	require.Equal(t, background.StatusRunning, outcome.Task.Status)

	close(executor.release)
	require.NoError(t, <-executeDone)
	terminal, err := execution.WaitTerminal(context.Background())
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, terminal.Status)
	for _, manager := range []*background.Manager{managerOne, managerTwo} {
		closeCtx, closeCancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		require.NoError(t, manager.Close(closeCtx))
		closeCancel()
	}
}

func TestAwaitRejectedTimeoutWaitsForCanceledTerminal(t *testing.T) {
	execution, manager, _ := newControlledExecution(t, &Policy{
		TimeoutMs: 1,
		ShouldAutoBackground: func(
			context.Context,
			*foreground.CandidateInfo,
		) bool {
			return false
		},
	})
	outcome, err := execution.Await(context.Background())
	require.Nil(t, outcome)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var timeoutErr *task.ForegroundTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, time.Millisecond, timeoutErr.Timeout)
	require.Equal(t, execution.TaskID(), timeoutErr.TaskID)

	canceled, getErr := manager.Get(context.Background(), execution.TaskID())
	require.NoError(t, getErr)
	require.Equal(t, background.StatusCanceled, canceled.Status)
	require.Equal(t, "timed out after 1ms", canceled.ResultError)
	require.Equal(t, background.PublicationDeferred, canceled.Publication)
}

func TestAwaitCallerAbortPolicy(t *testing.T) {
	t.Run("default detaches", func(t *testing.T) {
		execution, _, executor := newControlledExecution(t, &Policy{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome, err := execution.Await(ctx)
		require.NoError(t, err)
		require.True(t, outcome.Backgrounded)
		require.Equal(t, background.StatusRunning, outcome.Task.Status)
		close(executor.release)
	})

	t.Run("explicit policy cancels", func(t *testing.T) {
		execution, _, _ := newControlledExecution(t, &Policy{
			ShouldCancelOnCallerAbort: func(
				context.Context,
				*foreground.CallerAbortInfo,
			) bool {
				return true
			},
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome, err := execution.Await(ctx)
		require.NoError(t, err)
		require.False(t, outcome.Backgrounded)
		require.Equal(t, background.StatusCanceled, outcome.Task.Status)
		require.Equal(
			t,
			"caller aborted foreground projection",
			outcome.Task.ResultError,
		)
	})
}
