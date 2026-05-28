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
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/schema"
)

// Config is the configuration for the plan task middleware.
type Config struct {
	Backend Backend
	BaseDir string
}

// NewTyped creates a new plantask middleware that provides task management tools for agents.
// It adds TaskCreate, TaskGet, TaskUpdate, and TaskList tools to the agent's tool set,
// allowing agents to create and manage structured task lists during coding sessions.
//
// This is the generic constructor that supports both *schema.Message and *schema.AgenticMessage.
func NewTyped[M adk.MessageType](_ context.Context, config *Config) (adk.TypedChatModelAgentMiddleware[M], error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if config.BaseDir == "" {
		return nil, fmt.Errorf("baseDir is required")
	}

	return &typedMiddleware[M]{backend: config.Backend, baseDir: config.BaseDir}, nil
}

// New creates a new plantask middleware that provides task management tools for agents.
// It adds TaskCreate, TaskGet, TaskUpdate, and TaskList tools to the agent's tool set,
// allowing agents to create and manage structured task lists during coding sessions.
func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped[*schema.Message](ctx, config)
}

type typedMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	backend Backend
	baseDir string
}

func (m *typedMiddleware[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	lock := sync.Mutex{}
	nRunCtx.Tools = append(nRunCtx.Tools,
		newTaskCreateTool(m.backend, m.baseDir, &lock),
		newTaskGetTool(m.backend, m.baseDir, &lock),
		newTaskUpdateTool(m.backend, m.baseDir, &lock),
		newTaskListTool(m.backend, m.baseDir, &lock),
	)

	return ctx, &nRunCtx, nil
}

// BeforeModelRewriteState ensures that when tasks exist, their information is preserved in the conversation context.
//
// When tasks have been created, this middleware guarantees that either:
//   - The task messages (via task_list or task_create tool calls) are already present in the context, or
//   - A reminder message is injected to prompt the model to retrieve the task list.
//
// This prevents task information from vanishing after summarization or context truncation.
func (m *typedMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M], mc *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	if state == nil {
		return ctx, nil, nil
	}
	if len(state.Messages) == 0 {
		return ctx, state, nil
	}

	// Query the backend to discover if any tasks have been created.
	fileInfos, err := m.backend.LsInfo(ctx, &LsInfoRequest{
		Path: m.baseDir,
	})
	if err != nil {
		return ctx, nil, err
	}

	// No tasks exist yet — nothing to preserve.
	if len(fileInfos) == 0 {
		return ctx, state, nil
	}

	// Task information is already present in the conversation history.
	if hasTaskMessagesInHistory(state.Messages) {
		return ctx, state, nil
	}

	// A reminder message has already been injected — avoid duplicate warnings.
	if hasReminderMessageInHistory(state.Messages) {
		return ctx, state, nil
	}

	// Inject a reminder message so the model knows to retrieve the task list.
	nState := *state
	nState.Messages = append(append([]M(nil), state.Messages...), buildReminderMessage[M]())
	return ctx, &nState, nil
}

// hasTaskMessagesInHistory checks whether the conversation history already contains task-related messages.
func hasTaskMessagesInHistory[M adk.MessageType](messages []M) bool {
	for _, msg := range messages {
		if hasTaskInMessage(msg) {
			return true
		}
	}
	return false
}

// hasTaskInMessage checks whether a single message contains task-related content.
func hasTaskInMessage[M adk.MessageType](msg M) bool {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		m := any(msg).(*schema.Message)
		if m.Role == schema.Tool && m.ToolName == TaskListToolName {
			return true
		}
		if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if tc.Function.Name == TaskCreateToolName {
					return true
				}
			}
		}
	case *schema.AgenticMessage:
		m := any(msg).(*schema.AgenticMessage)
		for _, block := range m.ContentBlocks {
			if block == nil {
				continue
			}
			if block.Type == schema.ContentBlockTypeFunctionToolCall &&
				block.FunctionToolCall != nil && block.FunctionToolCall.Name == TaskCreateToolName {
				return true
			}
			if block.Type == schema.ContentBlockTypeFunctionToolResult &&
				block.FunctionToolResult != nil && block.FunctionToolResult.Name == TaskListToolName {
				return true
			}
		}
	default:
		panic("unreachable: unknown MessageType")
	}
	return false
}

// hasReminderMessageInHistory checks whether any message in the history is a reminder injected by this middleware.
func hasReminderMessageInHistory[M adk.MessageType](messages []M) bool {
	for _, msg := range messages {
		if hasReminderMessage(msg) {
			return true
		}
	}
	return false
}

// hasReminderMessage checks whether a message is a reminder injected by this middleware.
func hasReminderMessage[M adk.MessageType](msg M) bool {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		if m := any(msg).(*schema.Message); m.Extra != nil {
			if _, ok := m.Extra[reminderMessageFlag]; ok {
				return true
			}
		}
	case *schema.AgenticMessage:
		if m := any(msg).(*schema.AgenticMessage); m.Extra != nil {
			if _, ok := m.Extra[reminderMessageFlag]; ok {
				return true
			}
		}
	default:
		panic("unreachable: unknown MessageType")
	}
	return false
}

// buildReminderMessage constructs a reminder message prompting the model to retrieve the task list.
func buildReminderMessage[M adk.MessageType]() M {
	var zero M
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: defaultTaskNotInContextTemplate,
		Chinese: defaultTaskNotInContextTemplateChinese,
	})
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{
			Role:    schema.User,
			Content: desc,
			Extra:   map[string]any{reminderMessageFlag: struct{}{}},
		}).(M)
	case *schema.AgenticMessage:
		return any(&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: desc}),
			},
			Extra: map[string]any{reminderMessageFlag: struct{}{}},
		}).(M)
	default:
		panic("unreachable: unknown MessageType")
	}
}

const (
	defaultTaskNotInContextTemplate        = "Reminder: Tasks exist but task information is not in the current context. Call " + TaskListToolName + " to retrieve the task list."
	defaultTaskNotInContextTemplateChinese = "提醒：系统存在任务但任务信息不在当前上下文中，请调用 " + TaskListToolName + " 获取任务列表。"
)
