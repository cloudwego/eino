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

	"github.com/cloudwego/eino/adk/filesystem"
)

// closeWithTimeout closes the Manager with a short timeout to avoid blocking on uncompleted tasks.
func closeWithTimeout(m *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = m.Close(ctx)
}

func intPtr(v int) *int { return &v }

// anyRunning reports whether the manager still has a task in StatusRunning,
// derived from the public List() snapshot.
func anyRunning(m *Manager) bool {
	for _, t := range m.List() {
		if t.Status == StatusRunning {
			return true
		}
	}
	return false
}

// workReturning builds a WorkFunc that returns the given result/error immediately.
func workReturning(result string, err error) WorkFunc {
	return func(ctx context.Context) (string, error) {
		return result, err
	}
}

// workSleeping builds a WorkFunc that sleeps then returns result.
func workSleeping(d time.Duration, result string) WorkFunc {
	return func(ctx context.Context) (string, error) {
		time.Sleep(d)
		return result, nil
	}
}

// workBlocking builds a WorkFunc that blocks until its context is canceled.
func workBlocking() WorkFunc {
	return func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
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
	assert.Equal(t, StatusCompleted, result.Status)
	assert.Equal(t, "hello", result.Result)
	assert.NotEmpty(t, result.ID)
}

func TestManager_RunForegroundError(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "failing task", false, workReturning("", fmt.Errorf("something failed")))
	require.NoError(t, err) // Run itself doesn't error
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, "something failed", result.Error)
}

// --- Run (background) Tests ---

func TestManager_RunBackground(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "bg task", true, workSleeping(50*time.Millisecond, "bg result"))
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)
	assert.NotEmpty(t, result.ID)
	assert.True(t, anyRunning(m))

	task := waitTask(t, m, result.ID)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "bg result", task.Result)
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
		func(ctx context.Context) (string, error) {
			close(started)
			select {
			case <-release:
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
	require.NoError(t, err)
	require.Equal(t, StatusRunning, result.Status)
	<-started

	// Cancel the caller (per-turn) context; the background task must keep running.
	cancelCaller()
	time.Sleep(50 * time.Millisecond)
	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.Equal(t, StatusRunning, task.Status, "background task should survive caller ctx cancellation")

	// It finishes only when the work itself completes.
	close(release)
	task = waitTask(t, m, result.ID)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "done", task.Result)
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
	assert.Equal(t, StatusCanceled, result.Status)
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
		func(ctx context.Context) (string, error) {
			got <- ctx.Value(key)
			return "ok", nil
		})
	require.NoError(t, err)
	require.Equal(t, StatusRunning, result.Status)
	waitTask(t, m, result.ID)

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
		return event.Type == TaskEventCreated && event.Task.ID == result.ID
	})
	assert.False(t, created.Task.RunInBackground)
	assert.Equal(t, StatusRunning, created.Task.Status)

	completed := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCompleted && event.Task.ID == result.ID
	})
	assert.Equal(t, StatusCompleted, completed.Task.Status)
	assert.Equal(t, "done", completed.Task.Result)
}

func TestManager_Subscribe_BackgroundLifecycle(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	ch := m.Subscribe()
	result, err := run(m, "bg task", true, workSleeping(20*time.Millisecond, "bg result"))
	require.NoError(t, err)

	created := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCreated && event.Task.ID == result.ID
	})
	assert.True(t, created.Task.RunInBackground)
	assert.Equal(t, StatusRunning, created.Task.Status)

	done := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCompleted && event.Task.ID == result.ID
	})
	assert.Equal(t, StatusCompleted, done.Task.Status)
	assert.Equal(t, "bg result", done.Task.Result)
	assert.NotNil(t, done.Task.DoneAt)
}

func TestManager_Subscribe_AutoBackgroundChange(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(20), ShouldAutoBackground: allowBackground})
	defer closeWithTimeout(m)

	ch := m.Subscribe()
	result, err := run(m, "slow", false, workSleeping(80*time.Millisecond, "late"))
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)

	bg := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventBackgrounded && event.Task.ID == result.ID
	})
	assert.Equal(t, StatusRunning, bg.Task.Status)
	assert.True(t, bg.Task.RunInBackground)
	assert.Equal(t, "slow", bg.Task.Description)

	done := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCompleted && event.Task.ID == result.ID
	})
	assert.Equal(t, "late", done.Task.Result)
}

func TestManager_Subscribe_CancelChange(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	ch := m.Subscribe()
	result, err := run(m, "bg", true, workBlocking())
	require.NoError(t, err)
	require.NoError(t, m.Cancel(result.ID))

	done := waitTaskEvent(t, ch, func(event *TaskEvent) bool {
		return event.Type == TaskEventCanceled && event.Task.ID == result.ID
	})
	assert.Equal(t, canceledError, done.Task.Error)
}

func TestManager_Subscribe_ClosesOnClose(t *testing.T) {
	m := New(context.Background(), &Config{})
	ch := m.Subscribe()

	require.NoError(t, m.Close(context.Background()))
	_, ok := <-ch
	assert.False(t, ok)
}

// --- Type / ToolUseID ---

func TestManager_TypeAndToolUseIDStored(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := m.Run(context.Background(), &RunInput{
		Description: "task",
		Type:        "bash",
		ToolUseID:   "call_42",
	}, workReturning("done", nil))
	require.NoError(t, err)

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.Equal(t, "bash", task.Type)
	assert.Equal(t, "call_42", task.ToolUseID)
}

// --- Output persistence ---

type memOutputStore struct {
	mu    sync.Mutex
	files map[string]string
}

func (s *memOutputStore) Write(_ context.Context, req *filesystem.WriteRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string]string{}
	}
	s.files[req.FilePath] = req.Content
	return nil
}

func TestManager_OutputPersisted(t *testing.T) {
	store := &memOutputStore{}
	m := New(context.Background(), &Config{OutputStore: store, OutputDir: "/tasks"})
	defer closeWithTimeout(m)

	result, err := run(m, "task", false, workReturning("the output", nil))
	require.NoError(t, err)

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	wantPath := "/tasks/" + result.ID + ".output"
	assert.Equal(t, wantPath, task.OutputFile)
	assert.Equal(t, "the output", store.files[wantPath])
}

func TestManager_NoOutputStore_NoOutputFile(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "task", false, workReturning("the output", nil))
	require.NoError(t, err)

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.Empty(t, task.OutputFile)
	assert.Equal(t, "the output", task.Result)
}

// --- Auto-background Tests ---

// allowBackground is a ShouldAutoBackground hook that permits backgrounding any run.
func allowBackground(context.Context, *Task) bool { return true }

func TestManager_AutoBackground_Slow(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(50), ShouldAutoBackground: allowBackground})
	defer closeWithTimeout(m)

	result, err := run(m, "slow task", false, workSleeping(200*time.Millisecond, "slow result"))
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)
	assert.True(t, anyRunning(m))

	task := waitTask(t, m, result.ID)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "slow result", task.Result)
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
	assert.Equal(t, StatusRunning, result.Status) // moved to background at 50ms
	assert.True(t, anyRunning(m))

	task := waitTask(t, m, result.ID)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "slow result", task.Result)
}

// With no ShouldAutoBackground hook (the default), a run that hits its deadline is
// canceled and reported as timed out — not backgrounded.
func TestManager_DeadlineKillsWhenNotBackgroundable(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(50)}) // no hook
	defer closeWithTimeout(m)

	result, err := run(m, "slow task", false, workBlocking())
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Contains(t, result.Error, "timed out")

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.Equal(t, StatusFailed, task.Status)
	assert.False(t, anyRunning(m)) // work was canceled
}

// The hook receives the task so the business can decide per-run; here it backgrounds
// only tasks whose description marks them as a server.
func TestManager_ShouldAutoBackgroundPerTask(t *testing.T) {
	m := New(context.Background(), &Config{
		ForegroundTimeoutMs: intPtr(40),
		ShouldAutoBackground: func(_ context.Context, task *Task) bool {
			return task.Description == "server"
		},
	})
	defer closeWithTimeout(m)

	bg, err := run(m, "server", false, workSleeping(150*time.Millisecond, "up"))
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, bg.Status) // backgrounded

	killed, err := run(m, "oneshot", false, workBlocking())
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, killed.Status) // timed out
	assert.Contains(t, killed.Error, "timed out")

	waitTask(t, m, bg.ID)
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
	assert.Equal(t, StatusCompleted, result.Status) // blocked despite the 20ms default
	assert.Equal(t, "done", result.Result)
}

func TestManager_AutoBackground_Fast(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(5000)})
	defer closeWithTimeout(m)

	result, err := run(m, "fast task", false, workReturning("fast result", nil))
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, result.Status)
	assert.Equal(t, "fast result", result.Result)
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

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.Equal(t, result.ID, task.ID)
	assert.Equal(t, "test task", task.Description)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "done", task.Result)
	assert.NotNil(t, task.DoneAt)
}

func TestManager_Metadata(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	md := map[string]any{"toolCallID": "call_42", "session": "s1"}
	result, err := m.Run(context.Background(), &RunInput{
		Description: "task",
		Metadata:    md,
	}, workReturning("done", nil))
	require.NoError(t, err)

	// Metadata flows to the tracked task, visible via Get.
	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.Equal(t, "call_42", task.Metadata["toolCallID"])
	assert.Equal(t, "s1", task.Metadata["session"])

	// Mutating the caller's original map must not affect the recorded task.
	md["toolCallID"] = "mutated"
	task, _ = m.Get(result.ID)
	assert.Equal(t, "call_42", task.Metadata["toolCallID"])
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
		byID[task.ID] = task
	}
	assert.Equal(t, StatusCompleted, byID[r1.ID].Status)
	assert.Equal(t, StatusCompleted, byID[r2.ID].Status)
}

// --- Cancel Tests ---

func TestManager_Cancel(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "cancellable", true, workBlocking())
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)

	err = m.Cancel(result.ID)
	require.NoError(t, err)

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.Equal(t, StatusCanceled, task.Status)
	assert.NotNil(t, task.DoneAt)
	// A canceled task carries a reason rather than an empty terminal state.
	assert.Equal(t, canceledError, task.Error)
}

// A foreground run stopped by Cancel reports StatusCanceled (with the cancel
// reason) back to the caller, not StatusFailed from the work's ctx-canceled error.
func TestManager_Cancel_ForegroundReportsCanceled(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(0)})
	defer closeWithTimeout(m)

	started := make(chan string, 1)
	go func() {
		id := <-started
		_ = m.Cancel(id)
	}()

	result, err := m.Run(context.Background(), &RunInput{Description: "fg cancelable"},
		func(ctx context.Context) (string, error) {
			// Surface the task id to the canceller, then block until canceled.
			for _, t := range m.List() {
				started <- t.ID
			}
			<-ctx.Done()
			return "", ctx.Err()
		})
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, result.Status)
	assert.Equal(t, canceledError, result.Error)
}

func TestManager_CancelNotFound(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	err := m.Cancel("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to stop")
}

func TestManager_CancelAlreadyDone(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, _ := run(m, "task", false, workReturning("done", nil))

	err := m.Cancel(result.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already finished")
}

// --- Running-state transitions ---

func TestManager_RunningState(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	assert.False(t, anyRunning(m))

	result, _ := run(m, "task", true, workBlocking())
	assert.True(t, anyRunning(m))

	_ = m.Cancel(result.ID)
	waitTask(t, m, result.ID)
	assert.False(t, anyRunning(m))
}

// --- Wait ---

func TestManager_WaitCompleted(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "task", true, workSleeping(50*time.Millisecond, "r1"))
	require.NoError(t, err)

	task := waitTask(t, m, result.ID)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "r1", task.Result)
}

func TestManager_WaitTimeout(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "task", true, workBlocking())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	task, done := m.Wait(ctx, result.ID)
	require.NotNil(t, task)
	assert.False(t, done)
	assert.Equal(t, StatusRunning, task.Status)
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
			assert.Equal(t, StatusCompleted, result.Status)
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
		assert.False(t, ids[result.ID], "duplicate ID: %s", result.ID)
		ids[result.ID] = true
	}
}

// --- RunInBackground flag ---

func TestManager_RunInBackground_Foreground(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "fg task", false, workReturning("done", nil))
	require.NoError(t, err)

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.False(t, task.RunInBackground)
}

func TestManager_RunInBackground_Background(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	result, err := run(m, "bg task", true, workSleeping(50*time.Millisecond, "bg done"))
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.True(t, task.RunInBackground)

	waitTask(t, m, result.ID)
}

var errSentinel = errors.New("sentinel")

func TestManager_ContextCancelStopsWork(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	started := make(chan struct{})
	work := func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		return "", errSentinel
	}

	result, err := run(m, "task", true, work)
	require.NoError(t, err)
	<-started

	require.NoError(t, m.Cancel(result.ID))
	waitTask(t, m, result.ID)

	task, ok := m.Get(result.ID)
	require.True(t, ok)
	assert.Equal(t, StatusCanceled, task.Status)
}
