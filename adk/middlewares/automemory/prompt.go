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
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/adk/internal"
)

const (
	defaultMemoryInstructionWithIndex = `# Auto memory

You have access to persistent memory stores. Their contents persist across conversations.

As you work, consult your memory files to build on previous experience.

## How to save memories:
- Organize memory semantically by topic, not chronologically
- Use the Write and Edit tools to update your memory files
- When a store has MEMORY.md enabled, it is loaded into your system prompt context — content is truncated after configured line and byte limits, so keep it concise
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
- Search topic files inside the relevant memory store.
- Use narrow search terms (error messages, file paths, function names) rather than broad keywords.

`

	defaultMemoryInstructionWithoutIndex = `# Auto memory

You have access to persistent memory stores. Their contents persist across conversations.

As you work, consult your memory files to build on previous experience.

## How to save memories:
- Organize memory semantically by topic, not chronologically
- Use the Write and Edit tools to update your memory files
- Create separate topic files (e.g., 'debugging.md'', 'patterns.md'') for detailed notes
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
- Search topic files inside the relevant memory store.
- Use narrow search terms (error messages, file paths, function names) rather than broad keywords.

`

	defaultAppendCurrentIndexTruncNotify = `WARNING: MEMORY.md was truncated (lines: {memory_lines}, limit: 200; byte limit: 4096). Move detailed content into separate topic files and keep MEMORY.md as a concise index.`

	defaultAppendEmptyIndexTemplate = `Your MEMORY.md is currently empty. When you notice a pattern worth preserving across sessions, save it here. Anything in MEMORY.md will be included in your system prompt next time.`

	defaultTopicSelectionSystemPrompt = `You are selecting memories that will be useful to the agent as it processes a user's query. You will be given the user's query and a list of available memory files across one or more memory stores, with their displayed memory paths and descriptions.

Return a list of memory paths exactly as shown in the available memories list, for the memories that will clearly be useful to the agent as it processes the user's query, up to the selection limit provided by the user message. Only include memories that you are certain will be helpful based on their store, name, description, or type.
- If you are unsure if a memory will be useful in processing the user's query, then do not include it in your list. Be selective and discerning.
- If there are no memories in the list that would clearly be useful, feel free to return an empty list.
- If a list of recently-used tools is provided, do not select memories that are usage reference or API documentation for those tools (the agent is already exercising them). DO still select memories containing warnings, gotchas, or known issues about those tools — active use is exactly when those matter.`

	defaultTopicSelectionUserPrompt = `Query: {user_query}

Selection limit: {top_k}

Available memories:
{available_memories}

Recently used tools:
{tools}`

	defaultTopicMemoryTruncNotify = `
> This memory file was truncated ({reason}). Use the Read tool to view the complete file at: {abs_path}`

	defaultMemoryInstructionChineseWithIndex = `# 自动记忆

你可以访问持久化的记忆存储。其中的内容会在不同会话之间保留。

在工作过程中，请查阅这些记忆文件，以便基于过去的经验继续推进。

## 如何保存记忆：
- 按主题组织记忆，而不是按时间顺序堆叠
- 使用 Write 和 Edit 工具更新你的记忆文件
- 当某个记忆存储启用 MEMORY.md 时，它会被加载进系统提示词，其内容会按配置的行数和字节数限制截断，因此请保持简洁
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
- 在相关记忆存储中搜索主题文件
- 尽量使用更窄的检索词，例如报错信息、文件路径、函数名，而不是宽泛关键词

`

	defaultMemoryInstructionChineseWithoutIndex = `# 自动记忆

你可以访问持久化的记忆存储。其中的内容会在不同会话之间保留。

在工作过程中，请查阅这些记忆文件，以便基于过去的经验继续推进。

## 如何保存记忆：
- 按主题组织记忆，而不是按时间顺序堆叠
- 使用 Write 和 Edit 工具更新你的记忆文件
- 将详细内容写入单独的主题文件（例如 'debugging.md'、'patterns.md'）
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
- 在相关记忆存储中搜索主题文件
- 尽量使用更窄的检索词，例如报错信息、文件路径、函数名，而不是宽泛关键词

`

	defaultAppendCurrentIndexTruncNotifyChinese = `警告：MEMORY.md 已被截断（总行数：{memory_lines}，限制：200 行；字节限制：4096）。请将详细内容迁移到独立的主题文件中，并让 MEMORY.md 只保留简洁索引。`

	defaultAppendEmptyIndexTemplateChinese = `你的 MEMORY.md 当前为空。当你发现值得跨会话保留的模式时，请把它写在这里。下一次对话中，MEMORY.md 的内容会被自动加入系统提示词。`

	defaultTopicSelectionSystemPromptChinese = `你需要从记忆列表中选择对当前用户问题真正有帮助的记忆。你会拿到用户问题，以及来自一个或多个记忆存储的可用记忆文件列表，列表中包含展示给你的记忆路径和描述。

请返回一个记忆路径列表，必须与可用记忆列表中展示的路径完全一致，列出那些在处理当前用户问题时显然有帮助的记忆文件，数量不能超过用户消息中给出的选择上限。只有在你能够基于存储、名称、描述或类型确认其确实有帮助时才选择。
- 如果你不能确定某条记忆是否有帮助，就不要选它。请保持克制和甄别。
- 如果列表中没有任何明显有帮助的记忆，可以返回空列表。
- 如果提供了最近使用过的工具列表，不要选择那些仅包含这些工具使用说明或 API 文档的记忆（智能体已经在使用它们）。但如果记忆中包含这些工具的警告、坑点或已知问题，仍然应该选择，因为这些内容在实际调用时尤其重要。`

	defaultTopicSelectionUserPromptChinese = `问题：{user_query}

选择上限：{top_k}

可用记忆：
{available_memories}

最近使用的工具：
{tools}`

	defaultTopicMemoryTruncNotifyChinese = `
> 该记忆文件已被截断（{reason}）。请使用 Read 工具查看完整文件：{abs_path}`
)

type memoryStorePromptInfo struct {
	Name        string
	Mount       string
	Description string
	Index       *memoryIndexPromptInfo
}

type memoryIndexPromptInfo struct {
	FileName       string
	Path           string
	Content        string
	Empty          bool
	Truncated      bool
	Lines          int
	IncludeContent bool
}

type memoryManifestStorePromptInfo struct {
	Name  string
	Mount string
	Files []memoryManifestFilePromptInfo
}

type memoryManifestFilePromptInfo struct {
	MemoryPath  string
	AbsPath     string
	Saved       string
	Description string
}

func buildSystemMemoryInstruction(baseInstruction, memoryInstruction string, stores []memoryStorePromptInfo) (string, error) {
	return baseInstruction + "\n" + internal.SelectPrompt(internal.I18nPrompts{
		English: buildSystemMemoryInstructionEnglish(memoryInstruction, stores),
		Chinese: buildSystemMemoryInstructionChinese(memoryInstruction, stores),
	}), nil
}

func buildSystemMemoryInstructionEnglish(memoryInstruction string, stores []memoryStorePromptInfo) string {
	return strings.Join([]string{memoryInstruction, buildMemoryStoresManifestEnglish(stores)}, "\n")
}

func buildSystemMemoryInstructionChinese(memoryInstruction string, stores []memoryStorePromptInfo) string {
	return strings.Join([]string{memoryInstruction, buildMemoryStoresManifestChinese(stores)}, "\n")
}

func buildMemoryStoresManifestEnglish(stores []memoryStorePromptInfo) string {
	lines := []string{
		"## Memory stores",
		"",
		"Available memory stores (each is a directory):",
		"",
	}
	for i, store := range stores {
		lines = append(lines,
			fmt.Sprintf("### %d. Name: %s", i+1, store.Name),
			fmt.Sprintf("Path: %s", store.Mount),
		)
		if strings.TrimSpace(store.Description) != "" {
			lines = append(lines, fmt.Sprintf("Description: %s", strings.TrimSpace(store.Description)))
		}
		if store.Index != nil {
			lines = append(lines, fmt.Sprintf("Index file path: %s", store.Index.Path), "")
			if block := buildMemoryIndexBlockEnglish(*store.Index); block != "" {
				lines = append(lines, block)
			}
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func buildMemoryStoresManifestChinese(stores []memoryStorePromptInfo) string {
	lines := []string{
		"## 记忆存储",
		"",
		"可用记忆存储 (每一条是一个目录)：",
		"",
	}
	for i, store := range stores {
		lines = append(lines,
			fmt.Sprintf("### %d. 名称: %s", i+1, store.Name),
			fmt.Sprintf("存储路径：%s", store.Mount),
		)
		if strings.TrimSpace(store.Description) != "" {
			lines = append(lines, fmt.Sprintf("功能描述：%s", strings.TrimSpace(store.Description)))
		}
		if store.Index != nil {
			lines = append(lines, fmt.Sprintf("索引文件路径：%s", store.Index.Path), "")
			if block := buildMemoryIndexBlockChinese(*store.Index); block != "" {
				lines = append(lines, block)
			}
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func buildMemoryIndexBlockEnglish(index memoryIndexPromptInfo) string {
	if !index.IncludeContent {
		return ""
	}
	lines := []string{fmt.Sprintf("#### Index file content: %s", index.FileName)}
	if index.Empty {
		lines = append(lines, getAppendEmptyIndexTemplate())
	} else {
		lines = append(lines, index.Content)
		if index.Truncated {
			lines = append(lines, strings.ReplaceAll(getAppendCurrentIndexTruncNotify(), "{memory_lines}", fmt.Sprintf("%d", index.Lines)))
		}
	}
	return strings.Join(lines, "\n")
}

func buildMemoryIndexBlockChinese(index memoryIndexPromptInfo) string {
	if !index.IncludeContent {
		return ""
	}
	lines := []string{fmt.Sprintf("#### 索引文件内容：%s", index.FileName)}
	if index.Empty {
		lines = append(lines, getAppendEmptyIndexTemplate())
	} else {
		lines = append(lines, index.Content)
		if index.Truncated {
			lines = append(lines, strings.ReplaceAll(getAppendCurrentIndexTruncNotify(), "{memory_lines}", fmt.Sprintf("%d", index.Lines)))
		}
	}
	return strings.Join(lines, "\n")
}

func buildExtractAutoOnlyPrompt(memoryStores string, newMessageCount int, existingMemories string, enableMemoryIndex bool) string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: buildExtractAutoOnlyPromptEnglish(memoryStores, newMessageCount, existingMemories, enableMemoryIndex),
		Chinese: buildExtractAutoOnlyPromptChinese(memoryStores, newMessageCount, existingMemories, enableMemoryIndex),
	})
}

func buildMemoryStoresManifest(stores []memoryStorePromptInfo) string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: buildMemoryStoresManifestEnglish(stores),
		Chinese: buildMemoryStoresManifestChinese(stores),
	})
}

func buildMemoryIndexReminder(stores []memoryStorePromptInfo) string {
	return "<!-- automemory:index -->\n" + internal.SelectPrompt(internal.I18nPrompts{
		English: buildMemoryIndexReminderEnglish(stores),
		Chinese: buildMemoryIndexReminderChinese(stores),
	})
}

func buildTopicMemoryReminder(topics []topicMemoryPromptInfo) string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: buildTopicMemoryReminderEnglish(topics),
		Chinese: buildTopicMemoryReminderChinese(topics),
	})
}

func buildTopicMemoryReminderEnglish(topics []topicMemoryPromptInfo) string {
	lines := []string{
		"<system-reminder>",
		"Topic memories are long-term memory files selected as relevant to the current query. Use them as supporting context for this turn. They may contain durable user preferences, project conventions, or previously saved facts; do not treat them as a replacement for the current user request.",
		"",
	}
	for i, topic := range topics {
		lines = append(lines,
			fmt.Sprintf("<topic-memory-%d>", i+1),
			fmt.Sprintf("1. Memory Store Name: %s", topic.StoreName),
			fmt.Sprintf("2. Topic Memory File Path: %s", filepath.Join(topic.StorePath, topic.Path)),
			fmt.Sprintf("3. Topic Memory Modified at: %s", topic.Saved),
			"4. Topic Memory Content:",
			"<topic-memory-content>",
			topic.Content,
			"</topic-memory-content>",
			fmt.Sprintf("</topic-memory-%d>", i+1),
			"",
		)
	}
	lines = append(lines, "</system-reminder>")
	return strings.Join(lines, "\n")
}

func buildTopicMemoryReminderChinese(topics []topicMemoryPromptInfo) string {
	lines := []string{
		"<system-reminder>",
		"主题记忆是本次查询相关的长期记忆文件。请将它们作为当前轮次的辅助上下文使用，其中可能包含稳定的用户偏好、项目约定或此前保存的事实；不要用它们替代当前用户请求。",
		"",
	}
	for i, topic := range topics {
		lines = append(lines,
			fmt.Sprintf("<topic-memory-%d>", i+1),
			fmt.Sprintf("1. 记忆存储名称：%s", topic.StoreName),
			fmt.Sprintf("2. 主题文件路径：%s", filepath.Join(topic.StorePath, topic.Path)),
			fmt.Sprintf("3. 更新时间：%s", topic.Saved),
			"4. 主题记忆内容：",
			"<topic-memory-content>",
			topic.Content,
			"</topic-memory-content>",
			fmt.Sprintf("</topic-memory-%d>", i+1),
			"",
		)
	}
	lines = append(lines, "</system-reminder>")
	return strings.Join(lines, "\n")
}

func buildMemoryIndexReminderEnglish(stores []memoryStorePromptInfo) string {
	lines := []string{
		"<system-reminder>",
		"Memory indexes are the high-level table of contents for your memory stores. Use them to understand what long-term memories may exist and decide which memory files to inspect with tools. They are not the full memory content; detailed notes usually live in the linked topic files.",
		"",
	}
	for i, store := range stores {
		lines = append(lines,
			fmt.Sprintf("<memory-index-%d>", i+1),
			fmt.Sprintf("1. Memory Store Name: %s", store.Name),
		)
		if strings.TrimSpace(store.Description) != "" {
			lines = append(lines, fmt.Sprintf("2. Description: %s", strings.TrimSpace(store.Description)))
		}
		if store.Index != nil {
			lines = append(lines,
				fmt.Sprintf("3. Index Memory File Path: %s", store.Index.Path),
				"4. Index Memory File Content:",
				"<index-file-content>",
				renderMemoryIndexContentEnglish(*store.Index),
				"</index-file-content>",
			)
		}
		lines = append(lines, fmt.Sprintf("</memory-index-%d>", i+1), "")
	}
	lines = append(lines, "</system-reminder>")
	return strings.Join(lines, "\n")
}

func buildMemoryIndexReminderChinese(stores []memoryStorePromptInfo) string {
	lines := []string{
		"<system-reminder>",
		"记忆索引是每个记忆存储的高层目录。请用它判断当前可能有哪些长期记忆，以及需要通过工具进一步查看哪些记忆文件。它不是完整记忆内容，详细信息通常保存在索引中链接的主题文件里。",
		"",
	}
	for i, store := range stores {
		lines = append(lines,
			fmt.Sprintf("<memory-index-%d>", i+1),
			fmt.Sprintf("1. 记忆存储名称：%s", store.Name),
		)
		if strings.TrimSpace(store.Description) != "" {
			lines = append(lines, fmt.Sprintf("2. 功能描述：%s", strings.TrimSpace(store.Description)))
		}
		if store.Index != nil {
			lines = append(lines,
				fmt.Sprintf("3. 索引记忆文件路径：%s", store.Index.Path),
				"4. 索引记忆文件内容：",
				"<index-file-content>",
				renderMemoryIndexContentChinese(*store.Index),
				"</index-file-content>",
			)
		}
		lines = append(lines, fmt.Sprintf("</memory-index-%d>", i+1), "")
	}
	lines = append(lines, "</system-reminder>")
	return strings.Join(lines, "\n")
}

func renderMemoryIndexContentEnglish(index memoryIndexPromptInfo) string {
	if index.Empty {
		return "The index file is currently empty."
	}
	lines := []string{index.Content}
	if index.Truncated {
		lines = append(lines, strings.ReplaceAll(getAppendCurrentIndexTruncNotify(), "{memory_lines}", fmt.Sprintf("%d", index.Lines)))
	}
	return strings.Join(lines, "\n")
}

func renderMemoryIndexContentChinese(index memoryIndexPromptInfo) string {
	if index.Empty {
		return "索引文件当前为空。"
	}
	lines := []string{index.Content}
	if index.Truncated {
		lines = append(lines, strings.ReplaceAll(getAppendCurrentIndexTruncNotify(), "{memory_lines}", fmt.Sprintf("%d", index.Lines)))
	}
	return strings.Join(lines, "\n")
}

func buildExtractionMemoryManifest(stores []memoryManifestStorePromptInfo) string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: buildExtractionMemoryManifestEnglish(stores),
		Chinese: buildExtractionMemoryManifestChinese(stores),
	})
}

func buildExtractionMemoryManifestEnglish(stores []memoryManifestStorePromptInfo) string {
	var lines []string
	for _, store := range stores {
		lines = append(lines, fmt.Sprintf("### %s", store.Name))
		lines = append(lines, fmt.Sprintf("Store path: %s", store.Mount))
		if len(store.Files) == 0 {
			lines = append(lines, "- No existing memory files.")
			continue
		}
		for _, file := range store.Files {
			if file.Description != "" {
				lines = append(lines, fmt.Sprintf("- %s (path: %s, saved %s): %s", file.MemoryPath, file.AbsPath, file.Saved, file.Description))
			} else {
				lines = append(lines, fmt.Sprintf("- %s (path: %s, saved %s)", file.MemoryPath, file.AbsPath, file.Saved))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func buildExtractionMemoryManifestChinese(stores []memoryManifestStorePromptInfo) string {
	var lines []string
	for _, store := range stores {
		lines = append(lines, fmt.Sprintf("### %s", store.Name))
		lines = append(lines, fmt.Sprintf("存储路径：%s", store.Mount))
		if len(store.Files) == 0 {
			lines = append(lines, "- 暂无已有 memory 文件。")
			continue
		}
		for _, file := range store.Files {
			if file.Description != "" {
				lines = append(lines, fmt.Sprintf("- %s（路径：%s，保存时间：%s）：%s", file.MemoryPath, file.AbsPath, file.Saved, file.Description))
			} else {
				lines = append(lines, fmt.Sprintf("- %s（路径：%s，保存时间：%s）", file.MemoryPath, file.AbsPath, file.Saved))
			}
		}
	}
	return strings.Join(lines, "\n")
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

func getDefaultMemoryInstruction(enableIndex bool) string {
	english := defaultMemoryInstructionWithoutIndex
	chinese := defaultMemoryInstructionChineseWithoutIndex
	if enableIndex {
		english = defaultMemoryInstructionWithIndex
		chinese = defaultMemoryInstructionChineseWithIndex
	}
	return internal.SelectPrompt(internal.I18nPrompts{
		English: english,
		Chinese: chinese,
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

func buildExtractHowToSaveEnglish(enableMemoryIndex bool) []string {
	if !enableMemoryIndex {
		return []string{
			"## How to save memories",
			"",
			"Write each memory to its own file. Do not create duplicate files.",
			"",
			"- Organize memory semantically by topic, not chronologically.",
			"- Update or remove memories that turn out to be wrong or outdated.",
			"- Do not write duplicate memories.",
		}
	}
	return []string{
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
}

func buildExtractHowToSaveChinese(enableMemoryIndex bool) []string {
	if !enableMemoryIndex {
		return []string{
			"## 如何保存记忆",
			"",
			"将每条记忆写入各自独立的文件中，不要创建重复文件。",
			"",
			"- 按主题组织记忆，而不是按时间顺序堆叠。",
			"- 当记忆被证明错误或过时时，要及时更新或删除。",
			"- 不要写入重复记忆。",
		}
	}
	return []string{
		"## 如何保存记忆",
		"",
		"保存记忆分为两步：",
		"",
		"第 1 步：将记忆写入独立文件。",
		"第 2 步：在 MEMORY.md 中添加指向该文件的索引。MEMORY.md 只是索引，不应存放记忆正文。",
		"",
		"- 保持 MEMORY.md 简洁，因为它会被加载进系统提示词。",
		"- 按主题组织记忆，而不是按时间顺序堆叠。",
		"- 当记忆被证明错误或过时时，要及时更新或删除。",
		"- 不要写入重复记忆。",
	}
}

func buildExtractAutoOnlyPromptEnglish(memoryStores string, newMessageCount int, existingMemories string, enableMemoryIndex bool) string {
	manifest := ""
	if existingMemories != "" {
		manifest = fmt.Sprintf("\n\n## Existing memory files\n\n%s\n\nCheck this list before writing — update an existing file rather than creating a duplicate.", existingMemories)
	}

	howToSave := buildExtractHowToSaveEnglish(enableMemoryIndex)

	parts := []string{
		fmt.Sprintf("You are now acting as the memory extraction subagent. Analyze only the most recent ~%d messages above and use them to update persistent memory.", newMessageCount),
		"",
		memoryStores,
		"",
		"Available tools: read_file, glob, write_file, edit_file. Only paths inside the memory stores are allowed. Use absolute paths or the listed relative path prefixes when reading or writing memory files. All other tools are denied.",
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

func buildExtractAutoOnlyPromptChinese(memoryStores string, newMessageCount int, existingMemories string, enableMemoryIndex bool) string {
	manifest := ""
	if existingMemories != "" {
		manifest = fmt.Sprintf("\n\n## 现有记忆文件\n\n%s\n\n写入前请先检查这份列表，优先更新已有文件，而不是创建重复记忆。", existingMemories)
	}

	howToSave := buildExtractHowToSaveChinese(enableMemoryIndex)

	parts := []string{
		fmt.Sprintf("你现在扮演记忆提取子智能体。只分析上方最近约 %d 条消息，并用它们来更新持久化记忆。", newMessageCount),
		"",
		memoryStores,
		"",
		"可用工具：read_file、glob、write_file、edit_file。只允许访问记忆存储内的路径。读写记忆文件时请使用绝对路径，或使用上方列出的相对路径前缀。其他工具均禁止使用。",
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
