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
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	backgroundlocal "github.com/cloudwego/eino/adk/task/local"
	durablesubagent "github.com/cloudwego/eino/adk/task/subagent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*adk.InterruptInfo](
		"_eino_adk_subagent_test_interrupt_info",
	)
}

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *background.Config,
) *background.Manager {
	t.Helper()
	if config == nil {
		config = &background.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *background.TaskSnapshot) error { return nil }
	}
	manager, err := background.New(ctx, config)
	require.NoError(t, err)
	return manager
}

type mockAgent struct {
	name       string
	desc       string
	run        func(context.Context, *adk.AgentInput) string
	runOptions func([]adk.AgentRunOption)
}

type interruptingMockAgent struct {
	name string
}

type runtimeIdentity struct {
	taskID         string
	childSessionID string
}

type checkpointInterruptAgent struct {
	name        string
	identities  chan<- runtimeIdentity
	resumeInfos chan<- *adk.ResumeInfo
	runCalls    int64
	resumeCalls int64
}

type controllerToolModel struct {
	response *schema.Message
	inputs   chan<- []*schema.Message
}

type countingMailboxStore struct {
	*background.InMemoryStore
	created int64
}

func awaitMiddlewareValue[T any](
	t *testing.T,
	ctx context.Context,
	values <-chan T,
) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-ctx.Done():
		t.Fatalf("timed out waiting for middleware test value: %v", ctx.Err())
		var zero T
		return zero
	}
}

func (s *countingMailboxStore) Register(
	ctx context.Context,
	req *task.RegisterMailboxRequest,
) (*task.RegisterMailboxResult, error) {
	result, err := s.InMemoryStore.Register(ctx, req)
	if err == nil && result.Created {
		atomic.AddInt64(&s.created, 1)
	}
	return result, err
}

func (m *controllerToolModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	if m.inputs != nil {
		m.inputs <- append([]*schema.Message(nil), input...)
	}
	return m.response, nil
}

func (m *controllerToolModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *controllerToolModel) WithTools(
	[]*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (a *checkpointInterruptAgent) Name(context.Context) string { return a.name }

func (*checkpointInterruptAgent) Description(context.Context) string {
	return "checkpoint interrupt worker"
}

func (a *checkpointInterruptAgent) captureIdentity(ctx context.Context) {
	execution, executionOK := task.ExecutionContextFromContext(ctx)
	childSessionID, childOK := durablesubagent.ChildSessionID(ctx)
	if executionOK && childOK && a.identities != nil {
		a.identities <- runtimeIdentity{
			taskID: execution.TaskID, childSessionID: childSessionID,
		}
	}
}

func (a *checkpointInterruptAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	atomic.AddInt64(&a.runCalls, 1)
	a.captureIdentity(ctx)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.Interrupt(ctx, "approval required"))
	generator.Close()
	return iter
}

func (a *checkpointInterruptAgent) Resume(
	ctx context.Context,
	info *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	atomic.AddInt64(&a.resumeCalls, 1)
	a.captureIdentity(ctx)
	if a.resumeInfos != nil {
		copy := *info
		a.resumeInfos <- &copy
	}
	content, _ := info.ResumeData.(string)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage(content, nil),
		nil,
		schema.Assistant,
		a.name,
	))
	generator.Close()
	return iter
}

func (a *interruptingMockAgent) Name(context.Context) string { return a.name }

func (*interruptingMockAgent) Description(context.Context) string {
	return "interrupting worker"
}

func (a *interruptingMockAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.Interrupt(ctx, "approval required"))
	generator.Close()
	return iter
}

func (a *interruptingMockAgent) Resume(
	_ context.Context,
	_ *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("approved", nil),
		nil,
		schema.Assistant,
		a.name,
	))
	generator.Close()
	return iter
}

type middlewareRunOptions struct {
	value string
}

type runtimeBarrierFunc func(
	context.Context,
	*durablesubagent.CompletionContext[*schema.Message],
) (durablesubagent.CompletionAction, error)

func (f runtimeBarrierFunc) Check(
	ctx context.Context,
	input *durablesubagent.CompletionContext[*schema.Message],
) (durablesubagent.CompletionAction, error) {
	return f(ctx, input)
}

var testManagerStores sync.Map

func newTestManager(t testing.TB, ctx context.Context) *background.Manager {
	store := background.NewInMemoryStore(nil)
	manager := mustNewBackgroundManager(t, ctx, &background.Config{
		Tasks: store,
	})
	testManagerStores.Store(manager, store)
	return manager
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

func TestControllerAgentToolForeground(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, ctx)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	controller, err := durablesubagent.NewController(
		&durablesubagent.ControllerConfig[*schema.Message]{
			Manager: manager,
			Barrier: runtimeBarrierFunc(func(
				context.Context,
				*durablesubagent.CompletionContext[*schema.Message],
			) (durablesubagent.CompletionAction, error) {
				return durablesubagent.CompletionComplete, nil
			}),
			InputsToAgentInput: func(
				context.Context,
				[]*task.InputRecord,
			) (*adk.AgentInput, error) {
				return &adk.AgentInput{
					Messages: []*schema.Message{schema.UserMessage("event")},
				}, nil
			},
			SessionStore: sessionStore, CheckPointStore: sessionStore,
		},
	)
	require.NoError(t, err)
	agent := &mockAgent{name: "worker", desc: "runtime result"}
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Tasks: &TaskConfig{Durable: &DurableTaskConfig{
			Runtime: controller,
		}},
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(
		ctx,
		&adk.ChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx,
		`{"subagent_type":"worker","prompt":"work","description":"test"}`,
	)
	require.NoError(t, err)
	decoded := decodeDurableAgentToolResult(t, result)
	require.Equal(t, "runtime result", decoded.Result)
	require.NotEmpty(t, decoded.TaskID)
	require.NotEmpty(t, decoded.ChildSessionID)
	require.Equal(t, background.StatusCompleted, decoded.Status)
	secondRaw, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx,
		fmt.Sprintf(
			`{"subagent_type":"worker","prompt":"again","description":"test","child_session_id":%q}`,
			decoded.ChildSessionID,
		),
	)
	require.NoError(t, err)
	second := decodeDurableAgentToolResult(t, secondRaw)
	require.NotEqual(t, decoded.TaskID, second.TaskID)
	require.Equal(t, decoded.ChildSessionID, second.ChildSessionID)
}

func TestControllerAgentToolRunnerCheckpointResumeAfterControllerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lifecycleStore := &countingMailboxStore{
		InMemoryStore: background.NewInMemoryStore(nil),
	}
	runnerStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runtimeSessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	newManager := func() *background.Manager {
		return mustNewBackgroundManager(t, ctx, &background.Config{
			Tasks: lifecycleStore, TaskEvents: lifecycleStore,
		})
	}
	newRunner := func(
		manager *background.Manager,
		agent adk.Agent,
		chatModel model.ToolCallingChatModel,
	) *adk.Runner {
		controller, err := durablesubagent.NewController(
			&durablesubagent.ControllerConfig[*schema.Message]{
				Manager: manager,
				Barrier: runtimeBarrierFunc(func(
					context.Context,
					*durablesubagent.CompletionContext[*schema.Message],
				) (durablesubagent.CompletionAction, error) {
					return durablesubagent.CompletionComplete, nil
				}),
				InputsToAgentInput: func(
					context.Context,
					[]*task.InputRecord,
				) (*adk.AgentInput, error) {
					return &adk.AgentInput{
						Messages: []*schema.Message{schema.UserMessage("resume")},
					}, nil
				},
				SessionStore: runtimeSessionStore, CheckPointStore: runtimeSessionStore,
			},
		)
		require.NoError(t, err)
		middleware, err := New(ctx, &Config{
			SubAgents: []adk.Agent{agent},
			Tasks: &TaskConfig{Durable: &DurableTaskConfig{
				Runtime: controller,
			}},
		})
		require.NoError(t, err)
		root, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name: "root", Description: "root", Model: chatModel,
			Handlers: []adk.ChatModelAgentMiddleware{middleware},
		})
		require.NoError(t, err)
		return adk.NewRunner(ctx, adk.RunnerConfig{
			Agent: root, CheckPointStore: runnerStore,
			SessionID: "parent-session",
		})
	}

	const args = `{"subagent_type":"worker","prompt":"work","description":"approval"}`
	firstIdentities := make(chan runtimeIdentity, 1)
	firstAgent := &checkpointInterruptAgent{
		name: "worker", identities: firstIdentities,
	}
	manager1 := newManager()
	runner1 := newRunner(
		manager1,
		firstAgent,
		&controllerToolModel{response: schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-1", Type: "function",
			Function: schema.FunctionCall{Name: agentToolName, Arguments: args},
		}})},
	)
	const checkpointID = "controller-tool-interrupt"
	iter := runner1.Query(ctx, "delegate", adk.WithCheckPointID(checkpointID))
	var interrupt *adk.InterruptInfo
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			interrupt = event.Action.Interrupted
		}
	}
	require.NotNil(t, interrupt)
	require.NotEmpty(t, interrupt.InterruptContexts)
	original := awaitMiddlewareValue(t, ctx, firstIdentities)
	require.NotEmpty(t, original.taskID)
	require.NotEmpty(t, original.childSessionID)
	require.Equal(t, int64(1), atomic.LoadInt64(&firstAgent.runCalls))
	require.Zero(t, atomic.LoadInt64(&firstAgent.resumeCalls))
	checkpoint, exists, err := runnerStore.Get(ctx, checkpointID)
	require.NoError(t, err)
	require.True(t, exists)
	require.NotEmpty(t, checkpoint)

	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	require.NoError(t, manager1.Close(closeCtx))
	closeCancel()

	resumedIdentities := make(chan runtimeIdentity, 1)
	resumeInfos := make(chan *adk.ResumeInfo, 1)
	resumedAgent := &checkpointInterruptAgent{
		name: "worker", identities: resumedIdentities, resumeInfos: resumeInfos,
	}
	modelInputs := make(chan []*schema.Message, 1)
	manager2 := newManager()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), time.Second,
		)
		defer cleanupCancel()
		require.NoError(t, manager2.Close(cleanupCtx))
	})
	runner2 := newRunner(
		manager2,
		resumedAgent,
		&controllerToolModel{
			response: schema.AssistantMessage("parent complete", nil),
			inputs:   modelInputs,
		},
	)
	var interruptID string
	for _, interruptContext := range interrupt.InterruptContexts {
		if interruptContext.IsRootCause {
			interruptID = interruptContext.ID
			break
		}
	}
	require.NotEmpty(t, interruptID)
	resumed, err := runner2.ResumeWithParams(
		ctx,
		checkpointID,
		&adk.ResumeParams{Targets: map[string]any{interruptID: "approved"}},
	)
	require.NoError(t, err)
	var final string
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Output != nil && event.Output.MessageOutput != nil &&
			event.Output.MessageOutput.Message != nil {
			final = event.Output.MessageOutput.Message.Content
		}
	}
	require.Equal(t, "parent complete", final)
	resumedIdentity := awaitMiddlewareValue(t, ctx, resumedIdentities)
	require.Equal(t, original, resumedIdentity)
	require.Zero(t, atomic.LoadInt64(&resumedAgent.runCalls))
	require.Equal(t, int64(1), atomic.LoadInt64(&resumedAgent.resumeCalls))
	require.Equal(t, int64(1), atomic.LoadInt64(&lifecycleStore.created))
	resumeInfo := awaitMiddlewareValue(t, ctx, resumeInfos)
	require.True(t, resumeInfo.WasInterrupted)
	require.True(t, resumeInfo.IsResumeTarget)
	require.Equal(t, "approved", resumeInfo.ResumeData)

	inputs := awaitMiddlewareValue(t, ctx, modelInputs)
	require.NotEmpty(t, inputs)
	toolResult := inputs[len(inputs)-1]
	require.Equal(t, schema.Tool, toolResult.Role)
	require.Contains(t, toolResult.Content, original.taskID)
	require.Contains(t, toolResult.Content, original.childSessionID)
	require.Contains(t, toolResult.Content, "approved")

	mailbox, err := manager2.GetMailbox(ctx, original.taskID)
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, mailbox.State)
	require.Equal(t, original.childSessionID, mailbox.ChildSessionID)
	inputRecords, err := manager2.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: original.taskID,
	})
	require.NoError(t, err)
	require.Len(t, inputRecords.Inputs, 2)
	require.Equal(t, int64(2), inputRecords.LatestSequence)
	require.Equal(t, inputRecords.LatestSequence, inputRecords.ConsumedCursor)
	require.Equal(
		t,
		durablesubagent.ResumeInputKind,
		inputRecords.Inputs[1].Kind,
	)
	var resumeTargets map[string]any
	require.NoError(t, sonic.Unmarshal(inputRecords.Inputs[1].Data, &resumeTargets))
	require.Len(t, resumeTargets, 1)
	for targetID, resumeData := range resumeTargets {
		require.NotEmpty(t, targetID)
		require.Equal(t, "approved", resumeData)
	}
}

func durableBackground(t *testing.T, mgr *background.Manager, agents ...adk.Agent) *TaskConfig {
	t.Helper()
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	controller, err := durablesubagent.NewController(
		&durablesubagent.ControllerConfig[*schema.Message]{
			Manager: mgr,
			Barrier: runtimeBarrierFunc(func(
				context.Context,
				*durablesubagent.CompletionContext[*schema.Message],
			) (durablesubagent.CompletionAction, error) {
				return durablesubagent.CompletionComplete, nil
			}),
			InputsToAgentInput: func(
				context.Context,
				[]*task.InputRecord,
			) (*adk.AgentInput, error) {
				return &adk.AgentInput{
					Messages: []*schema.Message{schema.UserMessage("event")},
				}, nil
			},
			SessionStore: sessionStore, CheckPointStore: sessionStore,
		},
	)
	require.NoError(t, err)
	return &TaskConfig{
		Durable: &DurableTaskConfig{
			Runtime: controller,
		},
	}
}

func mustLocalRunner(t *testing.T, manager *background.Manager) *backgroundlocal.Runner {
	t.Helper()
	runner, err := backgroundlocal.New(&backgroundlocal.Config{
		Manager: manager,
	})
	require.NoError(t, err)
	return runner
}

func localBackground(t *testing.T, manager *background.Manager) *TaskConfig {
	return &TaskConfig{
		Local: &LocalTaskConfig{Runner: mustLocalRunner(t, manager)},
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

func terminalTask(t *testing.T, mgr *background.Manager) *background.TaskSnapshot {
	t.Helper()
	store, ok := testManagerStores.Load(mgr)
	require.True(t, ok, "test Manager Store is unavailable")
	outbox := store.(background.NotificationOutbox)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, err := outbox.Receive(context.Background(), &background.ReceiveNotificationsRequest{
			Limit: 100, LeaseDuration: time.Millisecond,
		})
		require.NoError(t, err)
		for i := len(result.Deliveries) - 1; i >= 0; i-- {
			record := result.Deliveries[i].Record
			task, getErr := mgr.Get(context.Background(), record.TaskID)
			require.NoError(t, getErr)
			if task.Status == background.StatusCompleted ||
				task.Status == background.StatusFailed ||
				task.Status == background.StatusCanceled {
				return task
			}
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

func waitTaskTerminalByID(t *testing.T, mgr *background.Manager, taskID string) *background.TaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, err := mgr.Get(context.Background(), taskID)
		require.NoError(t, err)
		if task.Status == background.StatusCompleted ||
			task.Status == background.StatusFailed ||
			task.Status == background.StatusCanceled {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not finish", taskID)
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
		SubAgents: []adk.Agent{agent},
		Tasks:     &TaskConfig{},
	})
	assert.Error(t, err)

	manager := newTestManager(t, context.Background())
	_, err = New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent},
		Tasks: &TaskConfig{
			Local:   &LocalTaskConfig{Runner: mustLocalRunner(t, manager)},
			Durable: &DurableTaskConfig{},
		},
	})
	require.ErrorContains(t, err, "exactly one")

	_, err = New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent},
		Tasks: &TaskConfig{
			Durable: &DurableTaskConfig{},
		},
	})
	require.ErrorContains(t, err, "Controller")

	_, err = New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent},
		Tasks: &TaskConfig{
			Local: &LocalTaskConfig{Runner: mustLocalRunner(t, manager)},
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

func TestOnlyDurableAgentToolExposesPersistentChildSession(t *testing.T) {
	agent := &mockAgent{name: "worker", desc: "does work"}
	localManager := newTestManager(t, context.Background())
	durableManager := newTestManager(t, context.Background())
	local, err := New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent}, Tasks: localBackground(t, localManager),
	})
	require.NoError(t, err)
	durable, err := New(context.Background(), &Config{
		SubAgents: []adk.Agent{agent}, Tasks: durableBackground(t, durableManager, agent),
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

func TestDurableAgentToolBackgroundPreservesParentContextValues(t *testing.T) {
	type traceKey struct{}
	const traceValue = "trace-123"

	parentCtx := context.WithValue(runnerEnvironmentContext(t), traceKey{}, traceValue)
	callCtx, cancelCall := context.WithCancel(parentCtx)
	defer cancelCall()
	manager := newTestManager(t, parentCtx)
	seenValue := make(chan any, 1)
	seenErr := make(chan error, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	agent := &mockAgent{name: "worker", run: func(ctx context.Context, _ *adk.AgentInput) string {
		seenValue <- ctx.Value(traceKey{})
		close(started)
		<-release
		seenErr <- ctx.Err()
		return "done"
	}}
	middleware, err := New(parentCtx, &Config{
		SubAgents: []adk.Agent{agent},
		Tasks:     durableBackground(t, manager, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(
		parentCtx,
		&adk.ChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	_, err = runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		callCtx,
		`{"subagent_type":"worker","prompt":"work","description":"test","run_in_background":true}`,
	)
	require.NoError(t, err)
	<-started
	cancelCall()
	close(release)
	task := terminalTask(t, manager)
	require.NotNil(t, task)
	require.Equal(t, background.StatusCompleted, task.Status)
	require.Equal(t, traceValue, <-seenValue)
	require.NoError(t, <-seenErr)
}

func TestFormatManagedAgentResultLifecycleStates(t *testing.T) {
	ctx := context.Background()
	manager := newTestManager(t, ctx)
	runner := mustLocalRunner(t, manager)
	t.Cleanup(func() {
		require.NoError(t, manager.Close(context.Background()))
	})
	for _, testCase := range []struct {
		name      string
		work      backgroundlocal.WorkFunc
		want      string
		wantError func(string) string
	}{
		{
			name: "foreground completed",
			work: func(context.Context, background.ExecutionRuntime) (string, error) {
				return "done", nil
			},
			want: "done",
		},
		{
			name: "foreground failed",
			work: func(context.Context, background.ExecutionRuntime) (string, error) {
				return "", errors.New("model failed")
			},
			wantError: func(id string) string {
				return fmt.Sprintf(`subagent "worker" execution %q failed: model failed`, id)
			},
		},
		{
			name: "foreground canceled",
			work: func(context.Context, background.ExecutionRuntime) (string, error) {
				return "", context.Canceled
			},
			wantError: func(id string) string {
				return fmt.Sprintf(`subagent "worker" execution %q was canceled: context canceled`, id)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runResult, err := runner.Run(
				ctx,
				&backgroundlocal.Input{Description: testCase.name},
				testCase.work,
			)
			require.NoError(t, err)
			got, err := formatManagedAgentResult("worker", runResult, "")
			if testCase.wantError != nil {
				require.Empty(t, got)
				require.EqualError(t, err, testCase.wantError(runResult.ID()))
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}

	for _, testCase := range []struct {
		status    background.Status
		format    string
		want      string
		wantError string
	}{
		{status: background.StatusCompleted, want: "done"},
		{
			status: background.StatusPending, format: "JSONL",
			want: "Agent running in background with ID: subagent_task. " +
				"Output is being written to: /tasks/output. " +
				"You will be notified when it completes. " +
				"To check interim output, use Read on that file path (JSONL).",
		},
		{
			status: background.StatusRunning, format: "JSONL",
			want: "Agent running in background with ID: subagent_task. " +
				"Output is being written to: /tasks/output. " +
				"You will be notified when it completes. " +
				"To check interim output, use Read on that file path (JSONL).",
		},
		{
			status: background.StatusWaitingInput,
			want: "Agent task subagent_task requires input. " +
				"Use task_output to inspect the request.",
		},
		{
			status: background.StatusSuspended,
			want:   "Agent task subagent_task is suspended.",
		},
		{
			status: background.StatusCanceled,
			wantError: `subagent "worker" task "subagent_task" ` +
				`(review implementation) was canceled`,
		},
		{
			status: background.StatusFailed,
			wantError: `subagent "worker" task "subagent_task" ` +
				`(review implementation) failed: model failed`,
		},
		{
			status:    background.Status("unknown"),
			wantError: `subagent "worker" task "subagent_task" has unknown status "unknown"`,
		},
	} {
		t.Run("task "+string(testCase.status), func(t *testing.T) {
			runResult := &background.TaskSnapshot{
				Spec: background.Spec{
					ID: "subagent_task", Description: "review implementation",
					OutputFile: "/tasks/output",
				},
				Status: testCase.status, ResultData: []byte("done"),
				ResultError: "model failed",
			}
			got, err := formatManagedAgentTaskResult(
				"worker",
				runResult,
				testCase.format,
			)
			if testCase.wantError != "" {
				require.Empty(t, got)
				require.EqualError(t, err, testCase.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}

	got, err := formatManagedAgentResult("worker", nil, "")
	require.Empty(t, got)
	require.EqualError(t, err, "subagent: invalid local run result")
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

type agentEventErrorWriter struct {
	err error
}

func (w agentEventErrorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type agentEventRuntimeStub struct {
	writer *agentEventTaskWriter
}

func (*agentEventRuntimeStub) Controls() <-chan background.ControlRequest {
	return make(chan background.ControlRequest)
}

func (r *agentEventRuntimeStub) NewTaskEventWriter(
	eventID string,
) (background.TaskEventScope, background.TaskEventWriter) {
	if eventID == "" {
		eventID = "generated"
	}
	scope := background.TaskEventScope{
		TaskID: "task", Attempt: 1, EventID: eventID,
	}
	r.writer.scope = scope
	return scope, r.writer
}

func (*agentEventRuntimeStub) ReportTranscriptFailure(context.Context, error) error {
	return nil
}

func (*agentEventRuntimeStub) ListInputs(
	context.Context,
	int64,
	int,
) (*task.ListInputsResult, error) {
	return &task.ListInputsResult{}, nil
}

func (*agentEventRuntimeStub) WaitInputs(
	context.Context,
	int64,
) (*task.ListInputsResult, error) {
	return &task.ListInputsResult{}, nil
}

func (*agentEventRuntimeStub) AdvanceInputCursor(context.Context, int64, int64) error {
	return nil
}

func (*agentEventRuntimeStub) CommitInput(context.Context, int64, int64, []byte) error {
	return nil
}

func (*agentEventRuntimeStub) CommitStart(context.Context, []byte) error {
	return nil
}

type agentEventTaskWriter struct {
	scope     background.TaskEventScope
	parts     []*background.TaskEventPartInput
	persisted map[string]*background.TaskEventPart
	appendErr error
}

func (w *agentEventTaskWriter) Append(
	_ context.Context,
	part *background.TaskEventPartInput,
) (*background.AppendTaskEventResult, error) {
	if w.appendErr != nil {
		return nil, w.appendErr
	}
	copy := *part
	copy.Data = append([]byte(nil), part.Data...)
	w.parts = append(w.parts, &copy)
	key := w.scope.EventID + "\x00" + copy.PartID
	if persisted := w.persisted[key]; persisted != nil {
		replayed := *persisted
		replayed.Data = append([]byte(nil), persisted.Data...)
		return &background.AppendTaskEventResult{Part: &replayed}, nil
	}
	if w.persisted == nil {
		w.persisted = make(map[string]*background.TaskEventPart)
	}
	persisted := &background.TaskEventPart{
		TaskID: w.scope.TaskID, EventID: w.scope.EventID,
		PartID: copy.PartID, Data: copy.Data, Final: copy.Final,
	}
	w.persisted[key] = persisted
	return &background.AppendTaskEventResult{
		Part:     persisted,
		Inserted: true,
	}, nil
}

func TestAgentTaskEventPersisterFormatterWriterAndEmptyRecord(t *testing.T) {
	event := &adk.AgentEvent{
		AgentName: "worker",
		Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
			Message: schema.AssistantMessage("done", nil),
		}},
	}
	input := &background.TaskEventEnvelope[*adk.AgentEvent, *schema.Message]{
		Event: event,
	}

	t.Run("formatter error", func(t *testing.T) {
		formatErr := errors.New("format failed")
		writer := &agentEventTaskWriter{}
		err := (agentTaskEventPersister[*schema.Message]{
			format: func(context.Context, string, *schema.Message) (string, error) {
				return "", formatErr
			},
		}).Persist(context.Background(), background.TaskEventScope{}, input, writer)
		require.ErrorIs(t, err, formatErr)
		require.Empty(t, writer.parts)
	})

	t.Run("writer error", func(t *testing.T) {
		writeErr := errors.New("append failed")
		writer := &agentEventTaskWriter{appendErr: writeErr}
		err := (agentTaskEventPersister[*schema.Message]{
			format: func(context.Context, string, *schema.Message) (string, error) {
				return "record", nil
			},
		}).Persist(context.Background(), background.TaskEventScope{}, input, writer)
		require.ErrorIs(t, err, writeErr)
		require.Empty(t, writer.parts)
	})

	t.Run("empty record", func(t *testing.T) {
		writer := &agentEventTaskWriter{}
		err := (agentTaskEventPersister[*schema.Message]{
			format: func(context.Context, string, *schema.Message) (string, error) {
				return "", nil
			},
		}).Persist(context.Background(), background.TaskEventScope{}, input, writer)
		require.NoError(t, err)
		require.Empty(t, writer.parts)
	})

	t.Run("record", func(t *testing.T) {
		writer := &agentEventTaskWriter{}
		err := (agentTaskEventPersister[*schema.Message]{
			format: func(context.Context, string, *schema.Message) (string, error) {
				return "record", nil
			},
		}).Persist(context.Background(), background.TaskEventScope{}, input, writer)
		require.NoError(t, err)
		require.Equal(t, []*background.TaskEventPartInput{{
			PartID: "event", Data: []byte("record\n"), Final: true,
		}}, writer.parts)
	})

	t.Run("non-message event", func(t *testing.T) {
		writer := &agentEventTaskWriter{}
		err := (agentTaskEventPersister[*schema.Message]{
			format: func(context.Context, string, *schema.Message) (string, error) {
				t.Fatal("formatter must not be called")
				return "", nil
			},
		}).Persist(
			context.Background(),
			background.TaskEventScope{},
			&background.TaskEventEnvelope[*adk.AgentEvent, *schema.Message]{
				Event: &adk.AgentEvent{AgentName: "worker"},
			},
			writer,
		)
		require.NoError(t, err)
		require.Empty(t, writer.parts)
	})
}

type capturingAgentEventPersister struct {
	event  *adk.AgentEvent
	chunks []string
}

type prefixThenStreamErrorPersister struct{}

func (prefixThenStreamErrorPersister) Persist(
	ctx context.Context,
	_ background.TaskEventScope,
	input *background.TaskEventEnvelope[*adk.AgentEvent, *schema.Message],
	writer background.TaskEventWriter,
) error {
	message, err := input.Stream.Recv()
	if err != nil {
		return err
	}
	if _, err = writer.Append(ctx, &background.TaskEventPartInput{
		PartID: "chunk-0", Data: []byte(message.Content),
	}); err != nil {
		return err
	}
	_, err = input.Stream.Recv()
	return err
}

func (p *capturingAgentEventPersister) Persist(
	ctx context.Context,
	_ background.TaskEventScope,
	input *background.TaskEventEnvelope[*adk.AgentEvent, *schema.Message],
	writer background.TaskEventWriter,
) error {
	p.event = input.Event
	if input.Stream != nil {
		for {
			message, err := input.Stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			p.chunks = append(p.chunks, message.Content)
		}
	}
	data := strings.Join(p.chunks, "")
	if data == "" {
		data = input.Event.AgentName
	}
	_, err := writer.Append(ctx, &background.TaskEventPartInput{
		PartID: "event", Data: []byte(data),
		Final: true,
	})
	return err
}

func TestAgentEventFileReceiverFailurePaths(t *testing.T) {
	event := &adk.AgentEvent{
		AgentName: "worker",
		Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
			Message: schema.AssistantMessage("done", nil),
		}},
	}
	reportErr := errors.New("report failed")
	receiver := &agentEventPersistenceReceiver[*schema.Message]{
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
	receiver = &agentEventPersistenceReceiver[*schema.Message]{
		ctx: context.Background(),
		format: func(context.Context, string, *schema.Message) (string, error) {
			return "record", nil
		},
		writer:  agentEventErrorWriter{err: recordErr},
		onError: func(err error) error { return err },
	}
	receiver.receive(event)
	require.True(t, receiver.failed)
	require.ErrorIs(t, receiver.reportErr, recordErr)

	receiver = &agentEventPersistenceReceiver[*schema.Message]{
		ctx: context.Background(),
		format: func(context.Context, string, *schema.Message) (string, error) {
			return "", nil
		},
	}
	receiver.fail(nil)
	require.False(t, receiver.failed)
	receiver.receive(event)
	require.False(t, receiver.failed)

	receiver = &agentEventPersistenceReceiver[*schema.Message]{
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

func TestAgentEventPersisterReceivesRawEventAndSeparateStream(t *testing.T) {
	stream, writer := schema.Pipe[*schema.Message](2)
	writer.Send(schema.AssistantMessage("hello ", nil), nil)
	writer.Send(schema.AssistantMessage("world", nil), nil)
	writer.Close()
	event := &adk.AgentEvent{
		AgentName: "worker",
		Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
			IsStreaming: true, MessageStream: stream,
		}},
		SessionEventVariant: &adk.SessionEventVariant[*schema.Message]{
			MessageStreamRef: &adk.MessageStreamRef{
				EventID: "stream-event",
				Kind:    adk.SessionEventMessage,
			},
		},
	}
	taskWriter := &agentEventTaskWriter{}
	persister := &capturingAgentEventPersister{}
	var transcript strings.Builder
	receiver := &agentEventPersistenceReceiver[*schema.Message]{
		ctx: context.Background(), writer: &transcript,
		format:    defaultTranscriptFormat[*schema.Message],
		runtime:   &agentEventRuntimeStub{writer: taskWriter},
		persister: persister,
	}

	receiver.receive(event)

	require.False(t, receiver.failed)
	require.NotNil(t, persister.event)
	require.Nil(t, persister.event.Output.MessageOutput.MessageStream)
	require.Equal(t, []string{"hello ", "world"}, persister.chunks)
	require.Equal(t, "stream-event", taskWriter.scope.EventID)
	require.Len(t, taskWriter.parts, 1)
	require.Equal(t, "hello world", string(taskWriter.parts[0].Data))
	require.Equal(t, "hello world", transcript.String())

	stream, writer = schema.Pipe[*schema.Message](2)
	writer.Send(schema.AssistantMessage("persist ", nil), nil)
	writer.Send(schema.AssistantMessage("only", nil), nil)
	writer.Close()
	persister = &capturingAgentEventPersister{}
	taskWriter = &agentEventTaskWriter{}
	receiver = &agentEventPersistenceReceiver[*schema.Message]{
		ctx:       context.Background(),
		format:    defaultTranscriptFormat[*schema.Message],
		runtime:   &agentEventRuntimeStub{writer: taskWriter},
		persister: persister,
	}
	receiver.receive(&adk.AgentEvent{
		AgentName: "worker",
		Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
			IsStreaming: true, MessageStream: stream,
		}},
	})
	require.False(t, receiver.failed)
	require.Equal(t, []string{"persist ", "only"}, persister.chunks)

	persister = &capturingAgentEventPersister{}
	taskWriter = &agentEventTaskWriter{}
	receiver = &agentEventPersistenceReceiver[*schema.Message]{
		ctx:       context.Background(),
		format:    defaultTranscriptFormat[*schema.Message],
		runtime:   &agentEventRuntimeStub{writer: taskWriter},
		persister: persister,
	}
	receiver.receive(&adk.AgentEvent{
		AgentName: "custom",
		Output: &adk.AgentOutput{
			CustomizedOutput: "state",
		},
	})
	require.False(t, receiver.failed)
	require.Equal(t, "custom", persister.event.AgentName)
	require.Empty(t, persister.chunks)
}

func TestAgentEventPersistenceReceiverMaterializesInsertedPrefixOnceOnStreamError(
	t *testing.T,
) {
	streamErr := errors.New("stream failed")
	newEvent := func() *adk.AgentEvent {
		stream, writer := schema.Pipe[*schema.Message](2)
		writer.Send(schema.AssistantMessage("persisted-prefix", nil), nil)
		writer.Send(nil, streamErr)
		writer.Close()
		return &adk.AgentEvent{
			AgentName: "worker",
			Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
				IsStreaming: true, MessageStream: stream,
			}},
			SessionEventVariant: &adk.SessionEventVariant[*schema.Message]{
				MessageStreamRef: &adk.MessageStreamRef{
					EventID: "stream-error", Kind: adk.SessionEventMessage,
				},
			},
		}
	}
	taskWriter := &agentEventTaskWriter{}
	var transcript strings.Builder
	newReceiver := func() *agentEventPersistenceReceiver[*schema.Message] {
		return &agentEventPersistenceReceiver[*schema.Message]{
			ctx: context.Background(), writer: &transcript,
			runtime:   &agentEventRuntimeStub{writer: taskWriter},
			persister: prefixThenStreamErrorPersister{},
		}
	}

	first := newReceiver()
	first.receive(newEvent())
	require.True(t, first.failed)
	require.ErrorIs(t, first.reportErr, streamErr)
	require.Equal(t, "persisted-prefix", transcript.String())

	replayed := newReceiver()
	replayed.receive(newEvent())
	require.True(t, replayed.failed)
	require.ErrorIs(t, replayed.reportErr, streamErr)
	require.Equal(t, "persisted-prefix", transcript.String())
	require.Len(t, taskWriter.parts, 2)
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
	require.Empty(t, NameFromTask(&background.TaskSnapshot{}))
	require.Empty(t, NameFromTask(&background.TaskSnapshot{Spec: background.Spec{
		Kind: TaskKindSubagent, Payload: []byte(`{`),
	}}))
	require.Equal(t, "worker", NameFromTask(&background.TaskSnapshot{
		Spec: background.Spec{
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
		Tasks: &TaskConfig{Local: &LocalTaskConfig{
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
		ctx, `{"subagent_type":"worker","prompt":"work","description":"test","run_in_background":true}`,
	)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")
	task := terminalTask(t, manager)
	require.NotNil(t, task)
	assert.Equal(t, "parent-session", task.Spec.RootSessionID)
	assert.True(t, task.Spec.NotifySession)
	require.NotEmpty(t, task.Spec.OutputFile)
	content, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: task.Spec.OutputFile})
	require.NoError(t, err)
	assert.Equal(t, "worker: local output\n", content.Content)
	feed, err := manager.ListTaskEvents(ctx, &background.ListTaskEventsRequest{
		TaskID: task.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, feed.Parts, 1)
	require.NotNil(t, feed.Parts[0])
	require.NotEmpty(t, feed.Parts[0].EventID)
	assert.Equal(t, "worker: local output\n", string(feed.Parts[0].Data))
}

func TestDurableAgentToolBackgroundSurvivesCaller(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	mgr := newTestManager(t, context.Background())
	agent := &mockAgent{name: "slow", run: func(context.Context, *adk.AgentInput) string {
		time.Sleep(30 * time.Millisecond)
		return "done"
	}}
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Tasks: durableBackground(t, mgr, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(ctx,
		`{"subagent_type":"slow","prompt":"work","description":"test","run_in_background":true}`)
	require.NoError(t, err)
	decoded := decodeDurableAgentToolResult(t, result)
	assert.Contains(t, []background.Status{
		background.StatusPending,
		background.StatusRunning,
	}, decoded.Status)
	require.Eventually(t, func() bool {
		task := terminalTask(t, mgr)
		return task != nil && task.Status == background.StatusCompleted
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
		SubAgents: []adk.Agent{agent},
		Tasks:     durableBackground(t, manager, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(
		ctx, &adk.ChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	invokable := runCtx.Tools[0].(tool.InvokableTool)

	firstRaw, err := invokable.InvokableRun(
		ctx,
		`{"subagent_type":"worker","prompt":"first","description":"first turn","run_in_background":true}`,
	)
	require.NoError(t, err)
	first := decodeDurableAgentToolResult(t, firstRaw)
	require.Contains(t, []background.Status{
		background.StatusPending,
		background.StatusRunning,
	}, first.Status)
	firstTask := waitTaskTerminalByID(t, manager, first.TaskID)
	require.Contains(t, string(firstTask.ResultData), `"content":"reply-1"`)

	secondRaw, err := invokable.InvokableRun(
		ctx,
		fmt.Sprintf(
			`{"subagent_type":"worker","prompt":"second","description":"second turn","child_session_id":%q,"run_in_background":true}`,
			first.ChildSessionID,
		),
	)
	require.NoError(t, err)
	second := decodeDurableAgentToolResult(t, secondRaw)
	require.Contains(t, []background.Status{
		background.StatusPending,
		background.StatusRunning,
	}, second.Status)
	secondTask := waitTaskTerminalByID(t, manager, second.TaskID)
	require.Contains(t, string(secondTask.ResultData), `"content":"reply-2"`)
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
	backgroundConfig := durableBackground(t, mgr, first)
	_, err := New(ctx, &Config{
		SubAgents: []adk.Agent{first}, Tasks: backgroundConfig,
	})
	require.NoError(t, err)
	_, err = New(ctx, &Config{
		SubAgents: []adk.Agent{second}, Tasks: backgroundConfig,
	})
	assert.ErrorIs(t, err, background.ErrAlreadyExists)
}

func TestDurableTaskProgressReadsSessionTranscript(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	agent := &mockAgent{name: "worker", desc: "durable output"}
	tasks := durableBackground(t, manager, agent)
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Tasks:     tasks,
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx, `{"subagent_type":"worker","prompt":"work","description":"test","run_in_background":true}`,
	)
	require.NoError(t, err)
	decoded := decodeDurableAgentToolResult(t, result)
	assert.Contains(t, []background.Status{
		background.StatusPending,
		background.StatusRunning,
	}, decoded.Status)
	task := waitTaskTerminalByID(t, manager, decoded.TaskID)
	assert.Empty(t, task.Spec.OutputFile)
	feed, err := manager.ListTaskEvents(ctx, &background.ListTaskEventsRequest{TaskID: task.Spec.ID})
	require.NoError(t, err)
	assert.Empty(t, feed.Parts)
	reader, err := NewDurableProgressReader(
		tasks.Durable.Runtime, TranscriptFormat[*schema.Message](nil),
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
	tasks := durableBackground(t, manager, agent)
	format := func(_ context.Context, agentName string, message *schema.Message) (string, error) {
		return agentName + ": " + message.Content, nil
	}
	tasks.TranscriptFormat = format
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Tasks:     tasks,
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx, `{"subagent_type":"worker","prompt":"work","description":"test","run_in_background":true}`,
	)
	require.NoError(t, err)
	decoded := decodeDurableAgentToolResult(t, result)
	assert.Contains(t, []background.Status{
		background.StatusPending,
		background.StatusRunning,
	}, decoded.Status)
	task := waitTaskTerminalByID(t, manager, decoded.TaskID)
	assert.Equal(t, background.StatusCompleted, task.Status)
	assert.Contains(t, string(task.ResultData), `"content":"durable output"`)
	reader, err := NewDurableProgressReader(
		tasks.Durable.Runtime, format,
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
		SubAgents: []adk.Agent{agent}, Tasks: durableBackground(t, manager, agent),
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
	assert.Equal(t, int64(1), atomic.LoadInt64(&calls))
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
			task.Status == background.StatusCompleted
	}, time.Second, 10*time.Millisecond)
	assert.Zero(t, atomic.LoadInt64(&calls))
}

func TestDurableAgentToolRejectsInvocationScopedRunOptions(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	agent := &mockAgent{name: "worker", desc: "done"}
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Tasks: durableBackground(t, manager, agent),
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	invokable := runCtx.Tools[0].(tool.InvokableTool)

	_, err = invokable.InvokableRun(
		ctx,
		`{"subagent_type":"worker","prompt":"work","description":"foreground","run_in_background":true}`,
		agenttool.WithInvocationOptions(
			"worker", []adk.AgentRunOption{adk.WithTimelineEvents()},
		),
	)
	require.ErrorContains(t, err, "configure RunOptionsFactories")
	pending, listErr := manager.ListPending(
		context.Background(),
		&background.ListPendingRequest{ExecutorKeys: []string{durablesubagent.ExecutorKey}},
	)
	require.NoError(t, listErr)
	assert.Empty(t, pending.Tasks)
}

func TestDurableAgentToolUsesRegisteredRunOptionsFactory(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	var got atomic.Value
	agent := &mockAgent{
		name: "worker", desc: "done",
		runOptions: func(options []adk.AgentRunOption) {
			got.Store(adk.GetImplSpecificOptions[middlewareRunOptions](nil, options...).value)
		},
	}
	tasks := durableBackground(t, manager, agent)
	tasks.Durable.RunOptionsFactories = map[string]durablesubagent.RunOptionsFactory{
		"worker": func() ([]adk.AgentRunOption, error) {
			return []adk.AgentRunOption{adk.WrapImplSpecificOptFn(
				func(options *middlewareRunOptions) {
					options.value = "registered"
				},
			)}, nil
		},
	}
	middleware, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent},
		Tasks:     tasks,
	})
	require.NoError(t, err)
	_, runCtx, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	_, err = runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		ctx, `{"subagent_type":"worker","prompt":"work","description":"foreground","run_in_background":true}`,
	)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		value, ok := got.Load().(string)
		return ok && value == "registered"
	}, time.Second, time.Millisecond)
}

func TestDurableTaskConfigRequiresController(t *testing.T) {
	ctx := runnerEnvironmentContext(t)
	manager := newTestManager(t, context.Background())
	agent := &mockAgent{name: "worker", desc: "done"}
	background := durableBackground(t, manager, agent)
	background.Durable.Runtime = nil
	_, err := New(ctx, &Config{
		SubAgents: []adk.Agent{agent}, Tasks: background,
	})
	require.ErrorContains(t, err, "durable Controller is required")
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
