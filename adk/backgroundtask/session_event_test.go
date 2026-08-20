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

package backgroundtask

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type taskCreatedEventModel struct{}

type taskCreatedSubmitMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	submit func(context.Context) error
}

func (m *taskCreatedSubmitMiddleware) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	return ctx, state, m.submit(ctx)
}

func (*taskCreatedEventModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return schema.AssistantMessage("done", nil), nil
}

func (*taskCreatedEventModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := (&taskCreatedEventModel{}).Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestManagerSubmitAppendsTaskCreatedSessionEvent_BitsUT(t *testing.T) {
	ctx := context.Background()
	const sessionID = "parent-session"
	manager := managerWithTaskCreatedSender(
		t,
		TaskCreatedSessionEventSender[*schema.Message](),
	)
	var task *Task
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "task-created-event-agent",
		Instruction: "test",
		Model:       &taskCreatedEventModel{},
		Handlers: []adk.ChatModelAgentMiddleware{&taskCreatedSubmitMiddleware{
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
			submit: func(runCtx context.Context) error {
				spec := validSpec("task-created")
				spec.SessionID = sessionID
				spec.NotifySession = false
				var submitErr error
				task, submitErr = manager.Submit(runCtx, &SubmitRequest{Spec: spec})
				return submitErr
			},
		}},
	})
	require.NoError(t, err)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent: agent, SessionID: sessionID, SessionStore: sessionStore,
	})
	iterator := runner.Query(ctx, "create task")
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	require.NotNil(t, task)

	result, err := sessionStore.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{
		Kinds: []adk.SessionEventKind{SessionEventTaskCreated},
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	event := result.Events[0]
	require.True(t, task.CreatedAt.Equal(event.Timestamp))
	require.Equal(t, SessionEventTaskCreated, event.Kind)
	require.Equal(t, TaskCreatedSessionEventID(task.Spec.ID), event.EventID)
	require.NotEmpty(t, event.TurnID)
	require.NotNil(t, event.Extension)
	payload, ok := event.Extension.Data.(*TaskCreatedSessionEvent)
	require.True(t, ok)
	require.Equal(t, task.Spec.ID, payload.TaskID)
}

func TestManagerSubmitReturnsUndeliveredSentinelAfterCreate_BitsUT(t *testing.T) {
	sendErr := errors.New("session timeline unavailable")
	calls := 0
	manager := managerWithTaskCreatedSender(
		t,
		func(context.Context, *Task) error {
			calls++
			if calls == 1 {
				return sendErr
			}
			return nil
		},
	)
	spec := validSpec("task-created-repair")

	persisted, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.ErrorIs(t, err, ErrTaskCreatedEventUndelivered)
	require.ErrorIs(t, err, sendErr)
	require.NotNil(t, persisted)
	require.Equal(t, spec.ID, persisted.Spec.ID)

	duplicate, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.Nil(t, duplicate)
	require.ErrorIs(t, err, ErrAlreadyExists)
	require.Equal(t, 1, calls)
}

func TestAttack_TaskCreatedFailureLeavesSingleRecoveryRecord(t *testing.T) {
	sendErr := errors.New("session timeline unavailable")
	calls := 0
	registry := NewExecutorRegistry()
	require.NoError(t, registry.Register(&scriptedExecutor{}))
	store := NewInMemoryStore(nil)
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: store, Executors: registry,
		SendTaskCreatedEvent: func(context.Context, *Task) error {
			calls++
			if calls == 1 {
				return sendErr
			}
			return nil
		},
	})
	spec := validSpec("task-created-recovery")

	persisted, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.ErrorIs(t, err, ErrTaskCreatedEventUndelivered)
	require.ErrorIs(t, err, sendErr)
	require.NotNil(t, persisted)
	recovery, err := store.Receive(
		context.Background(),
		&ReceiveNotificationsRequest{Limit: 10, LeaseDuration: time.Second},
	)
	require.NoError(t, err)
	require.Len(t, recovery.Deliveries, 1)
	require.Equal(t, NotificationTaskCreated, recovery.Deliveries[0].Record.Kind)
	require.Equal(t, spec.ID, recovery.Deliveries[0].Record.TaskID)
	require.Equal(t, spec.SessionID, recovery.Deliveries[0].Record.SessionID)
	require.NoError(t, store.Ack(context.Background(), recovery.Deliveries[0].Receipt))

	retried, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.Nil(t, retried)
	require.ErrorIs(t, err, ErrAlreadyExists)
	remaining, err := store.Receive(
		context.Background(),
		&ReceiveNotificationsRequest{Limit: 10, LeaseDuration: time.Second},
	)
	require.NoError(t, err)
	require.Empty(t, remaining.Deliveries)
	require.Equal(t, 1, calls)
	t.Log("sender failure retained exactly one durable TaskCreated recovery record")
}

func TestManagerSubmitRequiresTaskCreatedSenderBeforeCreate_BitsUT(t *testing.T) {
	registry := NewExecutorRegistry()
	require.NoError(t, registry.Register(&scriptedExecutor{}))
	store := NewInMemoryStore(nil)
	manager, err := New(context.Background(), &Config{
		Tasks: store, Executors: registry,
	})
	require.NoError(t, err)
	spec := validSpec("missing-task-created-sender")

	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.Nil(t, task)
	require.EqualError(
		t,
		err,
		"backgroundtask: task-created session event sender is required for parent-session tasks",
	)
	_, err = store.Get(context.Background(), spec.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func managerWithTaskCreatedSender(
	t *testing.T,
	sender func(context.Context, *Task) error,
) *Manager {
	t.Helper()
	registry := NewExecutorRegistry()
	require.NoError(t, registry.Register(&scriptedExecutor{}))
	return mustNewManager(t, context.Background(), &Config{
		Executors: registry, SendTaskCreatedEvent: sender,
	})
}
