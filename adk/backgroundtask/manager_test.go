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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closeWithTimeout closes the Manager with a short timeout to avoid blocking on uncompleted tasks.
func closeWithTimeout(m *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = m.Close(ctx)
}

func intPtr(v int) *int { return &v }

// anyRunning reports whether the manager still has a task in StateRunning,
// derived from the public List() snapshot.
func anyRunning(m *Manager) bool {
	for _, t := range m.List() {
		if t.Status == StateRunning {
			return true
		}
	}
	return false
}

// workReturning builds a WorkFunc that returns the given result/error immediately.
func workReturning(result string, err error) WorkFunc {
	return func(ctx context.Context, _ ExecutionRuntime) (string, error) {
		return result, err
	}
}

// workSleeping builds a WorkFunc that sleeps then returns result.
func workSleeping(d time.Duration, result string) WorkFunc {
	return func(ctx context.Context, _ ExecutionRuntime) (string, error) {
		time.Sleep(d)
		return result, nil
	}
}

// workBlocking builds a WorkFunc that blocks until its context is canceled.
func workBlocking() WorkFunc {
	return func(ctx context.Context, _ ExecutionRuntime) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
}

type outputFailureFaultStore struct {
	Store
	failReport bool
	failFinal  bool
	failCancel bool
}

func (s *outputFailureFaultStore) ReportOutputFailure(
	context.Context,
	*ReportOutputFailureRequest,
) (*Task, error) {
	if s.failReport {
		return nil, errors.New("report unavailable")
	}
	return nil, errors.New("unexpected report path")
}

func (s *outputFailureFaultStore) Fail(ctx context.Context, request *FailTaskRequest) (*Task, error) {
	if s.failFinal {
		return nil, errors.New("fail unavailable")
	}
	return s.Store.Fail(ctx, request)
}

func (s *outputFailureFaultStore) Cancel(ctx context.Context, request *CancelTaskRequest) (*Task, error) {
	if s.failCancel {
		return nil, errors.New("cancel commit unavailable")
	}
	return s.Store.Cancel(ctx, request)
}

func run(m *Manager, description string, background bool, work WorkFunc) (*Task, error) {
	return m.Run(context.Background(), &RunInput{
		Description:     description,
		RunInBackground: background,
	}, work)
}

func waitTask(t *testing.T, m *Manager, id string) *Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	task, done := m.Wait(ctx, id)
	require.NotNil(t, task)
	require.True(t, done, "task %s did not finish before the wait deadline", id)
	return task
}

func waitTaskEvent(t *testing.T, ch <-chan *TaskEvent, match func(*TaskEvent) bool) *TaskEvent {
	t.Helper()
	timeout := time.After(time.Second)
	for {
		select {
		case event, ok := <-ch:
			require.True(t, ok, "subscription closed before the expected update")
			if match(event) {
				return event
			}
		case <-timeout:
			t.Fatal("timed out waiting for the expected task update")
		}
	}
}

// --- Run (foreground) Tests ---

func TestManager_RunForeground(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "test task", false, workReturning("hello", nil))
	require.NoError(t, err)
	assert.Equal(t, StateCompleted, result.Status)
	assert.Equal(t, "hello", string(result.ResultData))
	assert.NotEmpty(t, result.Spec.ID)
}

func TestManager_RunForegroundError(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "failing task", false, workReturning("", fmt.Errorf("something failed")))
	require.NoError(t, err) // Run itself doesn't error
	assert.Equal(t, StateFailed, result.Status)
	assert.Equal(t, "something failed", result.ResultError)
}

// --- Run (background) Tests ---

func TestManager_RunBackground(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "bg task", true, workSleeping(50*time.Millisecond, "bg result"))
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.Status)
	assert.NotEmpty(t, result.Spec.ID)
	assert.True(t, anyRunning(m))

	task := waitTask(t, m, result.Spec.ID)
	assert.Equal(t, StateCompleted, task.Status)
	assert.Equal(t, "bg result", string(task.ResultData))
}

// --- Work context lifetime Tests ---

type bgCtxKey string

// A backgrounded task must survive cancellation of the per-call (per-turn)
// context that launched it: it is stopped only by Cancel/Close/deadline.
func TestManager_RunBackground_SurvivesCallerCtxCancel(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	callerCtx, cancelCaller := context.WithCancel(context.Background())

	started := make(chan struct{})
	release := make(chan struct{})
	result, err := m.Run(callerCtx, &RunInput{Description: "bg", RunInBackground: true},
		func(ctx context.Context, _ ExecutionRuntime) (string, error) {
			close(started)
			select {
			case <-release:
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
	require.NoError(t, err)
	require.Equal(t, StateRunning, result.Status)
	<-started

	// Cancel the caller (per-turn) context; the background task must keep running.
	cancelCaller()
	time.Sleep(50 * time.Millisecond)
	task, ok := m.Get(result.Spec.ID)
	require.True(t, ok)
	assert.Equal(t, StateRunning, task.Status, "background task should survive caller ctx cancellation")

	// It finishes only when the work itself completes.
	close(release)
	task = waitTask(t, m, result.Spec.ID)
	assert.Equal(t, StateCompleted, task.Status)
	assert.Equal(t, "done", string(task.ResultData))
}

// A foreground task with no deadline must still be stopped when the caller
// abandons its wait (per-call context canceled).
func TestManager_RunForeground_CallerCtxCancelStops(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(0)})
	defer closeWithTimeout(m)

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancelCaller()
	}()

	result, err := m.Run(callerCtx, &RunInput{Description: "fg blocking"}, workBlocking())
	require.NoError(t, err)
	assert.Equal(t, StateCanceled, result.Status)
}

// The work context preserves the caller context's values (framework/session
// state) even though it is detached from the caller's cancellation.
func TestManager_RunBackground_PreservesCallerCtxValues(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	const key bgCtxKey = "trace"
	callerCtx := context.WithValue(context.Background(), key, "abc")

	got := make(chan interface{}, 1)
	result, err := m.Run(callerCtx, &RunInput{Description: "bg", RunInBackground: true},
		func(ctx context.Context, _ ExecutionRuntime) (string, error) {
			got <- ctx.Value(key)
			return "ok", nil
		})
	require.NoError(t, err)
	require.NotNil(t, result)
	task := waitTask(t, m, result.Spec.ID)
	assert.Equal(t, StateCompleted, task.Status)

	select {
	case v := <-got:
		assert.Equal(t, "abc", v, "background work should see caller ctx values")
	case <-time.After(time.Second):
		t.Fatal("work did not run")
	}
}

// --- Subscribe Tests ---

func TestManager_Subscribe_ForegroundLifecycle(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	ch := m.Subscribe()
	result, err := run(m, "fg task", false, workReturning("done", nil))
	require.NoError(t, err)

	created := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCreated && event.Task.Spec.ID == result.Spec.ID
	})
	assert.Equal(t, StateRunning, created.Task.Status)

	completed := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCompleted && event.Task.Spec.ID == result.Spec.ID
	})
	assert.Equal(t, StateCompleted, completed.Task.Status)
	assert.Equal(t, "done", string(completed.Task.ResultData))
}

func TestManager_Subscribe_BackgroundLifecycle(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	ch := m.Subscribe()
	result, err := run(m, "bg task", true, workSleeping(20*time.Millisecond, "bg result"))
	require.NoError(t, err)

	created := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCreated && event.Task.Spec.ID == result.Spec.ID
	})
	assert.Equal(t, StateRunning, created.Task.Status)

	done := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCompleted && event.Task.Spec.ID == result.Spec.ID
	})
	assert.Equal(t, StateCompleted, done.Task.Status)
	assert.Equal(t, "bg result", string(done.Task.ResultData))
	assert.NotNil(t, done.Task.DoneAt)
}

func TestManager_Subscribe_AutoBackgroundChange(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(20), ShouldAutoBackground: allowBackground})
	defer closeWithTimeout(m)

	ch := m.Subscribe()
	result, err := run(m, "slow", false, workSleeping(80*time.Millisecond, "late"))
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.Status)

	bg := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventBackgrounded && event.Task.Spec.ID == result.Spec.ID
	})
	assert.Equal(t, StateRunning, bg.Task.Status)
	assert.Equal(t, "slow", bg.Task.Spec.Description)

	done := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCompleted && event.Task.Spec.ID == result.Spec.ID
	})
	assert.Equal(t, "late", string(done.Task.ResultData))
}

func TestManager_Subscribe_CancelChange(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	ch := m.Subscribe()
	result, err := run(m, "bg", true, workBlocking())
	require.NoError(t, err)
	require.NoError(t, m.Cancel(result.Spec.ID))

	done := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCanceled && event.Task.Spec.ID == result.Spec.ID
	})
	assert.Equal(t, canceledError, done.Task.ResultError)
}

func TestManager_Subscribe_ClosesOnClose(t *testing.T) {
	m := New(context.Background(), &Config{})
	ch := m.Subscribe()

	require.NoError(t, m.Close(context.Background()))
	_, ok := <-ch
	assert.False(t, ok)
}

// --- Process-local hints ---

func TestManager_TypeIsNotCoreSpecField(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := m.Run(context.Background(), &RunInput{
		Description: "task",
		Type:        "bash",
	}, workReturning("done", nil))
	require.NoError(t, err)

	task, ok := m.Get(result.Spec.ID)
	require.True(t, ok)
	assert.Equal(t, "task", task.Spec.Description)
}

// --- Auto-background Tests ---

// allowBackground is a ShouldAutoBackground hook that permits backgrounding any run.
func allowBackground(context.Context, *Task) bool { return true }

func TestManager_AutoBackground_Slow(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(50), ShouldAutoBackground: allowBackground})
	defer closeWithTimeout(m)

	result, err := run(m, "slow task", false, workSleeping(200*time.Millisecond, "slow result"))
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.Status)
	assert.True(t, anyRunning(m))

	task := waitTask(t, m, result.Spec.ID)
	assert.Equal(t, StateCompleted, task.Status)
	assert.Equal(t, "slow result", string(task.ResultData))
}

// A per-run ForegroundTimeoutMs overrides the Manager default: here the Manager has
// auto-background disabled (0), but the run sets a short per-call deadline, so a
// slow command is moved to the background (the hook permits it) rather than blocking.
func TestManager_PerRunAutoBackgroundOverride(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(0), ShouldAutoBackground: allowBackground})
	defer closeWithTimeout(m)

	override := 50
	result, err := m.Run(context.Background(), &RunInput{
		Description:         "slow",
		ForegroundTimeoutMs: &override,
	}, workSleeping(300*time.Millisecond, "slow result"))
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.Status) // moved to background at 50ms
	assert.True(t, anyRunning(m))

	task := waitTask(t, m, result.Spec.ID)
	assert.Equal(t, StateCompleted, task.Status)
	assert.Equal(t, "slow result", string(task.ResultData))
}

// With no ShouldAutoBackground hook (the default), a run that hits its deadline is
// canceled and reported as timed out — not backgrounded.
func TestManager_DeadlineKillsWhenNotBackgroundable(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(50)}) // no hook
	defer closeWithTimeout(m)

	result, err := run(m, "slow task", false, workBlocking())
	require.NoError(t, err)
	assert.Equal(t, StateFailed, result.Status)
	assert.Equal(t, "timed out after 50ms", result.ResultError)
	assert.False(t, anyRunning(m)) // work was canceled
}

func TestManagerOutputFailureReportErrorCannotCompleteTask(t *testing.T) {
	t.Run("final failure persists", func(t *testing.T) {
		store := &outputFailureFaultStore{Store: NewMemoryStore(nil), failReport: true}
		manager := New(context.Background(), &Config{Store: store})
		task, err := manager.Run(context.Background(), &RunInput{
			Description: "output", OutputFile: "/tasks/output",
		}, func(ctx context.Context, runtime ExecutionRuntime) (string, error) {
			return "", runtime.ReportOutputFailure(ctx, "write failed")
		})
		require.NoError(t, err)
		assert.Equal(t, StatusFailed, task.Status)
		assert.Contains(t, task.ResultError, "report unavailable")
		assert.Empty(t, task.OutputFileErr)
	})

	t.Run("report and final failure return execute error", func(t *testing.T) {
		store := &outputFailureFaultStore{
			Store: NewMemoryStore(nil), failReport: true, failFinal: true,
		}
		manager := New(context.Background(), &Config{Store: store})
		task, err := manager.Run(context.Background(), &RunInput{
			Description: "output", OutputFile: "/tasks/output",
		}, func(ctx context.Context, runtime ExecutionRuntime) (string, error) {
			return "", runtime.ReportOutputFailure(ctx, "write failed")
		})
		require.ErrorContains(t, err, "fail unavailable")
		assert.Nil(t, task)
		tasks := manager.List()
		require.Len(t, tasks, 1)
		assert.NotEqual(t, StatusCompleted, tasks[0].Status)
		assert.Empty(t, tasks[0].OutputFileErr)
	})
}

func TestRunSubmittedSharesForegroundCoordinator(t *testing.T) {
	t.Run("timeout becomes deterministic failure", func(t *testing.T) {
		executor := &scriptedExecutor{
			execute: func(
				_ context.Context,
				_ *Task,
				runtime ExecutionRuntime,
			) (*ExecutionResult, error) {
				control := <-runtime.Controls()
				return &ExecutionResult{Status: StatusFailed, Error: control.Reason}, nil
			},
		}
		manager := managerWithExecutor(t, NewMemoryStore(nil), executor, time.Minute)
		task, err := manager.Submit(context.Background(), validSpec("submitted-timeout"))
		require.NoError(t, err)
		timeout := 10
		result, err := manager.RunSubmitted(context.Background(), &RunSubmittedRequest{
			TaskID: task.Spec.ID, ForegroundTimeoutMs: &timeout,
		})
		require.NoError(t, err)
		assert.Equal(t, StatusFailed, result.Status)
		assert.Equal(t, "timed out after 10ms", result.ResultError)
	})

	t.Run("explicit background signal closes before execute", func(t *testing.T) {
		backgrounded := make(chan bool, 1)
		executor := &scriptedExecutor{
			execute: func(
				_ context.Context,
				_ *Task,
				runtime ExecutionRuntime,
			) (*ExecutionResult, error) {
				backgrounded <- isClosed(runtime.Backgrounded())
				return &ExecutionResult{Status: StatusCompleted, Data: []byte("done")}, nil
			},
		}
		manager := managerWithExecutor(t, NewMemoryStore(nil), executor, time.Minute)
		task, err := manager.Submit(context.Background(), validSpec("submitted-background"))
		require.NoError(t, err)
		result, err := manager.RunSubmitted(context.Background(), &RunSubmittedRequest{
			TaskID: task.Spec.ID, RunInBackground: true,
		})
		require.NoError(t, err)
		assert.Equal(t, StatusRunning, result.Status)
		assert.True(t, <-backgrounded)
		assert.Equal(t, StatusCompleted, waitTask(t, manager, task.Spec.ID).Status)
	})
}

// The hook receives the task so the business can decide per-run; here it backgrounds
// only tasks whose description marks them as a server.
func TestManager_ShouldAutoBackgroundPerTask(t *testing.T) {
	m := New(context.Background(), &Config{
		ForegroundTimeoutMs: intPtr(40),
		ShouldAutoBackground: func(_ context.Context, task *Task) bool {
			return task.Spec.Description == "server"
		},
	})
	defer closeWithTimeout(m)

	bg, err := run(m, "server", false, workSleeping(150*time.Millisecond, "up"))
	require.NoError(t, err)
	assert.Equal(t, StateRunning, bg.Status) // backgrounded

	killed, err := run(m, "oneshot", false, workBlocking())
	require.NoError(t, err)
	assert.Equal(t, StateFailed, killed.Status)
	assert.Equal(t, "timed out after 40ms", killed.ResultError)

	waitTask(t, m, bg.Spec.ID)
}

// A per-run override of <=0 disables auto-background even when the Manager has a
// default, so the run blocks until completion.
func TestManager_PerRunAutoBackgroundDisable(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(20)}) // would auto-bg fast
	defer closeWithTimeout(m)

	off := 0
	result, err := m.Run(context.Background(), &RunInput{
		Description:         "blocking-foreground",
		ForegroundTimeoutMs: &off,
	}, workSleeping(60*time.Millisecond, "done"))
	require.NoError(t, err)
	assert.Equal(t, StateCompleted, result.Status) // blocked despite the 20ms default
	assert.Equal(t, "done", string(result.ResultData))
}

func TestManager_AutoBackground_Fast(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(5000)})
	defer closeWithTimeout(m)

	result, err := run(m, "fast task", false, workReturning("fast result", nil))
	require.NoError(t, err)
	assert.Equal(t, StateCompleted, result.Status)
	assert.Equal(t, "fast result", string(result.ResultData))
	assert.False(t, anyRunning(m))
}

// --- Get/List Tests ---

func TestManager_GetNotFound(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	task, ok := m.Get("nonexistent")
	assert.False(t, ok)
	assert.Nil(t, task)
}

func TestManager_Get(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "test task", false, workReturning("done", nil))
	require.NoError(t, err)

	task, ok := m.Get(result.Spec.ID)
	require.True(t, ok)
	assert.Equal(t, result.Spec.ID, task.Spec.ID)
	assert.Equal(t, "test task", task.Spec.Description)
	assert.Equal(t, StateCompleted, task.Status)
	assert.Equal(t, "done", string(task.ResultData))
	assert.NotNil(t, task.DoneAt)
}

func TestManager_List(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	r1, _ := run(m, "task1", false, workReturning("r1", nil))
	r2, _ := run(m, "task2", false, workReturning("r2", nil))

	tasks := m.List()
	assert.Len(t, tasks, 2)

	byID := make(map[string]*Task)
	for _, task := range tasks {
		byID[task.Spec.ID] = task
	}
	assert.Equal(t, StateCompleted, byID[r1.Spec.ID].Status)
	assert.Equal(t, StateCompleted, byID[r2.Spec.ID].Status)
}

// --- Cancel Tests ---

func TestManager_Cancel(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "cancellable", true, workBlocking())
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.Status)

	err = m.Cancel(result.Spec.ID)
	require.NoError(t, err)

	task := waitTask(t, m, result.Spec.ID)
	assert.Equal(t, StateCanceled, task.Status)
	assert.NotNil(t, task.DoneAt)
	// A canceled task carries a reason rather than an empty terminal state.
	assert.Equal(t, canceledError, task.ResultError)
}

// A foreground run stopped by Cancel reports StateCanceled (with the cancel
// reason) back to the caller, not StateFailed from the work's ctx-canceled error.
func TestManager_Cancel_ForegroundReportsCanceled(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(0)})
	defer closeWithTimeout(m)

	started := make(chan string, 1)
	go func() {
		id := <-started
		_ = m.Cancel(id)
	}()

	result, err := m.Run(context.Background(), &RunInput{Description: "fg cancelable"},
		func(ctx context.Context, _ ExecutionRuntime) (string, error) {
			// Surface the task id to the canceller, then block until canceled.
			for _, t := range m.List() {
				started <- t.Spec.ID
			}
			<-ctx.Done()
			return "", ctx.Err()
		})
	require.NoError(t, err)
	assert.Equal(t, StateCanceled, result.Status)
	assert.Equal(t, canceledError, result.ResultError)
}

func TestManager_CancelNotFound(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	err := m.Cancel("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no background task")
}

func TestManager_CancelAlreadyDone(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, _ := run(m, "task", false, workReturning("done", nil))

	err := m.Cancel(result.Spec.ID)
	assert.ErrorIs(t, err, ErrAlreadyTerminal)
}

// --- Running-state transitions ---

func TestManager_RunningState(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	assert.False(t, anyRunning(m))

	result, _ := run(m, "task", true, workBlocking())
	assert.True(t, anyRunning(m))

	_ = m.Cancel(result.Spec.ID)
	waitTask(t, m, result.Spec.ID)
	assert.False(t, anyRunning(m))
}

// --- Wait ---

func TestManager_WaitCompleted(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "task", true, workSleeping(50*time.Millisecond, "r1"))
	require.NoError(t, err)

	task := waitTask(t, m, result.Spec.ID)
	assert.Equal(t, StateCompleted, task.Status)
	assert.Equal(t, "r1", string(task.ResultData))
}

func TestManager_WaitTimeout(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "task", true, workBlocking())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	task, done := m.Wait(ctx, result.Spec.ID)
	require.NotNil(t, task)
	assert.False(t, done)
	assert.Equal(t, StateRunning, task.Status)
}

func TestManager_WaitNotFound(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	task, done := m.Wait(context.Background(), "missing")
	assert.Nil(t, task)
	assert.False(t, done)
}

// --- Close ---

func TestManager_Close(t *testing.T) {
	m := New(context.Background(), &Config{})

	_, _ = run(m, "task", true, workBlocking())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := m.Close(ctx)
	assert.NoError(t, err)

	_, err = run(m, "new", false, workReturning("x", nil))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shut down")
}

func TestManager_CloseRequiresDeadlineWhileTaskIsActive(t *testing.T) {
	manager := New(context.Background(), &Config{})
	task, err := run(manager, "active", true, workBlocking())
	require.NoError(t, err)
	err = manager.Close(context.Background())
	assert.ErrorIs(t, err, ErrCloseDeadlineRequired)

	completed, err := run(manager, "still-open", false, workReturning("done", nil))
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, completed.Status)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.NoError(t, manager.Close(ctx))
	canceled := waitTask(t, manager, task.Spec.ID)
	assert.Equal(t, StatusCanceled, canceled.Status)
}

func TestManagerCloseReturnsLocalCancelCommitFailure(t *testing.T) {
	store := &outputFailureFaultStore{Store: NewMemoryStore(nil), failCancel: true}
	manager := New(context.Background(), &Config{Store: store})
	_, err := run(manager, "active", true, workBlocking())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = manager.Close(ctx)
	require.ErrorContains(t, err, "cancel commit unavailable")
}

func TestManagerCloseDrainsDurableAndCancelsLocalAtDeadline(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(
			_ context.Context,
			_ *Task,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			control := <-runtime.Controls()
			if control.Kind != ControlDrain {
				return nil, fmt.Errorf("unexpected control %q", control.Kind)
			}
			return &ExecutionResult{
				Status: StatusSuspended, Checkpoint: []byte("checkpoint"),
			}, nil
		},
	}
	store := NewMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	durable, err := manager.Submit(context.Background(), validSpec("durable-close"))
	require.NoError(t, err)
	_, err = manager.RunSubmitted(context.Background(), &RunSubmittedRequest{
		TaskID: durable.Spec.ID, RunInBackground: true,
	})
	require.NoError(t, err)
	local, err := run(manager, "local-close", true, workBlocking())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.NoError(t, manager.Close(ctx))
	durableResult, err := store.Get(context.Background(), durable.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusSuspended, durableResult.Status)
	localResult, err := store.Get(context.Background(), local.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, localResult.Status)
}

func TestManager_RunAfterClose(t *testing.T) {
	m := New(context.Background(), &Config{})
	_ = m.Close(context.Background())

	_, err := run(m, "task", false, workReturning("x", nil))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shut down")
}

// --- Concurrency ---

func TestManager_ConcurrentRuns(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			result, err := run(m, fmt.Sprintf("task-%d", i), false, workReturning(fmt.Sprintf("result-%d", i), nil))
			require.NoError(t, err)
			assert.Equal(t, StateCompleted, result.Status)
		}(i)
	}

	wg.Wait()
	assert.False(t, anyRunning(m))
	assert.Len(t, m.List(), n)
}

// --- Unique IDs ---

func TestManager_UniqueIDs(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		result, err := run(m, "task", false, workReturning("x", nil))
		require.NoError(t, err)
		assert.False(t, ids[result.Spec.ID], "duplicate ID: %s", result.Spec.ID)
		ids[result.Spec.ID] = true
	}
}

// --- RunInBackground flag ---

func TestManager_RunInBackground_Foreground(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "fg task", false, workReturning("done", nil))
	require.NoError(t, err)

	_, ok := m.Get(result.Spec.ID)
	require.True(t, ok)
}

func TestManager_RunInBackground_Background(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "bg task", true, workSleeping(50*time.Millisecond, "bg done"))
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.Status)

	_, ok := m.Get(result.Spec.ID)
	require.True(t, ok)

	waitTask(t, m, result.Spec.ID)
}

var errSentinel = errors.New("sentinel")

func TestManager_ContextCancelStopsWork(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	started := make(chan struct{})
	work := func(ctx context.Context, _ ExecutionRuntime) (string, error) {
		close(started)
		<-ctx.Done()
		return "", errSentinel
	}

	result, err := run(m, "task", true, work)
	require.NoError(t, err)
	<-started

	require.NoError(t, m.Cancel(result.Spec.ID))
	waitTask(t, m, result.Spec.ID)

	task, ok := m.Get(result.Spec.ID)
	require.True(t, ok)
	assert.Equal(t, StateCanceled, task.Status)
}

// --- TaskInfo.Backgrounded signal Tests ---

// isClosed reports whether a done channel has fired without blocking.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// An explicit RunInBackground launch is background from the start, so the work
// must observe Backgrounded already closed on entry.
func TestManager_Backgrounded_ExplicitClosedBeforeWork(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	seen := make(chan bool, 1)
	release := make(chan struct{})
	result, err := m.Run(context.Background(), &RunInput{Description: "bg", RunInBackground: true},
		func(_ context.Context, runtime ExecutionRuntime) (string, error) {
			seen <- isClosed(runtime.Backgrounded())
			<-release
			return "ok", nil
		})
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.Status)

	assert.True(t, <-seen, "explicit background work should see Backgrounded already closed")
	close(release)
	waitTask(t, m, result.Spec.ID)
}

// A run that completes in the foreground is never backgrounded: its Backgrounded
// signal stays open for the whole run.
func TestManager_Backgrounded_ForegroundStaysOpen(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	var duringRun bool
	result, err := run(m, "fg", false, func(_ context.Context, runtime ExecutionRuntime) (string, error) {
		duringRun = isClosed(runtime.Backgrounded())
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, StateCompleted, result.Status)
	assert.False(t, duringRun, "foreground work must not see Backgrounded closed")
}

// A foreground run that is auto-moved to the background at its deadline must have
// its Backgrounded signal close at the transition, so still-running work learns it
// detached.
func TestManager_Backgrounded_AutoBackgroundCloses(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(50), ShouldAutoBackground: allowBackground})
	defer closeWithTimeout(m)

	closedCh := make(chan struct{})
	release := make(chan struct{})
	result, err := m.Run(context.Background(), &RunInput{Description: "slow"},
		func(_ context.Context, runtime ExecutionRuntime) (string, error) {
			// Block until the deadline detaches the run, then confirm the signal fired.
			<-runtime.Backgrounded()
			close(closedCh)
			<-release
			return "slow result", nil
		})
	require.NoError(t, err)
	assert.Equal(t, StateRunning, result.Status)
	close(release)

	select {
	case <-closedCh:
	case <-time.After(time.Second):
		t.Fatal("Backgrounded did not close at the auto-background transition")
	}
	waitTask(t, m, result.Spec.ID)
}
