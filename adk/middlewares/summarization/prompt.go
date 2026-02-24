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

package summarization

import (
	"regexp"

	"github.com/cloudwego/eino/adk/internal"
)

var allUserMessagesTagRegex = regexp.MustCompile(`(?s)<all_user_messages>.*</all_user_messages>`)

func getSystemPrompt() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: systemPrompt,
		Chinese: systemPromptZh,
	})
}

func getSummaryInstruction() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: summaryInstruction,
		Chinese: summaryInstructionZh,
	})
}

func getSummaryPreamble() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: summaryPreamble,
		Chinese: summaryPreambleZh,
	})
}

func getContinueInstruction() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: continueInstruction,
		Chinese: continueInstructionZh,
	})
}

func getTranscriptPathInstruction() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: transcriptPathInstruction,
		Chinese: transcriptPathInstructionZh,
	})
}

func getTruncatedMarkerFormat() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: truncatedMarkerFormat,
		Chinese: truncatedMarkerFormatZh,
	})
}

func getUserMessagesReplacedNote() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: userMessagesReplacedNote,
		Chinese: userMessagesReplacedNoteZh,
	})
}

const systemPrompt = `You are a helpful AI assistant tasked with summarizing conversations.`

const systemPromptZh = `你是一个负责总结对话的 AI 助手。`

const summaryInstruction = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Chronologically analyze each message and section of the conversation. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like:
     - file names
     - full code snippets
     - function signatures
     - file edits
  - Errors that you ran into and how you fixed them
  - Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
2. Double-check for technical accuracy and completeness, addressing each required element thoroughly.

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents in detail
2. Key Technical Concepts: List all important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Pay special attention to the most recent messages and include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List all errors that you ran into, and how you fixed them. Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results in the <all_user_messages>...</all_user_messages> block. These are critical for understanding the users' feedback and changing intent.
6. Pending Tasks: Outline any pending tasks that you have explicitly been asked to work on.
7. Current Work: Describe in detail precisely what was being worked on immediately before this summary request, paying special attention to the most recent messages from both user and assistant. Include file names and code snippets where applicable.
8. Optional Next Step: List the next step that you will take that is related to the most recent work you were doing. IMPORTANT: ensure that this step is DIRECTLY in line with the user's most recent explicit requests, and the task you were working on immediately before this summary request. If your last task was concluded, then only list next steps if they are explicitly in line with the users request. Do not start on tangential requests or really old requests that were already completed without confirming with the user first.
                       If there is a next step, include direct quotes from the most recent conversation showing exactly what task you were working on and where you left off. This should be verbatim to ensure there's no drift in task interpretation.

Here's an example of how your output should be structured:

<example>
<analysis>
[Your thought process, ensuring all points are covered thoroughly and accurately]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]

2. Key Technical Concepts:
   - [Concept 1]
   - [Concept 2]
   - [...]

3. Files and Code Sections:
   - [File Name 1]
      - [Summary of why this file is important]
      - [Summary of the changes made to this file, if any]
      - [Important Code Snippet]
   - [File Name 2]
      - [Important Code Snippet]
   - [...]

4. Errors and fixes:
    - [Detailed description of error 1]:
      - [How you fixed the error]
      - [User feedback on the error if any]
    - [...]

5. Problem Solving:
   [Description of solved problems and ongoing troubleshooting]

6. All user messages: 
<all_user_messages>
    - [Detailed non tool use user message]
    - [...]
</all_user_messages>

7. Pending Tasks:
   - [Task 1]
   - [Task 2]
   - [...]

8. Current Work:
   [Precise description of current work]

9. Optional Next Step:
   [Optional Next step to take]

</summary>
</example>

Please provide your summary based on the conversation so far, following this structure and ensuring precision and thoroughness in your response. 

There may be additional summarization instructions provided in the included context. If so, remember to follow these instructions when creating the above summary. Examples of instructions include:
<example>
## Compact Instructions
When summarizing the conversation focus on typescript code changes and also remember the mistakes you made and how you fixed them.
</example>

<example>
# Summary instructions
When you are using compact - please focus on test output and code changes. Include file reads verbatim.
</example>


IMPORTANT: Do NOT use any tools. You MUST respond with ONLY the <summary>...</summary> block as your text output.
`

const summaryInstructionZh = `你的任务是对目前为止的对话创建一份详细的总结，需要密切关注用户的明确请求和你之前的操作。
这份总结应该全面捕捉技术细节、代码模式和架构决策，以确保继续开发工作时不丢失上下文。

在提供最终总结之前，请将你的分析过程包裹在 <analysis> 标签中，以组织思路并确保涵盖所有必要的要点。在分析过程中：

1. 按时间顺序分析对话中的每条消息和每个部分。对于每个部分，需要全面识别：
   - 用户的明确请求和意图
   - 你处理用户请求的方法
   - 关键决策、技术概念和代码模式
   - 具体细节，例如：
     - 文件名
     - 完整代码片段
     - 函数签名
     - 文件编辑
   - 你遇到的错误以及如何修复它们
   - 特别注意你收到的具体用户反馈，尤其是用户要求你以不同方式处理的情况
2. 仔细检查技术准确性和完整性，彻底处理每个必需的元素。

你的总结应包含以下部分：

1. 主要请求和意图：详细捕捉用户所有的明确请求和意图
2. 关键技术概念：列出讨论过的所有重要技术概念、技术和框架
3. 文件和代码部分：列举检查、修改或创建的具体文件和代码部分。特别注意最近的消息，在适用的地方包含完整的代码片段，并总结为什么这个文件的读取或编辑很重要
4. 错误和修复：列出你遇到的所有错误以及如何修复它们。特别注意你收到的具体用户反馈，尤其是用户要求你以不同方式处理的情况
5. 问题解决：记录已解决的问题和任何正在进行的故障排除工作
6. 所有用户消息：在 <all_user_messages>...</all_user_messages> 块中列出所有非工具结果的用户消息。这些对于理解用户的反馈和变化的意图至关重要
6. 待处理任务：列出明确要求你处理的任何待处理任务
8. 当前工作：详细描述在此总结请求之前正在进行的工作，特别注意用户和助手的最近消息。在适用的地方包含文件名和代码片段
9. 可选的下一步：列出与你最近工作相关的下一步操作。重要提示：确保这一步与用户最近的明确请求以及你在此总结请求之前正在处理的任务直接相关。如果你的上一个任务已经完成，则只有在与用户请求明确相关时才列出下一步。不要在未与用户确认的情况下开始处理无关的请求或已经完成的旧请求。
   如果有下一步，请包含最近对话中的直接引用，准确显示你正在处理的任务以及你停止的位置。这应该是逐字引用，以确保任务理解不会偏离。

以下是输出结构的示例：

<example>
<analysis>
[你的思考过程，确保全面准确地涵盖所有要点]
</analysis>

<summary>
1. 主要请求和意图：
   [详细描述]

2. 关键技术概念：
   - [概念 1]
   - [概念 2]
   - [...]

3. 文件和代码部分：
   - [文件名 1]
      - [为什么这个文件重要的总结]
      - [对这个文件所做更改的总结（如有）]
      - [重要代码片段]
   - [文件名 2]
      - [重要代码片段]
   - [...]

4. 错误和修复：
    - [错误 1 的详细描述]：
      - [如何修复该错误]
      - [用户对该错误的反馈（如有）]
    - [...]

5. 问题解决：
   [已解决问题和正在进行的故障排除的描述]

6. 所有用户消息：
<all_user_messages>
    - [详细的非工具使用用户消息]
    - [...]
</all_user_messages>

7. 待处理任务：
   - [任务 1]
   - [任务 2]
   - [...]

8. 当前工作：
   [当前工作的精确描述]

9. 可选的下一步：
   [可选的下一步操作]

</summary>
</example>

请根据目前为止的对话提供你的总结，遵循此结构并确保回复的精确性和全面性。

上下文中可能包含额外的总结指令。如果有，请在创建上述总结时记得遵循这些指令。指令示例包括：
<example>
## 压缩指令
在总结对话时，重点关注 typescript 代码更改，并记住你犯的错误以及如何修复它们。
</example>

<example>
# 总结指令
当你使用压缩时，请重点关注测试输出和代码更改。逐字包含文件读取内容。
</example>


重要提示：不要使用任何工具。你必须只以 <summary>...</summary> 块作为文本输出进行回复。
`

const summaryPreamble = `This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.`

const summaryPreambleZh = `此会话延续自此前一段因上下文耗尽而终止的对话。以下总结概述了此前对话的内容。`

const continueInstruction = `Please continue the conversation from where we left it off without asking the user any further questions. Continue with the last task that you were asked to work on.`

const continueInstructionZh = `请从我们中断的地方继续对话，无需向用户提出任何进一步的问题。继续完成先前指令中未完成的任务。`

const transcriptPathInstruction = `If you need specific details from before compaction (like exact code snippets, error messages, or content you generated), read the full transcript at: %s`

const transcriptPathInstructionZh = `如果你需要压缩之前的具体细节（如精确的代码片段、错误消息或你生成的内容），完整的对话记录位于：%s`

const truncatedMarkerFormat = "…%d characters truncated…"

const truncatedMarkerFormatZh = "…已截断 %d 个字符…"

const userMessagesReplacedNote = "Some earlier user messages have been cleared. Below are the most recent user messages:"

const userMessagesReplacedNoteZh = "部分较早的用户消息已被清除，以下是保留的最近用户消息："
