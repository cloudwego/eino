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
	"github.com/cloudwego/eino/schema"
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

// TestAgentTool_RunBackground_TeamNameDoesNotBypassNoActiveTeam verifies that
// passing an explicit team_name does NOT let the Agent tool spawn a teammate
// when the leader has no active team. A teammate must attach to the team the
// leader is actually running; honoring an arbitrary team_name would start a
// teammate against a team with no leader pump reading its messages.
func TestAgentTool_RunBackground_TeamNameDoesNotBypassNoActiveTeam(t *testing.T) {
	mw, conf := newTestTeamMiddleware()

	// A previously-existing team on disk that the leader never activated.
	_, err := newConfigStore(conf).CreateTeam(context.Background(), "stale-team", "", LeaderAgentName, "")
	assert.NoError(t, err)

	tool := newAgentTool(mw)

	_, err = tool.InvokableRun(context.Background(),
		`{"prompt":"do something","description":"test task","run_in_background":true,"team_name":"stale-team"}`)
	assert.Error(t, err)
	assert.ErrorIs(t, err, errTeamNotFound)
	assert.Contains(t, err.Error(), "active team")

	// The stale team must not have gained a member from this rejected spawn.
	exists, err := newConfigStore(conf).HasMember(context.Background(), "stale-team", defaultTeammateName)
	assert.NoError(t, err)
	assert.False(t, exists, "rejected spawn must not register a member in the stale team")
}

func TestSendInitialPrompt_StoresRawPromptForSingleEnvelopeFormatting(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool := newAgentTool(mw)

	teamName := "myteam"
	_, err := newConfigStore(mw.lifecycle.teamCfg).CreateTeam(context.Background(), teamName, "", LeaderAgentName, "")
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
		// Use a blocking model so the spawned teammate's turn does not complete
		// (and trigger cleanupExitedTeammate → RemoveMember) before the membership
		// assertion below runs. ShutdownAllTeammates cancels it at the end.
		Model: &blockingChatModel{},
	}

	runnerConf := &RunnerConfig{
		AgentConfig: agentConf,
		TeamConfig:  conf,
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
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

	has, _ := newConfigStore(conf).HasMember(context.Background(), "myteam", "worker")
	assert.True(t, has)

	runner.leaderMW.ShutdownAllTeammates(context.Background())
}

// TestAgentTool_TeammateSurvivesToolCtxCancel verifies that a background
// teammate is bound to the team runtime root context (captured by Runner.Run),
// not to the per-turn tool call context. Cancelling the ctx that was passed to
// the Agent tool must NOT cancel the teammate: a host that supplies a per-turn
// RunCtx with its own deadline would otherwise kill background teammates as soon
// as the spawning turn ends, breaking the cross-turn survival contract.
func TestAgentTool_TeammateSurvivesToolCtxCancel(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}

	agentConf := &adk.ChatModelAgentConfig{
		Name:        "leader",
		Description: "test leader",
		// Block so the teammate's turn does not complete on its own; its liveness
		// is then governed purely by context cancellation.
		Model: &blockingChatModel{},
	}

	runnerConf := &RunnerConfig{
		AgentConfig: agentConf,
		TeamConfig:  conf,
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			// Build a real Input so the teammate's turn actually runs (and blocks in
			// blockingChatModel) instead of failing with "agent input is nil" and
			// self-cleaning — which would defeat the survival assertion below.
			var msgs []adk.Message
			for _, it := range items {
				for _, m := range it.Messages {
					msgs = append(msgs, schema.UserMessage(m))
				}
			}
			return &adk.GenInputResult[TurnInput, adk.Message]{
				Consumed: items,
				Input:    &adk.AgentInput{Messages: msgs},
			}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	// Start the runner with a long-lived root context so teammates derive from it.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	runner, err := NewRunner(rootCtx, runnerConf)
	assert.NoError(t, err)
	runner.Run(rootCtx)

	createTool := newTeamCreateTool(runner.leaderMW)
	_, err = createTool.InvokableRun(context.Background(), `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	// Spawn the teammate using a per-turn context that we cancel right after.
	turnCtx, turnCancel := context.WithCancel(context.Background())
	agentT := newAgentTool(runner.leaderMW)
	_, err = agentT.InvokableRun(turnCtx, `{"name":"worker","prompt":"do something","description":"test task"}`)
	assert.NoError(t, err)

	// End the spawning turn. With the fix the teammate is bound to rootCtx, so it
	// must stay alive (and registered) despite this cancellation.
	turnCancel()

	// The teammate must remain registered (alive) for a sustained window after the
	// spawning turn ctx is cancelled. The callback returns true (the "bad"
	// condition for assert.Never) only when "worker" is missing, so the assertion
	// fails the moment the teammate is erroneously torn down.
	assert.Never(t, func() bool {
		for _, n := range runner.leaderMW.lifecycle.activeTeammateNames() {
			if n == "worker" {
				return false
			}
		}
		return true
	}, 300*time.Millisecond, 30*time.Millisecond, "teammate must survive cancellation of the spawning turn ctx")

	runner.Stop()
	runner.leaderMW.ShutdownAllTeammates(context.Background())
}

func TestAgentTool_RunBackground_InvalidMemberName(t *testing.T) {
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
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)

	createTool := newTeamCreateTool(runner.leaderMW)
	_, err = createTool.InvokableRun(context.Background(), `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	agentT := newAgentTool(runner.leaderMW)

	// Path traversal in the member name must be rejected before any member is
	// registered.
	_, err = agentT.InvokableRun(context.Background(), `{"name":"../evil","prompt":"do something","description":"test"}`)
	assert.Error(t, err)
	has, _ := newConfigStore(conf).HasMember(context.Background(), "myteam", "../evil")
	assert.False(t, has)

	// The reserved leader name must be rejected for a teammate.
	_, err = agentT.InvokableRun(context.Background(), `{"name":"team-lead","prompt":"do something","description":"test"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reserved for the team leader")
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

// TestAgentTool_NamedAgent_NoActiveTeam_RunsForeground verifies that a named
// Agent call with run_in_background unset runs as a one-shot foreground sub-agent
// when no team is active, rather than erroring or hanging. The team-active branch
// of the dispatch decision is resolved under teamOpLock; when the team is empty
// the lock is released and the call falls through to the foreground path.
func TestAgentTool_NamedAgent_NoActiveTeam_RunsForeground(t *testing.T) {
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
	// No TeamCreate: getTeamName() is empty.
	assert.Equal(t, "", mw.getTeamName())

	agentT := newAgentTool(mw)

	// A named agent without an active team must NOT be treated as a teammate
	// (which would require a team and return errTeamNotFound); it runs foreground.
	result, err := agentT.InvokableRun(context.Background(), `{"name":"worker","prompt":"say hello","description":"greeting"}`)
	assert.NoError(t, err)
	assert.NotEmpty(t, result)

	// No member should have been registered anywhere, since the foreground path
	// does not touch team config.
	has, _ := newConfigStore(conf).HasMember(context.Background(), "anyteam", "worker")
	assert.False(t, has)
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
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
	}

	conf.ensureInit()
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)

	cm := newConfigStore(conf)
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
