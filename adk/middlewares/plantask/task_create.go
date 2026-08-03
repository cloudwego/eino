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
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskCreateToolDesc,
		Chinese: taskCreateToolDescChinese,
	})

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
				Desc:     "What needs to be done",
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
			if content.Content != "" {
				var val int64
				if _, scanErr := fmt.Sscanf(content.Content, "%d", &val); scanErr == nil {
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
			return "", fmt.Errorf("task #%d already exists", taskID)
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
- **description**: What needs to be done
- **activeForm** (optional): Present continuous form shown in the spinner when the task is in_progress (e.g., "Fixing authentication bug"). If omitted, the spinner shows the subject instead.

All tasks are created with status ` + "`" + `pending` + "`" + `.

## Tips

- Create tasks with clear, specific subjects that describe the outcome
- After creating tasks, use TaskUpdate to set up dependencies (blocks/blockedBy) if needed
- Check TaskList first to avoid creating duplicate tasks
`

const taskCreateToolDescChinese = `使用此工具为当前编码会话创建结构化的任务列表。这有助于你跟踪进度、组织复杂任务，并向用户展示工作的完整性。
它也帮助用户理解任务进展以及其请求的整体进度。

## 何时使用本工具
在以下场景主动使用：
- 复杂的多步骤任务 —— 当任务需要 3 个或更多不同步骤或操作时
- 非平凡的复杂任务 —— 需要仔细规划或多次操作的任务
- 计划模式 —— 使用计划模式时，创建任务列表来跟踪工作
- 用户明确要求待办列表 —— 当用户直接要求你使用待办列表时
- 用户提供多个任务 —— 当用户给出一组待办事项（编号或逗号分隔）时
- 收到新指令后 —— 立即将用户需求记录为任务
- 开始处理某个任务时 —— 在动手之前先标记为 in_progress
- 完成某个任务后 —— 标记为 completed，并补充实现过程中发现的后续任务

## 何时不要使用本工具
在以下情况跳过：
- 只有单个、直接了当的任务
- 任务很琐碎，跟踪它没有组织上的收益
- 任务可在少于 3 个琐碎步骤内完成
- 任务纯属对话或信息性质
注意：如果只有一个琐碎任务要做，就不要用本工具。这种情况下直接做更好。

## 任务字段
- **subject**：一个简短、可执行的祈使句标题（例如 "Fix authentication bug in login flow"）
- **description**：需要做什么
- **activeForm**（可选）：任务处于 in_progress 时在 spinner 中显示的现在进行时形式（例如 "Fixing authentication bug"）。省略时 spinner 显示 subject。
所有任务创建时状态均为 ` + "`" + `pending` + "`" + `。

## 提示
- 创建任务时用清晰、具体、描述结果的 subject
- 创建任务后，如有需要用 TaskUpdate 设置依赖（blocks/blockedBy）
- 先用 TaskList 检查，避免创建重复任务`
