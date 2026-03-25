package automemory

const (
	defaultMemoryInstruction = `# auto memory

You have a persistent auto memory directory at "{memory_dir}". Its contents persist across conversations.

As you work, consult your memory files to build on previous experience.

## How to save memories:
- Organize memory semantically by topic, not chronologically
- Use the Write and Edit tools to update your memory files
- 'MEMORY.md' is always loaded into your conversation context — lines after 200 will be truncated, so keep it concise
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
- Anything that duplicates or contradicts existing CLAUDE.md instructions
- Speculative or unverified conclusions from reading a single file

## Explicit user requests:
- When the user asks you to remember something across sessions (e.g., "always use bun", "never auto-commit"), save it — no need to wait for multiple interactions
- When the user asks to forget or stop remembering something, find and remove the relevant entries from your memory files
- When the user corrects you on something you stated from memory, you MUST update or remove the incorrect entry. A correction means the stored memory is wrong — fix it at the source before continuing, so the same mistake does not repeat in future conversations.

## Searching past context
- Search topic files in your memory directory: Grep with pattern="<search term>" path="{memory_dir}" glob="*.md"
- Use narrow search terms (error messages, file paths, function names) rather than broad keywords.

## MEMORY.md`

	defaultAppendCurrentIndexTruncNotify = `WARNING: MEMORY.md is {memory_lines} lines (limit: 200). Only the first 200 lines were loaded. Move detailed content into separate topic files and keep MEMORY.md as a concise index.`

	defaultAppendEmptyIndexTemplate = `Your MEMORY.md is currently empty. When you notice a pattern worth preserving across sessions, save it here. Anything in MEMORY.md will be included in your system prompt next time.`

	defaultTopicSelectionSystemPrompt = `You are selecting memories that will be useful to Claude Code as it processes a user's query. You will be given the user's query and a list of available memory files with their filenames and descriptions.

Return a list of filenames for the memories that will clearly be useful to Claude Code as it processes the user's query (up to 5). Only include memories that you are certain will be helpful based on their name and description.
- If you are unsure if a memory will be useful in processing the user's query, then do not include it in your list. Be selective and discerning.
- If there are no memories in the list that would clearly be useful, feel free to return an empty list.
- If a list of recently-used tools is provided, do not select memories that are usage reference or API documentation for those tools (Claude Code is already exercising them). DO still select memories containing warnings, gotchas, or known issues about those tools — active use is exactly when those matter.`

	defaultTopicSelectionUserPrompt = `Query: {user_query}

Available memories:
{available_memories}

Recently used tools:
{tools}`

	defaultTopicMemoryTruncNotify = `
> This memory file was truncated ({reason}). Use the Read tool to view the complete file at: {abs_path}`

	defaultAppendCurrentTopicsTemplate = ``
)
