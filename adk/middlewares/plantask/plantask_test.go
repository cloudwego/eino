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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestNew(t *testing.T) {
	ctx := context.Background()

	_, err := New(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")

	_, err = New(ctx, &Config{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "backend is required")

	_, err = New(ctx, &Config{Backend: newInMemoryBackend()})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "baseDir is required")

	m, err := New(ctx, &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp/tasks"})
	assert.NoError(t, err)
	assert.NotNil(t, m)
}

func TestMiddlewareBeforeAgent(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	m, err := New(ctx, &Config{Backend: backend, BaseDir: baseDir})
	assert.NoError(t, err)

	mw := m.(*typedMiddleware[*schema.Message])

	ctx, runCtx, err := mw.BeforeAgent(ctx, nil)
	assert.NoError(t, err)
	assert.Nil(t, runCtx)

	runCtx = &adk.ChatModelAgentContext{
		Tools: []tool.BaseTool{},
	}
	ctx, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	assert.NoError(t, err)
	assert.NotNil(t, newRunCtx)
	assert.Len(t, newRunCtx.Tools, 4)

	toolNames := make([]string, 0, 4)
	for _, t := range newRunCtx.Tools {
		info, _ := t.Info(ctx)
		toolNames = append(toolNames, info.Name)
	}
	assert.Contains(t, toolNames, "TaskCreate")
	assert.Contains(t, toolNames, "TaskGet")
	assert.Contains(t, toolNames, "TaskUpdate")
	assert.Contains(t, toolNames, "TaskList")
}

func TestIntegration(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	createTool := newTaskCreateTool(backend, baseDir, lock)
	getTool := newTaskGetTool(backend, baseDir, lock)
	updateTool := newTaskUpdateTool(backend, baseDir, lock)
	listTool := newTaskListTool(backend, baseDir, lock)

	result, err := createTool.InvokableRun(ctx, `{"subject": "Task 1", "description": "First task"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Task #1")

	result, err = createTool.InvokableRun(ctx, `{"subject": "Task 2", "description": "Second task"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Task #2")

	_, err = updateTool.InvokableRun(ctx, `{"taskId": "2", "addBlockedBy": ["1"]}`)
	assert.NoError(t, err)

	result, err = listTool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "#1 [pending] Task 1")
	assert.Contains(t, result, "#2 [pending] Task 2")
	assert.Contains(t, result, "[blocked by #1]")

	_, err = updateTool.InvokableRun(ctx, `{"taskId": "1", "status": "in_progress"}`)
	assert.NoError(t, err)

	result, err = getTool.InvokableRun(ctx, `{"taskId": "1"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Status: in_progress")

	_, err = updateTool.InvokableRun(ctx, `{"taskId": "1", "status": "completed"}`)
	assert.NoError(t, err)

	result, err = listTool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "#1 [completed] Task 1")
}

func TestNewTypedAgenticMessage(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &Config{
		Backend: newInMemoryBackend(),
		BaseDir: "/tmp/tasks",
	})
	assert.NoError(t, err)
	assert.NotNil(t, mw)

	var _ adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] = mw
}

// TestBeforeModelRewriteState_Generic runs the same tests for both Message and AgenticMessage
func TestBeforeModelRewriteState_Generic(t *testing.T) {
	t.Run("Message", testBeforeModelRewriteStateGeneric[*schema.Message])
	t.Run("AgenticMessage", testBeforeModelRewriteStateGeneric[*schema.AgenticMessage])
}

func testBeforeModelRewriteStateGeneric[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	defer func() {
		adk.SetLanguage(adk.LanguageEnglish)
	}()

	t.Run("nil state returns nil", func(t *testing.T) {
		backend := newInMemoryBackend()
		mw := newTypedMiddlewareForTestGeneric[M](backend)
		_, newState, err := mw.BeforeModelRewriteState(ctx, nil, nil)
		assert.NoError(t, err)
		assert.Nil(t, newState)
	})

	t.Run("empty messages returns state with empty messages", func(t *testing.T) {
		backend := newInMemoryBackend()
		mw := newTypedMiddlewareForTestGeneric[M](backend)
		state := &adk.TypedChatModelAgentState[M]{
			Messages: []M{},
		}
		_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
		assert.NoError(t, err)
		assert.NotNil(t, newState)
		assert.Len(t, newState.Messages, 0)
	})

	t.Run("no tasks in backend does not inject reminder", func(t *testing.T) {
		backend := newInMemoryBackend()
		mw := newTypedMiddlewareForTestGeneric[M](backend)
		state := &adk.TypedChatModelAgentState[M]{
			Messages: []M{makeUserMsg[M]("hello"), makeAssistantMsg[M]("hi")},
		}
		_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
		assert.NoError(t, err)
		assert.Equal(t, state.Messages, newState.Messages)
		assert.Len(t, newState.Messages, 2)
	})

	t.Run("task info in messages does not inject reminder", func(t *testing.T) {
		tests := []struct {
			name     string
			messages []M
		}{
			{
				name:     "TaskList result in messages",
				messages: []M{makeUserMsg[M]("hello"), makeTaskListResultMsg[M]("call_1")},
			},
			{
				name:     "TaskCreate call in messages",
				messages: []M{makeUserMsg[M]("create task"), makeTaskCreateCallMsg[M]("call_2")},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				backend := newInMemoryBackendWithTask(ctx)
				mw := newTypedMiddlewareForTestGeneric[M](backend)
				state := &adk.TypedChatModelAgentState[M]{
					Messages: tt.messages,
				}
				_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
				assert.NoError(t, err)
				assert.Equal(t, state.Messages, newState.Messages)
				assert.Len(t, newState.Messages, len(tt.messages))
			})
		}
	})

	t.Run("tasks exist but reminder already injected does not duplicate", func(t *testing.T) {
		backend := newInMemoryBackendWithTask(ctx)
		mw := newTypedMiddlewareForTestGeneric[M](backend)
		state := &adk.TypedChatModelAgentState[M]{
			Messages: []M{makeUserMsg[M]("hello"), makeReminderMsg[M]("reminder")},
		}
		_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
		assert.NoError(t, err)
		assert.Equal(t, state.Messages, newState.Messages)
		assert.Len(t, newState.Messages, 2)
	})

	t.Run("tasks exist injects reminder", func(t *testing.T) {
		tests := []struct {
			name        string
			language    adk.Language
			expectedMsg string
		}{
			{
				name:        "English",
				language:    adk.LanguageEnglish,
				expectedMsg: defaultTaskNotInContextTemplate,
			},
			{
				name:        "Chinese",
				language:    adk.LanguageChinese,
				expectedMsg: defaultTaskNotInContextTemplateChinese,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				adk.SetLanguage(tt.language)
				backend := newInMemoryBackendWithTask(ctx)
				mw := newTypedMiddlewareForTestGeneric[M](backend)
				state := &adk.TypedChatModelAgentState[M]{
					Messages: []M{makeUserMsg[M]("hello")},
				}
				_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
				assert.NoError(t, err)
				assert.Len(t, newState.Messages, 2)

				reminder := newState.Messages[1]
				assertIsReminderMessage(t, reminder, tt.expectedMsg)
			})
		}
	})
}

// Helper functions for BeforeModelRewriteState tests

func makeUserMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(content)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(content)).(M)
	}
	panic("unreachable")
}

func makeAssistantMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.AssistantMessage(content, nil)).(M)
	case *schema.AgenticMessage:
		return any(&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: content}),
			},
		}).(M)
	}
	panic("unreachable")
}

func makeTaskListResultMsg[M adk.MessageType](callID string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.ToolMessage("task list result", callID, schema.WithToolName(TaskListToolName))).(M)
	case *schema.AgenticMessage:
		return any(&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.FunctionToolResult{
					CallID: callID,
					Name:   TaskListToolName,
					Content: []*schema.FunctionToolResultContentBlock{
						{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "task list result"}},
					},
				}),
			},
		}).(M)
	}
	panic("unreachable")
}

func makeTaskCreateCallMsg[M adk.MessageType](callID string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.AssistantMessage("", []schema.ToolCall{
			{ID: callID, Function: schema.FunctionCall{Name: TaskCreateToolName, Arguments: "{}"}},
		})).(M)
	case *schema.AgenticMessage:
		return any(&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.FunctionToolCall{
					CallID:    callID,
					Name:      TaskCreateToolName,
					Arguments: "{}",
				}),
			},
		}).(M)
	}
	panic("unreachable")
}

func makeReminderMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{
			Role:    schema.User,
			Content: content,
			Extra:   map[string]any{reminderMessageFlag: struct{}{}},
		}).(M)
	case *schema.AgenticMessage:
		return any(&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: content}),
			},
			Extra: map[string]any{reminderMessageFlag: struct{}{}},
		}).(M)
	}
	panic("unreachable")
}

func assertIsReminderMessage[M adk.MessageType](t *testing.T, msg M, expectedContent string) {
	t.Helper()
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		m := any(msg).(*schema.Message)
		assert.Equal(t, schema.User, m.Role, "reminder message should have User role")
		_, ok := m.Extra[reminderMessageFlag]
		assert.True(t, ok, "message should have reminderMessageFlag")
		assert.Equal(t, expectedContent, m.Content, "reminder message content should match")
	case *schema.AgenticMessage:
		m := any(msg).(*schema.AgenticMessage)
		assert.Equal(t, schema.AgenticRoleTypeUser, m.Role, "reminder message should have User role")
		_, ok := m.Extra[reminderMessageFlag]
		assert.True(t, ok, "message should have reminderMessageFlag")
		for _, block := range m.ContentBlocks {
			if block != nil && block.Type == schema.ContentBlockTypeUserInputText && block.UserInputText != nil {
				assert.Equal(t, expectedContent, block.UserInputText.Text, "reminder message content should match")
				return
			}
		}
		t.Fatal("reminder message should have UserInputText content block")
	}
}

func newTypedMiddlewareForTestGeneric[M adk.MessageType](backend Backend) *typedMiddleware[M] {
	return &typedMiddleware[M]{
		backend: backend,
		baseDir: "/tmp/tasks",
	}
}

func newInMemoryBackendWithTask(ctx context.Context) Backend {
	backend := newInMemoryBackend()
	lock := &sync.Mutex{}
	createTool := newTaskCreateTool(backend, "/tmp/tasks", lock)
	_, _ = createTool.InvokableRun(ctx, `{"subject": "Test Task", "description": "Test desc"}`)
	return backend
}
