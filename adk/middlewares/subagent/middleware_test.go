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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// --- Mock Agent ---

type mockAgent struct {
	name string
	desc string
	// runFunc allows custom behavior in Run.
	runFunc func(ctx context.Context, input *adk.AgentInput) string
}

func (m *mockAgent) Name(_ context.Context) string {
	return m.name
}

func (m *mockAgent) Description(_ context.Context) string {
	return m.desc
}

func (m *mockAgent) Run(ctx context.Context, input *adk.AgentInput, options ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()

	result := m.desc // default: return description as result
	if m.runFunc != nil {
		result = m.runFunc(ctx, input)
	}

	gen.Send(adk.EventFromMessage(schema.UserMessage(result), nil, schema.User, ""))
	gen.Close()
	return iter
}

// --- Config Validation Tests ---

func TestConfigValidation_EmptySubAgents(t *testing.T) {
	_, err := New(context.Background(), &Config{
		SubAgents: nil,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestConfigValidation_DuplicateNames(t *testing.T) {
	_, err := New(context.Background(), &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "agent1", desc: "first"},
			&mockAgent{name: "agent1", desc: "second"},
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

// --- Middleware BeforeAgent Tests ---

func TestBeforeAgent_InjectsToolsAndInstruction(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "researcher", desc: "researches things"},
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base instruction",
	}

	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	// Instruction should be appended.
	assert.Contains(t, newRunCtx.Instruction, "base instruction")
	assert.Contains(t, newRunCtx.Instruction, "agent")

	// Agent tool should be injected.
	assert.Len(t, newRunCtx.Tools, 1)
}

func TestBeforeAgent_NilRunCtx(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "helper", desc: "helps"},
		},
	})
	require.NoError(t, err)

	newCtx, newRunCtx, err := mw.BeforeAgent(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, newRunCtx)
	assert.Equal(t, ctx, newCtx)
}

func TestBeforeAgent_WithTaskMgr_InjectsThreeTools(t *testing.T) {
	ctx := context.Background()
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "worker", desc: "does work"},
		},
		TaskMgr: mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
	}

	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	// Should have agent + task_output + task_stop = 3 tools.
	assert.Len(t, newRunCtx.Tools, 3)

	// Instruction should include background prompt.
	assert.Contains(t, newRunCtx.Instruction, "background")
}

func TestBeforeAgent_CustomSystemPrompt(t *testing.T) {
	ctx := context.Background()
	customPrompt := "custom prompt"
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "helper", desc: "helps"},
		},
		SystemPrompt: &customPrompt,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
	}

	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	assert.Contains(t, newRunCtx.Instruction, "custom prompt")
}

// --- Agent Tool Tests ---

func TestAgentTool_ForegroundRouting(t *testing.T) {
	ctx := context.Background()
	a1 := &mockAgent{name: "agent1", desc: "desc of agent 1"}
	a2 := &mockAgent{name: "agent2", desc: "desc of agent 2"}

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{a1, a2},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	// Get the agent tool.
	require.Len(t, newRunCtx.Tools, 1)

	// Use the tool directly.
	at := newRunCtx.Tools[0].(*typedAgentTool[*schema.Message])

	result, err := at.InvokableRun(ctx, `{"subagent_type":"agent1","prompt":"test task","description":"test"}`)
	require.NoError(t, err)
	assert.Equal(t, "desc of agent 1", result)

	result, err = at.InvokableRun(ctx, `{"subagent_type":"agent2","prompt":"test task","description":"test"}`)
	require.NoError(t, err)
	assert.Equal(t, "desc of agent 2", result)
}

func TestAgentTool_NotFound(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "agent1", desc: "desc"},
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(*typedAgentTool[*schema.Message])
	_, err = at.InvokableRun(ctx, `{"subagent_type":"nonexistent","prompt":"test","description":"test"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestAgentTool_Background(t *testing.T) {
	ctx := context.Background()
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	ch := mgr.Subscribe()

	slowAgent := &mockAgent{
		name: "slow",
		desc: "slow agent",
		runFunc: func(ctx context.Context, input *adk.AgentInput) string {
			time.Sleep(50 * time.Millisecond)
			return "slow result"
		},
	}

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{slowAgent},
		TaskMgr:   mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(*typedAgentTool[*schema.Message])
	result, err := at.InvokableRun(ctx, `{"subagent_type":"slow","prompt":"bg task detail","description":"bg task","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "launched in background")

	// Running notification.
	select {
	case n := <-ch:
		assert.Equal(t, StatusRunning, n.Task.Status)
		assert.NotNil(t, n.Events)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for running notification")
	}

	// Wait for the background task to complete.
	err = mgr.WaitAllDone(context.Background())
	require.NoError(t, err)

	// Completed notification.
	select {
	case n := <-ch:
		assert.Equal(t, StatusCompleted, n.Task.Status)
		assert.Equal(t, "slow result", n.Task.Result)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completed notification")
	}
}

func TestAgentTool_Info(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "helper", desc: "helps with tasks"},
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	info, err := newRunCtx.Tools[0].Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, agentToolName, info.Name)
	assert.Contains(t, info.Desc, "helper")
	assert.Contains(t, info.Desc, "helps with tasks")
}

func TestAgentTool_CustomName(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "helper", desc: "helps"},
		},
		ToolName: "task",
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	info, err := newRunCtx.Tools[0].Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, "task", info.Name)
}

// --- TaskOutput Tool Tests ---

func TestTaskOutputTool(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	// Create a mock agent to run through TaskMgr
	mockA := &mockAgent{name: "mock", desc: "task result"}
	mgr.RegisterAgent("mock", mockA)
	result, err := mgr.Run(context.Background(), &RunInput{
		SubagentType: "mock",
		Prompt:       "do something",
		Description:  "test task",
	})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, result.Status)

	tl, err := newTaskOutputTool(mgr)
	require.NoError(t, err)
	output, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, result.TaskID))
	require.NoError(t, err)
	assert.Contains(t, output, "test task")
	assert.Contains(t, output, "task result")
	assert.Contains(t, output, "completed")

	// Verify that TaskOutput marks the result as queried.
	task, ok := mgr.Get(result.TaskID)
	require.True(t, ok)
	assert.True(t, task.ResultQueried)
}

func TestTaskOutputTool_NotFound(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	tl, err := newTaskOutputTool(mgr)
	require.NoError(t, err)
	result, err := tl.InvokableRun(context.Background(), `{"task_id":"nonexistent"}`)
	require.NoError(t, err)
	assert.Contains(t, result, "not found")
}

// --- TaskStop Tool Tests ---

func TestTaskStopTool(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	// Start a slow background task
	slowA := &mockAgent{
		name: "slow",
		desc: "done",
		runFunc: func(ctx context.Context, input *adk.AgentInput) string {
			<-ctx.Done()
			return ""
		},
	}
	mgr.RegisterAgent("slow", slowA)
	runResult, err := mgr.Run(context.Background(), &RunInput{
		SubagentType:    "slow",
		Prompt:          "run slow",
		Description:     "running task",
		RunInBackground: true,
	})
	require.NoError(t, err)

	tl, err := newTaskStopTool(mgr)
	require.NoError(t, err)
	result, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, runResult.TaskID))
	require.NoError(t, err)
	assert.Contains(t, result, "Successfully stopped")

	// Verify the task is killed.
	task, ok := mgr.Get(runResult.TaskID)
	require.True(t, ok)
	assert.Equal(t, StatusCanceled, task.Status)
}

func TestTaskStopTool_AlreadyDone(t *testing.T) {
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	mockA := &mockAgent{name: "mock", desc: "done"}
	mgr.RegisterAgent("mock", mockA)
	runResult, err := mgr.Run(context.Background(), &RunInput{
		SubagentType: "mock",
		Prompt:       "do something",
		Description:  "done task",
	})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, runResult.Status)

	tl, err := newTaskStopTool(mgr)
	require.NoError(t, err)
	result, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, runResult.TaskID))
	require.NoError(t, err)
	assert.Contains(t, result, "Failed to stop")
}

// --- Foreground with TaskMgr tracking ---

func TestAgentTool_ForegroundWithTaskMgr(t *testing.T) {
	ctx := context.Background()
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	agent := &mockAgent{name: "fast", desc: "fast agent"}

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		TaskMgr:   mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(*typedAgentTool[*schema.Message])

	// Foreground run with TaskMgr: should block and return result.
	result, err := at.InvokableRun(ctx, `{"subagent_type":"fast","prompt":"foreground task detail","description":"foreground task"}`)
	require.NoError(t, err)
	assert.Equal(t, "fast agent", result)

	// Task should be completed in TaskMgr.
	assert.False(t, mgr.HasRunning())
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, StatusCompleted, tasks[0].Status)
	assert.Equal(t, "fast agent", tasks[0].Result)
}

// --- Auto-background ---

func TestAgentTool_AutoBackground(t *testing.T) {
	ctx := context.Background()
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{AutoBackgroundMs: intPtr(50)}) // 50ms timeout
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	slowAgent := &mockAgent{
		name: "slow",
		desc: "slow agent",
		runFunc: func(ctx context.Context, input *adk.AgentInput) string {
			time.Sleep(200 * time.Millisecond)
			return "slow result"
		},
	}

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{slowAgent},
		TaskMgr:   mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(*typedAgentTool[*schema.Message])

	// Should auto-background after 50ms since agent takes 200ms.
	result, err := at.InvokableRun(ctx, `{"subagent_type":"slow","prompt":"auto-bg task detail","description":"auto-bg task"}`)
	require.NoError(t, err)
	assert.Contains(t, result, "launched in background")

	// Task should still be running.
	assert.True(t, mgr.HasRunning())

	// Wait for completion.
	err = mgr.WaitAllDone(context.Background())
	require.NoError(t, err)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, StatusCompleted, tasks[0].Status)
	assert.Equal(t, "slow result", tasks[0].Result)
}

func TestAgentTool_AutoBackground_FastAgent(t *testing.T) {
	ctx := context.Background()
	mgr := NewTaskMgr(context.Background(), &TaskMgrConfig{AutoBackgroundMs: intPtr(5000)}) // 5s timeout, agent finishes instantly
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	fastAgent := &mockAgent{name: "fast", desc: "fast agent"}

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{fastAgent},
		TaskMgr:   mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(*typedAgentTool[*schema.Message])

	// Fast agent completes before timeout — should return foreground result.
	result, err := at.InvokableRun(ctx, `{"subagent_type":"fast","prompt":"fast task detail","description":"fast task"}`)
	require.NoError(t, err)
	assert.Equal(t, "fast agent", result)
	assert.False(t, mgr.HasRunning())
}
