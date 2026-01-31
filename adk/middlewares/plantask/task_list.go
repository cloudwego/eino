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

func newTaskListTool(backend Backend, baseDir string, lock *sync.Mutex) *taskListTool {
	return &taskListTool{Backend: backend, BaseDir: baseDir, lock: lock}
}

type taskListTool struct {
	Backend Backend
	BaseDir string
	lock    *sync.Mutex
}

func (t *taskListTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        taskListToolName,
		Desc:        taskListToolDesc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func listTasks(ctx context.Context, backend Backend, baseDir string) ([]*task, error) {
	files, err := backend.LsInfo(ctx, &LsInfoRequest{
		Path: baseDir,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list files in %s: %w", baseDir, err)
	}

	var tasks []*task
	for _, file := range files {
		fileName := filepath.Base(file.Path)
		if !strings.HasSuffix(fileName, ".json") {
			continue
		}

		content, err := backend.Read(ctx, &ReadRequest{
			FilePath: file.Path,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to read task file %s: %w", file.Path, err)
		}

		taskData := &task{}
		err = sonic.UnmarshalString(content, taskData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse task file %s: %w", file.Path, err)
		}

		tasks = append(tasks, taskData)
	}

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
		return "No tasks found.", nil
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

	return result.String(), nil
}

const taskListToolName = "TaskList"
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
