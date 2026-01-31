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
	"path/filepath"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func newTaskUpdateTool(backend Backend, baseDir string, lock *sync.Mutex) *taskUpdateTool {
	return &taskUpdateTool{Backend: backend, BaseDir: baseDir, lock: lock}
}

type taskUpdateTool struct {
	Backend Backend
	BaseDir string
	lock    *sync.Mutex
}

func (t *taskUpdateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: taskUpdateToolName,
		Desc: taskUpdateToolDesc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"taskId": {
				Type:     schema.String,
				Desc:     "The ID of the task to update",
				Required: true,
			},
			"subject": {
				Type:     schema.String,
				Desc:     "New subject for the task",
				Required: false,
			},
			"description": {
				Type:     schema.String,
				Desc:     "New description for the task",
				Required: false,
			},
			"activeForm": {
				Type:     schema.String,
				Desc:     "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")",
				Required: false,
			},
			"status": {
				Type:     schema.String,
				Desc:     "New status for the task: 'pending', 'in_progress', 'completed', or 'deleted'",
				Enum:     []string{"pending", "in_progress", "completed", "deleted"},
				Required: false,
			},
			"addBlocks": {
				Type:     schema.Array,
				Desc:     "Task IDs that this task blocks",
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
				Required: false,
			},
			"addBlockedBy": {
				Type:     schema.Array,
				Desc:     "Task IDs that block this task",
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
				Required: false,
			},
			"owner": {
				Type:     schema.String,
				Desc:     "New owner for the task",
				Required: false,
			},
			"metadata": {
				Type: schema.Object,
				Desc: "Metadata keys to merge into the task. Set a key to null to delete it.",
				SubParams: map[string]*schema.ParameterInfo{
					"propertyNames": {
						Type: schema.String,
					},
				},
				Required: false,
			},
		}),
	}, nil
}

type taskUpdateArgs struct {
	TaskID       string                 `json:"taskId"`
	Subject      string                 `json:"subject,omitempty"`
	Description  string                 `json:"description,omitempty"`
	ActiveForm   string                 `json:"activeForm,omitempty"`
	Status       string                 `json:"status,omitempty"`
	AddBlocks    []string               `json:"addBlocks,omitempty"`
	AddBlockedBy []string               `json:"addBlockedBy,omitempty"`
	Owner        string                 `json:"owner,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

func (t *taskUpdateTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	params := &taskUpdateArgs{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", err
	}

	if !isValidTaskID(params.TaskID) {
		return "", fmt.Errorf("%s validate task ID failed, err: invalid format: %s", taskUpdateToolName, params.TaskID)
	}

	taskFileName := fmt.Sprintf("%s.json", params.TaskID)
	taskFilePath := filepath.Join(t.BaseDir, taskFileName)

	if params.Status == "deleted" {
		if err := t.removeTaskFromDependencies(ctx, params.TaskID); err != nil {
			return "", fmt.Errorf("%s remove Task #%s from dependencies failed, err: %w", taskUpdateToolName, params.TaskID, err)
		}

		err = t.Backend.Delete(ctx, &DeleteRequest{
			FilePath: taskFilePath,
		})
		if err != nil {
			return "", fmt.Errorf("%s delete Task #%s failed, err: %w", taskUpdateToolName, params.TaskID, err)
		}

		return fmt.Sprintf("Updated task #%s deleted", params.TaskID), nil
	}

	content, err := t.Backend.Read(ctx, &ReadRequest{
		FilePath: taskFilePath,
	})
	if err != nil {
		return "", fmt.Errorf("%s read Task #%s failed, err: %w", taskUpdateToolName, params.TaskID, err)
	}

	taskData := &task{}
	err = sonic.UnmarshalString(content, taskData)
	if err != nil {
		return "", fmt.Errorf("%s parse Task #%s failed, err: %w", taskUpdateToolName, params.TaskID, err)
	}

	var updatedFields []string

	if params.Subject != "" {
		taskData.Subject = params.Subject
		updatedFields = append(updatedFields, "subject")
	}
	if params.Description != "" {
		taskData.Description = params.Description
		updatedFields = append(updatedFields, "description")
	}
	if params.ActiveForm != "" {
		taskData.ActiveForm = params.ActiveForm
		updatedFields = append(updatedFields, "activeForm")
	}
	if params.Status != "" {
		taskData.Status = params.Status
		updatedFields = append(updatedFields, "status")
	}
	if len(params.AddBlocks) > 0 || len(params.AddBlockedBy) > 0 {
		tasks, err := listTasks(ctx, t.Backend, t.BaseDir)
		if err != nil {
			return "", fmt.Errorf("%s list tasks failed, err: %w", taskUpdateToolName, err)
		}
		taskMap := make(map[string]*task, len(tasks))
		for _, tk := range tasks {
			taskMap[tk.ID] = tk
		}

		if len(params.AddBlocks) > 0 {
			for _, blockedTaskID := range params.AddBlocks {
				if !isValidTaskID(blockedTaskID) {
					return "", fmt.Errorf("%s validate blocked task ID failed, err: invalid format: %s", taskUpdateToolName, blockedTaskID)
				}
				if hasCyclicDependency(taskMap, params.TaskID, blockedTaskID) {
					return "", fmt.Errorf("%s adding Task #%s to blocks of Task #%s would create a cyclic dependency", taskUpdateToolName, blockedTaskID, params.TaskID)
				}
			}
			for _, blockedTaskID := range params.AddBlocks {
				if err := t.addBlockedByToTask(ctx, blockedTaskID, params.TaskID); err != nil {
					return "", fmt.Errorf("%s update Task #%s blocks failed, err: %w", taskUpdateToolName, params.TaskID, err)
				}
			}
			taskData.Blocks = appendUnique(taskData.Blocks, params.AddBlocks...)
			updatedFields = append(updatedFields, "blocks")
		}
		if len(params.AddBlockedBy) > 0 {
			for _, blockingTaskID := range params.AddBlockedBy {
				if !isValidTaskID(blockingTaskID) {
					return "", fmt.Errorf("%s validate blocking task ID failed, err: invalid format: %s", taskUpdateToolName, blockingTaskID)
				}
				if hasCyclicDependency(taskMap, blockingTaskID, params.TaskID) {
					return "", fmt.Errorf("%s adding Task #%s to blockedBy of Task #%s would create a cyclic dependency", taskUpdateToolName, blockingTaskID, params.TaskID)
				}
			}
			for _, blockingTaskID := range params.AddBlockedBy {
				if err := t.addBlocksToTask(ctx, blockingTaskID, params.TaskID); err != nil {
					return "", fmt.Errorf("%s update Task #%s blockedBy failed, err: %w", taskUpdateToolName, params.TaskID, err)
				}
			}
			taskData.BlockedBy = appendUnique(taskData.BlockedBy, params.AddBlockedBy...)
			updatedFields = append(updatedFields, "blockedBy")
		}
	}
	if params.Owner != "" {
		taskData.Owner = params.Owner
		updatedFields = append(updatedFields, "owner")
	}
	if params.Metadata != nil {
		if taskData.Metadata == nil {
			taskData.Metadata = make(map[string]interface{})
		}
		for k, v := range params.Metadata {
			if v == nil {
				delete(taskData.Metadata, k)
			} else {
				taskData.Metadata[k] = v
			}
		}
		updatedFields = append(updatedFields, "metadata")
	}

	updatedContent, err := sonic.MarshalString(taskData)
	if err != nil {
		return "", fmt.Errorf("%s marshal Task #%s failed, err: %w", taskUpdateToolName, params.TaskID, err)
	}

	err = t.Backend.Write(ctx, &WriteRequest{
		FilePath: taskFilePath,
		Content:  updatedContent,
	})
	if err != nil {
		return "", fmt.Errorf("%s write Task #%s failed, err: %w", taskUpdateToolName, params.TaskID, err)
	}

	return fmt.Sprintf("Updated task #%s %s", params.TaskID, strings.Join(updatedFields, ", ")), nil
}

func (t *taskUpdateTool) removeTaskFromDependencies(ctx context.Context, deletedTaskID string) error {
	tasks, err := listTasks(ctx, t.Backend, t.BaseDir)
	if err != nil {
		return err
	}

	for _, taskData := range tasks {
		if taskData.ID == deletedTaskID {
			continue
		}

		modified := false
		newBlocks := make([]string, 0, len(taskData.Blocks))
		for _, id := range taskData.Blocks {
			if id != deletedTaskID {
				newBlocks = append(newBlocks, id)
			} else {
				modified = true
			}
		}

		newBlockedBy := make([]string, 0, len(taskData.BlockedBy))
		for _, id := range taskData.BlockedBy {
			if id != deletedTaskID {
				newBlockedBy = append(newBlockedBy, id)
			} else {
				modified = true
			}
		}

		if modified {
			taskData.Blocks = newBlocks
			taskData.BlockedBy = newBlockedBy

			updatedContent, err := sonic.MarshalString(taskData)
			if err != nil {
				return fmt.Errorf("failed to marshal task #%s: %w", taskData.ID, err)
			}

			taskFilePath := filepath.Join(t.BaseDir, fmt.Sprintf("%s.json", taskData.ID))
			if err := t.Backend.Write(ctx, &WriteRequest{FilePath: taskFilePath, Content: updatedContent}); err != nil {
				return fmt.Errorf("failed to write task #%s: %w", taskData.ID, err)
			}
		}
	}

	return nil
}

func (t *taskUpdateTool) addBlockedByToTask(ctx context.Context, targetTaskID, blockerTaskID string) error {
	taskFilePath := filepath.Join(t.BaseDir, fmt.Sprintf("%s.json", targetTaskID))

	content, err := t.Backend.Read(ctx, &ReadRequest{FilePath: taskFilePath})
	if err != nil {
		return fmt.Errorf("failed to read task #%s for updating blockedBy: %w", targetTaskID, err)
	}

	targetTask := &task{}
	if err := sonic.UnmarshalString(content, targetTask); err != nil {
		return fmt.Errorf("failed to parse task #%s: %w", targetTaskID, err)
	}

	targetTask.BlockedBy = appendUnique(targetTask.BlockedBy, blockerTaskID)

	updatedContent, err := sonic.MarshalString(targetTask)
	if err != nil {
		return fmt.Errorf("failed to marshal task #%s: %w", targetTaskID, err)
	}

	if err := t.Backend.Write(ctx, &WriteRequest{FilePath: taskFilePath, Content: updatedContent}); err != nil {
		return fmt.Errorf("failed to write task #%s: %w", targetTaskID, err)
	}

	return nil
}

func (t *taskUpdateTool) addBlocksToTask(ctx context.Context, targetTaskID, blockedTaskID string) error {
	taskFilePath := filepath.Join(t.BaseDir, fmt.Sprintf("%s.json", targetTaskID))

	content, err := t.Backend.Read(ctx, &ReadRequest{FilePath: taskFilePath})
	if err != nil {
		return fmt.Errorf("failed to read task #%s for updating blocks: %w", targetTaskID, err)
	}

	targetTask := &task{}
	if err := sonic.UnmarshalString(content, targetTask); err != nil {
		return fmt.Errorf("failed to parse task #%s: %w", targetTaskID, err)
	}

	targetTask.Blocks = appendUnique(targetTask.Blocks, blockedTaskID)

	updatedContent, err := sonic.MarshalString(targetTask)
	if err != nil {
		return fmt.Errorf("failed to marshal task #%s: %w", targetTaskID, err)
	}

	if err := t.Backend.Write(ctx, &WriteRequest{FilePath: taskFilePath, Content: updatedContent}); err != nil {
		return fmt.Errorf("failed to write task #%s: %w", targetTaskID, err)
	}

	return nil
}

const taskUpdateToolName = "TaskUpdate"
const taskUpdateToolDesc = `Use this tool to update a task in the task list.

## When to Use This Tool

**Mark tasks as resolved:**
- When you have completed the work described in a task
- When a task is no longer needed or has been superseded
- IMPORTANT: Always mark your assigned tasks as resolved when you finish them
- After resolving, call TaskList to find your next task

- ONLY mark a task as completed when you have FULLY accomplished it
- If you encounter errors, blockers, or cannot finish, keep the task as in_progress
- When blocked, create a new task describing what needs to be resolved
- Never mark a task as completed if:
  - Tests are failing
  - Implementation is partial
  - You encountered unresolved errors
  - You couldn't find necessary files or dependencies

**Delete tasks:**
- When a task is no longer relevant or was created in error
- Setting status to ` + "`deleted`" + ` permanently removes the task

**Update task details:**
- When requirements change or become clearer
- When establishing dependencies between tasks

## Fields You Can Update

- **status**: The task status (see Status Workflow below)
- **subject**: Change the task title (imperative form, e.g., "Run tests")
- **description**: Change the task description
- **activeForm**: Present continuous form shown in spinner when in_progress (e.g., "Running tests")
- **owner**: Change the task owner (agent name)
- **metadata**: Merge metadata keys into the task (set a key to null to delete it)
- **addBlocks**: Mark tasks that cannot start until this one completes
- **addBlockedBy**: Mark tasks that must complete before this one can start

## Status Workflow

Status progresses: ` + "`pending`" + ` → ` + "`in_progress`" + ` → ` + "`completed`" + `

Use ` + "`deleted`" + ` to permanently remove a task.

## Staleness

Make sure to read a task's latest state using ` + "`TaskGet`" + ` before updating it.

## Examples

Mark task as in progress when starting work:
` + "```json" + `
{"taskId": "1", "status": "in_progress"}
` + "```" + `

Mark task as completed after finishing work:
` + "```json" + `
{"taskId": "1", "status": "completed"}
` + "```" + `

Delete a task:
` + "```json" + `
{"taskId": "1", "status": "deleted"}
` + "```" + `

Claim a task by setting owner:
` + "```json" + `
{"taskId": "1", "owner": "my-name"}
` + "```" + `

Set up task dependencies:
` + "```json" + `
{"taskId": "2", "addBlockedBy": ["1"]}
` + "```" + `
`
