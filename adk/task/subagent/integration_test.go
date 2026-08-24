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
	return CompletionWaitInput, nil
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

	require.Eventually(t, func() bool {
		snapshot, getErr := manager.Get(ctx, handle.ID())
		return getErr == nil && snapshot.Status == background.StatusPending
	}, time.Second, time.Millisecond)
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
	barrier := completionBarrierFunc[*schema.Message](func(
		context.Context,
		*CompletionContext[*schema.Message],
	) (CompletionAction, error) {
		return CompletionComplete, nil
	})

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
	closeIntegrationManager(t, manager1)

	manager2 := newIntegrationManager(t, store)
	t.Cleanup(func() { closeIntegrationManager(t, manager2) })
	controller2 := newIntegrationController(
		t, manager2, sessionStore,
		&interruptThenCompleteAgent{name: "worker"}, barrier, testEventMapper,
	)
	handle2, err := controller2.Handle(ctx, handle1.ID())
	require.NoError(t, err)
	require.Equal(t, handle1.ID(), handle2.ID())
	require.Equal(t, handle1.ChildSessionID(), handle2.ChildSessionID())

	require.NoError(t, handle2.SendInput(ctx, &task.Input{
		EventID: "resume-after-restart", Kind: ResumeInputKind,
		Data: []byte(`{"approve":"yes"}`),
	}))
	result, err := controller2.Wait(ctx, handle2.ID())
	require.NoError(t, err)
	require.Equal(t, "approved", result.FinalMessage.Content)

	completed, err := manager2.Get(ctx, handle2.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, completed.Status)
	require.Equal(t, int64(2), completed.Attempt)
	mailbox, err := manager2.GetMailbox(ctx, handle2.ID())
	require.NoError(t, err)
	require.Equal(t, int64(2), mailbox.LatestSequence)
	require.Equal(t, mailbox.LatestSequence, mailbox.ConsumedCursor)
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, nil
		}),
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
