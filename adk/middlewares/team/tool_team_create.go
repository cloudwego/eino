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

package team

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

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

type teamCreateArgs struct {
	TeamName    string `json:"team_name"`
	Description string `json:"description,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
}

type teamCreateTool struct {
	mw *teamMiddleware
}

func newTeamCreateTool(mw *teamMiddleware) *teamCreateTool {
	return &teamCreateTool{mw: mw}
}

func (t *teamCreateTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: teamCreateToolDesc,
		Chinese: teamCreateToolDescChinese,
	})

	return &schema.ToolInfo{
		Name: teamCreateToolName,
		Desc: desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"team_name": {
				Type:     schema.String,
				Desc:     "Name for the new team to create.",
				Required: true,
			},
			"description": {
				Type: schema.String,
				Desc: "Team description/purpose.",
			},
			"agent_type": {
				Type: schema.String,
				Desc: `Type/role of the team lead (e.g., "researcher", "test-runner"). Used for team file and inter-agent coordination.`,
			},
		}),
	}, nil
}

func (t *teamCreateTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args teamCreateArgs
	if err := sonic.UnmarshalString(argumentsInJSON, &args); err != nil {
		return "", fmt.Errorf("parse TeamCreate args: %w", err)
	}

	if args.TeamName == "" {
		return "", fmt.Errorf("team_name is required")
	}
	if err := validateTeamName(args.TeamName); err != nil {
		return "", err
	}
	if currentTeamName := t.mw.teamName; currentTeamName != "" {
		return "", fmt.Errorf("team %q is already active, delete it before creating a new team", currentTeamName)
	}

	cm := t.mw.configStore()
	team, err := cm.CreateTeam(ctx, args.TeamName, args.Description, LeaderAgentName, args.AgentType)
	if err != nil {
		return "", fmt.Errorf("create team: %w", err)
	}

	// Use the resolved team name (may differ from args.TeamName if there was
	// a name collision and a timestamped suffix was appended).
	teamName := team.Name

	cleanup := func() {
		_ = cm.DeleteTeam(ctx, teamName)
		if t.mw.teamName == teamName {
			t.mw.teamName = ""
		}
	}

	// Create the leader's inbox file and mailbox now that teamName is known.
	if err = t.mw.initInbox(ctx, teamName, LeaderAgentName); err != nil {
		cleanup()
		return "", fmt.Errorf("create leader inbox file: %w", err)
	}

	// Persist teamName on the middleware so subsequent turns can re-inject it.
	t.mw.teamName = teamName

	leaderMailbox := t.mw.mailbox(teamName, LeaderAgentName)
	leaderMailboxSource := NewMailboxMessageSource(leaderMailbox, &MailboxSourceConfig{
		OwnerName:          LeaderAgentName,
		Role:               teamRoleLeader,
		OnShutdownApproval: t.makeShutdownApprovalHandler(teamName),
		IdleNotifyInterval: t.mw.conf.TeamConfig.IdleNotifyInterval,
	})
	t.mw.router.SetMailbox(LeaderAgentName, leaderMailboxSource)

	result, _ := sonic.MarshalString(map[string]any{
		"team_name":      team.Name,
		"team_file_path": cm.configFilePath(team.Name),
		"lead_agent_id":  cm.LeadAgentID(team.Name),
	})
	return result, nil
}

// makeShutdownApprovalHandler returns a callback for handling shutdown_approved messages.
// It removes the member from team config, unassigns their tasks, and cancels the teammate goroutine.
func (t *teamCreateTool) makeShutdownApprovalHandler(teamName string) func(ctx context.Context, fromName string) (string, error) {
	return func(ctx context.Context, fromName string) (string, error) {
		unassigned, err := t.mw.removeTeammate(ctx, teamName, fromName)
		if err != nil {
			return "", err
		}
		return t.mw.buildTeammateTerminationMessage(fromName, unassigned), nil
	}
}
