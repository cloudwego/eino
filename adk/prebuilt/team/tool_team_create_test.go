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
	"fmt"
	"strings"
	"sync"
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

// TestTeamCreateTool_InvokableRun_ConcurrentSingleWinner verifies that when
// multiple TeamCreate calls race (as can happen with parallel tool calls in a
// single assistant turn), exactly one succeeds and the rest are rejected with
// "already active". Without the teamOpLock serializing the
// check→create→setTeamName sequence, more than one call could observe an empty
// team name and each create a team.
func TestTeamCreateTool_InvokableRun_ConcurrentSingleWinner(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)

	var mu sync.Mutex
	var successCount int

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf(`{"team_name":"team-%d"}`, i)
			if _, err := tool.InvokableRun(context.Background(), args); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, 1, successCount, "exactly one concurrent TeamCreate should succeed")
	assert.NotEqual(t, "", mw.getTeamName(), "the winning team must be the active team")
}

func TestTeamCreateTool_InvokableRun_InvalidJSON(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newTeamCreateTool(mw)

	_, err := tool.InvokableRun(context.Background(), `not json`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse TeamCreate args")
}

func TestTeamCreateTool_InvokableRun_InvalidName(t *testing.T) {
	cases := []string{
		`{"team_name":"../escape"}`,
		`{"team_name":"a/b"}`,
		`{"team_name":".."}`,
		`{"team_name":"team*"}`,
		`{"team_name":".hidden"}`,
		`{"team_name":"has space"}`,
	}
	for _, args := range cases {
		mw, _ := newTestTeamMiddleware()
		tool := newTeamCreateTool(mw)
		_, err := tool.InvokableRun(context.Background(), args)
		assert.Error(t, err, "expected rejection for %s", args)
		assert.Equal(t, "", mw.getTeamName(), "team must not be created for %s", args)
	}
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

	cm := newConfigStore(conf)
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

// rollbackBackend wraps inMemoryBackend to fail inbox writes (so setupMailbox
// fails and rollback runs) and fail Delete (so the rollback cleanup itself
// errors), letting tests assert the rollback error is surfaced rather than
// silently dropped.
type rollbackBackend struct {
	*inMemoryBackend
	deleteErr error
}

func (b *rollbackBackend) Write(ctx context.Context, req *WriteRequest) error {
	if strings.Contains(req.FilePath, "inboxes") {
		return errors.New("inbox write failed")
	}
	return b.inMemoryBackend.Write(ctx, req)
}

func (b *rollbackBackend) Delete(ctx context.Context, req *DeleteRequest) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	return b.inMemoryBackend.Delete(ctx, req)
}

func TestTeamCreateTool_InvokableRun_RollbackLogsCleanupError(t *testing.T) {
	backend := &rollbackBackend{
		inMemoryBackend: newInMemoryBackend(),
		deleteErr:       errors.New("delete boom"),
	}
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	var logged string
	logger := &testLogger{onPrintf: func(format string, args ...any) {
		logged += fmt.Sprintf(format, args...)
	}}

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
		Logger:      logger,
	}
	router := newSourceRouter(LeaderAgentName, logger)
	pumpMgr := newPumpManager(router, logger)
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)
	tool := newTeamCreateTool(mw)

	// setupMailbox fails (inbox write rejected), triggering rollback; the
	// rollback DeleteTeam also fails, so the error must be logged.
	_, err := tool.InvokableRun(context.Background(), `{"team_name":"myteam"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "setup leader mailbox")

	assert.Contains(t, logged, "TeamCreate rollback")
	assert.Contains(t, logged, "delete boom")
	// Team name must be cleared on failure.
	assert.Equal(t, "", mw.getTeamName())
}
