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

func TestTeamDeleteTool_InvokableRun_ResidualConfigMemberRefused(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	// Add a member in config but do NOT register it in the registry.
	// This simulates a teammate whose goroutine has exited but whose config
	// entry was not cleaned up (e.g. a failed teardown or a process restart).
	cm := newConfigStore(conf)
	err = cm.AddMember(ctx, mw.getTeamName(), teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	// TeamDelete must refuse: deleting would silently discard the recoverable
	// member state still recorded in config.json (the persistent source of truth).
	deleteTool := newTeamDeleteTool(mw)
	result, err := deleteTool.InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Contains(t, result, `"success":false`)
	assert.Contains(t, result, "config.json")
	assert.Contains(t, result, "worker")
	// Team name must remain so the operation can be retried after verification.
	assert.Equal(t, "myteam", mw.getTeamName())
}

func TestTeamDeleteTool_InvokableRun_ResidualConfigMemberForce(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	cm := newConfigStore(conf)
	err = cm.AddMember(ctx, mw.getTeamName(), teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	// force=true overrides the residual-member guard and deletes anyway.
	deleteTool := newTeamDeleteTool(mw)
	result, err := deleteTool.InvokableRun(ctx, `{"force":true}`)
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

// TestTeamDeleteTool_AcquiresTeamOpLock verifies that TeamDelete takes the
// shared teamOpLock so it is serialized against TeamCreate and the Agent spawn
// path. While the lock is held by another lifecycle operation, TeamDelete must
// block rather than racing on active-team state.
func TestTeamDeleteTool_AcquiresTeamOpLock(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	// Hold the lock to simulate an in-flight TeamCreate / Agent spawn.
	mw.teamOpLock.Lock()

	done := make(chan struct{})
	go func() {
		deleteTool := newTeamDeleteTool(mw)
		_, _ = deleteTool.InvokableRun(ctx, "")
		close(done)
	}()

	// TeamDelete must not complete while the lock is held.
	select {
	case <-done:
		mw.teamOpLock.Unlock()
		t.Fatal("TeamDelete ran while teamOpLock was held; it does not acquire the lock")
	case <-time.After(100 * time.Millisecond):
	}

	// Release the lock; TeamDelete should now proceed and finish.
	mw.teamOpLock.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TeamDelete did not finish after teamOpLock was released")
	}
	assert.Equal(t, "", mw.getTeamName())
}

// TestAgentTool_RunTeammate_AcquiresTeamOpLock verifies that the Agent spawn
// path takes the shared teamOpLock so it is serialized against TeamDelete and
// TeamCreate. While the lock is held, runTeammate must block before reading the
// active-team state.
func TestAgentTool_RunTeammate_AcquiresTeamOpLock(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	mw.teamOpLock.Lock()

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		tool := newAgentTool(mw)
		close(started)
		// run_in_background forces the teammate path. It will ultimately fail to
		// build a real agent (no model wired in the test middleware), but it must
		// first block on teamOpLock — which is what this test asserts.
		_, _ = tool.runTeammate(ctx, agentToolArgs{
			Name:        "worker",
			Prompt:      "do work",
			Description: "desc",
		})
		close(done)
	}()

	<-started
	select {
	case <-done:
		mw.teamOpLock.Unlock()
		t.Fatal("runTeammate ran while teamOpLock was held; it does not acquire the lock")
	case <-time.After(100 * time.Millisecond):
	}

	mw.teamOpLock.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTeammate did not finish after teamOpLock was released")
	}
}
