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

	agentToolDescription = `Launch a new agent to handle complex, multi-step tasks. Each agent type has specific capabilities and tools available to it.

When using the agent tool, specify a subagent_type parameter to select which agent type to use.

Available agent types and the tools they have access to:
{other_agents}

## When to use

Reach for this when the task matches an available agent type, when you have independent work to run in parallel, or when answering would mean reading across several files — delegate it and you keep the conclusion, not the file dumps. For a single-fact lookup where you already know the file, symbol, or value, search directly. Once you've delegated a search, don't also run it yourself — wait for the result.

- The agent's final message is returned to you as the tool result; it is not shown to the user — relay what matters.
- Each agent call starts fresh, so give a complete, self-contained task description.
`

	agentToolDescriptionChinese = `启动新智能体来处理复杂的多步骤任务。每种智能体类型都有特定的能力和可用的工具。

使用 agent 工具时，指定 subagent_type 参数来选择要使用的智能体类型。

可用的智能体类型及其可访问的工具：
{other_agents}

## 何时使用

当任务匹配某个可用的智能体类型、当你有可以并行处理的独立工作、或者当回答问题需要跨多个文件阅读时——把它委托出去，你只需保留结论，而无需处理大量文件内容。对于你已经知道文件、符号或具体值的单点查找，直接自己搜索即可。一旦你把某个搜索委托出去，就不要自己再重复执行——等待它的结果。

- 智能体的最终消息会作为工具结果返回给你；它不会展示给用户——请转述其中重要的内容。
- 每次智能体调用都是全新开始，因此请提供完整、自包含的任务描述。
`

	agentToolBackgroundPrompt = `
## Running agents in the background
- Set run_in_background=true to run an agent in the background. It keeps running after the tool
  call returns, and you will be notified when it completes. Do not block waiting on it — continue
  with other work, and use the task_output tool to check its status or retrieve its result by
  task_id when you need it.
- Use foreground (the default) when you need the agent's result before you can proceed; use
  background when you have genuinely independent work to do in parallel.
- Use the task_stop tool to cancel a background agent by task_id.
`

	agentToolBackgroundPromptChinese = `
## 在后台运行智能体
- 设置 run_in_background=true 可在后台运行智能体。它在工具调用返回后会继续运行，完成时你将收到通知。
  不要为等待它而阻塞——请继续处理其他工作，并在需要时使用 task_output 工具通过 task_id 查询其状态或获取结果。
- 当你需要智能体的结果才能继续时使用前台（默认）；当你有真正独立的工作可以并行完成时使用后台。
- 使用 task_stop 工具通过 task_id 取消后台智能体。
`
)
