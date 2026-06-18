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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type taskOutputInput struct {
	TaskID string `json:"task_id" jsonschema:"required" jsonschema_description:"The task ID to get output from"`
}

func newTaskOutputTool[M adk.MessageType](mgr *TypedTaskMgr[M]) (tool.InvokableTool, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskOutputToolDescription,
		Chinese: taskOutputToolDescriptionChinese,
	})
	return utils.InferTool(taskOutputToolName, desc, func(ctx context.Context, input taskOutputInput) (string, error) {
		task, ok := mgr.Get(input.TaskID)
		if !ok {
			return fmt.Sprintf("Task %q not found", input.TaskID), nil
		}

		// Mark this task's result as queried by the main agent.
		mgr.MarkQueried(input.TaskID)

		return formatTask(task), nil
	})
}

func formatTask(task *Task) string {
	result := fmt.Sprintf("Task ID: %s\nDescription: %s\nStatus: %s",
		task.ID, task.Description, task.Status)

	if task.Result != "" {
		result += fmt.Sprintf("\nResult: %s", task.Result)
	}
	if task.Error != "" {
		result += fmt.Sprintf("\nError: %s", task.Error)
	}
	if task.DoneAt != nil {
		result += fmt.Sprintf("\nCompleted at: %s", task.DoneAt.Format("2006-01-02 15:04:05"))
	}

	return result
}
