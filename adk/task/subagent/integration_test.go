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
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adkinternal "github.com/cloudwego/eino/adk/internal"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type integrationExecutor struct {
	key string
}

func (e *integrationExecutor) Key() string { return e.key }

func (*integrationExecutor) LeaseExpiryPolicy() background.LeaseExpiryPolicy {
	return background.LeaseExpiryRetry
}

func (*integrationExecutor) ValidateSpec(background.Spec) error { return nil }

func (*integrationExecutor) ValidateExecution(
	context.Context,
	*background.TaskSnapshot,
) error {
	return nil
}

func (*integrationExecutor) SupportsDrain() bool { return true }

func (e *integrationExecutor) Execute(
	context.Context,
	*background.TaskSnapshot,
	background.ExecutionRuntime,
) (*background.ExecutionResult, error) {
	return &background.ExecutionResult{
		Action: background.ExecutionActionComplete,
		Data:   []byte("nested result"),
	}, nil
}

type nestedNotificationAgent struct {
	manager          *background.Manager
	childExecutorKey string
	childTaskID      string
	childCreated     chan string
	parentExecution  chan task.ExecutionContext
	executeChild     bool
	runs             int64
}

func (*nestedNotificationAgent) Name(context.Context) string { return "parent-agent" }

func (*nestedNotificationAgent) Description(context.Context) string {
	return "starts a nested durable task"
}

func (a *nestedNotificationAgent) Run(
	ctx context.Context,
	input *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	if atomic.AddInt64(&a.runs, 1) == 1 {
		execution, ok := task.ExecutionContextFromContext(ctx)
		if !ok {
			return integrationAgentError(errors.New("parent execution context is missing"))
		}
		child, err := a.manager.Submit(ctx, &background.SubmitRequest{
			Spec: background.Spec{
				ID:            a.childTaskID,
				ExecutorKey:   a.childExecutorKey,
				Kind:          "integration-child",
				NotifySession: true,
			},
		})
		if err != nil {
			return integrationAgentError(err)
		}
		a.parentExecution <- execution
		a.childCreated <- child.Spec.ID
		if a.executeChild {
			if err = a.manager.Execute(ctx, child.Spec.ID); err != nil {
				return integrationAgentError(err)
			}
		}
	}
	content := "started"
	if input != nil && len(input.Messages) > 0 {
		content = input.Messages[len(input.Messages)-1].Content
	}
	return integrationAgentMessage(a.Name(ctx), content)
}

func (a *nestedNotificationAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

type blockingCaptureAgent struct {
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	runs        int64

	mu               sync.Mutex
	messages         map[string]string
	identityConflict bool
}

type checkpointAuditPreemptModel struct {
	firstStarted  chan struct{}
	queuedStarted chan struct{}
	calls         int64
}

func (m *checkpointAuditPreemptModel) Generate(
	ctx context.Context,
	messages []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	call := atomic.AddInt64(&m.calls, 1)
	input := ""
	for _, message := range messages {
		if message.Role == schema.User {
			input = message.Content
		}
	}
	switch call {
	case 1:
		close(m.firstStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	case 3:
		close(m.queuedStarted)
		return schema.AssistantMessage(input, nil), nil
	default:
		return schema.AssistantMessage(input, nil), nil
	}
}

func (m *checkpointAuditPreemptModel) Stream(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (*blockingCaptureAgent) Name(context.Context) string { return "concurrent-agent" }

func (*blockingCaptureAgent) Description(context.Context) string {
	return "captures concurrent continuation inputs"
}

func (a *blockingCaptureAgent) Run(
	ctx context.Context,
	input *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.mu.Lock()
	for _, message := range input.Messages {
		if len(message.Content) < len("message-") ||
			message.Content[:len("message-")] != "message-" {
			continue
		}
		id := adkinternal.GetMessageID(message.Extra)
		if id == "" {
			a.identityConflict = true
			continue
		}
		if a.messages == nil {
			a.messages = make(map[string]string)
		}
		if existing, ok := a.messages[id]; ok && existing != message.Content {
			a.identityConflict = true
		}
		a.messages[id] = message.Content
	}
	a.mu.Unlock()
	if atomic.AddInt64(&a.runs, 1) == 1 {
		close(a.started)
		select {
		case <-a.release:
		case <-ctx.Done():
			return integrationAgentError(ctx.Err())
		}
	}
	return integrationAgentMessage(a.Name(ctx), "done")
}

func (a *blockingCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func (a *blockingCaptureAgent) unblock() {
	a.releaseOnce.Do(func() { close(a.release) })
}

func (a *blockingCaptureAgent) capturedMessages() ([]string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	messages := make([]string, 0, len(a.messages))
	for _, content := range a.messages {
		messages = append(messages, content)
	}
	return messages, a.identityConflict
}

func awaitIntegrationValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for integration test value")
		var zero T
		return zero
	}
}

func integrationAgentMessage(name, content string) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage(content, nil), nil, schema.Assistant, name,
	))
	generator.Close()
	return iter
}

func integrationAgentError(err error) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Err: err})
	generator.Close()
	return iter
}

func newIntegrationManager(
	t *testing.T,
	store *background.InMemoryStore,
) *background.Manager {
	t.Helper()
	manager, err := background.New(context.Background(), &background.Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	return manager
}

func newIntegrationController(
	t *testing.T,
	manager *background.Manager,
	sessionStore *adksession.InMemoryStore[*schema.Message],
	agent adk.ResumableAgent,
	barrier CompletionBarrier[*schema.Message],
	mapper InputsToAgentInput[*schema.Message],
) *Controller[*schema.Message] {
	t.Helper()
	controller, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager, Barrier: barrier, InputsToAgentInput: mapper,
		SessionStore: sessionStore, CheckPointStore: sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, controller.RegisterAgent(agent.Name(context.Background()),
		&AgentRegistration[*schema.Message]{Agent: agent}))
	return controller
}

func closeIntegrationManager(t *testing.T, manager *background.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Close(ctx))
}

func notificationInputMapper(
	_ context.Context,
	inputs []*task.InputRecord,
) (*adk.AgentInput, error) {
	messages := make([]*schema.Message, 0, len(inputs))
	for _, input := range inputs {
		messages = append(messages, schema.UserMessage(input.Kind))
	}
	return &adk.AgentInput{Messages: messages}, nil
}

func completeAfterNestedTask(
	_ context.Context,
	completion *CompletionContext[*schema.Message],
) (CompletionAction, error) {
	if completion.FinalMessage != nil &&
		completion.FinalMessage.Content == string(background.NotificationCompleted) {
		return CompletionComplete, nil
	}
	return CompletionSuspend, nil
}

func TestIntegration_ForegroundHandoffConsumesNestedCompletion(t *testing.T) {
	ctx := context.Background()
	store := background.NewInMemoryStore(nil)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	manager := newIntegrationManager(t, store)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })

	childExecutor := &integrationExecutor{key: "integration-child"}
	_, loaded, err := manager.LoadOrRegisterExecutor(childExecutor)
	require.NoError(t, err)
	require.False(t, loaded)
	agent := &nestedNotificationAgent{
		manager: manager, childExecutorKey: childExecutor.Key(),
		childTaskID: "nested-child", childCreated: make(chan string, 1),
		parentExecution: make(chan task.ExecutionContext, 1),
	}
	controller := newIntegrationController(
		t, manager, sessionStore, agent,
		completionBarrierFunc[*schema.Message](completeAfterNestedTask),
		notificationInputMapper,
	)

	handle, err := controller.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "root:parent", ParentSessionID: "root-session",
		AgentName: agent.Name(ctx), StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("start")},
		},
	})
	require.NoError(t, err)
	childID := awaitIntegrationValue(t, agent.childCreated)
	staleParentExecution := awaitIntegrationValue(t, agent.parentExecution)

	require.Eventually(t, func() bool {
		snapshot, getErr := manager.Get(ctx, handle.ID())
		return getErr == nil && snapshot.Status == background.StatusSuspended
	}, time.Second, time.Millisecond)
	parentMailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, task.MailboxBackground, parentMailbox.State)
	require.Greater(t, parentMailbox.Generation, staleParentExecution.Generation)

	staleCtx := task.WithExecutionContext(ctx, staleParentExecution)
	_, err = manager.Submit(staleCtx, &background.SubmitRequest{
		Spec: background.Spec{
			ID: "stale-child", ExecutorKey: childExecutor.Key(),
			Kind: "integration-child",
		},
	})
	require.ErrorIs(t, err, task.ErrOwnershipLost)

	require.NoError(t, manager.Execute(ctx, childID))
	child, err := manager.Get(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, child.Status)
	require.Equal(t, handle.ID(), child.Spec.ParentTaskID)
	require.Equal(t, "root-session", child.Spec.RootSessionID)

	suspended, err := manager.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusSuspended, suspended.Status)
	released, err := manager.ReleaseSuspension(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, released.Status)
	require.NoError(t, manager.Execute(ctx, handle.ID()))

	result, err := controller.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, string(background.NotificationCompleted), result.FinalMessage.Content)
	require.Equal(t, handle.ID(), result.Handle.ID())
	require.Equal(t, handle.ChildSessionID(), result.Handle.ChildSessionID())

	parent, err := manager.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, parent.Status)
	require.Equal(t, int64(1), parent.Attempt)
	inputs, err := manager.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: handle.ID(),
	})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, 3)
	require.Equal(t, initialSignalKind, inputs.Inputs[0].Kind)
	require.Equal(t, string(background.NotificationTaskCreated), inputs.Inputs[1].Kind)
	require.Equal(t, string(background.NotificationCompleted), inputs.Inputs[2].Kind)
	require.Equal(t, inputs.LatestSequence, inputs.ConsumedCursor)
	outbox, err := store.Receive(ctx, &background.ReceiveNotificationsRequest{
		Limit: 10, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Len(t, outbox.Deliveries, 2)
	for _, delivery := range outbox.Deliveries {
		require.Equal(t, handle.ID(), delivery.Record.TaskID)
	}
}

func TestIntegration_ForegroundConsumesNestedCompletionWithoutHandoff(t *testing.T) {
	ctx := context.Background()
	store := background.NewInMemoryStore(nil)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	manager := newIntegrationManager(t, store)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })

	childExecutor := &integrationExecutor{key: "integration-child"}
	_, loaded, err := manager.LoadOrRegisterExecutor(childExecutor)
	require.NoError(t, err)
	require.False(t, loaded)
	agent := &nestedNotificationAgent{
		manager: manager, childExecutorKey: childExecutor.Key(),
		childTaskID: "nested-child", childCreated: make(chan string, 1),
		parentExecution: make(chan task.ExecutionContext, 1),
		executeChild:    true,
	}
	controller := newIntegrationController(
		t, manager, sessionStore, agent,
		completionBarrierFunc[*schema.Message](completeAfterNestedTask),
		notificationInputMapper,
	)

	handle, err := controller.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "root:attached-parent", ParentSessionID: "root-session",
		AgentName: agent.Name(ctx), StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("start")},
		},
	})
	require.NoError(t, err)
	childID := awaitIntegrationValue(t, agent.childCreated)
	awaitIntegrationValue(t, agent.parentExecution)

	result, err := controller.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, string(background.NotificationCompleted), result.FinalMessage.Content)
	require.Equal(t, int64(2), atomic.LoadInt64(&agent.runs))

	_, err = manager.Get(ctx, handle.ID())
	require.ErrorIs(t, err, background.ErrNotFound)
	parentMailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, parentMailbox.State)
	require.Equal(t, int64(3), parentMailbox.ConsumedCursor)

	child, err := manager.Get(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, child.Status)
	require.Equal(t, handle.ID(), child.Spec.ParentTaskID)
	require.Equal(t, "root-session", child.Spec.RootSessionID)
	outbox, err := store.Receive(ctx, &background.ReceiveNotificationsRequest{
		Limit: 10, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Empty(t, outbox.Deliveries)
}

func TestIntegration_BackgroundResumeSurvivesControllerRestart(t *testing.T) {
	ctx := context.Background()
	store := background.NewInMemoryStore(nil)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	barrier := completeBarrier[*schema.Message]()

	manager1 := newIntegrationManager(t, store)
	controller1 := newIntegrationController(
		t, manager1, sessionStore,
		&interruptThenCompleteAgent{name: "worker"}, barrier, testEventMapper,
	)
	handle1, err := controller1.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "root:restart", ParentSessionID: "root-session",
		AgentName: "worker", StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot, getErr := manager1.Get(ctx, handle1.ID())
		return getErr == nil && snapshot.Status == background.StatusWaitingInput
	}, time.Second, time.Millisecond)
	waiting, err := manager1.Get(ctx, handle1.ID())
	require.NoError(t, err)
	require.Equal(t, int64(1), waiting.Attempt)
	require.NotEmpty(t, waiting.Checkpoint)
	durableResume, err := decodeRuntimeCheckpoint[*schema.Message](waiting.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, runtimeCheckpointResume, durableResume.Mode)
	require.Len(t, durableResume.TargetIDs, 1)
	closeIntegrationManager(t, manager1)

	manager2 := newIntegrationManager(t, store)
	t.Cleanup(func() { closeIntegrationManager(t, manager2) })
	resumeInfos := make(chan *adk.ResumeInfo, 1)
	controller2 := newIntegrationController(
		t, manager2, sessionStore,
		&interruptThenCompleteAgent{
			name: "worker", resumeInfos: resumeInfos,
		},
		barrier,
		testEventMapper,
	)
	handle2, err := controller2.Handle(ctx, handle1.ID())
	require.NoError(t, err)
	require.Equal(t, handle1.ID(), handle2.ID())
	require.Equal(t, handle1.ChildSessionID(), handle2.ChildSessionID())

	resumePayload, err := json.Marshal(map[string]any{
		durableResume.TargetIDs[0]: "approved",
	})
	require.NoError(t, err)
	require.NoError(t, handle2.SendInput(ctx, &task.Input{
		EventID: "resume-after-restart", Kind: ResumeInputKind,
		Data: resumePayload,
	}))
	result, err := controller2.Wait(ctx, handle2.ID())
	require.NoError(t, err)
	require.Equal(t, "approved", result.FinalMessage.Content)
	resumeInfo := awaitIntegrationValue(t, resumeInfos)
	require.True(t, resumeInfo.WasInterrupted)
	require.True(t, resumeInfo.IsResumeTarget)
	require.Equal(t, "approved", resumeInfo.ResumeData)

	completed, err := manager2.Get(ctx, handle2.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, completed.Status)
	require.Equal(t, int64(2), completed.Attempt)
	mailbox, err := manager2.GetMailbox(ctx, handle2.ID())
	require.NoError(t, err)
	require.Equal(t, int64(2), mailbox.LatestSequence)
	require.Equal(t, mailbox.LatestSequence, mailbox.ConsumedCursor)
	inputs, err := manager2.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: handle2.ID(),
	})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, 2)
	require.Equal(t, ResumeInputKind, inputs.Inputs[1].Kind)
	decodedResume, err := decodeRuntimeResumeTargets(inputs.Inputs[1].Data)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		durableResume.TargetIDs[0]: "approved",
	}, decodedResume)
}

func TestIntegration_AttachedPreemptPreservesQueuedInputAcrossRecovery( //nolint:funlen // Exercises the full handoff and recovery boundary.
	t *testing.T,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	memoryStore := background.NewInMemoryStore(nil)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	model := &checkpointAuditPreemptModel{
		firstStarted:  make(chan struct{}),
		queuedStarted: make(chan struct{}),
	}
	baseAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "worker", Description: "checkpoint audit preempt", Model: model,
	})
	require.NoError(t, err)
	agent := &runResumeCountingAgent{ResumableAgent: baseAgent}
	var barrierCalls int32
	barrier := completionBarrierFunc[*schema.Message](func(
		context.Context,
		*CompletionContext[*schema.Message],
	) (CompletionAction, error) {
		if atomic.AddInt32(&barrierCalls, 1) == 1 {
			return CompletionSuspend, nil
		}
		return CompletionComplete, nil
	})
	manager1, err := background.New(ctx, &background.Config{
		Tasks: memoryStore, TaskEvents: memoryStore,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	controller1, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager1, Barrier: barrier,
		InputsToAgentInput: testEventMapper,
		SessionStore:       sessionStore, CheckPointStore: sessionStore,
		DrainCancelTimeout: 5 * time.Millisecond,
		InputPreemptPolicy: func(
			context.Context,
			*task.InputRecord,
			*adk.TurnContext[*task.InputRecord, *schema.Message],
		) []adk.PushOption[*task.InputRecord, *schema.Message] {
			return []adk.PushOption[*task.InputRecord, *schema.Message]{
				adk.WithPreemptTimeout[*task.InputRecord, *schema.Message](
					adk.AnySafePoint,
					5*time.Millisecond,
				),
			}
		},
	})
	require.NoError(t, err)
	require.NoError(t, controller1.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	handle, err := controller1.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "audit:preempt", ParentSessionID: "root-session",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("sequence-1")},
		},
	})
	require.NoError(t, err)
	awaitIntegrationValue(t, model.firstStarted)
	require.NoError(t, controller1.SendInput(ctx, handle.ID(), &task.Input{
		EventID: "sequence-2", Kind: messageInputKind,
		Data: mustEncodeAgentInput(t, "sequence-2"),
	}))
	require.NoError(t, controller1.SendInput(ctx, handle.ID(), &task.Input{
		EventID: "sequence-3", Kind: messageInputKind,
		Data: mustEncodeAgentInput(t, "sequence-3"), Delivery: task.InputPreempt,
	}))
	awaitIntegrationValue(t, model.queuedStarted)
	require.Eventually(t, func() bool {
		snapshot, getErr := memoryStore.Get(ctx, handle.ID())
		return getErr == nil && snapshot.Status == background.StatusSuspended
	}, time.Second, time.Millisecond)

	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	require.NoError(t, manager1.Close(closeCtx))
	closeCancel()
	suspended, err := manager1.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusSuspended, suspended.Status)
	suspendedCheckpoint, err := decodeRuntimeCheckpoint[*schema.Message](
		suspended.Checkpoint,
	)
	require.NoError(t, err)
	require.Equal(t, int64(3), suspendedCheckpoint.InputCursor)
	require.Empty(t, suspendedCheckpoint.SparseAcks)

	manager2 := newIntegrationManager(t, memoryStore)
	t.Cleanup(func() { closeIntegrationManager(t, manager2) })
	controller2, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager2, Barrier: barrier,
		InputsToAgentInput: testEventMapper,
		SessionStore:       sessionStore, CheckPointStore: sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, controller2.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	released, err := manager2.ReleaseSuspension(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, released.Status)
	require.NoError(t, manager2.Execute(ctx, handle.ID()))
	result, err := controller2.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.NotNil(t, result.FinalMessage)
	runInputs := agent.runInputs()
	runInputIDs := agent.runInputIDs()
	eventMessageIDs := make(map[string]map[string]struct{})
	for callIndex, input := range runInputs {
		for messageIndex, content := range input {
			if eventMessageIDs[content] == nil {
				eventMessageIDs[content] = make(map[string]struct{})
			}
			eventMessageIDs[content][runInputIDs[callIndex][messageIndex]] = struct{}{}
		}
	}
	require.Equal(
		t, 1, len(eventMessageIDs["sequence-2"]),
		"agent inputs: %#v, IDs: %#v", runInputs, runInputIDs,
	)
	require.Equal(
		t, 1, len(eventMessageIDs["sequence-3"]),
		"agent inputs: %#v, IDs: %#v", runInputs, runInputIDs,
	)
	require.NotContains(t, eventMessageIDs["sequence-2"], "")
	require.NotContains(t, eventMessageIDs["sequence-3"], "")
	mailbox, err := manager2.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, int64(3), mailbox.ConsumedCursor)
	inputs, err := manager2.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: handle.ID(),
	})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, 3)
	require.Equal(t, []string{
		"audit:preempt:initial", "sequence-2", "sequence-3",
	}, []string{
		inputs.Inputs[0].EventID,
		inputs.Inputs[1].EventID,
		inputs.Inputs[2].EventID,
	})
	finalTask, err := manager2.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Empty(t, finalTask.Checkpoint)
}

func TestIntegration_BetweenTurnCheckpointSkipsSparseAckOnRecovery( //nolint:funlen // Constructs the persisted crash boundary explicitly.
	t *testing.T,
) {
	ctx := context.Background()
	store := background.NewInMemoryStore(nil)
	manager := newIntegrationManager(t, store)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &runResumeCountingAgent{
		ResumableAgent: &resumableTestAgent{name: "worker"},
	}
	controller := newIntegrationController(
		t, manager, sessionStore, agent,
		completeBarrier[*schema.Message](), testEventMapper,
	)

	const taskID = "between-turn-sparse-ack"
	metadata := &runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "root-session",
		RootSessionID: "root-session", ChildSessionID: taskID + "-child",
		AgentName: "worker", StartMode: task.StartModeBackground,
	}
	identity, err := json.Marshal(metadata)
	require.NoError(t, err)
	reserved, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: taskID, InvocationID: taskID, Identity: identity,
		RootSessionID: metadata.RootSessionID, ChildSessionID: metadata.ChildSessionID,
	})
	require.NoError(t, err)
	require.True(t, reserved.Created)

	for sequence, content := range []string{"sequence-1", "sequence-2", "sequence-3"} {
		_, err = manager.SendInput(ctx, &task.SendInputRequest{
			TaskID: taskID,
			Input: task.Input{
				EventID: fmt.Sprintf("sequence-%d", sequence+1),
				Kind:    messageInputKind,
				Data:    mustEncodeAgentInput(t, content),
			},
		})
		require.NoError(t, err)
	}
	require.NoError(t, store.AdvanceCursor(ctx, &task.AdvanceCursorRequest{
		TaskID: taskID, ExpectedCursor: 0, Cursor: 1,
		ExpectedGeneration: reserved.Mailbox.Generation,
	}))
	inputs, err := manager.ListInputs(ctx, &task.ListInputsRequest{TaskID: taskID})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, 3)

	sequence2 := *inputs.Inputs[1]
	sequence2.Data = append([]byte(nil), sequence2.Data...)
	var turnLoopCheckpoint bytes.Buffer
	require.NoError(t, gob.NewEncoder(&turnLoopCheckpoint).Encode(&struct {
		RunnerCheckpointID string
		RunnerCheckpoint   []byte
		HasRunnerState     bool
		UnhandledItems     []*task.InputRecord
		ResumeItems        []*task.InputRecord
		CanceledItems      []*task.InputRecord
	}{
		UnhandledItems: []*task.InputRecord{&sequence2},
	}))
	runtimeCheckpoint, err := encodeRuntimeCheckpointState[*schema.Message](
		1, []int64{3}, nil, turnLoopCheckpoint.Bytes(),
	)
	require.NoError(t, err)
	payload, err := json.Marshal(&taskPayload{
		Version: payloadVersion, SubAgentName: metadata.AgentName,
		ChildSessionID: metadata.ChildSessionID,
	})
	require.NoError(t, err)
	pending, err := manager.AdoptForeground(ctx, &background.AdoptForegroundRequest{
		Spec: background.Spec{
			ID: taskID, ExecutorKey: ExecutorKey, Kind: "subagent",
			Payload: payload, RootSessionID: metadata.RootSessionID,
		},
		ExpectedGeneration: reserved.Mailbox.Generation,
		InputCursor:        1,
		InitialCheckpoint:  runtimeCheckpoint,
		StartPending:       true,
	})
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, pending.Status)

	require.NoError(t, manager.Execute(ctx, taskID))
	result, err := controller.Wait(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)
	require.Equal(t, [][]string{{"sequence-2"}}, agent.runInputs())
	runInputIDs := agent.runInputIDs()
	require.Len(t, runInputIDs, 1)
	require.Len(t, runInputIDs[0], 1)
	require.NotEmpty(t, runInputIDs[0][0])

	mailbox, err := manager.GetMailbox(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, int64(3), mailbox.ConsumedCursor)
	completed, err := manager.Get(ctx, taskID)
	require.NoError(t, err)
	require.Empty(t, completed.Checkpoint)
}

func mustEncodeAgentInput(t *testing.T, content string) []byte {
	t.Helper()
	encoded, err := encodeTypedInput(&adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage(content)},
	})
	require.NoError(t, err)
	data, err := json.Marshal(encoded)
	require.NoError(t, err)
	return data
}

func TestIntegration_ConcurrentContinueKeepsSingleActiveTask(t *testing.T) {
	ctx := context.Background()
	store := background.NewInMemoryStore(nil)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	manager := newIntegrationManager(t, store)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })
	agent := &blockingCaptureAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(agent.unblock)
	controller := newIntegrationController(
		t, manager, sessionStore, agent,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)

	const callers = 16
	type continueResult struct {
		handle *Handle
		err    error
	}
	start := make(chan struct{})
	results := make(chan continueResult, callers)
	var group sync.WaitGroup
	for i := 0; i < callers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			handle, err := controller.Continue(ctx, &ContinueRequest[*schema.Message]{
				ChildSessionID: "shared-child",
				InvocationID:   fmt.Sprintf("root:continue:%02d", index),
				Input: &adk.AgentInput{
					Messages: []*schema.Message{
						schema.UserMessage(fmt.Sprintf("message-%02d", index)),
					},
				},
				IfIdle: &StartOptions[*schema.Message]{
					ParentSessionID: "root-session",
					AgentName:       agent.Name(ctx),
					StartMode:       task.StartModeForeground,
				},
			})
			results <- continueResult{handle: handle, err: err}
		}(i)
	}
	close(start)
	group.Wait()
	close(results)

	taskIDs := make(map[string]struct{})
	var handle *Handle
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.handle)
		taskIDs[result.handle.ID()] = struct{}{}
		handle = result.handle
	}
	require.Len(t, taskIDs, 1)
	require.Equal(t, "shared-child", handle.ChildSessionID())
	awaitIntegrationValue(t, agent.started)

	inputs, err := manager.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: handle.ID(),
	})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, callers)
	eventIDs := make(map[string]struct{}, callers)
	for _, input := range inputs.Inputs {
		eventIDs[input.EventID] = struct{}{}
	}
	require.Len(t, eventIDs, callers)

	agent.unblock()
	result, err := controller.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)

	actual, identityConflict := agent.capturedMessages()
	require.False(t, identityConflict)
	sort.Strings(actual)
	expected := make([]string, callers)
	for i := 0; i < callers; i++ {
		expected[i] = fmt.Sprintf("message-%02d", i)
	}
	require.Equal(t, expected, actual)
	mailbox, err := manager.GetMailbox(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, task.MailboxSealed, mailbox.State)
	require.Equal(t, int64(callers), mailbox.ConsumedCursor)
}
