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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
)

type deleteErrBackend struct {
	*inMemoryBackend
	err error
}

func (b *deleteErrBackend) Delete(_ context.Context, _ *DeleteRequest) error {
	return b.err
}

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

	result, err := tool.InvokableRun(context.Background(), "")
	assert.NoError(t, err)
	assert.Contains(t, result, `"success":true`)
	assert.Contains(t, result, "No team name found, nothing to clean up")
}

func TestTeamDeleteTool_InvokableRun_ActiveTeammates(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	// Register a running teammate in the registry (simulates a live goroutine).
	mw.lifecycle.registry.register("worker", &teammateHandle{})

	deleteTool := newTeamDeleteTool(mw)
	result, err := deleteTool.InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Contains(t, result, "active teammates")
	assert.Contains(t, result, `"success":false`)

	// Clean up: remove the teammate so the registry is empty.
	mw.lifecycle.registry.remove("worker")
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

func TestTeamDeleteTool_InvokableRun_NoRunningGoroutinesAllowed(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	// Add a member in config but do NOT register it in the registry.
	// This simulates a teammate that has already been shut down (goroutine exited)
	// but its config entry was not yet cleaned up.
	cm := conf
	err = cm.AddMember(ctx, mw.getTeamName(), teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	// TeamDelete should succeed because no goroutine is running.
	deleteTool := newTeamDeleteTool(mw)
	result, err := deleteTool.InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Contains(t, result, `"success":true`)
	assert.Equal(t, "", mw.getTeamName())
}

func TestTeamDeleteTool_InvokableRun_DeleteFailureReturnsError(t *testing.T) {
	backend := &deleteErrBackend{
		inMemoryBackend: newInMemoryBackend(),
		err:             errors.New("delete failed"),
	}
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)

	ctx := context.Background()
	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	deleteTool := newTeamDeleteTool(mw)
	_, err = deleteTool.InvokableRun(ctx, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "delete failed")
	// Team name should NOT be cleared when deletion fails.
	assert.Equal(t, "myteam", mw.getTeamName())
}
