package skill

const (
	skillToolDescriptionBase = `Execute a skill within the main conversation

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
	skillToolDescriptionTemplate = `
<available_skills>
{{- range .Skills }}
<skill>
<name>
{{ .Name }}
</name>
<description>
{{ .Description }}
</description>
{{- range $k, $v := .Extra }}
<{{ $k }}>
{{ $v }}
</{{ $k }}>
{{- end }}
</skill>
{{- end }}
</available_skills>
` // todo: claude-code 里还有 location 属性，enterprise、personal、project、plugin 等级别
	skillToolResult  = "Launching skill: %s\n"
	skillUserContent = `Base directory for this skill: %s

%s`
)
