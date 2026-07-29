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

package subagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type mockAgent struct {
	name string
	desc string
	run  func(context.Context, *adk.AgentInput) string
}

func (m *mockAgent) Name(context.Context) string        { return m.name }
func (m *mockAgent) Description(context.Context) string { return m.desc }
func (m *mockAgent) Run(ctx context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	result := m.desc
	if m.run != nil {
		result = m.run(ctx, input)
	}
	gen.Send(adk.EventFromMessage(schema.AssistantMessage(result, nil), nil, schema.Assistant, m.name))
	gen.Close()
	return iter
}
func (m *mockAgent) Resume(ctx context.Context, _ *adk.ResumeInfo, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	return m.Run(ctx, &adk.AgentInput{}, opts...)
}

func durableBackground(mgr *backgroundtask.Manager, agents ...adk.Agent) *BackgroundConfig {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	refs := make(map[string]durablesubagent.AgentRef, len(agents))
	for _, agent := range agents {
		name := agent.Name(context.Background())
		refs[name] = durablesubagent.AgentRef{
			Namespace: "test", Name: name, Version: "v1",
			MessageType: "schema.Message", DefinitionDigest: "definition-" + name,
		}
	}
	return &BackgroundConfig{
		Manager: mgr, AgentRefs: refs,
		SessionID:    func(context.Context) (string, error) { return "parent-session", nil },
		SessionStore: store, CheckPointStore: store,
	}
}

func terminalTask(t *testing.T, mgr *backgroundtask.Manager) *backgroundtask.Task {
	t.Helper()
	outbox := mgr.Store().(backgroundtask.NotificationOutbox)
	result, err := outbox.Receive(context.Background(), &backgroundtask.ReceiveNotificationsRequest{
		ConsumerID: "test", Limit: 100, VisibilityTime: time.Millisecond,
	})
	require.NoError(t, err)
	for i := len(result.Deliveries) - 1; i >= 0; i-- {
		record := result.Deliveries[i].Record
		task, getErr := mgr.GetTask(context.Background(), record.TaskID)
		require.NoError(t, getErr)
		if task.Status == backgroundtask.StateCompleted ||
			task.Status == backgroundtask.StateFailed ||
			task.Status == backgroundtask.StateCanceled {
			return task
		}
	}
	return nil
}

func TestConfigValidation(t *testing.T) {
	_, err := New(context.Background(), &Config{})
	assert.Error(t, err)

	_, err = New(context.Background(), &Config{SubAgents: []adk.Agent{
		&mockAgent{name: "same"}, &mockAgent{name: "same"},
	}})
	assert.Error(t, err)

	agent := &mockAgent{name: "worker"}
	_, err = New(context.Background(), &Config{
		SubAgents:  []adk.Agent{agent},
		Background: &BackgroundConfig{Manager: backgroundtask.New(context.Background(), nil)},
	})
	assert.Error(t, err)
}

func TestBeforeAgentInjectsOneTool(t *testing.T) {
	agent := &mockAgent{name: "worker", desc: "does work"}
	mw, err := New(context.Background(), &Config{SubAgents: []adk.Agent{agent}})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
	})
	require.NoError(t, err)
	require.Len(t, runCtx.Tools, 1)
	assert.Contains(t, runCtx.Instruction, "base")
	info, err := runCtx.Tools[0].Info(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, info.Desc, "does work")

	state := &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: []*schema.Message{schema.UserMessage("hi")},
	}
	_, state, err = mw.BeforeModelRewriteState(context.Background(), state, nil)
	require.NoError(t, err)
	require.Len(t, state.Messages, 2)
	assert.Contains(t, state.Messages[1].Content, "worker")
	assert.Contains(t, state.Messages[1].Content, "does work")
}

func TestAgentToolForegroundRouting(t *testing.T) {
	first := &mockAgent{name: "first", desc: "first result"}
	second := &mockAgent{name: "second", desc: "second result"}
	mw, err := New(context.Background(), &Config{SubAgents: []adk.Agent{first, second}})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	agentTool := runCtx.Tools[0].(tool.InvokableTool)

	result, err := agentTool.InvokableRun(context.Background(),
		`{"subagent_type":"second","prompt":"work","description":"test"}`)
	require.NoError(t, err)
	assert.Equal(t, "second result", result)
}

func TestDurableAgentToolForeground(t *testing.T) {
	ctx := context.Background()
	mgr := backgroundtask.New(ctx, nil)
	agent := &mockAgent{name: "worker", desc: "durable result"}
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Background: durableBackground(mgr, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)

	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(ctx,
		`{"subagent_type":"worker","prompt":"work","description":"test"}`)
	require.NoError(t, err)
	assert.Contains(t, result, "durable result")
	task := terminalTask(t, mgr)
	require.NotNil(t, task)
	assert.Equal(t, backgroundtask.StateCompleted, task.Status)
	assert.Contains(t, string(task.ResultData), "durable result")
}

func TestDurableAgentToolBackgroundSurvivesCaller(t *testing.T) {
	mgr := backgroundtask.New(context.Background(), nil)
	agent := &mockAgent{name: "slow", run: func(context.Context, *adk.AgentInput) string {
		time.Sleep(30 * time.Millisecond)
		return "done"
	}}
	mw, err := New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent}, Background: durableBackground(mgr, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(context.Background(),
		`{"subagent_type":"slow","prompt":"work","description":"test","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")
	assert.True(t, strings.Contains(result, "ID:"))
	require.Eventually(t, func() bool {
		task := terminalTask(t, mgr)
		return task != nil && task.Status == backgroundtask.StateCompleted
	}, time.Second, 10*time.Millisecond)
}

func TestDurableAgentRegistrationRejectsDuplicateExactIdentity(t *testing.T) {
	ctx := context.Background()
	mgr := backgroundtask.New(ctx, nil)
	first := &mockAgent{name: "worker", desc: "first"}
	second := &mockAgent{name: "worker", desc: "second"}
	_, err := New(ctx, &Config{
		SubAgents: []adk.Agent{first}, Background: durableBackground(mgr, first),
	})
	require.NoError(t, err)
	_, err = New(ctx, &Config{
		SubAgents: []adk.Agent{second}, Background: durableBackground(mgr, second),
	})
	assert.ErrorIs(t, err, backgroundtask.ErrAlreadyExists)
}

func TestSubagentReminderInsertedOnce(t *testing.T) {
	ctx := context.Background()
	middleware := &typedSubagentMiddleware[*schema.Message]{
		reminder: buildAgentTypesSectionFromEntries([]agentTypeEntry{{
			Name: "worker", Description: "does work",
		}}),
	}
	state := &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: []*schema.Message{schema.UserMessage("hi")},
	}

	_, state, err := middleware.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, state.Messages, 2)
	assert.Equal(t, schema.User, state.Messages[0].Role)
	assert.Equal(t, schema.System, state.Messages[1].Role)
	assert.True(t, state.Messages[1].Extra[agentTypesReminderExtraKey].(bool))
	assert.Contains(t, state.Messages[1].Content, "worker")

	_, state, err = middleware.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	assert.Len(t, state.Messages, 2)
}

func TestSubagentReminderPreservesOtherMessages(t *testing.T) {
	ctx := context.Background()
	middleware := &typedSubagentMiddleware[*schema.Message]{
		reminder: buildAgentTypesSectionFromEntries([]agentTypeEntry{{
			Name: "worker", Description: "does work",
		}}),
	}
	const otherKey = "__eino_other_middleware_section__"
	otherReminder := schema.SystemMessage("other middleware section")
	otherReminder.Extra = map[string]any{otherKey: true}
	state := &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: []*schema.Message{
			schema.SystemMessage("base instruction"),
			otherReminder,
			schema.UserMessage("hi"),
		},
	}

	_, state, err := middleware.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, state.Messages, 4)
	assert.Equal(t, "base instruction", state.Messages[0].Content)
	assert.True(t, state.Messages[1].Extra[otherKey].(bool))
	assert.Equal(t, schema.User, state.Messages[2].Role)
	assert.Equal(t, schema.System, state.Messages[3].Role)
	assert.True(t, state.Messages[3].Extra[agentTypesReminderExtraKey].(bool))
}

func TestSubagentReminderKeepsToolCallPairContiguous(t *testing.T) {
	ctx := context.Background()
	middleware := &typedSubagentMiddleware[*schema.Message]{
		reminder: buildAgentTypesSectionFromEntries([]agentTypeEntry{{
			Name: "worker", Description: "does work",
		}}),
	}
	assistantToolCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call_1",
		Function: schema.FunctionCall{
			Name: "worker", Arguments: "{}",
		},
	}})
	state := &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: []*schema.Message{
			schema.UserMessage("hi"),
			assistantToolCall,
			schema.ToolMessage("result", "call_1"),
		},
	}

	_, state, err := middleware.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, state.Messages, 4)
	assert.Equal(t, schema.User, state.Messages[0].Role)
	assert.Equal(t, schema.System, state.Messages[1].Role)
	assert.Len(t, state.Messages[2].ToolCalls, 1)
	assert.Equal(t, schema.Tool, state.Messages[3].Role)
}
