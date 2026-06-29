/*
 * Copyright 2026 CloudWeGo Authors
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

package backgroundtask

const (
	taskOutputToolDescription = `Retrieve the output and status of a running or completed background task.

- Takes a task_id parameter identifying the task
- Returns the task's output along with its status and any error
- Use this tool to check on a background task or retrieve its result by task_id
`

	taskOutputToolDescriptionChinese = `获取正在运行或已完成的后台任务的输出与状态。

- 接受 task_id 参数来标识任务
- 返回该任务的输出，以及其状态和任何错误信息
- 当你需要查询后台任务或通过 task_id 获取其结果时使用此工具
`

	taskStopToolDescription = `Stop a running background task by its ID.

- Takes a task_id parameter identifying the task to stop
- Returns a success or failure status
- Use this tool when you need to cancel a long-running background task
`

	taskStopToolDescriptionChinese = `通过 ID 停止正在运行的后台任务。

- 接受 task_id 参数来标识要停止的任务
- 返回成功或失败状态
- 当你需要取消一个长时间运行的后台任务时使用此工具
`

	// The instruction is assembled from these pieces so the per-tool sentences name
	// the tools as actually registered: a tool renamed via ToolConfig.Name is
	// referenced by that name, and a disabled tool's sentence is omitted entirely.
	// Keeping the model's instructions in sync with the live tool set avoids telling
	// it to call a tool that was renamed or no longer exists.
	backgroundTaskPromptHeader = `
## Background Task Management
- Some tools can launch work in the background. Background tasks keep running after the
  tool call returns; you will be notified when they complete.`

	// %s is the registered task_output tool name.
	backgroundTaskOutputLine = "\n- Use the %s tool to check a background task's status or retrieve its result by task_id."

	// %s is the registered task_stop tool name.
	backgroundTaskStopLine = "\n- Use the %s tool to cancel a running background task by task_id."

	backgroundTaskPromptFooter = "\n- These tasks are running executions, not planning to-dos.\n"

	backgroundTaskPromptHeaderChinese = `
## 后台任务管理
- 部分工具可以在后台启动任务。后台任务在工具调用返回后会继续运行；任务完成时你将收到通知。`

	// %s is the registered task_output tool name.
	backgroundTaskOutputLineChinese = "\n- 使用 %s 工具通过 task_id 查询后台任务的状态或获取其结果。"

	// %s is the registered task_stop tool name.
	backgroundTaskStopLineChinese = "\n- 使用 %s 工具通过 task_id 取消正在运行的后台任务。"

	backgroundTaskPromptFooterChinese = "\n- 这些任务是正在运行的执行实例，而非用于规划的待办事项。\n"
)
