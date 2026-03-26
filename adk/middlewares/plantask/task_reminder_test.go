/*
 * Copyright 2025 CloudWeGo Authors
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

package plantask

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestComputeTurnStats_EmptyMessages(t *testing.T) {
	stats := computeTurnStats(nil)
	assert.Equal(t, 0, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 0, stats.turnsSinceLastReminder)

	stats = computeTurnStats([]adk.Message{})
	assert.Equal(t, 0, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 0, stats.turnsSinceLastReminder)
}

func TestComputeTurnStats_NoTaskWriteToolsNoReminders(t *testing.T) {
	messages := []adk.Message{
		schema.UserMessage("hello"),
		schema.AssistantMessage("hi there", nil),
		schema.UserMessage("do something"),
		schema.AssistantMessage("sure", nil),
		schema.UserMessage("more"),
		schema.AssistantMessage("done", nil),
	}
	stats := computeTurnStats(messages)
	assert.Equal(t, 3, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 3, stats.turnsSinceLastReminder)
}

func TestComputeTurnStats_WithTaskCreateToolCall(t *testing.T) {
	messages := []adk.Message{
		schema.UserMessage("hello"),
		schema.AssistantMessage("creating task", []schema.ToolCall{
			{Function: schema.FunctionCall{Name: TaskCreateToolName}},
		}),
		schema.UserMessage("next"),
		schema.AssistantMessage("working", nil),
		schema.UserMessage("more"),
		schema.AssistantMessage("done", nil),
	}
	stats := computeTurnStats(messages)
	assert.Equal(t, 2, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 3, stats.turnsSinceLastReminder)
}

func TestComputeTurnStats_WithTaskUpdateToolCall(t *testing.T) {
	messages := []adk.Message{
		schema.UserMessage("hello"),
		schema.AssistantMessage("updating task", []schema.ToolCall{
			{Function: schema.FunctionCall{Name: TaskUpdateToolName}},
		}),
		schema.UserMessage("next"),
		schema.AssistantMessage("working", nil),
	}
	stats := computeTurnStats(messages)
	assert.Equal(t, 1, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 2, stats.turnsSinceLastReminder)
}

func TestComputeTurnStats_WithTaskReminderMessage(t *testing.T) {
	reminderMsg := &schema.Message{
		Role:    schema.User,
		Content: "reminder content",
		Extra:   map[string]any{extraKeyTaskReminder: true},
	}
	messages := []adk.Message{
		schema.UserMessage("hello"),
		schema.AssistantMessage("hi", nil),
		reminderMsg,
		schema.AssistantMessage("ok", nil),
		schema.UserMessage("more"),
		schema.AssistantMessage("done", nil),
	}
	stats := computeTurnStats(messages)
	assert.Equal(t, 3, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 2, stats.turnsSinceLastReminder)
}

func TestComputeTurnStats_MixedToolCallsAndReminders(t *testing.T) {
	reminderMsg := &schema.Message{
		Role:    schema.User,
		Content: "reminder",
		Extra:   map[string]any{extraKeyTaskReminder: true},
	}
	messages := []adk.Message{
		schema.UserMessage("hello"),
		schema.AssistantMessage("creating", []schema.ToolCall{
			{Function: schema.FunctionCall{Name: TaskCreateToolName}},
		}),
		reminderMsg,
		schema.AssistantMessage("working", nil),
		schema.UserMessage("next"),
		schema.AssistantMessage("updating", []schema.ToolCall{
			{Function: schema.FunctionCall{Name: TaskUpdateToolName}},
		}),
		schema.UserMessage("continue"),
		schema.AssistantMessage("final", nil),
	}
	stats := computeTurnStats(messages)
	assert.Equal(t, 1, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 3, stats.turnsSinceLastReminder)
}

func TestComputeTurnStats_NilMessagesSkipped(t *testing.T) {
	messages := []adk.Message{
		nil,
		schema.AssistantMessage("hi", nil),
		nil,
		schema.AssistantMessage("done", nil),
		nil,
	}
	stats := computeTurnStats(messages)
	assert.Equal(t, 2, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 2, stats.turnsSinceLastReminder)
}

func TestComputeTurnStats_TaskWriteAtEnd(t *testing.T) {
	messages := []adk.Message{
		schema.UserMessage("hello"),
		schema.AssistantMessage("creating", []schema.ToolCall{
			{Function: schema.FunctionCall{Name: TaskCreateToolName}},
		}),
	}
	stats := computeTurnStats(messages)
	assert.Equal(t, 0, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 1, stats.turnsSinceLastReminder)
}

func TestComputeTurnStats_NonTaskToolCallsIgnored(t *testing.T) {
	messages := []adk.Message{
		schema.UserMessage("hello"),
		schema.AssistantMessage("using other tool", []schema.ToolCall{
			{Function: schema.FunctionCall{Name: "SomeOtherTool"}},
		}),
		schema.AssistantMessage("done", nil),
	}
	stats := computeTurnStats(messages)
	assert.Equal(t, 2, stats.turnsSinceLastTaskManagement)
	assert.Equal(t, 2, stats.turnsSinceLastReminder)
}

func TestHasTaskUpdateTool(t *testing.T) {
	assert.False(t, hasTaskUpdateTool(nil))
	assert.False(t, hasTaskUpdateTool([]*schema.ToolInfo{}))
	assert.False(t, hasTaskUpdateTool([]*schema.ToolInfo{
		{Name: "TaskCreate"},
		{Name: "TaskList"},
	}))
	assert.True(t, hasTaskUpdateTool([]*schema.ToolInfo{
		{Name: "TaskCreate"},
		{Name: TaskUpdateToolName},
		{Name: "TaskList"},
	}))
	assert.True(t, hasTaskUpdateTool([]*schema.ToolInfo{
		{Name: TaskUpdateToolName},
	}))
}

func TestFormatTaskList_Empty(t *testing.T) {
	result := formatTaskList(nil)
	assert.Equal(t, "", result)

	result = formatTaskList([]*task{})
	assert.Equal(t, "", result)
}

func TestFormatTaskList_WithTasks(t *testing.T) {
	tasks := []*task{
		{ID: "1", Status: "pending", Subject: "First task"},
		{ID: "2", Status: "in_progress", Subject: "Second task", Owner: "agent1"},
		{ID: "3", Status: "completed", Subject: "Third task"},
	}
	result := formatTaskList(tasks)
	assert.Contains(t, result, "Here are the existing tasks:")
	assert.Contains(t, result, "#1. [pending] First task")
	assert.Contains(t, result, "#2. [in_progress] Second task [owner: agent1]")
	assert.Contains(t, result, "#3. [completed] Third task")
	assert.NotContains(t, result, "#3. [completed] Third task [owner:")
}

func TestBeforeModelRewriteState_NotTeamMode(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	m := testMiddleware(backend, "/tmp/tasks")

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{schema.UserMessage("hello")},
	}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, state, resultState)
}

func TestBeforeModelRewriteState_ReminderIntervalZero(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	m := testMiddleware(backend, "/tmp/tasks")
	m.taskBaseDirResolver = func(ctx context.Context) string { return "/tmp/tasks" }
	m.reminderInterval = 0

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{schema.UserMessage("hello")},
	}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, state, resultState)
}

func TestBeforeModelRewriteState_NegativeInterval(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	m := testMiddleware(backend, "/tmp/tasks")
	m.taskBaseDirResolver = func(ctx context.Context) string { return "/tmp/tasks" }
	m.reminderInterval = -1

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{schema.UserMessage("hello")},
	}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, state, resultState)
}

func TestBeforeModelRewriteState_EmptyMessages(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	m := testMiddleware(backend, "/tmp/tasks")
	m.taskBaseDirResolver = func(ctx context.Context) string { return "/tmp/tasks" }
	m.reminderInterval = 2

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{},
	}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, state, resultState)
}

func TestBeforeModelRewriteState_NoTaskUpdateTool(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	m := testMiddleware(backend, "/tmp/tasks")
	m.taskBaseDirResolver = func(ctx context.Context) string { return "/tmp/tasks" }
	m.reminderInterval = 2

	messages := make([]adk.Message, 0)
	for i := 0; i < 5; i++ {
		messages = append(messages, schema.UserMessage("q"))
		messages = append(messages, schema.AssistantMessage("a", nil))
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: "TaskCreate"}, {Name: "TaskList"}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, state, resultState)
}

func TestBeforeModelRewriteState_StatsBelowThreshold(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	m := testMiddleware(backend, "/tmp/tasks")
	m.taskBaseDirResolver = func(ctx context.Context) string { return "/tmp/tasks" }
	m.reminderInterval = 10

	messages := []adk.Message{
		schema.UserMessage("hello"),
		schema.AssistantMessage("hi", nil),
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, state, resultState)
}

func TestBeforeModelRewriteState_InjectsReminder(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	m := testMiddleware(backend, baseDir)
	m.taskBaseDirResolver = func(ctx context.Context) string { return baseDir }
	m.reminderInterval = 3

	messages := make([]adk.Message, 0)
	for i := 0; i < 4; i++ {
		messages = append(messages, schema.UserMessage("q"))
		messages = append(messages, schema.AssistantMessage("a", nil))
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, len(messages)+1, len(resultState.Messages))

	lastMsg := resultState.Messages[len(resultState.Messages)-1]
	assert.Equal(t, schema.User, lastMsg.Role)
	assert.NotEmpty(t, lastMsg.Content)
	assert.NotNil(t, lastMsg.Extra)
	_, ok := lastMsg.Extra[extraKeyTaskReminder]
	assert.True(t, ok)

	assert.Equal(t, len(messages), len(state.Messages))
}

func TestBeforeModelRewriteState_InjectsReminderWithTaskList(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	m := testMiddleware(backend, baseDir)
	m.taskBaseDirResolver = func(ctx context.Context) string { return baseDir }
	m.reminderInterval = 2

	taskData := &task{
		ID:      "1",
		Subject: "Test task",
		Status:  taskStatusPending,
		Blocks:  []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	messages := make([]adk.Message, 0)
	for i := 0; i < 3; i++ {
		messages = append(messages, schema.UserMessage("q"))
		messages = append(messages, schema.AssistantMessage("a", nil))
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, len(messages)+1, len(resultState.Messages))

	lastMsg := resultState.Messages[len(resultState.Messages)-1]
	assert.Contains(t, lastMsg.Content, "#1. [pending] Test task")
}

func TestBeforeModelRewriteState_WithOnReminderCallback(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	m := testMiddleware(backend, baseDir)
	m.taskBaseDirResolver = func(ctx context.Context) string { return baseDir }
	m.reminderInterval = 2

	var callbackCalled bool
	var callbackText string
	m.onReminder = func(ctx context.Context, text string) {
		callbackCalled = true
		callbackText = text
	}

	messages := make([]adk.Message, 0)
	for i := 0; i < 3; i++ {
		messages = append(messages, schema.UserMessage("q"))
		messages = append(messages, schema.AssistantMessage("a", nil))
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.True(t, callbackCalled)
	assert.NotEmpty(t, callbackText)
	assert.Equal(t, state, resultState)
	assert.Equal(t, len(messages), len(resultState.Messages))
}

func TestBeforeModelRewriteState_ListTasksErrorStillWorks(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/nonexistent/path"
	m := testMiddleware(backend, baseDir)
	m.taskBaseDirResolver = func(ctx context.Context) string { return baseDir }
	m.reminderInterval = 2

	messages := make([]adk.Message, 0)
	for i := 0; i < 3; i++ {
		messages = append(messages, schema.UserMessage("q"))
		messages = append(messages, schema.AssistantMessage("a", nil))
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, len(messages)+1, len(resultState.Messages))

	lastMsg := resultState.Messages[len(resultState.Messages)-1]
	assert.Equal(t, schema.User, lastMsg.Role)
	assert.NotNil(t, lastMsg.Extra)
	_, ok := lastMsg.Extra[extraKeyTaskReminder]
	assert.True(t, ok)
	assert.NotContains(t, lastMsg.Content, "Here are the existing tasks:")
}

func TestBeforeModelRewriteState_InternalTasksFilteredInReminder(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	m := testMiddleware(backend, baseDir)
	m.taskBaseDirResolver = func(ctx context.Context) string { return baseDir }
	m.reminderInterval = 2

	visibleTask := &task{
		ID:      "1",
		Subject: "Visible task",
		Status:  taskStatusPending,
		Blocks:  []string{},
	}
	visibleJSON, _ := sonic.MarshalString(visibleTask)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: visibleJSON})

	internalTask := &task{
		ID:       "2",
		Subject:  "Internal task",
		Status:   taskStatusInProgress,
		Blocks:   []string{},
		Metadata: map[string]any{MetadataKeyInternal: true},
	}
	internalJSON, _ := sonic.MarshalString(internalTask)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: internalJSON})

	messages := make([]adk.Message, 0)
	for i := 0; i < 3; i++ {
		messages = append(messages, schema.UserMessage("q"))
		messages = append(messages, schema.AssistantMessage("a", nil))
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)

	lastMsg := resultState.Messages[len(resultState.Messages)-1]
	assert.Contains(t, lastMsg.Content, "Visible task")
	assert.NotContains(t, lastMsg.Content, "Internal task")
}

func TestBeforeModelRewriteState_RecentTaskWriteResetsCounter(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	m := testMiddleware(backend, baseDir)
	m.taskBaseDirResolver = func(ctx context.Context) string { return baseDir }
	m.reminderInterval = 3

	messages := []adk.Message{
		schema.UserMessage("q"),
		schema.AssistantMessage("a", nil),
		schema.UserMessage("q"),
		schema.AssistantMessage("creating", []schema.ToolCall{
			{Function: schema.FunctionCall{Name: TaskCreateToolName}},
		}),
		schema.UserMessage("q"),
		schema.AssistantMessage("a", nil),
		schema.UserMessage("q"),
		schema.AssistantMessage("a", nil),
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, state, resultState)
}

func TestBeforeModelRewriteState_RecentReminderResetsCounter(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	m := testMiddleware(backend, baseDir)
	m.taskBaseDirResolver = func(ctx context.Context) string { return baseDir }
	m.reminderInterval = 3

	reminderMsg := &schema.Message{
		Role:    schema.User,
		Content: "reminder",
		Extra:   map[string]any{extraKeyTaskReminder: true},
	}
	messages := []adk.Message{
		schema.UserMessage("q"),
		schema.AssistantMessage("a", nil),
		schema.UserMessage("q"),
		schema.AssistantMessage("a", nil),
		reminderMsg,
		schema.AssistantMessage("a", nil),
		schema.UserMessage("q"),
		schema.AssistantMessage("a", nil),
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	mc := &adk.ModelContext{
		Tools: []*schema.ToolInfo{{Name: TaskUpdateToolName}},
	}

	_, resultState, err := m.BeforeModelRewriteState(ctx, state, mc)
	assert.NoError(t, err)
	assert.Equal(t, state, resultState)
}
