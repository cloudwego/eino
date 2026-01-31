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
	"sync"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func newTaskCreateTool(backend Backend, baseDir string, lock *sync.Mutex) *taskCreateTool {
	return &taskCreateTool{Backend: backend, BaseDir: baseDir, lock: lock}
}

type taskCreateTool struct {
	Backend Backend
	BaseDir string
	lock    *sync.Mutex
}

func (t *taskCreateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: taskCreateToolName,
		Desc: taskCreateToolDesc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"subject": {
				Type:     schema.String,
				Desc:     "A brief title for the task",
				Required: true,
			},
			"description": {
				Type:     schema.String,
				Desc:     "A detailed description of what needs to be done",
				Required: true,
			},
			"activeForm": {
				Type:     schema.String,
				Desc:     "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")",
				Required: false,
			},
			"metadata": {
				Type: schema.Object,
				Desc: "Arbitrary metadata to attach to the task",
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

type taskCreateArgs struct {
	Subject     string                 `json:"subject"`
	Description string                 `json:"description"`
	ActiveForm  string                 `json:"activeForm,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

func (t *taskCreateTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	params := &taskCreateArgs{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", err
	}

	files, err := t.Backend.LsInfo(ctx, &LsInfoRequest{
		Path: t.BaseDir,
	})
	if err != nil {
		return "", fmt.Errorf("%s list files in %s failed, err: %w", taskCreateToolName, t.BaseDir, err)
	}

	highwatermark := int64(0)
	for _, file := range files {
		fileName := filepath.Base(file.Path)
		if fileName == highWatermarkFileName {
			content, err := t.Backend.Read(ctx, &ReadRequest{
				FilePath: file.Path,
			})
			if err != nil {
				return "", fmt.Errorf("%s read highwatermark file %s failed, err: %w", taskCreateToolName, file.Path, err)
			}
			if content != "" {
				var val int64
				if _, err := fmt.Sscanf(content, "%d", &val); err == nil {
					highwatermark = val
				}
				// if content is empty or not a valid int64, highwatermark is 0
			}
			break
		}
	}

	taskID := highwatermark + 1
	taskFileName := fmt.Sprintf("%d.json", taskID)

	for _, file := range files {
		fileName := filepath.Base(file.Path)
		if fileName == taskFileName {
			return "", fmt.Errorf("Task #%d already exists", taskID)
		}
	}

	newTask := &task{
		ID:          fmt.Sprintf("%d", taskID),
		Subject:     params.Subject,
		Description: params.Description,
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
		ActiveForm:  params.ActiveForm,
		Metadata:    params.Metadata,
	}

	taskData, err := sonic.MarshalString(newTask)
	if err != nil {
		return "", fmt.Errorf("%s marshal task #%d failed, err: %w", taskCreateToolName, taskID, err)
	}

	taskFilePath := filepath.Join(t.BaseDir, taskFileName)
	err = t.Backend.Write(ctx, &WriteRequest{
		FilePath: taskFilePath,
		Content:  taskData,
	})
	if err != nil {
		return "", fmt.Errorf("%s create Task #%d failed, err: %w", taskCreateToolName, taskID, err)
	}

	highwatermarkPath := filepath.Join(t.BaseDir, highWatermarkFileName)
	err = t.Backend.Write(ctx, &WriteRequest{
		FilePath: highwatermarkPath,
		Content:  fmt.Sprintf("%d", taskID),
	})
	if err != nil {
		return "", fmt.Errorf("%s update highwatermark file %s failed, err: %w", taskCreateToolName, highwatermarkPath, err)
	}

	return fmt.Sprintf("Task #%d created successfully: %s", taskID, params.Subject), nil
}

const taskCreateToolName = "TaskCreate"
const taskCreateToolDesc = `Use this tool to create a structured task list for your current coding session. This helps you track progress, organize complex tasks, and demonstrate thoroughness to the user.
It also helps the user understand the progress of the task and overall progress of their requests.

## When to Use This Tool

Use this tool proactively in these scenarios:

- Complex multi-step tasks - When a task requires 3 or more distinct steps or actions
- Non-trivial and complex tasks - Tasks that require careful planning or multiple operations
- Plan mode - When using plan mode, create a task list to track the work
- User explicitly requests todo list - When the user directly asks you to use the todo list
- User provides multiple tasks - When users provide a list of things to be done (numbered or comma-separated)
- After receiving new instructions - Immediately capture user requirements as tasks
- When you start working on a task - Mark it as in_progress BEFORE beginning work
- After completing a task - Mark it as completed and add any new follow-up tasks discovered during implementation

## When NOT to Use This Tool

Skip using this tool when:
- There is only a single, straightforward task
- The task is trivial and tracking it provides no organizational benefit
- The task can be completed in less than 3 trivial steps
- The task is purely conversational or informational

NOTE that you should not use this tool if there is only one trivial task to do. In this case you are better off just doing the task directly.

## Task Fields

- **subject**: A brief, actionable title in imperative form (e.g., "Fix authentication bug in login flow")
- **description**: Detailed description of what needs to be done, including context and acceptance criteria
- **activeForm**: Present continuous form shown in spinner when task is in_progress (e.g., "Fixing authentication bug"). This is displayed to the user while you work on the task.

**IMPORTANT**: Always provide activeForm when creating tasks. The subject should be imperative ("Run tests") while activeForm should be present continuous ("Running tests"). All tasks are created with status "pending".

## Tips

- Create tasks with clear, specific subjects that describe the outcome
- Include enough detail in the description for another agent to understand and complete the task
- After creating tasks, use TaskUpdate to set up dependencies (blocks/blockedBy) if needed
- Check TaskList first to avoid creating duplicate tasks
`
