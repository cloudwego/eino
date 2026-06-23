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

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type teamDeleteTool struct {
	mw *teamMiddleware
}

func newTeamDeleteTool(mw *teamMiddleware) *teamDeleteTool {
	return &teamDeleteTool{mw: mw}
}

type teamDeleteArgs struct {
	// Force allows deletion even when config.json still lists non-leader members
	// (e.g. a prior teardown failed or the process restarted before cleanup). It
	// does not bypass the running-goroutine guard.
	Force bool `json:"force,omitempty"`
}

func (t *teamDeleteTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: teamDeleteToolName,
		Desc: selectToolDesc(teamDeleteToolDesc, teamDeleteToolDescChinese),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"force": {
				Type:     schema.Boolean,
				Desc:     "Delete the team even if config.json still lists members (e.g. after a failed cleanup). Does not bypass the running-teammate check.",
				Required: false,
			},
		}),
	}, nil
}

func (t *teamDeleteTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	args := teamDeleteArgs{}
	if strings.TrimSpace(argumentsInJSON) != "" {
		if err := sonic.UnmarshalString(argumentsInJSON, &args); err != nil {
			return "", fmt.Errorf("%s parse arguments failed: %w", teamDeleteToolName, err)
		}
	}

	// Serialize the whole "check running teammates → delete dirs → clear team
	// name" sequence against the other leader-only lifecycle tools (TeamCreate,
	// Agent spawn). Without this lock a concurrent Agent spawn could register a
	// member and start a goroutine just after this tool observed an empty
	// teammate registry, leaving a live teammate bound to a team whose
	// directories are being deleted.
	t.mw.teamOpLock.Lock()
	defer t.mw.teamOpLock.Unlock()

	teamName := t.mw.getTeamName()

	if teamName != "" {
		// Check the teammate registry for goroutines that are still running.
		// Liveness is determined solely by the in-process registry: a running
		// teammate has a live goroutine that would be disrupted by deleting the
		// team directories. Busy/idle status is never persisted to config.json.
		runningNames := t.mw.lifecycle.activeTeammateNames()
		if len(runningNames) > 0 {
			return marshalToolResult(map[string]any{
				"success":   false,
				"message":   fmt.Sprintf("Team %q still has active teammates [%s], shut them down first via SendMessage with shutdown_request", teamName, strings.Join(runningNames, ", ")),
				"team_name": teamName,
			}), nil
		}

		// No goroutine is running, but config.json — the persistent source of
		// truth — may still list members if a previous teardown failed or the
		// process restarted before cleanup completed. Deleting the team directory
		// would silently discard that recoverable state, so refuse unless the
		// caller explicitly forces it.
		if !args.Force {
			residual, err := t.mw.lifecycle.store.NonLeaderMemberNames(ctx, teamName)
			if err != nil {
				return "", fmt.Errorf("%s check residual members for %q: %w", teamDeleteToolName, teamName, err)
			}
			if len(residual) > 0 {
				return marshalToolResult(map[string]any{
					"success":   false,
					"message":   fmt.Sprintf("Team %q still lists member(s) [%s] in config.json with no running goroutine (likely incomplete cleanup). Verify they are done, then retry with force=true to delete anyway.", teamName, strings.Join(residual, ", ")),
					"team_name": teamName,
				}), nil
			}
		}

		cm := t.mw.lifecycle.store
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
