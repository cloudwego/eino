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

func newTaskGetTool(backend Backend, baseDir string, lock *sync.Mutex) *taskGetTool {
	return &taskGetTool{Backend: backend, BaseDir: baseDir, lock: lock}
}

type taskGetTool struct {
	Backend Backend
	BaseDir string
	lock    *sync.Mutex
}

func (t *taskGetTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: taskGetToolName,
		Desc: taskGetToolDesc,
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
		return "", fmt.Errorf("invalid task ID format: %s", params.TaskID)
	}

	taskFileName := fmt.Sprintf("%s.json", params.TaskID)
	taskFilePath := filepath.Join(t.BaseDir, taskFileName)

	content, err := t.Backend.Read(ctx, &ReadRequest{
		FilePath: taskFilePath,
	})
	if err != nil {
		return "", fmt.Errorf("%s get Task #%s failed, err: %w", taskGetToolName, params.TaskID, err)
	}

	taskData := &task{}
	err = sonic.UnmarshalString(content, taskData)
	if err != nil {
		return "", fmt.Errorf("%s get Task #%s failed, err: %w", taskGetToolName, params.TaskID, err)
	}

	// 	Task #38: 创建前端页面框架
	// Status: pending
	// Description: 搭建前端项目，配置路由，创建基础布局组件（头部、导航、底部）。
	// Blocked by: #30
	// Blocks: #39, #40, #41, #42, #43
	var result strings.Builder
	result.WriteString(fmt.Sprintf("Task #%s: %s\n", taskData.ID, taskData.Subject))
	result.WriteString(fmt.Sprintf("Status: %s\n", taskData.Status))
	result.WriteString(fmt.Sprintf("Description: %s", taskData.Description))

	if len(taskData.BlockedBy) > 0 {
		blockedByIDs := make([]string, len(taskData.BlockedBy))
		for i, id := range taskData.BlockedBy {
			blockedByIDs[i] = "#" + id
		}
		result.WriteString(fmt.Sprintf("\nBlocked by: %s", strings.Join(blockedByIDs, ", ")))
	}
	if len(taskData.Blocks) > 0 {
		blocksIDs := make([]string, len(taskData.Blocks))
		for i, id := range taskData.Blocks {
			blocksIDs[i] = "#" + id
		}
		result.WriteString(fmt.Sprintf("\nBlocks: %s", strings.Join(blocksIDs, ", ")))
	}

	return result.String(), nil
}

const taskGetToolName = "TaskGet"
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
