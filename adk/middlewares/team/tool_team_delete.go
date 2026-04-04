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

// tool_team_delete.go implements the TeamDelete tool, which removes the
// team directory, tasks directory, and clears session state.

package team

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type teamDeleteTool struct {
	mw *teamMiddleware
}

func newTeamDeleteTool(mw *teamMiddleware) *teamDeleteTool {
	return &teamDeleteTool{mw: mw}
}

func (t *teamDeleteTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        teamDeleteToolName,
		Desc:        selectToolDesc(teamDeleteToolDesc, teamDeleteToolDescChinese),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (t *teamDeleteTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	teamName := t.mw.getTeamName()

	if teamName != "" {
		// Check the teammate registry for goroutines that are still running.
		// This is more accurate than the config-level IsActive flag, which
		// only tracks idle/busy status — an idle teammate (IsActive=false)
		// still has a live goroutine that would be disrupted by deleting the
		// team directories.
		runningNames := t.mw.lifecycle.activeTeammateNames()
		if len(runningNames) > 0 {
			return marshalToolResult(map[string]any{
				"success":   false,
				"message":   fmt.Sprintf("Team %q still has active teammates [%s], shut them down first via SendMessage with shutdown_request", teamName, strings.Join(runningNames, ", ")),
				"team_name": teamName,
			}), nil
		}

		cm := t.mw.lifecycle.teamCfg
		if err := cm.DeleteTeam(ctx, teamName); err != nil {
			return "", fmt.Errorf("delete team %q: %w", teamName, err)
		}
	}

	// Always clean up state, even when no team name exists.
	t.mw.lifecycle.cleanupLeaderMailbox()
	t.mw.setTeamName("")

	msg := "No team name found, nothing to clean up"
	if teamName != "" {
		msg = fmt.Sprintf("Cleaned up directories for team %q", teamName)
	}

	return marshalToolResult(map[string]any{
		"success":   true,
		"message":   msg,
		"team_name": teamName,
	}), nil
}
