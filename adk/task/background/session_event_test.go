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

package background

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	taskcore "github.com/cloudwego/eino/adk/task"
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

func runInTaskCreatedEventRunner(
	t *testing.T,
	sessionID string,
	callback func(context.Context),
) {
	t.Helper()
	called := false
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "task-created-event-context-agent",
		Instruction: "test",
		Model:       &taskCreatedEventModel{},
		Handlers: []adk.ChatModelAgentMiddleware{&taskCreatedSubmitMiddleware{
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
			submit: func(ctx context.Context) error {
				called = true
				callback(ctx)
				return nil
			},
		}},
	})
	require.NoError(t, err)
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, SessionID: sessionID,
	})
	iterator := runner.Query(context.Background(), "invoke callback")
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	require.True(t, called)
}

func TestTaskCreatedSessionEventSenderRejectsInvalidSession(t *testing.T) {
	sender := TaskCreatedSessionEventSender[*schema.Message]()
	const invalidTaskMessage = "task/background: task id and parent session id are required for task-created event"
	for _, testCase := range []struct {
		name string
		task *TaskSnapshot
	}{
		{name: "nil task"},
		{name: "missing task id", task: &TaskSnapshot{
			Spec: Spec{RootSessionID: "parent-session"},
		}},
		{name: "missing parent session id", task: &TaskSnapshot{
			Spec: Spec{ID: "task"},
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.EqualError(
				t,
				sender(context.Background(), testCase.task),
				invalidTaskMessage,
			)
		})
	}

	task := &TaskSnapshot{Spec: Spec{
		ID: "task", RootSessionID: "parent-session",
	}}
	require.EqualError(
		t,
		sender(context.Background(), task),
		"task/background: task-created event requires the matching parent Runner session",
	)
	runInTaskCreatedEventRunner(t, "other-session", func(ctx context.Context) {
		require.EqualError(
			t,
			sender(ctx, task),
			"task/background: task-created event requires the matching parent Runner session",
		)
	})
}

func TestManagerSubmitAppendsTaskCreatedSessionEvent_BitsUT(t *testing.T) {
	ctx := context.Background()
	const sessionID = "parent-session"
	manager := managerWithTaskCreatedSender(
		t,
		TaskCreatedSessionEventSender[*schema.Message](),
	)
	var task *TaskSnapshot
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "task-created-event-agent",
		Instruction: "test",
		Model:       &taskCreatedEventModel{},
		Handlers: []adk.ChatModelAgentMiddleware{&taskCreatedSubmitMiddleware{
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
			submit: func(runCtx context.Context) error {
				spec := validSpec("task-created")
				spec.RootSessionID = sessionID
				spec.NotifySession = false
				spec.Description = "Research task creation"
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
	var liveEvent *adk.TypedAgentEvent[*schema.Message]
	iterator := runner.Query(ctx, "create task", adk.WithTimelineEvents())
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEventVariant != nil &&
			event.SessionEventVariant.Event != nil &&
			event.SessionEventVariant.Event.Kind == SessionEventTaskCreated {
			liveEvent = event
		}
	}
	require.NotNil(t, task)
	require.NotNil(t, liveEvent)
	require.Equal(t, sessionID, liveEvent.SessionEventVariant.SessionID)
	require.Equal(
		t,
		TaskCreatedSessionEventID(task.Spec.ID),
		liveEvent.SessionEventVariant.Event.EventID,
	)

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
	require.Equal(t, "Research task creation", payload.Description)
}

func TestManagerSubmitReturnsUndeliveredSentinelAfterCreate_BitsUT(t *testing.T) {
	sendErr := errors.New("session timeline unavailable")
	calls := 0
	var sent *TaskSnapshot
	manager := managerWithTaskCreatedSender(
		t,
		func(_ context.Context, task *TaskSnapshot) error {
			calls++
			sent = task
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
	require.EqualError(
		t,
		err,
		`task/background: immediate task-created event was not delivered: `+
			`send task-created session event for "task-created-repair": `+
			`session timeline unavailable`,
	)
	require.NotNil(t, persisted)
	require.Equal(t, spec.ID, persisted.Spec.ID)
	require.Equal(t, StatusPending, persisted.Status)
	require.Equal(t, PublicationOnCreate, persisted.Publication)
	require.NotNil(t, sent)
	require.NotSame(t, persisted, sent)
	require.Equal(t, persisted.Spec, sent.Spec)
	require.Equal(t, persisted.Status, sent.Status)
	require.Equal(t, persisted.Publication, sent.Publication)
	require.Equal(t, persisted.Version, sent.Version)
	require.Equal(t, persisted.CreatedAt, sent.CreatedAt)

	duplicate, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.Nil(t, duplicate)
	require.ErrorIs(t, err, ErrAlreadyExists)
	require.Equal(t, 1, calls)
}

func TestAttack_TaskCreatedFailureLeavesSingleRecoveryRecord(t *testing.T) {
	sendErr := errors.New("session timeline unavailable")
	calls := 0
	store := NewInMemoryStore(nil)
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: store,
		SendTaskCreatedEvent: func(context.Context, *TaskSnapshot) error {
			calls++
			if calls == 1 {
				return sendErr
			}
			return nil
		},
	})
	_, _, err := manager.LoadOrRegisterExecutor(&scriptedExecutor{})
	require.NoError(t, err)
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
	record := recovery.Deliveries[0].Record
	require.Equal(t, spec.ID+":1:"+string(NotificationTaskCreated), record.ID)
	require.Equal(t, NotificationTaskCreated, record.Kind)
	require.Equal(t, spec.ID, record.TaskID)
	require.Equal(t, spec.RootSessionID, record.SessionID)
	require.Equal(t, persisted.Version, record.Version)
	require.Equal(t, taskcore.InputQueued, record.Delivery)
	require.Empty(t, record.Data)
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
	store := NewInMemoryStore(nil)
	manager, err := New(context.Background(), &Config{
		Tasks: store,
	})
	require.NoError(t, err)
	_, _, err = manager.LoadOrRegisterExecutor(&scriptedExecutor{})
	require.NoError(t, err)
	spec := validSpec("missing-task-created-sender")

	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.Nil(t, task)
	require.EqualError(
		t,
		err,
		"task/background: task-created session event sender is required for parent-session tasks",
	)
	_, err = store.Get(context.Background(), spec.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func managerWithTaskCreatedSender(
	t *testing.T,
	sender func(context.Context, *TaskSnapshot) error,
) *Manager {
	t.Helper()
	manager := mustNewManager(t, context.Background(), &Config{
		SendTaskCreatedEvent: sender,
	})
	_, _, err := manager.LoadOrRegisterExecutor(&scriptedExecutor{})
	require.NoError(t, err)
	return manager
}
