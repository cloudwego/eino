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
	"sort"
	"strings"
	"sync"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func newTaskListTool(backend Backend, baseDir string, lock *sync.Mutex) *taskListTool {
	return &taskListTool{Backend: backend, BaseDir: baseDir, lock: lock}
}

type taskListTool struct {
	Backend Backend
	BaseDir string
	lock    *sync.Mutex
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
		return nil, fmt.Errorf("%s list files in %s failed, err: %w", TaskListToolName, baseDir, err)
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
			return nil, fmt.Errorf("%s read task file %s failed, err: %w", TaskListToolName, file.Path, err)
		}

		taskData := &task{}
		err = sonic.UnmarshalString(content, taskData)
		if err != nil {
			return nil, fmt.Errorf("%s parse task file %s failed, err: %w", TaskListToolName, file.Path, err)
		}

		tasks = append(tasks, taskData)
	}

	// sort tasks by ID
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})

	return tasks, nil
}

func (t *taskListTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	tasks, err := listTasks(ctx, t.Backend, t.BaseDir)
	if err != nil {
		return "", err
	}

	if len(tasks) == 0 {
		resp := &taskOut{
			Result: "No tasks found.",
		}
		jsonResp, marshalErr := sonic.MarshalString(resp)
		if marshalErr != nil {
			return "", fmt.Errorf("%s marshal taskOut failed, err: %w", TaskListToolName, marshalErr)
		}
		return jsonResp, nil
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
		if len(taskData.BlockedBy) > 0 {
			blockedByIDs := make([]string, len(taskData.BlockedBy))
			for j, id := range taskData.BlockedBy {
				blockedByIDs[j] = "#" + id
			}
			result.WriteString(fmt.Sprintf(" [blocked by %s]", strings.Join(blockedByIDs, ", ")))
		}
	}

	resp := &taskOut{
		Result: result.String(),
	}

	jsonResp, err := sonic.MarshalString(resp)
	if err != nil {
		return "", fmt.Errorf("%s marshal taskOut failed, err: %w", TaskListToolName, err)
	}

	return jsonResp, nil
}

const TaskListToolName = "TaskList"
const taskListToolDesc = `Use this tool to list all tasks in the task list.

## When to Use This Tool

- To see what tasks are available to work on (status: 'pending', no owner, not blocked)
- To check overall progress on the project
- To find tasks that are blocked and need dependencies resolved
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
`

const taskListToolDescChinese = `使用此工具列出任务列表中的所有任务。

## 何时使用此工具

- 查看可以处理的任务（状态：'pending'，无所有者，未被阻塞）
- 检查项目的整体进度
- 查找被阻塞且需要解决依赖关系的任务
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
`
