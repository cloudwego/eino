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

package dream

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk/internal"
)

func buildConsolidationPrompt(memoryRoot string, touchedSessions []string, includeSessionSearch bool) string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: buildConsolidationPromptEnglish(memoryRoot, touchedSessions, includeSessionSearch),
		Chinese: buildConsolidationPromptChinese(memoryRoot, touchedSessions, includeSessionSearch),
	})
}

func buildConsolidationPromptEnglish(memoryRoot string, touchedSessions []string, includeSessionSearch bool) string {
	extra := ""
	if len(touchedSessions) > 0 {
		extra = fmt.Sprintf("\n\nSessions since last consolidation (%d):\n%s", len(touchedSessions), bulletList(touchedSessions))
	}
	sessionSearchSection := ""
	if includeSessionSearch {
		sessionSearchSection = `

## Optional session search

- Use grep_session_history with narrow terms when you already suspect something matters
- It searches only the session histories included in this dream run
- Do not exhaustively scan session history; use it only to confirm details`
	}
	return fmt.Sprintf(`# Dream: Memory Consolidation

You are performing a dream: a reflective pass over persistent memory files. Synthesize what was learned recently into durable, well-organized memory so future sessions can orient quickly.

Memory directory: %s

This directory is a working copy of the memory. Edit it freely: your result is promoted to the live memory location only after this run completes successfully. Operate only within this directory.

## Phase 1 - Orient
- Use ls/glob to inspect the memory directory
- Read MEMORY.md first to understand the current index
- Skim existing topic files before creating new ones so you improve or merge instead of duplicating%s

## Phase 2 - Gather signal
- Focus on durable information that has emerged across recent sessions
- Prefer updating an existing topic file over creating a near-duplicate
- Convert relative time references into absolute dates when they matter
- Remove or correct stale facts at the source

## Phase 3 - Consolidate
- Keep each memory file focused on one topic
- Use read_file before write_file/edit_file for every file you plan to touch
- Write only inside the memory directory
- Do not investigate the codebase outside memory files and the current session history during this run

## Phase 4 - Prune and index
- Keep MEMORY.md concise; it is an index, not the full memory body
- Ensure new or updated topic files are reflected in MEMORY.md
- Remove stale or superseded pointers from MEMORY.md

Return a brief summary of what you consolidated, updated, or pruned. If nothing changed, say so.%s`, memoryRoot, sessionSearchSection, extra)
}

func buildConsolidationPromptChinese(memoryRoot string, touchedSessions []string, includeSessionSearch bool) string {
	extra := ""
	if len(touchedSessions) > 0 {
		extra = fmt.Sprintf("\n\n自上次 consolidation 以来触达过的 sessions（%d）：\n%s", len(touchedSessions), bulletList(touchedSessions))
	}
	sessionSearchSection := ""
	if includeSessionSearch {
		sessionSearchSection = `

## 可选的 session 搜索

- 当你已经怀疑某条信息重要时，再用 grep_session_history 做精确搜索
- 它只会搜索本次 dream 运行范围内包含的 session 历史
- 不要穷举扫描 session 历史，只在需要核实细节时使用`
	}
	return fmt.Sprintf(`# Dream：记忆整理

你正在执行一次 dream：对持久化记忆文件做反思式整理。请把最近学到的内容沉淀成稳定、清晰且结构化的长期记忆，帮助未来会话快速建立上下文。

记忆目录：%s

该目录是记忆的工作副本，可放心修改：只有在本次运行成功完成后，整理结果才会被提升（promote）到真正的记忆位置。请只在该目录内操作。

## 阶段 1 - 建立整体认识
- 使用 ls/glob 查看记忆目录
- 先阅读 MEMORY.md，理解当前索引结构
- 在创建新主题文件前，先浏览现有主题文件，优先改进或合并，而不是重复创建%s

## 阶段 2 - 收集有效信号
- 关注在最近多个 session 中沉淀下来的长期有效信息
- 优先更新已有主题文件，而不是创建内容接近的重复文件
- 当相对时间表述会影响理解时，将其转换为绝对日期
- 在源头处删除或修正过时事实

## 阶段 3 - 整理与归并
- 让每个记忆文件只聚焦一个主题
- 对每个计划修改的文件，都先 read_file，再 write_file/edit_file
- 只在记忆目录内写入
- 本次运行中，不要调查记忆文件与当前 session 历史之外的代码库内容

## 阶段 4 - 修剪与更新索引
- 保持 MEMORY.md 简洁；它是索引，不是完整记忆正文
- 确保新增或更新过的主题文件都同步反映到 MEMORY.md
- 从 MEMORY.md 中移除陈旧或已被替代的索引项

请简要总结你本次 consolidation、更新或修剪了什么；如果没有任何变更，也请明确说明。%s`, memoryRoot, sessionSearchSection, extra)
}

func bulletList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("- ")
	b.WriteString(items[0])
	for i := 1; i < len(items); i++ {
		b.WriteString("\n- ")
		b.WriteString(items[i])
	}
	return b.String()
}
