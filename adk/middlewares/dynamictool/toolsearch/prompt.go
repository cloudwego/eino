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
	toolDescription = `Fetches full schema definitions for deferred tools so they can be called.

Deferred tools appear by name in <system-reminder> messages. Until fetched, only the name is known — there is no parameter schema, so the tool cannot be invoked. This tool takes a query, matches it against the deferred tool list, and returns the matched tools' complete JSONSchema definitions inside a <functions> block. Once a tool's schema appears in that result, it is callable exactly like any tool defined at the top of the prompt.

Result format: each matched tool appears as one <function>{"description": "...", "name": "...", "parameters": {...}}</function> line inside the <functions> block — the same encoding as the tool list at the top of this prompt.

Query forms:
- "select:Read,Edit,Grep" — fetch these exact tools by name
- "notebook jupyter" — keyword search, up to max_results best matches
- "+slack send" — require "slack" in the name, rank by remaining terms`

	toolDescriptionChinese = `获取延迟加载（deferred）工具的完整 schema 定义，使其可被调用。

延迟加载工具只以名称形式出现在 <system-reminder> 消息中。在被获取之前，只有名称是已知的——没有参数 schema，因此工具无法被调用。此工具接收一个 query，将其与延迟加载工具列表进行匹配，并在 <functions> 块中返回匹配工具的完整 JSONSchema 定义。一旦某个工具的 schema 出现在返回结果中，它就能像 prompt 顶部定义的任何工具一样被调用。

返回格式：每个匹配的工具在 <functions> 块中作为一行 <function>{"description": "...", "name": "...", "parameters": {...}}</function> 出现——与 prompt 顶部工具列表的编码方式相同。

Query 形式：
- "select:Read,Edit,Grep" —— 按名称精确获取这些工具
- "notebook jupyter" —— 关键字搜索，返回至多 max_results 个最佳匹配
- "+slack send" —— 要求名称中包含 "slack"，按其余词项排序`

	reminderTpl = `The following deferred tools are now available via tool_search. Their schemas are NOT loaded — calling them directly will fail with InputValidationError. Use tool_search with query "select:<name>[,<name>...]" to load tool schemas before calling them:
{{- range .Tools }}
{{ . }}
{{- end }}`
)
