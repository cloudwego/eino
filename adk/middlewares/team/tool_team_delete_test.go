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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTeamDeleteTool_Info(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamDeleteTool(mw)

	info, err := tool.Info(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, teamDeleteToolName, info.Name)
}

func TestTeamDeleteTool_InvokableRun_NoActiveTeam(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamDeleteTool(mw)

	_, err := tool.InvokableRun(context.Background(), "")
	assert.ErrorIs(t, err, errTeamNotFound)
}

func TestTeamDeleteTool_InvokableRun_ActiveTeammates(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	cm := conf.configStore()
	err = cm.AddMember(ctx, mw.getTeamName(), teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	deleteTool := newTeamDeleteTool(mw)
	result, err := deleteTool.InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Contains(t, result, "active teammates")
	assert.Contains(t, result, `"success":false`)
}

func TestTeamDeleteTool_InvokableRun_Success(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)
	assert.Equal(t, "myteam", mw.getTeamName())

	deleteTool := newTeamDeleteTool(mw)
	result, err := deleteTool.InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Contains(t, result, "success")
	assert.Equal(t, "", mw.getTeamName())
}
