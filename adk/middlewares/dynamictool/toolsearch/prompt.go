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

package toolsearch

const (
	toolDescription = `Search for or select deferred tools to make them available for use.

MANDATORY PREREQUISITE - THIS IS A HARD REQUIREMENT

You MUST use this tool to load deferred tools BEFORE calling them directly.

This is a BLOCKING REQUIREMENT - deferred tools are NOT available until you load them using this tool. Look for <available-deferred-tools> messages in the conversation for the list of tools you can discover. Both query modes (keyword search and direct selection) load the returned tools — once a tool appears in the results, it is immediately available to call.

Why this is non-negotiable:
- Deferred tools are not loaded until discovered via this tool
- Calling a deferred tool without first loading it will fail
Query modes:

1. Keyword search - Use keywords when you're unsure which tool to use or need to discover multiple tools at once:
  - "list directory" - find tools for listing directories
  - "notebook jupyter" - find notebook editing tools
  - "slack message" - find slack messaging tools
  - Returns up to 5 matching tools ranked by relevance
  - All returned tools are immediately available to call — no further selection step needed
2. Direct selection - Use select:<tool_name> when you know the exact tool name:
  - "select:mcp__slack__read_channel"
  - "select:NotebookEdit"
  - "select:Read,Edit,Grep" - load multiple tools at once with comma separation
  - Returns the named tool(s) if they exist
IMPORTANT: Both modes load tools equally. Do NOT follow up a keyword search with select: calls for tools already returned — they are already loaded.

3. Required keyword - Prefix with + to require a match:
  - "+linear create issue" - only tools from "linear", ranked by "create"/"issue"
  - "+slack send" - only "slack" tools, ranked by "send"
  - Useful when you know the service name but not the exact tool
CORRECT Usage Patterns:

<example>
User: I need to work with slack somehow
Assistant: Let me search for slack tools.
[Calls tool_search with query: "slack"]
Assistant: Found several options including mcp__slack__read_channel.
[Calls mcp__slack__read_channel directly — it was loaded by the keyword search]
</example>

<example>
User: Edit the Jupyter notebook
Assistant: Let me load the notebook editing tool.
[Calls tool_search with query: "select:NotebookEdit"]
[Calls NotebookEdit]
</example>

<example>
User: List files in the src directory
Assistant: I can see mcp__filesystem__list_directory in the available tools. Let me select it.
[Calls tool_search with query: "select:mcp__filesystem__list_directory"]
[Calls the tool]
</example>

INCORRECT Usage Patterns - NEVER DO THESE:

<bad-example>
User: Read my slack messages
Assistant: [Directly calls mcp__slack__read_channel without loading it first]
WRONG - You must load the tool FIRST using this tool
</bad-example>

<bad-example>
Assistant: [Calls tool_search with query: "slack", gets back mcp__slack__read_channel]
Assistant: [Calls tool_search with query: "select:mcp__slack__read_channel"]
WRONG - The keyword search already loaded the tool. The select call is redundant.
</bad-example>`

	toolDescriptionChinese = `搜索或选择延迟加载（deferred）的工具，使其可供调用。

强制前提条件（MANDATORY PREREQUISITE）— 硬性要求

在直接调用任何 延迟加载工具（deferred tools） 之前，你 必须先使用此工具将其加载。

这是一个 阻塞性要求（BLOCKING REQUIREMENT) — 延迟加载工具在被加载之前是 不可用的。你需要在对话中查找 <available-deferred-tools> 消息，以获取可以发现的工具列表。无论使用哪种查询方式（关键字搜索 或 直接选择），只要工具出现在返回结果中，它们就会自动被加载并立即可调用。

为什么这是不可协商的规则：
- 延迟加载工具在被发现之前不会被加载
- 如果你在加载之前直接调用延迟工具，调用将会失败
查询模式：

1. 关键字搜索（Keyword search）- 当你不确定具体需要哪个工具，或希望一次发现多个工具时使用关键字搜索：
- "list directory" — 查找用于列出目录的工具
- "notebook jupyter" — 查找 Jupyter Notebook 编辑工具
- "slack message" — 查找 Slack 消息相关工具
- 返回最多 5 个最相关的工具
- 所有返回的工具都会立即加载并可直接调用 — 不需要额外执行 select 步骤

2. 直接选择（Direct selection）— 当你已经知道工具的确切名称时使用 select:<tool_name>：
- "select:mcp__slack__read_channel"
- "select:NotebookEdit"
- "select:Read,Edit,Grep" — 一次加载多个工具
- 如果工具存在，将被加载并返回
重要说明：两种模式的加载效果完全相同。不要在关键词搜索之后，对返回的工具再次进行 select: 选择 — 它们已经加载好了。

3. 必须匹配关键字（Required keyword）— 在关键字前添加 + 可以 强制匹配特定服务或来源。
- "+linear create issue" — 仅返回名字中包含 "linear" 的工具，按 "create" / "issue" 排序
- "+slack send" — 仅返回名字中包含 "slack" 的工具，按 "send" 排序
- 适用于你知道服务名称但不知道具体工具名称

正确使用示例：

<example>
User: 我需要处理 Slack 相关的事情
Assistant: 让我搜索 Slack 工具。
[调用 tool_search，query: "slack"]
Assistant: 找到多个选项，包括 mcp__slack__read_channel。
[直接调用 mcp__slack__read_channel — 关键字搜索已经加载了该工具]
</example>

<example>
User: 编辑这个 Jupyter Notebook
Assistant: 让我加载 Notebook 编辑工具。
[调用 tool_search，query: "select:NotebookEdit"]
[调用 NotebookEdit]
</example>

<example>
User: 列出 src 目录中的文件
Assistant: 我看到可用工具中有 mcp__filesystem__list_directory，让我加载它。
[调用 tool_search，query: "select:mcp__filesystem__list_directory"]
[调用该工具]
</example>

错误用法（严禁）

<bad-example>
User: 读取我的 Slack 消息
Assistant: [不调用 tool_search 工具加载，直接调用 mcp__slack__read_channel]
错误 — 在调用工具之前没有先使用 tool_search 加载该工具。
</bad-example>

<bad-example>
Assistant:[调用 tool_search，query: "slack"，返回 mcp__slack__read_channel]
Assistant:[再次调用 tool_search，query: "select:mcp__slack__read_channel"]
错误 — 关键字搜索 已经加载了该工具，再次 select 是冗余操作。`

	systemReminderTpl = `<available-deferred-tools>
{{- range .Tools }}
{{ . }}
{{- end }}
</available-deferred-tools>`
)
