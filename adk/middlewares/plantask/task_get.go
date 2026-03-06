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

func newTaskGetTool(backend Backend, baseDir string, lock *sync.Mutex) *taskGetTool {
	return &taskGetTool{Backend: backend, BaseDir: baseDir, lock: lock}
}

type taskGetTool struct {
	Backend Backend
	BaseDir string
	lock    *sync.Mutex
}

func (t *taskGetTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskGetToolDesc,
		Chinese: taskGetToolDescChinese,
	})

	return &schema.ToolInfo{
		Name: TaskGetToolName,
		Desc: desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"taskId": {
				Type:     schema.String,
				Desc:     "The ID of the task to retrieve",
				Required: true,
			},
		}),
	}, nil
}

type taskGetArgs struct {
	TaskID string `json:"taskId"`
}

func (t *taskGetTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	params := &taskGetArgs{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", err
	}

	if !isValidTaskID(params.TaskID) {
		return "", fmt.Errorf("%s validate task ID failed, err: invalid format: %s", TaskGetToolName, params.TaskID)
	}

	taskFileName := fmt.Sprintf("%s.json", params.TaskID)
	taskFilePath := filepath.Join(t.BaseDir, taskFileName)

	content, err := t.Backend.Read(ctx, &ReadRequest{
		FilePath: taskFilePath,
	})
	if err != nil {
		return "", fmt.Errorf("%s get Task #%s failed, err: %w", TaskGetToolName, params.TaskID, err)
	}

	taskData := &task{}
	err = sonic.UnmarshalString(content, taskData)
	if err != nil {
		return "", fmt.Errorf("%s get Task #%s failed, err: %w", TaskGetToolName, params.TaskID, err)
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Task #%s: %s\n", taskData.ID, taskData.Subject))
	result.WriteString(fmt.Sprintf("Status: %s\n", taskData.Status))
	result.WriteString(fmt.Sprintf("Description: %s\n", taskData.Description))

	if len(taskData.BlockedBy) > 0 {
		blockedByIDs := make([]string, len(taskData.BlockedBy))
		for i, id := range taskData.BlockedBy {
			blockedByIDs[i] = "#" + id
		}
		result.WriteString(fmt.Sprintf("Blocked by: %s\n", strings.Join(blockedByIDs, ", ")))
	}
	if len(taskData.Blocks) > 0 {
		blocksIDs := make([]string, len(taskData.Blocks))
		for i, id := range taskData.Blocks {
			blocksIDs[i] = "#" + id
		}
		result.WriteString(fmt.Sprintf("Blocks: %s\n", strings.Join(blocksIDs, ", ")))
	}
	if taskData.Owner != "" {
		result.WriteString(fmt.Sprintf("Owner: %s\n", taskData.Owner))
	}

	resp := &taskOut{
		Result: result.String(),
	}

	jsonResp, err := sonic.MarshalString(resp)
	if err != nil {
		return "", fmt.Errorf("%s marshal taskOut failed, err: %w", TaskGetToolName, err)
	}

	return jsonResp, nil
}

const TaskGetToolName = "TaskGet"
const taskGetToolDesc = `Use this tool to retrieve a task by its ID from the task list.

## When to Use This Tool

- When you need the full description and context before starting work on a task
- To understand task dependencies (what it blocks, what blocks it)
- After being assigned a task, to get complete requirements

## Output

Returns full task details:
- **subject**: Task title
- **description**: Detailed requirements and context
- **status**: 'pending', 'in_progress', or 'completed'
- **blocks**: Tasks waiting on this one to complete
- **blockedBy**: Tasks that must complete before this one can start

## Tips

- After fetching a task, verify its blockedBy list is empty before beginning work.
- Use TaskList to see all tasks in summary form.
`

const taskGetToolDescChinese = `使用此工具通过任务 ID 从任务列表中获取任务。

## 何时使用此工具

- 当你需要在开始处理任务之前获取完整的描述和上下文时
- 了解任务依赖关系（它阻塞什么，什么阻塞它）
- 被分配任务后，获取完整的需求

## 输出

返回完整的任务详情：
- **subject**：任务标题
- **description**：详细的需求和上下文
- **status**：'pending'、'in_progress' 或 'completed'
- **blocks**：等待此任务完成的任务
- **blockedBy**：必须在此任务开始之前完成的任务

## 提示

- 获取任务后，在开始工作之前验证其 blockedBy 列表是否为空。
- 使用 TaskList 查看所有任务的摘要形式。
`
