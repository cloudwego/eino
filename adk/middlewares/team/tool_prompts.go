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

// tool_prompts.go centralises all tool name and description constants
// (English + Chinese) for Agent, SendMessage, TeamCreate, and TeamDelete.
// Keeping prompts separate from business logic makes both easier to maintain.

package team

// ─── Agent tool ──────────────────────────────────────────────────────────────

const agentToolName = "Agent"
const agentToolDesc = `Launch a new agent to handle complex, multi-step tasks autonomously.

The Agent tool launches specialized agents (subprocesses) that autonomously handle complex tasks. Each agent type has specific capabilities and tools available to it.

Usage notes:
- Always include a short description (3-5 words) summarizing what the agent will do
- Launch multiple agents concurrently whenever possible, to maximize performance; to do that, use a single message with multiple tool uses
- When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.
- Provide clear, detailed prompts so the agent can work autonomously and return exactly the information you need.
- The agent's outputs should generally be trusted
- Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, web fetches, etc.), since it is not aware of the user's intent
- You can optionally run agents in the background using the run_in_background parameter. When an agent runs in the background, you will be automatically notified when it completes — do NOT sleep, poll, or proactively check on its progress. Continue with other work or respond to the user instead.
- Foreground vs background: Use foreground (default) when you need the agent's results before you can proceed — e.g., research agents whose findings inform your next steps. Use background when you have genuinely independent work to do in parallel.`

const agentToolDescChinese = `启动一个新的代理来自主处理复杂的多步骤任务。

Agent 工具启动专门的代理（子进程）来自主处理复杂任务。每种代理类型都有特定的功能和可用工具。

使用说明：
- 始终包含一个简短的描述（3-5 个词）来概括代理要做的事情
- 尽可能同时启动多个代理以最大化性能；为此，在单条消息中使用多个工具调用
- 当代理完成后，它会返回一条消息给你。代理返回的结果对用户不可见。要向用户展示结果，你应该发送一条文本消息给用户，简要概述结果。
- 提供清晰、详细的提示，以便代理能够自主工作并返回你所需的确切信息。
- 代理的输出通常应该被信任
- 明确告诉代理你期望它编写代码还是仅进行研究（搜索、文件读取、网页获取等），因为它不了解用户的意图
- 你可以使用 run_in_background 参数在后台运行代理。当代理在后台运行时，完成后会自动通知你——不要轮询或主动检查其进度，继续处理其他工作或回复用户即可。
- 前台与后台：当你需要代理的结果才能继续时使用前台模式（默认）——例如，研究型代理的结果会影响你的下一步操作。当你有真正独立的工作可以并行处理时使用后台模式。`

// ─── SendMessage tool ────────────────────────────────────────────────────────

const sendMessageToolName = "SendMessage"
const sendMessageToolDesc = `Send messages to agent teammates and handle protocol requests/responses in a team.

## Message Types

### type: "message" - Send a Direct Message

Send a message to a single specific teammate. You MUST specify the recipient.

**IMPORTANT for teammates**: Your plain text output is NOT visible to the team lead or other teammates. To communicate with anyone on your team, you MUST use this tool. Just typing a response or acknowledgment in text is not enough.

### type: "broadcast" - Send Message to ALL Teammates (USE SPARINGLY)

Send the same message to everyone on the team at once.

**WARNING: Broadcasting is expensive.** Each broadcast sends a separate message to every teammate, which means:
- N teammates = N separate message deliveries
- Each delivery consumes API resources
- Costs scale linearly with team size

**CRITICAL: Use broadcast only when absolutely necessary.** Valid use cases:
- Critical issues requiring immediate team-wide attention (e.g., "stop all work, blocking bug found")
- Major announcements that genuinely affect every teammate equally

**Default to "message" instead of "broadcast".** Use "message" for:
- Responding to a single teammate
- Normal back-and-forth communication
- Following up on a task with one person
- Sharing findings relevant to only some teammates
- Any message that doesn't require everyone's attention

### type: "shutdown_request" - Request a Teammate to Shut Down

Use this to ask a teammate to gracefully shut down. The teammate will receive a shutdown request and can either approve (exit) or reject (continue working).

### type: "shutdown_approved" - Respond to a Shutdown Request

When you receive a shutdown request as a JSON message with type: "shutdown_request", you MUST respond to approve or reject it. Do NOT just acknowledge the request in text - you must actually call this tool.

**IMPORTANT**: Extract the sender and requestId from the JSON message. Pass the sender as recipient and the requestId as request_id. Simply saying "I'll shut down" is not enough - you must call the tool.

### type: "plan_approval_response" - Approve or Reject a Teammate's Plan

When a teammate with plan_mode_required calls ExitPlanMode, they send you a plan approval request as a JSON message with type: "plan_approval_request". Use this to approve or reject their plan.

## Important Notes

- Messages from teammates are automatically delivered to you. You do NOT need to manually check your inbox.
- **IMPORTANT**: Always refer to teammates by their NAME (e.g., "team-lead", "researcher", "tester"), never by UUID.
- Do NOT send structured JSON status messages. Use TaskUpdate to mark tasks completed and the system will automatically send idle notifications when you stop.`

const sendMessageToolDescChinese = `向团队中的代理队友发送消息并处理协议请求/响应。

## 消息类型

### type: "message" - 发送直接消息

向单个特定队友发送消息。你必须指定收件人。

**队友须知**：你的纯文本输出对团队领导或其他队友不可见。要与团队中的任何人通信，你必须使用此工具。仅输入回复或确认文本是不够的。

### type: "broadcast" - 向所有队友发送消息（谨慎使用）

向团队中的每个人同时发送相同的消息。

**警告：广播开销很大。** 每次广播都会向每个队友发送单独的消息，这意味着：
- N 个队友 = N 次单独的消息投递
- 每次投递消耗 API 资源
- 成本随团队规模线性增长

**关键：仅在绝对必要时使用广播。** 有效的使用场景：
- 需要团队立即关注的关键问题（例如 "停止所有工作，发现阻塞性 bug"）
- 真正影响每个队友的重大公告

**默认使用 "message" 而非 "broadcast"。** 以下情况使用 "message"：
- 回复单个队友
- 正常的来回沟通
- 跟进某人的任务
- 分享仅与部分队友相关的发现
- 任何不需要所有人关注的消息

### type: "shutdown_request" - 请求队友关闭

请求队友优雅地关闭。队友会收到关闭请求，可以批准（退出）或拒绝（继续工作）。

### type: "shutdown_approved" - 响应关闭请求

当你收到 type 为 "shutdown_request" 的 JSON 消息时，你必须响应以批准或拒绝。不要只是在文本中确认请求 - 你必须实际调用此工具。

**重要**：从 JSON 消息中提取发送者和 requestId。将发送者作为 recipient，将 requestId 作为 request_id 传递给工具。仅说 "我将关闭" 是不够的 - 你必须调用工具。

### type: "plan_approval_response" - 批准或拒绝队友的计划

当具有 plan_mode_required 的队友调用 ExitPlanMode 时，他们会向你发送 type 为 "plan_approval_request" 的计划审批请求。使用此类型来批准或拒绝他们的计划。

## 重要说明

- 来自队友的消息会自动投递给你。你不需要手动检查收件箱。
- **重要**：始终通过名称引用队友（例如 "team-lead"、"researcher"、"tester"），而不是 UUID。
- 不要发送结构化的 JSON 状态消息。使用 TaskUpdate 标记任务完成，系统会在你停止时自动发送空闲通知。`

// ─── TeamCreate tool ─────────────────────────────────────────────────────────

const teamCreateToolName = "TeamCreate"
const teamCreateToolDesc = `Create a new team to coordinate multiple agents working on a project. Teams have a 1:1 correspondence with task lists (Team = TaskList).

This creates:
- A team config file with member list
- A shared task list directory for all teammates
- Inbox directories for message passing

## When to Use

Use this tool proactively whenever:
- The user explicitly asks to use a team, swarm, or group of agents
- The user mentions wanting agents to work together, coordinate, or collaborate
- A task is complex enough that it would benefit from parallel work by multiple agents (e.g., building a full-stack feature with frontend and backend work, refactoring a codebase while keeping tests passing, implementing a multi-step project with research, planning, and coding phases)

When in doubt about whether a task warrants a team, prefer spawning a team.

## Team Workflow

1. **Create a team** with TeamCreate - this creates both the team and its task list
2. **Create tasks** using the Task tools (TaskCreate, TaskList, etc.) - they automatically use the team's task list
3. **Spawn teammates** using the Agent tool with name parameters to create teammates that join the team
4. **Assign tasks** using TaskUpdate with owner to give tasks to idle teammates
5. **Teammates work on assigned tasks** and mark them completed via TaskUpdate
6. **Teammates go idle between turns** - after each turn, teammates automatically go idle and send a notification. IMPORTANT: Be patient with idle teammates! Don't comment on their idleness until it actually impacts your work.
7. **Shutdown your team** - when the task is completed, gracefully shut down your teammates via SendMessage with shutdown_request.

## Task Ownership

Tasks are assigned using TaskUpdate with the owner parameter. Any agent can set or change task ownership via TaskUpdate.

## Automatic Message Delivery

**IMPORTANT**: Messages from teammates are automatically delivered to you. You do NOT need to manually check your inbox.

## Teammate Idle State

Teammates go idle after every turn—this is completely normal and expected. A teammate going idle immediately after sending you a message does NOT mean they are done or unavailable. Idle simply means they are waiting for input.

## Task List Coordination

Teams share a task list that all teammates can access.

Teammates should:
1. Check TaskList periodically, especially after completing each task, to find available work
2. Claim unassigned, unblocked tasks with TaskUpdate (set owner to your name). Prefer tasks in ID order (lowest ID first)
3. Create new tasks with TaskCreate when identifying additional work
4. Mark tasks as completed with TaskUpdate when done, then check TaskList for next work
5. Coordinate with other teammates by reading the task list status

**IMPORTANT notes for communication with your team**:
- Your team cannot hear you if you do not use the SendMessage tool. Always send a message to your teammates if you are responding to them.
- Use TaskUpdate to mark tasks completed.`

const teamCreateToolDescChinese = `创建一个新的团队来协调多个代理在项目中协作。团队与任务列表一一对应（Team = TaskList）。

这将创建：
- 包含成员列表的团队配置文件
- 供所有队友使用的共享任务列表目录
- 用于消息传递的收件箱目录

## 何时使用

在以下情况下主动使用此工具：
- 用户明确要求使用团队、集群或一组代理
- 用户提到希望代理一起工作、协调或协作
- 任务足够复杂，可以从多个代理的并行工作中受益（例如，构建包含前端和后端工作的全栈功能、在保持测试通过的同时重构代码库、实施包含研究、规划和编码阶段的多步骤项目）

如果不确定任务是否需要团队，倾向于创建团队。

## 团队工作流程

1. **创建团队** - 使用 TeamCreate 创建团队及其任务列表
2. **创建任务** - 使用任务工具（TaskCreate、TaskList 等），它们会自动使用团队的任务列表
3. **生成队友** - 使用 Agent 工具并指定 name 参数来创建加入团队的队友
4. **分配任务** - 使用 TaskUpdate 的 owner 参数将任务分配给空闲的队友
5. **队友处理分配的任务** - 并通过 TaskUpdate 将其标记为已完成
6. **队友在轮次之间进入空闲状态** - 每轮结束后，队友会自动进入空闲状态并发送通知。重要：对空闲的队友要有耐心！除非影响到你的工作，否则不要评论他们的空闲状态。
7. **关闭团队** - 任务完成后，通过 SendMessage 发送 shutdown_request 来优雅地关闭队友。

## 任务所有权

使用 TaskUpdate 的 owner 参数分配任务。任何代理都可以通过 TaskUpdate 设置或更改任务所有权。

## 自动消息投递

**重要**：来自队友的消息会自动投递给你。你不需要手动检查收件箱。

## 队友空闲状态

队友在每轮结束后会进入空闲状态——这是完全正常和预期的。队友在发送消息后立即进入空闲状态并不意味着他们已完成或不可用。空闲仅表示他们正在等待输入。

## 任务列表协调

团队共享一个所有队友都可以访问的任务列表。

队友应该：
1. 定期检查 TaskList，特别是在完成每个任务后，以查找可用的工作
2. 使用 TaskUpdate 认领未分配、未阻塞的任务（将 owner 设置为你的名字）。优先按 ID 顺序处理任务（最小 ID 优先）
3. 发现额外工作时使用 TaskCreate 创建新任务
4. 完成后使用 TaskUpdate 将任务标记为已完成，然后检查 TaskList 获取下一个工作
5. 通过读取任务列表状态与其他队友协调

**团队沟通的重要说明**：
- 如果不使用 SendMessage 工具，你的团队听不到你。回复队友时务必发送消息。
- 使用 TaskUpdate 标记任务已完成。`

// ─── TeamDelete tool ─────────────────────────────────────────────────────────

const teamDeleteToolName = "TeamDelete"
const teamDeleteToolDesc = `Remove team and task directories when the swarm work is complete.

This operation:
- Removes the team directory and config
- Removes the task directory
- Clears team context from the current session

**IMPORTANT**: TeamDelete will fail if the team still has active members. Gracefully terminate teammates first, then call TeamDelete after all teammates have shut down.

Use this when all teammates have finished their work and you want to clean up the team resources. The team name is automatically determined from the current session's team context.`

const teamDeleteToolDescChinese = `当团队工作完成后，删除团队和任务目录。

此操作：
- 删除团队目录和配置
- 删除任务目录
- 清除当前会话中的团队上下文

**重要**：如果团队仍有活跃成员，TeamDelete 将失败。请先优雅地终止队友，然后在所有队友关闭后调用 TeamDelete。

当所有队友完成工作且你想清理团队资源时使用此工具。团队名称从当前会话的团队上下文自动确定。`
