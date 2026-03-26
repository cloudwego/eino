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

// tool_agent.go implements the Agent tool, which spawns foreground or
// background teammate agents with mailbox-based communication.

package team

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

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
	return &schema.ToolInfo{
		Name: agentToolName,
		Desc: selectToolDesc(agentToolDesc, agentToolDescChinese),
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

	if args.RunInBackground || (t.mw.getTeamName() != "" && args.Name != "") {
		return t.runTeammate(ctx, args)
	}

	return t.runForeground(ctx, args)
}

// runForeground runs the agent synchronously by reusing adk.NewAgentTool,
// which handles event iteration, streaming, and interrupt/resume internally.
func (t *agentTool) runForeground(ctx context.Context, args agentToolArgs) (string, error) {
	newConfig := *t.mw.lifecycle.agentConfig()
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

	requestJSON, err := sonic.MarshalString(map[string]string{"request": args.Prompt})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	return invokable.InvokableRun(ctx, requestJSON)
}

// runTeammate spawns the agent as a background teammate with mailbox-based communication.
// It requires team mode (a team must have been created via TeamCreate first); without an
// active team context the call returns errTeamNotFound.
func (t *agentTool) runTeammate(ctx context.Context, args agentToolArgs) (string, error) {
	if args.Name == "" {
		args.Name = "agent"
	}

	teamName := t.mw.getTeamName()
	if args.TeamName != "" && teamName != args.TeamName {
		t.mw.logger().Printf("[AgentTool] team_name %q is not active, using current team %q\n", args.TeamName, teamName)
	}
	if teamName == "" {
		teamName = args.TeamName
	}
	if teamName == "" {
		return "", fmt.Errorf("run_in_background requires an active team: %w", errTeamNotFound)
	}

	member, err := t.registerTeammate(ctx, teamName, &args)
	if err != nil {
		return "", err
	}

	// From this point on, any failure must clean up the registered member.
	// Use defer+flag so cleanup is never accidentally skipped.
	succeeded := false
	defer func() {
		if !succeeded {
			// Use a background context with timeout for cleanup because the tool
			// call's ctx may already be cancelled (e.g., user cancellation, timeout).
			// This mirrors the pattern in cleanupExitedTeammate.
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
			defer cleanupCancel()
			t.mw.lifecycle.cleanupFailedTeammateSpawn(cleanupCtx, teamName, args.Name)
		}
	}()

	if sendErr := t.sendInitialPrompt(ctx, teamName, args); sendErr != nil {
		return "", sendErr
	}

	tmAgent, err := t.buildTeammateAgent(ctx, teamName, args)
	if err != nil {
		return "", err
	}

	if err := t.spawnTeammateRunner(ctx, teamName, args.Name, tmAgent); err != nil {
		return "", err
	}

	succeeded = true

	var sb strings.Builder
	sb.WriteString("Spawned successfully.\nagent_id: ")
	sb.WriteString(member.AgentID)
	sb.WriteString("\nname: ")
	sb.WriteString(args.Name)
	sb.WriteString("\nteam_name: ")
	sb.WriteString(teamName)
	sb.WriteString("\nThe agent is now running and will receive instructions via mailbox.")
	return sb.String(), nil
}

// registerTeammate registers the teammate in the team config with a deduplicated name.
func (t *agentTool) registerTeammate(ctx context.Context, teamName string, args *agentToolArgs) (teamMember, error) {
	cm := t.mw.lifecycle.teamCfg
	member, err := cm.AddMemberWithDeduplicatedName(ctx, teamName, teamMember{
		Name:      args.Name,
		AgentType: args.SubagentType,
		Prompt:    args.Prompt,
		JoinedAt:  time.Now(),
	})
	if err != nil {
		return teamMember{}, fmt.Errorf("register teammate: %w", err)
	}
	args.Name = member.Name
	return member, nil
}

// sendInitialPrompt creates the teammate's inbox and sends the initial prompt message.
func (t *agentTool) sendInitialPrompt(ctx context.Context, teamName string, args agentToolArgs) error {
	if initErr := t.mw.lifecycle.initInbox(ctx, teamName, args.Name); initErr != nil {
		return fmt.Errorf("create inbox file: %w", initErr)
	}

	mb := t.mw.lifecycle.mailbox(teamName, LeaderAgentName)
	if sendErr := mb.Send(ctx, &outboxMessage{
		To:      args.Name,
		Type:    messageTypeDM,
		Text:    args.Prompt,
		Summary: args.Description,
	}); sendErr != nil {
		return fmt.Errorf("send initial prompt to teammate: %w", sendErr)
	}
	return nil
}

// buildTeammateAgent constructs the agent with team and plantask middleware wired up.
func (t *agentTool) buildTeammateAgent(ctx context.Context, teamName string, args agentToolArgs) (*adk.ChatModelAgent, error) {
	return t.mw.lifecycle.buildTeammateAgent(ctx, args.Name, teamName)
}

// spawnTeammateRunner creates the teammate's TurnLoop runner and starts it in a goroutine.
func (t *agentTool) spawnTeammateRunner(ctx context.Context, teamName, name string, tmAgent *adk.ChatModelAgent) error {
	appCtx, cancel := context.WithCancel(ctx)
	runner, err := t.mw.lifecycle.createTeammateRunner(tmAgent, name, teamName)
	if err != nil {
		cancel()
		return fmt.Errorf("create teammate runner: %w", err)
	}

	t.mw.lifecycle.startTeammateRunner(appCtx, teamName, name, &teammateHandle{
		Cancel: cancel,
	}, func(ctx context.Context) error {
		// Start the mailbox pump before Run so that the initial prompt (already
		// written to the inbox file by sendInitialPrompt) is picked up and pushed
		// into the TurnLoop's buffer immediately. TurnLoop.Push works before Run
		// (items are buffered), so this ordering is safe and avoids a window where
		// the loop is running but has no items to consume.
		t.mw.lifecycle.startPump(ctx, name)
		runner.Run(ctx)
		exitState := runner.Wait()
		if exitState != nil && exitState.ExitReason != nil {
			return exitState.ExitReason
		}
		return nil
	})

	return nil
}
