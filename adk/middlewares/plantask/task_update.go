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
	"sort"
	"strconv"
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
	if err := t.mw.checkGuard(ctx); err != nil {
		return "", fmt.Errorf("%s %w", TaskUpdateToolName, err)
	}

	result, assignment, err := t.doUpdate(ctx, argumentsInJSON)
	if err != nil {
		return "", err
	}

	// Notify assignee outside the lock to avoid blocking other task operations
	// during mailbox I/O. The owner has already been persisted, so a notification
	// failure must not be silently swallowed: the task would be assigned without the
	// assignee ever being told. Surface the failure in the tool result so the model
	// can re-send the message, instead of returning an unqualified success.
	if assignment != nil && t.mw.onTaskAssigned != nil {
		if notifyErr := t.mw.onTaskAssigned(ctx, *assignment); notifyErr != nil {
			t.mw.effectiveLogger().Printf("[plantask] notify task assignment (task %s -> %s) failed: %v",
				assignment.TaskID, assignment.Owner, notifyErr)
			warning := fmt.Sprintf("task #%s assigned to %q but the assignment notification could not be delivered (%v); the assignee may be unaware, consider re-sending the message",
				assignment.TaskID, assignment.Owner, notifyErr)
			withWarning, marshalErr := marshalTaskResponseWithWarning(extractTaskResult(result), warning)
			if marshalErr != nil {
				return result, nil
			}
			return withWarning, nil
		}
	}

	return result, nil
}

// extractTaskResult parses the result string portion of a marshalled taskOut so a
// notification warning can be attached without losing the original result text.
func extractTaskResult(marshalled string) string {
	out := &taskOut{}
	if err := sonic.UnmarshalString(marshalled, out); err != nil {
		return marshalled
	}
	return out.Result
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
	// (dependency updates, completion cleanup, or all-completed check). Every
	// graph mutation below runs against this single in-memory snapshot and is
	// flushed together at the end (see persistGraph), so a validation or
	// cycle-detection failure never persists a partial change, and a successful
	// update never leaves a one-sided dependency edge from interleaved per-file
	// writes.
	needsTaskList := len(params.AddBlocks) > 0 || len(params.AddBlockedBy) > 0 || params.Status == taskStatusCompleted
	var allTasks []*task
	if needsTaskList {
		var listErr error
		allTasks, listErr = listTasks(ctx, t.mw.backend, baseDir, t.mw.logger)
		if listErr != nil {
			return "", nil, fmt.Errorf("%s list tasks failed, err: %w", TaskUpdateToolName, listErr)
		}
		// Replace the allTasks entry for the current task with taskData so that
		// in-memory modifications (status, dependency edges, cleared edges) are
		// visible to downstream consumers (cycle detection, completion cleanup,
		// deleteAllTasksIfCompleted) and to the batched write-back below.
		for i, tk := range allTasks {
			if tk.ID == params.TaskID {
				allTasks[i] = taskData
				break
			}
		}
	}

	// dirty collects every task object mutated by this update so they can be
	// flushed in one batch. taskData is always dirty (it is the task being
	// updated); dependency and completion handling add the counterpart tasks
	// they touch. Keyed by ID so a task touched by both phases is written once.
	dirty := map[string]*task{params.TaskID: taskData}

	var updatedFields []string

	updatedFields = t.updateBasicFields(taskData, params, updatedFields)

	if len(params.AddBlocks) > 0 || len(params.AddBlockedBy) > 0 {
		fields, depErr := t.updateDependencies(taskData, params, allTasks, dirty)
		if depErr != nil {
			return "", nil, depErr
		}
		updatedFields = append(updatedFields, fields...)
	}

	fields, ownerErr := t.updateOwnerAndMetadata(ctx, taskData, params, updatedFields)
	if ownerErr != nil {
		return "", nil, ownerErr
	}
	updatedFields = fields

	if params.Status == taskStatusCompleted {
		// Completion clears this task's edges and removes references to it from
		// its counterparts, all in memory against the same allTasks snapshot the
		// dependency phase mutated; touched counterparts are added to dirty so
		// they are flushed together with taskData below.
		updatedFields = append(updatedFields, t.handleCompletion(taskData, allTasks, dirty)...)
	}

	// Flush all mutated tasks in a single batch. Nothing above writes to the
	// backend, so a validation or cycle-detection failure aborts before any
	// persisted change. On a mid-batch backend failure persistGraph returns an
	// error naming the tasks it did and did not persist; because every mutation
	// is idempotent (appendUnique / reference removal), retrying the same
	// TaskUpdate reconciles any one-sided edge.
	if err := t.persistGraph(ctx, baseDir, dirty); err != nil {
		return "", nil, err
	}

	// Check if all tasks are completed. Reuse the in-memory allTasks slice:
	// handleCompletion may have modified task objects (cleared dependencies),
	// but status fields remain accurate for the all-completed check.
	// Cleanup is best-effort: the task graph has already been persisted above,
	// so a cleanup failure should not fail the main operation.
	//
	// Only the single-agent ("scratch pad") mode auto-clears the whole task list
	// once everything is completed. In shared-task mode the task directory is
	// shared by the entire team (see WithTaskBaseDirResolver in the team runner),
	// so one teammate completing its last task while the team's tasks all happen
	// to be completed must NOT wipe the team-wide task graph: that would be
	// non-deterministic (it depends on which member finishes last) and would
	// strip the leader's visibility into completed work during the gap before it
	// queues the next batch. Shared-mode tasks are removed explicitly via the
	// "deleted" status instead.
	if params.Status == taskStatusCompleted && !t.mw.usesSharedTaskMode() {
		if checkErr := t.deleteAllTasksIfCompleted(ctx, allTasks); checkErr != nil {
			t.mw.effectiveLogger().Printf("[plantask] auto-delete all completed tasks failed, err: %v", checkErr)
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

// updateDependencies validates and applies blocks/blockedBy changes with cycle
// detection. It mutates only in-memory task objects (the pre-loaded snapshot)
// and records every counterpart it touches in dirty so doUpdate can flush the
// whole graph in a single batch; it performs no backend writes itself.
func (t *taskUpdateTool) updateDependencies(taskData *task, params *taskUpdateArgs, tasks []*task, dirty map[string]*task) ([]string, error) {
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
			// taskData blocks blockedTaskID, so blockedTaskID is blockedBy taskData.
			target := taskMap[blockedTaskID]
			target.BlockedBy = appendUnique(target.BlockedBy, params.TaskID)
			dirty[target.ID] = target
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
			// taskData is blockedBy blockingTaskID, so blockingTaskID blocks taskData.
			target := taskMap[blockingTaskID]
			target.Blocks = appendUnique(target.Blocks, params.TaskID)
			dirty[target.ID] = target
		}
		taskData.BlockedBy = appendUnique(taskData.BlockedBy, params.AddBlockedBy...)
		updatedFields = append(updatedFields, "blockedBy")
	}

	return updatedFields, nil
}

// updateOwnerAndMetadata applies owner and metadata changes.
// In shared-task mode, it auto-sets owner to the current agent when marking a
// task as in_progress without explicitly providing an owner.
//
// When an explicit non-empty owner is supplied in shared-task mode and an owner
// validator is configured, the validator is consulted first; a rejection aborts
// the whole update so the task is never persisted with an unknown owner (which
// would otherwise create an orphaned task and a notification no one consumes).
func (t *taskUpdateTool) updateOwnerAndMetadata(ctx context.Context, taskData *task, params *taskUpdateArgs, updatedFields []string) ([]string, error) {
	if params.Owner != "" {
		if t.mw.usesSharedTaskMode() && t.mw.ownerValidator != nil {
			if err := t.mw.ownerValidator(ctx, params.Owner); err != nil {
				return nil, fmt.Errorf("%s validate owner %q failed, err: %w", TaskUpdateToolName, params.Owner, err)
			}
		}
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
	return updatedFields, nil
}

// handleCompletion clears dependencies from the completed task and removes
// references to it from its counterparts, all in memory against the pre-loaded
// snapshot. Touched counterparts are recorded in dirty for the batched flush.
func (t *taskUpdateTool) handleCompletion(taskData *task, allTasks []*task, dirty map[string]*task) []string {
	if t.clearCompletedTaskDependencies(taskData, allTasks, dirty) {
		return []string{"blocks", "blockedBy"}
	}
	return nil
}

// persistGraph writes every task in dirty back to the backend in one batch. It
// is the single write surface for a task-graph update: all mutation and
// validation happens in memory before this is called, so a failure here is the
// only persistence error a TaskUpdate can surface. On a mid-batch failure it
// reports which tasks were persisted and which were not, so a retry of the same
// (idempotent) update can reconcile any one-sided edge left behind.
func (t *taskUpdateTool) persistGraph(ctx context.Context, baseDir string, dirty map[string]*task) error {
	// Write in deterministic ID order so partial-failure reporting is stable.
	ids := make([]string, 0, len(dirty))
	for id := range dirty {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ni, errI := strconv.ParseInt(ids[i], 10, 64)
		nj, errJ := strconv.ParseInt(ids[j], 10, 64)
		if errI == nil && errJ == nil {
			return ni < nj
		}
		return ids[i] < ids[j]
	})

	var persisted []string
	for _, id := range ids {
		if err := writeTask(ctx, t.mw.backend, baseDir, dirty[id]); err != nil {
			remaining := ids[len(persisted):]
			return fmt.Errorf("%s persist task graph failed at Task #%s (persisted %v, not persisted %v); retry the same update to reconcile, err: %w",
				TaskUpdateToolName, id, persisted, remaining, err)
		}
		persisted = append(persisted, id)
	}
	return nil
}

// clearCompletedTaskDependencies removes references to completedTask from every
// other task's blocks/blockedBy in memory, recording each modified task in
// dirty, then clears completedTask's own edges. It reports whether completedTask
// had any edges to clear. No backend writes happen here; persistGraph flushes.
func (t *taskUpdateTool) clearCompletedTaskDependencies(completedTask *task, tasks []*task, dirty map[string]*task) bool {
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
		dirty[otherTask.ID] = otherTask
	}

	dependenciesCleared := len(completedTask.Blocks) > 0 || len(completedTask.BlockedBy) > 0
	completedTask.Blocks = nil
	completedTask.BlockedBy = nil

	return dependenciesCleared
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
