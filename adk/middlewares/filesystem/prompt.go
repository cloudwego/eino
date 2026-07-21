/*
 * Copyright (c) 2025 Harrison Chase
 * Copyright (c) 2025 CloudWeGo Authors
 * SPDX-License-Identifier: MIT
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

package filesystem

// This file contains prompt templates and tool descriptions adapted from the DeepAgents project.
// Original source: https://github.com/langchain-ai/deepagents
//
// These prompts are used under the terms of the original project's open source license.
// When using this code in your own open source project, ensure compliance with the original license requirements.

const (
	tooLargeToolMessage = `Tool result too large, the result of this tool call {tool_call_id} was saved in the filesystem at this path: {file_path}
You can read the result from the filesystem by using the read_file tool, but make sure to only read part of the result at a time.
You can do this by specifying an offset and limit in the read_file tool call.
For example, to read the first 100 lines, you can use the read_file tool with offset=0 and limit=100.

Here are the first 10 lines of the result:
{content_sample}`

	tooLargeToolMessageChinese = `工具结果过大，此工具调用 {tool_call_id} 的结果已保存到文件系统的以下路径：{file_path}
你可以使用 read_file 工具从文件系统读取结果，但请确保每次只读取部分结果。
你可以通过在 read_file 工具调用中指定 offset 和 limit 来实现。
例如，要读取前 100 行，你可以使用 read_file 工具，设置 offset=0 和 limit=100。

以下是结果的前 10 行：
{content_sample}`

	ListFilesToolDesc = `Lists all files in the filesystem, filtering by directory.

Usage:
- The path parameter must be an absolute path, not a relative path
- The ls tool will return a list of all files in the specified directory.
- This is very useful for exploring the file system and finding the right file to read or edit.
- You should almost ALWAYS use this tool before using the read_file or edit_file tools.`

	ListFilesToolDescChinese = `列出文件系统中的所有文件，按目录过滤。

使用方法：
- path 参数必须是绝对路径，不能是相对路径
- ls 工具将返回指定目录中所有文件的列表
- 这对于探索文件系统和找到要读取或编辑的正确文件非常有用
- 在使用 read_file 或 edit_file 工具之前，你几乎总是应该先使用此工具`

	ReadFileToolDesc = `Reads a file from the local filesystem.

- ` + "`" + `file_path` + "`" + ` must be an absolute path.
- Reads up to 2000 lines by default.
- When you already know which part of the file you need, only read that part. This can be important for larger files.
- Results are returned using cat -n format, with line numbers starting at 1{EnhancedReadFileDesc}
- Reading a directory, a missing file, or an empty file returns an error or system reminder rather than content.
- Do NOT re-read a file you just edited to verify — edit_file/write_file would have errored if the change failed, and the harness tracks file state for you.`

	ReadFileToolDescChinese = `从本地文件系统读取文件。

- ` + "`" + `file_path` + "`" + ` 必须是绝对路径。
- 默认最多读取 2000 行。
- 当你已经知道需要文件的哪一部分时，只读取那一部分。这对较大的文件尤其重要。
- 结果以 cat -n 格式返回，行号从 1 开始。{EnhancedReadFileDesc}
- 读取目录、不存在的文件或空文件时，返回错误或系统提醒而非内容。
- 不要为了验证而重新读取你刚编辑过的文件 —— 若改动失败 edit_file/write_file 早已报错，且 harness 会为你跟踪文件状态。`

	EnhancedReadFileDesc = `
- Reads images (PNG, JPG, …) and presents them visually. Reads PDFs via the ` + "`" + `pages` + "`" + ` parameter (e.g. "1-5", max 20 pages/request; required for PDFs over 10 pages). Reads Jupyter notebooks (.ipynb) as cells with outputs.`

	EnhancedReadFileDescChinese = `
- 可读取图片（PNG、JPG 等）并以视觉方式呈现。可通过 ` + "`" + `pages` + "`" + ` 参数读取 PDF（如 "1-5"，每次最多 20 页；超过 10 页的 PDF 必须提供该参数）。可将 Jupyter notebook（.ipynb）按单元格及其输出读取。`

	EditFileToolDesc = `Performs exact string replacement in a file.

- You must Read the file in this conversation before editing, or the call will fail.
- ` + "`" + `old_string` + "`" + ` must match the file exactly, including indentation, and be unique — the edit fails otherwise. Strip the Read line prefix (line number + tab) before matching.
- ` + "`" + `replace_all: true` + "`" + ` replaces every occurrence instead.`

	EditFileToolDescChinese = `在文件中执行精确的字符串替换。

- 编辑前你必须在本次对话中 Read 过该文件，否则调用会失败。
- ` + "`" + `old_string` + "`" + ` 必须与文件完全一致（包括缩进）且唯一，否则编辑失败。匹配前请去掉 Read 输出的行前缀（行号 + 制表符）。
- ` + "`" + `replace_all: true` + "`" + ` 则替换所有出现处。`

	WriteFileToolDesc = `Writes a file to the local filesystem, overwriting if one exists.

When to use: creating a new file, or fully replacing one you've already Read. Overwriting an existing file you haven't Read will fail. For partial changes, edit file instead.`

	WriteFileToolDescChinese = `将文件写入本地文件系统，若已存在则覆盖。

何时使用：创建新文件，或完全替换一个你已 Read 过的文件。覆盖你尚未 Read 过的已有文件会失败。局部改动请Edit file`

	GlobToolDesc = `- Fast file pattern matching tool that works with any codebase size
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns
- When you are doing an open ended search that may require multiple rounds of globbing and grepping, use the Agent tool instead`

	GlobToolDescChinese = `- 适用于任何代码库规模的快速文件模式匹配工具
- 支持形如 "**/*.js" 或 "src/**/*.ts" 的 glob 模式
- 返回按修改时间排序的匹配文件路径
- 当你需要按名称模式查找文件时使用此工具
- 当你进行可能需要多轮 glob 与 grep 的开放式搜索时，改用 Agent 工具`

	GrepToolDesc = `A powerful search tool built on ripgrep

  Usage:
  - ALWAYS use Grep for search tasks. NEVER invoke ` + "`" + `grep` + "`" + ` or ` + "`" + `rg` + "`" + ` as a Bash command. The Grep tool has been optimized for correct permissions and access.
  - Supports full regex syntax (e.g., "log.*Error", "function\s+\w+")
  - Filter files with glob parameter (e.g., "*.js", "**/*.tsx") or type parameter (e.g., "js", "py", "rust")
  - Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
  - Use Agent tool for open-ended searches requiring multiple rounds
  - Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use ` + "`" + `interface\{\}` + "`" + ` to find ` + "`" + `interface{}` + "`" + ` in Go code)
  - Multiline matching: By default patterns match within single lines only. For cross-line patterns like ` + "`" + `struct \{[\s\S]*?field` + "`" + `, use ` + "`" + `multiline: true` + "`"

	GrepToolDescChinese = `基于 ripgrep 的强大搜索工具

  使用方法：
  - 搜索任务始终使用 Grep。不要将 ` + "`" + `grep` + "`" + ` 或 ` + "`" + `rg` + "`" + ` 作为 Bash 命令调用。Grep 工具已针对正确的权限与访问做了优化。
  - 支持完整正则语法（如 "log.*Error"、"function\s+\w+"）
  - 用 glob 参数（如 "*.js"、"**/*.tsx"）或 type 参数（如 "js"、"py"、"rust"）过滤文件
  - 输出模式："content" 显示匹配行，"files_with_matches" 仅显示文件路径（默认），"count" 显示匹配计数
  - 需要多轮的开放式搜索请使用 Agent 工具
  - 模式语法：使用 ripgrep（非 grep）——字面大括号需转义（用 ` + "`" + `interface\{\}` + "`" + ` 匹配 Go 代码中的 ` + "`" + `interface{}` + "`" + `）
  - 多行匹配：默认模式仅在单行内匹配。对于跨行模式如 ` + "`" + `struct \{[\s\S]*?field` + "`" + `，使用 ` + "`" + `multiline: true` + "`"

	ExecuteToolDesc = `Executes a bash command and returns its output.

- Working directory persists between calls, but prefer absolute paths — ` + "`" + `cd` + "`" + ` in a compound command can trigger a permission prompt. Shell state (env vars, functions) does not persist; the shell is initialized from the user's profile.
- IMPORTANT: Avoid using this tool to run ` + "`" + `cat` + "`" + `, ` + "`" + `head` + "`" + `, ` + "`" + `tail` + "`" + `, ` + "`" + `sed` + "`" + `, ` + "`" + `awk` + "`" + `, or ` + "`" + `echo` + "`" + ` commands, unless explicitly instructed or after you have verified that a dedicated tool cannot accomplish your task. Instead, use the appropriate dedicated tool as this will provide a much better experience for the user.`

	ExecuteToolDescChinese = `执行一条 bash 命令并返回其输出。

- 工作目录在多次调用间保持，但优先使用绝对路径 —— 复合命令中的 ` + "`" + `cd` + "`" + ` 可能触发权限确认。Shell 状态（环境变量、函数）不会保留；shell 以用户的 profile 初始化。
- 重要：除非明确要求，或你已确认没有专用工具能完成任务，否则避免用本工具运行 ` + "`" + `cat` + "`" + `、` + "`" + `head` + "`" + `、` + "`" + `tail` + "`" + `、` + "`" + `sed` + "`" + `、` + "`" + `awk` + "`" + `、` + "`" + `echo` + "`" + ` 命令。请改用相应的专用工具，这会带来更好的体验。`

	ManagedExecuteToolDesc = `Executes a bash command and returns its output.

- Working directory persists between calls, but prefer absolute paths — ` + "`" + `cd` + "`" + ` in a compound command can trigger a permission prompt. Shell state (env vars, functions) does not persist; the shell is initialized from the user's profile.
- IMPORTANT: Avoid using this tool to run ` + "`" + `cat` + "`" + `, ` + "`" + `head` + "`" + `, ` + "`" + `tail` + "`" + `, ` + "`" + `sed` + "`" + `, ` + "`" + `awk` + "`" + `, or ` + "`" + `echo` + "`" + ` commands, unless explicitly instructed or after you have verified that a dedicated tool cannot accomplish your task. Instead, use the appropriate dedicated tool as this will provide a much better experience for the user.
- ` + "`" + `timeout` + "`" + ` is in milliseconds: default 120000, max 600000.
- ` + "`" + `run_in_background` + "`" + ` runs the command detached: it keeps running across turns and re-invokes you when it exits. No ` + "`" + `&` + "`" + ` needed. Foreground ` + "`" + `sleep` + "`" + ` is blocked; use Monitor with an until-loop to wait on a condition.`

	ManagedExecuteToolDescChinese = `执行一条 bash 命令并返回其输出。

- 工作目录在多次调用间保持，但优先使用绝对路径 —— 复合命令中的 ` + "`" + `cd` + "`" + ` 可能触发权限确认。Shell 状态（环境变量、函数）不会保留；shell 以用户的 profile 初始化。
- 重要：除非明确要求，或你已确认没有专用工具能完成任务，否则避免用本工具运行 ` + "`" + `cat` + "`" + `、` + "`" + `head` + "`" + `、` + "`" + `tail` + "`" + `、` + "`" + `sed` + "`" + `、` + "`" + `awk` + "`" + `、` + "`" + `echo` + "`" + ` 命令。请改用相应的专用工具，这会带来更好的体验。
- ` + "`" + `timeout` + "`" + ` 单位为毫秒：默认 120000，最大 600000。
- ` + "`" + `run_in_background` + "`" + ` 以分离方式运行命令：它跨轮次持续运行，退出时会重新唤起你。无需 ` + "`" + `&` + "`" + `。前台 ` + "`" + `sleep` + "`" + ` 被禁止；用 Monitor 配合 until 循环来等待某个条件。`
)
