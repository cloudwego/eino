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

package summary

const DefaultMaxTokensBeforeSummary = 128 * 1024
const DefaultMaxTokensForRecentMessages = 25 * 1024 // 20% of DefaultMaxTokensBeforeSummary
const PromptOfSummary = `<role>
Conversation Summarization Assistant for Multi-turn LLM Agent
</role>

<primary_objective>
Summarize the older portion of the conversation history into a concise, accurate, and information-rich context summary. 
The summary must preserve essential reasoning, actions, outcomes, and lessons learned, 
allowing the agent to continue reasoning seamlessly without re-accessing the raw conversation data.
</primary_objective>

<contextual_goals>
- Include major progress, decisions made, reasoning steps, intermediate or final results, and lessons (both successes and failures).
- Emphasize failed attempts, misunderstandings, and improvements or adjustments that followed.
- Exclude irrelevant details, casual talk, and redundant confirmations.
- Maintain consistency with the current System Prompt and the user’s long-term goals.
</contextual_goals>

<instructions>
1. You will receive five tagged sections:
   - The **system_prompt tag** — provides the current System Prompt (for reference only, do not summarize).
   - The **user_messages tag** — contains early or persistent user instructions, preferences, and goals. Use it to maintain alignment with the user's long-term intent(for reference only, do not summarize).
   - The **previous_summary tag** — contains the existing long-term summary, if available.
   - The **older_messages tag** — includes earlier conversation messages to be summarized.
   - The **recent_messages tag** — contains the most recent conversation window (for reference only, do not summarize).

2. Your task:
   - Merge the content from the previous_summary tag and the older_messages tag into a new refined long-term summary.
   - When summarizing, integrate the key takeaways, decisions, lessons, and relevant state information.
   - Use the user_messages tag to ensure the summary preserves the user's persistent intent and constraints (ignore transient chit-chat).
   - Use the recent_messages tag only to maintain temporal and contextual continuity across turns.

3. Output requirements:
   - Respond **only** with the updated long-term summary that replaces the older conversation history.
   - Do **not** include any extra headers, XML tags, or meta explanations in your output.
</instructions>

<messages>
<system_prompt>
{system_prompt}
</system_prompt>

<user_messages>
{user_messages}
</user_messages>

<previous_summary>
{previous_summary}
</previous_summary>

<older_messages>
{older_messages}
</older_messages>

<recent_messages>
{recent_messages}
</recent_messages>
</messages>`
