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

// Package subagent provides a ChatModelAgentMiddleware that injects Agent, TaskOutput,
// and TaskStop tools for spawning and managing sub-agents.
package subagent

// This file contains prompt templates and tool descriptions for the subagent middleware.

const (
	agentToolPrompt = `
# Agent Tool

You have access to an 'agent' tool to launch specialized agents that handle isolated tasks autonomously. Each agent invocation starts fresh — provide a complete task description.

When to use the agent tool:
- When a task is complex and multi-step, and can be fully delegated in isolation
- When a task is independent of other tasks and can run in parallel
- When a task requires focused reasoning or heavy token/context usage that would bloat the orchestrator thread
- When you only care about the output of the subagent, and not the intermediate steps (e.g. performing research then returning a synthesized report)

When NOT to use the agent tool:
- If you need to see the intermediate reasoning or steps (the agent tool hides them)
- If the task is trivial (a few tool calls or simple lookup)
- If delegating does not reduce token usage, complexity, or context switching

## Usage Notes
- Whenever possible, parallelize the work. Launch multiple agents concurrently by issuing multiple tool calls within a single response. This saves time for the user.
- Always include a short description (3-5 words) summarizing what the agent will do.
- The agent's outputs should generally be trusted.
- Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, etc.), since it is not aware of the user's intent.
- If the agent description mentions that it should be used proactively, then you should try your best to use it without the user having to ask for it first.
- If the user specifies that they want you to run agents "in parallel", you MUST issue multiple Agent tool calls within a single response.

## Writing the prompt

Brief the agent like a smart colleague who just walked into the room — it hasn't seen this conversation, doesn't know what you've tried, doesn't understand why this task matters.
- Explain what you're trying to accomplish and why.
- Describe what you've already learned or ruled out.
- Give enough context about the surrounding problem that the agent can make judgment calls rather than just following a narrow instruction.
- If you need a short response, say so ("report in under 200 words").
- Lookups: hand over the exact command. Investigations: hand over the question — prescribed steps become dead weight when the premise is wrong.

Terse command-style prompts produce shallow, generic work.

**Never delegate understanding.** Don't write "based on your findings, fix the bug" or "based on the research, implement it." Those phrases push synthesis onto the agent instead of doing it yourself. Write prompts that prove you understood: include file paths, line numbers, what specifically to change.
`

	agentToolPromptChinese = `
# Agent 工具

你可以使用 'agent' 工具启动专门的智能体来自主处理独立任务。每次智能体调用都从零开始——请提供完整的任务描述。

何时使用 agent 工具：
- 当任务复杂且包含多个步骤，并且可以完全独立委托时
- 当任务独立于其他任务并且可以并行运行时
- 当任务需要集中推理或大量 token/上下文使用，这会使编排器线程膨胀时
- 当你只关心子智能体的输出，而不关心中间步骤时（例如执行大量研究然后返回综合报告）

何时不使用 agent 工具：
- 如果你需要查看中间推理或步骤（agent 工具会隐藏它们）
- 如果任务很简单（几个工具调用或简单查找）
- 如果委托不会减少 token 使用、复杂性或上下文切换

## 使用注意事项
- 尽可能并行化工作。通过在一条消息中使用多个工具调用来同时启动多个智能体。这为用户节省了时间。
- 始终包含一个简短的描述（3-5 个词）来概括智能体要做的事情。
- 智能体的输出通常应该被信任。
- 明确告诉智能体你期望它编写代码还是只是进行研究（搜索、文件读取等），因为它不知道用户的意图。
- 如果智能体描述提到应该主动使用它，那么你应该尽力主动使用它。
- 如果用户指定他们希望你"并行"运行智能体，你必须在一次回复中发起多个 Agent 工具调用。

## 编写提示词

像给一个刚走进房间的聪明同事做简报一样对待智能体——它没有看过这段对话，不知道你尝试过什么，不了解为什么这个任务重要。
- 解释你要完成什么以及为什么。
- 描述你已经了解到或排除的内容。
- 提供足够的背景上下文，使智能体能够做出判断而不只是执行狭隘的指令。
- 如果你需要简短的回复，请说明（"200 字以内报告"）。
- 查找任务：给出确切的命令。调查任务：给出问题——预设步骤在前提错误时会成为负担。

简短的命令式提示词会产生浅层、泛化的结果。

**不要把"理解问题"这一步交给子智能体。**不要写"根据你的发现修复这个 bug"或"根据研究来实现它"——这类写法把本该由你完成的分析与综合推给了子智能体。要写出能证明你已经理解的提示词：包含文件路径、行号、具体要改什么。
`

	agentToolDescription = `Launch a new agent to handle complex, multi-step tasks autonomously.

The Agent tool launches specialized agents that autonomously handle complex tasks. Each agent type has specific capabilities and tools available to it.

Available agent types and the tools they have access to:
{other_agents}

When using the Agent tool, specify a subagent_type parameter to select which agent type to use.

When NOT to use the Agent tool:
- If you want to read a specific file path, use the Read or Glob tool instead of the Agent tool, to find the match more quickly
- If you are searching for a specific class definition like "class Foo", use the Glob tool instead, to find the match more quickly
- If you are searching for code within a specific file or set of 2-3 files, use the Read tool instead of the Agent tool, to find the match more quickly
- Other tasks that are not related to the agent descriptions above

Usage notes:
- Always include a short description summarizing what the agent will do
- Launch multiple agents concurrently whenever possible, to maximize performance; to do that, issue multiple tool calls within a single response
- When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.
- The agent's outputs should generally be trusted
- Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, web fetches, etc.), since it is not aware of the user's intent
- If the agent description mentions that it should be used proactively, then you should try your best to use it without the user having to ask for it first.
- If the user specifies that they want you to run agents "in parallel", you MUST issue multiple Agent tool calls within a single response.
`

	agentToolDescriptionChinese = `启动新智能体以自主处理复杂的多步骤任务。

Agent 工具启动专门的智能体，自主处理复杂任务。每种智能体类型都有特定的功能和可用的工具。

可用的智能体类型及其可访问的工具：
{other_agents}

使用 Agent 工具时，指定 subagent_type 参数来选择要使用的智能体类型。

何时不使用 Agent 工具：
- 如果你想读取特定的文件路径，请使用 Read 或 Glob 工具而不是 Agent 工具，以更快地找到匹配项
- 如果你正在搜索特定的类定义，如"class Foo"，请使用 Glob 工具，以更快地找到匹配项
- 如果你正在特定文件或 2-3 个文件集中搜索代码，请使用 Read 工具而不是 Agent 工具，以更快地找到匹配项
- 与上述智能体描述无关的其他任务

使用说明：
- 始终包含一个简短的描述来概括智能体要做的事情
- 尽可能同时启动多个智能体，以最大化性能；为此，在一次回复中发起多个工具调用
- 当智能体完成时，它将向你返回一条消息。智能体返回的结果对用户不可见。要向用户显示结果，你应该向用户发送一条文本消息，简要总结结果。
- 智能体的输出通常应该被信任
- 明确告诉智能体你期望它编写代码还是只是进行研究（搜索、文件读取、网络获取等），因为它不知道用户的意图
- 如果智能体描述提到应该主动使用它，那么你应该尽力使用它即使用户没有这样要求。
- 如果用户指定他们希望你"并行"运行智能体，你必须在一次回复中发起多个 Agent 工具调用。
`

	agentToolBackgroundPrompt = `
## Background Task Support
- You can optionally run agents in the background using the run_in_background parameter.
  When an agent runs in the background, you will be automatically notified when it completes.
  Do not block waiting on it — continue with other work, and use the task_output tool to check
  its status or retrieve its result when you need it.
- Foreground vs background: Use foreground (default) when you need the agent's results before
  you can proceed. Use background when you have genuinely independent work to do in parallel.
- Use task_output tool to check background task status. Use task_stop tool to cancel a background task.
`

	agentToolBackgroundPromptChinese = `
## 后台任务支持
- 你可以使用 run_in_background 参数在后台运行智能体。
  当智能体在后台运行时，你将自动收到完成通知。
  不要为等待它而阻塞——请继续处理其他工作，并在需要时使用 task_output 工具查询其状态或获取结果。
- 前台与后台：当你需要智能体结果才能继续时使用前台（默认）。当你有真正独立的工作可以并行完成时使用后台。
- 使用 task_output 工具检查后台任务状态。使用 task_stop 工具取消后台任务。
`

	taskOutputToolDescription = `Retrieve output from a running or completed background task.

- Takes a task_id parameter identifying the task
- Returns the task entry with status, result, and error information
- Use this tool when you need to check the status of a background task
`

	taskOutputToolDescriptionChinese = `获取正在运行或已完成的后台任务的输出。

- 接受 task_id 参数来标识任务
- 返回包含状态、结果和错误信息的任务条目
- 当你需要检查后台任务的状态时使用此工具
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
)
