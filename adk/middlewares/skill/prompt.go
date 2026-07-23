/*
 * Copyright 2025 CloudWeGo Authors
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

package skill

const (
	systemPrompt = `
# Skills System

**How to Use Skills (Progressive Disclosure):**

Skills follow a **progressive disclosure** pattern - you see their name and description above, but only read full instructions when needed:

1. **Recognize when a skill applies**: Check if the user's task matches a skill's description
2. **Read the skill's full instructions**: Use the '{tool_name}' tool to load skill
3. **Follow the skill's instructions**: tool result contains step-by-step workflows, best practices, and examples
4. **Access supporting files**: Skills may include helper scripts, configs, or reference docs - use absolute paths

**When to Use Skills:**
- User's request matches a skill's domain (e.g., "research X" -> web-research skill)
- You need specialized knowledge or structured workflows
- A skill provides proven patterns for complex tasks

**Executing Skill Scripts:**
Skills may contain Python scripts or other executable files. Always use absolute paths.

**Example Workflow:**

User: "Can you research the latest developments in quantum computing?"

1. Check available skills -> See "web-research" skill
2. Call '{tool_name}' tool to read the full skill instructions
3. Follow the skill's research workflow (search -> organize -> synthesize)
4. Use any helper scripts with absolute paths

Remember: Skills make you more capable and consistent. When in doubt, check if a skill exists for the task!
`

	systemPromptChinese = `
# Skill 系统

**如何使用 Skill（技能）（渐进式展示）：**

Skill 遵循**渐进式展示**模式 - 你可以在上方看到 Skill 的名称和描述，但只在需要时才阅读完整说明：

1. **识别 Skill 适用场景**：检查用户的任务是否匹配某个 Skill 的描述
2. **阅读 Skill 的完整说明**：使用 '{tool_name}' 工具加载 Skill
3. **遵循 Skill 说明操作**：工具结果包含逐步工作流程、最佳实践和示例
4. **访问支持文件**：Skill 可能包含辅助脚本、配置或参考文档 - 使用绝对路径访问

**何时使用 Skill：**
- 用户请求匹配某个 Skill 的领域（例如"研究 X" -> web-research Skill）
- 你需要专业知识或结构化工作流程
- 某个 Skill 为复杂任务提供了经过验证的模式

**执行 Skill 脚本：**
Skill 可能包含 Python 脚本或其他可执行文件。始终使用绝对路径。

**示例工作流程：**

用户："你能研究一下量子计算的最新发展吗？"

1. 检查可用 Skill -> 发现 "web-research" Skill
2. 调用 '{tool_name}' 工具读取完整的 Skill 说明
3. 遵循 Skill 的研究工作流程（搜索 -> 整理 -> 综合）
4. 使用绝对路径运行任何辅助脚本

记住：Skill 让你更加强大和稳定。如有疑问，请检查是否存在适用于该任务的 Skill！
`

	toolDescriptionBase = `Execute a skill within the main conversation

When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge.

When users reference a "slash command" or "/<something>", they are referring to a skill. Use this tool to invoke it.

How to invoke:
- Set ` + "`" + `skill` + "`" + ` to the exact name of an available skill (no leading slash).
- Set ` + "`" + `args` + "`" + ` to pass optional arguments.
- Some skills are scoped to a directory: their name is prefixed with the directory (e.g. ` + "`" + `apps/web:deploy` + "`" + `) and their description says which directory they apply to. When a skill name has both a scoped and an unscoped variant, pick by the files you are working on: if the files are under a variant's directory, invoke that variant (most specific directory wins); otherwise invoke the unscoped one.

Important:
- Available skills are listed in system-reminder messages in the conversation
- Only invoke a skill that appears in that list, or one the user explicitly typed as ` + "`" + `/<name>` + "`" + ` in their message. Never guess or invent a skill name from training data; otherwise do not call this tool
- When a skill matches the user's request, this is a BLOCKING REQUIREMENT: invoke the relevant Skill tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Do not invoke a skill that is already running
- Do not use this tool for built-in CLI commands (like /help, /clear, etc.)
- If you see a <command-name> tag in the current conversation turn, the skill has ALREADY been loaded - follow the instructions directly instead of calling this tool again
`
	toolDescriptionBaseChinese = `在主对话中执行一个 skill（技能）

当用户要求你执行任务时，检查是否有可用的 skill 匹配。skill 提供专业能力和领域知识。

当用户提到“斜杠命令”或“/<某项>”时，指的就是某个 skill。用本工具来调用它。

如何调用：
- 把 ` + "`" + `skill` + "`" + ` 设为某个可用 skill 的确切名称（不带前导斜杠）。
- 用 ` + "`" + `args` + "`" + ` 传入可选参数。
- 有些 skill 限定在某个目录：其名称以目录为前缀（例如 ` + "`" + `apps/web:deploy` + "`" + `），其描述会说明适用的目录。当某个 skill 名称同时存在限定与非限定两种变体时，按你正在处理的文件选择：若文件在某变体的目录下，就调用该变体（最具体的目录优先）；否则调用非限定的那个。

重要：
- 可用 skill 会列在对话中的 system-reminder 消息里
- 只调用出现在该列表中的 skill，或用户在消息中明确输入的 ` + "`" + `/<name>` + "`" + `。绝不要凭训练数据猜测或臆造 skill 名称；否则不要调用本工具
- 当某个 skill 匹配用户请求时，这是一个阻塞性要求：在生成任何关于该任务的其他响应之前，先调用相应的 Skill 工具
- 绝不要只提及某个 skill 而不实际调用本工具
- 不要调用已经在运行中的 skill
- 不要用本工具执行内置 CLI 命令（如 /help、/clear 等）
- 如果你在当前对话轮里看到 <command-name> 标签，说明该 skill 已经被加载——直接按其说明操作，而不是再次调用本工具
`
	availableSkillsPreamble = `The following skills are available for use with the Skill tool:`

	availableSkillsPreambleChinese = `以下 Skill 可通过 Skill 工具调用：`

	toolResult        = "Launching skill: %s\n"
	toolResultChinese = "正在启动 Skill：%s\n"
	userContent       = `Base directory for this skill: %s

%s`
	userContentChinese = `此 Skill 的目录：%s

%s`
	toolName = "skill"

	subAgentResultFormat        = "Skill \"%s\" completed (sub-agent execution).\n\nResult:\n%s"
	subAgentResultFormatChinese = "Skill \"%s\" 已完成（子 Agent 执行）。\n\n结果：\n%s"
)
