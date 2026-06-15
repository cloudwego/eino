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

package automemory

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk/internal"
)

const (
	defaultMemoryInstruction = `# auto memory

You have a persistent auto memory directory at "{memory_dir}". Its contents persist across conversations.

As you work, consult your memory files to build on previous experience.

## How to save memories:
- Organize memory semantically by topic, not chronologically
- Use the Write and Edit tools to update your memory files
- 'MEMORY.md' is always loaded into your conversation context — content is truncated after 200 lines or 4KB, so keep it concise
- Create separate topic files (e.g., 'debugging.md'', 'patterns.md'') for detailed notes and link to them from MEMORY.md
- Update or remove memories that turn out to be wrong or outdated
- Do not write duplicate memories. First check if there is an existing memory you can update before writing a new one.

## What to save:
- Stable patterns and conventions confirmed across multiple interactions
- Key architectural decisions, important file paths, and project structure
- User preferences for workflow, tools, and communication style
- Solutions to recurring problems and debugging insights

## What NOT to save:
- Session-specific context (current task details, in-progress work, temporary state)
- Information that might be incomplete — verify against project docs before writing
- Anything that duplicates or contradicts existing AGENTS.md instructions
- Speculative or unverified conclusions from reading a single file

## Explicit user requests:
- When the user asks you to remember something across sessions (e.g., "always use bun", "never auto-commit"), save it — no need to wait for multiple interactions
- When the user asks to forget or stop remembering something, find and remove the relevant entries from your memory files
- When the user corrects you on something you stated from memory, you MUST update or remove the incorrect entry. A correction means the stored memory is wrong — fix it at the source before continuing, so the same mistake does not repeat in future conversations.

## Searching past context
- Search topic files in your memory directory: Grep with pattern="<search term>" path="{memory_dir}" glob="*.md"
- Use narrow search terms (error messages, file paths, function names) rather than broad keywords.

`

	defaultAppendCurrentIndexTruncNotify = `WARNING: MEMORY.md was truncated (lines: {memory_lines}, limit: 200; byte limit: 4096). Move detailed content into separate topic files and keep MEMORY.md as a concise index.`

	defaultAppendEmptyIndexTemplate = `Your MEMORY.md is currently empty. When you notice a pattern worth preserving across sessions, save it here. Anything in MEMORY.md will be included in your system prompt next time.`

	defaultTopicSelectionSystemPrompt = `You are selecting memories that will be useful to the agent as it processes a user's query. You will be given the user's query and a list of available memory files with their filenames and descriptions.

Return a list of RELATIVE FILE PATHS (relative to the memory directory) for the memories that will clearly be useful to the agent as it processes the user's query (up to 5). Only include memories that you are certain will be helpful based on their name/description/type.
- If you are unsure if a memory will be useful in processing the user's query, then do not include it in your list. Be selective and discerning.
- If there are no memories in the list that would clearly be useful, feel free to return an empty list.
- If a list of recently-used tools is provided, do not select memories that are usage reference or API documentation for those tools (the agent is already exercising them). DO still select memories containing warnings, gotchas, or known issues about those tools — active use is exactly when those matter.`

	defaultTopicSelectionUserPrompt = `Query: {user_query}

Available memories:
{available_memories}

Recently used tools:
{tools}`

	defaultTopicMemoryTruncNotify = `
> This memory file was truncated ({reason}). Use the Read tool to view the complete file at: {abs_path}`

	defaultMemoryInstructionChinese = `# 自动记忆

你有一个持久化的自动记忆目录 "{memory_dir}"。其中的内容会在不同会话之间保留。

在工作过程中，请查阅这些记忆文件，以便基于过去的经验继续推进。

## 如何保存记忆：
- 按主题组织记忆，而不是按时间顺序堆叠
- 使用 Write 和 Edit 工具更新你的记忆文件
- 'MEMORY.md' 会始终被加载到对话上下文中，其内容在超过 200 行或 4KB 时会被截断，因此请保持简洁
- 将详细内容写入单独的主题文件（例如 'debugging.md'、'patterns.md'），并在 MEMORY.md 中链接它们
- 当某条记忆被证明错误或过时时，请更新或删除它
- 不要写入重复记忆。创建新记忆前，先检查是否已有可更新的现有文件

## 应该保存什么：
- 已在多次交互中得到确认的稳定模式和约定
- 关键架构决策、重要文件路径和项目结构
- 用户在工作流、工具使用和沟通方式上的偏好
- 可复用的问题解决经验与调试结论

## 不应保存什么：
- 仅属于当前会话的上下文（当前任务细节、进行中的工作、临时状态）
- 可能不完整的信息，在写入前应先根据项目文档核实
- 与现有 AGENTS.md 指令重复或冲突的内容
- 仅基于阅读单个文件得到的猜测性或未经验证的结论

## 用户的明确要求：
- 当用户明确要求你跨会话记住某件事时（例如“始终使用 bun”“不要自动提交”），应立即保存，无需等待多轮交互确认
- 当用户要求你遗忘某件事或停止记忆时，找到对应条目并从记忆文件中删除
- 当用户指出你基于记忆给出的内容有误时，你必须更新或删除错误条目。纠正意味着原有记忆已经错误，必须先从源头修正，避免今后重复犯错

## 如何检索历史上下文
- 在记忆目录中搜索主题文件：使用 Grep，pattern="<搜索词>" path="{memory_dir}" glob="*.md"
- 尽量使用更窄的检索词，例如报错信息、文件路径、函数名，而不是宽泛关键词

`

	defaultAppendCurrentIndexTruncNotifyChinese = `警告：MEMORY.md 已被截断（总行数：{memory_lines}，限制：200 行；字节限制：4096）。请将详细内容迁移到独立的主题文件中，并让 MEMORY.md 只保留简洁索引。`

	defaultAppendEmptyIndexTemplateChinese = `你的 MEMORY.md 当前为空。当你发现值得跨会话保留的模式时，请把它写在这里。下一次对话中，MEMORY.md 的内容会被自动加入 system prompt。`

	defaultTopicSelectionSystemPromptChinese = `你需要从记忆列表中选择对当前用户问题真正有帮助的记忆。你会拿到用户问题，以及一组可用记忆文件的文件名和描述。

请返回一个 RELATIVE FILE PATHS 列表（相对于 memory directory），列出那些在处理当前用户问题时显然有帮助的记忆文件（最多 5 个）。只有在你能够基于名称、描述或类型确认其确实有帮助时才选择。
- 如果你不能确定某条记忆是否有帮助，就不要选它。请保持克制和甄别。
- 如果列表中没有任何明显有帮助的记忆，可以返回空列表。
- 如果提供了最近使用过的工具列表，不要选择那些仅包含这些工具使用说明或 API 文档的记忆（agent 已经在使用它们）。但如果记忆中包含这些工具的警告、坑点或已知问题，仍然应该选择，因为这些内容在实际调用时尤其重要。`

	defaultTopicSelectionUserPromptChinese = `问题：{user_query}

可用记忆：
{available_memories}

最近使用的工具：
{tools}`

	defaultTopicMemoryTruncNotifyChinese = `
> 该记忆文件已被截断（{reason}）。请使用 Read 工具查看完整文件：{abs_path}`
)

func buildExtractAutoOnlyPrompt(memoryDir string, newMessageCount int, existingMemories string, skipIndex bool) string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: buildExtractAutoOnlyPromptEnglish(memoryDir, newMessageCount, existingMemories, skipIndex),
		Chinese: buildExtractAutoOnlyPromptChinese(memoryDir, newMessageCount, existingMemories, skipIndex),
	})
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(lines[0])
	for i := 1; i < len(lines); i++ {
		b.WriteString("\n")
		b.WriteString(lines[i])
	}
	return b.String()
}

func getDefaultMemoryInstruction() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: defaultMemoryInstruction,
		Chinese: defaultMemoryInstructionChinese,
	})
}

func getAppendCurrentIndexTruncNotify() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: defaultAppendCurrentIndexTruncNotify,
		Chinese: defaultAppendCurrentIndexTruncNotifyChinese,
	})
}

func getAppendEmptyIndexTemplate() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: defaultAppendEmptyIndexTemplate,
		Chinese: defaultAppendEmptyIndexTemplateChinese,
	})
}

func getTopicSelectionSystemPrompt() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: defaultTopicSelectionSystemPrompt,
		Chinese: defaultTopicSelectionSystemPromptChinese,
	})
}

func getTopicSelectionUserPrompt() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: defaultTopicSelectionUserPrompt,
		Chinese: defaultTopicSelectionUserPromptChinese,
	})
}

func getTopicMemoryTruncNotify() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: defaultTopicMemoryTruncNotify,
		Chinese: defaultTopicMemoryTruncNotifyChinese,
	})
}

func buildExtractAutoOnlyPromptEnglish(memoryDir string, newMessageCount int, existingMemories string, skipIndex bool) string {
	manifest := ""
	if existingMemories != "" {
		manifest = fmt.Sprintf("\n\n## Existing memory files\n\n%s\n\nCheck this list before writing — update an existing file rather than creating a duplicate.", existingMemories)
	}

	howToSave := []string{
		"## How to save memories",
		"",
		"Saving a memory is a two-step process:",
		"",
		"Step 1 — write the memory to its own file.",
		"Step 2 — add a pointer to that file in MEMORY.md. MEMORY.md is an index, not the memory body.",
		"",
		"- Keep MEMORY.md concise because it is loaded into system prompt context.",
		"- Organize memory semantically by topic, not chronologically.",
		"- Update or remove memories that turn out to be wrong or outdated.",
		"- Do not write duplicate memories.",
	}
	if skipIndex {
		howToSave = []string{
			"## How to save memories",
			"",
			"Write each memory to its own file. Do not create duplicate files.",
		}
	}

	parts := []string{
		fmt.Sprintf("You are now acting as the memory extraction subagent. Analyze only the most recent ~%d messages above and use them to update persistent memory.", newMessageCount),
		"",
		fmt.Sprintf("Memory directory: %s", memoryDir),
		"",
		"Available tools: read_file, glob, write_file, edit_file. Only paths inside the memory directory are allowed. All other tools are denied.",
		"",
		"You have a limited turn budget. read_file should happen first for every file you may update, then write_file/edit_file should happen after that. Do not interleave read and write across many turns.",
		"",
		fmt.Sprintf("You MUST only use content from the last ~%d messages to update memories. Do not investigate code or verify against source files further.", newMessageCount) + manifest,
		"",
		"If the user explicitly asks you to remember something, save it immediately. If they ask you to forget something, find and remove the relevant memory.",
		"",
		"## What to save",
		"- Stable patterns and conventions confirmed across multiple interactions",
		"- Important file paths, architectural decisions, and user preferences",
		"- Recurring debugging insights and known gotchas",
		"",
		"## What NOT to save",
		"- Session-specific temporary state or current task details",
		"- Secrets, credentials, or personal data",
		"- Speculative or unverified conclusions",
		"",
	}
	parts = append(parts, howToSave...)
	return joinLines(parts)
}

func buildExtractAutoOnlyPromptChinese(memoryDir string, newMessageCount int, existingMemories string, skipIndex bool) string {
	manifest := ""
	if existingMemories != "" {
		manifest = fmt.Sprintf("\n\n## 现有记忆文件\n\n%s\n\n写入前请先检查这份列表，优先更新已有文件，而不是创建重复记忆。", existingMemories)
	}

	howToSave := []string{
		"## 如何保存记忆",
		"",
		"保存记忆分为两步：",
		"",
		"第 1 步：将记忆写入独立文件。",
		"第 2 步：在 MEMORY.md 中添加指向该文件的索引。MEMORY.md 只是索引，不应存放记忆正文。",
		"",
		"- 保持 MEMORY.md 简洁，因为它会被加载进 system prompt。",
		"- 按主题组织记忆，而不是按时间顺序堆叠。",
		"- 当记忆被证明错误或过时时，要及时更新或删除。",
		"- 不要写入重复记忆。",
	}
	if skipIndex {
		howToSave = []string{
			"## 如何保存记忆",
			"",
			"将每条记忆写入各自独立的文件中，不要创建重复文件。",
		}
	}

	parts := []string{
		fmt.Sprintf("你现在扮演 memory extraction subagent。只分析上方最近约 %d 条消息，并用它们来更新持久化记忆。", newMessageCount),
		"",
		fmt.Sprintf("记忆目录：%s", memoryDir),
		"",
		"可用工具：read_file、glob、write_file、edit_file。只允许访问记忆目录内的路径，其他工具均禁止使用。",
		"",
		"你的轮次预算有限。对于每个可能更新的文件，应先 read_file，再进行 write_file/edit_file；不要在多轮里交叉读写大量文件。",
		"",
		fmt.Sprintf("你必须只使用最近约 %d 条消息中的内容来更新记忆。不要继续调查代码，也不要再去源码中额外验证。", newMessageCount) + manifest,
		"",
		"如果用户明确要求你记住某件事，请立即保存；如果用户要求遗忘某件事，请找到对应记忆并删除。",
		"",
		"## 应该保存什么",
		"- 已在多次交互中得到确认的稳定模式和约定",
		"- 重要文件路径、架构决策和用户偏好",
		"- 可复用的调试经验与已知坑点",
		"",
		"## 不应保存什么",
		"- 仅属于当前会话的临时状态或当前任务细节",
		"- 密钥、凭据或个人数据",
		"- 猜测性或未经验证的结论",
		"",
	}
	parts = append(parts, howToSave...)
	return joinLines(parts)
}
