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
	"log"
	"strings"
	"sync"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func newTaskUpdateTool(mw *middleware, turnLock *sync.RWMutex) *taskUpdateTool {
	return &taskUpdateTool{mw: mw, turnLock: turnLock}
}

type taskUpdateTool struct {
	mw       *middleware
	turnLock *sync.RWMutex
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
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskUpdateToolDesc,
		Chinese: taskUpdateToolDescChinese,
	})

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
	result, assignment, err := t.doUpdate(ctx, argumentsInJSON)
	if err != nil {
		return "", err
	}

	// Notify assignee outside the lock to avoid blocking other task operations
	// during mailbox I/O.
	if assignment != nil && t.mw.onTaskAssigned != nil {
		if err := t.mw.onTaskAssigned(ctx, *assignment); err != nil {
			log.Printf("[plantask] notify task assignment (task %s -> %s) failed: %v",
				assignment.TaskID, assignment.Owner, err)
		}
	}

	return result, nil
}

// doUpdate performs the actual task update under lock and returns the result string
// plus an optional TaskAssignment if an owner was set (to be notified outside the lock).
func (t *taskUpdateTool) doUpdate(ctx context.Context, argumentsInJSON string) (string, *TaskAssignment, error) {
	lock := t.mw.getLock(t.turnLock)
	lock.Lock()
	defer lock.Unlock()

	params := &taskUpdateArgs{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", nil, err
	}

	if !isValidTaskID(params.TaskID) {
		return "", nil, fmt.Errorf("%s validate task ID failed, err: invalid format: %s", TaskUpdateToolName, params.TaskID)
	}
	if params.Status != "" && !isValidTaskStatus(params.Status) {
		return "", nil, fmt.Errorf("%s invalid task status: %s", TaskUpdateToolName, params.Status)
	}

	if params.Status == taskStatusDeleted {
		if deleteErr := deleteTaskLocked(ctx, t.mw.backend, t.mw.resolveBaseDir(ctx), params.TaskID); deleteErr != nil {
			return "", nil, fmt.Errorf("%s delete Task #%s failed, err: %w", TaskUpdateToolName, params.TaskID, deleteErr)
		}

		result, marshalErr := marshalTaskResponse(fmt.Sprintf("Task #%s deleted", params.TaskID))
		return result, nil, marshalErr
	}

	baseDir := t.mw.resolveBaseDir(ctx)
	taskData, err := readTask(ctx, t.mw.backend, baseDir, params.TaskID)
	if err != nil {
		return "", nil, fmt.Errorf("%s %w", TaskUpdateToolName, err)
	}
	if taskData == nil {
		return "", nil, fmt.Errorf("%s Task #%s not found", TaskUpdateToolName, params.TaskID)
	}

	// Load the full task list once upfront when any operation needs it
	// (dependency updates, completion cleanup, or all-completed check).
	needsTaskList := len(params.AddBlocks) > 0 || len(params.AddBlockedBy) > 0 || params.Status == taskStatusCompleted
	var allTasks []*task
	if needsTaskList {
		var listErr error
		allTasks, listErr = listTasks(ctx, t.mw.backend, baseDir)
		if listErr != nil {
			return "", nil, fmt.Errorf("%s list tasks failed, err: %w", TaskUpdateToolName, listErr)
		}
		// Replace the allTasks entry for the current task with taskData so that
		// in-memory modifications (e.g., status set to "completed") are visible
		// to downstream consumers like deleteAllTasksIfCompleted.
		for i, tk := range allTasks {
			if tk.ID == params.TaskID {
				allTasks[i] = taskData
				break
			}
		}
	}

	var updatedFields []string

	updatedFields = t.updateBasicFields(taskData, params, updatedFields)

	if len(params.AddBlocks) > 0 || len(params.AddBlockedBy) > 0 {
		fields, depErr := t.updateDependencies(ctx, taskData, params, allTasks)
		if depErr != nil {
			return "", nil, depErr
		}
		updatedFields = append(updatedFields, fields...)
	}

	updatedFields = t.updateOwnerAndMetadata(ctx, taskData, params, updatedFields)

	if params.Status == taskStatusCompleted {
		// If dependency updates were applied above, allTasks is stale because
		// addDependencyToTask wrote modified tasks directly to the backend.
		// Reload to prevent clearCompletedTaskDependencies from overwriting
		// those changes with the stale in-memory snapshot.
		hasDependencyUpdates := len(params.AddBlocks) > 0 || len(params.AddBlockedBy) > 0
		if hasDependencyUpdates {
			var reloadErr error
			allTasks, reloadErr = listTasks(ctx, t.mw.backend, baseDir)
			if reloadErr != nil {
				return "", nil, fmt.Errorf("%s reload tasks after dependency update failed, err: %w", TaskUpdateToolName, reloadErr)
			}
			for i, tk := range allTasks {
				if tk.ID == params.TaskID {
					allTasks[i] = taskData
					break
				}
			}
		}
		fields, compErr := t.handleCompletion(ctx, taskData, params.TaskID, allTasks)
		if compErr != nil {
			return "", nil, compErr
		}
		updatedFields = append(updatedFields, fields...)
	}

	if err := writeTask(ctx, t.mw.backend, baseDir, taskData); err != nil {
		return "", nil, fmt.Errorf("%s %w", TaskUpdateToolName, err)
	}

	// Check if all tasks are completed. Reuse the in-memory allTasks slice:
	// handleCompletion may have modified task objects (cleared dependencies),
	// but status fields remain accurate for the all-completed check.
	// Cleanup is best-effort: the task update has already been persisted above,
	// so a cleanup failure should not fail the main operation.
	if params.Status == taskStatusCompleted {
		if checkErr := t.deleteAllTasksIfCompleted(ctx, allTasks); checkErr != nil {
			log.Printf("[plantask] auto-delete all completed tasks failed, err: %v", checkErr)
		}
	}

	// Build assignment info to notify outside the lock.
	var assignment *TaskAssignment
	if t.mw.usesSharedTaskMode() && containsString(updatedFields, "owner") {
		assignment = &TaskAssignment{
			TaskID:      params.TaskID,
			Subject:     taskData.Subject,
			Description: taskData.Description,
			Owner:       taskData.Owner,
			AssignedBy:  t.mw.getAgentName(ctx),
		}
	}

	result, marshalErr := marshalTaskResponse(fmt.Sprintf("Updated task #%s %s", params.TaskID, strings.Join(updatedFields, ", ")))
	return result, assignment, marshalErr
}

// updateBasicFields applies simple field updates (subject, description, activeForm, status).
func (t *taskUpdateTool) updateBasicFields(taskData *task, params *taskUpdateArgs, updatedFields []string) []string {
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
	return updatedFields
}

// updateDependencies validates and applies blocks/blockedBy changes with cycle detection.
// It uses the pre-loaded task list and builds a map for efficient cycle checks.
func (t *taskUpdateTool) updateDependencies(ctx context.Context, taskData *task, params *taskUpdateArgs, tasks []*task) ([]string, error) {
	taskMap := make(map[string]*task, len(tasks))
	for _, tk := range tasks {
		taskMap[tk.ID] = tk
	}
	// Point taskMap entry to the in-memory taskData so that cycle detection
	// for addBlockedBy can see addBlocks modifications made earlier in this call.
	taskMap[params.TaskID] = taskData

	var updatedFields []string

	if len(params.AddBlocks) > 0 {
		for _, blockedTaskID := range params.AddBlocks {
			if !isValidTaskID(blockedTaskID) {
				return nil, fmt.Errorf("%s validate blocked task ID failed, err: invalid format: %s", TaskUpdateToolName, blockedTaskID)
			}
			if _, exists := taskMap[blockedTaskID]; !exists {
				return nil, fmt.Errorf("%s update Task #%s blocks failed, err: target Task #%s not found", TaskUpdateToolName, params.TaskID, blockedTaskID)
			}
			if hasCyclicDependency(taskMap, params.TaskID, blockedTaskID) {
				return nil, fmt.Errorf("%s adding Task #%s to blocks of Task #%s would create a cyclic dependency", TaskUpdateToolName, blockedTaskID, params.TaskID)
			}
		}
		for _, blockedTaskID := range params.AddBlocks {
			if addErr := t.addDependencyToTask(ctx, blockedTaskID, params.TaskID, "blockedBy"); addErr != nil {
				return nil, fmt.Errorf("%s update Task #%s blocks failed, err: %w", TaskUpdateToolName, params.TaskID, addErr)
			}
		}
		taskData.Blocks = appendUnique(taskData.Blocks, params.AddBlocks...)
		updatedFields = append(updatedFields, "blocks")
	}
	if len(params.AddBlockedBy) > 0 {
		for _, blockingTaskID := range params.AddBlockedBy {
			if !isValidTaskID(blockingTaskID) {
				return nil, fmt.Errorf("%s validate blocking task ID failed, err: invalid format: %s", TaskUpdateToolName, blockingTaskID)
			}
			if _, exists := taskMap[blockingTaskID]; !exists {
				return nil, fmt.Errorf("%s update Task #%s blockedBy failed, err: target Task #%s not found", TaskUpdateToolName, params.TaskID, blockingTaskID)
			}
			if hasCyclicDependency(taskMap, blockingTaskID, params.TaskID) {
				return nil, fmt.Errorf("%s adding Task #%s to blockedBy of Task #%s would create a cyclic dependency", TaskUpdateToolName, blockingTaskID, params.TaskID)
			}
		}
		for _, blockingTaskID := range params.AddBlockedBy {
			if addErr := t.addDependencyToTask(ctx, blockingTaskID, params.TaskID, "blocks"); addErr != nil {
				return nil, fmt.Errorf("%s update Task #%s blockedBy failed, err: %w", TaskUpdateToolName, params.TaskID, addErr)
			}
		}
		taskData.BlockedBy = appendUnique(taskData.BlockedBy, params.AddBlockedBy...)
		updatedFields = append(updatedFields, "blockedBy")
	}

	return updatedFields, nil
}

// updateOwnerAndMetadata applies owner and metadata changes.
// In shared-task mode, it auto-sets owner to the current agent when marking a
// task as in_progress without explicitly providing an owner.
func (t *taskUpdateTool) updateOwnerAndMetadata(ctx context.Context, taskData *task, params *taskUpdateArgs, updatedFields []string) []string {
	if params.Owner != "" {
		if taskData.Owner != params.Owner {
			taskData.Owner = params.Owner
			updatedFields = append(updatedFields, "owner")
		}
	} else if t.mw.usesSharedTaskMode() && params.Status == taskStatusInProgress && taskData.Owner == "" {
		if agentName := t.mw.getAgentName(ctx); agentName != "" {
			params.Owner = agentName
			taskData.Owner = agentName
			updatedFields = append(updatedFields, "owner")
		}
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
	return updatedFields
}

// handleCompletion clears dependencies from the completed task using the pre-loaded task list,
// and returns additional updated fields.
func (t *taskUpdateTool) handleCompletion(ctx context.Context, taskData *task, taskID string, allTasks []*task) ([]string, error) {
	dependenciesCleared, clearErr := t.clearCompletedTaskDependencies(ctx, taskData, allTasks)
	if clearErr != nil {
		return nil, fmt.Errorf("%s clear dependencies for completed Task #%s failed, err: %w", TaskUpdateToolName, taskID, clearErr)
	}
	if dependenciesCleared {
		return []string{"blocks", "blockedBy"}, nil
	}
	return nil, nil
}

// addDependencyToTask reads a task, appends depID to the specified dependency field, and writes it back.
// field must be "blocks" or "blockedBy".
func (t *taskUpdateTool) addDependencyToTask(ctx context.Context, targetTaskID, depID, field string) error {
	baseDir := t.mw.resolveBaseDir(ctx)
	targetTask, err := readTask(ctx, t.mw.backend, baseDir, targetTaskID)
	if err != nil {
		return fmt.Errorf("updating %s: %w", field, err)
	}
	if targetTask == nil {
		return fmt.Errorf("updating %s: task #%s not found", field, targetTaskID)
	}

	switch field {
	case "blockedBy":
		targetTask.BlockedBy = appendUnique(targetTask.BlockedBy, depID)
	case "blocks":
		targetTask.Blocks = appendUnique(targetTask.Blocks, depID)
	}

	if err := writeTask(ctx, t.mw.backend, baseDir, targetTask); err != nil {
		return fmt.Errorf("updating %s: %w", field, err)
	}
	return nil
}

func (t *taskUpdateTool) clearCompletedTaskDependencies(ctx context.Context, completedTask *task, tasks []*task) (bool, error) {
	for _, otherTask := range tasks {
		if otherTask.ID == completedTask.ID {
			continue
		}

		modified := false
		newBlocks := make([]string, 0, len(otherTask.Blocks))
		for _, id := range otherTask.Blocks {
			if id != completedTask.ID {
				newBlocks = append(newBlocks, id)
			} else {
				modified = true
			}
		}

		newBlockedBy := make([]string, 0, len(otherTask.BlockedBy))
		for _, id := range otherTask.BlockedBy {
			if id != completedTask.ID {
				newBlockedBy = append(newBlockedBy, id)
			} else {
				modified = true
			}
		}

		if !modified {
			continue
		}

		otherTask.Blocks = newBlocks
		otherTask.BlockedBy = newBlockedBy

		if err := writeTask(ctx, t.mw.backend, t.mw.resolveBaseDir(ctx), otherTask); err != nil {
			return false, fmt.Errorf("clear dependencies: %w", err)
		}
	}

	dependenciesCleared := len(completedTask.Blocks) > 0 || len(completedTask.BlockedBy) > 0
	completedTask.Blocks = nil
	completedTask.BlockedBy = nil

	return dependenciesCleared, nil
}

// deleteAllTasksIfCompleted deletes all tasks if every task is completed.
func (t *taskUpdateTool) deleteAllTasksIfCompleted(ctx context.Context, tasks []*task) error {
	for _, tk := range tasks {
		if tk.Status != taskStatusCompleted {
			return nil
		}
	}

	for _, tk := range tasks {
		err := t.mw.backend.Delete(ctx, &DeleteRequest{
			FilePath: taskFileJoin(t.mw.resolveBaseDir(ctx), tk.ID),
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
