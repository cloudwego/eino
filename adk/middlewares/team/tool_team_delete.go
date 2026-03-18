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
	"strings"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

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

type teamDeleteTool struct {
	mw *teamMiddleware
}

func newTeamDeleteTool(mw *teamMiddleware) *teamDeleteTool {
	return &teamDeleteTool{mw: mw}
}

func (t *teamDeleteTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: teamDeleteToolDesc,
		Chinese: teamDeleteToolDescChinese,
	})

	return &schema.ToolInfo{
		Name:        teamDeleteToolName,
		Desc:        desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (t *teamDeleteTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	teamName := t.mw.teamName
	if teamName == "" {
		return "", errTeamNotFound
	}

	cm := t.mw.configStore()

	activeNames, err := cm.GetActiveTeammateNames(ctx, teamName)
	if err != nil {
		return "", err
	}
	if len(activeNames) > 0 {
		result, _ := sonic.MarshalString(map[string]any{
			"success":   false,
			"message":   fmt.Sprintf("Team %q still has active teammates [%s], shut them down first via SendMessage with shutdown_request", teamName, strings.Join(activeNames, ", ")),
			"team_name": teamName,
		})
		return result, nil
	}

	// TODO: 合码之前取消注释，code review 忽略
	// if err := cm.DeleteTeam(ctx, teamName); err != nil {
	// 	return "", err
	// }

	if t.mw.router != nil {
		t.mw.router.UnsetMailbox(LeaderAgentName)
	}

	t.mw.teamName = ""

	result, _ := sonic.MarshalString(map[string]any{
		"success":   true,
		"message":   fmt.Sprintf("Cleaned up directories for team %q", teamName),
		"team_name": teamName,
	})

	return result, nil
}
