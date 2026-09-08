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
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/components/model"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type completionBarrierFunc[M adk.MessageType] func(
	context.Context,
	*CompletionContext[M],
) (CompletionAction, error)

type cancellationHookFunc func(context.Context, string, string, string) error

func (f cancellationHookFunc) OnCancel(
	ctx context.Context,
	taskID, childSessionID, reason string,
) error {
	return f(ctx, taskID, childSessionID, reason)
}

type preemptModel struct {
	started chan struct{}
	runs    int64
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

type drainTimeoutToolModel struct {
	calls int32
}

type drainTimeoutTool struct {
	started chan struct{}
	calls   int32
}

type runResumeCountingAgent struct {
	adk.ResumableAgent
	runs     int64
	resumes  int64
	mu       sync.Mutex
	inputs   [][]string
	inputIDs [][]string
}

type recordingCheckpointStore struct {
	store  adk.CheckPointStore
	record func(string)
}

func (s *recordingCheckpointStore) Get(
	ctx context.Context,
	checkpointID string,
) ([]byte, bool, error) {
	return s.store.Get(ctx, checkpointID)
}

func (s *recordingCheckpointStore) Set(
	ctx context.Context,
	checkpointID string,
	checkpoint []byte,
) error {
	s.record("set:" + checkpointID)
	return s.store.Set(ctx, checkpointID, checkpoint)
}

func (s *recordingCheckpointStore) Delete(
	ctx context.Context,
	checkpointID string,
) error {
	s.record("delete:" + checkpointID)
	deleter, ok := s.store.(adk.CheckPointDeleter)
	if !ok {
		return nil
	}
	return deleter.Delete(ctx, checkpointID)
}

type activeLookupBarrierStore struct {
	*background.InMemoryStore
	found   chan struct{}
	resume  chan struct{}
	pause   sync.Once
	release sync.Once
	childID string
}

func (s *activeLookupBarrierStore) GetActiveMailboxBySession(
	ctx context.Context,
	childSessionID string,
) (*task.Mailbox, error) {
	mailbox, err := s.InMemoryStore.GetActiveMailboxBySession(ctx, childSessionID)
	if err == nil && childSessionID == s.childID {
		s.pause.Do(func() {
			close(s.found)
			select {
			case <-s.resume:
			case <-ctx.Done():
			}
		})
	}
	return mailbox, err
}

func (s *activeLookupBarrierStore) unblock() {
	s.release.Do(func() { close(s.resume) })
}

type adoptionBarrierStore struct {
	*background.InMemoryStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	adopted sync.Once
	done    chan struct{}
	mu      sync.Mutex
	modes   []bool
}

func (s *adoptionBarrierStore) AdoptForeground(
	ctx context.Context,
	req *background.AdoptForegroundStoreRequest,
) (*background.TaskSnapshot, error) {
	s.mu.Lock()
	s.modes = append(s.modes, req.StartPending)
	s.mu.Unlock()
	if !req.StartPending {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	snapshot, err := s.InMemoryStore.AdoptForeground(ctx, req)
	if err == nil && req.StartPending {
		s.adopted.Do(func() { close(s.done) })
	}
	return snapshot, err
}

func (s *adoptionBarrierStore) startModes() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.modes...)
}

type resumableTestAgent struct {
	name string
}

type interruptThenCompleteAgent struct {
	name        string
	resumeInfos chan<- *adk.ResumeInfo
}

type emptyResultAgent struct {
	name string
}

type inputCaptureAgent struct {
	adk.ResumableAgent
	streaming chan<- bool
}

type errorResultAgent struct {
	name string
}

func (a *errorResultAgent) Name(context.Context) string      { return a.name }
func (*errorResultAgent) Description(context.Context) string { return "error result" }
func (a *errorResultAgent) Run(
	context.Context,
	*adk.AgentInput,
	...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Err: errors.New("agent failed")})
	generator.Close()
	return iter
}
func (a *errorResultAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func (a *inputCaptureAgent) Run(
	ctx context.Context,
	input *adk.AgentInput,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.streaming <- input.EnableStreaming
	return a.ResumableAgent.Run(ctx, input, options...)
}

func (a *emptyResultAgent) Name(context.Context) string      { return a.name }
func (*emptyResultAgent) Description(context.Context) string { return "empty result" }
func (*emptyResultAgent) Run(
	context.Context,
	*adk.AgentInput,
	...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Close()
	return iter
}
func (a *emptyResultAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func (a *resumableTestAgent) Name(context.Context) string        { return a.name }
func (a *resumableTestAgent) Description(context.Context) string { return "test agent" }
func (a *resumableTestAgent) Run(
	_ context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("done", nil), nil, schema.Assistant, a.name,
	))
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

func (a *interruptThenCompleteAgent) Name(context.Context) string { return a.name }
func (*interruptThenCompleteAgent) Description(context.Context) string {
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
	info *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	if a.resumeInfos != nil {
		copy := *info
		a.resumeInfos <- &copy
	}
	content, _ := info.ResumeData.(string)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage(content, nil), nil, schema.Assistant, a.name,
	))
	generator.Close()
	return iter
}

func (m *preemptModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	if atomic.AddInt64(&m.runs, 1) == 1 {
		close(m.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return schema.AssistantMessage("preempted", nil), nil
}

func (m *preemptModel) Stream(
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

func (a *runResumeCountingAgent) Run(
	ctx context.Context,
	input *adk.AgentInput,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	atomic.AddInt64(&a.runs, 1)
	contents := make([]string, 0, len(input.Messages))
	ids := make([]string, 0, len(input.Messages))
	for _, message := range input.Messages {
		if message.Role != schema.User {
			continue
		}
		contents = append(contents, message.Content)
		ids = append(ids, adk.GetMessageID(message))
	}
	a.mu.Lock()
	a.inputs = append(a.inputs, contents)
	a.inputIDs = append(a.inputIDs, ids)
	a.mu.Unlock()
	return a.ResumableAgent.Run(ctx, input, options...)
}

func (a *runResumeCountingAgent) Resume(
	ctx context.Context,
	info *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	atomic.AddInt64(&a.resumes, 1)
	return a.ResumableAgent.Resume(ctx, info, options...)
}

func (a *runResumeCountingAgent) runInputs() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([][]string, len(a.inputs))
	for index := range a.inputs {
		result[index] = append([]string(nil), a.inputs[index]...)
	}
	return result
}

func (a *runResumeCountingAgent) runInputIDs() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([][]string, len(a.inputIDs))
	for index := range a.inputIDs {
		result[index] = append([]string(nil), a.inputIDs[index]...)
	}
	return result
}

func (f completionBarrierFunc[M]) Check(
	ctx context.Context,
	input *CompletionContext[M],
) (CompletionAction, error) {
	return f(ctx, input)
}

func completeBarrier[M adk.MessageType]() CompletionBarrier[M] {
	return completionBarrierFunc[M](func(
		context.Context,
		*CompletionContext[M],
	) (CompletionAction, error) {
		return CompletionComplete, nil
	})
}

func newControllerForTest(
	t *testing.T,
	barrier CompletionBarrier[*schema.Message],
	mapper InputsToAgentInput[*schema.Message],
) (*Controller[*schema.Message], *background.Manager, *adksession.InMemoryStore[*schema.Message]) {
	return newControllerWithAgentForTest(
		t, &resumableTestAgent{name: "worker"}, barrier, mapper,
	)
}

func newControllerWithAgentForTest(
	t *testing.T,
	agent adk.ResumableAgent,
	barrier CompletionBarrier[*schema.Message],
	mapper InputsToAgentInput[*schema.Message],
) (*Controller[*schema.Message], *background.Manager, *adksession.InMemoryStore[*schema.Message]) {
	t.Helper()
	ctx := context.Background()
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	store := background.NewInMemoryStore(nil)
	manager, err := background.New(ctx, &background.Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	runtime, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager,
		Barrier: barrier, InputsToAgentInput: mapper,
		SessionStore: sessionStore, CheckPointStore: sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(
		agent.Name(ctx),
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	return runtime, manager, sessionStore
}

func executeDrainTimeoutTask(
	t *testing.T,
	agent adk.ResumableAgent,
	waitUntilRunning <-chan struct{},
) (*background.InMemoryStore, *adksession.InMemoryStore[*schema.Message], *Handle) {
	t.Helper()
	store := background.NewInMemoryStore(nil)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	manager := newIntegrationManager(t, store)
	runtime, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager,
		Barrier: completeBarrier[*schema.Message](), InputsToAgentInput: testEventMapper,
		SessionStore: sessionStore, CheckPointStore: sessionStore,
		DrainCancelTimeout: 20 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(
		agent.Name(context.Background()),
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	handle, err := runtime.Start(context.Background(), &StartRequest[*schema.Message]{
		InvocationID: "parent:drain-timeout", ParentSessionID: "parent",
		AgentName: agent.Name(context.Background()), StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages:        []*schema.Message{schema.UserMessage("work")},
			EnableStreaming: true,
		},
	})
	require.NoError(t, err)
	awaitIntegrationValue(t, waitUntilRunning)

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Close(closeCtx))
	suspended, err := manager.Get(context.Background(), handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusSuspended, suspended.Status)
	require.NotEmpty(t, suspended.Checkpoint)
	runtimeCheckpoint, err := decodeRuntimeCheckpoint[*schema.Message](
		suspended.Checkpoint,
	)
	require.NoError(t, err)
	require.NotEmpty(t, runtimeCheckpoint.TurnLoopCheckpoint)
	_, exists, err := sessionStore.Get(
		context.Background(),
		runtimeTurnLoopCheckpointID(handle.ID()),
	)
	require.NoError(t, err)
	require.False(t, exists)
	return store, sessionStore, handle
}

func requireDrainCheckpointResumes(
	t *testing.T,
	store *background.InMemoryStore,
	sessionStore *adksession.InMemoryStore[*schema.Message],
	handle *Handle,
	agent adk.ResumableAgent,
) {
	t.Helper()
	manager := newIntegrationManager(t, store)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })
	runtime, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager,
		Barrier: completeBarrier[*schema.Message](), InputsToAgentInput: testEventMapper,
		SessionStore: sessionStore, CheckPointStore: sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(
		agent.Name(context.Background()),
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	released, err := manager.ReleaseSuspension(context.Background(), handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, released.Status)
	require.NoError(t, manager.Execute(context.Background(), handle.ID()))
	result, err := runtime.Wait(context.Background(), handle.ID())
	require.NoError(t, err)
	require.Equal(t, "resumed", result.FinalMessage.Content)
}

func testEventMapper(
	context.Context,
	[]*task.InputRecord,
) (*adk.AgentInput, error) {
	return &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("background event")},
	}, nil
}

func TestControllerForegroundCompletesWithoutBackgroundRecord(t *testing.T) {
	ctx := context.Background()
	runtime, manager, sessionStore := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	request := &StartRequest[*schema.Message]{
		InvocationID: "parent:call", ParentSessionID: "parent",
		AgentName: "worker", Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
		StartMode: task.StartModeForeground,
	}
	handle, err := runtime.Start(ctx, request)
	require.NoError(t, err)
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)
	_, err = manager.Get(ctx, handle.ID())
	require.ErrorIs(t, err, background.ErrNotFound)
	mailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, mailbox.State)
	events, err := sessionStore.LoadEvents(
		ctx, handle.ChildSessionID(), &adk.LoadSessionEventsRequest{},
	)
	require.NoError(t, err)
	require.Len(t, events.Events, 4)
	require.Equal(t, adk.SessionEventSessionStatusRunning, events.Events[0].Kind)
	require.Equal(t, adk.SessionEventMessage, events.Events[1].Kind)
	require.Equal(t, schema.User, events.Events[1].Message.Role)
	require.Equal(t, "work", events.Events[1].Message.Content)
	require.Equal(t, adk.SessionEventMessage, events.Events[2].Kind)
	require.Equal(t, schema.Assistant, events.Events[2].Message.Role)
	require.Equal(t, "done", events.Events[2].Message.Content)
	require.Equal(t, adk.SessionEventSessionStatusIdle, events.Events[3].Kind)
	replayedHandle, err := runtime.Start(ctx, request)
	require.NoError(t, err)
	require.Equal(t, handle.ID(), replayedHandle.ID())
	replayed, err := runtime.Wait(ctx, replayedHandle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", replayed.FinalMessage.Content)
	restoredHandle, err := runtime.Handle(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, handle.ID(), restoredHandle.ID())
	require.Equal(t, handle.ChildSessionID(), restoredHandle.ChildSessionID())
	outcome, err := restoredHandle.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, task.OutcomeCompleted, outcome.Status)
}

func TestAttack_InactiveForegroundNotificationSurvivesReplay(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	input := &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("work")},
	}
	inputHash, err := stableRuntimeInputHash(input)
	require.NoError(t, err)
	metadata, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "parent", ChildSessionID: "persistent-child",
		AgentName: "worker", StartMode: task.StartModeForeground, InputHash: inputHash,
	})
	require.NoError(t, err)
	_, err = manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "orphaned-foreground", InvocationID: "parent:orphaned",
		Identity: metadata, RootSessionID: "parent",
		ChildSessionID: "persistent-child",
	})
	require.NoError(t, err)
	encoded, err := encodeTypedInput(input)
	require.NoError(t, err)
	initialData, err := json.Marshal(encoded)
	require.NoError(t, err)
	_, err = manager.SendInput(ctx, &task.SendInputRequest{
		TaskID: "orphaned-foreground",
		Input: task.Input{
			EventID: "parent:orphaned:initial",
			Kind:    initialSignalKind, Data: initialData,
		},
	})
	require.NoError(t, err)
	_, err = manager.SendInput(ctx, &task.SendInputRequest{
		TaskID: "orphaned-foreground",
		Input: task.Input{
			EventID: "notification",
			Kind:    "child.completed", Data: []byte("child"),
		},
	})
	require.NoError(t, err)

	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:orphaned", ParentSessionID: "parent",
		ChildSessionID: "persistent-child", AgentName: "worker",
		Input: input, StartMode: task.StartModeForeground,
	})
	require.NoError(t, err)
	require.Equal(t, "orphaned-foreground", handle.ID())
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)
	mailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, int64(2), mailbox.ConsumedCursor)
	require.Equal(t, task.MailboxSealed, mailbox.State)
}

func TestAttack_ForegroundTerminalCandidateSealsWithoutReplay(t *testing.T) {
	ctx := context.Background()
	runtime, manager, checkpointStore := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	input := &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("work")},
	}
	inputHash, err := stableRuntimeInputHash(input)
	require.NoError(t, err)
	metadata, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "parent", ChildSessionID: "persistent-child",
		AgentName: "worker", StartMode: task.StartModeForeground, InputHash: inputHash,
	})
	require.NoError(t, err)
	registered, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "candidate", InvocationID: "parent:candidate",
		Identity: metadata, RootSessionID: "parent",
		ChildSessionID: "persistent-child",
	})
	require.NoError(t, err)
	encoded, err := encodeTypedInput(input)
	require.NoError(t, err)
	initialData, err := json.Marshal(encoded)
	require.NoError(t, err)
	_, err = manager.SendInput(ctx, &task.SendInputRequest{
		TaskID: registered.Mailbox.TaskID,
		Input: task.Input{
			EventID: "parent:candidate:initial",
			Kind:    initialSignalKind, Data: initialData,
		},
	})
	require.NoError(t, err)
	require.NoError(t, manager.AdvanceInputCursor(ctx, &task.AdvanceCursorRequest{
		TaskID: registered.Mailbox.TaskID, ExpectedCursor: 0, Cursor: 1,
		ExpectedGeneration: registered.Mailbox.Generation,
	}))
	final, err := encodeRuntimeMessage(schema.AssistantMessage("candidate", nil))
	require.NoError(t, err)
	candidate, err := json.Marshal(&foregroundResultCheckpoint{
		Version: foregroundResultVersion, State: foregroundResultTerminal,
		Status:      task.OutcomeCompleted,
		InputCursor: 1, FinalMessage: final,
	})
	require.NoError(t, err)
	require.NoError(t, checkpointStore.Set(
		ctx,
		runtimeForegroundResultCheckpointID(registered.Mailbox.TaskID),
		candidate,
	))

	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:candidate", ParentSessionID: "parent",
		ChildSessionID: "persistent-child", AgentName: "worker",
		Input: input, StartMode: task.StartModeForeground,
	})
	require.NoError(t, err)
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "candidate", result.FinalMessage.Content)
	mailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, mailbox.State)
}

func TestRecoverForegroundResult(t *testing.T) {
	ctx := context.Background()
	runtime, manager, checkpointStore := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	metadata, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "parent", ChildSessionID: "recovered-child",
		AgentName: "worker", StartMode: task.StartModeForeground,
	})
	require.NoError(t, err)
	registered, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "recovered-foreground",
		InvocationID:    "parent:recovered",
		Identity:        metadata,
		RootSessionID:   "parent",
		ChildSessionID:  "recovered-child",
	})
	require.NoError(t, err)

	_, err = runtime.recoverForegroundResult(ctx, registered.Mailbox)
	require.EqualError(t, err, "task/subagent: foreground mailbox is not sealed")
	sealed, err := manager.SealMailbox(ctx, &task.SealMailboxRequest{
		TaskID:             registered.Mailbox.TaskID,
		ExpectedCursor:     0,
		ExpectedGeneration: registered.Mailbox.Generation,
	})
	require.NoError(t, err)
	_, err = runtime.recoverForegroundResult(ctx, sealed)
	require.EqualError(t, err, "task/subagent: sealed foreground result is unavailable")

	failedCheckpoint, err := json.Marshal(&foregroundResultCheckpoint{
		Version: foregroundResultVersion,
		State:   foregroundResultTerminal,
		Status:  task.OutcomeFailed,
		Error:   "persisted failure",
	})
	require.NoError(t, err)
	require.NoError(t, checkpointStore.Set(
		ctx,
		runtimeForegroundResultCheckpointID(sealed.TaskID),
		failedCheckpoint,
	))
	_, err = runtime.recoverForegroundResult(ctx, sealed)
	require.EqualError(t, err, "persisted failure")

	final, err := encodeRuntimeMessage(schema.AssistantMessage("recovered", nil))
	require.NoError(t, err)
	completedCheckpoint, err := json.Marshal(&foregroundResultCheckpoint{
		Version:      foregroundResultVersion,
		State:        foregroundResultTerminal,
		Status:       task.OutcomeCompleted,
		FinalMessage: final,
	})
	require.NoError(t, err)
	require.NoError(t, checkpointStore.Set(
		ctx,
		runtimeForegroundResultCheckpointID(sealed.TaskID),
		completedCheckpoint,
	))
	result, err := runtime.recoverForegroundResult(ctx, sealed)
	require.NoError(t, err)
	require.Equal(t, sealed.TaskID, result.Handle.ID())
	require.Equal(t, "recovered-child", result.Handle.ChildSessionID())
	require.Equal(t, "recovered", result.FinalMessage.Content)
}

func TestSubmitTaskAdoptionIsIdempotent(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	metadata := &runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "parent", ChildSessionID: "adopted-child",
		AgentName: "worker", Description: "adopt task",
		StartMode: task.StartModeForeground,
	}
	identity, err := json.Marshal(metadata)
	require.NoError(t, err)
	registered, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "adopted-task",
		InvocationID:    "parent:adopted",
		Identity:        identity,
		RootSessionID:   "parent",
		ChildSessionID:  metadata.ChildSessionID,
	})
	require.NoError(t, err)
	handle := runtime.newHandle(registered.Mailbox.TaskID, metadata.ChildSessionID)
	checkpoint, err := encodeRuntimeCheckpoint[*schema.Message](0, nil)
	require.NoError(t, err)

	first, err := runtime.submitTask(ctx, handle, metadata, checkpoint)
	require.NoError(t, err)
	require.Equal(t, background.StatusSuspended, first.Status)
	require.Equal(t, background.PublicationOnBackground, first.Publication)
	require.Equal(t, checkpoint, first.Checkpoint)
	require.Equal(t, metadata.Description, first.Spec.Description)
	require.Equal(t, metadata.ChildSessionID, first.Spec.ID[:0]+handle.ChildSessionID())

	replayed, err := runtime.submitTask(
		ctx,
		handle,
		metadata,
		[]byte("must not replace the persisted checkpoint"),
	)
	require.NoError(t, err)
	require.Equal(t, first.Spec.ID, replayed.Spec.ID)
	require.Equal(t, first.Version, replayed.Version)
	require.Equal(t, checkpoint, replayed.Checkpoint)

	pending, err := manager.ListSuspended(ctx, &background.ListSuspendedRequest{
		ExecutorKeys: []string{ExecutorKey},
	})
	require.NoError(t, err)
	require.Len(t, pending.Tasks, 1)
	require.Equal(t, handle.ID(), pending.Tasks[0].Spec.ID)
}

func TestForegroundRecoveryProcessesInputPendingAtCandidateSeal(t *testing.T) {
	ctx := context.Background()
	runtime, manager, checkpointStore := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	input := &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("work")},
	}
	inputHash, err := stableRuntimeInputHash(input)
	require.NoError(t, err)
	metadata, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "parent", ChildSessionID: "pending-child",
		AgentName: "worker", StartMode: task.StartModeForeground, InputHash: inputHash,
	})
	require.NoError(t, err)
	registered, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "pending-candidate", InvocationID: "parent:pending",
		Identity: metadata, RootSessionID: "parent",
		ChildSessionID: "pending-child",
	})
	require.NoError(t, err)
	encoded, err := encodeTypedInput(input)
	require.NoError(t, err)
	initialData, err := json.Marshal(encoded)
	require.NoError(t, err)
	_, err = manager.SendInput(ctx, &task.SendInputRequest{
		TaskID: registered.Mailbox.TaskID,
		Input: task.Input{
			EventID: "parent:pending:initial",
			Kind:    initialSignalKind,
			Data:    initialData,
		},
	})
	require.NoError(t, err)
	require.NoError(t, manager.AdvanceInputCursor(ctx, &task.AdvanceCursorRequest{
		TaskID: registered.Mailbox.TaskID, ExpectedCursor: 0, Cursor: 1,
		ExpectedGeneration: registered.Mailbox.Generation,
	}))
	final, err := encodeRuntimeMessage(schema.AssistantMessage("stale candidate", nil))
	require.NoError(t, err)
	candidate, err := json.Marshal(&foregroundResultCheckpoint{
		Version: foregroundResultVersion, State: foregroundResultTerminal,
		Status:      task.OutcomeCompleted,
		InputCursor: 1, FinalMessage: final,
	})
	require.NoError(t, err)
	require.NoError(t, checkpointStore.Set(
		ctx,
		runtimeForegroundResultCheckpointID(registered.Mailbox.TaskID),
		candidate,
	))
	_, err = manager.SendInput(ctx, &task.SendInputRequest{
		TaskID: registered.Mailbox.TaskID,
		Input: task.Input{
			EventID: "pending-input", Kind: "external", Data: []byte("pending"),
		},
	})
	require.NoError(t, err)

	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:pending", ParentSessionID: "parent",
		ChildSessionID: "pending-child", AgentName: "worker",
		Input: input, StartMode: task.StartModeForeground,
	})
	require.NoError(t, err)
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)
	mailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, mailbox.State)
	require.Equal(t, int64(2), mailbox.ConsumedCursor)
}

func TestAttack_ForegroundFailureIsReplayableAndReleasesSession(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, errors.New("barrier failed")
		}),
		testEventMapper,
	)
	request := &StartRequest[*schema.Message]{
		InvocationID: "parent:failed", ParentSessionID: "parent",
		ChildSessionID: "persistent-child", AgentName: "worker",
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
		StartMode: task.StartModeForeground,
	}
	handle, err := runtime.Start(ctx, request)
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, handle.ID())
	require.EqualError(t, err, "barrier failed")
	outcome, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, task.OutcomeFailed, outcome.Status)
	require.Equal(t, "barrier failed", outcome.Error)
	mailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, mailbox.State)

	replayed, err := runtime.Start(ctx, request)
	require.NoError(t, err)
	require.Equal(t, handle.ID(), replayed.ID())
	_, err = runtime.Wait(ctx, replayed.ID())
	require.EqualError(t, err, "barrier failed")

	next, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:next", ParentSessionID: "parent",
		ChildSessionID: handle.ChildSessionID(), AgentName: "worker",
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("next")},
		},
		StartMode: task.StartModeForeground,
	})
	require.NoError(t, err)
	require.NotEqual(t, handle.ID(), next.ID())
}

func TestAttack_InactiveForegroundCancelSealsMailbox(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	var reason string
	runtime.cancellationHook = cancellationHookFunc(func(
		_ context.Context,
		_, _, value string,
	) error {
		reason = value
		return nil
	})
	metadata, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "parent", ChildSessionID: "child",
		AgentName: "worker", StartMode: task.StartModeForeground,
	})
	require.NoError(t, err)
	registered, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "inactive", InvocationID: "inactive",
		Identity: metadata, RootSessionID: "parent", ChildSessionID: "child",
	})
	require.NoError(t, err)
	handle := runtime.newHandle(registered.Mailbox.TaskID, "child")
	require.NoError(t, handle.Cancel(ctx, "operator canceled"))
	require.Equal(t, "operator canceled", reason)
	mailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, mailbox.State)
	outcome, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, task.OutcomeCanceled, outcome.Status)
	require.Equal(t, "operator canceled", outcome.Error)
	_, err = runtime.Wait(ctx, handle.ID())
	require.EqualError(t, err, "operator canceled")
}

func TestControllerBackgroundCompletes(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:background", ParentSessionID: "parent",
		AgentName: "worker", Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
		StartMode: task.StartModeBackground,
	})
	require.NoError(t, err)
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)
	replayed, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", replayed.FinalMessage.Content)
	completed, err := manager.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, completed.Status)
	require.Equal(t, int64(1), completed.Attempt)
}

func TestControllerSuspendedHandoffRetriesPendingForRacingInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := &adoptionBarrierStore{
		InMemoryStore: background.NewInMemoryStore(nil),
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
		done:          make(chan struct{}),
	}
	manager, err := background.New(ctx, &background.Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runtime, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager,
		Barrier: completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionSuspend, nil
		}),
		InputsToAgentInput: testEventMapper,
		SessionStore:       sessionStore,
		CheckPointStore:    sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{
			Agent: &resumableTestAgent{name: "worker"},
		},
	))

	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:adoption-race", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("first")},
		},
	})
	require.NoError(t, err)
	awaitIntegrationValue(t, store.entered)
	require.NoError(t, runtime.SendInput(ctx, handle.ID(), &task.Input{
		EventID: "late", Kind: messageInputKind,
		Data: mustEncodeAgentInput(t, "late"),
	}))
	close(store.release)
	awaitIntegrationValue(t, store.done)

	suspended, err := manager.WaitForTaskVersion(
		ctx,
		&background.WaitForTaskVersionRequest{
			TaskID: handle.ID(), AfterVersion: 3,
		},
	)
	require.NoError(t, err)
	require.Equal(t, background.StatusSuspended, suspended.Status)
	require.Equal(t, []bool{false, true}, store.startModes())
	mailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, int64(2), mailbox.ConsumedCursor)
}

func TestControllerDrainTimeoutCheckpointsBlockedModel(t *testing.T) {
	model := &drainTimeoutModel{started: make(chan struct{})}
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "worker", Description: "drain timeout test", Model: model,
	})
	require.NoError(t, err)

	store, sessionStore, handle := executeDrainTimeoutTask(t, agent, model.started)
	requireDrainCheckpointResumes(t, store, sessionStore, handle, agent)
	require.GreaterOrEqual(t, atomic.LoadInt32(&model.calls), int32(2))
}

func TestControllerDrainTimeoutCheckpointsBlockedModelStream(t *testing.T) {
	model := &drainTimeoutStreamingModel{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(model.release)
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "worker", Description: "drain timeout stream test", Model: model,
	})
	require.NoError(t, err)

	store, sessionStore, handle := executeDrainTimeoutTask(t, agent, model.started)
	requireDrainCheckpointResumes(t, store, sessionStore, handle, agent)
	require.GreaterOrEqual(t, atomic.LoadInt32(&model.calls), int32(2))
}

func TestControllerDrainTimeoutCheckpointsBlockedTool(t *testing.T) {
	model := &drainTimeoutToolModel{}
	blockingTool := &drainTimeoutTool{started: make(chan struct{})}
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "worker", Description: "drain timeout tool test", Model: model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{blockingTool},
			},
		},
	})
	require.NoError(t, err)

	store, sessionStore, handle := executeDrainTimeoutTask(t, agent, blockingTool.started)
	requireDrainCheckpointResumes(t, store, sessionStore, handle, agent)
	require.GreaterOrEqual(t, atomic.LoadInt32(&model.calls), int32(2))
}

func TestControllerCommitFailureDoesNotPublishCapturedCheckpoint( //nolint:funlen // Covers the full crash-recovery boundary.
	t *testing.T,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commitErr := errors.New("commit input failed")
	store := &stage3FaultStore{
		InMemoryStore:  background.NewInMemoryStore(nil),
		commitInputErr: commitErr,
	}
	var operationsMu sync.Mutex
	var operations []string
	recordOperation := func(operation string) {
		operationsMu.Lock()
		operations = append(operations, operation)
		operationsMu.Unlock()
	}
	store.commitInputHook = func() { recordOperation("commit_input") }
	manager1, err := background.New(ctx, &background.Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	checkpointStore := &recordingCheckpointStore{
		store: sessionStore, record: recordOperation,
	}
	model := &drainTimeoutModel{started: make(chan struct{})}
	baseAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "worker", Description: "checkpoint replay test", Model: model,
	})
	require.NoError(t, err)
	agent := &runResumeCountingAgent{ResumableAgent: baseAgent}
	suspendBarrier := completionBarrierFunc[*schema.Message](func(
		context.Context,
		*CompletionContext[*schema.Message],
	) (CompletionAction, error) {
		return CompletionSuspend, nil
	})
	inputMapper := func(
		_ context.Context,
		inputs []*task.InputRecord,
	) (*adk.AgentInput, error) {
		messages := make([]*schema.Message, 0, len(inputs))
		for _, input := range inputs {
			messages = append(messages, schema.UserMessage(string(input.Data)))
		}
		return &adk.AgentInput{Messages: messages}, nil
	}
	controller1, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager1, Barrier: suspendBarrier,
		InputsToAgentInput: inputMapper,
		SessionStore:       sessionStore, CheckPointStore: checkpointStore,
		DrainCancelTimeout: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, controller1.RegisterAgent(
		agent.Name(ctx),
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	handle, err := controller1.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID:    "parent:checkpoint-replay",
		ParentSessionID: "parent", AgentName: agent.Name(ctx),
		StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("sequence-1")},
		},
	})
	require.NoError(t, err)
	awaitIntegrationValue(t, model.started)

	closeCtx, closeCancel := context.WithTimeout(ctx, time.Second)
	defer closeCancel()
	require.ErrorIs(t, manager1.Close(closeCtx), commitErr)
	require.Equal(t, 1, store.commitInputCalls)
	require.Len(t, store.commitInputRequests, 1)
	failedCommitCheckpoint, err := decodeRuntimeCheckpoint[*schema.Message](
		store.commitInputRequests[0].Checkpoint,
	)
	require.NoError(t, err)
	require.NotEmpty(t, failedCommitCheckpoint.TurnLoopCheckpoint)
	operationsMu.Lock()
	recordedOperations := append([]string(nil), operations...)
	operationsMu.Unlock()
	require.Equal(t, []string{"commit_input"}, recordedOperations)
	_, checkpointExists, err := sessionStore.Get(
		ctx, runtimeTurnLoopCheckpointID(handle.ID()),
	)
	require.NoError(t, err)
	require.False(t, checkpointExists)
	running, err := store.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusRunning, running.Status)
	pending, err := store.Yield(ctx, &background.YieldTaskRequest{
		TaskID: handle.ID(), ExpectedVersion: running.Version,
	})
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, pending.Status)
	mailbox, err := store.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Zero(t, mailbox.ConsumedCursor)
	store.commitInputErr = nil

	manager2, err := background.New(ctx, &background.Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeIntegrationManager(t, manager2) })
	controller2, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager2, Barrier: suspendBarrier,
		InputsToAgentInput: inputMapper,
		SessionStore:       sessionStore, CheckPointStore: checkpointStore,
	})
	require.NoError(t, err)
	require.NoError(t, controller2.RegisterAgent(
		agent.Name(ctx),
		&AgentRegistration[*schema.Message]{Agent: agent},
	))

	require.NoError(t, manager2.Execute(ctx, handle.ID()))
	suspended, err := manager2.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusSuspended, suspended.Status)
	mailbox, err = manager2.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, int64(1), mailbox.ConsumedCursor)
	require.Equal(t, int64(2), atomic.LoadInt64(&agent.runs))
	require.Zero(t, atomic.LoadInt64(&agent.resumes))

	require.NoError(t, controller2.SendInput(ctx, handle.ID(), &task.Input{
		EventID: "sequence-2", Kind: "external", Data: []byte("sequence-2"),
	}))
	released, err := manager2.ReleaseSuspension(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, released.Status)
	require.NoError(t, manager2.Execute(ctx, handle.ID()))
	suspended, err = manager2.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusSuspended, suspended.Status)
	mailbox, err = manager2.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, int64(2), mailbox.ConsumedCursor)
	require.Equal(t, int64(3), atomic.LoadInt64(&agent.runs))
	require.Zero(t, atomic.LoadInt64(&agent.resumes))
	runInputs := agent.runInputs()
	require.Len(t, runInputs, 3)
	require.Equal(t, []string{"sequence-1"}, runInputs[0])
	require.Contains(t, runInputs[1], "sequence-1")
	require.Contains(t, runInputs[2], "sequence-2")
}

func TestMergeResumeInputRecords(t *testing.T) {
	createdAt := time.Unix(100, 200)
	record := func(sequence int64, kind string) *task.InputRecord {
		return &task.InputRecord{
			TaskID: "task", Sequence: sequence,
			Input: task.Input{
				EventID: fmt.Sprintf("event-%d", sequence),
				Kind:    kind, Data: []byte(fmt.Sprintf("data-%d", sequence)),
			},
			CreatedAt: createdAt.Add(time.Duration(sequence)),
		}
	}
	interrupted := record(1, initialSignalKind)
	unhandled := record(2, ResumeInputKind)
	mailbox1, mailbox2, mailbox3 := record(1, initialSignalKind),
		record(2, ResumeInputKind), record(3, "external")

	gotInterrupted, gotPending, err := mergeResumeInputRecords(
		[]*task.InputRecord{interrupted},
		[]*task.InputRecord{unhandled},
		[]*task.InputRecord{mailbox1, mailbox2, mailbox3},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, []*task.InputRecord{interrupted}, gotInterrupted)
	require.Equal(t, []*task.InputRecord{unhandled, mailbox3}, gotPending)
	require.Same(t, interrupted, gotInterrupted[0])
	require.Same(t, unhandled, gotPending[0])

	resumed := record(3, ResumeInputKind)
	gotInterrupted, gotPending, err = mergeResumeInputRecords(
		[]*task.InputRecord{resumed},
		[]*task.InputRecord{unhandled},
		[]*task.InputRecord{mailbox2, record(3, ResumeInputKind)},
		[]int64{3},
	)
	require.NoError(t, err)
	require.Equal(t, []*task.InputRecord{resumed}, gotInterrupted)
	require.Equal(t, []*task.InputRecord{unhandled}, gotPending)

	gotInterrupted, gotPending, err = mergeResumeInputRecords(
		nil,
		nil,
		[]*task.InputRecord{mailbox2, mailbox3},
		[]int64{3},
	)
	require.NoError(t, err)
	require.Empty(t, gotInterrupted)
	require.Equal(t, []*task.InputRecord{mailbox2}, gotPending)

	for _, testCase := range []struct {
		name   string
		mutate func(*task.InputRecord)
	}{
		{
			name: "content",
			mutate: func(input *task.InputRecord) {
				input.Data = []byte("conflicting")
			},
		},
		{
			name: "metadata",
			mutate: func(input *task.InputRecord) {
				input.EventID = "conflicting"
			},
		},
	} {
		t.Run(testCase.name+" conflict fails closed", func(t *testing.T) {
			conflict := record(1, initialSignalKind)
			testCase.mutate(conflict)
			_, _, mergeErr := mergeResumeInputRecords(
				[]*task.InputRecord{interrupted}, nil,
				[]*task.InputRecord{conflict},
				nil,
			)
			require.ErrorContains(t, mergeErr, "conflicting input replay")
		})
	}

	nilData := record(4, "external")
	nilData.Data = nil
	emptyData := record(4, "external")
	emptyData.Data = []byte{}
	require.True(t, equalInputRecords(nilData, emptyData))

	var encoded bytes.Buffer
	require.NoError(t, gob.NewEncoder(&encoded).Encode(nilData))
	var decoded task.InputRecord
	require.NoError(t, gob.NewDecoder(&encoded).Decode(&decoded))
	require.True(t, equalInputRecords(&decoded, emptyData))
}

func TestControllerNoRunnerStateCheckpointMergesMailboxReplay(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		conflict bool
	}{
		{name: "duplicate is consumed once"},
		{name: "conflict fails closed", conflict: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			store := background.NewInMemoryStore(nil)
			manager, err := background.New(ctx, &background.Config{
				Tasks: store, TaskEvents: store,
				SendTaskCreatedEvent: func(
					context.Context,
					*background.TaskSnapshot,
				) error {
					return nil
				},
			})
			require.NoError(t, err)
			t.Cleanup(func() { closeIntegrationManager(t, manager) })
			checkpointStore := adksession.NewInMemoryStore[*schema.Message](nil)
			agent := &runResumeCountingAgent{
				ResumableAgent: &resumableTestAgent{name: "worker"},
			}
			controller, err := NewController(&ControllerConfig[*schema.Message]{
				Manager: manager, Barrier: completeBarrier[*schema.Message](),
				InputsToAgentInput: testEventMapper,
				SessionStore:       checkpointStore, CheckPointStore: checkpointStore,
			})
			require.NoError(t, err)
			require.NoError(t, controller.RegisterAgent(
				"worker",
				&AgentRegistration[*schema.Message]{
					Agent: agent,
				},
			))

			taskID := "no-runner-" + testCase.name
			metadata := &runtimeMetadata{
				Version: runtimeMetadataVersion, ParentSessionID: "parent",
				RootSessionID: "parent", ChildSessionID: taskID + "-child",
				AgentName: "worker", StartMode: task.StartModeBackground,
			}
			identity, err := json.Marshal(metadata)
			require.NoError(t, err)
			reserved, err := manager.RegisterMailbox(
				ctx,
				&task.RegisterMailboxRequest{
					CandidateTaskID: taskID, InvocationID: taskID,
					Identity: identity, RootSessionID: metadata.RootSessionID,
					ChildSessionID: metadata.ChildSessionID,
				},
			)
			require.NoError(t, err)
			input := mustEncodeAgentInput(t, "work")
			terminal, err := controller.prepareStartMailbox(
				ctx,
				reserved,
				taskID,
				input,
			)
			require.NoError(t, err)
			require.False(t, terminal)
			signals, err := manager.ListInputs(ctx, &task.ListInputsRequest{
				TaskID: taskID,
			})
			require.NoError(t, err)
			require.Len(t, signals.Inputs, 1)
			checkpointInput := *signals.Inputs[0]
			checkpointInput.Data = append([]byte(nil), signals.Inputs[0].Data...)
			if testCase.conflict {
				checkpointInput.Data = []byte("conflicting")
			}
			var encoded bytes.Buffer
			require.NoError(t, gob.NewEncoder(&encoded).Encode(&struct {
				RunnerCheckpointID string
				RunnerCheckpoint   []byte
				HasRunnerState     bool
				UnhandledItems     []*task.InputRecord
				ResumeItems        []*task.InputRecord
				CanceledItems      []*task.InputRecord
			}{
				UnhandledItems: []*task.InputRecord{&checkpointInput},
			}))
			require.NoError(t, checkpointStore.Set(
				ctx,
				runtimeTurnLoopCheckpointID(taskID),
				encoded.Bytes(),
			))
			runtimeCheckpoint, err := json.Marshal(&turnLoopCheckpoint{
				Version: legacyRuntimeCheckpointVersion,
				Mode:    runtimeCheckpointIdle,
			})
			require.NoError(t, err)
			payload, err := json.Marshal(&taskPayload{
				Version: payloadVersion, SubAgentName: metadata.AgentName,
				ChildSessionID: metadata.ChildSessionID,
			})
			require.NoError(t, err)
			pending, err := manager.AdoptForeground(
				ctx,
				&background.AdoptForegroundRequest{
					Spec: background.Spec{
						ID: taskID, ExecutorKey: ExecutorKey, Kind: "subagent",
						Payload: payload, RootSessionID: metadata.RootSessionID,
					},
					ExpectedGeneration: reserved.Mailbox.Generation,
					InputCursor:        0,
					InitialCheckpoint:  runtimeCheckpoint,
					StartPending:       true,
				},
			)
			require.NoError(t, err)
			require.Equal(t, background.StatusPending, pending.Status)
			require.NoError(t, manager.Execute(ctx, taskID))

			snapshot, err := manager.Get(ctx, taskID)
			require.NoError(t, err)
			if testCase.conflict {
				require.Equal(t, background.StatusFailed, snapshot.Status)
				require.Contains(t, snapshot.ResultError, "conflicting input replay")
				require.Zero(t, atomic.LoadInt64(&agent.runs))
				_, exists, getErr := checkpointStore.Get(
					ctx,
					runtimeTurnLoopCheckpointID(taskID),
				)
				require.NoError(t, getErr)
				require.True(t, exists)
				return
			}
			require.Equal(t, background.StatusCompleted, snapshot.Status)
			require.Empty(t, snapshot.Checkpoint)
			require.Equal(t, int64(1), atomic.LoadInt64(&agent.runs))
			require.Equal(t, [][]string{{"work"}}, agent.runInputs())
			_, exists, getErr := checkpointStore.Get(
				ctx,
				runtimeTurnLoopCheckpointID(taskID),
			)
			require.NoError(t, getErr)
			require.False(t, exists)
		})
	}
}

func TestAcknowledgeInputRecordsFoldsOnlyContiguousPrefix(t *testing.T) {
	cursor, sparseAcks, err := acknowledgeInputRecords(
		1, nil, []*task.InputRecord{{Sequence: 3}},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), cursor)
	require.Equal(t, []int64{3}, sparseAcks)

	cursor, sparseAcks, err = acknowledgeInputRecords(
		cursor, sparseAcks, []*task.InputRecord{{Sequence: 2}},
	)
	require.NoError(t, err)
	require.Equal(t, int64(3), cursor)
	require.Empty(t, sparseAcks)
}

func TestControllerActiveCancellationInvokesHookOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agent := &blockingCaptureAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(agent.unblock)
	runtime, manager, _ := newControllerWithAgentForTest(
		t,
		agent,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	var cancellationCalls int64
	runtime.cancellationHook = cancellationHookFunc(func(
		context.Context,
		string,
		string,
		string,
	) error {
		atomic.AddInt64(&cancellationCalls, 1)
		return nil
	})
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:cancel-active", ParentSessionID: "parent",
		AgentName: agent.Name(ctx), StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	awaitIntegrationValue(t, agent.started)

	require.NoError(t, runtime.Cancel(ctx, handle.ID()))
	agent.unblock()
	require.Eventually(t, func() bool {
		snapshot, getErr := manager.Get(ctx, handle.ID())
		return getErr == nil && snapshot.Status == background.StatusCanceled
	}, time.Second, time.Millisecond)
	require.Equal(t, int64(1), atomic.LoadInt64(&cancellationCalls))
}

func TestControllerForegroundInterruptResumesFromMailbox(t *testing.T) {
	ctx := context.Background()
	runtime, _, _ := newControllerWithAgentForTest(
		t,
		&interruptThenCompleteAgent{name: "worker"},
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	request := &StartRequest[*schema.Message]{
		InvocationID: "parent:interrupt", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	}
	handle, err := runtime.Start(ctx, request)
	require.NoError(t, err)
	first, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.NotNil(t, first.Interrupted)
	targets := make(map[string]any)
	for _, interruptContext := range first.Interrupted.InterruptContexts {
		targets[interruptContext.ID] = "approved"
	}
	data, err := json.Marshal(targets)
	require.NoError(t, err)
	require.NoError(t, runtime.SendInput(ctx, handle.ID(), &task.Input{
		EventID: "resume", Kind: ResumeInputKind, Data: data,
	}))
	replayed, err := runtime.Start(ctx, request)
	require.NoError(t, err)
	require.Equal(t, handle.ID(), replayed.ID())
	second, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Nil(t, second.Interrupted)
	require.Equal(t, "approved", second.FinalMessage.Content)
}

func TestAttack_MultipleResumeInputsFailClosed(t *testing.T) {
	ctx := context.Background()
	runtime, _, _ := newControllerWithAgentForTest(
		t,
		&interruptThenCompleteAgent{name: "worker"},
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	request := &StartRequest[*schema.Message]{
		InvocationID: "parent:ambiguous-resume", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	}
	handle, err := runtime.Start(ctx, request)
	require.NoError(t, err)
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.NotNil(t, result.Interrupted)
	for _, eventID := range []string{"resume-1", "resume-2"} {
		require.NoError(t, runtime.SendInput(ctx, handle.ID(), &task.Input{
			EventID: eventID, Kind: ResumeInputKind,
			Data: []byte(`{"approve":true}`),
		}))
	}
	_, err = runtime.Start(ctx, request)
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, handle.ID())
	require.ErrorContains(t, err, "multiple resume")
}

func TestAttack_CancelRacingForegroundHandoffCancelsNewBackgroundTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	barrierEntered := make(chan struct{})
	releaseBarrier := make(chan struct{})
	runtime, manager, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			_ context.Context,
			_ *CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			close(barrierEntered)
			select {
			case <-releaseBarrier:
				return CompletionSuspend, nil
			case <-ctx.Done():
				return CompletionUnknown, ctx.Err()
			}
		}),
		testEventMapper,
	)
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:cancel-handoff", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	awaitIntegrationValue(t, barrierEntered)
	require.NoError(t, handle.Cancel(ctx, "cancel"))
	close(releaseBarrier)
	require.Eventually(t, func() bool {
		backgroundTask, getErr := manager.Get(ctx, handle.ID())
		return getErr == nil && backgroundTask.Status == background.StatusCanceled
	}, time.Second, time.Millisecond)
	outcome, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, task.OutcomeCanceled, outcome.Status)
}

func TestContinueCreatesNewTaskInPersistentChildSession(t *testing.T) {
	ctx := context.Background()
	runtime, _, sessionStore := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	const childSessionID = "persistent-child"
	first, err := runtime.Continue(ctx, &ContinueRequest[*schema.Message]{
		ChildSessionID: childSessionID,
		InvocationID:   "parent:first",
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("first")},
		},
		IfIdle: &StartOptions[*schema.Message]{
			ParentSessionID: "parent",
			AgentName:       "worker",
			StartMode:       task.StartModeForeground,
		},
	})
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, first.ID())
	require.NoError(t, err)
	second, err := runtime.Continue(ctx, &ContinueRequest[*schema.Message]{
		ChildSessionID: childSessionID,
		InvocationID:   "parent:second",
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("second")},
		},
		IfIdle: &StartOptions[*schema.Message]{
			ParentSessionID: "parent",
			AgentName:       "worker",
			StartMode:       task.StartModeForeground,
		},
	})
	require.NoError(t, err)
	require.NotEqual(t, first.ID(), second.ID())
	require.Equal(t, first.ChildSessionID(), second.ChildSessionID())
	_, err = runtime.Wait(ctx, second.ID())
	require.NoError(t, err)
	events, err := sessionStore.LoadEvents(
		ctx,
		childSessionID,
		&adk.LoadSessionEventsRequest{},
	)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events.Events), 4)
}

func TestContinueIdleSessionRequiresStartOptions(t *testing.T) {
	runtime, _, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	_, err := runtime.Continue(context.Background(), &ContinueRequest[*schema.Message]{
		ChildSessionID: "idle-child",
		InvocationID:   "parent:continue",
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("continue")},
		},
	})
	require.ErrorIs(t, err, task.ErrMailboxNotFound)
}

func TestContinueRetriesWhenActiveMailboxSealsBeforeSend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const childSessionID = "seal-race-child"
	store := &activeLookupBarrierStore{
		InMemoryStore: background.NewInMemoryStore(nil),
		found:         make(chan struct{}),
		resume:        make(chan struct{}),
		childID:       childSessionID,
	}
	t.Cleanup(store.unblock)
	manager, err := background.New(ctx, &background.Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runtime, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager, Barrier: completeBarrier[*schema.Message](),
		InputsToAgentInput: testEventMapper,
		SessionStore:       sessionStore, CheckPointStore: sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{
			Agent: &resumableTestAgent{name: "worker"},
		},
	))
	metadata, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "parent", ChildSessionID: childSessionID,
		AgentName: "worker", StartMode: task.StartModeForeground,
	})
	require.NoError(t, err)
	old, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "sealing-task", InvocationID: "sealing-task",
		Identity: metadata, RootSessionID: "parent",
		ChildSessionID: childSessionID,
	})
	require.NoError(t, err)

	type continueResult struct {
		handle *Handle
		err    error
	}
	resultCh := make(chan continueResult, 1)
	go func() {
		handle, continueErr := runtime.Continue(ctx, &ContinueRequest[*schema.Message]{
			ChildSessionID: childSessionID,
			InvocationID:   "continue-after-seal",
			Input: &adk.AgentInput{
				Messages: []*schema.Message{schema.UserMessage("continued")},
			},
			IfIdle: &StartOptions[*schema.Message]{
				ParentSessionID: "parent", AgentName: "worker",
				StartMode: task.StartModeBackground,
			},
		})
		resultCh <- continueResult{handle: handle, err: continueErr}
	}()
	awaitIntegrationValue(t, store.found)
	_, err = manager.SealMailbox(ctx, &task.SealMailboxRequest{
		TaskID: old.Mailbox.TaskID, ExpectedCursor: 0,
		ExpectedGeneration: old.Mailbox.Generation,
	})
	require.NoError(t, err)
	store.unblock()

	continued := awaitIntegrationValue(t, resultCh)
	require.NoError(t, continued.err)
	require.NotNil(t, continued.handle)
	require.NotEqual(t, old.Mailbox.TaskID, continued.handle.ID())
	inputs, err := manager.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: continued.handle.ID(),
	})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, 1)
	require.Equal(t, "continue-after-seal:initial", inputs.Inputs[0].EventID)
	require.Equal(t, initialSignalKind, inputs.Inputs[0].Kind)
	result, err := runtime.Wait(ctx, continued.handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)
	completed, err := manager.Get(ctx, continued.handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, completed.Status)
}

func TestForegroundInputCanPreemptActiveTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	model := &preemptModel{started: make(chan struct{})}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "worker", Description: "preempt test", Model: model,
	})
	require.NoError(t, err)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	store := background.NewInMemoryStore(nil)
	manager, err := background.New(ctx, &background.Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	runtime, err := NewController(&ControllerConfig[*schema.Message]{
		Manager:            manager,
		Barrier:            completeBarrier[*schema.Message](),
		InputsToAgentInput: testEventMapper,
		SessionStore:       sessionStore, CheckPointStore: sessionStore,
		InputPreemptPolicy: func(
			context.Context,
			*task.InputRecord,
			*adk.TurnContext[*task.InputRecord, *schema.Message],
		) []adk.PushOption[*task.InputRecord, *schema.Message] {
			return []adk.PushOption[*task.InputRecord, *schema.Message]{
				adk.WithPreemptTimeout[*task.InputRecord, *schema.Message](
					adk.AnySafePoint,
					20*time.Millisecond,
				),
			}
		},
	})
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:preempt", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("slow")},
		},
	})
	require.NoError(t, err)
	awaitIntegrationValue(t, model.started)
	require.NoError(t, runtime.SendInput(ctx, handle.ID(), &task.Input{
		EventID: "urgent", Kind: "external", Delivery: task.InputPreempt,
	}))
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "preempted", result.FinalMessage.Content)
	require.Equal(t, int64(3), atomic.LoadInt64(&model.runs))
}

func TestAttack_ReplayIdentityIgnoresFrameworkMessageID(t *testing.T) {
	ctx := context.Background()
	runtime, _, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	request := func(messageID, tenant string) *StartRequest[*schema.Message] {
		message := schema.UserMessage("work")
		message.Extra = map[string]any{
			"_eino_msg_id": messageID,
			"tenant":       tenant,
		}
		return &StartRequest[*schema.Message]{
			InvocationID: "parent:identity", ParentSessionID: "parent",
			AgentName: "worker", StartMode: task.StartModeForeground,
			Input: &adk.AgentInput{Messages: []*schema.Message{message}},
		}
	}
	first, err := runtime.Start(ctx, request("generated-1", "tenant-a"))
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, first.ID())
	require.NoError(t, err)
	replayed, err := runtime.Start(ctx, request("generated-2", "tenant-a"))
	require.NoError(t, err)
	require.Equal(t, first.ID(), replayed.ID())
	_, err = runtime.Start(ctx, request("generated-3", "tenant-b"))
	require.ErrorIs(t, err, task.ErrMailboxIdentityConflict)
}

func TestControllerValidationAndContextHelpers(t *testing.T) {
	_, err := NewController[*schema.Message](nil)
	require.Error(t, err)
	runtime, _, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	_, err = runtime.Start(context.Background(), nil)
	require.Error(t, err)
	_, err = runtime.Start(context.Background(), &StartRequest[*schema.Message]{
		InvocationID: "invalid-mode", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartMode(99),
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.Error(t, err)
	_, err = runtime.Start(context.Background(), &StartRequest[*schema.Message]{
		InvocationID: "oversized", ParentSessionID: "parent",
		ChildSessionID: string(make([]byte, maxChildSessionIDLength+1)),
		AgentName:      "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.Error(t, err)
	require.Error(t, runtime.SendInput(context.Background(), "", nil))

	ctx := task.WithExecutionContext(context.Background(), task.ExecutionContext{
		TaskID: "task",
	})
	ctx = withChildSessionID(ctx, "child")
	execution, ok := task.ExecutionContextFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "task", execution.TaskID)
	childSessionID, ok := ChildSessionID(ctx)
	require.True(t, ok)
	require.Equal(t, "child", childSessionID)
	_, ok = ChildSessionID(nil)
	require.False(t, ok)

	var nilHandle *Handle
	require.Empty(t, nilHandle.ID())
	require.ErrorIs(t, nilHandle.SendInput(ctx, &task.Input{}), task.ErrMailboxNotFound)
	_, err = nilHandle.Wait(ctx)
	require.ErrorIs(t, err, task.ErrMailboxNotFound)
	require.ErrorIs(t, nilHandle.Cancel(ctx, "cancel"), task.ErrMailboxNotFound)
}

func TestControllerRejectsInvalidCompletionAndMissingFinal(t *testing.T) {
	ctx := context.Background()
	runtime, _, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionAction(99), nil
		}),
		testEventMapper,
	)
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "invalid-completion", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, handle.ID())
	require.ErrorContains(t, err, "invalid completion action")

	emptyRuntime, _, _ := newControllerWithAgentForTest(
		t,
		&emptyResultAgent{name: "empty"},
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	empty, err := emptyRuntime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "missing-final", ParentSessionID: "parent",
		AgentName: "empty", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	_, err = emptyRuntime.Wait(ctx, empty.ID())
	require.ErrorContains(t, err, "runtime final message is required")
}

func TestAttack_StartHonorsStreamingAndTerminalCancelIsIdempotent(t *testing.T) {
	ctx := context.Background()
	streaming := make(chan bool, 1)
	agent := &inputCaptureAgent{
		ResumableAgent: &resumableTestAgent{name: "worker"},
		streaming:      streaming,
	}
	runtime, _, _ := newControllerWithAgentForTest(
		t,
		agent,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	var canceled int64
	runtime.cancellationHook = cancellationHookFunc(func(
		context.Context,
		string,
		string,
		string,
	) error {
		atomic.AddInt64(&canceled, 1)
		return nil
	})
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "streaming", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages:        []*schema.Message{schema.UserMessage("work")},
			EnableStreaming: true,
		},
	})
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.True(t, awaitIntegrationValue(t, streaming))
	require.NoError(t, runtime.Cancel(ctx, handle.ID()))
	require.Zero(t, atomic.LoadInt64(&canceled))
}

func TestAttack_BackgroundAgentErrorIsDurablyFailed(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerWithAgentForTest(
		t,
		&errorResultAgent{name: "worker"},
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "background-error", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, handle.ID())
	require.ErrorContains(t, err, "agent failed")
	failed, err := manager.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusFailed, failed.Status)
	require.Contains(t, failed.ResultError, "agent failed")
}

func TestControllerBackgroundControlResults(t *testing.T) {
	runtime, _, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	result, err := runtime.controlResult(
		&activationResult[*schema.Message]{
			control: background.ControlRequest{
				Kind: background.ControlStop, Reason: "stop",
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, background.ExecutionActionCancel, result.Action)

	result, err = runtime.controlResult(
		&activationResult[*schema.Message]{
			control: background.ControlRequest{Kind: background.ControlDrain},
			cursor:  3, final: schema.AssistantMessage("partial", nil),
			turnLoopCheckpoint: []byte("opaque"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, background.ExecutionActionSuspend, result.Action)
	checkpoint, err := decodeRuntimeCheckpoint[*schema.Message](result.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, int64(3), checkpoint.InputCursor)
	require.Equal(t, []byte("opaque"), checkpoint.TurnLoopCheckpoint)

	result, err = runtime.controlResult(
		&activationResult[*schema.Message]{
			control: background.ControlRequest{Kind: background.ControlTimeout},
		},
	)
	require.NoError(t, err)
	require.Equal(t, background.ExecutionActionFail, result.Action)
	require.NotEmpty(t, result.Error)

	_, err = runtime.controlResult(
		&activationResult[*schema.Message]{
			control: background.ControlRequest{Kind: "unknown"},
		},
	)
	require.Error(t, err)
}

func TestInterruptResultRequiresCheckpointAndTarget(t *testing.T) {
	ctx := context.Background()
	runtime, _, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	result := &activationResult[*schema.Message]{
		interrupted: &adk.InterruptInfo{
			InterruptContexts: []*adk.InterruptCtx{{ID: "target"}},
		},
	}

	_, err := runtime.interruptResult(ctx, result)
	require.EqualError(t, err, "task/subagent: turn loop checkpoint is missing")

	result.turnLoopCheckpoint = []byte("checkpoint")
	result.interrupted.InterruptContexts = []*adk.InterruptCtx{{}}
	_, err = runtime.interruptResult(ctx, result)
	require.EqualError(t, err, "task/subagent: runtime interrupt has no targets")

	result.interrupted.InterruptContexts = []*adk.InterruptCtx{{ID: "target"}}
	waiting, err := runtime.interruptResult(ctx, result)
	require.NoError(t, err)
	require.Equal(t, background.ExecutionActionWaitInput, waiting.Action)
	checkpoint, err := decodeRuntimeCheckpoint[*schema.Message](waiting.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, runtimeCheckpointResume, checkpoint.Mode)
	require.Equal(t, []string{"target"}, checkpoint.TargetIDs)
	require.Equal(t, []byte("checkpoint"), checkpoint.TurnLoopCheckpoint)
}
