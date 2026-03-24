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
	"log"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

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

type agentToolArgs struct {
	Name            string `json:"name"`
	Prompt          string `json:"prompt"`
	Description     string `json:"description,omitempty"`
	SubagentType    string `json:"subagent_type,omitempty"`
	TeamName        string `json:"team_name,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

type agentTool struct {
	mw *teamMiddleware
}

func newAgentTool(mw *teamMiddleware) *agentTool {
	return &agentTool{mw: mw}
}

func (t *agentTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: agentToolDesc,
		Chinese: agentToolDescChinese,
	})

	return &schema.ToolInfo{
		Name: agentToolName,
		Desc: desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {
				Type: schema.String,
				Desc: "Name for the spawned agent. Makes it addressable via SendMessage({to: name}) while running.",
			},
			"prompt": {
				Type:     schema.String,
				Desc:     "The task for the agent to perform",
				Required: true,
			},
			"description": {
				Type:     schema.String,
				Desc:     "A short (3-5 word) description of the task",
				Required: true,
			},
			"subagent_type": {
				Type: schema.String,
				Desc: "The type of specialized agent to use for this task",
			},
			"team_name": {
				Type: schema.String,
				Desc: "Team name for spawning. Uses current team context if omitted.",
			},
			"run_in_background": {
				Type: schema.Boolean,
				Desc: "Set to true to run this agent in the background. You will be notified when it completes.",
			},
		}),
	}, nil
}

func (t *agentTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args agentToolArgs
	if err := sonic.UnmarshalString(argumentsInJSON, &args); err != nil {
		return "", fmt.Errorf("parse Agent args: %w", err)
	}

	if args.Prompt == "" || args.Description == "" {
		return "", fmt.Errorf("prompt and description are required")
	}

	if !args.RunInBackground {
		return t.runForeground(ctx, args)
	}

	return t.runBackground(ctx, args)
}

// runForeground runs the agent synchronously by reusing adk.NewAgentTool,
// which handles event iteration, streaming, and interrupt/resume internally.
func (t *agentTool) runForeground(ctx context.Context, args agentToolArgs) (string, error) {
	newConfig := *t.mw.conf.AgentConfig
	newConfig.Instruction = args.Prompt

	agent, err := adk.NewChatModelAgent(ctx, &newConfig)
	if err != nil {
		return "", fmt.Errorf("create agent: %w", err)
	}

	agentToolInstance := adk.NewAgentTool(ctx, agent)
	invokable, ok := agentToolInstance.(tool.InvokableTool)
	if !ok {
		return "", fmt.Errorf("agent tool does not implement InvokableTool")
	}

	return invokable.InvokableRun(ctx, args.Prompt)
}

// runBackground spawns the agent as a background teammate with mailbox-based communication.
// It requires team mode (a team must have been created via TeamCreate first); without an
// active team context the call returns errTeamNotFound.
func (t *agentTool) runBackground(ctx context.Context, args agentToolArgs) (string, error) {
	if args.Name == "" {
		args.Name = "agent"
	}
	if err := validateAgentName(args.Name); err != nil {
		return "", err
	}

	teamName := t.mw.teamName
	if args.TeamName != "" {
		teamName = args.TeamName
	}
	if teamName != "" {
		if err := validateTeamName(teamName); err != nil {
			return "", err
		}
	}
	if teamName == "" {
		return "", fmt.Errorf("run_in_background requires an active team: %w", errTeamNotFound)
	}

	cm := t.mw.configStore()
	wrappedPrompt := wrapTeammatePrompt(args.Description, args.Prompt)

	member, err := cm.AddMemberWithDeduplicatedName(ctx, teamName, teamMember{
		Name:      args.Name,
		AgentType: args.SubagentType,
		Prompt:    args.Prompt,
		JoinedAt:  time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("register teammate: %w", err)
	}
	args.Name = member.Name
	agentID := member.AgentID

	if initErr := t.mw.initInbox(ctx, teamName, args.Name); initErr != nil {
		t.mw.cleanupFailedTeammateSpawn(ctx, teamName, args.Name)
		return "", fmt.Errorf("create inbox file: %w", initErr)
	}

	tmMW := newTeamTeammateMiddleware(t.mw.conf, args.Name, teamName)

	// Always inject team-aware plantask middleware for teammate, removing any user-provided one.
	defaultHandlers := []adk.ChatModelAgentMiddleware{tmMW}
	ptMW, err := newTeamPlantaskMiddleware(ctx, t.mw.conf,
		func() string { return tmMW.teamName },
		func() string { return tmMW.agentName },
	)
	if err != nil {
		t.mw.cleanupFailedTeammateSpawn(ctx, teamName, args.Name)
		return "", fmt.Errorf("create teammate plantask middleware: %w", err)
	}
	defaultHandlers = append(defaultHandlers, ptMW)

	handlers := append(defaultHandlers, stripPlantaskMiddleware(t.mw.conf.AgentConfig.Handlers)...)

	newConfig := *t.mw.conf.AgentConfig
	newConfig.Handlers = handlers
	newConfig.Instruction = wrappedPrompt

	tmAgent, err := adk.NewChatModelAgent(ctx, &newConfig)
	if err != nil {
		t.mw.cleanupFailedTeammateSpawn(ctx, teamName, args.Name)
		return "", fmt.Errorf("create teammate agent: %w", err)
	}

	appCtx, cancel := context.WithCancel(ctx)
	runner, err := newTeammateRunner(t.mw.conf, t.mw.router, tmAgent, args.Name, teamName)
	if err != nil {
		cancel()
		t.mw.cleanupFailedTeammateSpawn(ctx, teamName, args.Name)
		return "", fmt.Errorf("create teammate runner: %w", err)
	}

	// Create an _internal shadow task for this teammate.
	// This task stays in_progress while the teammate is alive, preventing
	// checkIfNeedDeleteAllTasks from clearing the task list prematurely.
	var localTaskID string
	taskDesc := args.Prompt
	if len(taskDesc) > 100 {
		taskDesc = taskDesc[:100]
	}
	var createErr error
	for attempt := 0; attempt < 3; attempt++ {
		var taskID string
		taskID, createErr = t.mw.ptMW.CreateTask(ctx, &plantask.TaskInput{
			Subject:     args.Name,
			Description: taskDesc,
			Status:      "in_progress",
			Metadata:    map[string]any{plantask.MetadataKeyInternal: true},
		})
		if createErr == nil {
			localTaskID = taskID
			break
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
		}
	}
	if createErr != nil {
		log.Printf("team agent: failed to create _internal task for teammate=%s team=%s: %v", args.Name, teamName, createErr)
	}

	t.mw.startTeammateRunner(appCtx, teamName, args.Name, &spawnResult{
		Cancel:      cancel,
		LocalTaskID: localTaskID,
	}, runner.Run)

	var sb strings.Builder
	sb.WriteString("Spawned successfully.\nagent_id: ")
	sb.WriteString(agentID)
	sb.WriteString("\nname: ")
	sb.WriteString(args.Name)
	sb.WriteString("\nteam_name: ")
	sb.WriteString(teamName)
	sb.WriteString("\nThe agent is now running and will receive instructions via mailbox.")
	return sb.String(), nil
}

func safeGo(f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("safeGo panic: %v\n", r)
			}
		}()
		f()
	}()
}

// wrapTeammatePrompt wraps the raw prompt with teammate metadata in XML format.
func wrapTeammatePrompt(description, prompt string) string {
	return formatTeammateMessageEnvelope(LeaderAgentName, prompt, description)
}
