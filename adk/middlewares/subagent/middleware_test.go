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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// --- Mock Agent ---

func intPtr(v int) *int { return &v }

// anyRunning reports whether the manager still has a task in StatusRunning,
// derived from the public List() snapshot.
func anyRunning(m *backgroundtask.Manager) bool {
	for _, t := range m.List() {
		if t.Status == backgroundtask.StatusRunning {
			return true
		}
	}
	return false
}

func waitAllTasks(t *testing.T, m *backgroundtask.Manager) {
	t.Helper()
	require.Eventually(t, func() bool {
		return !anyRunning(m)
	}, time.Second, 10*time.Millisecond)
}

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

func TestBeforeAgent_WithManager_InjectsAgentToolOnly(t *testing.T) {
	ctx := context.Background()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{
			&mockAgent{name: "worker", desc: "does work"},
		},
		Manager: mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
	}

	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	// Only the agent tool is injected here; task_output/task_stop are owned by
	// the backgroundtask control middleware.
	assert.Len(t, newRunCtx.Tools, 1)

	// Instruction should include the background-support prompt.
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
	at := newRunCtx.Tools[0].(tool.InvokableTool)

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

	at := newRunCtx.Tools[0].(tool.InvokableTool)
	_, err = at.InvokableRun(ctx, `{"subagent_type":"nonexistent","prompt":"test","description":"test"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestAgentTool_Background(t *testing.T) {
	ctx := context.Background()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

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
		Manager:   mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(tool.InvokableTool)
	result, err := at.InvokableRun(ctx, `{"subagent_type":"slow","prompt":"bg task detail","description":"bg task","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")
	assert.True(t, anyRunning(mgr))

	// Wait for the background task to complete, then inspect final state.
	waitAllTasks(t, mgr)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "slow result", tasks[0].Result)
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

// --- Foreground with Manager tracking ---

func TestAgentTool_ForegroundWithTaskMgr(t *testing.T) {
	ctx := context.Background()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	agent := &mockAgent{name: "fast", desc: "fast agent"}

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Manager:   mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(tool.InvokableTool)

	// Foreground run with TaskMgr: should block and return result.
	result, err := at.InvokableRun(ctx, `{"subagent_type":"fast","prompt":"foreground task detail","description":"foreground task"}`)
	require.NoError(t, err)
	assert.Equal(t, "fast agent", result)

	// Task should be completed in TaskMgr.
	assert.False(t, anyRunning(mgr))
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "fast agent", tasks[0].Result)
}

// --- Auto-background ---

func TestAgentTool_AutoBackground(t *testing.T) {
	ctx := context.Background()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{
		ForegroundTimeoutMs:  intPtr(50), // 50ms deadline
		ShouldAutoBackground: func(context.Context, *backgroundtask.Task) bool { return true },
	})
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
		Manager:   mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(tool.InvokableTool)

	// Should auto-background after 50ms since agent takes 200ms.
	result, err := at.InvokableRun(ctx, `{"subagent_type":"slow","prompt":"auto-bg task detail","description":"auto-bg task"}`)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")

	// Task should still be running.
	assert.True(t, anyRunning(mgr))

	// Wait for completion.
	waitAllTasks(t, mgr)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "slow result", tasks[0].Result)
}

func TestAgentTool_AutoBackground_FastAgent(t *testing.T) {
	ctx := context.Background()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{ForegroundTimeoutMs: intPtr(5000)}) // 5s timeout, agent finishes instantly
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	fastAgent := &mockAgent{name: "fast", desc: "fast agent"}

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{fastAgent},
		Manager:   mgr,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	at := newRunCtx.Tools[0].(tool.InvokableTool)

	// Fast agent completes before timeout — should return foreground result.
	result, err := at.InvokableRun(ctx, `{"subagent_type":"fast","prompt":"fast task detail","description":"fast task"}`)
	require.NoError(t, err)
	assert.Equal(t, "fast agent", result)
	assert.False(t, anyRunning(mgr))
}
