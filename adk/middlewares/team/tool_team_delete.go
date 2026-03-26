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
	if teamName == "" {
		return "", errTeamNotFound
	}

	cm := t.mw.lifecycle.configStore()

	activeNames, err := cm.GetActiveTeammateNames(ctx, teamName)
	if err != nil {
		return "", err
	}
	if len(activeNames) > 0 {
		result := marshalToolResult(map[string]any{
			"success":   false,
			"message":   fmt.Sprintf("Team %q still has active teammates [%s], shut them down first via SendMessage with shutdown_request", teamName, strings.Join(activeNames, ", ")),
			"team_name": teamName,
		})
		return result, nil
	}

	if err := cm.DeleteTeam(ctx, teamName); err != nil {
		return "", err
	}

	t.mw.lifecycle.cleanupLeaderMailbox()

	t.mw.setTeamName("")

	result := marshalToolResult(map[string]any{
		"success":   true,
		"message":   fmt.Sprintf("Cleaned up directories for team %q", teamName),
		"team_name": teamName,
	})

	return result, nil
}
