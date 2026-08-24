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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type completionBarrierFunc[M adk.MessageType] func(
	context.Context,
	*CompletionContext[M],
) (CompletionDecision, error)

type lifecycleHookFunc func(context.Context, string, string, string) error

func (f lifecycleHookFunc) OnCancel(
	ctx context.Context,
	taskID, childSessionID, reason string,
) error {
	return f(ctx, taskID, childSessionID, reason)
}

type preemptModel struct {
	started chan struct{}
	runs    atomic.Int64
}

type resumableTestAgent struct {
	name string
}

type interruptThenCompleteAgent struct {
	name string
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

func (m *preemptModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	if m.runs.Add(1) == 1 {
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

func (f completionBarrierFunc[M]) Check(
	ctx context.Context,
	input *CompletionContext[M],
) (CompletionDecision, error) {
	return f(ctx, input)
}

func newControllerForTest(
	t *testing.T,
	barrier CompletionBarrier[*schema.Message],
	mapper EventToInput[*schema.Message],
) (*Controller[*schema.Message], *background.Manager, *adksession.InMemoryStore[*schema.Message]) {
	return newControllerWithAgentForTest(
		t, &resumableTestAgent{name: "worker"}, barrier, mapper,
	)
}

func newControllerWithAgentForTest(
	t *testing.T,
	agent adk.ResumableAgent,
	barrier CompletionBarrier[*schema.Message],
	mapper EventToInput[*schema.Message],
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
		Barrier: barrier, EventToInput: mapper,
		SessionStore: sessionStore, CheckPointStore: sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(
		agent.Name(ctx),
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	return runtime, manager, sessionStore
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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
	require.NotEmpty(t, events.Events)
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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
		Version: foregroundResultVersion, Status: task.OutcomeCompleted,
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

func TestAttack_ForegroundFailureIsReplayableAndReleasesSession(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, errors.New("barrier failed")
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
		testEventMapper,
	)
	var reason string
	runtime.lifecycleHook = lifecycleHookFunc(func(
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
	runtime, _, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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
}

func TestControllerForegroundInterruptResumesFromMailbox(t *testing.T) {
	ctx := context.Background()
	runtime, _, _ := newControllerWithAgentForTest(
		t,
		&interruptThenCompleteAgent{name: "worker"},
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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

func TestAttack_BackgroundInterruptResumeWakesTask(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerWithAgentForTest(
		t,
		&interruptThenCompleteAgent{name: "worker"},
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
		testEventMapper,
	)
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:background-interrupt", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		backgroundTask, getErr := manager.Get(ctx, handle.ID())
		return getErr == nil && backgroundTask.Status == background.StatusWaitingInput
	}, time.Second, time.Millisecond)
	require.NoError(t, runtime.SendInput(ctx, handle.ID(), &task.Input{
		EventID: "resume", Kind: ResumeInputKind,
		Data: []byte(`{"approve":"yes"}`),
	}))
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "approved", result.FinalMessage.Content)
}

func TestAttack_MultipleResumeInputsFailClosed(t *testing.T) {
	ctx := context.Background()
	runtime, _, _ := newControllerWithAgentForTest(
		t,
		&interruptThenCompleteAgent{name: "worker"},
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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

func TestControllerBarrierWaitThenInput(t *testing.T) {
	ctx := context.Background()
	var barrierCalls atomic.Int64
	runtime, manager, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			if barrierCalls.Add(1) > 1 {
				return Complete, nil
			}
			return Wait, nil
		}),
		testEventMapper,
	)
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent:wait", ParentSessionID: "parent",
		AgentName: "worker", Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
		StartMode: task.StartModeForeground,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		backgroundTask, getErr := manager.Get(ctx, handle.ID())
		return getErr == nil && backgroundTask.Status == background.StatusSuspended
	}, 3*time.Second, 10*time.Millisecond)
	sent, err := runtime.Continue(ctx, &ContinueRequest[*schema.Message]{
		ChildSessionID: handle.ChildSessionID(),
		InvocationID:   "parent:wait:send",
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("continue")},
		},
	})
	require.NoError(t, err)
	require.Equal(t, handle.ID(), sent.ID())
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)
}

func TestAttack_CancelRacingForegroundHandoffCancelsNewBackgroundTask(t *testing.T) {
	ctx := context.Background()
	barrierEntered := make(chan struct{})
	releaseBarrier := make(chan struct{})
	runtime, manager, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			close(barrierEntered)
			<-releaseBarrier
			return Wait, nil
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
	<-barrierEntered
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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

func TestNestedSubAgentMailboxUsesDirectParent(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
		testEventMapper,
	)
	parent, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "parent-task", InvocationID: "parent-task",
		RootSessionID: "root-session",
	})
	require.NoError(t, err)
	ctx = task.WithExecutionContext(ctx, task.ExecutionContext{
		TaskID: parent.Mailbox.TaskID, Owner: task.OwnerParent,
		Generation: parent.Mailbox.Generation, RootSessionID: "root-session",
	})
	child, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "parent-task:child", ParentSessionID: "parent-child-session",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, child.ID())
	require.NoError(t, err)
	mailbox, err := manager.GetMailbox(ctx, child.ID())
	require.NoError(t, err)
	require.Equal(t, parent.Mailbox.TaskID, mailbox.ParentTaskID)
	require.Equal(t, "root-session", mailbox.RootSessionID)
}

func TestForegroundInputCanPreemptActiveTurn(t *testing.T) {
	ctx := context.Background()
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
		Manager: manager,
		Barrier: completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
		EventToInput: testEventMapper,
		SessionStore: sessionStore, CheckPointStore: sessionStore,
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
	<-model.started
	require.NoError(t, runtime.SendInput(ctx, handle.ID(), &task.Input{
		EventID: "urgent", Kind: "external", Delivery: task.InputPreempt,
	}))
	result, err := runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "preempted", result.FinalMessage.Content)
	require.Equal(t, int64(2), model.runs.Load())
}

func TestAttack_ReplayIdentityIgnoresFrameworkMessageID(t *testing.T) {
	ctx := context.Background()
	runtime, _, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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

	ctx := WithRuntimeContext(context.Background(), "task", "child")
	taskID, ok := TaskID(ctx)
	require.True(t, ok)
	require.Equal(t, "task", taskID)
	childSessionID, ok := ChildSessionID(ctx)
	require.True(t, ok)
	require.Equal(t, "child", childSessionID)

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
		) (CompletionDecision, error) {
			return CompletionDecision(99), nil
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
	require.ErrorContains(t, err, "invalid completion decision")

	emptyRuntime, _, _ := newControllerWithAgentForTest(
		t,
		&emptyResultAgent{name: "empty"},
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
		testEventMapper,
	)
	var canceled atomic.Int64
	runtime.lifecycleHook = lifecycleHookFunc(func(
		context.Context,
		string,
		string,
		string,
	) error {
		canceled.Add(1)
		return nil
	})
	handle, err := runtime.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "streaming", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeBackground, EnableStreaming: true,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	_, err = runtime.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.True(t, <-streaming)
	require.NoError(t, runtime.Cancel(ctx, handle.ID()))
	require.Zero(t, canceled.Load())
}

func TestAttack_BackgroundAgentErrorIsDurablyFailed(t *testing.T) {
	ctx := context.Background()
	runtime, manager, _ := newControllerWithAgentForTest(
		t,
		&errorResultAgent{name: "worker"},
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionDecision, error) {
			return Complete, nil
		}),
		testEventMapper,
	)
	var canceled atomic.Int64
	runtime.lifecycleHook = lifecycleHookFunc(func(
		context.Context,
		string,
		string,
		string,
	) error {
		canceled.Add(1)
		return nil
	})
	backgroundTask := &background.TaskSnapshot{
		Spec: background.Spec{ID: "task"},
	}
	metadata := &runtimeMetadata{ChildSessionID: "child"}
	result, err := runtime.controlResult(
		context.Background(),
		backgroundTask,
		metadata,
		&activationResult[*schema.Message]{
			control: background.ControlRequest{
				Kind: background.ControlStop, Reason: "stop",
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, background.ExecutionActionCancel, result.Action)
	require.Equal(t, int64(1), canceled.Load())

	result, err = runtime.controlResult(
		context.Background(),
		backgroundTask,
		metadata,
		&activationResult[*schema.Message]{
			control: background.ControlRequest{Kind: background.ControlDrain},
			cursor:  3, final: schema.AssistantMessage("partial", nil),
		},
	)
	require.NoError(t, err)
	require.Equal(t, background.ExecutionActionSuspend, result.Action)
	checkpoint, err := decodeRuntimeCheckpoint[*schema.Message](result.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, int64(3), checkpoint.InputCursor)

	result, err = runtime.controlResult(
		context.Background(),
		backgroundTask,
		metadata,
		&activationResult[*schema.Message]{
			control: background.ControlRequest{Kind: background.ControlTimeout},
		},
	)
	require.NoError(t, err)
	require.Equal(t, background.ExecutionActionFail, result.Action)
	require.NotEmpty(t, result.Error)

	_, err = runtime.controlResult(
		context.Background(),
		backgroundTask,
		metadata,
		&activationResult[*schema.Message]{
			control: background.ControlRequest{Kind: "unknown"},
		},
	)
	require.Error(t, err)
}
