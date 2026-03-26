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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func newTaskListTool(mw *middleware, turnLock *sync.RWMutex) *taskListTool {
	return &taskListTool{mw: mw, turnLock: turnLock}
}

type taskListTool struct {
	mw       *middleware
	turnLock *sync.RWMutex
}

func (t *taskListTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskListToolDesc,
		Chinese: taskListToolDescChinese,
	})

	return &schema.ToolInfo{
		Name:        TaskListToolName,
		Desc:        desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func listTasks(ctx context.Context, backend Backend, baseDir string) ([]*task, error) {
	files, err := backend.LsInfo(ctx, &LsInfoRequest{
		Path: baseDir,
	})
	if err != nil {
		return nil, fmt.Errorf("list files in %s failed: %w", baseDir, err)
	}

	var tasks []*task
	for _, file := range files {
		fileName := filepath.Base(file.Path)
		if !strings.HasSuffix(fileName, ".json") {
			continue
		}

		taskID := strings.TrimSuffix(fileName, ".json")
		if !isValidTaskID(taskID) {
			continue
		}

		content, err := backend.Read(ctx, &ReadRequest{
			FilePath: file.Path,
		})
		if err != nil {
			return nil, fmt.Errorf("read task file %s failed: %w", file.Path, err)
		}

		taskData := &task{}
		err = sonic.UnmarshalString(content.Content, taskData)
		if err != nil {
			log.Printf("[plantask] parse task file %s failed, skipping: %v", file.Path, err)
			continue
		}

		tasks = append(tasks, taskData)
	}

	// sort tasks by numeric ID to ensure the order is stable.
	sort.Slice(tasks, func(i, j int) bool {
		idI, _ := strconv.ParseInt(tasks[i].ID, 10, 64)
		idJ, _ := strconv.ParseInt(tasks[j].ID, 10, 64)
		return idI < idJ
	})

	return tasks, nil
}

// filterVisibleTasks removes internal tasks (metadata._internal == true) from the list.
// Internal tasks are automatically created by the team system when spawning teammates,
// used for internal coordination to track teammate status (subject is agent name, status is in_progress),
// not business tasks created by users via TaskCreate tool.
//
// Filtering rules:
//   - TaskList tool call: filtered (invisible) — prevents internal tasks from interfering with normal todo management.
//   - UI status line/todo display: filtered (invisible).
//   - TaskUpdate (by ID): not filtered (visible) — allows system to update internal task status by ID.
//   - TaskGet (by ID): not filtered (visible).
//   - Underlying storage API: not filtered (visible).
func filterVisibleTasks(tasks []*task) []*task {
	filtered := make([]*task, 0, len(tasks))
	for _, tk := range tasks {
		if !isInternalTask(tk) {
			filtered = append(filtered, tk)
		}
	}
	return filtered
}

func (t *taskListTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	lock := t.mw.getLock(t.turnLock)
	lock.RLock()
	defer lock.RUnlock()

	tasks, err := listTasks(ctx, t.mw.backend, t.mw.resolveBaseDir(ctx))
	if err != nil {
		return "", fmt.Errorf("%s %w", TaskListToolName, err)
	}

	// Filter out internal tasks (e.g., teammate shadow tasks)
	tasks = filterVisibleTasks(tasks)

	if len(tasks) == 0 {
		return marshalTaskResponse("No tasks found.")
	}

	// Build a set of completed task IDs so we can filter them out of blockedBy lists.
	completedTaskIDs := make(map[string]struct{})
	for _, taskData := range tasks {
		if taskData.Status == taskStatusCompleted {
			completedTaskIDs[taskData.ID] = struct{}{}
		}
	}

	var result strings.Builder
	for i, taskData := range tasks {
		if i > 0 {
			result.WriteString("\n")
		}
		result.WriteString(fmt.Sprintf("#%s [%s] %s", taskData.ID, taskData.Status, taskData.Subject))
		if taskData.Owner != "" {
			result.WriteString(fmt.Sprintf(" [owner: %s]", taskData.Owner))
		}

		// Filter out completed tasks from blockedBy
		var activeBlockedBy []string
		for _, id := range taskData.BlockedBy {
			if _, resolved := completedTaskIDs[id]; !resolved {
				activeBlockedBy = append(activeBlockedBy, "#"+id)
			}
		}
		if len(activeBlockedBy) > 0 {
			result.WriteString(fmt.Sprintf(" [blocked by %s]", strings.Join(activeBlockedBy, ", ")))
		}
	}

	return marshalTaskResponse(result.String())
}

const TaskListToolName = "TaskList"
const taskListToolDesc = `Use this tool to list all tasks in the task list.

## When to Use This Tool

- To see what tasks are available to work on (status: 'pending', no owner, not blocked)
- To check overall progress on the project
- To find tasks that are blocked and need dependencies resolved
- Before assigning tasks to teammates, to see what's available
- After completing a task, to check for newly unblocked work or claim the next available task
- **Prefer working on tasks in ID order** (lowest ID first) when multiple tasks are available, as earlier tasks often set up context for later ones

## Output

Returns a summary of each task:
- **id**: Task identifier (use with TaskGet, TaskUpdate)
- **subject**: Brief description of the task
- **status**: 'pending', 'in_progress', or 'completed'
- **owner**: Agent ID if assigned, empty if available
- **blockedBy**: List of open task IDs that must be resolved first (tasks with blockedBy cannot be claimed until dependencies resolve)

Use TaskGet with a specific task ID to view full details including description and comments.

## Teammate Workflow

When working as a teammate:
1. After completing your current task, call TaskList to find available work
2. Look for tasks with status 'pending', no owner, and empty blockedBy
3. **Prefer tasks in ID order** (lowest ID first) when multiple tasks are available, as earlier tasks often set up context for later ones
4. Claim an available task using TaskUpdate (set owner to your name), or wait for leader assignment
5. If blocked, focus on unblocking tasks or notify the team lead
`

const taskListToolDescChinese = `使用此工具列出任务列表中的所有任务。

## 何时使用此工具

- 查看可以处理的任务（状态：'pending'，无所有者，未被阻塞）
- 检查项目的整体进度
- 查找被阻塞且需要解决依赖关系的任务
- 分配任务给队友之前，查看可用的任务
- 完成任务后，检查新解除阻塞的工作或认领下一个可用任务
- **优先按 ID 顺序处理任务**（最小 ID 优先），当有多个任务可用时，因为较早的任务通常为后续任务建立上下文

## 输出

返回每个任务的摘要：
- **id**：任务标识符（与 TaskGet、TaskUpdate 一起使用）
- **subject**：任务的简要描述
- **status**：'pending'、'in_progress' 或 'completed'
- **owner**：如果已分配则为代理 ID，如果可用则为空
- **blockedBy**：必须首先解决的开放任务 ID 列表（具有 blockedBy 的任务在依赖关系解决之前无法被认领）

使用 TaskGet 配合特定任务 ID 查看完整详情，包括描述和评论。

## 队友工作流程

作为队友工作时：
1. 完成当前任务后，调用 TaskList 查找可用的工作
2. 查找状态为 'pending'、无所有者且 blockedBy 为空的任务
3. **优先按 ID 顺序处理任务**（最小 ID 优先），当有多个任务可用时，因为较早的任务通常为后续任务建立上下文
4. 使用 TaskUpdate 认领可用任务（将 owner 设置为你的名字），或等待领导分配
5. 如果被阻塞，专注于解除阻塞任务或通知团队领导
`
