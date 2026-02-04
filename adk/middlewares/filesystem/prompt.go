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

	ReadFileToolDesc = `Reads a file from the filesystem. You can access any file directly by using this tool.
Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The file_path parameter must be an absolute path, not a relative path
- By default, it reads up to 500 lines starting from the beginning of the file
- **IMPORTANT for large files and codebase exploration**: Use pagination with offset and limit parameters to avoid context overflow
	- First scan: read_file(path, limit=100) to see file structure
	- Read more sections: read_file(path, offset=100, limit=200) for next 200 lines
	- Only omit limit (read full file) when necessary for editing
- Specify offset and limit: read_file(path, offset=0, limit=100) reads first 100 lines
- Results are returned using cat -n format, with line numbers starting at 1
- You have the capability to call multiple tools in a single response. It is always better to speculatively read multiple files as a batch that are potentially useful.
- If you read a file that exists but has empty contents you will receive a system reminder warning in place of file contents.
- You should ALWAYS make sure a file has been read before editing it.`

	ReadFileToolDescChinese = `从文件系统读取文件。你可以使用此工具直接访问任何文件。
假设此工具能够读取机器上的所有文件。如果用户提供了文件路径，假设该路径是有效的。读取不存在的文件是可以的；将返回错误。

使用方法：
- file_path 参数必须是绝对路径，不能是相对路径
- 默认情况下，从文件开头读取最多 500 行
- **大文件和代码库探索的重要提示**：使用 offset 和 limit 参数进行分页，以避免上下文溢出
	- 首次扫描：read_file(path, limit=100) 查看文件结构
	- 读取更多部分：read_file(path, offset=100, limit=200) 读取接下来的 200 行
	- 仅在编辑必要时才省略 limit（读取完整文件）
- 指定 offset 和 limit：read_file(path, offset=0, limit=100) 读取前 100 行
- 结果以 cat -n 格式返回，行号从 1 开始
- 你可以在单个响应中调用多个工具。最好同时推测性地批量读取多个可能有用的文件
- 如果你读取的文件存在但内容为空，你将收到系统提醒警告而不是文件内容
- 在编辑文件之前，你应该始终确保已读取该文件`

	EditFileToolDesc = `Performs exact string replacements in files.

Usage:
- You must use your 'read_file' tool at least once in the conversation before editing. This tool will error if you attempt an edit without reading the file.
- When editing text from Read tool output, ensure you preserve the exact indentation (tabs/spaces) as it appears AFTER the line number prefix. The line number prefix format is: spaces + line number + tab. Everything after that tab is the actual file content to match. Never include any part of the line number prefix in the old_string or new_string.
- ALWAYS prefer editing existing files. NEVER write new files unless explicitly required.
- Only use emojis if the user explicitly requests it. Avoid adding emojis to files unless asked.
- The edit will FAIL if 'old_string' is not unique in the file. Either provide a larger string with more surrounding context to make it unique or use 'replace_all' to change every instance of 'old_string'.
- Use 'replace_all' for replacing and renaming strings across the file. This parameter is useful if you want to rename a variable for instance.`

	EditFileToolDescChinese = `在文件中执行精确的字符串替换。

使用方法：
- 在编辑之前，你必须在对话中至少使用一次 'read_file' 工具。如果你在未读取文件的情况下尝试编辑，此工具将报错
- 当从 Read 工具输出编辑文本时，请确保保留行号前缀之后的确切缩进（制表符/空格）。行号前缀格式为：空格 + 行号 + 制表符。制表符之后的所有内容都是要匹配的实际文件内容。永远不要在 old_string 或 new_string 中包含行号前缀的任何部分
- 始终优先编辑现有文件。除非明确要求，否则不要创建新文件
- 仅在用户明确要求时使用表情符号。除非被要求，否则避免在文件中添加表情符号
- 如果 'old_string' 在文件中不唯一，编辑将失败。要么提供包含更多上下文的更长字符串使其唯一，要么使用 'replace_all' 更改 'old_string' 的每个实例
- 使用 'replace_all' 在整个文件中替换和重命名字符串。例如，如果你想重命名变量，此参数很有用`

	WriteFileToolDesc = `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- If this is an existing file, you MUST use the Read tool first to read the file's contents. This tool will fail if you did not read the file first.
- ALWAYS prefer editing existing files in the codebase. NEVER write new files unless explicitly required.
- NEVER proactively create documentation files (*.md) or README files. Only create documentation files if explicitly requested by the User.
- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked.`

	WriteFileToolDescChinese = `将文件写入本地文件系统。

使用方法：
- 如果提供的路径已存在文件，此工具将覆盖现有文件
- 如果这是一个现有文件，你必须先使用 Read 工具读取文件内容。如果你没有先读取文件，此工具将失败
- 始终优先编辑代码库中的现有文件。除非明确要求，否则不要创建新文件
- 不要主动创建文档文件（*.md）或 README 文件。仅在用户明确要求时才创建文档文件
- 仅在用户明确要求时使用表情符号。除非被要求，否则避免在文件中写入表情符号`

	GlobToolDesc = `Fast file pattern matching tool that works with any codebase size
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns
- You can call multiple tools in a single response. It is always better to speculatively perform multiple searches in parallel if they are potentially useful.

Examples:
- '**/*.py' - Find all Python files
- '*.txt' - Find all text files in root
- '/subdir/**/*.md' - Find all markdown files under /subdir`

	GlobToolDescChinese = `适用于任何代码库大小的快速文件模式匹配工具
- 支持 glob 模式，如 "**/*.js" 或 "src/**/*.ts"
- 返回按修改时间排序的匹配文件路径
- 当你需要按名称模式查找文件时使用此工具
- 你可以在单个响应中调用多个工具。最好同时并行执行多个可能有用的搜索

示例：
- '**/*.py' - 查找所有 Python 文件
- '*.txt' - 查找根目录中的所有文本文件
- '/subdir/**/*.md' - 查找 /subdir 下的所有 markdown 文件`

	GrepToolDesc = `
A powerful search tool built on ripgrep

  Usage:
  - ALWAYS use Grep for search tasks. NEVER invoke 'grep' or 'rg' as a Bash command. The Grep tool has been optimized for correct permissions and access.
  - Supports full regex syntax (e.g., "log.*Error", "function\s+\w+")
  - Filter files with glob parameter (e.g., "*.js", "**/*.tsx") or type parameter (e.g., "js", "py", "rust")
  - Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
  - Use Task tool for open-ended searches requiring multiple rounds
  - Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use 'interface\{\}' to find 'interface{}' in Go code)
  - Multiline matching: By default patterns match within single lines only. For cross-line patterns like 'struct \{[\s\S]*?field', use 'multiline: true'`

	GrepToolDescChinese = `
基于 ripgrep 的强大搜索工具

  使用方法：
  - 始终使用 Grep 进行搜索任务。不要将 'grep' 或 'rg' 作为 Bash 命令调用。Grep 工具已针对正确的权限和访问进行了优化
  - 支持完整的正则表达式语法（例如，"log.*Error"，"function\s+\w+"）
  - 使用 glob 参数（例如，"*.js"，"**/*.tsx"）或 type 参数（例如，"js"，"py"，"rust"）过滤文件
  - 输出模式："content" 显示匹配行，"files_with_matches" 仅显示文件路径（默认），"count" 显示匹配计数
  - 对于需要多轮的开放式搜索，使用 Task 工具
  - 模式语法：使用 ripgrep（不是 grep）- 字面大括号需要转义（使用 'interface\{\}' 在 Go 代码中查找 'interface{}'）
  - 多行匹配：默认情况下，模式仅在单行内匹配。对于跨行模式如 'struct \{[\s\S]*?field'，使用 'multiline: true'`

	ExecuteToolDesc = `
Executes a given command in the sandbox environment with proper handling and security measures.

Before executing the command, please follow these steps:

1. Directory Verification:
- If the command will create new directories or files, first use the ls tool to verify the parent directory exists and is the correct location
- For example, before running "mkdir foo/bar", first use ls to check that "foo" exists and is the intended parent directory

2. Command Execution:
- Always quote file paths that contain spaces with double quotes (e.g., cd "path with spaces/file.txt")
- Examples of proper quoting:
- cd "/Users/name/My Documents" (correct)
- cd /Users/name/My Documents (incorrect - will fail)
- python "/path/with spaces/script.py" (correct)
- python /path/with spaces/script.py (incorrect - will fail)
- After ensuring proper quoting, execute the command
- Capture the output of the command

Usage notes:
- The command parameter is required
- Commands run in an isolated sandbox environment
- Returns combined stdout/stderr output with exit code
- If the output is very large, it may be truncated
- VERY IMPORTANT: You MUST avoid using search commands like find and grep. Instead use the grep, glob tools to search. You MUST avoid read tools like cat, head, tail, and use read_file to read files.
- When issuing multiple commands, use the ';' or '&&' operator to separate them. DO NOT use newlines (newlines are ok in quoted strings)
- Use '&&' when commands depend on each other (e.g., "mkdir dir && cd dir")
- Use ';' only when you need to run commands sequentially but don't care if earlier commands fail
- Try to maintain your current working directory throughout the session by using absolute paths and avoiding usage of cd

Examples:
Good examples:
- execute(command="pytest /foo/bar/tests")
- execute(command="python /path/to/script.py")
- execute(command="npm install && npm test")

Bad examples (avoid these):
- execute(command="cd /foo/bar && pytest tests")  # Use absolute path instead
- execute(command="cat file.txt")  # Use read_file tool instead
- execute(command="find . -name '*.py'")  # Use glob tool instead
- execute(command="grep -r 'pattern' .")  # Use grep tool instead
`

	ExecuteToolDescChinese = `
在沙箱环境中执行给定命令，具有适当的处理和安全措施。

执行命令前，请按照以下步骤操作：

1. 目录验证：
- 如果命令将创建新目录或文件，首先使用 ls 工具验证父目录是否存在且是正确的位置
- 例如，在运行 "mkdir foo/bar" 之前，首先使用 ls 检查 "foo" 是否存在且是预期的父目录

2. 命令执行：
- 始终用双引号引用包含空格的文件路径（例如，cd "path with spaces/file.txt"）
- 正确引用的示例：
- cd "/Users/name/My Documents"（正确）
- cd /Users/name/My Documents（错误 - 将失败）
- python "/path/with spaces/script.py"（正确）
- python /path/with spaces/script.py（错误 - 将失败）
- 确保正确引用后，执行命令
- 捕获命令的输出

使用说明：
- command 参数是必需的
- 命令在隔离的沙箱环境中运行
- 返回合并的 stdout/stderr 输出和退出代码
- 如果输出非常大，可能会被截断
- 非常重要：你必须避免使用 find 和 grep 等搜索命令。请改用 grep、glob 工具进行搜索。你必须避免使用 cat、head、tail 等读取工具，请使用 read_file 读取文件
- 发出多个命令时，使用 ';' 或 '&&' 运算符分隔它们。不要使用换行符（引号字符串中的换行符是可以的）
- 当命令相互依赖时使用 '&&'（例如，"mkdir dir && cd dir"）
- 仅当你需要按顺序运行命令但不关心早期命令是否失败时使用 ';'
- 尝试通过使用绝对路径并避免使用 cd 来在整个会话中保持当前工作目录

示例：
好的示例：
- execute(command="pytest /foo/bar/tests")
- execute(command="python /path/to/script.py")
- execute(command="npm install && npm test")

不好的示例（避免这些）：
- execute(command="cd /foo/bar && pytest tests")  # 改用绝对路径
- execute(command="cat file.txt")  # 改用 read_file 工具
- execute(command="find . -name '*.py'")  # 改用 glob 工具
- execute(command="grep -r 'pattern' .")  # 改用 grep 工具
`

	ToolsSystemPrompt = `
# Filesystem Tools 'ls', 'read_file', 'write_file', 'edit_file', 'glob', 'grep'

You have access to a filesystem which you can interact with using these tools.
All file paths must start with a '/'.

- ls: list files in a directory (requires absolute path)
- read_file: read a file from the filesystem
- write_file: write to a file in the filesystem
- edit_file: edit a file in the filesystem
- glob: find files matching a pattern (e.g., "**/*.py")
- grep: search for text within files
`

	ToolsSystemPromptChinese = `
# 文件系统工具 'ls', 'read_file', 'write_file', 'edit_file', 'glob', 'grep'

你可以使用这些工具与文件系统交互。
所有文件路径必须以 '/' 开头。

- ls：列出目录中的文件（需要绝对路径）
- read_file：从文件系统读取文件
- write_file：写入文件系统中的文件
- edit_file：编辑文件系统中的文件
- glob：查找匹配模式的文件（例如，"**/*.py"）
- grep：在文件中搜索文本
`

	ExecuteToolsSystemPrompt = `
# Execute Tool 'execute'

You have access to an 'execute' tool for running shell commands in a sandboxed environment.
Use this tool to run commands, scripts, tests, builds, and other shell operations.

- execute: run a shell command in the sandbox (returns output and exit code)
`

	ExecuteToolsSystemPromptChinese = `
# 执行工具 'execute'

你可以使用 'execute' 工具在沙箱环境中运行 shell 命令。
使用此工具运行命令、脚本、测试、构建和其他 shell 操作。

- execute：在沙箱中运行 shell 命令（返回输出和退出代码）
`
)
