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

package subagent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// closeWithTimeout closes the TaskMgr with a short timeout to avoid blocking on uncompleted tasks.
func closeWithTimeout(mgr *TaskMgr) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = mgr.Close(ctx)
}

func intPtr(v int) *int { return &v }

// simpleAgent is a test helper implementing adk.Agent for TaskMgr tests.
type simpleAgent struct {
	name    string
	desc    string
	result  string
	err     error
	runFunc func(ctx context.Context) (string, error)
}

func (s *simpleAgent) Name(_ context.Context) string {
	if s.name != "" {
		return s.name
	}
	return "simple"
}

func (s *simpleAgent) Description(_ context.Context) string {
	if s.desc != "" {
		return s.desc
	}
	return "simple agent"
}

func (s *simpleAgent) Run(ctx context.Context, input *adk.AgentInput, options ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()

	var result string
	var err error
	if s.runFunc != nil {
		result, err = s.runFunc(ctx)
	} else if s.err != nil {
		err = s.err
	} else {
		result = s.result
	}

	if err != nil {
		gen.Send(&adk.AgentEvent{Err: err})
	} else {
		gen.Send(adk.EventFromMessage(schema.UserMessage(result), nil, schema.User, ""))
	}
	gen.Close()
	return iter
}

// registerAndRun is a test helper that registers the agent and runs it in one call.
func registerAndRun(mgr *TaskMgr, agent *simpleAgent, description string, background bool) (*RunResult, error) {
	name := agent.Name(context.Background())
	mgr.RegisterAgent(name, agent)
	return mgr.Run(context.Background(), &RunInput{
		SubagentType:    name,
		Prompt:          "test prompt",
		Description:     description,
		RunInBackground: background,
	})
}

// --- Run (foreground) Tests ---

func TestTaskMgr_RunForeground(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{name: "a1", result: "hello"}
	result, err := registerAndRun(mgr, agent, "test task", false)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, result.Status)
	assert.Equal(t, "hello", result.Result)
	assert.NotEmpty(t, result.TaskID)
}

func TestTaskMgr_RunForegroundError(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{name: "a1", err: fmt.Errorf("something failed")}
	result, err := registerAndRun(mgr, agent, "failing task", false)
	require.NoError(t, err) // Run itself doesn't error
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, "something failed", result.Error)
}

// --- Run (background) Tests ---

func TestTaskMgr_RunBackground(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	ch := mgr.Subscribe()

	agent := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			time.Sleep(50 * time.Millisecond)
			return "bg result", nil
		},
	}

	result, err := registerAndRun(mgr, agent, "bg task", true)
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)
	assert.NotEmpty(t, result.TaskID)

	// Task should be running.
	assert.True(t, mgr.HasRunning())

	// Running notification should arrive with Events iterator.
	select {
	case n := <-ch:
		assert.Equal(t, StatusRunning, n.Task.Status)
		assert.NotNil(t, n.Events)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for running notification")
	}

	// Wait for completion.
	err = mgr.WaitAllDone(context.Background())
	require.NoError(t, err)

	// Completed notification.
	select {
	case n := <-ch:
		assert.Equal(t, StatusCompleted, n.Task.Status)
		assert.Equal(t, "bg result", n.Task.Result)
		assert.Nil(t, n.Events)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completed notification")
	}
}

// --- Auto-background Tests ---

func TestTaskMgr_AutoBackground_Slow(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{AutoBackgroundMs: intPtr(50)})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{
		name: "slow",
		runFunc: func(ctx context.Context) (string, error) {
			time.Sleep(200 * time.Millisecond)
			return "slow result", nil
		},
	}

	result, err := registerAndRun(mgr, agent, "slow task", false)
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)
	assert.True(t, mgr.HasRunning())

	err = mgr.WaitAllDone(context.Background())
	require.NoError(t, err)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, StatusCompleted, tasks[0].Status)
	assert.Equal(t, "slow result", tasks[0].Result)
}

func TestTaskMgr_AutoBackground_Fast(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{AutoBackgroundMs: intPtr(5000)})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{name: "fast", result: "fast result"}

	result, err := registerAndRun(mgr, agent, "fast task", false)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, result.Status)
	assert.Equal(t, "fast result", result.Result)
	assert.False(t, mgr.HasRunning())
}

// --- Run with unregistered agent ---

func TestTaskMgr_RunUnregistered(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	_, err := mgr.Run(context.Background(), &RunInput{
		SubagentType: "nonexistent",
		Prompt:       "test",
		Description:  "test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")
}

// --- Get/List Tests ---

func TestTaskMgr_GetNotFound(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	task, ok := mgr.Get("nonexistent")
	assert.False(t, ok)
	assert.Nil(t, task)
}

func TestTaskMgr_Get(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{name: "a1", result: "done"}
	result, err := registerAndRun(mgr, agent, "test task", false)
	require.NoError(t, err)

	task, ok := mgr.Get(result.TaskID)
	require.True(t, ok)
	assert.Equal(t, result.TaskID, task.ID)
	assert.Equal(t, "test task", task.Description)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "done", task.Result)
	assert.NotNil(t, task.DoneAt)
}

func TestTaskMgr_List(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	a1 := &simpleAgent{name: "a1", result: "r1"}
	a2 := &simpleAgent{name: "a2", result: "r2"}

	r1, _ := registerAndRun(mgr, a1, "task1", false)
	r2, _ := registerAndRun(mgr, a2, "task2", false)

	tasks := mgr.List()
	assert.Len(t, tasks, 2)

	byID := make(map[string]*Task)
	for _, task := range tasks {
		byID[task.ID] = task
	}
	assert.Equal(t, StatusCompleted, byID[r1.TaskID].Status)
	assert.Equal(t, StatusCompleted, byID[r2.TaskID].Status)
}

// --- Cancel Tests ---

func TestTaskMgr_Cancel(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}

	result, err := registerAndRun(mgr, agent, "cancellable", true)
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)

	err = mgr.Cancel(result.TaskID)
	require.NoError(t, err)

	task, ok := mgr.Get(result.TaskID)
	require.True(t, ok)
	assert.Equal(t, StatusCanceled, task.Status)
	assert.NotNil(t, task.DoneAt)
}

func TestTaskMgr_CancelNotFound(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	err := mgr.Cancel("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTaskMgr_CancelAlreadyDone(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{name: "a1", result: "done"}
	result, _ := registerAndRun(mgr, agent, "task", false)

	err := mgr.Cancel(result.TaskID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
}

// --- Notifications ---

func TestTaskMgr_Notifications(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	ch := mgr.Subscribe()

	a1 := &simpleAgent{name: "a1", result: "r1"}
	a2 := &simpleAgent{name: "a2", err: fmt.Errorf("e2")}

	r1, _ := registerAndRun(mgr, a1, "task1", false)
	r2, _ := registerAndRun(mgr, a2, "task2", false)

	// 2 tasks × 2 notifications (running + terminal) = 4.
	var notifications []*Notification
	timeout := time.After(time.Second)
	for i := 0; i < 4; i++ {
		select {
		case n := <-ch:
			notifications = append(notifications, n)
		case <-timeout:
			t.Fatal("timed out waiting for notifications")
		}
	}

	assert.Len(t, notifications, 4)

	// Verify both task IDs appear in terminal notifications.
	var terminalIDs []string
	for _, n := range notifications {
		if n.Task.Status != StatusRunning {
			terminalIDs = append(terminalIDs, n.Task.ID)
		}
	}
	assert.Len(t, terminalIDs, 2)
	ids := map[string]bool{terminalIDs[0]: true, terminalIDs[1]: true}
	assert.True(t, ids[r1.TaskID])
	assert.True(t, ids[r2.TaskID])
}

func TestTaskMgr_CancelNotification(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	ch := mgr.Subscribe()

	agent := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}

	result, _ := registerAndRun(mgr, agent, "task", true)

	// Consume running notification.
	select {
	case n := <-ch:
		assert.Equal(t, StatusRunning, n.Task.Status)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for running notification")
	}

	_ = mgr.Cancel(result.TaskID)

	select {
	case n := <-ch:
		assert.Equal(t, StatusCanceled, n.Task.Status)
		assert.Equal(t, result.TaskID, n.Task.ID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancel notification")
	}
}

// --- HasRunning ---

func TestTaskMgr_HasRunning(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	assert.False(t, mgr.HasRunning())

	agent := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	result, _ := registerAndRun(mgr, agent, "task", true)
	assert.True(t, mgr.HasRunning())

	_ = mgr.Cancel(result.TaskID)
	_ = mgr.WaitAllDone(context.Background())
	assert.False(t, mgr.HasRunning())
}

// --- WaitAllDone ---

func TestTaskMgr_WaitAllDone(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	a1 := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			time.Sleep(50 * time.Millisecond)
			return "r1", nil
		},
	}
	a2 := &simpleAgent{
		name: "a2",
		runFunc: func(ctx context.Context) (string, error) {
			time.Sleep(100 * time.Millisecond)
			return "r2", nil
		},
	}

	_, _ = registerAndRun(mgr, a1, "task1", true)
	_, _ = registerAndRun(mgr, a2, "task2", true)

	err := mgr.WaitAllDone(context.Background())
	assert.NoError(t, err)
	assert.False(t, mgr.HasRunning())
}

func TestTaskMgr_WaitAllDoneTimeout(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	_, _ = registerAndRun(mgr, agent, "task", true)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := mgr.WaitAllDone(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestTaskMgr_WaitAllDoneNoTasks(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	err := mgr.WaitAllDone(context.Background())
	assert.NoError(t, err)
}

// --- Close ---

func TestTaskMgr_Close(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})

	agent := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	_, _ = registerAndRun(mgr, agent, "task", true)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := mgr.Close(ctx)
	assert.NoError(t, err)

	// Run after close should fail.
	_, err = mgr.Run(context.Background(), &RunInput{
		SubagentType: "a1",
		Prompt:       "test",
		Description:  "new",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestTaskMgr_RunAfterClose(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	_ = mgr.Close(context.Background())

	agent := &simpleAgent{name: "a1", result: "x"}
	mgr.RegisterAgent("a1", agent)
	_, err := mgr.Run(context.Background(), &RunInput{
		SubagentType: "a1",
		Prompt:       "test",
		Description:  "task",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

// --- Concurrency ---

func TestTaskMgr_ConcurrentRuns(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	const n = 50
	// Register all agents first.
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("agent-%d", i)
		mgr.RegisterAgent(name, &simpleAgent{name: name, result: fmt.Sprintf("result-%d", i)})
	}

	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			result, err := mgr.Run(context.Background(), &RunInput{
				SubagentType: fmt.Sprintf("agent-%d", i),
				Prompt:       "test",
				Description:  fmt.Sprintf("task-%d", i),
			})
			require.NoError(t, err)
			assert.Equal(t, StatusCompleted, result.Status)
		}(i)
	}

	wg.Wait()
	assert.False(t, mgr.HasRunning())
	assert.Len(t, mgr.List(), n)
}

// --- Unique IDs ---

func TestTaskMgr_UniqueIDs(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{name: "a1", result: "x"}
	mgr.RegisterAgent("a1", agent)

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		result, err := mgr.Run(context.Background(), &RunInput{
			SubagentType: "a1",
			Prompt:       "test",
			Description:  "task",
		})
		require.NoError(t, err)
		assert.False(t, ids[result.TaskID], "duplicate ID: %s", result.TaskID)
		ids[result.TaskID] = true
	}
}

// --- RunInBackground ---

func TestTaskMgr_RunInBackground_Foreground(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{name: "a1", result: "done"}
	result, err := registerAndRun(mgr, agent, "fg task", false)
	require.NoError(t, err)

	task, ok := mgr.Get(result.TaskID)
	require.True(t, ok)
	assert.False(t, task.RunInBackground)
}

func TestTaskMgr_RunInBackground_Background(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			time.Sleep(50 * time.Millisecond)
			return "bg done", nil
		},
	}
	result, err := registerAndRun(mgr, agent, "bg task", true)
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, result.Status)

	task, ok := mgr.Get(result.TaskID)
	require.True(t, ok)
	assert.True(t, task.RunInBackground)

	_ = mgr.WaitAllDone(context.Background())
}

// --- MarkQueried / ResultQueried ---

func TestTaskMgr_MarkQueried(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	agent := &simpleAgent{name: "a1", result: "done"}
	result, err := registerAndRun(mgr, agent, "task", false)
	require.NoError(t, err)

	// Before marking, ResultQueried should be false.
	task, ok := mgr.Get(result.TaskID)
	require.True(t, ok)
	assert.False(t, task.ResultQueried)

	// Mark as queried.
	mgr.MarkQueried(result.TaskID)

	// After marking, ResultQueried should be true.
	task, ok = mgr.Get(result.TaskID)
	require.True(t, ok)
	assert.True(t, task.ResultQueried)
}

func TestTaskMgr_MarkQueried_NonExistent(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	// Should not panic on non-existent ID.
	mgr.MarkQueried("nonexistent")
}

// --- Events Streaming ---

func TestTaskMgr_EventsStreaming(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer closeWithTimeout(mgr)

	ch := mgr.Subscribe()

	agent := &simpleAgent{
		name: "a1",
		runFunc: func(ctx context.Context) (string, error) {
			time.Sleep(50 * time.Millisecond)
			return "streamed result", nil
		},
	}

	_, err := registerAndRun(mgr, agent, "streaming task", true)
	require.NoError(t, err)

	// Running notification should carry an Events iterator.
	var events *adk.AsyncIterator[*adk.AgentEvent]
	select {
	case n := <-ch:
		require.Equal(t, StatusRunning, n.Task.Status)
		require.NotNil(t, n.Events)
		events = n.Events
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for running notification")
	}

	// Consume events from the iterator.
	var agentEvents []*adk.AgentEvent
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		agentEvents = append(agentEvents, event)
	}

	// The mock agent emits one event with the result message.
	require.NotEmpty(t, agentEvents)

	// Completed notification should have nil Events.
	select {
	case n := <-ch:
		assert.Equal(t, StatusCompleted, n.Task.Status)
		assert.Nil(t, n.Events)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completed notification")
	}
}
