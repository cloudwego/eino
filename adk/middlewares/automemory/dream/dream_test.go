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

package dream

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/automemory"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type dreamModel struct {
	mu        sync.Mutex
	prompts   []string
	toolNames [][]string
	calls     int32
}

func (m *dreamModel) BindTools(tools []*schema.ToolInfo) []string {
	names := make([]string, 0, len(tools))
	for _, ti := range tools {
		if ti != nil {
			names = append(names, ti.Name)
		}
	}
	return names
}

func (m *dreamModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	callCount := atomic.AddInt32(&m.calls, 1)
	toolList := model.GetCommonOptions(nil, opts...).Tools
	m.mu.Lock()
	m.toolNames = append(m.toolNames, m.BindTools(toolList))
	for _, msg := range input {
		if msg.Role == schema.User {
			m.prompts = append(m.prompts, messageText(msg))
		}
	}
	m.mu.Unlock()
	content := ""
	for _, msg := range input {
		if msg.Role == schema.User {
			content = messageText(msg)
		}
	}
	if callCount > 1 {
		return schema.AssistantMessage("dream complete", nil), nil
	}
	calls := []schema.ToolCall{
		{ID: "1", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"file_path":"MEMORY.md"}`}},
		{ID: "2", Function: schema.FunctionCall{Name: "write_file", Arguments: `{"file_path":"dream.md","content":"consolidated"}`}},
		{ID: "3", Function: schema.FunctionCall{Name: "write_file", Arguments: `{"file_path":"MEMORY.md","content":"- [Dream](dream.md) - consolidated"}`}},
	}
	if strings.Contains(content, "Optional session search") {
		calls = append([]schema.ToolCall{{ID: "0", Function: schema.FunctionCall{Name: "grep_session_history", Arguments: `{"query":"build failure"}`}}}, calls...)
	}
	return schema.AssistantMessage("dream", calls), nil
}

func (m *dreamModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

func (m *dreamModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.mu.Lock()
	m.toolNames = append(m.toolNames, m.BindTools(tools))
	m.mu.Unlock()
	return m, nil
}

func messageText(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	return msg.Content
}

type mainAgentModel struct {
	reply string
}

func (m *mainAgentModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(m.reply, nil), nil
}

func (m *mainAgentModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

func (m *mainAgentModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func drainIterator(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) []*adk.AgentEvent {
	t.Helper()
	var out []*adk.AgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			return out
		}
		out = append(out, ev)
		if ev != nil && ev.Err != nil {
			return out
		}
	}
}

type countingSessionStore struct {
	adk.SessionEventStore[*schema.Message]
	loadCalls int32
}

func (s *countingSessionStore) LoadEvents(ctx context.Context, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	atomic.AddInt32(&s.loadCalls, 1)
	return s.SessionEventStore.LoadEvents(ctx, req)
}

type nilStateStore struct {
	Store
}

func (s *nilStateStore) GetScheduleState(context.Context, string) (*ScheduleState, error) {
	return nil, nil
}

func TestBuildConsolidationPrompt_OmitsSessionSearchSectionWhenProviderMissing(t *testing.T) {
	prompt := buildConsolidationPrompt("/mem", []string{"a", "b"}, false)
	require.NotContains(t, prompt, "Optional session search")
	require.Contains(t, prompt, "Sessions since last consolidation (2)")
}

func TestBuildConsolidationPrompt_Chinese(t *testing.T) {
	require.NoError(t, adk.SetLanguage(adk.LanguageChinese))
	defer func() {
		require.NoError(t, adk.SetLanguage(adk.LanguageEnglish))
	}()

	prompt := buildConsolidationPrompt("/mem", []string{"a", "b"}, true)
	require.Contains(t, prompt, "## 可选的 session 搜索")
	require.Contains(t, prompt, "它只会搜索本次 dream 运行范围内包含的 session 历史")
	require.Contains(t, prompt, "自上次 consolidation 以来触达过的 sessions（2）")
}

func TestNew_DoesNotMutateConfig(t *testing.T) {
	ctx := context.Background()
	cfg := &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   automemory.NewInMemoryBackend(),
		Model:           &dreamModel{},
		SessionStore:    adksession.NewInMemoryStore[*schema.Message](nil),
		Schedule:        &ScheduleConfig{},
	}

	_, err := New(ctx, cfg)
	require.NoError(t, err)
	require.Nil(t, cfg.SessionIDFunc)
	require.Zero(t, cfg.Schedule.MinInterval)
	require.Zero(t, cfg.Schedule.MinTouchedSession)
	require.Zero(t, cfg.Schedule.ScanInterval)
	require.Zero(t, cfg.Schedule.LockTTL)
	require.Nil(t, cfg.Schedule.Store)
}

func TestMiddleware_AfterAgent_RunInlineWithSessionStore(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte("- [Existing](existing.md) - old"), 0o644))
	store := NewLocalStore()
	model := &dreamModel{}
	eventStore := &countingSessionStore{SessionEventStore: adksession.NewInMemoryStore[*schema.Message](nil)}
	_, err := eventStore.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{
		SessionID: "session-a",
		Events: []*adk.SessionEvent[*schema.Message]{{
			EventID: "e1",
			Kind:    adk.SessionEventMessage,
			Message: schema.AssistantMessage("build failure: missing dependency", nil),
		}},
	})
	require.NoError(t, err)
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: tmp,
		MemoryBackend:   automemory.NewLocalBackend(),
		Model:           model,
		SessionStore:    eventStore,
		Schedule: &ScheduleConfig{
			RunInline:         true,
			Store:             store,
			MinInterval:       time.Hour,
			MinTouchedSession: 1,
			ScanInterval:      time.Minute,
		},
	})
	require.NoError(t, err)
	impl, ok := mw.(*middleware[*schema.Message])
	require.True(t, ok)
	now := time.Now()
	impl.now = func() time.Time { return now }
	require.NoError(t, store.SetScheduleState(ctx, tmp, &ScheduleState{LastConsolidatedAt: now.Add(-2 * time.Hour), NextCheckAt: now}))
	require.NoError(t, store.RecordSessionTouch(ctx, tmp, "session-a", now.Add(-30*time.Minute)))

	_, err = impl.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{})
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(tmp, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))
	require.GreaterOrEqual(t, atomic.LoadInt32(&eventStore.loadCalls), int32(1))
	model.mu.Lock()
	defer model.mu.Unlock()
	require.NotEmpty(t, model.prompts)
	require.Contains(t, model.prompts[0], "Optional session search")
}

func TestMiddleware_AfterAgent_FirstEligibleTouchCanTriggerImmediately(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	store := NewLocalStore()
	model := &dreamModel{}
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: tmp,
		MemoryBackend:   automemory.NewLocalBackend(),
		Model:           model,
		Schedule: &ScheduleConfig{
			RunInline:         true,
			Store:             store,
			MinInterval:       time.Hour,
			MinTouchedSession: 1,
			ScanInterval:      time.Minute,
		},
	})
	require.NoError(t, err)
	impl, ok := mw.(*middleware[*schema.Message])
	require.True(t, ok)
	now := time.Now()
	impl.now = func() time.Time { return now }
	require.NoError(t, store.RecordSessionTouch(ctx, tmp, "older-session", now.Add(-2*time.Minute)))

	_, err = impl.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{})
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(tmp, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))
}

func TestMiddleware_AfterAgent_NilScheduleStateDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))

	baseStore := NewLocalStore()
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: tmp,
		MemoryBackend:   automemory.NewLocalBackend(),
		Model:           &dreamModel{},
		Schedule: &ScheduleConfig{
			RunInline:         true,
			Store:             &nilStateStore{Store: baseStore},
			MinInterval:       time.Hour,
			MinTouchedSession: 1,
			ScanInterval:      time.Minute,
		},
	})
	require.NoError(t, err)

	_, err = mw.(*middleware[*schema.Message]).AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{})
	require.NoError(t, err)
}

func TestRun_ManualDreamWithoutSchedule(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	model := &dreamModel{}

	err := Run(ctx, &Config[*schema.Message]{
		MemoryDirectory: tmp,
		MemoryBackend:   automemory.NewLocalBackend(),
		Model:           model,
	}, &RunRequest{
		SessionID: "manual-session",
	})
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(tmp, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))
	model.mu.Lock()
	defer model.mu.Unlock()
	require.NotEmpty(t, model.prompts)
	require.NotContains(t, model.prompts[0], "Sessions since last consolidation")
	require.NotContains(t, model.prompts[0], "Optional session search")
}

func TestIntegration_UserPerspective_AgentMiddlewareAutoDream(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte("- [Existing](existing.md) - old\n"), 0o644))

	store := NewLocalStore()
	dreamModel := &dreamModel{}
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: tmp,
		MemoryBackend:   automemory.NewLocalBackend(),
		Model:           dreamModel,
		Schedule: &ScheduleConfig{
			RunInline:         true,
			Store:             store,
			MinInterval:       time.Hour,
			MinTouchedSession: 1,
			ScanInterval:      time.Minute,
		},
	})
	require.NoError(t, err)
	impl, ok := mw.(*middleware[*schema.Message])
	require.True(t, ok)
	now := time.Now()
	impl.now = func() time.Time { return now }
	require.NoError(t, store.RecordSessionTouch(ctx, tmp, "older-session", now.Add(-3*time.Minute)))

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "main_agent",
		Description: "main agent for dream integration test",
		Model:       &mainAgentModel{reply: "main answer"},
		Handlers:    []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)

	events := drainIterator(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("please help")},
	}))
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.NotNil(t, last)
	require.Nil(t, last.Err)
	require.NotNil(t, last.Output)
	require.Equal(t, "main answer", last.Output.MessageOutput.Message.Content)

	raw, err := os.ReadFile(filepath.Join(tmp, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))
	index, err := os.ReadFile(filepath.Join(tmp, "MEMORY.md"))
	require.NoError(t, err)
	require.Contains(t, string(index), "dream.md")
}

func TestIntegration_UserPerspective_RunReturnsCallbackErrors(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))

	expected := fmt.Errorf("resolve session failed")
	var onErrStages []string
	err := Run(ctx, &Config[*schema.Message]{
		MemoryDirectory: tmp,
		MemoryBackend:   automemory.NewLocalBackend(),
		Model:           &dreamModel{},
		SessionIDFunc: func(context.Context, *adk.TypedChatModelAgentState[*schema.Message]) (string, error) {
			return "", expected
		},
		OnError: func(_ context.Context, stage string, err error) {
			onErrStages = append(onErrStages, stage+":"+err.Error())
		},
	}, nil)
	require.ErrorIs(t, err, expected)
	require.Equal(t, []string{stageResolveSessionID + ":" + expected.Error()}, onErrStages)
}

func TestIntegration_UserPerspective_RunFallsBackToSessionIDFuncWithoutState(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))

	model := &dreamModel{}
	var resolvedState *adk.TypedChatModelAgentState[*schema.Message]
	err := Run(ctx, &Config[*schema.Message]{
		MemoryDirectory: tmp,
		MemoryBackend:   automemory.NewLocalBackend(),
		Model:           model,
		SessionIDFunc: func(_ context.Context, state *adk.TypedChatModelAgentState[*schema.Message]) (string, error) {
			resolvedState = state
			return "fallback-session", nil
		},
	}, nil)
	require.NoError(t, err)
	require.Nil(t, resolvedState)

	raw, err := os.ReadFile(filepath.Join(tmp, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))
}
