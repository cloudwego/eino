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
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/schema"
)

// taskWriteToolNames is the set of task tool names that count as "task management writes".
// Only write operations (TaskCreate/TaskUpdate) reset the reminder counter,
// matching the reference implementation behavior.
var taskWriteToolNames = map[string]bool{
	TaskCreateToolName: true,
	TaskUpdateToolName: true,
}

// defaultReminderInterval is the default number of assistant turns before a reminder is injected.
const defaultReminderInterval = 10

// extraKeyTaskReminder is the marker in message.Extra to identify task reminder messages.
const extraKeyTaskReminder = "_task_reminder"

// reminderTurnStats holds the turn distance metrics computed from message history.
type reminderTurnStats struct {
	// turnsSinceLastTaskManagement is the number of assistant turns since the last
	// TaskCreate or TaskUpdate tool call.
	turnsSinceLastTaskManagement int
	// turnsSinceLastReminder is the number of assistant turns since the last
	// task_reminder message was injected.
	turnsSinceLastReminder int
}

// countAssistantMessages returns the total number of assistant messages in the history.
func countAssistantMessages(messages []adk.Message) int {
	count := 0
	for _, msg := range messages {
		if msg != nil && msg.Role == schema.Assistant {
			count++
		}
	}
	return count
}

// computeTurnStats scans the message history from the end, counting assistant turns
// to find how long ago task management tools were used and how long ago the last
// reminder was injected.
func computeTurnStats(messages []adk.Message) reminderTurnStats {
	var (
		foundTaskWrite   = false
		foundReminder    = false
		turnsSinceWrite  = 0
		turnsSinceRemind = 0
	)

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}

		if msg.Role == schema.Assistant {
			// Check if this assistant message contains TaskCreate or TaskUpdate tool calls
			if !foundTaskWrite {
				for _, tc := range msg.ToolCalls {
					if taskWriteToolNames[tc.Function.Name] {
						foundTaskWrite = true
						break
					}
				}
				if !foundTaskWrite {
					turnsSinceWrite++
				}
			}
			if !foundReminder {
				turnsSinceRemind++
			}
		} else if msg.Role == schema.User && !foundReminder {
			// Check if this is a task_reminder message (injected by us)
			if msg.Extra != nil {
				if _, ok := msg.Extra[extraKeyTaskReminder]; ok {
					foundReminder = true
				}
			}
		}

		if foundTaskWrite && foundReminder {
			break
		}
	}

	return reminderTurnStats{
		turnsSinceLastTaskManagement: turnsSinceWrite,
		turnsSinceLastReminder:       turnsSinceRemind,
	}
}

// hasTaskUpdateTool checks whether TaskUpdate is available in the current tool list.
func hasTaskUpdateTool(tools []*schema.ToolInfo) bool {
	for _, t := range tools {
		if t.Name == TaskUpdateToolName {
			return true
		}
	}
	return false
}

// formatTaskList formats existing tasks for inclusion in the reminder message.
func formatTaskList(tasks []*task) string {
	if len(tasks) == 0 {
		return ""
	}

	var sb strings.Builder
	_, _ = sb.WriteString("\n\nHere are the existing tasks:\n\n")
	for _, t := range tasks {
		_, _ = fmt.Fprintf(&sb, "#%s. [%s] %s", t.ID, t.Status, t.Subject)
		if t.Owner != "" {
			_, _ = fmt.Fprintf(&sb, " [owner: %s]", t.Owner)
		}
		_, _ = sb.WriteString("\n")
	}
	return sb.String()
}

// BeforeModelRewriteState injects a task reminder message into the conversation history
// before the model is called, if task tools haven't been used for a while.
//
// The reminder is injected when ALL of the following conditions are met:
//  1. Shared-task mode is enabled (task base dir resolver configured)
//  2. TaskUpdate tool is available in the current tool list
//  3. Message history is not empty
//  4. >= reminderInterval assistant turns since last TaskCreate/TaskUpdate usage
//  5. >= reminderInterval assistant turns since last task_reminder injection
func (m *middleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	// Only active in shared-task mode
	if !m.usesSharedTaskMode() {
		return ctx, state, nil
	}

	// Reminder disabled
	if m.reminderInterval <= 0 {
		return ctx, state, nil
	}

	// Must have messages and TaskUpdate tool available
	if len(state.Messages) == 0 || !hasTaskUpdateTool(mc.Tools) {
		return ctx, state, nil
	}

	interval := m.reminderInterval

	// Compute turn distances
	stats := computeTurnStats(state.Messages)

	// When onReminder is set, the callback path doesn't inject a _task_reminder
	// marker into messages, so computeTurnStats can't find it. Use the stored
	// assistant count to compute turnsSinceLastReminder as a fallback.
	if m.onReminder != nil && m.lastCallbackReminderAssistantCount > 0 {
		currentAssistant := countAssistantMessages(state.Messages)
		callbackTurnsSince := currentAssistant - m.lastCallbackReminderAssistantCount
		if callbackTurnsSince < 0 {
			callbackTurnsSince = 0 // handle message compaction edge case
		}
		if callbackTurnsSince < stats.turnsSinceLastReminder {
			stats.turnsSinceLastReminder = callbackTurnsSince
		}
	}

	if stats.turnsSinceLastTaskManagement < interval || stats.turnsSinceLastReminder < interval {
		return ctx, state, nil
	}

	// Build reminder content
	reminderText := internal.SelectPrompt(internal.I18nPrompts{
		English: taskReminderPrompt,
		Chinese: taskReminderPromptChinese,
	})

	// Try to append current task list
	tasks, err := listTasks(ctx, m.backend, m.resolveBaseDir(ctx))
	if err == nil {
		tasks = filterVisibleTasks(tasks)
		reminderText += formatTaskList(tasks)
	}

	reminderMsg := &schema.Message{
		Role:    schema.User,
		Content: reminderText,
		Extra: map[string]any{
			extraKeyTaskReminder: true,
		},
	}

	if m.onReminder != nil {
		// Record current assistant count for throttling, then deliver via callback.
		// Don't inject into state — the callback (e.g. router.Push) handles delivery.
		m.lastCallbackReminderAssistantCount = countAssistantMessages(state.Messages)
		m.onReminder(ctx, reminderText)
		return ctx, state, nil
	}

	// Inject reminder as a user message marked with _task_reminder in Extra
	nState := *state
	nState.Messages = make([]adk.Message, len(state.Messages)+1)
	copy(nState.Messages, state.Messages)
	nState.Messages[len(state.Messages)] = reminderMsg

	return ctx, &nState, nil
}

const taskReminderPrompt = `<system-reminder>
The task tools haven't been used recently. If you're working on tasks that would benefit from tracking progress, consider using TaskCreate to add new tasks and TaskUpdate to update task status (set to in_progress when starting, completed when done). Also consider cleaning up the task list if it has become stale. Only use these if relevant to the current work. This is just a gentle reminder - ignore if not applicable. Make sure that you NEVER mention this reminder to the user
</system-reminder>`

const taskReminderPromptChinese = `<system-reminder>
任务工具最近没有被使用。如果你正在处理需要跟踪进度的工作，请考虑使用 TaskCreate 添加新任务，使用 TaskUpdate 更新任务状态（开始时设为 in_progress，完成时设为 completed）。如果任务列表已过时，也请考虑清理。仅在与当前工作相关时使用这些工具。这只是一个温和的提醒 - 如果不适用请忽略。请确保你永远不要向用户提及此提醒
</system-reminder>`
