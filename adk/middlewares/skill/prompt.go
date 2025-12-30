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
# 技能系统

**如何使用技能（渐进式展示）：**

技能遵循**渐进式展示**模式 - 你可以在上方看到技能的名称和描述，但只在需要时才阅读完整说明：

1. **识别技能适用场景**：检查用户的任务是否匹配某个技能的描述
2. **阅读技能的完整说明**：使用 '{tool_name}' 工具加载 skill
3. **遵循技能说明操作**：工具结果包含逐步工作流程、最佳实践和示例
4. **访问支持文件**：技能可能包含辅助脚本、配置或参考文档 - 使用绝对路径访问

**何时使用技能：**
- 用户请求匹配某个技能的领域（例如"研究 X" -> web-research 技能）
- 你需要专业知识或结构化工作流程
- 某个技能为复杂任务提供了经过验证的模式

**执行技能脚本：**
技能可能包含 Python 脚本或其他可执行文件。始终使用绝对路径。

**示例工作流程：**

用户："你能研究一下量子计算的最新发展吗？"

1. 检查可用技能 -> 发现 "web-research" 技能
2. 调用 '{tool_name}' 工具读取完整的技能说明
3. 遵循技能的研究工作流程（搜索 -> 整理 -> 综合）
4. 使用绝对路径运行任何辅助脚本

记住：技能让你更加强大和稳定。如有疑问，请检查是否存在适用于该任务的技能！
`

	toolDescriptionBase = `Execute a skill within the main conversation

<skills_instructions>
When users ask you to perform tasks, check if any of the available skills below can help complete the task more effectively. Skills provide specialized capabilities and domain knowledge.

How to invoke:
- Use this tool with the skill name only (no arguments)
- Examples:
  - ` + "`" + `skill: "pdf"` + "`" + ` - invoke the pdf skill
  - ` + "`" + `skill: "xlsx"` + "`" + ` - invoke the xlsx skill
  - ` + "`" + `skill: "ms-office-suite:pdf"` + "`" + ` - invoke using fully qualified name

Important:
- When a skill is relevant, you must invoke this tool IMMEDIATELY as your first action
- NEVER just announce or mention a skill in your text response without actually calling this tool
- This is a BLOCKING REQUIREMENT: invoke the relevant Skill tool BEFORE generating any other response about the task
- Only use skills listed in <available_skills> below
- Do not invoke a skill that is already running
- Do not use this tool for built-in CLI commands (like /help, /clear, etc.)
</skills_instructions>

`
	toolDescriptionBaseChinese = `在主对话中执行技能

<技能指令>
当用户要求你执行任务时，检查下方可用技能列表中是否有技能可以更有效地完成任务。技能提供专业能力和领域知识。

如何调用：
- 仅使用技能名称调用此工具（无需其他参数）
- 示例：
  - ` + "`" + `skill: "pdf"` + "`" + ` - 调用 pdf 技能
  - ` + "`" + `skill: "xlsx"` + "`" + ` - 调用 xlsx 技能
  - ` + "`" + `skill: "ms-office-suite:pdf"` + "`" + ` - 使用完全限定名称调用

重要说明：
- 当技能相关时，你必须立即调用此工具作为第一个动作
- 切勿仅在文本回复中提及技能而不实际调用此工具
- 这是阻塞性要求：在生成任何关于任务的其他响应之前，先调用相关的技能工具
- 仅使用 <可用技能> 中列出的技能
- 不要调用已经运行中的技能
- 不要将此工具用于内置 CLI 命令（如 /help、/clear 等）
</技能指令>

`
	toolDescriptionTemplate = `
<available_skills>
{{- range .Matters }}
<skill>
<name>
{{ .Name }}
</name>
<description>
{{ .Description }}
</description>
</skill>
{{- end }}
</available_skills>
`
	toolDescriptionTemplateChinese = `
<可用技能>
{{- range .Matters }}
<技能>
<名称>
{{ .Name }}
</名称>
<描述>
{{ .Description }}
</描述>
</技能>
{{- end }}
</可用技能>
`
	toolResult        = "Launching skill: %s\n"
	toolResultChinese = "正在启动技能：%s\n"
	userContent       = `Base directory for this skill: %s

%s`
	userContentChinese = `此技能的目录：%s

%s`
	toolName = "skill"
)
