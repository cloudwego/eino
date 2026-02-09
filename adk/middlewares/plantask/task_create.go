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

	"github.com/cloudwego/eino/adk/internal"
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

type taskCreateArgs struct {
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

func (t *taskCreateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	desc, err := internal.SelectPrompt(internal.I18nPrompts{
		English: taskCreateToolDesc,
		Chinese: taskCreateToolDescChinese,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to select prompt: %w", err)
	}

	return &schema.ToolInfo{
		Name: TaskCreateToolName,
		Desc: desc,
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
		return "", fmt.Errorf("%s list files in %s failed, err: %w", TaskCreateToolName, t.BaseDir, err)
	}

	highwatermark := int64(0)
	for _, file := range files {
		fileName := filepath.Base(file.Path)
		if fileName == highWatermarkFileName {
			content, readErr := t.Backend.Read(ctx, &ReadRequest{
				FilePath: file.Path,
			})
			if readErr != nil {
				return "", fmt.Errorf("%s read highwatermark file %s failed, err: %w", TaskCreateToolName, file.Path, readErr)
			}
			if content != "" {
				var val int64
				if _, scanErr := fmt.Sscanf(content, "%d", &val); scanErr == nil {
					highwatermark = val
				}
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
		return "", fmt.Errorf("%s marshal task #%d failed, err: %w", TaskCreateToolName, taskID, err)
	}

	//  Write highwatermark file first
	highwatermarkPath := filepath.Join(t.BaseDir, highWatermarkFileName)
	err = t.Backend.Write(ctx, &WriteRequest{
		FilePath: highwatermarkPath,
		Content:  fmt.Sprintf("%d", taskID),
	})
	if err != nil {
		return "", fmt.Errorf("%s update highwatermark file %s failed, err: %w", TaskCreateToolName, highwatermarkPath, err)
	}

	taskFilePath := filepath.Join(t.BaseDir, taskFileName)
	err = t.Backend.Write(ctx, &WriteRequest{
		FilePath: taskFilePath,
		Content:  taskData,
	})
	if err != nil {
		return "", fmt.Errorf("%s create Task #%d failed, err: %w", TaskCreateToolName, taskID, err)
	}

	resp := &taskOut{
		Result: fmt.Sprintf("Task #%d created successfully: %s", taskID, params.Subject),
	}

	jsonResp, err := sonic.MarshalString(resp)
	if err != nil {
		return "", fmt.Errorf("%s marshal taskOut failed, err: %w", TaskCreateToolName, err)
	}

	return jsonResp, nil
}

const TaskCreateToolName = "TaskCreate"
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

const taskCreateToolDescChinese = `使用此工具为当前编码会话创建结构化的任务列表。这有助于跟踪进度、组织复杂任务，并向用户展示工作的完整性。
它还帮助用户了解任务的进度和请求的整体进展。

## 何时使用此工具

在以下场景中主动使用此工具：

- 复杂的多步骤任务 - 当任务需要 3 个或更多不同的步骤或操作时
- 非简单的复杂任务 - 需要仔细规划或多个操作的任务
- 计划模式 - 使用计划模式时，创建任务列表来跟踪工作
- 用户明确要求待办列表 - 当用户直接要求使用待办列表时
- 用户提供多个任务 - 当用户提供待办事项列表时（编号或逗号分隔）
- 收到新指令后 - 立即将用户需求记录为任务
- 开始处理任务时 - 在开始工作之前将其标记为 in_progress
- 完成任务后 - 将其标记为已完成，并添加实施过程中发现的任何后续任务

## 何时不使用此工具

在以下情况下跳过使用此工具：
- 只有一个简单直接的任务
- 任务很简单，跟踪它没有组织上的好处
- 任务可以在少于 3 个简单步骤内完成
- 任务纯粹是对话性或信息性的

注意：如果只有一个简单任务要做，不应该使用此工具。在这种情况下，直接完成任务更好。

## 任务字段

- **subject**：简短的、可操作的标题，使用祈使句形式（例如，"修复登录流程中的认证错误"）
- **description**：需要完成的工作的详细描述，包括上下文和验收标准
- **activeForm**：任务处于 in_progress 状态时在加载动画中显示的现在进行时形式（例如，"正在修复认证错误"）。这会在你处理任务时显示给用户。

**重要**：创建任务时始终提供 activeForm。subject 应该是祈使句（"运行测试"），而 activeForm 应该是现在进行时（"正在运行测试"）。所有任务创建时状态为 "pending"。

## 提示

- 创建具有清晰、具体主题的任务，描述预期结果
- 在描述中包含足够的细节，以便其他代理能够理解并完成任务
- 创建任务后，如果需要，使用 TaskUpdate 设置依赖关系（blocks/blockedBy）
- 先检查 TaskList 以避免创建重复任务
`
