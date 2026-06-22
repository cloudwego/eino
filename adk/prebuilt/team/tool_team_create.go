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

// tool_team_create.go implements the TeamCreate tool, which initialises a new
// team's directory structure, config, leader inbox, and mailbox pump.

package team

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

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
	return &schema.ToolInfo{
		Name: teamCreateToolName,
		Desc: selectToolDesc(teamCreateToolDesc, teamCreateToolDescChinese),
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
	if currentTeamName := t.mw.getTeamName(); currentTeamName != "" {
		return "", fmt.Errorf("team %q is already active, delete it before creating a new team", currentTeamName)
	}

	cm := t.mw.lifecycle.teamCfg
	team, err := cm.CreateTeam(ctx, args.TeamName, args.Description, LeaderAgentName, args.AgentType)
	if err != nil {
		return "", fmt.Errorf("create team: %w", err)
	}

	// Use the resolved team name (may differ from args.TeamName if there was
	// a name collision and a timestamped suffix was appended).
	teamName := team.Name

	succeeded := false
	defer func() {
		if !succeeded {
			// Use a background context with timeout for cleanup because the tool
			// call's ctx may already be cancelled (e.g., user cancellation, timeout).
			// This mirrors the pattern in cleanupExitedTeammate.
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
			defer cleanupCancel()
			_ = cm.DeleteTeam(cleanupCtx, teamName)
			if t.mw.getTeamName() == teamName {
				t.mw.setTeamName("")
			}
		}
	}()

	// Create the leader's inbox file, mailbox source, and start the pump atomically.
	if err = t.mw.lifecycle.setupMailbox(ctx, teamName, LeaderAgentName, &MailboxSourceConfig{
		OwnerName:          LeaderAgentName,
		Role:               teamRoleLeader,
		OnShutdownResponse: t.makeShutdownResponseHandler(teamName),
	}); err != nil {
		return "", fmt.Errorf("setup leader mailbox: %w", err)
	}

	// Persist teamName on the middleware so subsequent turns can re-inject it.
	t.mw.setTeamName(teamName)

	succeeded = true

	result := marshalToolResult(map[string]any{
		"team_name":      team.Name,
		"team_file_path": cm.configFilePath(team.Name),
		"lead_agent_id":  cm.LeadAgentID(team.Name),
	})
	return result, nil
}

// makeShutdownResponseHandler returns a callback for handling shutdown_response messages.
// It removes the member from team config, unassigns their tasks, and cancels the teammate goroutine.
func (t *teamCreateTool) makeShutdownResponseHandler(teamName string) func(ctx context.Context, fromName string) (string, error) {
	return func(ctx context.Context, fromName string) (string, error) {
		unassigned, firstStop, err := t.mw.lifecycle.removeTeammate(ctx, teamName, fromName)
		// When firstStop is false, the teammate's goroutine already exited and
		// cleanupExitedTeammate sent a termination notification via the router.
		// Return "" so handleLeaderControlMessages skips the duplicate notification.
		if !firstStop {
			return "", nil
		}
		// firstStop=true means we are the first to stop this teammate. Always
		// generate a notification so the leader learns about the exit, even if
		// cleanup (unassign/remove) partially failed — cleanupExitedTeammate
		// will not send a duplicate because firstStop will be false there.
		if err != nil {
			t.mw.logger().Printf("removeTeammate(%s) partial cleanup error: %v", fromName, err)
			return buildTeammateTerminationMessage(fromName, unassigned), nil
		}
		return buildTeammateTerminationMessage(fromName, unassigned), nil
	}
}
