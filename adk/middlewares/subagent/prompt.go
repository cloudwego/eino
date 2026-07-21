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

Available agent types are listed in <system-reminder> messages in the conversation.

When using the Agent tool, specify a subagent_type parameter to select which agent type to use.

## When to use

Reach for this when the task matches an available agent type, when you have independent work to run in parallel, or when answering would mean reading across several files — delegate it and you keep the conclusion, not the file dumps. For a single-fact lookup where you already know the file, symbol, or value, search directly. Once you've delegated a search, don't also run it yourself — wait for the result.

- The agent's final message is returned to you as the tool result; it is not shown to the user — relay what matters.{back_ground_prompt}`

	agentToolDescriptionChinese = `启动一个新的智能体来处理复杂的多步骤任务。每种智能体类型都有其特定的能力和可用工具。

可用的智能体类型会列在对话中的 <system-reminder> 消息里。

使用 Agent 工具时，指定 subagent_type 参数来选择要使用的智能体类型。

## 何时使用

当任务匹配某个可用的智能体类型、当你有可并行的独立工作、或当回答需要跨多个文件阅读时——把它委托出去，你只保留结论，而不必处理大量文件内容。对于你已知文件、符号或具体值的单点查找，直接自己搜索。一旦把某个搜索委托出去，就不要自己再重复执行——等待其结果。

- 智能体的最终消息会作为工具结果返回给你；它不会展示给用户——请转述其中重要的内容。`

	agentToolBackgroundPrompt = `

Subagents run in the background by default; you'll be notified when one completes. Pass ` + "`" + `run_in_background: false` + "`" + ` for a synchronous run when you need the result before continuing.`

	agentToolBackgroundPromptChinese = `

子智能体默认在后台运行；完成时你会收到通知。当你需要先拿到结果才能继续时，传 ` + "`" + `run_in_background: false` + "`" + ` 以同步运行。`

	availableAgentTypesPreamble = `Available agent types for the Agent tool:`

	availableAgentTypesPreambleChinese = `Agent 工具可用的智能体类型：`
)
