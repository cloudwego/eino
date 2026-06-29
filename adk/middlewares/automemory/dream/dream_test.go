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
	"encoding/json"
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
	adkfs "github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/automemory"
	ainternal "github.com/cloudwego/eino/adk/middlewares/automemory/internal"
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

// failingDreamModel always returns an error from Generate, simulating a dream
// run that fails before it can consolidate anything.
type failingDreamModel struct {
	calls int32
}

func (m *failingDreamModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	atomic.AddInt32(&m.calls, 1)
	return nil, errDreamModel
}

func (m *failingDreamModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errDreamModel
}

func (m *failingDreamModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

var errDreamModel = fmt.Errorf("dream model failure")

// toolFailDreamModel emits an edit_file call against a nonexistent file on its first
// turn, so the tool invocation fails inside the agent loop (surfacing as event.Err)
// rather than the model call itself failing.
type toolFailDreamModel struct {
	calls int32
}

func (m *toolFailDreamModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if atomic.AddInt32(&m.calls, 1) == 1 {
		return schema.AssistantMessage("editing", []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "edit_file", Arguments: `{"file_path":"missing.md","old_string":"a","new_string":"b"}`}},
		}), nil
	}
	return schema.AssistantMessage("done", nil), nil
}

func (m *toolFailDreamModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

func (m *toolFailDreamModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
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

func (s *countingSessionStore) LoadEvents(ctx context.Context, sessionID string, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	atomic.AddInt32(&s.loadCalls, 1)
	return s.SessionEventStore.LoadEvents(ctx, sessionID, req)
}

// newMiddlewareConfig builds a MiddlewareConfig wired for inline, low-threshold runs
// so tests trigger immediately. It keeps a separate staging dir so the input memory
// directory is never written during a run.
func newMiddlewareConfig(t *testing.T, dir string, backend automemory.Backend, m model.BaseModel[*schema.Message], store KVStore) *MiddlewareConfig[*schema.Message] {
	t.Helper()
	cfg := &MiddlewareConfig[*schema.Message]{
		MinInterval:       time.Hour,
		MinTouchedSession: 1,
		ScanInterval:      time.Minute,
		RunInline:         true,
	}
	cfg.MemoryDirectory = dir
	cfg.StagingDirectory = t.TempDir()
	cfg.MemoryBackend = backend
	cfg.Model = m
	cfg.Store = store
	return cfg
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
	cfg := &MiddlewareConfig[*schema.Message]{}
	cfg.MemoryDirectory = "/mem"
	cfg.MemoryBackend = automemory.NewInMemoryBackend()
	cfg.Model = &dreamModel{}
	cfg.SessionStore = adksession.NewInMemoryStore[*schema.Message](nil)

	_, err := New(ctx, cfg)
	require.NoError(t, err)
	require.Empty(t, cfg.SessionID)
	require.Zero(t, cfg.MinInterval)
	require.Zero(t, cfg.MinTouchedSession)
	require.Zero(t, cfg.ScanInterval)
	require.Zero(t, cfg.LockTTL)
	require.Nil(t, cfg.Store)
}

func TestMiddleware_AfterAgent_RunInlineWithSessionStore(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte("- [Existing](existing.md) - old"), 0o644))
	store := NewLocalKVStore()
	model := &dreamModel{}
	eventStore := &countingSessionStore{SessionEventStore: adksession.NewInMemoryStore[*schema.Message](nil)}
	err := eventStore.AppendEvents(ctx, "session-a", []*adk.SessionEvent[*schema.Message]{{
		EventID: "e1",
		Kind:    adk.SessionEventMessage,
		Message: schema.AssistantMessage("build failure: missing dependency", nil),
	}})
	require.NoError(t, err)
	cfg := newMiddlewareConfig(t, tmp, automemory.NewLocalBackend(), model, store)
	cfg.SessionStore = eventStore
	mw, err := New(ctx, cfg)
	require.NoError(t, err)
	impl, ok := mw.(*middleware[*schema.Message])
	require.True(t, ok)
	now := time.Now()
	impl.now = func() time.Time { return now }
	require.NoError(t, setScheduleState(ctx, store, impl.resolvedMemoryDir, &ScheduleState{LastConsolidatedAt: now.Add(-2 * time.Hour), NextCheckAt: now}))
	require.NoError(t, store.AddToSet(ctx, touchSetKey(impl.resolvedMemoryDir), "session-a", now.Add(-30*time.Minute), time.Hour))

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
	store := NewLocalKVStore()
	model := &dreamModel{}
	mw, err := New(ctx, newMiddlewareConfig(t, tmp, automemory.NewLocalBackend(), model, store))
	require.NoError(t, err)
	impl, ok := mw.(*middleware[*schema.Message])
	require.True(t, ok)
	now := time.Now()
	impl.now = func() time.Time { return now }
	require.NoError(t, store.AddToSet(ctx, touchSetKey(impl.resolvedMemoryDir), "older-session", now.Add(-2*time.Minute), time.Hour))

	_, err = impl.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{})
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(tmp, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))
}

func TestRun_ManualDreamWithoutSchedule(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	model := &dreamModel{}

	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory: tmp,
			MemoryBackend:   automemory.NewLocalBackend(),
			Model:           model,
		},
		SessionID: "manual-session",
		Sync:      true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, jobID)

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

	store := NewLocalKVStore()
	dm := &dreamModel{}
	mw, err := New(ctx, newMiddlewareConfig(t, tmp, automemory.NewLocalBackend(), dm, store))
	require.NoError(t, err)
	impl, ok := mw.(*middleware[*schema.Message])
	require.True(t, ok)
	now := time.Now()
	impl.now = func() time.Time { return now }
	require.NoError(t, store.AddToSet(ctx, touchSetKey(impl.resolvedMemoryDir), "older-session", now.Add(-3*time.Minute), time.Hour))

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

func TestIntegration_UserPerspective_RunUsesConfigSessionID(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))

	model := &dreamModel{}
	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory: tmp,
			MemoryBackend:   automemory.NewLocalBackend(),
			Model:           model,
		},
		SessionID: "fallback-session",
		Sync:      true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, jobID)

	raw, err := os.ReadFile(filepath.Join(tmp, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))
}

func TestMiddleware_AfterAgent_PrunesTouchesAfterSuccess(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	store := NewLocalKVStore()
	mw, err := New(ctx, newMiddlewareConfig(t, tmp, automemory.NewLocalBackend(), &dreamModel{}, store))
	require.NoError(t, err)
	impl := mw.(*middleware[*schema.Message])
	now := time.Now()
	impl.now = func() time.Time { return now }
	require.NoError(t, store.AddToSet(ctx, touchSetKey(impl.resolvedMemoryDir), "older-session", now.Add(-3*time.Minute), time.Hour))

	_, err = impl.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{})
	require.NoError(t, err)

	// After a successful run, touches at or before LastConsolidatedAt are dropped.
	remaining, err := store.ListSet(ctx, touchSetKey(impl.resolvedMemoryDir), time.Time{})
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func TestMiddleware_AfterAgent_AdvancesWindowAfterMaxFailures(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	store := NewLocalKVStore()
	cfg := newMiddlewareConfig(t, tmp, automemory.NewLocalBackend(), &failingDreamModel{}, store)
	cfg.MaxConsecutiveFailures = 2
	mw, err := New(ctx, cfg)
	require.NoError(t, err)
	impl := mw.(*middleware[*schema.Message])
	now := time.Now()
	impl.now = func() time.Time { return now }
	require.NoError(t, store.AddToSet(ctx, touchSetKey(impl.resolvedMemoryDir), "session-a", now.Add(-3*time.Minute), time.Hour))

	// First failure: window not yet advanced, failure counted.
	_, err = impl.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{})
	require.NoError(t, err)
	st, err := getScheduleState(ctx, store, impl.resolvedMemoryDir)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, 1, st.ConsecutiveFailures)
	require.True(t, st.LastConsolidatedAt.IsZero())

	// Allow the next check to fire and retry.
	require.NoError(t, setScheduleState(ctx, store, impl.resolvedMemoryDir, &ScheduleState{ConsecutiveFailures: 1, NextCheckAt: now}))

	// Second failure reaches MaxConsecutiveFailures: window advances and counter resets.
	_, err = impl.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{})
	require.NoError(t, err)
	st, err = getScheduleState(ctx, store, impl.resolvedMemoryDir)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, 0, st.ConsecutiveFailures)
	require.True(t, st.LastConsolidatedAt.Equal(now))
}

func TestRun_InputDirUnchangedAndOutputPopulated(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	output := t.TempDir()
	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(input, "MEMORY.md"), []byte("- [Existing](existing.md) - old\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(input, "existing.md"), []byte("original"), 0o644))

	store := NewLocalKVStore()
	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  input,
			OutputDirectory:  output,
			StagingDirectory: staging,
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &dreamModel{},
			Store:            store,
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, jobID)

	// Input directory is untouched.
	existing, err := os.ReadFile(filepath.Join(input, "existing.md"))
	require.NoError(t, err)
	require.Equal(t, "original", string(existing))
	_, err = os.Stat(filepath.Join(input, "dream.md"))
	require.True(t, os.IsNotExist(err), "input dir must not gain consolidated files")

	// Output directory has the consolidated result plus the seeded copy.
	consolidated, err := os.ReadFile(filepath.Join(output, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(consolidated))
	seeded, err := os.ReadFile(filepath.Join(output, "existing.md"))
	require.NoError(t, err)
	require.Equal(t, "original", string(seeded))

	// Job reached completed and is queryable.
	job, err := GetDreamStatus(ctx, store, jobID)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, StatusCompleted, job.Status)
	require.Equal(t, input, job.InputDir)
	require.Equal(t, output, job.OutputDir)
}

func TestRun_OnlyCopiesMarkdown(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	output := t.TempDir()
	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(input, "MEMORY.md"), []byte(""), 0o644))
	// Non-markdown files mixed into the memory directory must not be copied.
	require.NoError(t, os.WriteFile(filepath.Join(input, "data.json"), []byte(`{"k":"v"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(input, "notes.txt"), []byte("scratch"), 0o644))

	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  input,
			OutputDirectory:  output,
			StagingDirectory: staging,
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &dreamModel{},
			Store:            NewLocalKVStore(),
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.NoError(t, err)

	// Markdown is consolidated into the output.
	consolidated, err := os.ReadFile(filepath.Join(output, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(consolidated))

	// Non-markdown files are neither staged nor promoted.
	for _, name := range []string{"data.json", "notes.txt"} {
		_, statErr := os.Stat(filepath.Join(staging, jobID, name))
		require.True(t, os.IsNotExist(statErr), "staging must not contain %s", name)
		_, statErr = os.Stat(filepath.Join(output, name))
		require.True(t, os.IsNotExist(statErr), "output must not contain %s", name)
	}
	// They remain untouched in the input directory.
	raw, err := os.ReadFile(filepath.Join(input, "data.json"))
	require.NoError(t, err)
	require.Equal(t, `{"k":"v"}`, string(raw))
}

func TestRun_FailedJobStatus(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	store := NewLocalKVStore()

	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  tmp,
			StagingDirectory: t.TempDir(),
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &failingDreamModel{},
			Store:            store,
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.Error(t, err)
	require.NotEmpty(t, jobID)

	job, err := GetDreamStatus(ctx, store, jobID)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, StatusFailed, job.Status)
	require.NotEmpty(t, job.ErrMsg)
}

func TestRun_ToolErrorRecoveredAndReported(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	store := NewLocalKVStore()

	var mu sync.Mutex
	var toolErrs int
	model := &toolFailDreamModel{}

	// The model's first turn issues an edit_file that fails; recovery feeds the error
	// back and the model finishes on its next turn. The run completes, and the tool
	// failure is reported (once) through OnError.
	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  tmp,
			StagingDirectory: t.TempDir(),
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            model,
			Store:            store,
			OnError: func(_ context.Context, stage ErrorStage, _ error) {
				if stage == OnErrorStageToolCall {
					mu.Lock()
					toolErrs++
					mu.Unlock()
				}
			},
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.NoError(t, err, "a recoverable tool error must not fail the run")
	require.NotEmpty(t, jobID)

	job, err := GetDreamStatus(ctx, store, jobID)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, StatusCompleted, job.Status)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, toolErrs, "the tool failure should be reported exactly once via OnError")
	require.GreaterOrEqual(t, atomic.LoadInt32(&model.calls), int32(2), "model should be re-invoked after the tool error")
}

func TestRun_ModelErrorFailsJobEvenWhenHandleIteratorIgnoresErr(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	store := NewLocalKVStore()

	// A HandleIterator that drains events but never inspects event.Err. A fatal
	// (non-tool) error — here a model-call failure — must still fail the job.
	handler := func(_ context.Context, iter *adk.AsyncIterator[*adk.AgentEvent]) error {
		for {
			_, ok := iter.Next()
			if !ok {
				return nil
			}
		}
	}

	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  tmp,
			StagingDirectory: t.TempDir(),
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &failingDreamModel{},
			Store:            store,
			HandleIterator:   handler,
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.Error(t, err, "a fatal model error must surface as a run error")
	require.NotEmpty(t, jobID)

	job, err := GetDreamStatus(ctx, store, jobID)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, StatusFailed, job.Status)
	require.NotEmpty(t, job.ErrMsg)
}

func TestRun_AsyncByDefaultReturnsImmediatelyAndCompletes(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	output := t.TempDir()
	store := NewLocalKVStore()

	// Default (Sync == false): Run returns the job id immediately with a nil error,
	// before the consolidation finishes.
	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  tmp,
			OutputDirectory:  output,
			StagingDirectory: t.TempDir(),
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &dreamModel{},
			Store:            store,
		},
		SessionID: "s1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, jobID)

	// The job is observable and eventually reaches completed via polling.
	waitForJobStatus(t, ctx, store, jobID, StatusCompleted)

	consolidated, err := os.ReadFile(filepath.Join(output, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(consolidated))
}

func TestCancelDream_TerminatesRunningJobStatus(t *testing.T) {
	ctx := context.Background()
	store := NewLocalKVStore()
	job := &Job{ID: "drm_test", Status: StatusRunning, CreatedAt: time.Now()}
	require.NoError(t, setJob(ctx, store, job, time.Hour))

	require.NoError(t, CancelDream(ctx, store, "drm_test"))

	got, err := GetDreamStatus(ctx, store, "drm_test")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, StatusCanceled, got.Status)
	require.False(t, got.EndedAt.IsZero())

	// Canceling a terminal job is a no-op.
	require.NoError(t, CancelDream(ctx, store, "drm_test"))
}

func TestLocalKVStore_SetOps(t *testing.T) {
	ctx := context.Background()
	store := NewLocalKVStore()
	base := time.Now()
	require.NoError(t, store.AddToSet(ctx, "k", "a", base.Add(-10*time.Minute), time.Hour))
	require.NoError(t, store.AddToSet(ctx, "k", "b", base.Add(-1*time.Minute), time.Hour))

	all, err := store.ListSet(ctx, "k", time.Time{})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a", "b"}, all)

	recent, err := store.ListSet(ctx, "k", base.Add(-5*time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, recent)

	require.NoError(t, store.PruneSet(ctx, "k", base.Add(-5*time.Minute)))
	left, err := store.ListSet(ctx, "k", time.Time{})
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, left)
}

func TestNew_WarnsOnLocalStoreDefault(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	var mu sync.Mutex
	var warnings []error
	cfg := &MiddlewareConfig[*schema.Message]{}
	cfg.MemoryDirectory = tmp
	cfg.MemoryBackend = automemory.NewLocalBackend()
	cfg.Model = &dreamModel{}
	cfg.SessionID = "s1" // isolate the store warning from the count-gate warning
	cfg.OnError = func(_ context.Context, stage ErrorStage, err error) {
		if stage == OnErrorStageInit {
			mu.Lock()
			warnings = append(warnings, err)
			mu.Unlock()
		}
	}
	_, err := New(ctx, cfg)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, warnings, 1)
	require.ErrorIs(t, warnings[0], errLocalKVStoreSingleProcess)
}

func TestNew_WarnsWhenCountGateLacksSessionID(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	var mu sync.Mutex
	var gotCountGate bool
	cfg := &MiddlewareConfig[*schema.Message]{MinTouchedSession: 5}
	cfg.MemoryDirectory = tmp
	cfg.MemoryBackend = automemory.NewLocalBackend()
	cfg.Model = &dreamModel{}
	cfg.Store = NewLocalKVStore() // avoid the unrelated store warning
	// SessionID intentionally empty.
	cfg.OnError = func(_ context.Context, _ ErrorStage, err error) {
		if err == errCountGateNeedsSessionID {
			mu.Lock()
			gotCountGate = true
			mu.Unlock()
		}
	}
	_, err := New(ctx, cfg)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.True(t, gotCountGate, "expected count-gate warning when MinTouchedSession>1 and SessionID empty")
}

func TestNew_NoCountGateWarningWhenSatisfiable(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	var mu sync.Mutex
	var gotCountGate bool
	record := func(_ context.Context, _ ErrorStage, err error) {
		if err == errCountGateNeedsSessionID {
			mu.Lock()
			gotCountGate = true
			mu.Unlock()
		}
	}

	// Case 1: SessionID set with the count gate active -> no warning.
	cfg := &MiddlewareConfig[*schema.Message]{MinTouchedSession: 5}
	cfg.MemoryDirectory = tmp
	cfg.MemoryBackend = automemory.NewLocalBackend()
	cfg.Model = &dreamModel{}
	cfg.Store = NewLocalKVStore()
	cfg.SessionID = "s1"
	cfg.OnError = record
	_, err := New(ctx, cfg)
	require.NoError(t, err)

	// Case 2: count gate disabled (MinTouchedSession=1), no SessionID -> no warning.
	cfg2 := &MiddlewareConfig[*schema.Message]{MinTouchedSession: 1}
	cfg2.MemoryDirectory = tmp
	cfg2.MemoryBackend = automemory.NewLocalBackend()
	cfg2.Model = &dreamModel{}
	cfg2.Store = NewLocalKVStore()
	cfg2.OnError = record
	_, err = New(ctx, cfg2)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.False(t, gotCountGate, "count-gate warning should not fire when the gate is satisfiable or disabled")
}

// recordingShell captures the commands it is asked to execute so cleanup behavior
// can be asserted without touching the real filesystem.
type recordingShell struct {
	mu       sync.Mutex
	commands []string
}

func (s *recordingShell) Execute(_ context.Context, req *adkfs.ExecuteRequest) (*adkfs.ExecuteResponse, error) {
	s.mu.Lock()
	s.commands = append(s.commands, req.Command)
	s.mu.Unlock()
	code := 0
	return &adkfs.ExecuteResponse{ExitCode: &code}, nil
}

func (s *recordingShell) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func TestRun_ShellCleansUpStagingAfterPromote(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	output := t.TempDir()
	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(input, "MEMORY.md"), []byte(""), 0o644))
	shell := &recordingShell{}

	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  input,
			OutputDirectory:  output,
			StagingDirectory: staging,
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &dreamModel{},
			Shell:            shell,
			Store:            NewLocalKVStore(),
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.NoError(t, err)

	cmds := shell.snapshot()
	require.Len(t, cmds, 1)
	require.Contains(t, cmds[0], "rm -rf")
	require.Contains(t, cmds[0], filepath.Join(staging, jobID))
}

// backendWithShell embeds a Backend and a Shell in one struct, mirroring a
// sandbox/filesystem implementation that satisfies both interfaces. It lets the test
// verify the Shell is auto-derived from MemoryBackend without configuring it twice.
type backendWithShell struct {
	automemory.Backend
	shell *recordingShell
}

func (b *backendWithShell) Execute(ctx context.Context, req *adkfs.ExecuteRequest) (*adkfs.ExecuteResponse, error) {
	return b.shell.Execute(ctx, req)
}

func TestRun_ShellAutoDerivedFromBackend(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	output := t.TempDir()
	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(input, "MEMORY.md"), []byte(""), 0o644))
	shell := &recordingShell{}
	backend := &backendWithShell{Backend: automemory.NewLocalBackend(), shell: shell}

	// Config.Shell is intentionally left nil; the Shell must be derived from the
	// MemoryBackend because it also implements filesystem.Shell.
	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  input,
			OutputDirectory:  output,
			StagingDirectory: staging,
			MemoryBackend:    backend,
			Model:            &dreamModel{},
			Store:            NewLocalKVStore(),
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.NoError(t, err)

	cmds := shell.snapshot()
	require.Len(t, cmds, 1)
	require.Contains(t, cmds[0], "rm -rf")
	require.Contains(t, cmds[0], filepath.Join(staging, jobID))
}

func TestRun_NoShellLeavesStagingInPlace(t *testing.T) {
	ctx := context.Background()
	input := t.TempDir()
	output := t.TempDir()
	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(input, "MEMORY.md"), []byte(""), 0o644))

	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  input,
			OutputDirectory:  output,
			StagingDirectory: staging,
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &dreamModel{},
			Store:            NewLocalKVStore(),
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.NoError(t, err)

	// Without a Shell, the staging directory and its consolidated output remain.
	staged, err := os.ReadFile(filepath.Join(staging, jobID, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(staged))
}

func TestRun_InPlacePromoteWarns(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))

	var mu sync.Mutex
	var promoteWarns []error
	_, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  tmp, // OutputDirectory defaults to MemoryDirectory -> in place
			StagingDirectory: t.TempDir(),
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &dreamModel{},
			OnError: func(_ context.Context, stage ErrorStage, err error) {
				if stage == OnErrorStagePromote {
					mu.Lock()
					promoteWarns = append(promoteWarns, err)
					mu.Unlock()
				}
			},
			Store: NewLocalKVStore(),
		},
		SessionID: "s1",
		Sync:      true,
	})
	require.NoError(t, err)

	// The consolidated result lands in the source directory.
	raw, err := os.ReadFile(filepath.Join(tmp, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, promoteWarns, 1)
	require.ErrorIs(t, promoteWarns[0], errPromoteInPlaceBestEffort)
}

func TestRun_ReturnsEmptyWhenRunLockHeld(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	store := NewLocalKVStore()

	resolved, err := ainternal.ResolveMemoryDir(tmp)
	require.NoError(t, err)
	// Pre-acquire the run lock another runner would hold.
	_, ok, err := store.AcquireLock(ctx, runLockKey(resolved), time.Hour)
	require.NoError(t, err)
	require.True(t, ok)

	jobID, err := Run(ctx, &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  tmp,
			StagingDirectory: t.TempDir(),
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            &dreamModel{},
			Store:            store,
		},
		SessionID: "s1",
	})
	require.NoError(t, err)
	require.Empty(t, jobID, "Run must not run while the run lock is held")

	// The dream did not execute, so no consolidated file was produced.
	_, statErr := os.Stat(filepath.Join(tmp, "dream.md"))
	require.True(t, os.IsNotExist(statErr))
}

func TestGetDreamStatus_UnknownJobReturnsNil(t *testing.T) {
	ctx := context.Background()
	store := NewLocalKVStore()
	job, err := GetDreamStatus(ctx, store, "drm_missing")
	require.NoError(t, err)
	require.Nil(t, job)
}

func TestCancelDream_UnknownJobErrors(t *testing.T) {
	ctx := context.Background()
	store := NewLocalKVStore()
	err := CancelDream(ctx, store, "drm_missing")
	require.Error(t, err)
}

func TestLifecycle_NilStoreGuards(t *testing.T) {
	ctx := context.Background()
	_, err := GetDreamStatus(ctx, nil, "drm_x")
	require.Error(t, err)
	require.Error(t, CancelDream(ctx, nil, "drm_x"))
}

func TestCancelDream_AbortsRunningJobBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	store := NewLocalKVStore()

	// gateModel blocks on the first generation until released, giving the test time
	// to cancel an in-flight run; the cancel is observed via the run context.
	gm := &gateModel{released: make(chan struct{})}

	cfg := &RunConfig[*schema.Message]{
		BaseConfig: BaseConfig[*schema.Message]{
			MemoryDirectory:  tmp,
			StagingDirectory: t.TempDir(),
			MemoryBackend:    automemory.NewLocalBackend(),
			Model:            gm,
			Store:            store,
		},
		SessionID: "s1",
	}

	// Run is asynchronous by default: it returns immediately while the run blocks in
	// gateModel.
	jobID, err := Run(ctx, cfg)
	require.NoError(t, err)
	require.NotEmpty(t, jobID)

	// Wait until the run is in progress, then cancel it and let the model proceed.
	running := waitForRunningJob(t, ctx, store)
	require.Equal(t, jobID, running)
	require.NoError(t, CancelDream(ctx, store, jobID))
	close(gm.released)

	// The job settles into canceled.
	waitForJobStatus(t, ctx, store, jobID, StatusCanceled)

	// A canceled run must not promote a consolidated result.
	_, statErr := os.Stat(filepath.Join(tmp, "dream.md"))
	require.True(t, os.IsNotExist(statErr))
}

// gateModel blocks the first Generate call until released is closed, then drives a
// normal consolidation. It lets a test observe and cancel a running dream.
type gateModel struct {
	released chan struct{}
	calls    int32
}

func (m *gateModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if atomic.AddInt32(&m.calls, 1) == 1 {
		select {
		case <-m.released:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return schema.AssistantMessage("dream", []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "write_file", Arguments: `{"file_path":"dream.md","content":"consolidated"}`}},
		}), nil
	}
	return schema.AssistantMessage("dream complete", nil), nil
}

func (m *gateModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

func (m *gateModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func waitForRunningJob(t *testing.T, ctx context.Context, store KVStore) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if id := scanForRunningJob(ctx, store); id != "" {
			return id
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no running dream job appeared")
	return ""
}

// waitForJobStatus polls until the job reaches want or the deadline elapses.
func waitForJobStatus(t *testing.T, ctx context.Context, store KVStore, jobID string, want Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := GetDreamStatus(ctx, store, jobID)
		require.NoError(t, err)
		if job != nil && job.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, _ := GetDreamStatus(ctx, store, jobID)
	var got Status
	if job != nil {
		got = job.Status
	}
	t.Fatalf("job %s did not reach status %q (last: %q)", jobID, want, got)
}

// scanForRunningJob looks up the running job by probing the local store's job keys.
func scanForRunningJob(ctx context.Context, store KVStore) string {
	ls, ok := store.(*localKVStore)
	if !ok {
		return ""
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for key := range ls.kv {
		if !strings.HasPrefix(key, "dream::job::") {
			continue
		}
		id := strings.TrimPrefix(key, "dream::job::")
		if job, _ := getJobLocked(ls, id); job != nil && job.Status == StatusRunning {
			return id
		}
	}
	return ""
}

// getJobLocked reads a job from an already-locked localKVStore.
func getJobLocked(ls *localKVStore, jobID string) (*Job, error) {
	v, ok := ls.kv[jobKey(jobID)]
	if !ok {
		return nil, nil
	}
	var job Job
	if err := json.Unmarshal(v.value, &job); err != nil {
		return nil, err
	}
	return &job, nil
}
