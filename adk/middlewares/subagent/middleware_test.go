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
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/schema/openai"
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

type stagedAgent struct {
	release   <-chan struct{}
	firstSent chan<- struct{}
}

func (s *stagedAgent) Name(context.Context) string        { return "staged" }
func (s *stagedAgent) Description(context.Context) string { return "staged agent" }
func (s *stagedAgent) Run(context.Context, *adk.AgentInput, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	stream, writer := schema.Pipe[*schema.Message](0)
	go func() {
		gen.Send(adk.EventFromMessage(nil, stream, schema.Assistant, ""))
		gen.Close()
	}()
	go func() {
		writer.Send(schema.AssistantMessage("first", nil), nil)
		close(s.firstSent)
		<-s.release
		writer.Send(schema.AssistantMessage("second", nil), nil)
		writer.Close()
	}()
	return iter
}

type outputEventRecord struct {
	Type      string         `json:"type"`
	AgentName string         `json:"agent_name"`
	Message   map[string]any `json:"message"`
}

type countingAppendStore struct {
	backend *filesystem.InMemoryBackend
	opens   int32
}

func (s *countingAppendStore) OpenAppend(ctx context.Context, req *filesystem.OpenAppendRequest) (io.WriteCloser, error) {
	atomic.AddInt32(&s.opens, 1)
	return s.backend.OpenAppend(ctx, req)
}

func decodeOutputEventRecords(t *testing.T, content string) []outputEventRecord {
	t.Helper()
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	records := make([]outputEventRecord, len(lines))
	for i, line := range lines {
		require.NoError(t, sonic.UnmarshalString(line, &records[i]))
	}
	return records
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
		Background: &BackgroundConfig{Manager: mgr},
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
		SubAgents:  []adk.Agent{slowAgent},
		Background: &BackgroundConfig{Manager: mgr},
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
		SubAgents:  []adk.Agent{agent},
		Background: &BackgroundConfig{Manager: mgr},
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

// With OutputStore and OutputDir configured, a completed managed agent run writes
// its AgentEvent output to the task's output file.
func TestAgentTool_WritesOutputFile(t *testing.T) {
	ctx := context.Background()
	backend := filesystem.NewInMemoryBackend()
	store := &countingAppendStore{backend: backend}
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{&mockAgent{name: "fast", desc: "fast agent"}},
		Background: &BackgroundConfig{
			Manager:     mgr,
			OutputStore: store,
			OutputDir:   "/tasks",
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	at := newRunCtx.Tools[0].(tool.InvokableTool)

	_, err = at.InvokableRun(ctx, `{"subagent_type":"fast","prompt":"task detail","description":"task"}`)
	require.NoError(t, err)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	path := tasks[0].OutputFile
	require.NotEmpty(t, path)

	got, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: path})
	require.NoError(t, err)
	assert.EqualValues(t, 1, atomic.LoadInt32(&store.opens), "one append session should create and write the output file")
	records := decodeOutputEventRecords(t, got.Content)
	require.Len(t, records, 1)
	assert.Equal(t, "message", records[0].Type)
	assert.Equal(t, "fast agent", records[0].Message["content"])
	assert.Equal(t, string(schema.User), records[0].Message["role"])
}

func TestAgentTool_WritesInterimEventsWithoutParentReceiver(t *testing.T) {
	ctx := context.Background()
	backend := filesystem.NewInMemoryBackend()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(closeCtx)
	}()

	release := make(chan struct{})
	firstSent := make(chan struct{})
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{&stagedAgent{release: release, firstSent: firstSent}},
		Background: &BackgroundConfig{
			Manager:     mgr,
			OutputStore: backend,
			OutputDir:   "/tasks",
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{}
	_, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	at := newRunCtx.Tools[0].(tool.InvokableTool)

	result, err := at.InvokableRun(ctx, `{"subagent_type":"staged","prompt":"task detail","description":"task","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	path := tasks[0].OutputFile
	require.NotEmpty(t, path)
	<-firstSent
	got, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: path})
	require.NoError(t, err)
	assert.Empty(t, got.Content, "a streaming AgentEvent is written after GetMessage materializes it")
	assert.True(t, anyRunning(mgr))

	close(release)
	waitAllTasks(t, mgr)
	got, err = backend.Read(ctx, &filesystem.ReadRequest{FilePath: path})
	require.NoError(t, err)
	records := decodeOutputEventRecords(t, got.Content)
	require.Len(t, records, 1)
	assert.Equal(t, "firstsecond", records[0].Message["content"])
}

func TestSanitizedMessageValue_RemovesOnlyRootExtra(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		original := &schema.Message{
			Role:    schema.Assistant,
			Content: "answer",
			Extra:   map[string]any{"not_json": func() {}},
			UserInputMultiContent: []schema.MessageInputPart{{
				Type:  schema.ChatMessagePartTypeText,
				Text:  "nested",
				Extra: map[string]any{"provider": "kept"},
			}},
		}
		value := sanitizedMessageValue(original).(adk.Message)
		assert.NotSame(t, original, value)
		assert.Nil(t, value.Extra)
		assert.Equal(t, map[string]any{"provider": "kept"}, value.UserInputMultiContent[0].Extra)
		assert.NotNil(t, original.Extra)

		data, err := sonic.Marshal(value)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"extra":{"provider":"kept"}`)
		assert.Contains(t, string(data), `"answer"`)
	})

	t.Run("agentic message", func(t *testing.T) {
		original := &schema.AgenticMessage{
			Role:  schema.AgenticRoleTypeAssistant,
			Extra: map[string]any{"not_json": func() {}},
			ResponseMeta: &schema.AgenticResponseMeta{
				TokenUsage: &schema.TokenUsage{TotalTokens: 1},
				OpenAIExtension: &openai.ResponseMetaExtension{
					ID: "response-id",
				},
				Extension: map[string]any{"provider": "kept"},
			},
			ContentBlocks: []*schema.ContentBlock{{
				Type:             schema.ContentBlockTypeAssistantGenText,
				AssistantGenText: &schema.AssistantGenText{Text: "answer"},
				Extra:            map[string]any{"provider": "kept"},
			}},
		}
		value := sanitizedMessageValue(original).(adk.AgenticMessage)
		assert.NotSame(t, original, value)
		assert.Nil(t, value.Extra)
		assert.Equal(t, map[string]any{"provider": "kept"}, value.ContentBlocks[0].Extra)
		assert.NotNil(t, original.Extra)

		data, err := sonic.Marshal(value)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"extra":{"provider":"kept"}`)
		assert.Contains(t, string(data), `"openai_extension":{"id":"response-id"}`)
		assert.Contains(t, string(data), `"extension":{"provider":"kept"}`)
		assert.Contains(t, string(data), `"token_usage"`)
		assert.Contains(t, string(data), `"answer"`)
	})
}

func TestManagedEventReceiverTransform_GatesOnlyParentReceivers(t *testing.T) {
	backgrounded := make(chan struct{})
	close(backgrounded)

	var parentEvents, taskEvents int
	transform := managedEventReceiverTransform(backgrounded, agenttool.EventReceiver[int](func(int) {
		taskEvents++
	}))
	receivers := transform([]agenttool.EventReceiver[int]{func(int) {
		parentEvents++
	}})

	require.Len(t, receivers, 2)
	for _, receiver := range receivers {
		receiver(1)
	}
	assert.Zero(t, parentEvents)
	assert.Equal(t, 1, taskEvents)
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
		SubAgents:  []adk.Agent{slowAgent},
		Background: &BackgroundConfig{Manager: mgr},
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
		SubAgents:  []adk.Agent{fastAgent},
		Background: &BackgroundConfig{Manager: mgr},
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
