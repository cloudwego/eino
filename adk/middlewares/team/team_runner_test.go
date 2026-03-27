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

func TestNewRunner_NilAgentConfig(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig: nil,
		TeamConfig:  &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp"},
		GenInput: func(context.Context, *adk.TurnLoop[TurnInput], []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return nil, nil
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AgentConfig is required")
}

func TestNewRunner_NilTeamConfig(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{},
		TeamConfig:  nil,
		GenInput: func(context.Context, *adk.TurnLoop[TurnInput], []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return nil, nil
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TeamConfig is required")
}

func TestNewRunner_NilGenInput(t *testing.T) {
	ctx := context.Background()
	_, err := NewRunner(ctx, &RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{},
		TeamConfig:  &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp"},
		GenInput:    nil,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GenInput is required")
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
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)
	assert.NotNil(t, runner)
	assert.NotNil(t, runner.loop)
	assert.NotNil(t, runner.router)
	assert.NotNil(t, runner.leaderMW)
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
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			go func() {
				time.Sleep(10 * time.Millisecond)
				loop.Stop()
			}()
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)

	accepted, _ := runner.Push(TurnInput{Messages: []string{"hello"}})
	assert.True(t, accepted)

	runner.Run(context.Background())
	exitState := runner.Wait()
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
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
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

	agent, ptMW, err := buildTeamAgent(context.Background(), runnerConf, mw, "extra instruction")
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

	ptMW, err := newTeamPlantaskMiddleware(context.Background(), conf, mw)
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
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
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
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
	}

	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)
	assert.NotNil(t, runner)

	assert.NotNil(t, conf.OnReminder)
}
