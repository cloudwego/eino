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

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

func TestConfig_EnsureInit_InitializesState(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}

	assert.Nil(t, conf.state)

	conf.ensureInit()

	assert.NotNil(t, conf.state)
	assert.NotNil(t, conf.state.locks)
	assert.NotNil(t, conf.state.cfgLock)
}

func TestConfig_EnsureInit_OnlyOnce(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}

	conf.ensureInit()
	firstState := conf.state

	conf.ensureInit()
	assert.Same(t, firstState, conf.state)
}

func TestRunnerConfig_Logger_ReturnsDefaultWhenNil(t *testing.T) {
	conf := &RunnerConfig{}

	logger := conf.logger()
	assert.NotNil(t, logger)
	_, ok := logger.(defaultLogger)
	assert.True(t, ok)
}

func TestRunnerConfig_Logger_ReturnsCustomLogger(t *testing.T) {
	custom := nopLogger{}
	conf := &RunnerConfig{Logger: custom}

	logger := conf.logger()
	assert.NotNil(t, logger)
	_, ok := logger.(nopLogger)
	assert.True(t, ok)
}

func TestConfig_RemoveLock(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	conf.state.locks.ForName("some-agent")
	conf.removeLock("some-agent")
}

func TestConfig_RemoveLock_NilState(t *testing.T) {
	conf := &Config{}
	conf.removeLock("anything")
}

func TestTeamMiddleware_GetSetTeamName(t *testing.T) {
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

	assert.Equal(t, "", mw.getTeamName())

	mw.setTeamName("my-team")
	assert.Equal(t, "my-team", mw.getTeamName())

	mw.setTeamName("other-team")
	assert.Equal(t, "other-team", mw.getTeamName())
}

func TestTeamMiddleware_BeforeAgent_NilRunCtx(t *testing.T) {
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

	ctx := context.Background()
	ctx, result, err := mw.BeforeAgent(ctx, nil)
	assert.NoError(t, err)
	assert.Nil(t, result)
	assert.NotNil(t, ctx)
}

func TestTeamMiddleware_BeforeAgent_Leader(t *testing.T) {
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

	ctx := context.Background()
	runCtx := &adk.ChatModelAgentContext{Tools: []tool.BaseTool{}}
	ctx, result, err := mw.BeforeAgent(ctx, runCtx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Len(t, result.Tools, 4)
}

func TestTeamMiddleware_BeforeAgent_Teammate(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	tmMW := newTeamTeammateMiddleware(runnerConf, "worker", "myteam")

	ctx := context.Background()
	runCtx := &adk.ChatModelAgentContext{Tools: []tool.BaseTool{}}
	ctx, result, err := tmMW.BeforeAgent(ctx, runCtx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Len(t, result.Tools, 1)
}

func TestNewTeamTeammateMiddleware_SetsTeamName(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	tmMW := newTeamTeammateMiddleware(runnerConf, "worker", "myteam")
	assert.Equal(t, "myteam", tmMW.getTeamName())
	assert.Equal(t, "worker", tmMW.agentName)
	assert.False(t, tmMW.isLeader)
}

func TestTeamMiddleware_Logger(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	assert.NotNil(t, mw.logger())
}

func TestTeamMiddleware_ShutdownAllTeammates(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	mw.setTeamName("myteam")
	_, cancel := context.WithCancel(context.Background())
	mw.lifecycle.registry.register("worker", &teammateHandle{Cancel: cancel})

	mw.ShutdownAllTeammates(ctx, "myteam")
}
