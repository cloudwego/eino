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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
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

type resumableTestAgent struct {
	name         string
	events       []*adk.AgentEvent
	eventFactory func(context.Context) *adk.AgentEvent
}

type interruptThenCompleteAgent struct {
	name string
}

type resumeContextCaptureAgent struct {
	name       string
	resumeCtxs chan context.Context
}

type cancelThenMessageAgent struct {
	name    string
	started chan struct{}
	release chan struct{}
}

type contextCaptureAgent struct {
	name     string
	contexts chan context.Context
	release  chan struct{}
}

type drainTimeoutModel struct {
	started chan struct{}
	calls   int32
}

type drainTimeoutStreamingModel struct {
	started chan struct{}
	release chan struct{}
	calls   int32
}

func (m *drainTimeoutStreamingModel) Generate(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	if atomic.AddInt32(&m.calls, 1) == 1 {
		return schema.AssistantMessage("unreachable", nil), nil
	}
	return schema.AssistantMessage("resumed", nil), nil
}

func (m *drainTimeoutStreamingModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if atomic.LoadInt32(&m.calls) > 0 {
		message, err := m.Generate(ctx, input, options...)
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]*schema.Message{message}), nil
	}
	atomic.AddInt32(&m.calls, 1)
	reader, writer := schema.Pipe[*schema.Message](1)
	close(m.started)
	go func() {
		<-m.release
		writer.Close()
	}()
	return reader, nil
}

func (m *drainTimeoutModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	if atomic.AddInt32(&m.calls, 1) == 1 {
		close(m.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return schema.AssistantMessage("resumed", nil), nil
}

func (m *drainTimeoutModel) Stream(
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

func (*drainTimeoutModel) BindTools([]*schema.ToolInfo) error { return nil }

type drainTimeoutToolModel struct {
	calls int32
}

func (m *drainTimeoutToolModel) Generate(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	if atomic.AddInt32(&m.calls, 1) == 1 {
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "wait",
					Arguments: `{"input":"work"}`,
				},
			}},
		}, nil
	}
	return schema.AssistantMessage("resumed", nil), nil
}

func (m *drainTimeoutToolModel) Stream(
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

func (*drainTimeoutToolModel) BindTools([]*schema.ToolInfo) error { return nil }

type drainTimeoutTool struct {
	started chan struct{}
	calls   int32
}

type drainTimeoutStreamableTool struct {
	started chan struct{}
	release chan struct{}
	calls   int32
}

func (*drainTimeoutTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "wait",
		Desc: "waits for cancellation",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String},
		}),
	}, nil
}

func (t *drainTimeoutTool) InvokableRun(
	ctx context.Context,
	_ string,
	_ ...componenttool.Option,
) (string, error) {
	if atomic.AddInt32(&t.calls, 1) == 1 {
		close(t.started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "resumed tool", nil
}

func (t *drainTimeoutStreamableTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "wait",
		Desc: "streams until cancellation",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String},
		}),
	}, nil
}

func (t *drainTimeoutStreamableTool) StreamableRun(
	context.Context,
	string,
	...componenttool.Option,
) (*schema.StreamReader[string], error) {
	if atomic.AddInt32(&t.calls, 1) > 1 {
		return schema.StreamReaderFromArray([]string{"resumed tool"}), nil
	}
	reader, writer := schema.Pipe[string](1)
	go func() {
		defer writer.Close()
		if writer.Send("partial-1", nil) {
			return
		}
		if writer.Send("partial-2", nil) {
			return
		}
		close(t.started)
		<-t.release
	}()
	return reader, nil
}

type historyCaptureAgent struct {
	name string
	runs [][]string
}

func (a *historyCaptureAgent) Name(context.Context) string        { return a.name }
func (a *historyCaptureAgent) Description(context.Context) string { return "capture history" }
func (a *historyCaptureAgent) Run(
	_ context.Context,
	input *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	contents := make([]string, 0, len(input.Messages))
	for _, message := range input.Messages {
		contents = append(contents, message.Content)
	}
	a.runs = append(a.runs, contents)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage(
			fmt.Sprintf("reply-%d", len(a.runs)), nil,
		),
		nil,
		schema.Assistant,
		a.name,
	))
	generator.Close()
	return iter
}
func (a *historyCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func (a *contextCaptureAgent) Name(context.Context) string        { return a.name }
func (a *contextCaptureAgent) Description(context.Context) string { return "capture context" }
func (a *contextCaptureAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	a.contexts <- ctx
	go func() {
		<-a.release
		generator.Close()
	}()
	return iter
}
func (a *contextCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

type registrationRunOptions struct {
	value string
}

type optionCaptureAgent struct {
	name string
	seen []string
}

func (a *optionCaptureAgent) Name(context.Context) string        { return a.name }
func (a *optionCaptureAgent) Description(context.Context) string { return "option capture" }
func (a *optionCaptureAgent) Run(
	_ context.Context,
	_ *adk.AgentInput,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	resolved := adk.GetImplSpecificOptions[registrationRunOptions](nil, options...)
	a.seen = append(a.seen, resolved.value)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("done", nil), nil, schema.Assistant, a.name,
	))
	generator.Close()
	return iter
}
func (a *optionCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func (a *cancelThenMessageAgent) Name(context.Context) string        { return a.name }
func (a *cancelThenMessageAgent) Description(context.Context) string { return "cancel then message" }
func (a *cancelThenMessageAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		close(a.started)
		<-a.release
		generator.Send(adk.EventFromMessage(
			schema.AssistantMessage("late completion", nil), nil, schema.Assistant, a.name,
		))
		generator.Close()
	}()
	return iter
}
func (a *cancelThenMessageAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func (a *interruptThenCompleteAgent) Name(context.Context) string { return a.name }
func (a *interruptThenCompleteAgent) Description(context.Context) string {
	return "interrupt then complete"
}
func (a *interruptThenCompleteAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("before interrupt", nil), nil, schema.Assistant, a.name,
	))
	generator.Send(adk.Interrupt(ctx, "approve"))
	generator.Close()
	return iter
}
func (a *interruptThenCompleteAgent) Resume(
	_ context.Context,
	_ *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("approved", nil), nil, schema.Assistant, a.name,
	))
	generator.Close()
	return iter
}

func (a *resumeContextCaptureAgent) Name(context.Context) string { return a.name }
func (a *resumeContextCaptureAgent) Description(context.Context) string {
	return "capture resume context"
}
func (a *resumeContextCaptureAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.Interrupt(ctx, "approve"))
	generator.Close()
	return iter
}
func (a *resumeContextCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.resumeCtxs <- ctx
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("resumed", nil), nil, schema.Assistant, a.name,
	))
	generator.Close()
	return iter
}

func (a *resumableTestAgent) Name(context.Context) string        { return a.name }
func (a *resumableTestAgent) Description(context.Context) string { return "test agent" }
func (a *resumableTestAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	if len(a.events) == 0 {
		if a.eventFactory != nil {
			generator.Send(a.eventFactory(ctx))
		} else {
			generator.Send(adk.EventFromMessage(
				schema.AssistantMessage("done", nil), nil, schema.Assistant, a.name,
			))
		}
	}
	for _, event := range a.events {
		generator.Send(event)
	}
	generator.Close()
	return iter
}
func (a *resumableTestAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func newTestExecutor(
	t *testing.T,
	store *adksession.InMemoryStore[*schema.Message],
) *Executor[*schema.Message] {
	t.Helper()
	if store == nil {
		store = adksession.NewInMemoryStore[*schema.Message](nil)
	}
	executor, err := NewExecutor(&ExecutorConfig[*schema.Message]{
		SessionStore: store, CheckPointStore: store,
	})
	require.NoError(t, err)
	return executor
}

type subagentContextSnapshotKey struct{}

type subagentContextSnapshotter struct{}

func (subagentContextSnapshotter) CaptureContext(ctx context.Context) ([]byte, error) {
	value, _ := ctx.Value(subagentContextSnapshotKey{}).(string)
	if value == "" {
		return nil, nil
	}
	return []byte(value), nil
}

func (subagentContextSnapshotter) RestoreContext(ctx context.Context, snapshot []byte) (context.Context, error) {
	return context.WithValue(ctx, subagentContextSnapshotKey{}, string(snapshot)), nil
}

func textInput(query string) *adk.AgentInput {
	return &adk.AgentInput{Messages: []*schema.Message{schema.UserMessage(query)}}
}

func executeDrainTimeout(
	t *testing.T,
	agent adk.ResumableAgent,
	waitUntilRunning <-chan struct{},
) (*backgroundtask.Task, *adksession.InMemoryStore[*schema.Message]) {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	executor, err := NewExecutor(&ExecutorConfig[*schema.Message]{
		SessionStore:       store,
		CheckPointStore:    store,
		DrainCancelTimeout: 20 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	registry := backgroundtask.NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: registry,
	})
	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker",
		Input: &adk.AgentInput{
			Messages:        []*schema.Message{schema.UserMessage("work")},
			EnableStreaming: true,
		},
		SessionID: "parent",
	})
	require.NoError(t, err)

	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	select {
	case <-waitUntilRunning:
	case <-time.After(time.Second):
		t.Fatal("sub-agent did not reach the blocked operation")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Close(closeCtx))
	require.NoError(t, <-executeDone)

	suspended, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusSuspended, suspended.Status)
	_, exists, err := store.Get(context.Background(), checkpointID(task.Spec.ID))
	require.NoError(t, err)
	require.True(t, exists)
	return task, store
}

func requireDrainCheckpointResumes(
	t *testing.T,
	task *backgroundtask.Task,
	store *adksession.InMemoryStore[*schema.Message],
	agent adk.ResumableAgent,
) {
	t.Helper()
	childSessionID, err := ChildSessionIDFromTask(task)
	require.NoError(t, err)
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: store,
		SessionID:       childSessionID,
		SessionStore:    store,
	})
	iter, err := runner.Resume(context.Background(), checkpointID(task.Spec.ID))
	require.NoError(t, err)
	var final string
	for {
		event, open := iter.Next()
		if !open {
			break
		}
		require.NoError(t, event.Err)
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, messageErr := event.Output.MessageOutput.GetMessage()
		require.NoError(t, messageErr)
		final = message.Content
	}
	require.Equal(t, "resumed", final)
}

func TestNewExecutorRequiresDependencies_BitsUT(t *testing.T) {
	_, err := NewExecutor[*schema.Message](nil)
	require.Error(t, err)
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	_, err = NewExecutor(&ExecutorConfig[*schema.Message]{SessionStore: store})
	require.Error(t, err)
	_, err = NewExecutor(&ExecutorConfig[*schema.Message]{
		SessionStore: store,
		SessionStoreFactory: func(
			context.Context,
			*backgroundtask.Task,
		) (adk.SessionEventStore[*schema.Message], error) {
			return store, nil
		},
		CheckPointStore: store,
	})
	require.Error(t, err)
	var executor *Executor[*schema.Message]
	_, err = executor.ReadProgress(
		context.Background(),
		&backgroundtask.Task{Spec: backgroundtask.Spec{ExecutorKey: ExecutorKey}},
		func(context.Context, string, *schema.Message) (string, error) { return "", nil },
	)
	require.Error(t, err)
	err = executor.ValidateExecution(
		context.Background(),
		&backgroundtask.Task{},
	)
	require.ErrorContains(t, err, "dependencies are unavailable")
	executor, err = NewExecutor(&ExecutorConfig[*schema.Message]{
		SessionStore: store, CheckPointStore: store,
	})
	require.NoError(t, err)
	err = executor.ValidateExecution(context.Background(), nil)
	require.ErrorContains(t, err, "task is required")
}

func TestAttack_FactoryBackedExecutorExecutesAndReadsProgress(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	var seenTask *backgroundtask.Task
	factoryCalls := 0
	executor, err := NewExecutor(&ExecutorConfig[*schema.Message]{
		SessionStoreFactory: func(
			_ context.Context,
			task *backgroundtask.Task,
		) (adk.SessionEventStore[*schema.Message], error) {
			seenTask = task
			factoryCalls++
			return store, nil
		},
		CheckPointStore: store,
	})
	require.NoError(t, err)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&backgroundtask.Config{Executors: executors},
	)
	defer manager.Close(context.Background())
	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("work"), SessionID: "parent",
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	require.NotNil(t, seenTask)
	require.Equal(t, task.Spec.ID, seenTask.Spec.ID)
	require.Equal(t, int64(1), seenTask.Attempt)
	completed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	progress, err := executor.ReadProgress(
		context.Background(),
		completed,
		func(
			_ context.Context,
			_ string,
			message *schema.Message,
		) (string, error) {
			return message.Content, nil
		},
	)
	require.NoError(t, err)
	require.Contains(t, progress, "done")
	require.Equal(t, 2, factoryCalls)
}

func TestSessionHelpersHandleEmptyProgressAndMergeTaskMetadata_BitsUT(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	var eventStore adk.SessionEventStore[*schema.Message] = store
	eventID, err := firstTaskMessageEventID(
		context.Background(),
		eventStore,
		"empty-session",
		"task",
	)
	require.NoError(t, err)
	require.Empty(t, eventID)

	baseExtra := map[string]any{
		"application":       "value",
		taskIDEventExtraKey: "caller-value",
	}
	executor, err := NewExecutor(&ExecutorConfig[*schema.Message]{
		SessionStore:    store,
		CheckPointStore: store,
		SessionConfig: &adk.SessionConfig[*schema.Message]{
			EventExtraProvider: func(
				context.Context,
				*adk.SessionEvent[*schema.Message],
			) (map[string]any, error) {
				return baseExtra, nil
			},
		},
	})
	require.NoError(t, err)
	config := executor.sessionConfigForTask("task")
	extra, err := config.EventExtraProvider(
		context.Background(),
		&adk.SessionEvent[*schema.Message]{},
	)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"application":       "value",
		taskIDEventExtraKey: "task",
	}, extra)
	require.Equal(t, "caller-value", baseExtra[taskIDEventExtraKey])

	_, err = ChildSessionIDFromTask(nil)
	require.ErrorContains(t, err, "task is required")
}

func TestExecutorRegistersAgentsByStableName_BitsUT(t *testing.T) {
	executor := newTestExecutor(t, nil)
	agent := &resumableTestAgent{name: "worker"}
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{Agent: agent}))
	assert.ErrorIs(
		t,
		executor.Register("worker", &AgentRegistration[*schema.Message]{Agent: agent}),
		backgroundtask.ErrAlreadyExists,
	)

	resolved, err := executor.resolveAgent("worker")
	require.NoError(t, err)
	assert.Same(t, agent, resolved)

	_, err = executor.resolveAgent("other")
	require.Error(t, err)
}

func TestExecutorRegistryListsExecutorKey_BitsUT(t *testing.T) {
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(newTestExecutor(t, nil)))
	assert.Equal(t, []string{ExecutorKey}, executors.Keys())
}

func resumeFixture(
	t *testing.T,
	targets ...string,
) (*Executor[*schema.Message], backgroundtask.Spec, []byte) {
	t.Helper()
	executor := newTestExecutor(t, nil)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	state, err := json.Marshal(checkpointState{
		TargetIDs: targets, Sequence: 1,
	})
	require.NoError(t, err)
	input, err := encodeTypedInput(textInput("query"))
	require.NoError(t, err)
	payload, err := json.Marshal(taskPayload{
		Version: payloadVersion, SubAgentName: "worker", Input: input,
		ChildSessionID: "42",
	})
	require.NoError(t, err)
	return executor, backgroundtask.Spec{
		ID: "task", ExecutorKey: ExecutorKey, Kind: "subagent",
		Payload: payload, SessionID: "parent",
	}, state
}

func TestExecutorValidatesResumeTargets_BitsUT(t *testing.T) {
	t.Run("exact target", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, "approval")
		result, err := executor.validateResume(
			spec, checkpoint, []byte(`{"approval":{"approved":true}}`),
		)
		require.NoError(t, err)
		assert.JSONEq(t, `{"approval":{"approved":true}}`, string(result))
	})

	t.Run("unknown target", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, "approval")
		_, err := executor.validateResume(
			spec, checkpoint, []byte(`{"other":"value"}`),
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not interrupted")
	})

	t.Run("empty resume uses implicit resume all", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, "approval")
		result, err := executor.validateResume(spec, checkpoint, nil)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("non-object resume data is rejected", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, "approval")
		_, err := executor.validateResume(spec, checkpoint, []byte("continue"))
		require.ErrorContains(t, err, "resume targets are invalid")
	})

	t.Run("checkpoint schema must be valid", func(t *testing.T) {
		executor, spec, _ := resumeFixture(t, "approval")
		_, err := executor.validateResume(spec, []byte("invalid"), nil)
		require.Error(t, err)

		checkpoint, marshalErr := json.Marshal(checkpointState{})
		require.NoError(t, marshalErr)
		_, err = executor.validateResume(spec, checkpoint, nil)
		require.Error(t, err)
	})
}

func TestExecutorValidatesRecoveryCheckpoint_BitsUT(t *testing.T) {
	executor, spec, _ := resumeFixture(t, "approval")
	for _, testCase := range []struct {
		name       string
		checkpoint []byte
		errorText  string
	}{
		{name: "missing", errorText: "compatible checkpoint is required"},
		{name: "malformed", checkpoint: []byte("invalid"), errorText: "checkpoint state does not match task"},
		{name: "invalid state", checkpoint: []byte(`{"sequence":0}`), errorText: "checkpoint state does not match task"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := executor.Execute(
				context.Background(),
				&backgroundtask.Task{Spec: spec, Attempt: 2, Checkpoint: testCase.checkpoint},
				nil,
			)
			require.ErrorContains(t, err, testCase.errorText)
		})
	}
}

func TestExecutorRejectsInvalidPersistedResumeWithoutFailingTask_BitsUT(t *testing.T) {
	executor, spec, checkpoint := resumeFixture(t, "approval")
	result, err := executor.Execute(
		context.Background(),
		&backgroundtask.Task{
			Spec: spec, Attempt: 2, Checkpoint: checkpoint,
			PendingResume: []byte(`{"other":"value"}`),
		},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, result.Status)
	require.Equal(t, checkpoint, result.Checkpoint)
}

func TestAttack_ResumeValidationPreservesLargeIntegers(t *testing.T) {
	executor, spec, checkpoint := resumeFixture(t, "approval")
	const resume = `{"approval":{"ticket":9007199254740993}}`
	normalized, err := executor.validateResume(
		spec, checkpoint, []byte(resume),
	)
	require.NoError(t, err)
	t.Logf("normalized resume payload: %s", normalized)
	require.Equal(t, resume, string(normalized))
}

func TestResumeControlHelpers(t *testing.T) {
	targets, err := decodeResumeTargets([]byte(
		`{"approval":{"ticket":9007199254740993}}`,
	))
	require.NoError(t, err)
	approval, ok := targets["approval"].(map[string]any)
	require.True(t, ok)
	ticket, ok := approval["ticket"].(json.Number)
	require.True(t, ok)
	require.Equal(t, "9007199254740993", ticket.String())
	_, err = decodeResumeTargets([]byte(`{"approval":true} trailing`))
	require.Error(t, err)

	controls := make(chan backgroundtask.ControlRequest, 1)
	controls <- backgroundtask.ControlRequest{
		Kind: backgroundtask.ControlTimeout, Reason: "deadline",
	}
	require.Equal(t, backgroundtask.ControlTimeout, pollControl(controls).Kind)
	require.Empty(t, pollControl(controls).Kind)
	controls <- backgroundtask.ControlRequest{Kind: backgroundtask.ControlStop}
	require.Equal(t, backgroundtask.ControlStop,
		waitForControl(context.Background(), controls).Kind)

	executor := newTestExecutor(t, nil)
	task := &backgroundtask.Task{Spec: backgroundtask.Spec{ID: "task"}}
	result, controlErr, controlled := executor.controlResult(
		context.Background(), task,
		backgroundtask.ControlRequest{Kind: backgroundtask.ControlTimeout, Reason: "deadline"},
	)
	require.True(t, controlled)
	require.NoError(t, controlErr)
	require.Equal(t, backgroundtask.StatusFailed, result.Status)
	require.Equal(t, "deadline", result.Error)

	result, controlErr, controlled = executor.controlResult(
		context.Background(), task,
		backgroundtask.ControlRequest{Kind: backgroundtask.ControlDrain},
	)
	require.True(t, controlled)
	require.ErrorIs(t, controlErr, backgroundtask.ErrDrainCheckpointUnavailable)
	require.Nil(t, result)

	result, controlErr, controlled = executor.controlResult(
		context.Background(), task, backgroundtask.ControlRequest{},
	)
	require.False(t, controlled)
	require.NoError(t, controlErr)
	require.Nil(t, result)
}

func TestHandleRunErrorControlOutcomes(t *testing.T) {
	executor := newTestExecutor(t, nil)
	task := &backgroundtask.Task{Spec: backgroundtask.Spec{ID: "task"}}

	t.Run("ordinary error", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Close()
		wantErr := errors.New("model failed")
		result, err := executor.handleRunError(
			context.Background(), iter, task,
			make(chan backgroundtask.ControlRequest), wantErr,
		)
		require.ErrorIs(t, err, wantErr)
		require.Nil(t, result)
	})

	t.Run("busy child session yields", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Close()
		result, err := executor.handleRunError(
			context.Background(),
			iter,
			task,
			make(chan backgroundtask.ControlRequest),
			adk.ErrSessionBusy,
		)
		require.NoError(t, err)
		require.Equal(
			t,
			backgroundtask.ExecutionDirectiveYield,
			result.Directive,
		)
	})

	t.Run("stop", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Close()
		controls := make(chan backgroundtask.ControlRequest, 1)
		controls <- backgroundtask.ControlRequest{Kind: backgroundtask.ControlStop}
		result, err := executor.handleRunError(
			context.Background(), iter, task, controls, context.Canceled,
		)
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusCanceled, result.Status)
	})

	t.Run("timeout", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Close()
		controls := make(chan backgroundtask.ControlRequest, 1)
		controls <- backgroundtask.ControlRequest{
			Kind: backgroundtask.ControlTimeout, Reason: "deadline",
		}
		result, err := executor.handleRunError(
			context.Background(), iter, task, controls, context.Canceled,
		)
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusFailed, result.Status)
		require.Equal(t, "deadline", result.Error)
	})
}

func TestAttack_StreamCanceledWithoutControlRemainsFailure(t *testing.T) {
	executor := newTestExecutor(t, nil)
	task := &backgroundtask.Task{Spec: backgroundtask.Spec{ID: "task"}}
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Close()

	result, err := executor.handleRunError(
		context.Background(), iter, task,
		make(chan backgroundtask.ControlRequest), adk.ErrStreamCanceled,
	)

	require.Nil(t, result)
	require.ErrorIs(t, err, adk.ErrStreamCanceled)
}

func TestAttack_DrainControlAfterStreamCancellationSuspends(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	executor := newTestExecutor(t, store)
	task := &backgroundtask.Task{Spec: backgroundtask.Spec{ID: "task"}}
	require.NoError(t, store.Set(
		context.Background(), checkpointID(task.Spec.ID), []byte("runner checkpoint"),
	))
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Close()
	controls := make(chan backgroundtask.ControlRequest)
	go func() {
		time.Sleep(10 * time.Millisecond)
		controls <- backgroundtask.ControlRequest{Kind: backgroundtask.ControlDrain}
	}()

	result, err := executor.handleRunError(
		context.Background(), iter, task, controls, adk.ErrStreamCanceled,
	)

	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusSuspended, result.Status)
	require.JSONEq(t, `{"sequence":1}`, string(result.Checkpoint))
}

func TestSubagentPayloadValidation_BitsUT(t *testing.T) {
	executor, spec, _ := resumeFixture(t, "approval")
	require.NoError(t, executor.ValidateSpec(spec))

	var payload taskPayload
	require.NoError(t, json.Unmarshal(spec.Payload, &payload))
	payload.Version = 1
	var err error
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	assert.ErrorIs(t, executor.ValidateSpec(spec), backgroundtask.ErrUnsupportedExecutorPayloadVersion)

	payload.Version = payloadVersion
	payload.SubAgentName = ""
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	require.ErrorContains(t, executor.ValidateSpec(spec), "subagent name")

	payload.SubAgentName = "worker"
	payload.Input = nil
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	require.ErrorContains(t, executor.ValidateSpec(spec), "typed input")

	payload.Input, err = encodeTypedInput(textInput("query"))
	require.NoError(t, err)
	payload.ChildSessionID = ""
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	require.ErrorContains(t, executor.ValidateSpec(spec), "child session id")
}

func TestSubmitPersistsChildSessionIdentity_BitsUT(t *testing.T) {
	executor := newTestExecutor(t, nil)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{Executors: executors})
	defer manager.Close(context.Background())

	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("work"), Description: "child work",
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	payload, err := decodePayload(task.Spec)
	require.NoError(t, err)
	assert.Equal(t, payloadVersion, payload.Version)
	parsedChildSessionID, err := strconv.ParseInt(payload.ChildSessionID, 10, 64)
	require.NoError(t, err)
	assert.Positive(t, parsedChildSessionID)
	assert.False(t, strings.Contains(payload.ChildSessionID, "parent-session"))
	assert.False(t, strings.Contains(payload.ChildSessionID, "worker"))
	assert.False(t, strings.Contains(payload.ChildSessionID, task.Spec.ID))
	persistedInput, err := decodeTypedInput[*schema.Message](payload.Input)
	require.NoError(t, err)
	require.Equal(t, "work", persistedInput.Messages[0].Content)
	childSessionID, err := ChildSessionIDFromTask(task)
	require.NoError(t, err)
	assert.Equal(t, payload.ChildSessionID, childSessionID)
	assert.Equal(t, task.Spec.ID+"/checkpoint", checkpointID(task.Spec.ID))
	assert.Equal(t, "parent-session", task.Spec.SessionID)
	assert.True(t, task.Spec.NotifySession)

	disabled, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("silent work"),
		SessionID: "parent-session", DisableLifecycleNotifications: true,
	})
	require.NoError(t, err)
	assert.False(t, disabled.Spec.NotifySession)
}

func TestAttack_DefaultChildSessionIDIsOpaqueAndFresh_BitsUT(t *testing.T) {
	executor := newTestExecutor(t, nil)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	var sequence int
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			sequence++
			return "task_same", nil
		},
	})
	defer manager.Close(context.Background())

	first, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("first"), SessionID: "parent",
	})
	require.NoError(t, err)
	firstPayload, err := decodePayload(first.Spec)
	require.NoError(t, err)
	_, err = Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("second"), SessionID: "parent",
	})
	require.ErrorIs(t, err, backgroundtask.ErrAlreadyExists)
	assert.Equal(t, 2, sequence)
	secondID, err := defaultChildSessionID()
	require.NoError(t, err)
	assert.NotEqual(t, firstPayload.ChildSessionID, secondID)
	assert.NotContains(t, firstPayload.ChildSessionID, first.Spec.ID)
	assert.NotContains(t, firstPayload.ChildSessionID, "parent")
	assert.NotContains(t, firstPayload.ChildSessionID, "worker")
}

func TestTasksCanReusePersistentChildSessionHistory_BitsUT(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &historyCaptureAgent{name: "worker"}
	executor := newTestExecutor(t, sessionStore)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&backgroundtask.Config{Executors: executors},
	)
	defer manager.Close(context.Background())

	childSessionID := "1234567890"
	first, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("first request"),
		SessionID: "parent", ChildSessionID: childSessionID,
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), first.Spec.ID))
	second, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("second request"),
		SessionID: "parent", ChildSessionID: childSessionID,
	})
	require.NoError(t, err)
	require.NotEqual(t, first.Spec.ID, second.Spec.ID)
	require.NoError(t, manager.Execute(context.Background(), second.Spec.ID))

	require.Equal(t, [][]string{
		{"first request"},
		{"first request", "reply-1", "second request"},
	}, agent.runs)
	firstChild, err := ChildSessionIDFromTask(first)
	require.NoError(t, err)
	secondChild, err := ChildSessionIDFromTask(second)
	require.NoError(t, err)
	require.Equal(t, childSessionID, firstChild)
	require.Equal(t, firstChild, secondChild)
	format := func(
		_ context.Context,
		_ string,
		message *schema.Message,
	) (string, error) {
		return message.Content, nil
	}
	firstProgress, err := executor.ReadProgress(
		context.Background(), first, format,
	)
	require.NoError(t, err)
	require.Contains(t, firstProgress, "reply-1")
	require.NotContains(t, firstProgress, "reply-2")
	secondProgress, err := executor.ReadProgress(
		context.Background(), second, format,
	)
	require.NoError(t, err)
	require.Contains(t, secondProgress, "reply-2")
	require.NotContains(t, secondProgress, "reply-1")
}

func TestSubmitAcceptsOpaqueUserProvidedChildSessionID_BitsUT(t *testing.T) {
	executor := newTestExecutor(t, nil)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&backgroundtask.Config{Executors: executors},
	)

	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("work"), SessionID: "parent",
		ChildSessionID: "42",
	})
	require.NoError(t, err)
	childSessionID, err := ChildSessionIDFromTask(task)
	require.NoError(t, err)
	require.Equal(t, "42", childSessionID)
	_, err = Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("work"), SessionID: "parent",
		ChildSessionID: "43",
	})
	require.NoError(t, err)
}

func executionFixture(
	t *testing.T,
	agent *resumableTestAgent,
) (*backgroundtask.Manager, *adk.Runner, *backgroundtask.Task, *adksession.InMemoryStore[*schema.Message]) {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	executor := newTestExecutor(t, store)
	require.NoError(t, executor.Register(agent.name, &AgentRegistration[*schema.Message]{Agent: agent}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{Executors: executors})
	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: agent.name, Input: textInput("work"), Description: "work",
		SessionID: "parent",
	})
	require.NoError(t, err)
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: store,
		SessionID: "parent", SessionStore: store,
	})
	return manager, runner, task, store
}

func TestExecutorInterruptBecomesWaitingInput_BitsUT(t *testing.T) {
	agent := &resumableTestAgent{name: "worker", eventFactory: func(ctx context.Context) *adk.AgentEvent {
		return adk.Interrupt(ctx, "approve")
	}}
	manager, _, task, store := executionFixture(t, agent)
	defer manager.Close(context.Background())

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	result, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusWaitingInput, result.Status)
	var state checkpointState
	require.NoError(t, json.Unmarshal(result.Checkpoint, &state))
	require.Len(t, state.TargetIDs, 1)
	assert.NotEmpty(t, state.TargetIDs[0])
	assert.Empty(t, result.ResultData, "an interrupt is not a terminal message result")
	assert.Empty(t, result.ResultError)
	_, exists, err := store.Get(context.Background(), task.Spec.ID+"/checkpoint")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestExecutorMessageBecomesTerminalResult_BitsUT(t *testing.T) {
	message := adk.EventFromMessage(
		schema.AssistantMessage("progress", nil), nil, schema.Assistant, "worker",
	)
	manager, _, task, _ := executionFixture(t, &resumableTestAgent{
		name: "worker", events: []*adk.AgentEvent{message},
	})
	defer manager.Close(context.Background())

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	result, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, backgroundtask.StatusCompleted, result.Status)
	assert.Equal(t, "progress", string(result.ResultData))
}

func TestExecutorReconstructsRegisteredRunOptionsForEveryAttempt_BitsUT(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &optionCaptureAgent{name: "worker"}
	var factoryCalls int
	executor := newTestExecutor(t, store)
	require.NoError(t, executor.Register(agent.name, &AgentRegistration[*schema.Message]{
		Agent: agent,
		RunOptionsFactory: func() ([]adk.AgentRunOption, error) {
			factoryCalls++
			return []adk.AgentRunOption{adk.WrapImplSpecificOptFn(
				func(options *registrationRunOptions) {
					options.value = "registered"
				},
			)}, nil
		},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{Executors: executors})
	defer manager.Close(context.Background())
	for i := 0; i < 2; i++ {
		task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
			SubAgentName: agent.name, Input: textInput("work"), Description: "work", SessionID: "parent",
		})
		require.NoError(t, err)
		require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	}
	assert.Equal(t, 2, factoryCalls)
	assert.Equal(t, []string{"registered", "registered"}, agent.seen)
}

func TestManagerExecuteUsesConfiguredDependencies_BitsUT(t *testing.T) {
	manager, _, task, _ := executionFixture(t, &resumableTestAgent{name: "worker"})
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	completed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusCompleted, completed.Status)
	assert.Equal(t, int64(1), completed.Attempt)
}

func TestStopControlWinsOverLateFinalMessage_BitsUT(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &cancelThenMessageAgent{
		name: "worker", started: make(chan struct{}), release: make(chan struct{}),
	}
	executor := newTestExecutor(t, store)
	require.NoError(t, executor.Register(agent.name, &AgentRegistration[*schema.Message]{Agent: agent}))
	registry := backgroundtask.NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{Executors: registry})
	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: agent.name, Input: textInput("work"), Description: "work", SessionID: "parent",
	})
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	<-agent.started
	_, err = manager.RequestCancel(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	close(agent.release)
	require.NoError(t, <-executeDone)
	canceled, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusCanceled, canceled.Status)
	assert.NotEqual(t, "late completion", string(canceled.ResultData))
}

func TestExecutorDrainUsesDurableRunnerCheckpoint_BitsUT(t *testing.T) {
	agent := &resumableTestAgent{name: "worker", eventFactory: func(ctx context.Context) *adk.AgentEvent {
		return adk.Interrupt(ctx, "pause for drain")
	}}
	manager, _, task, _ := executionFixture(t, agent)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	result, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusWaitingInput, result.Status)
}

func TestExecutorDrainTimeoutEscalatesBlockedModelAndResumes_BitsUT(t *testing.T) {
	model := &drainTimeoutModel{started: make(chan struct{})}
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "worker", Description: "drain timeout test", Model: model,
	})
	require.NoError(t, err)

	task, store := executeDrainTimeout(t, agent, model.started)
	requireDrainCheckpointResumes(t, task, store, agent)
	require.GreaterOrEqual(t, atomic.LoadInt32(&model.calls), int32(2))
}

func TestExecutorDrainTimeoutEscalatesBlockedModelStreamAndResumes(t *testing.T) {
	model := &drainTimeoutStreamingModel{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(model.release)
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "worker", Description: "drain timeout stream test", Model: model,
	})
	require.NoError(t, err)

	task, store := executeDrainTimeout(t, agent, model.started)
	requireDrainCheckpointResumes(t, task, store, agent)
	require.GreaterOrEqual(t, atomic.LoadInt32(&model.calls), int32(2))
}

func TestExecutorDrainTimeoutEscalatesBlockedToolAndResumes_BitsUT(t *testing.T) {
	model := &drainTimeoutToolModel{}
	tool := &drainTimeoutTool{started: make(chan struct{})}
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "worker", Description: "drain timeout test", Model: model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{tool},
			},
		},
	})
	require.NoError(t, err)

	task, store := executeDrainTimeout(t, agent, tool.started)
	requireDrainCheckpointResumes(t, task, store, agent)
	require.GreaterOrEqual(t, atomic.LoadInt32(&model.calls), int32(2))
}

func TestExecutorDrainTimeoutEscalatesActiveStreamableToolAndResumes(t *testing.T) {
	model := &drainTimeoutToolModel{}
	tool := &drainTimeoutStreamableTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(tool.release)
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "worker", Description: "drain timeout streamable tool test", Model: model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{tool},
			},
		},
	})
	require.NoError(t, err)

	task, store := executeDrainTimeout(t, agent, tool.started)
	requireDrainCheckpointResumes(t, task, store, agent)
	require.Equal(t, int32(2), atomic.LoadInt32(&model.calls))
}

func TestControlAndInterruptUseRunnerCheckpoint(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &contextCaptureAgent{
		name: "worker", contexts: make(chan context.Context, 1), release: make(chan struct{}),
	}
	executor := newTestExecutor(t, store)
	require.NoError(t, executor.Register(agent.name, &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	registry := backgroundtask.NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{Executors: registry})
	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: agent.name, Input: textInput("work"), Description: "work", SessionID: "parent",
	})
	require.NoError(t, err)
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: store, SessionID: "parent", SessionStore: store,
	})
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()
	runCtx := <-agent.contexts
	taskCtx, ok := TaskContextFromContext(runCtx)
	require.True(t, ok)
	assert.Equal(t, task.Spec.ID, taskCtx.TaskID)
	assert.Equal(t, "parent", taskCtx.ParentSessionID)
	assert.Equal(t, agent.name, taskCtx.SubAgentName)
	assert.Equal(t, int64(1), taskCtx.Attempt)
	assert.NotEmpty(t, taskCtx.ChildSessionID)

	result, controlErr, controlled := executor.controlResult(
		runCtx, task, backgroundtask.ControlRequest{Kind: backgroundtask.ControlDrain},
	)
	require.True(t, controlled)
	require.ErrorIs(t, controlErr, backgroundtask.ErrDrainCheckpointUnavailable)
	require.Nil(t, result)

	require.NoError(t, store.Set(
		context.Background(), checkpointID(task.Spec.ID), []byte("runner checkpoint"),
	))
	result, controlErr, controlled = executor.controlResult(
		runCtx, task, backgroundtask.ControlRequest{Kind: backgroundtask.ControlDrain},
	)
	require.True(t, controlled)
	require.NoError(t, controlErr)
	require.Equal(t, backgroundtask.StatusSuspended, result.Status)

	result, err = executor.interruptResult(runCtx, task, &adk.InterruptInfo{})
	require.ErrorContains(t, err, "no resumable targets")
	require.Nil(t, result)
	result, err = executor.interruptResult(runCtx, task, &adk.InterruptInfo{
		InterruptContexts: []*adk.InterruptCtx{{ID: "approval"}},
	})
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, result.Status)

	_, _ = executor.beginRun(
		runCtx, runner, &backgroundtask.Task{
			Spec: task.Spec, Checkpoint: []byte(`{"sequence":1}`),
		}, nil,
	)
	_, _ = executor.beginRun(
		runCtx, runner, &backgroundtask.Task{
			Spec: task.Spec, Checkpoint: []byte(`{"sequence":1}`),
			PendingResume: []byte(`{"approval":true}`),
		}, nil,
	)

	close(agent.release)
	require.NoError(t, <-executeDone)
}

func TestWaitForControlBoundaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Empty(t, waitForControl(
		ctx, make(chan backgroundtask.ControlRequest),
	).Kind)
	require.Empty(t, waitForControl(
		context.Background(), make(chan backgroundtask.ControlRequest),
	).Kind)
}

func TestSubAgentTaskResumeRestoresLatestContextSnapshot(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &resumeContextCaptureAgent{
		name: "worker", resumeCtxs: make(chan context.Context, 1),
	}
	executor := newTestExecutor(t, sessionStore)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors:          executors,
		ContextSnapshotter: subagentContextSnapshotter{},
	})
	defer manager.Close(context.Background())

	submitCtx := context.WithValue(
		context.Background(), subagentContextSnapshotKey{}, "submit-trace",
	)
	task, err := Submit(submitCtx, manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("do work"), Description: "durable child",
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	require.Equal(t, "submit-trace", string(task.ContextSnapshot))
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	waiting, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, waiting.Status)

	resumeCtx := context.WithValue(
		context.Background(), subagentContextSnapshotKey{}, "resume-trace",
	)
	pending, err := manager.Resume(resumeCtx, &backgroundtask.ResumeRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version,
	})
	require.NoError(t, err)
	require.Equal(t, "resume-trace", string(pending.ContextSnapshot))
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))

	runCtx := <-agent.resumeCtxs
	require.Equal(t, "resume-trace", runCtx.Value(subagentContextSnapshotKey{}))
	completed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusCompleted, completed.Status)
}

func TestSubAgentTaskResumesAfterManagerReconstruction_BitsUT(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &interruptThenCompleteAgent{name: "worker"}
	executor := newTestExecutor(t, sessionStore)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	taskStore := backgroundtask.NewInMemoryStore(nil)

	manager1 := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: taskStore, Executors: executors,
	})
	task, err := Submit(context.Background(), manager1, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker", Input: textInput("do work"), Description: "durable child",
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	require.NoError(t, manager1.Execute(context.Background(), task.Spec.ID))
	waiting, err := manager1.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, waiting.Status)
	var state checkpointState
	require.NoError(t, json.Unmarshal(waiting.Checkpoint, &state))
	require.Len(t, state.TargetIDs, 1)
	require.NoError(t, manager1.Close(context.Background()))

	manager2 := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: taskStore, Executors: executors,
	})
	defer manager2.Close(context.Background())
	pending, err := manager2.Resume(context.Background(), &backgroundtask.ResumeRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusPending, pending.Status)
	require.NoError(t, manager2.Execute(context.Background(), task.Spec.ID))

	completed, err := manager2.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusCompleted, completed.Status)
	assert.Contains(t, string(completed.ResultData), "approved")
	assert.Equal(t, int64(2), completed.Attempt)
	assert.Empty(t, completed.Spec.OutputFile)
	feed, err := manager2.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: completed.Spec.ID,
	})
	require.NoError(t, err)
	assert.Empty(t, feed.Events)

	var persisted taskPayload
	require.NoError(t, json.Unmarshal(completed.Spec.Payload, &persisted))
	assert.Equal(t, payloadVersion, persisted.Version)
	persistedInput, err := decodeTypedInput[*schema.Message](persisted.Input)
	require.NoError(t, err)
	assert.Equal(t, "do work", persistedInput.Messages[0].Content)
	parsedChildSessionID, err := strconv.ParseInt(persisted.ChildSessionID, 10, 64)
	require.NoError(t, err)
	assert.Positive(t, parsedChildSessionID)
	assert.Equal(t, checkpointID(task.Spec.ID), completed.Spec.ID+"/checkpoint")
}
