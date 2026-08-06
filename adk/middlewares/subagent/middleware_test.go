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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	backgroundlocal "github.com/cloudwego/eino/adk/backgroundtask/local"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *backgroundtask.Config,
) *backgroundtask.Manager {
	t.Helper()
	if config == nil {
		config = &backgroundtask.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *backgroundtask.Task) error { return nil }
	}
	manager, err := backgroundtask.New(ctx, config)
	require.NoError(t, err)
	return manager
}

type mockAgent struct {
	name       string
	desc       string
	run        func(context.Context, *adk.AgentInput) string
	runOptions func([]adk.AgentRunOption)
}

type middlewareRunOptions struct {
	value string
}

var testManagerStores sync.Map
var testManagerExecutors sync.Map

func newTestManager(t testing.TB, ctx context.Context) *backgroundtask.Manager {
	store := backgroundtask.NewInMemoryStore(nil)
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, ctx, &backgroundtask.Config{
		Tasks: store, Executors: executors,
	})
	testManagerStores.Store(manager, store)
	testManagerExecutors.Store(manager, executors)
	return manager
}

func executorsForTest(t *testing.T, manager *backgroundtask.Manager) *backgroundtask.ExecutorRegistry {
	t.Helper()
	executors, ok := testManagerExecutors.Load(manager)
	require.True(t, ok)
	return executors.(*backgroundtask.ExecutorRegistry)
}

func (m *mockAgent) Name(context.Context) string        { return m.name }
func (m *mockAgent) Description(context.Context) string { return m.desc }
func (m *mockAgent) Run(ctx context.Context, input *adk.AgentInput, options ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	if m.runOptions != nil {
		m.runOptions(options)
	}
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

func durableExecutor(t *testing.T) *durablesubagent.Executor[*schema.Message] {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	executor, err := durablesubagent.NewExecutor(&durablesubagent.ExecutorConfig[*schema.Message]{
		SessionStore: store, CheckPointStore: store,
	})
	require.NoError(t, err)
	return executor
}

func durableBackground(t *testing.T, mgr *backgroundtask.Manager, agents ...adk.Agent) *BackgroundConfig {
	t.Helper()
	_ = agents
	executors, ok := testManagerExecutors.Load(mgr)
	require.True(t, ok)
	return &BackgroundConfig{
		Durable: &DurableBackgroundConfig{
			Manager: mgr, Executors: executors.(*backgroundtask.ExecutorRegistry),
			Executor: durableExecutor(t),
		},
	}
}

func mustLocalRunner(t *testing.T, manager *backgroundtask.Manager) *backgroundlocal.Runner {
	t.Helper()
	executors, ok := testManagerExecutors.Load(manager)
	require.True(t, ok)
	runner, err := backgroundlocal.New(&backgroundlocal.Config{
		Manager: manager, Executors: executors.(*backgroundtask.ExecutorRegistry),
	})
	require.NoError(t, err)
	return runner
}

func localBackground(t *testing.T, manager *backgroundtask.Manager) *BackgroundConfig {
	return &BackgroundConfig{
		Local: &LocalBackgroundConfig{Runner: mustLocalRunner(t, manager)},
	}
}

func runnerEnvironmentContext(t *testing.T) context.Context {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	var captured context.Context
	agent := &mockAgent{name: "capture", run: func(ctx context.Context, _ *adk.AgentInput) string {
		captured = ctx
		return "captured"
	}}
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: store,
		SessionID: "parent-session", SessionStore: store,
	})
	iter := runner.Query(context.Background(), "capture")
	for {
		if _, ok := iter.Next(); !ok {
			break
		}
	}
	require.NotNil(t, captured)
	return captured
}

func terminalTask(t *testing.T, mgr *backgroundtask.Manager) *backgroundtask.Task {
	t.Helper()
	store, ok := testManagerStores.Load(mgr)
	require.True(t, ok, "test Manager Store is unavailable")
	outbox := store.(backgroundtask.NotificationOutbox)
	result, err := outbox.Receive(context.Background(), &backgroundtask.ReceiveNotificationsRequest{
		Limit: 100, LeaseDuration: time.Millisecond,
	})
	require.NoError(t, err)
	for i := len(result.Deliveries) - 1; i >= 0; i-- {
		record := result.Deliveries[i].Record
		task, getErr := mgr.Get(context.Background(), record.TaskID)
		require.NoError(t, getErr)
		if task.Status == backgroundtask.StatusCompleted ||
			task.Status == backgroundtask.StatusFailed ||
			task.Status == backgroundtask.StatusCanceled {
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
		Background: &BackgroundConfig{},
	})
	assert.Error(t, err)

	manager := newTestManager(t, context.Background())
	_, err = New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent},
		Background: &BackgroundConfig{
			Local:   &LocalBackgroundConfig{Runner: mustLocalRunner(t, manager)},
			Durable: &DurableBackgroundConfig{Manager: manager, Executors: executorsForTest(t, manager)},
		},
	})
	require.ErrorContains(t, err, "exactly one")

	_, err = New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent},
		Background: &BackgroundConfig{
			Durable: &DurableBackgroundConfig{Manager: manager, Executors: executorsForTest(t, manager)},
		},
	})
	require.ErrorContains(t, err, "Manager, executor registry, and Executor")

	_, err = New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{Runner: mustLocalRunner(t, manager)},
		},
	})
	require.NoError(t, err)
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
	ctx := runnerEnvironmentContext(t)
	mgr := newTestManager(t, ctx)
	agent := &mockAgent{name: "worker", desc: "durable result"}
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Background: durableBackground(t, mgr, agent),
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
	assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
	assert.Contains(t, string(task.ResultData), "durable result")
}

func TestOnlyDurableAgentToolExposesPersistentChildSession(t *testing.T) {
	agent := &mockAgent{name: "worker", desc: "does work"}
	localManager := newTestManager(t, context.Background())
	durableManager := newTestManager(t, context.Background())
	local, err := New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent}, Background: localBackground(t, localManager),
	})
	require.NoError(t, err)
	durable, err := New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent}, Background: durableBackground(t, durableManager, agent),
	})
	require.NoError(t, err)
	_, localCtx, err := local.BeforeAgent(
		context.Background(), &adk.ChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	_, durableCtx, err := durable.BeforeAgent(
		context.Background(), &adk.ChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	localInfo, err := localCtx.Tools[0].Info(context.Background())
	require.NoError(t, err)
	durableInfo, err := durableCtx.Tools[0].Info(context.Background())
	require.NoError(t, err)
	localSchema, err := localInfo.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	durableSchema, err := durableInfo.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	localJSON, err := sonic.MarshalString(localSchema)
	require.NoError(t, err)
	durableJSON, err := sonic.MarshalString(durableSchema)
	require.NoError(t, err)
	assert.NotContains(t, localJSON, "child_session_id")
	assert.Contains(t, durableJSON, "child_session_id")
}

func decodeDurableAgentToolResult(
	t *testing.T,
	value string,
) *durableAgentToolResult {
	t.Helper()
	var result durableAgentToolResult
	require.NoError(t, sonic.UnmarshalString(value, &result))
	require.NotEmpty(t, result.TaskID)
	require.NotEmpty(t, result.ChildSessionID)
	require.NotEmpty(t, result.Status)
	return &result
}

func TestFormatManagedAgentResultPreservesDescriptionInErrors(t *testing.T) {
	task := &backgroundtask.Task{
		Spec: backgroundtask.Spec{
			ID: "subagent_secret", Description: "review implementation",
		},
		Status:      backgroundtask.StatusFailed,
		ResultError: "model failed",
	}
	_, err := formatManagedAgentResult("reviewer", task, "")
	assert.EqualError(
		t, err,
		`subagent "reviewer" task "subagent_secret" (review implementation) failed: model failed`,
	)

	task.Status = backgroundtask.StatusCanceled
	_, err = formatManagedAgentResult("reviewer", task, "")
	assert.EqualError(
		t, err,
		`subagent "reviewer" task "subagent_secret" (review implementation) was canceled`,
	)
}

func TestAttack_DurableTerminalResultPreservesChildSessionIdentity(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, ctx)
	agent := &mockAgent{name: "worker", desc: "done"}
	middleware, err := New(ctx, &Config{
		SubAgents:  []adk.Agent{agent},
		Background: durableBackground(t, manager, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(
		ctx,
		&adk.ChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	_, err = runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx,
		`{"subagent_type":"worker","prompt":"work","description":"test"}`,
	)
	require.NoError(t, err)
	task := terminalTask(t, manager)
	require.NotNil(t, task)

	for _, status := range []backgroundtask.Status{
		backgroundtask.StatusFailed,
		backgroundtask.StatusCanceled,
	} {
		task.Status = status
		task.ResultError = "terminal error"
		raw, formatErr := formatDurableAgentResult("worker", task)
		require.NoError(t, formatErr)
		result := decodeDurableAgentToolResult(t, raw)
		require.Equal(t, status, result.Status)
		require.Equal(t, "terminal error", result.Error)
	}
}

func TestFormatManagedAgentResultLifecycleStates(t *testing.T) {
	task := &backgroundtask.Task{
		Spec: backgroundtask.Spec{
			ID: "subagent_task", Description: "research",
			OutputFile: "/tasks/output",
		},
	}
	task.Status = backgroundtask.StatusCompleted
	task.ResultData = []byte("done")
	result, err := formatManagedAgentResult("worker", task, "")
	require.NoError(t, err)
	require.Equal(t, "done", result)

	for _, status := range []backgroundtask.Status{
		backgroundtask.StatusPending, backgroundtask.StatusRunning,
	} {
		task.Status = status
		result, err = formatManagedAgentResult("worker", task, "JSONL")
		require.NoError(t, err)
		require.Contains(t, result, "Agent running in background")
		require.Contains(t, result, "/tasks/output")
		require.Contains(t, result, "JSONL")
	}

	task.Status = backgroundtask.StatusWaitingInput
	result, err = formatManagedAgentResult("worker", task, "")
	require.NoError(t, err)
	require.Contains(t, result, "requires input")
	task.Status = backgroundtask.StatusSuspended
	result, err = formatManagedAgentResult("worker", task, "")
	require.NoError(t, err)
	require.Contains(t, result, "suspended")
	task.Status = backgroundtask.Status("unknown")
	_, err = formatManagedAgentResult("worker", task, "")
	require.ErrorContains(t, err, `unknown status "unknown"`)
}

func TestManagedEventReceiverTransformDetachesParentOnly(t *testing.T) {
	detached := make(chan struct{})
	var parentCalls, taskCalls int
	transform := managedEventReceiverTransform(
		detached, func(string) { taskCalls++ },
	)
	receivers := transform([]agenttool.EventReceiver[string]{
		func(string) { parentCalls++ },
	})
	require.Len(t, receivers, 2)
	receivers[0]("before")
	receivers[1]("before")
	close(detached)
	receivers[0]("after")
	receivers[1]("after")
	require.Equal(t, 1, parentCalls)
	require.Equal(t, 2, taskCalls)
	require.True(t, signalClosed(detached))
	require.False(t, signalClosed(make(chan struct{})))
}

func TestAgentEventFileReceiverFailurePaths(t *testing.T) {
	event := &adk.AgentEvent{
		AgentName: "worker",
		Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
			Message: schema.AssistantMessage("done", nil),
		}},
	}
	reportErr := errors.New("report failed")
	receiver := &agentEventFileReceiver[*schema.Message]{
		ctx: context.Background(),
		format: func(context.Context, string, *schema.Message) (string, error) {
			return "", errors.New("format failed")
		},
		onError: func(error) error { return reportErr },
	}
	receiver.receive(nil)
	require.False(t, receiver.failed)
	receiver.receive(event)
	require.True(t, receiver.failed)
	require.ErrorIs(t, receiver.reportErr, reportErr)
	receiver.fail(errors.New("ignored after failure"))
	require.ErrorIs(t, receiver.reportErr, reportErr)

	recordErr := errors.New("record failed")
	receiver = &agentEventFileReceiver[*schema.Message]{
		ctx: context.Background(),
		format: func(context.Context, string, *schema.Message) (string, error) {
			return "record", nil
		},
		onRecord: func([]byte) error { return recordErr },
	}
	receiver.receive(event)
	require.True(t, receiver.failed)
	require.ErrorIs(t, receiver.reportErr, recordErr)

	receiver = &agentEventFileReceiver[*schema.Message]{
		ctx: context.Background(),
		format: func(context.Context, string, *schema.Message) (string, error) {
			return "", nil
		},
	}
	receiver.fail(nil)
	require.False(t, receiver.failed)
	receiver.receive(event)
	require.False(t, receiver.failed)

	receiver = &agentEventFileReceiver[*schema.Message]{
		ctx: context.Background(),
		format: func(context.Context, string, *schema.Message) (string, error) {
			return "record", nil
		},
	}
	receiver.receive(event)
	require.False(t, receiver.failed)

	stream, streamWriter := schema.Pipe[*schema.Message](1)
	streamErr := errors.New("stream failed")
	streamWriter.Send(nil, streamErr)
	streamWriter.Close()
	receiver.receive(&adk.AgentEvent{
		Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
			IsStreaming: true, MessageStream: stream,
		}},
	})
	require.True(t, receiver.failed)
}

func TestSanitizedMessageValueAndTaskName(t *testing.T) {
	message := schema.AssistantMessage("done", nil)
	message.Extra = map[string]any{"private": true}
	sanitized := sanitizedMessageValue(message).(*schema.Message)
	require.Nil(t, sanitized.Extra)
	require.NotNil(t, message.Extra)
	var nilMessage *schema.Message
	require.Nil(t, sanitizedMessageValue(nilMessage))

	agentic := &schema.AgenticMessage{
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "done"}),
		},
	}
	agentic.Extra = map[string]any{"private": true}
	sanitizedAgentic := sanitizedMessageValue(agentic).(*schema.AgenticMessage)
	require.Nil(t, sanitizedAgentic.Extra)
	require.NotNil(t, agentic.Extra)

	require.Empty(t, NameFromTask(nil))
	require.Empty(t, NameFromTask(&backgroundtask.Task{}))
	require.Empty(t, NameFromTask(&backgroundtask.Task{Spec: backgroundtask.Spec{
		Kind: TaskKindSubagent, Payload: []byte(`{`),
	}}))
	require.Equal(t, "worker", NameFromTask(&backgroundtask.Task{
		Spec: backgroundtask.Spec{
			Kind:    TaskKindSubagent,
			Payload: []byte(`{"version":1,"subagent_name":"worker"}`),
		},
	}))

	require.Empty(t, reserveAgentOutput(context.Background(), nil, "/tasks"))
	require.Empty(t, reserveAgentOutput(
		context.Background(), filesystem.NewInMemoryBackend(), "",
	))
	require.NotEmpty(t, reserveAgentOutput(
		context.Background(), filesystem.NewInMemoryBackend(), "/tasks",
	))
}

func TestLocalAgentToolWritesEventTranscript(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, ctx)
	backend := filesystem.NewInMemoryBackend()
	agent := &mockAgent{name: "worker", desc: "local output"}
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Background: &BackgroundConfig{Local: &LocalBackgroundConfig{
			Runner: mustLocalRunner(t, manager), OutputStore: backend, OutputDir: "/tasks",
		}, TranscriptFormat: func(
			_ context.Context,
			agentName string,
			message *schema.Message,
		) (string, error) {
			return agentName + ": " + message.Content, nil
		}},
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx, `{"subagent_type":"worker","prompt":"work","description":"test"}`,
	)
	require.NoError(t, err)
	assert.Equal(t, "local output", result)
	task := terminalTask(t, manager)
	require.NotNil(t, task)
	assert.Equal(t, "parent-session", task.Spec.SessionID)
	assert.True(t, task.Spec.NotifySession)
	require.NotEmpty(t, task.Spec.OutputFile)
	content, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: task.Spec.OutputFile})
	require.NoError(t, err)
	assert.Equal(t, "worker: local output\n", content.Content)
	feed, err := manager.ListTaskEvents(ctx, &backgroundtask.ListTaskEventsRequest{
		TaskID: task.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, feed.Events, 1)
	require.NotNil(t, feed.Events[0])
	require.NotEmpty(t, feed.Events[0].EventID)
	assert.Equal(t, "worker: local output\n", string(feed.Events[0].Data))
}

func TestDurableAgentToolBackgroundSurvivesCaller(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	mgr := newTestManager(t, context.Background())
	agent := &mockAgent{name: "slow", run: func(context.Context, *adk.AgentInput) string {
		time.Sleep(30 * time.Millisecond)
		return "done"
	}}
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Background: durableBackground(t, mgr, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(ctx,
		`{"subagent_type":"slow","prompt":"work","description":"test","run_in_background":true}`)
	require.NoError(t, err)
	decoded := decodeDurableAgentToolResult(t, result)
	assert.Equal(t, backgroundtask.StatusRunning, decoded.Status)
	require.Eventually(t, func() bool {
		task := terminalTask(t, mgr)
		return task != nil && task.Status == backgroundtask.StatusCompleted
	}, time.Second, 10*time.Millisecond)
}

func TestDurableAgentToolReusesChildSessionAcrossTasks_BitsUT(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	var runs [][]string
	agent := &mockAgent{name: "worker", run: func(
		_ context.Context,
		input *adk.AgentInput,
	) string {
		contents := make([]string, 0, len(input.Messages))
		for _, message := range input.Messages {
			contents = append(contents, message.Content)
		}
		runs = append(runs, contents)
		return fmt.Sprintf("reply-%d", len(runs))
	}}
	middleware, err := New(ctx, &Config{
		SubAgents:  []adk.Agent{agent},
		Background: durableBackground(t, manager, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(
		ctx, &adk.ChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	invokable := runCtx.Tools[0].(tool.InvokableTool)

	firstRaw, err := invokable.InvokableRun(
		ctx,
		`{"subagent_type":"worker","prompt":"first","description":"first turn"}`,
	)
	require.NoError(t, err)
	first := decodeDurableAgentToolResult(t, firstRaw)
	require.Equal(t, backgroundtask.StatusCompleted, first.Status)
	require.Equal(t, "reply-1", first.Result)

	secondRaw, err := invokable.InvokableRun(
		ctx,
		fmt.Sprintf(
			`{"subagent_type":"worker","prompt":"second","description":"second turn","child_session_id":%q}`,
			first.ChildSessionID,
		),
	)
	require.NoError(t, err)
	second := decodeDurableAgentToolResult(t, secondRaw)
	require.Equal(t, backgroundtask.StatusCompleted, second.Status)
	require.Equal(t, "reply-2", second.Result)
	require.NotEqual(t, first.TaskID, second.TaskID)
	require.Equal(t, first.ChildSessionID, second.ChildSessionID)
	require.Equal(t, [][]string{
		{"first"},
		{"first", "reply-1", "second"},
	}, runs)
}

func TestDurableAgentRegistrationRejectsDuplicateExactIdentity(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t, ctx)
	first := &mockAgent{name: "worker", desc: "first"}
	second := &mockAgent{name: "worker", desc: "second"}
	background := durableBackground(t, mgr, first)
	_, err := New(ctx, &Config{
		SubAgents: []adk.Agent{first}, Background: background,
	})
	require.NoError(t, err)
	_, err = New(ctx, &Config{
		SubAgents: []adk.Agent{second}, Background: background,
	})
	assert.ErrorIs(t, err, backgroundtask.ErrAlreadyExists)
}

func TestDurableTaskProgressReadsSessionTranscript(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	agent := &mockAgent{name: "worker", desc: "durable output"}
	executor := durableExecutor(t)
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Background: &BackgroundConfig{Durable: &DurableBackgroundConfig{
			Manager: manager, Executors: executorsForTest(t, manager), Executor: executor,
		}},
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx, `{"subagent_type":"worker","prompt":"work","description":"test"}`,
	)
	require.NoError(t, err)
	decoded := decodeDurableAgentToolResult(t, result)
	assert.Equal(t, "durable output", decoded.Result)
	assert.Equal(t, backgroundtask.StatusCompleted, decoded.Status)
	task := terminalTask(t, manager)
	require.NotNil(t, task)
	assert.Empty(t, task.Spec.OutputFile)
	feed, err := manager.ListTaskEvents(ctx, &backgroundtask.ListTaskEventsRequest{TaskID: task.Spec.ID})
	require.NoError(t, err)
	assert.Empty(t, feed.Events)
	reader, err := NewDurableTaskProgressReader(
		executor, TranscriptFormat[*schema.Message](nil),
	)
	require.NoError(t, err)
	progress, err := reader.ReadProgress(ctx, task)
	require.NoError(t, err)
	assert.Contains(t, progress, `"agent_name":"worker"`)
	assert.Contains(t, progress, `"content":"durable output"`)
	assert.NotContains(t, progress, `"content":"work"`)
}

func TestDurableTaskProgressUsesSharedFormatter(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	agent := &mockAgent{name: "worker", desc: "durable output"}
	executor := durableExecutor(t)
	format := func(_ context.Context, agentName string, message *schema.Message) (string, error) {
		return agentName + ": " + message.Content, nil
	}
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Background: &BackgroundConfig{Durable: &DurableBackgroundConfig{
			Manager: manager, Executors: executorsForTest(t, manager), Executor: executor,
		}, TranscriptFormat: format},
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx, `{"subagent_type":"worker","prompt":"work","description":"test"}`,
	)
	require.NoError(t, err)
	decoded := decodeDurableAgentToolResult(t, result)
	assert.Equal(t, "durable output", decoded.Result)
	assert.Equal(t, backgroundtask.StatusCompleted, decoded.Status)
	task := terminalTask(t, manager)
	require.NotNil(t, task)
	assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
	assert.Equal(t, "durable output", string(task.ResultData))
	reader, err := NewDurableTaskProgressReader(
		executor, format,
	)
	require.NoError(t, err)
	progress, err := reader.ReadProgress(ctx, task)
	require.NoError(t, err)
	assert.Contains(t, progress, "worker: durable output")
	assert.NotContains(t, progress, "worker: work")
}

func TestDurableForegroundProjectionStopsAtBackgroundBoundary(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	agent := &mockAgent{name: "worker", desc: "done"}
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Background: durableBackground(t, manager, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	invokable := runCtx.Tools[0].(tool.InvokableTool)
	var calls int64
	receiver := agenttool.WithEventReceiverTransform(
		func(current []agenttool.EventReceiver[*adk.AgentEvent]) []agenttool.EventReceiver[*adk.AgentEvent] {
			return append(current, func(*adk.AgentEvent) { atomic.AddInt64(&calls, 1) })
		},
	)
	_, err = invokable.InvokableRun(
		ctx, `{"subagent_type":"worker","prompt":"work","description":"foreground"}`, receiver,
	)
	require.NoError(t, err)
	assert.Positive(t, atomic.LoadInt64(&calls))
	atomic.StoreInt64(&calls, 0)
	_, err = invokable.InvokableRun(
		ctx,
		`{"subagent_type":"worker","prompt":"work","description":"background","run_in_background":true}`,
		receiver,
	)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		task := terminalTask(t, manager)
		return task != nil && task.Spec.Description == "background" &&
			task.Status == backgroundtask.StatusCompleted
	}, time.Second, 10*time.Millisecond)
	assert.Zero(t, atomic.LoadInt64(&calls))
}

func TestDurableAgentToolRejectsInvocationScopedRunOptions(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	agent := &mockAgent{name: "worker", desc: "done"}
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Background: durableBackground(t, manager, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	invokable := runCtx.Tools[0].(tool.InvokableTool)

	_, err = invokable.InvokableRun(
		ctx,
		`{"subagent_type":"worker","prompt":"work","description":"foreground"}`,
		agenttool.WithInvocationOptions(
			"worker", []adk.AgentRunOption{adk.WithTimelineEvents()},
		),
	)
	require.ErrorContains(t, err, "configure RunOptionsFactories")
	pending, listErr := manager.ListPending(
		context.Background(),
		&backgroundtask.ListPendingRequest{ExecutorKeys: []string{durablesubagent.ExecutorKey}},
	)
	require.NoError(t, listErr)
	assert.Empty(t, pending.Tasks)
}

func TestDurableAgentToolUsesRegisteredRunOptionsFactory(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	var got string
	agent := &mockAgent{
		name: "worker", desc: "done",
		runOptions: func(options []adk.AgentRunOption) {
			got = adk.GetImplSpecificOptions[middlewareRunOptions](nil, options...).value
		},
	}
	executor := durableExecutor(t)
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Background: &BackgroundConfig{Durable: &DurableBackgroundConfig{
			Manager: manager, Executors: executorsForTest(t, manager), Executor: executor,
			RunOptionsFactories: map[string]durablesubagent.RunOptionsFactory{
				"worker": func() ([]adk.AgentRunOption, error) {
					return []adk.AgentRunOption{adk.WrapImplSpecificOptFn(
						func(options *middlewareRunOptions) {
							options.value = "registered"
						},
					)}, nil
				},
			},
		}},
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	_, err = runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx, `{"subagent_type":"worker","prompt":"work","description":"foreground"}`,
	)
	require.NoError(t, err)
	assert.Equal(t, "registered", got)
}

func TestDurableBlockingReceiverDoesNotBlockAutoBackgroundResponse(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	timeout := 20
	manager := newTestManager(t, context.Background())
	agent := &mockAgent{name: "worker", desc: "done"}
	background := durableBackground(t, manager, agent)
	background.Durable.ForegroundTimeoutMs = &timeout
	background.Durable.ShouldAutoBackground = func(context.Context, *backgroundtask.Task) bool { return true }
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Background: background,
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	invokable := runCtx.Tools[0].(tool.InvokableTool)
	receiverStarted := make(chan struct{})
	releaseReceiver := make(chan struct{})
	receiver := agenttool.WithEventReceiverTransform(
		func(current []agenttool.EventReceiver[*adk.AgentEvent]) []agenttool.EventReceiver[*adk.AgentEvent] {
			return append(current, func(*adk.AgentEvent) {
				close(receiverStarted)
				<-releaseReceiver
			})
		},
	)
	type invokeResult struct {
		output string
		err    error
	}
	invokeDone := make(chan invokeResult, 1)
	go func() {
		output, invokeErr := invokable.InvokableRun(
			ctx, `{"subagent_type":"worker","prompt":"work","description":"blocking"}`, receiver,
		)
		invokeDone <- invokeResult{output: output, err: invokeErr}
	}()
	<-receiverStarted
	select {
	case result := <-invokeDone:
		require.NoError(t, result.err)
		decoded := decodeDurableAgentToolResult(t, result.output)
		assert.Equal(t, backgroundtask.StatusRunning, decoded.Status)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("auto-background response waited for blocking receiver")
	}
	close(releaseReceiver)
	require.Eventually(t, func() bool {
		task := terminalTask(t, manager)
		return task != nil && task.Spec.Description == "blocking" &&
			task.Status == backgroundtask.StatusCompleted
	}, time.Second, 10*time.Millisecond)
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
