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

	"github.com/cloudwego/eino/adk"
)

func newTestTeamMiddleware() (*teamMiddleware, *Config) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)
	return mw, conf
}

func TestTeamCreateTool_Info(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	info, err := tool.Info(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, teamCreateToolName, info.Name)
}

func TestTeamCreateTool_InvokableRun_EmptyTeamName(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	_, err := tool.InvokableRun(context.Background(), `{"team_name":""}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "team_name is required")
}

func TestTeamCreateTool_InvokableRun_Success(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	result, err := tool.InvokableRun(context.Background(), `{"team_name":"myteam"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "team_name")
	assert.Contains(t, result, "team_file_path")
	assert.Contains(t, result, "lead_agent_id")
	assert.Equal(t, "myteam", mw.getTeamName())
}

func TestTeamCreateTool_InvokableRun_TeamAlreadyActive(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	_, err := tool.InvokableRun(context.Background(), `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(context.Background(), `{"team_name":"another"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already active")
}

func TestTeamCreateTool_InvokableRun_InvalidJSON(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	_, err := tool.InvokableRun(context.Background(), `not json`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse TeamCreate args")
}

func TestNewTeamCreateTool_NonNil(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)
	assert.NotNil(t, tool)
}

func TestMakeShutdownResponseHandler(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
	assert.NoError(t, err)

	_, cancel := context.WithCancel(context.Background())
	mw.lifecycle.registry.register("worker", &teammateHandle{Cancel: cancel})

	handler := createTool.makeShutdownResponseHandler(teamName)
	msg, err := handler(ctx, "worker")
	assert.NoError(t, err)
	assert.Contains(t, msg, "worker")
	assert.Contains(t, msg, "shut down")
}
