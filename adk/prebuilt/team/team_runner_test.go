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
	"github.com/cloudwego/eino/adk/middlewares/plantask"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type mockBaseChatModel struct{}

func (m *mockBaseChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
}

func (m *mockBaseChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg := &schema.Message{Role: schema.Assistant, Content: "ok"}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

// blockingChatModel keeps a teammate's turn alive until the context is cancelled
// (e.g. via ShutdownAllTeammates). A teammate backed by a model that returns
// immediately can finish its turn and run cleanupExitedTeammate (which removes
// the member from config) before the test observes the freshly-registered
// member, making membership assertions racy under load. Blocking until shutdown
// makes those assertions deterministic.
type blockingChatModel struct{}

func (m *blockingChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingChatModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func noopOnAgentEvents(context.Context, *adk.TurnContext[TurnInput, adk.Message], *adk.AsyncIterator[*adk.AgentEvent]) error {
	return nil
}

// TestNewRunner_NilConfig verifies that passing a nil *RunnerConfig returns an
// error instead of panicking on a nil-pointer dereference. NewRunner is an
// exported constructor, so a nil config must be reported as a validation error.
func TestNewRunner_NilConfig(t *testing.T) {
	_, err := NewRunner(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "RunnerConfig is required")
}

func TestNewRunner_NilAgentConfig(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig: nil,
		TeamConfig:  &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp"},
		GenInput: func(context.Context, *adk.TurnLoop[TurnInput, adk.Message], []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return nil, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AgentConfig is required")
}

func TestNewRunner_NilTeamConfig(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{},
		TeamConfig:  nil,
		GenInput: func(context.Context, *adk.TurnLoop[TurnInput, adk.Message], []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return nil, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TeamConfig is required")
}

func TestNewRunner_NilBackend(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{},
		TeamConfig:  &Config{BaseDir: "/tmp"},
		GenInput: func(context.Context, *adk.TurnLoop[TurnInput, adk.Message], []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return nil, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TeamConfig.Backend is required")
}

func TestNewRunner_EmptyBaseDir(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{},
		TeamConfig:  &Config{Backend: newInMemoryBackend(), BaseDir: " \t"},
		GenInput: func(context.Context, *adk.TurnLoop[TurnInput, adk.Message], []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return nil, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TeamConfig.BaseDir is required")
}

func TestNewRunner_NilGenInput(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig:   &adk.ChatModelAgentConfig{},
		TeamConfig:    &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp"},
		GenInput:      nil,
		OnAgentEvents: noopOnAgentEvents,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GenInput is required")
}

func TestNewRunner_NilOnAgentEvents(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{},
		TeamConfig:  &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp"},
		GenInput: func(context.Context, *adk.TurnLoop[TurnInput, adk.Message], []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return nil, nil
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "OnAgentEvents is required")
}

func TestStripPlantaskMiddleware_RemovesPlantask(t *testing.T) {
	ctx := context.Background()
	ptMW, err := plantask.New(ctx, &plantask.Config{
		Backend: newInMemoryBackend(),
		BaseDir: "/tmp/tasks",
	})
	assert.NoError(t, err)

	handlers := []adk.ChatModelAgentMiddleware{
		&adk.BaseChatModelAgentMiddleware{},
		ptMW,
		&adk.BaseChatModelAgentMiddleware{},
	}
	result := stripPlantaskMiddleware(handlers)
	assert.Len(t, result, 2)
	for _, h := range result {
		_, ok := h.(plantask.Middleware)
		assert.False(t, ok)
	}
}

func TestStripPlantaskMiddleware_EmptyHandlers(t *testing.T) {
	result := stripPlantaskMiddleware(nil)
	assert.Empty(t, result)
}

func TestStripPlantaskMiddleware_NoPlantask(t *testing.T) {
	handlers := []adk.ChatModelAgentMiddleware{
		&adk.BaseChatModelAgentMiddleware{},
		&adk.BaseChatModelAgentMiddleware{},
	}
	result := stripPlantaskMiddleware(handlers)
	assert.Len(t, result, 2)
}

func TestNewRunner_FullSuccess(t *testing.T) {
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
	assert.NotNil(t, runner)
	assert.NotNil(t, runner.loop)
	assert.NotNil(t, runner.router)
	assert.NotNil(t, runner.leaderMW)
}

// TestNewRunner_AutoCreatesNamedTeam verifies NewRunner creates the team named in
// TeamConfig.Name up front: config.json exists with the leader as a member, and
// the middleware reports it as the active team.
func TestNewRunner_AutoCreatesNamedTeam(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test", Name: "myteam"}

	runnerConf := &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{Name: "leader", Description: "test", Model: &mockBaseChatModel{}},
		TeamConfig:  conf,
		GenInput: func(_ context.Context, _ *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)
	assert.Equal(t, "myteam", runner.leaderMW.getTeamName())

	has, err := newConfigStore(conf).HasMember(context.Background(), "myteam", LeaderAgentName)
	assert.NoError(t, err)
	assert.True(t, has, "leader must be a member of the auto-created team")
}

// TestNewRunner_GeneratesTeamNameWhenUnset verifies that leaving TeamConfig.Name
// empty makes NewRunner generate a non-empty team name and create that team.
func TestNewRunner_GeneratesTeamNameWhenUnset(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}

	runnerConf := &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{Name: "leader", Description: "test", Model: &mockBaseChatModel{}},
		TeamConfig:  conf,
		GenInput: func(_ context.Context, _ *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)

	name := runner.leaderMW.getTeamName()
	assert.NotEmpty(t, name)
	assert.NoError(t, validateTeamName(name))

	has, err := newConfigStore(conf).HasMember(context.Background(), name, LeaderAgentName)
	assert.NoError(t, err)
	assert.True(t, has)
}

// TestRunner_WaitDeletesTeamByDefault verifies the team's on-disk data is removed
// after the loop exits when RetainDataOnExit is left false.
func TestRunner_WaitDeletesTeamByDefault(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test", Name: "myteam"}

	runnerConf := &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{Name: "leader", Description: "test", Model: &mockBaseChatModel{}},
		TeamConfig:  conf,
		GenInput: func(_ context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			loop.Stop()
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)

	// CreateTeam does not Mkdir the team dir itself (only its inbox/task subdirs),
	// so register it explicitly to mirror a real filesystem where config.json's
	// parent dir exists and is removable.
	assert.NoError(t, backend.Mkdir(context.Background(), teamDirPath(conf.BaseDir, "myteam")))

	cfgPath := newConfigStore(conf).configFilePath("myteam")
	exists, err := backend.Exists(context.Background(), cfgPath)
	assert.NoError(t, err)
	assert.True(t, exists, "config.json should exist after NewRunner")

	// Push an item so GenInput runs and stops the loop; without an item the loop
	// idles and Wait would block forever.
	runner.Push(TurnInput{Messages: []string{"hello"}})
	runner.Run(context.Background())
	runner.Wait()

	exists, err = backend.Exists(context.Background(), cfgPath)
	assert.NoError(t, err)
	assert.False(t, exists, "team data must be deleted after Wait when RetainDataOnExit is false")
}

// TestRunner_WaitRetainsTeamWhenConfigured verifies RetainDataOnExit keeps the
// team's on-disk data after the loop exits.
func TestRunner_WaitRetainsTeamWhenConfigured(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test", Name: "myteam", RetainDataOnExit: true}

	runnerConf := &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{Name: "leader", Description: "test", Model: &mockBaseChatModel{}},
		TeamConfig:  conf,
		GenInput: func(_ context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			loop.Stop()
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)

	runner.Push(TurnInput{Messages: []string{"hello"}})
	runner.Run(context.Background())
	runner.Wait()

	cfgPath := newConfigStore(conf).configFilePath("myteam")
	exists, err := backend.Exists(context.Background(), cfgPath)
	assert.NoError(t, err)
	assert.True(t, exists, "team data must be retained after Wait when RetainDataOnExit is true")
}

// TestNewRunner_InvalidTeamName verifies an invalid TeamConfig.Name is rejected
// before any team directory is created.
func TestNewRunner_InvalidTeamName(t *testing.T) {
	conf := &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp/test", Name: "../evil"}

	runnerConf := &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{Name: "leader", Description: "test", Model: &mockBaseChatModel{}},
		TeamConfig:  conf,
		GenInput: func(_ context.Context, _ *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	_, err := NewRunner(context.Background(), runnerConf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "team name")
}

func TestRunner_PushRunWaitStop(t *testing.T) {
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
			go func() {
				time.Sleep(10 * time.Millisecond)
				loop.Stop()
			}()
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)

	accepted, _ := runner.Push(TurnInput{Messages: []string{"hello"}})
	assert.True(t, accepted)

	runner.Run(context.Background())
	exitState := runner.Wait()
	assert.NotNil(t, exitState)
}

func TestRunner_WaitContext(t *testing.T) {
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
			go func() {
				time.Sleep(10 * time.Millisecond)
				loop.Stop()
			}()
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)

	accepted, _ := runner.Push(TurnInput{Messages: []string{"hello"}})
	assert.True(t, accepted)

	runner.Run(context.Background())

	// An already-cancelled context must not prevent WaitContext from returning
	// the exit state: the TurnLoop is always awaited and teammate teardown
	// (none here) simply returns immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exitState := runner.WaitContext(ctx)
	assert.NotNil(t, exitState)
}

func TestRunner_Stop(t *testing.T) {
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

	runner.Push(TurnInput{Messages: []string{"hello"}})
	runner.Run(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		runner.Stop()
	}()

	exitState := runner.Wait()
	assert.NotNil(t, exitState)
}

func TestBuildTeamAgent(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	agentConf := &adk.ChatModelAgentConfig{
		Name:        "test",
		Description: "test agent",
		Model:       &mockBaseChatModel{},
	}

	runnerConf := &RunnerConfig{
		AgentConfig: agentConf,
		TeamConfig:  conf,
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)

	agent, ptMW, err := buildTeamAgent(context.Background(), runnerConf, mw, "extra instruction", nil)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
	assert.NotNil(t, ptMW)
}

func TestNewTeamPlantaskMiddleware(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
		TeamConfig:  conf,
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)

	ptMW, err := newTeamPlantaskMiddleware(context.Background(), conf, mw, nil)
	assert.NoError(t, err)
	assert.NotNil(t, ptMW)
}

func TestNewTeammateRunner(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	agentConf := &adk.ChatModelAgentConfig{
		Name:        "worker",
		Description: "test worker",
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

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})

	agent, err := adk.NewChatModelAgent(context.Background(), agentConf)
	assert.NoError(t, err)

	runner, err := newTeammateRunner(runnerConf, router, pumpMgr, agent, "worker", "myteam")
	assert.NoError(t, err)
	assert.NotNil(t, runner)
	assert.NotNil(t, runner.loop)
}

func TestNewRunner_OnReminderCallback(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}

	runnerConf := &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{
			Name:        "leader",
			Description: "test leader",
			Model:       &mockBaseChatModel{},
		},
		TeamConfig: conf,
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)
	assert.NotNil(t, runner)

	// onReminder is now stored per-runner on the lifecycle manager, not on the shared Config.
	assert.NotNil(t, runner.leaderMW.lifecycle.onReminder)
}

func TestResolveReminderInterval(t *testing.T) {
	// Zero (unset) must fall back to the default rather than disabling reminders.
	assert.Equal(t, plantask.DefaultReminderInterval, resolveReminderInterval(0))
	// A positive value is honored as-is.
	assert.Equal(t, 5, resolveReminderInterval(5))
	// A negative value is preserved so reminders can be explicitly disabled.
	assert.Equal(t, -1, resolveReminderInterval(-1))
}
