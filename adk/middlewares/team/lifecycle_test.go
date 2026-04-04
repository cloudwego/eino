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
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

func setupLifecycleTest() (*lifecycleManager, *Config, *sourceRouter, *pumpManager) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	lm := newLifecycleManager(conf, runnerConf, true, router, pumpMgr)
	return lm, conf, router, pumpMgr
}

func TestBuildTeammateTerminationMessage_NoUnassigned(t *testing.T) {
	msg := buildTeammateTerminationMessage("worker", nil)
	assert.Equal(t, "worker has shut down.", msg)
}

func TestBuildTeammateTerminationMessage_EmptyUnassigned(t *testing.T) {
	msg := buildTeammateTerminationMessage("worker", []string{})
	assert.Equal(t, "worker has shut down.", msg)
}

func TestBuildTeammateTerminationMessage_WithUnassigned(t *testing.T) {
	msg := buildTeammateTerminationMessage("worker", []string{"1", "2"})
	assert.Contains(t, msg, "worker has shut down.")
	assert.Contains(t, msg, "2 task(s) were unassigned")
	assert.Contains(t, msg, "#1, #2")
}

func TestBuildTeammateTerminationMessage_SingleUnassigned(t *testing.T) {
	msg := buildTeammateTerminationMessage("agent-x", []string{"5"})
	assert.Contains(t, msg, "agent-x has shut down.")
	assert.Contains(t, msg, "1 task(s) were unassigned")
	assert.Contains(t, msg, "#5")
}

func TestNewLifecycleManager(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})

	lm := newLifecycleManager(conf, runnerConf, true, router, pumpMgr)

	assert.NotNil(t, lm)
	assert.NotNil(t, lm.registry)
	assert.Same(t, router, lm.router)
	assert.Same(t, pumpMgr, lm.pumpMgr)
	assert.Same(t, conf, lm.teamCfg)
	assert.Same(t, runnerConf, lm.runnerConf)
	assert.True(t, lm.isLeader)
	assert.NotNil(t, lm.logger)
}

func TestNewLifecycleManager_NotLeader(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	lm := newLifecycleManager(conf, runnerConf, false, nil, nil)

	assert.NotNil(t, lm)
	assert.False(t, lm.isLeader)
	assert.Nil(t, lm.router)
	assert.Nil(t, lm.pumpMgr)
}

func TestLifecycleManager_SetPlantaskMW(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	lm := newLifecycleManager(conf, runnerConf, true, nil, nil)

	assert.Nil(t, lm.ptMW)
	assert.Nil(t, lm.plantaskMW())

	lm.SetPlantaskMW(nil)
	assert.Nil(t, lm.ptMW)
}

func TestLifecycleManager_AgentConfig(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	agentCfg := &adk.ChatModelAgentConfig{Name: "leader", Description: "leader agent"}
	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: agentCfg,
	}

	lm := newLifecycleManager(conf, runnerConf, true, nil, nil)

	assert.Same(t, agentCfg, lm.agentConfig())
}

func TestLifecycleManager_BuildTeammateAgent_AppendsLocalizedInstruction(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig: conf,
		AgentConfig: &adk.ChatModelAgentConfig{
			Name:        "leader",
			Description: "leader agent",
			Instruction: "base instruction",
			Model:       &mockBaseChatModel{},
		},
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	lm := newLifecycleManager(conf, runnerConf, true, router, pumpMgr)

	agent, err := lm.buildTeammateAgent(context.Background(), "worker", "myteam")
	assert.NoError(t, err)
	assert.NotNil(t, agent)

	expectedExtraInstruction := fmt.Sprintf(
		"Your agent name is: %s\n\n%s",
		"worker",
		selectToolDesc(teammateInstruction, teammateInstructionChinese),
	)
	instruction := reflect.ValueOf(agent).Elem().FieldByName("instruction").String()
	assert.Equal(t, "base instruction\n"+expectedExtraInstruction, instruction)
}

func TestLifecycleManager_ConfigStore(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	lm := newLifecycleManager(conf, runnerConf, true, nil, nil)

	assert.Same(t, conf, lm.teamCfg)
}

func TestLifecycleManager_InboxPath(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/data"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	lm := newLifecycleManager(conf, runnerConf, true, nil, nil)

	expected := filepath.Join("/data", "teams", "myteam", "inboxes", "worker.json")
	assert.Equal(t, expected, lm.inboxPath("myteam", "worker"))
}

func TestLifecycleManager_CleanupLeaderMailbox_NilPumpMgr(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	lm := newLifecycleManager(conf, runnerConf, false, nil, nil)

	assert.NotPanics(t, func() {
		lm.cleanupLeaderMailbox()
	})
}

func TestLifecycleManager_StopTeammateRuntime(t *testing.T) {
	lm, conf, _, _ := setupLifecycleTest()
	ctx := context.Background()
	teamName := "myteam"

	cm := conf
	_, err := cm.CreateTeam(ctx, teamName, "", LeaderAgentName, "")
	assert.NoError(t, err)

	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	workerCtx, cancel := context.WithCancel(context.Background())
	lm.registry.register("worker", &teammateHandle{Cancel: cancel})

	firstStop := lm.stopTeammateRuntime(ctx, teamName, "worker")
	assert.True(t, firstStop)
	assert.Error(t, workerCtx.Err())

	secondStop := lm.stopTeammateRuntime(ctx, teamName, "worker")
	assert.False(t, secondStop)
}

func TestLifecycleManager_CleanupFailedTeammateSpawn(t *testing.T) {
	lm, conf, _, _ := setupLifecycleTest()
	ctx := context.Background()
	teamName := "myteam"

	cm := conf
	_, err := cm.CreateTeam(ctx, teamName, "", LeaderAgentName, "")
	assert.NoError(t, err)

	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
	assert.NoError(t, err)

	lm.cleanupFailedTeammateSpawn(ctx, teamName, "worker")

	has, _ := cm.HasMember(ctx, teamName, "worker")
	assert.False(t, has)

	exists, _ := conf.Backend.Exists(ctx, inboxPath)
	assert.False(t, exists)
}

func TestLifecycleManager_RemoveTeammate(t *testing.T) {
	lm, conf, _, _ := setupLifecycleTest()
	ctx := context.Background()
	teamName := "myteam"

	cm := conf
	_, _ = cm.CreateTeam(ctx, teamName, "", LeaderAgentName, "")
	_ = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})

	_, cancel := context.WithCancel(context.Background())
	lm.registry.register("worker", &teammateHandle{Cancel: cancel})

	unassigned, firstStop, err := lm.removeTeammate(ctx, teamName, "worker")
	assert.NoError(t, err)
	assert.Nil(t, unassigned)
	assert.True(t, firstStop)

	has, _ := cm.HasMember(ctx, teamName, "worker")
	assert.False(t, has)
}

func TestLifecycleManager_UnassignMemberTasks_NilPtMW(t *testing.T) {
	lm, _, _, _ := setupLifecycleTest()
	result, err := lm.unassignMemberTasks(context.Background(), "worker")
	assert.NoError(t, err)
	assert.Nil(t, result)
}

func TestLifecycleManager_NotifyLeaderTerminated_NotLeader(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()
	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "t", Description: "t"},
	}
	lm := newLifecycleManager(conf, runnerConf, false, nil, nil)
	assert.NotPanics(t, func() {
		lm.notifyLeaderTeammateTerminated(context.Background(), "team", "worker", nil)
	})
}

func TestLifecycleManager_NotifyLeaderTerminated_IsLeader(t *testing.T) {
	lm, _, router, _ := setupLifecycleTest()

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop(LeaderAgentName, loop)

	assert.NotPanics(t, func() {
		lm.notifyLeaderTeammateTerminated(context.Background(), "myteam", "worker", []string{"1", "2"})
	})
}

func TestLifecycleManager_StartPump_NilPumpMgr(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()
	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "t", Description: "t"},
	}
	lm := newLifecycleManager(conf, runnerConf, false, nil, nil)
	assert.NotPanics(t, func() {
		lm.startPump(context.Background(), "worker")
	})
}

func TestLifecycleManager_ShutdownAll(t *testing.T) {
	lm, _, _, _ := setupLifecycleTest()
	ctx, cancel := context.WithCancel(context.Background())
	lm.registry.register("worker", &teammateHandle{Cancel: cancel})
	lm.shutdownAll(nopLogger{})
	assert.Error(t, ctx.Err())
}

func TestLifecycleManager_CleanupExitedTeammate(t *testing.T) {
	lm, conf, router, _ := setupLifecycleTest()
	ctx := context.Background()
	teamName := "myteam"

	cm := conf
	_, _ = cm.CreateTeam(ctx, teamName, "", LeaderAgentName, "")
	_ = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})

	_, cancel := context.WithCancel(context.Background())
	lm.registry.register("worker", &teammateHandle{Cancel: cancel})

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop(LeaderAgentName, loop)

	lm.cleanupExitedTeammate(ctx, teamName, "worker")

	has, _ := cm.HasMember(ctx, teamName, "worker")
	assert.False(t, has)
}

func TestLifecycleManager_StartTeammateRunner(t *testing.T) {
	lm, conf, router, _ := setupLifecycleTest()
	ctx := context.Background()
	teamName := "myteam"

	cm := conf
	_, _ = cm.CreateTeam(ctx, teamName, "", LeaderAgentName, "")
	_ = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop(LeaderAgentName, loop)

	_, cancel := context.WithCancel(context.Background())
	handle := &teammateHandle{Cancel: cancel}

	done := make(chan struct{})
	lm.startTeammateRunner(ctx, teamName, "worker", handle, func(ctx context.Context) error {
		close(done)
		return nil
	})

	<-done
	time.Sleep(200 * time.Millisecond)

	has, _ := cm.HasMember(ctx, teamName, "worker")
	assert.False(t, has)
}

func TestLifecycleManager_ShutdownAllTeammates(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	cm := conf
	_, _ = cm.CreateTeam(ctx, "myteam", "", LeaderAgentName, "")
	mw.setTeamName("myteam")

	workerCtx, cancel := context.WithCancel(context.Background())
	mw.lifecycle.registry.register("worker", &teammateHandle{Cancel: cancel})

	mw.ShutdownAllTeammates(ctx, "myteam")
	assert.Error(t, workerCtx.Err())
}

func TestLifecycleManager_CleanupExitedTeammate_UnassignErr(t *testing.T) {
	lm, conf, router, _ := setupLifecycleTest()
	ctx := context.Background()
	teamName := "myteam"

	cm := conf
	_, _ = cm.CreateTeam(ctx, teamName, "", LeaderAgentName, "")
	_ = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})

	_, cancel := context.WithCancel(context.Background())
	lm.registry.register("worker", &teammateHandle{Cancel: cancel})

	errPtMW, err := plantask.New(ctx, &plantask.Config{
		Backend: newErrBackend(errors.New("unassign failed")),
		BaseDir: "/tmp/err",
	})
	assert.NoError(t, err)
	if p, ok := errPtMW.(plantask.Middleware); ok {
		lm.ptMW = p
	}

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop(LeaderAgentName, loop)

	assert.NotPanics(t, func() {
		lm.cleanupExitedTeammate(ctx, teamName, "worker")
	})

	has, _ := cm.HasMember(ctx, teamName, "worker")
	assert.False(t, has)
}

func TestLifecycleManager_RemoveTeammate_UnassignError(t *testing.T) {
	lm, conf, _, _ := setupLifecycleTest()
	ctx := context.Background()
	teamName := "myteam"

	cm := conf
	_, _ = cm.CreateTeam(ctx, teamName, "", LeaderAgentName, "")
	_ = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})

	_, cancel := context.WithCancel(context.Background())
	lm.registry.register("worker", &teammateHandle{Cancel: cancel})

	errPtMW, err := plantask.New(ctx, &plantask.Config{
		Backend: newErrBackend(errors.New("unassign failed")),
		BaseDir: "/tmp/err",
	})
	assert.NoError(t, err)
	if p, ok := errPtMW.(plantask.Middleware); ok {
		lm.ptMW = p
	}

	_, _, err = lm.removeTeammate(ctx, teamName, "worker")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unassign")
}

func TestLifecycleManager_SetupMailbox(t *testing.T) {
	lm, conf, _, _ := setupLifecycleTest()
	ctx := context.Background()
	teamName := "myteam"

	cm := conf
	_, _ = cm.CreateTeam(ctx, teamName, "", LeaderAgentName, "")

	err := lm.setupMailbox(ctx, teamName, "worker", &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	exists, _ := conf.Backend.Exists(ctx, inboxPath)
	assert.True(t, exists)
}

func TestLifecycleManager_CleanupFailedTeammateSpawn_RemoveMemberError(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	lm := newLifecycleManager(conf, runnerConf, true, router, pumpMgr)
	ctx := context.Background()

	assert.NotPanics(t, func() {
		lm.cleanupFailedTeammateSpawn(ctx, "nonexistent-team", "worker")
	})
}
