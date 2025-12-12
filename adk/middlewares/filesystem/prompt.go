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

	ListFilesToolDesc = `Lists all files in the filesystem, filtering by directory.

Usage:
- The path parameter must be an absolute path, not a relative path
- The list_files tool will return a list of all files in the specified directory.
- This is very useful for exploring the file system and finding the right file to read or edit.
- You should almost ALWAYS use this tool before using the Read or Edit tools.`

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

	EditFileToolDesc = `Performs exact string replacements in files.

Usage:
- You must use your 'Read' tool at least once in the conversation before editing. This tool will error if you attempt an edit without reading the file.
- When editing text from Read tool output, ensure you preserve the exact indentation (tabs/spaces) as it appears AFTER the line number prefix. The line number prefix format is: spaces + line number + tab. Everything after that tab is the actual file content to match. Never include any part of the line number prefix in the old_string or new_string.
- ALWAYS prefer editing existing files. NEVER write new files unless explicitly required.
- Only use emojis if the user explicitly requests it. Avoid adding emojis to files unless asked.
- The edit will FAIL if 'old_string' is not unique in the file. Either provide a larger string with more surrounding context to make it unique or use 'replace_all' to change every instance of 'old_string'.
- Use 'replace_all' for replacing and renaming strings across the file. This parameter is useful if you want to rename a variable for instance.`

	WriteFileToolDesc = `Writes to a new file in the filesystem.

Usage:
- The file_path parameter must be an absolute path, not a relative path
- The content parameter must be a string
- The write_file tool will create the a new file.
- Prefer to edit existing files over creating new ones when possible.`

	GlobToolDesc = `Find files matching a glob pattern.

Usage:
- The glob tool finds files by matching patterns with wildcards
- Supports standard glob patterns: '*' (any characters), '**' (any directories), '?' (single character)
- Patterns can be absolute (starting with '/') or relative
- Returns a list of absolute file paths that match the pattern

Examples:
- '**/*.py' - Find all Python files
- '*.txt' - Find all text files in root
- '/subdir/**/*.md' - Find all markdown files under /subdir`

	GrepToolDesc = `Search for a pattern in files.

Usage:
- The grep tool searches for text patterns across files
- The pattern parameter is the text to search for (literal string, not regex)
- The path parameter filters which directory to search in (default is the current working directory)
- The glob parameter accepts a glob pattern to filter which files to search (e.g., '*.py')
- The output_mode parameter controls the output format:
- 'files_with_matches': List only file paths containing matches (default)
- 'content': Show matching lines with file path and line numbers
- 'count': Show count of matches per file

Examples:
- Search all files: 'grep(pattern="TODO")'
- Search Python files only: 'grep(pattern="import", glob="*.py")'
- Show matching lines: 'grep(pattern="error", output_mode="content")'`

	ToolsSystemPrompt = `## Filesystem Tools 'ls', 'read_file', 'write_file', 'edit_file', 'glob', 'grep'

You have access to a filesystem which you can interact with using these tools.
All file paths must start with a '/'.

- ls: list files in a directory (requires absolute path)
- read_file: read a file from the filesystem
- write_file: write to a file in the filesystem
- edit_file: edit a file in the filesystem
- glob: find files matching a pattern (e.g., "**/*.py")
- grep: search for text within files`
)
