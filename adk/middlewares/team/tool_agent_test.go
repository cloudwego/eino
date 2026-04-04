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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

func TestNewAgentTool_NonNil(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newAgentTool(mw)
	assert.NotNil(t, tool)
}

func TestAgentTool_Info(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newAgentTool(mw)

	info, err := tool.Info(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "Agent", info.Name)
}

func TestAgentTool_InvokableRun_EmptyPrompt(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newAgentTool(mw)

	_, err := tool.InvokableRun(context.Background(), `{"prompt":"","description":"test task"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "prompt and description are required")
}

func TestAgentTool_InvokableRun_EmptyDescription(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newAgentTool(mw)

	_, err := tool.InvokableRun(context.Background(), `{"prompt":"do something","description":""}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "prompt and description are required")
}

func TestAgentTool_InvokableRun_InvalidJSON(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newAgentTool(mw)

	_, err := tool.InvokableRun(context.Background(), `not json`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse Agent args")
}

func TestAgentTool_RunBackground_NoActiveTeam(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newAgentTool(mw)

	_, err := tool.InvokableRun(context.Background(), `{"prompt":"do something","description":"test task","run_in_background":true}`)
	assert.Error(t, err)
	assert.ErrorIs(t, err, errTeamNotFound)
	assert.Contains(t, err.Error(), "active team")
}

func TestSendInitialPrompt_StoresRawPromptForSingleEnvelopeFormatting(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newAgentTool(mw)

	teamName := "myteam"
	_, err := mw.lifecycle.teamCfg.CreateTeam(context.Background(), teamName, "", LeaderAgentName, "")
	assert.NoError(t, err)

	args := agentToolArgs{
		Name:        "worker",
		Prompt:      "do something",
		Description: "short desc",
	}

	err = tool.sendInitialPrompt(context.Background(), teamName, args)
	assert.NoError(t, err)

	mb := mw.lifecycle.mailbox(teamName, args.Name)
	msgs, err := mb.ReadUnread(context.Background())
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, LeaderAgentName, msgs[0].From)
	assert.Equal(t, args.Prompt, msgs[0].Text)
	assert.Equal(t, args.Description, msgs[0].Summary)

	rendered := inboxMessagesToStrings(msgs)
	assert.Len(t, rendered, 1)
	assert.Equal(t, 1, strings.Count(rendered[0], "<teammate-message"))
	assert.Contains(t, rendered[0], `summary="short desc"`)
	assert.Contains(t, rendered[0], "do something")
	assert.NotContains(t, rendered[0], "&lt;/teammate-message&gt;")
}

func TestAgentTool_RunBackground_FullFlow(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}

	agentConf := &adk.ChatModelAgentConfig{
		Name:        "leader",
		Description: "test leader",
		Model:       &mockBaseChatModel{},
	}

	runnerConf := &RunnerConfig{
		AgentConfig: agentConf,
		TeamConfig:  conf,
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)

	createTool := newTeamCreateTool(runner.leaderMW)
	_, err = createTool.InvokableRun(context.Background(), `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	agentT := newAgentTool(runner.leaderMW)
	result, err := agentT.InvokableRun(context.Background(), `{"name":"worker","prompt":"do something useful","description":"test task"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Spawned successfully")
	assert.Contains(t, result, "worker")
	assert.Contains(t, result, "myteam")

	has, _ := conf.HasMember(context.Background(), "myteam", "worker")
	assert.True(t, has)

	runner.leaderMW.ShutdownAllTeammates(context.Background(), "myteam")
	time.Sleep(200 * time.Millisecond)
}

func TestAgentTool_RunForeground(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	agentConf := &adk.ChatModelAgentConfig{
		Name:        "leader",
		Description: "test leader",
		Model:       &mockBaseChatModel{},
	}

	runnerConf := &RunnerConfig{
		AgentConfig: agentConf,
		TeamConfig:  conf,
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)

	agentT := newAgentTool(mw)

	result, err := agentT.InvokableRun(context.Background(), `{"prompt":"say hello","description":"greeting"}`)
	assert.NoError(t, err)
	assert.NotEmpty(t, result)
}

func TestAgentTool_RunBackground_CleanupOnFailure(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}

	agentConf := &adk.ChatModelAgentConfig{
		Name:        "leader",
		Description: "test leader",
		Model:       nil,
	}

	runnerConf := &RunnerConfig{
		AgentConfig: agentConf,
		TeamConfig:  conf,
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
	}

	conf.ensureInit()
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)

	cm := conf
	_, _ = cm.CreateTeam(context.Background(), "myteam", "", LeaderAgentName, "")
	mw.setTeamName("myteam")

	ptMW, _ := plantask.New(context.Background(), &plantask.Config{
		Backend: backend,
		BaseDir: conf.BaseDir,
	})
	if p, ok := ptMW.(plantask.Middleware); ok {
		mw.lifecycle.SetPlantaskMW(p)
	}

	agentT := newAgentTool(mw)
	_, err := agentT.InvokableRun(context.Background(), `{"name":"worker","prompt":"do something","description":"test"}`)
	assert.Error(t, err)

	time.Sleep(100 * time.Millisecond)
	has, _ := cm.HasMember(context.Background(), "myteam", "worker")
	assert.False(t, has)
}
