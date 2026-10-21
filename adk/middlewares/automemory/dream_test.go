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

package automemory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type scriptedDreamModel[M adk.MessageType] struct {
	generate func(context.Context, []M, ...model.Option) (M, error)
}

func (m *scriptedDreamModel[M]) Generate(ctx context.Context, input []M, opts ...model.Option) (M, error) {
	return m.generate(ctx, input, opts...)
}

func (m *scriptedDreamModel[M]) Stream(context.Context, []M, ...model.Option) (*schema.StreamReader[M], error) {
	panic("Dream must use nonstreaming execution")
}

func dreamConstantModel(text string) *scriptedDreamModel[*schema.Message] {
	return &scriptedDreamModel[*schema.Message]{generate: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return schema.AssistantMessage(text, nil), nil
	}}
}

func dreamReply[M adk.MessageType](text string, calls ...schema.ToolCall) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.AssistantMessage(text, calls)).(M)
	case *schema.AgenticMessage:
		msg := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
		if text != "" {
			msg.ContentBlocks = append(msg.ContentBlocks, &schema.ContentBlock{
				Type:             schema.ContentBlockTypeAssistantGenText,
				AssistantGenText: &schema.AssistantGenText{Text: text},
			})
		}
		for _, call := range calls {
			msg.ContentBlocks = append(msg.ContentBlocks, &schema.ContentBlock{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					Name: call.Function.Name, CallID: call.ID, Arguments: call.Function.Arguments,
				},
			})
		}
		return any(msg).(M)
	default:
		panic("unreachable")
	}
}

func dreamToolCall(id, name, args string) schema.ToolCall {
	return schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}
}

type dreamTrackingStore[M adk.MessageType] struct {
	adk.SessionEventStore[M]
	loaded  []string
	appends int
	loadErr error
}

func (s *dreamTrackingStore[M]) LoadEvents(ctx context.Context, id string, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
	s.loaded = append(s.loaded, id)
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.SessionEventStore.LoadEvents(ctx, id, req)
}

func (s *dreamTrackingStore[M]) AppendEvents(ctx context.Context, id string, events []*adk.SessionEvent[M]) error {
	s.appends++
	return s.SessionEventStore.AppendEvents(ctx, id, events)
}

func testDreamFiles[M adk.MessageType](t *testing.T, bound bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	const index = "INDEX.md"
	require.NoError(t, os.WriteFile(filepath.Join(dir, index), []byte("old index"), 0o644))
	backend := NewLocalBackend()
	store := &dreamTrackingStore[M]{SessionEventStore: adksession.NewInMemoryStore[M](nil)}
	for _, id := range []string{"a", "b", "outside"} {
		require.NoError(t, store.SessionEventStore.AppendEvents(ctx, id, []*adk.SessionEvent[M]{{
			EventID: id + "-event", Kind: adk.SessionEventMessage, Message: makeUserMsg[M]("build failure in " + id),
		}}))
	}
	var calls int
	var prompts, names, searchResults []string
	cm := &scriptedDreamModel[M]{generate: func(ctx context.Context, input []M, opts ...model.Option) (M, error) {
		calls++
		for _, info := range model.GetCommonOptions(nil, opts...).Tools {
			names = append(names, info.Name)
		}
		for _, msg := range input {
			if isUserRole(msg) {
				prompts = append(prompts, sessionEventMessageString(msg))
			}
			if strings.Contains(sessionEventMessageString(msg), "[a]") {
				searchResults = append(searchResults, sessionEventMessageString(msg))
			}
		}
		if calls%2 == 0 {
			return dreamReply[M]("consolidation complete"), nil
		}
		toolCalls := []schema.ToolCall{
			dreamToolCall("read", "read_file", `{"file_path":"INDEX.md"}`),
			dreamToolCall("topic", "write_file", `{"file_path":"dream.md","content":"consolidated"}`),
			dreamToolCall("index", "write_file", `{"file_path":"INDEX.md","content":"- [Dream](dream.md)"}`),
		}
		for _, info := range model.GetCommonOptions(nil, opts...).Tools {
			if info.Name == "grep_session_history" {
				toolCalls = append(toolCalls, dreamToolCall("search", info.Name, `{"query":"build failure"}`))
			}
		}
		return dreamReply[M]("", toolCalls...), nil
	}}
	var observed []string
	options := &DreamOptions[M]{Model: cm, SessionStore: store, OnEvent: func(_ context.Context, e *adk.TypedAgentEvent[M]) error {
		if e.Output != nil && e.Output.MessageOutput != nil {
			require.False(t, e.Output.MessageOutput.IsStreaming)
			observed = append(observed, sessionEventMessageString(e.Output.MessageOutput.Message))
		}
		return nil
	}}
	var run func(context.Context, *DreamRequest) error
	if bound {
		options.Model = nil // Inherit only Config.Model.
		cfg := &Config[M]{MemoryDirectory: dir, MemoryBackend: backend, Model: cm, Dream: options,
			Read: &ReadConfig[M]{Index: &IndexConfig{FileName: index}, TopicSelection: &TopicSelectionConfig{Enable: boolPtr(false)}}}
		memory, err := New(ctx, cfg)
		require.NoError(t, err)
		var _ adk.TypedChatModelAgentMiddleware[M] = memory
		// Mutating caller-owned configuration must not redirect the bound run.
		cfg.MemoryDirectory = "/different"
		cfg.Read.Index.FileName = "changed.md"
		options.Model = &scriptedDreamModel[M]{generate: func(context.Context, []M, ...model.Option) (M, error) {
			var zero M
			return zero, errors.New("mutated config")
		}}
		run = memory.RunDream
	} else {
		cfg := &DreamConfig[M]{MemoryDirectory: dir, MemoryBackend: backend, IndexFileName: index, Options: options}
		run = func(ctx context.Context, req *DreamRequest) error { return RunDream(ctx, cfg, req) }
	}
	req := &DreamRequest{SessionIDs: []string{" a ", "b", "a"}}
	require.NoError(t, run(ctx, req))
	require.Equal(t, []string{" a ", "b", "a"}, req.SessionIDs)
	require.Equal(t, []string{"a", "b"}, store.loaded)
	require.Zero(t, store.appends, "the historical store is read-only")
	require.NotEmpty(t, searchResults)
	require.NotContains(t, strings.Join(searchResults, "\n"), "build failure in outside")
	require.Contains(t, prompts[0], "Read INDEX.md first")
	require.NotContains(t, prompts[0], "MEMORY.md")
	require.Contains(t, prompts[0], "Allowed session histories (2)")
	require.NotEmpty(t, observed)
	require.Contains(t, observed[len(observed)-1], "consolidation complete")
	raw, err := os.ReadFile(filepath.Join(dir, "dream.md"))
	require.NoError(t, err)
	require.Equal(t, "consolidated", string(raw))
	raw, err = os.ReadFile(filepath.Join(dir, index))
	require.NoError(t, err)
	require.Contains(t, string(raw), "dream.md")

	// A second call has fresh state and no history tool for an empty request.
	names = nil
	store.loaded = nil
	require.NoError(t, run(ctx, nil))
	require.Equal(t, 4, calls)
	require.NotContains(t, names, "grep_session_history")
	require.Empty(t, store.loaded)
	require.Zero(t, store.appends)
}

func TestRunDreamFiles(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprintf("Message/bound=%t", bound), func(t *testing.T) { testDreamFiles[*schema.Message](t, bound) })
		t.Run(fmt.Sprintf("AgenticMessage/bound=%t", bound), func(t *testing.T) { testDreamFiles[*schema.AgenticMessage](t, bound) })
	}
}

func TestDreamConfigurationAndRequestValidation(t *testing.T) {
	ctx := context.Background()
	cm := dreamConstantModel("done")
	backend := NewInMemoryBackend()
	memory, err := New(ctx, &Config[*schema.Message]{MemoryDirectory: "/mem", MemoryBackend: backend})
	require.NoError(t, err)
	require.ErrorIs(t, memory.RunDream(ctx, nil), ErrDreamNotConfigured)
	_, err = New(ctx, &Config[*schema.Message]{MemoryDirectory: "/mem", MemoryBackend: backend, Dream: &DreamOptions[*schema.Message]{}})
	require.ErrorContains(t, err, "model is required")
	// Read/Write model overrides must not become an implicit Dream fallback.
	_, err = New(ctx, &Config[*schema.Message]{MemoryDirectory: "/mem", MemoryBackend: backend,
		Read: &ReadConfig[*schema.Message]{Model: cm}, Dream: &DreamOptions[*schema.Message]{}})
	require.ErrorContains(t, err, "model is required")
	for _, cfg := range []*DreamConfig[*schema.Message]{
		nil, {}, {MemoryDirectory: "/mem", MemoryBackend: backend},
		{MemoryDirectory: "/mem", MemoryBackend: backend, Options: &DreamOptions[*schema.Message]{}},
		{MemoryDirectory: "/mem", MemoryBackend: backend, Options: &DreamOptions[*schema.Message]{Model: cm, MaxIterations: -1}},
	} {
		require.Error(t, RunDream(ctx, cfg, nil))
	}
	for _, index := range []string{"..", ".", "../index.md", "/index.md", `a\b`, " "} {
		require.Error(t, RunDream(ctx, &DreamConfig[*schema.Message]{
			MemoryDirectory: "/mem", MemoryBackend: backend, IndexFileName: index, Options: &DreamOptions[*schema.Message]{Model: cm},
		}, nil))
	}
	cfg := &DreamConfig[*schema.Message]{MemoryDirectory: "/mem", MemoryBackend: backend, Options: &DreamOptions[*schema.Message]{Model: cm}}
	require.ErrorContains(t, RunDream(ctx, cfg, &DreamRequest{SessionIDs: []string{"a", " "}}), "blank session ID")
	require.ErrorContains(t, RunDream(ctx, cfg, &DreamRequest{SessionIDs: []string{"a"}}), "require a session store")
	require.NoError(t, RunDream(ctx, cfg, nil))
	require.Zero(t, cfg.Options.MaxIterations, "defaults must not mutate user config")
}

func TestDreamDoesNotAutoRun(t *testing.T) {
	ctx := context.Background()
	var calls int32
	cm := &scriptedDreamModel[*schema.Message]{generate: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		atomic.AddInt32(&calls, 1)
		return schema.AssistantMessage("done", nil), nil
	}}
	memory, err := New(ctx, &Config[*schema.Message]{MemoryDirectory: "/mem", MemoryBackend: NewInMemoryBackend(),
		Dream: &DreamOptions[*schema.Message]{Model: cm}})
	require.NoError(t, err)
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "main", Model: dreamConstantModel("main answer"), Handlers: []adk.ChatModelAgentMiddleware{memory},
	})
	require.NoError(t, err)
	iter := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}).Query(ctx, "hello")
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	require.Zero(t, atomic.LoadInt32(&calls))
	require.NoError(t, memory.RunDream(ctx, nil))
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

type dreamBeforeModel struct {
	adk.BaseChatModelAgentMiddleware
	before func(context.Context, *adk.ChatModelAgentState) error
}

func (h *dreamBeforeModel) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	return ctx, state, h.before(ctx, state)
}

func TestDreamAdditionalHandlers(t *testing.T) {
	ctx := context.Background()
	var order []string
	cm := dreamConstantModel("done")
	reduce, err := reduction.New(ctx, &reduction.Config{SkipTruncation: true, SkipClear: true,
		TokenCounter: func(context.Context, []*schema.Message, []*schema.ToolInfo) (int64, error) {
			order = append(order, "reduction")
			return 0, nil
		}})
	require.NoError(t, err)
	summarize, err := summarization.New(ctx, &summarization.Config{Model: cm,
		TokenCounter: func(context.Context, *summarization.TokenCounterInput) (int, error) {
			order = append(order, "summarization")
			return 0, nil
		}})
	require.NoError(t, err)
	custom := &dreamBeforeModel{before: func(_ context.Context, state *adk.ChatModelAgentState) error {
		order = append(order, "custom")
		var names []string
		for _, ti := range state.ToolInfos {
			names = append(names, ti.Name)
		}
		require.Contains(t, names, "write_file", "filesystem handler must run before custom handlers")
		return nil
	}}
	cfg := &Config[*schema.Message]{MemoryDirectory: "/mem", MemoryBackend: NewInMemoryBackend(),
		Dream: &DreamOptions[*schema.Message]{Model: cm, Handlers: []adk.ChatModelAgentMiddleware{reduce, summarize, custom}}}
	memory, err := New(ctx, cfg)
	require.NoError(t, err)
	cfg.Dream.Handlers[0] = nil
	require.NoError(t, memory.RunDream(ctx, nil))
	require.Equal(t, []string{"reduction", "summarization", "custom"}, order[:3])
}

func TestDreamErrorsCancellationAndInterrupt(t *testing.T) {
	backend := NewInMemoryBackend()
	newConfig := func(cm model.BaseChatModel) *DreamConfig[*schema.Message] {
		return &DreamConfig[*schema.Message]{MemoryDirectory: "/mem", MemoryBackend: backend, Options: &DreamOptions[*schema.Message]{Model: cm}}
	}
	runtimeErr := errors.New("model failed")
	cfg := newConfig(&scriptedDreamModel[*schema.Message]{generate: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return nil, runtimeErr
	}})
	cfg.Options.OnEvent = func(context.Context, *adk.AgentEvent) error { return nil }
	require.ErrorIs(t, RunDream(context.Background(), cfg, nil), runtimeErr)
	callbackErr := errors.New("observer failed")
	cfg = newConfig(dreamConstantModel("done"))
	cfg.Options.OnEvent = func(context.Context, *adk.AgentEvent) error { return callbackErr }
	require.ErrorIs(t, RunDream(context.Background(), cfg, nil), callbackErr)
	cfg = newConfig(dreamConstantModel("done"))
	cfg.Options.Handlers = []adk.ChatModelAgentMiddleware{&dreamBeforeModel{before: func(ctx context.Context, _ *adk.ChatModelAgentState) error {
		return adk.InterruptSignalFromEvent(adk.Interrupt(ctx, "approval required"))
	}}}
	require.ErrorIs(t, RunDream(context.Background(), cfg, nil), ErrDreamInterrupted)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, RunDream(ctx, newConfig(dreamConstantModel("done")), nil), context.Canceled)
	started := make(chan struct{})
	cfg = newConfig(&scriptedDreamModel[*schema.Message]{generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunDream(ctx, cfg, nil) }()
	<-started
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Dream did not complete after cancellation")
	}
	cfg = newConfig(&scriptedDreamModel[*schema.Message]{generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, RunDream(ctx, cfg, nil), context.DeadlineExceeded)
}

func TestDreamHistoryToolErrorIsReturned(t *testing.T) {
	storeErr := errors.New("history store failed")
	cm := &scriptedDreamModel[*schema.Message]{generate: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return dreamReply[*schema.Message]("", dreamToolCall("search", "grep_session_history", `{"query":"build failure"}`)), nil
	}}
	err := RunDream(context.Background(), &DreamConfig[*schema.Message]{
		MemoryDirectory: "/mem", MemoryBackend: NewInMemoryBackend(),
		Options: &DreamOptions[*schema.Message]{
			Model: cm, SessionStore: &dreamTrackingStore[*schema.Message]{loadErr: storeErr},
			OnEvent: func(context.Context, *adk.AgentEvent) error { return nil },
		},
	}, &DreamRequest{SessionIDs: []string{"a"}})
	require.ErrorIs(t, err, storeErr)
}

func TestDreamIterationLimit(t *testing.T) {
	var calls int
	cm := &scriptedDreamModel[*schema.Message]{generate: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		calls++
		return dreamReply[*schema.Message]("", dreamToolCall(fmt.Sprint(calls), "read_file", `{"file_path":"MEMORY.md"}`)), nil
	}}
	require.ErrorIs(t, RunDream(context.Background(), &DreamConfig[*schema.Message]{
		MemoryDirectory: "/mem", MemoryBackend: NewInMemoryBackend(), Options: &DreamOptions[*schema.Message]{Model: cm, MaxIterations: 2},
	}, nil), adk.ErrExceedMaxIterations)
	require.Equal(t, 2, calls)
}

func TestDreamBoundDirectorySurvivesWorkingDirectoryChange(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	defer func() { _ = os.Chdir(oldwd) }()
	var calls int
	cm := &scriptedDreamModel[*schema.Message]{generate: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return dreamReply[*schema.Message]("", dreamToolCall("write", "write_file", `{"file_path":"topic.md","content":"kept"}`)), nil
		}
		return dreamReply[*schema.Message]("done"), nil
	}}
	memory, err := New(ctx, &Config[*schema.Message]{MemoryDirectory: "mem", MemoryBackend: NewLocalBackend(), Model: cm,
		Dream: &DreamOptions[*schema.Message]{}})
	require.NoError(t, err)
	other := t.TempDir()
	require.NoError(t, os.Chdir(other))
	require.NoError(t, memory.RunDream(ctx, nil))
	raw, err := os.ReadFile(filepath.Join(root, "mem", "topic.md"))
	require.NoError(t, err)
	require.Equal(t, "kept", string(raw))
	_, err = os.Stat(filepath.Join(other, "mem"))
	require.True(t, os.IsNotExist(err))
}

func TestDreamBoundRunnerStateIsRaceFree(t *testing.T) {
	// Shared runner state must be race-free; task coordination remains the caller's job.
	ctx := context.Background()
	memory, err := New(ctx, &Config[*schema.Message]{MemoryDirectory: "/mem", MemoryBackend: NewInMemoryBackend(),
		Dream: &DreamOptions[*schema.Message]{Model: dreamConstantModel("done")}})
	require.NoError(t, err)
	errs := make(chan error, 8)
	for i := 0; i < cap(errs); i++ {
		go func() { errs <- memory.RunDream(ctx, nil) }()
	}
	for i := 0; i < cap(errs); i++ {
		require.NoError(t, <-errs)
	}
}

func TestDreamConsumesAndClosesInjectedStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(nil)
	stream := schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("a", nil), schema.AssistantMessage("b", nil)})
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{IsStreaming: true, MessageStream: stream}}})
	gen.Close()
	require.NoError(t, consumeDreamEvents(ctx, cancel, iter, func(_ context.Context, e *adk.AgentEvent) error {
		require.False(t, e.Output.MessageOutput.IsStreaming)
		require.Equal(t, "ab", e.Output.MessageOutput.Message.Content)
		return nil
	}))
	_, err := stream.Recv()
	require.Error(t, err)

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	iter, gen = adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	first := errors.New("first failure")
	gen.Send(&adk.AgentEvent{Err: first})
	discard, discardWriter := schema.Pipe[*schema.Message](1)
	defer discardWriter.Close()
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{IsStreaming: true, MessageStream: discard}}})
	gen.Close()
	require.ErrorIs(t, consumeDreamEvents[*schema.Message](ctx, cancel, iter, nil), first)
	_, ok := iter.Next()
	require.False(t, ok, "the entire iterator must be drained after failure")
	require.True(t, discardWriter.Send(schema.AssistantMessage("discard", nil), nil), "discarded stream must be closed")
}

func TestDreamStreamErrorIsReturnedAndStreamClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	stream, writer := schema.Pipe[*schema.Message](1)
	streamErr := errors.New("stream failed")
	writer.Send(nil, streamErr)
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, MessageStream: stream,
	}}})
	gen.Close()
	observed := false
	err := consumeDreamEvents(ctx, cancel, iter, func(context.Context, *adk.AgentEvent) error {
		observed = true
		return nil
	})
	require.ErrorIs(t, err, streamErr)
	require.False(t, observed)
	require.True(t, writer.Send(nil, nil), "failed stream must be closed")
	writer.Close()
}

func TestDreamRunnerIsIsolatedFromParentRunner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type tracingKey struct{}
	ctx = context.WithValue(ctx, tracingKey{}, "trace-value")
	inner := &scriptedDreamModel[*schema.Message]{generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		_, hasSession := adk.RunnerSessionID(ctx)
		require.False(t, hasSession)
		require.Equal(t, "trace-value", ctx.Value(tracingKey{}))
		return schema.AssistantMessage("inner dream only", nil), nil
	}}
	history := &dreamTrackingStore[*schema.Message]{SessionEventStore: adksession.NewInMemoryStore[*schema.Message](nil)}
	memory, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem", MemoryBackend: NewInMemoryBackend(),
		Dream: &DreamOptions[*schema.Message]{Model: inner, SessionStore: history},
	})
	require.NoError(t, err)
	main := &scriptedDreamModel[*schema.Message]{generate: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		if err := memory.RunDream(ctx, nil); err != nil {
			return nil, err
		}
		return schema.AssistantMessage("main answer", nil), nil
	}}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: "main", Model: main})
	require.NoError(t, err)
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	iter := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, SessionID: "parent", SessionStore: store}).Query(ctx, "hello")
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		require.NotEqual(t, "automemory_dream", event.AgentName)
	}
	events, err := store.LoadEvents(ctx, "parent", &adk.LoadSessionEventsRequest{Kinds: []adk.SessionEventKind{adk.SessionEventMessage}})
	require.NoError(t, err)
	var transcript string
	for _, event := range events.Events {
		transcript += sessionEventMessageString(event.Message)
	}
	require.Contains(t, transcript, "main answer")
	require.NotContains(t, transcript, "inner dream only")
	require.Empty(t, history.loaded)
	require.Zero(t, history.appends)
}

func TestDreamPromptLanguageAndIndex(t *testing.T) {
	for _, language := range []adk.Language{adk.LanguageEnglish, adk.LanguageChinese} {
		require.NoError(t, adk.SetLanguage(language))
		prompt := buildConsolidationPrompt("/mem", "INDEX.md", []string{"a", "b"}, true)
		require.Contains(t, prompt, "INDEX.md")
		require.NotContains(t, prompt, "MEMORY.md")
		require.Contains(t, prompt, "grep_session_history")
		require.NotContains(t, prompt, "since last consolidation")
		require.NotContains(t, prompt, "触达过")
	}
	require.NoError(t, adk.SetLanguage(adk.LanguageEnglish))
	require.NotContains(t, buildConsolidationPrompt("/mem", "MEMORY.md", nil, false), "grep_session_history")
}
