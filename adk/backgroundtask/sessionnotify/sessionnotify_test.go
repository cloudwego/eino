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

package sessionnotify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/schema"
)

type recordingActivator struct {
	inbox       backgroundtask.SessionNotificationInbox
	err         error
	sessionID   string
	pendingSeen int
}

func TestRuntimeValidatesCompleteDeliveryRoute(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(nil)
	inbox := NewMemoryInbox()
	sink, err := NewSink(inbox, &recordingActivator{inbox: inbox})
	require.NoError(t, err)
	sinks := backgroundtask.NewSinkRegistry()
	require.NoError(t, sinks.Register(backgroundtask.SessionInboxNotificationKind, sink))
	readyCalls := 0
	runtime, err := NewRuntime(
		sinks,
		func(context.Context) error {
			readyCalls++
			return nil
		},
	)
	require.NoError(t, err)
	require.NoError(t, runtime.ValidateNotificationDelivery(
		context.Background(),
		&backgroundtask.NotificationDeliveryValidation{
			Store: store, TargetKind: backgroundtask.SessionInboxNotificationKind,
		},
	))
	assert.Equal(t, 1, readyCalls)
}

func TestRuntimeRejectsIncompleteDeliveryRoute(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(nil)
	runtime, err := NewRuntime(
		backgroundtask.NewSinkRegistry(),
		func(context.Context) error { return nil },
	)
	require.NoError(t, err)
	err = runtime.ValidateNotificationDelivery(
		context.Background(),
		&backgroundtask.NotificationDeliveryValidation{
			Store: store, TargetKind: backgroundtask.SessionInboxNotificationKind,
		},
	)
	require.ErrorContains(t, err, "sink is unavailable")

	_, err = NewRuntime(backgroundtask.NewSinkRegistry(), nil)
	require.ErrorContains(t, err, "readiness check")

	_, err = NewSink(nil, nil)
	require.ErrorContains(t, err, "inbox and activator are required")
}

func (a *recordingActivator) RequestTurn(
	ctx context.Context,
	req *backgroundtask.SessionActivationRequest,
) (*backgroundtask.SessionActivationResult, error) {
	a.sessionID = req.SessionID
	pending, err := a.inbox.ListPending(ctx, &backgroundtask.ListSessionNotificationsRequest{
		SessionID: req.SessionID,
	})
	if err != nil {
		return nil, err
	}
	a.pendingSeen = len(pending)
	if a.err != nil {
		return nil, a.err
	}
	return &backgroundtask.SessionActivationResult{
		Disposition: backgroundtask.SessionActivationQueued,
	}, nil
}

func TestSinkAcceptRejectsUnroutedNotification(t *testing.T) {
	err := (&Sink{}).Accept(context.Background(), backgroundtask.Notification{ID: "notification-1"})
	require.ErrorContains(t, err, "routed notification target is required")
}

func TestSinkAcceptTargetValidatesDependenciesAndIdentity(t *testing.T) {
	err := (&Sink{}).AcceptTarget(context.Background(), backgroundtask.NotificationTarget{
		Kind: "session_inbox", TargetID: "session-1",
	}, backgroundtask.Notification{ID: "notification-1"})
	require.ErrorContains(t, err, "inbox and activator are required")

	inbox := NewMemoryInbox()
	sink, sinkErr := NewSink(inbox, &recordingActivator{inbox: inbox})
	require.NoError(t, sinkErr)
	err = sink.AcceptTarget(
		context.Background(),
		backgroundtask.NotificationTarget{Kind: "session_inbox", TargetID: "session-1"},
		backgroundtask.Notification{},
	)
	require.ErrorContains(t, err, "notification id and target are required")
}

func TestSinkAcceptTargetEnqueuesBeforeActivation_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	activator := &recordingActivator{inbox: inbox}
	sink, err := NewSink(inbox, activator)
	require.NoError(t, err)

	err = sink.AcceptTarget(context.Background(), backgroundtask.NotificationTarget{
		Kind: "session_inbox", TargetID: "session-1",
	}, backgroundtask.Notification{
		ID: "notification-1", TaskID: "task-1",
		Kind: backgroundtask.NotificationCompleted,
		Task: &backgroundtask.Task{
			Spec:       backgroundtask.Spec{ID: "task-1"},
			Status:     backgroundtask.StatusCompleted,
			ResultData: []byte("done"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "session-1", activator.sessionID)
	assert.Equal(t, 1, activator.pendingSeen)
}

func TestSinkAcceptTargetActivationFailureRetainsInboxItem_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	wantErr := errors.New("activation unavailable")
	sink, err := NewSink(inbox, &recordingActivator{inbox: inbox, err: wantErr})
	require.NoError(t, err)

	err = sink.AcceptTarget(context.Background(), backgroundtask.NotificationTarget{
		Kind: "session_inbox", TargetID: "session-1",
	}, backgroundtask.Notification{ID: "notification-1"})
	assert.ErrorIs(t, err, wantErr)

	pending, listErr := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, listErr)
	require.Len(t, pending, 1)
	assert.Equal(t, "notification-1", pending[0].Notification.ID)
}

func TestTerminalTaskNotificationWakesParentSession_BitsUT(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(nil)
	spec := backgroundtask.Spec{
		ID: "task-1", ExecutorKey: "test", Payload: []byte("{}"),
		SessionID: "session-1",
		Notify:    &backgroundtask.NotificationTarget{Kind: "session_inbox", TargetID: "session-1"},
	}
	created, err := store.Create(context.Background(), &backgroundtask.CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: backgroundtask.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	started, err := store.Start(context.Background(), &backgroundtask.StartTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	_, err = store.Complete(context.Background(), &backgroundtask.CompleteTaskRequest{
		TaskID: spec.ID, ExpectedVersion: started.Version, Data: []byte("done"),
	})
	require.NoError(t, err)

	inbox := NewMemoryInbox()
	activator := &recordingActivator{inbox: inbox}
	registry := backgroundtask.NewSinkRegistry()
	sink, err := NewSink(inbox, activator)
	require.NoError(t, err)
	require.NoError(t, registry.Register(backgroundtask.SessionInboxNotificationKind, sink))
	dispatcher := &backgroundtask.Dispatcher{
		Outbox: store, Store: store, Sinks: registry, ConsumerID: "dispatcher",
		BatchSize: 10, Visibility: time.Minute,
	}
	delivered, err := dispatcher.DispatchOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, delivered)
	assert.Equal(t, spec.SessionID, activator.sessionID)

	pending, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: spec.SessionID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var completion *backgroundtask.SessionInboxItem
	for _, item := range pending {
		if item.Notification.Kind == backgroundtask.NotificationCompleted {
			completion = item
		}
	}
	require.NotNil(t, completion)
	require.NotNil(t, completion.Notification.Task)
	assert.Equal(t, "done", string(completion.Notification.Task.ResultData))
}

func TestMemoryInboxDeduplicatesAcrossAckAndRedelivery_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	request := &backgroundtask.EnqueueSessionNotificationRequest{
		SessionID: "session-1",
		Notification: backgroundtask.Notification{
			ID: "notification-1", TaskID: "task-1",
		},
	}
	first, err := inbox.Enqueue(context.Background(), request)
	require.NoError(t, err)
	duplicate, err := inbox.Enqueue(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, first.ItemID, duplicate.ItemID)

	require.NoError(t, inbox.Ack(context.Background(), &backgroundtask.AckSessionNotificationRequest{
		SessionID: first.SessionID, ItemID: first.ItemID, ExpectedVersion: first.ItemVersion,
	}))
	redelivery, err := inbox.Enqueue(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, first.ItemID, redelivery.ItemID)

	pending, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestMemoryInboxAckUsesVersionCAS_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	item, err := inbox.Enqueue(context.Background(), &backgroundtask.EnqueueSessionNotificationRequest{
		SessionID: "session-1",
		Notification: backgroundtask.Notification{
			ID: "notification-1",
		},
	})
	require.NoError(t, err)

	err = inbox.Ack(context.Background(), &backgroundtask.AckSessionNotificationRequest{
		SessionID: item.SessionID, ItemID: item.ItemID, ExpectedVersion: item.ItemVersion + 1,
	})
	assert.ErrorIs(t, err, backgroundtask.ErrVersionConflict)

	pending, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: item.SessionID,
	})
	require.NoError(t, err)
	assert.Len(t, pending, 1)
}

func TestMemoryInboxDeepCopiesNotification_DefectProbing_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	payload := []byte("result")
	checkpoint := []byte("checkpoint")
	pendingResume := []byte("resume")
	notification := backgroundtask.Notification{
		ID: "notification-1",
		Task: &backgroundtask.Task{
			Spec: backgroundtask.Spec{
				ID: "task-1",
				Notify: &backgroundtask.NotificationTarget{
					Kind: "session_inbox", TargetID: "session-1",
					Metadata: map[string]string{"test/key": "original"},
				},
			},
			Status:        backgroundtask.StatusCompleted,
			ResultData:    payload,
			Checkpoint:    checkpoint,
			PendingResume: pendingResume,
		},
	}
	_, err := inbox.Enqueue(context.Background(), &backgroundtask.EnqueueSessionNotificationRequest{
		SessionID: "session-1", Notification: notification,
	})
	require.NoError(t, err)
	payload[0] = 'X'
	checkpoint[0] = 'X'
	pendingResume[0] = 'X'

	pending, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "result", string(pending[0].Notification.Task.ResultData))
	assert.Equal(t, "checkpoint", string(pending[0].Notification.Task.Checkpoint))
	assert.Equal(t, "resume", string(pending[0].Notification.Task.PendingResume))

	pending[0].Notification.Task.ResultData[0] = 'Y'
	pending[0].Notification.Task.Spec.Notify.Metadata["test/key"] = "mutated"
	pending[0].Notification.Task.Checkpoint[0] = 'Y'
	pending[0].Notification.Task.PendingResume[0] = 'Y'
	again, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "result", string(again[0].Notification.Task.ResultData))
	assert.Equal(t, "original", again[0].Notification.Task.Spec.Notify.Metadata["test/key"])
	assert.Equal(t, "checkpoint", string(again[0].Notification.Task.Checkpoint))
	assert.Equal(t, "resume", string(again[0].Notification.Task.PendingResume))
}

func TestAttack_MemoryInboxDeepCopiesNotificationTargetMetadata(t *testing.T) {
	inbox := NewMemoryInbox()
	metadata := map[string]string{"test/key": "original"}
	_, err := inbox.Enqueue(context.Background(), &backgroundtask.EnqueueSessionNotificationRequest{
		SessionID: "session-1",
		Notification: backgroundtask.Notification{
			ID: "notification-1",
			Target: backgroundtask.NotificationTarget{
				Kind:     "session_inbox",
				TargetID: "session-1",
				Metadata: metadata,
			},
		},
	})
	require.NoError(t, err)
	metadata["test/key"] = "mutated-after-enqueue"

	pending, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	t.Log("stored notification target metadata must not alias the caller-owned map")
	assert.Equal(t, "original", pending[0].Notification.Target.Metadata["test/key"])

	pending[0].Notification.Target.Metadata["test/key"] = "mutated-after-list"
	again, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, "original", again[0].Notification.Target.Metadata["test/key"])
}

func TestTurnLoopActivatorQueuesWakeAndStartsLoop_BitsUT(t *testing.T) {
	received := make(chan []string, 1)
	loop := adk.NewTurnLoop(adk.TurnLoopConfig[string, *schema.Message]{
		GenInput: func(
			_ context.Context,
			_ *adk.TurnLoop[string, *schema.Message],
			items []string,
		) (*adk.GenInputResult[string, *schema.Message], error) {
			received <- append([]string(nil), items...)
			return nil, errors.New("test loop complete")
		},
		PrepareAgent: func(
			context.Context,
			*adk.TurnLoop[string, *schema.Message],
			[]string,
		) (adk.Agent, error) {
			return nil, errors.New("unexpected PrepareAgent call")
		},
	})
	activator := &TurnLoopActivator[string, *schema.Message]{
		Resolve: func(_ context.Context, sessionID string) (*TurnLoopTarget[string, *schema.Message], error) {
			assert.Equal(t, "session-1", sessionID)
			return &TurnLoopTarget[string, *schema.Message]{
				Loop: loop, RunContext: context.Background(),
			}, nil
		},
		WakeItem: func(req *backgroundtask.SessionActivationRequest) (string, error) {
			return "wake:" + req.SessionID, nil
		},
	}

	result, err := activator.RequestTurn(context.Background(), &backgroundtask.SessionActivationRequest{
		SessionID: "session-1",
	})
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.SessionActivationQueued, result.Disposition)
	select {
	case items := <-received:
		assert.Equal(t, []string{"wake:session-1"}, items)
	case <-time.After(time.Second):
		t.Fatal("TurnLoop did not consume the wake item")
	}
	assert.EqualError(t, loop.Wait().ExitReason, "test loop complete")
}

func TestTurnLoopActivatorRejectsStoppedLoop_BitsUT(t *testing.T) {
	loop := adk.NewTurnLoop(adk.TurnLoopConfig[string, *schema.Message]{
		GenInput: func(
			context.Context,
			*adk.TurnLoop[string, *schema.Message],
			[]string,
		) (*adk.GenInputResult[string, *schema.Message], error) {
			return nil, nil
		},
		PrepareAgent: func(
			context.Context,
			*adk.TurnLoop[string, *schema.Message],
			[]string,
		) (adk.Agent, error) {
			return nil, nil
		},
	})
	loop.Stop()
	activator := &TurnLoopActivator[string, *schema.Message]{
		Resolve: func(context.Context, string) (*TurnLoopTarget[string, *schema.Message], error) {
			return &TurnLoopTarget[string, *schema.Message]{
				Loop: loop, RunContext: context.Background(),
			}, nil
		},
		WakeItem: func(*backgroundtask.SessionActivationRequest) (string, error) {
			return "wake", nil
		},
	}

	_, err := activator.RequestTurn(context.Background(), &backgroundtask.SessionActivationRequest{
		SessionID: "session-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}
