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

	"github.com/cloudwego/eino/adk/internal"
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

type taskUpdateArgs struct {
	TaskID       string         `json:"taskId"`
	Subject      string         `json:"subject,omitempty"`
	Description  string         `json:"description,omitempty"`
	ActiveForm   string         `json:"activeForm,omitempty"`
	Status       string         `json:"status,omitempty"`
	AddBlocks    []string       `json:"addBlocks,omitempty"`
	AddBlockedBy []string       `json:"addBlockedBy,omitempty"`
	Owner        string         `json:"owner,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

func (t *taskUpdateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	desc, err := internal.SelectPrompt(internal.I18nPrompts{
		English: taskUpdateToolDesc,
		Chinese: taskUpdateToolDescChinese,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to select prompt: %w", err)
	}

	return &schema.ToolInfo{
		Name: TaskUpdateToolName,
		Desc: desc,
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

func (t *taskUpdateTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	params := &taskUpdateArgs{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", err
	}

	if !isValidTaskID(params.TaskID) {
		return "", fmt.Errorf("%s validate task ID failed, err: invalid format: %s", TaskUpdateToolName, params.TaskID)
	}

	taskFileName := fmt.Sprintf("%s.json", params.TaskID)
	taskFilePath := filepath.Join(t.BaseDir, taskFileName)

	if params.Status == taskStatusDeleted {
		if removeErr := t.removeTaskFromDependencies(ctx, params.TaskID); removeErr != nil {
			return "", fmt.Errorf("%s remove Task #%s from dependencies failed, err: %w", TaskUpdateToolName, params.TaskID, removeErr)
		}

		err = t.Backend.Delete(ctx, &DeleteRequest{
			FilePath: taskFilePath,
		})
		if err != nil {
			return "", fmt.Errorf("%s delete Task #%s failed, err: %w", TaskUpdateToolName, params.TaskID, err)
		}

		resp := &taskOut{
			Result: fmt.Sprintf("Updated task #%s deleted", params.TaskID),
		}
		jsonResp, marshalErr := sonic.MarshalString(resp)
		if marshalErr != nil {
			return "", fmt.Errorf("%s marshal taskOut failed, err: %w", TaskUpdateToolName, marshalErr)
		}
		return jsonResp, nil
	}

	content, err := t.Backend.Read(ctx, &ReadRequest{
		FilePath: taskFilePath,
	})
	if err != nil {
		return "", fmt.Errorf("%s read Task #%s failed, err: %w", TaskUpdateToolName, params.TaskID, err)
	}

	taskData := &task{}
	err = sonic.UnmarshalString(content, taskData)
	if err != nil {
		return "", fmt.Errorf("%s parse Task #%s failed, err: %w", TaskUpdateToolName, params.TaskID, err)
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
		tasks, listErr := listTasks(ctx, t.Backend, t.BaseDir)
		if listErr != nil {
			return "", fmt.Errorf("%s list tasks failed, err: %w", TaskUpdateToolName, listErr)
		}
		taskMap := make(map[string]*task, len(tasks))
		for _, tk := range tasks {
			taskMap[tk.ID] = tk
		}

		if len(params.AddBlocks) > 0 {
			for _, blockedTaskID := range params.AddBlocks {
				if !isValidTaskID(blockedTaskID) {
					return "", fmt.Errorf("%s validate blocked task ID failed, err: invalid format: %s", TaskUpdateToolName, blockedTaskID)
				}
				if hasCyclicDependency(taskMap, params.TaskID, blockedTaskID) {
					return "", fmt.Errorf("%s adding Task #%s to blocks of Task #%s would create a cyclic dependency", TaskUpdateToolName, blockedTaskID, params.TaskID)
				}
			}
			for _, blockedTaskID := range params.AddBlocks {
				if addErr := t.addBlockedByToTask(ctx, blockedTaskID, params.TaskID); addErr != nil {
					return "", fmt.Errorf("%s update Task #%s blocks failed, err: %w", TaskUpdateToolName, params.TaskID, addErr)
				}
			}
			taskData.Blocks = appendUnique(taskData.Blocks, params.AddBlocks...)
			updatedFields = append(updatedFields, "blocks")
		}
		if len(params.AddBlockedBy) > 0 {
			for _, blockingTaskID := range params.AddBlockedBy {
				if !isValidTaskID(blockingTaskID) {
					return "", fmt.Errorf("%s validate blocking task ID failed, err: invalid format: %s", TaskUpdateToolName, blockingTaskID)
				}
				if hasCyclicDependency(taskMap, blockingTaskID, params.TaskID) {
					return "", fmt.Errorf("%s adding Task #%s to blockedBy of Task #%s would create a cyclic dependency", TaskUpdateToolName, blockingTaskID, params.TaskID)
				}
			}
			for _, blockingTaskID := range params.AddBlockedBy {
				if addErr := t.addBlocksToTask(ctx, blockingTaskID, params.TaskID); addErr != nil {
					return "", fmt.Errorf("%s update Task #%s blockedBy failed, err: %w", TaskUpdateToolName, params.TaskID, addErr)
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
			taskData.Metadata = make(map[string]any)
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
		return "", fmt.Errorf("%s marshal Task #%s failed, err: %w", TaskUpdateToolName, params.TaskID, err)
	}

	err = t.Backend.Write(ctx, &WriteRequest{
		FilePath: taskFilePath,
		Content:  updatedContent,
	})
	if err != nil {
		return "", fmt.Errorf("%s write Task #%s failed, err: %w", TaskUpdateToolName, params.TaskID, err)
	}

	if params.Status == taskStatusCompleted {
		if checkErr := t.checkIfNeedDeleteAllTasks(ctx); checkErr != nil {
			return "", fmt.Errorf("%s check and delete all tasks failed, err: %w", TaskUpdateToolName, checkErr)
		}
	}

	resp := &taskOut{
		Result: fmt.Sprintf("Updated task #%s %s", params.TaskID, strings.Join(updatedFields, ", ")),
	}

	jsonResp, err := sonic.MarshalString(resp)
	if err != nil {
		return "", fmt.Errorf("%s marshal taskOut failed, err: %w", TaskUpdateToolName, err)
	}

	return jsonResp, nil
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
	if unmarshalErr := sonic.UnmarshalString(content, targetTask); unmarshalErr != nil {
		return fmt.Errorf("failed to parse task #%s: %w", targetTaskID, unmarshalErr)
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
	if unmarshalErr := sonic.UnmarshalString(content, targetTask); unmarshalErr != nil {
		return fmt.Errorf("failed to parse task #%s: %w", targetTaskID, unmarshalErr)
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

// checkIfNeedDeleteAllTasks checks if all tasks are completed, if so, it deletes all tasks
func (t *taskUpdateTool) checkIfNeedDeleteAllTasks(ctx context.Context) error {
	tasks, err := listTasks(ctx, t.Backend, t.BaseDir)
	if err != nil {
		return err
	}

	for _, task := range tasks {
		if task.Status != taskStatusCompleted {
			return nil
		}
	}

	for _, task := range tasks {
		err := t.Backend.Delete(ctx, &DeleteRequest{
			FilePath: filepath.Join(t.BaseDir, task.ID+".json"),
		})
		if err != nil {
			return err
		}
	}

	return nil
}

const TaskUpdateToolName = "TaskUpdate"
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

const taskUpdateToolDescChinese = `使用此工具更新任务列表中的任务。

## 何时使用此工具

**将任务标记为已完成：**
- 当你完成了任务中描述的工作时
- 当任务不再需要或已被取代时
- 重要：完成分配给你的任务后，务必将其标记为已完成
- 完成后，调用 TaskList 查找下一个任务

- 只有在完全完成任务时才将其标记为已完成
- 如果遇到错误、阻塞或无法完成，请保持任务为 in_progress 状态
- 当被阻塞时，创建一个新任务描述需要解决的问题
- 在以下情况下不要将任务标记为已完成：
  - 测试失败
  - 实现不完整
  - 遇到未解决的错误
  - 找不到必要的文件或依赖项

**删除任务：**
- 当任务不再相关或创建错误时
- 将状态设置为 ` + "`deleted`" + ` 会永久删除任务

**更新任务详情：**
- 当需求变更或变得更清晰时
- 当建立任务之间的依赖关系时

## 可更新的字段

- **status**：任务状态（参见下方状态流程）
- **subject**：更改任务标题（使用祈使句形式，例如"运行测试"）
- **description**：更改任务描述
- **activeForm**：in_progress 状态时在加载动画中显示的现在进行时形式（例如"正在运行测试"）
- **owner**：更改任务所有者（代理名称）
- **metadata**：将元数据键合并到任务中（将键设置为 null 可删除它）
- **addBlocks**：标记在此任务完成之前无法开始的任务
- **addBlockedBy**：标记必须在此任务开始之前完成的任务

## 状态流程

状态进展：` + "`pending`" + ` → ` + "`in_progress`" + ` → ` + "`completed`" + `

使用 ` + "`deleted`" + ` 永久删除任务。

## 过期性

更新任务前，请确保使用 ` + "`TaskGet`" + ` 读取任务的最新状态。

## 示例

开始工作时将任务标记为进行中：
` + "```json" + `
{"taskId": "1", "status": "in_progress"}
` + "```" + `

完成工作后将任务标记为已完成：
` + "```json" + `
{"taskId": "1", "status": "completed"}
` + "```" + `

删除任务：
` + "```json" + `
{"taskId": "1", "status": "deleted"}
` + "```" + `

通过设置 owner 认领任务：
` + "```json" + `
{"taskId": "1", "owner": "my-name"}
` + "```" + `

设置任务依赖关系：
` + "```json" + `
{"taskId": "2", "addBlockedBy": ["1"]}
` + "```" + `
`
