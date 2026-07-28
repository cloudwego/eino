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
	"crypto/sha256"
	"encoding/hex"
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

func TestSinkAcceptTargetEnqueuesBeforeActivation_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	activator := &recordingActivator{inbox: inbox}
	sink := &Sink{Inbox: inbox, Activator: activator}

	err := sink.AcceptTarget(context.Background(), backgroundtask.NotificationTarget{
		Kind: "session_inbox", TargetID: "session-1",
	}, backgroundtask.TaskNotification{
		NotificationID: "notification-1", TaskID: "task-1",
		EventKind: backgroundtask.NotificationCompleted,
		Status:    backgroundtask.StatusCompleted,
	})
	require.NoError(t, err)
	assert.Equal(t, "session-1", activator.sessionID)
	assert.Equal(t, 1, activator.pendingSeen)
}

func TestSinkAcceptTargetActivationFailureRetainsInboxItem_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	wantErr := errors.New("activation unavailable")
	sink := &Sink{
		Inbox: inbox, Activator: &recordingActivator{inbox: inbox, err: wantErr},
	}

	err := sink.AcceptTarget(context.Background(), backgroundtask.NotificationTarget{
		Kind: "session_inbox", TargetID: "session-1",
	}, backgroundtask.TaskNotification{NotificationID: "notification-1"})
	assert.ErrorIs(t, err, wantErr)

	pending, listErr := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, listErr)
	require.Len(t, pending, 1)
	assert.Equal(t, "notification-1", pending[0].Notification.NotificationID)
}

func TestTerminalTaskNotificationWakesParentSession_BitsUT(t *testing.T) {
	store := backgroundtask.NewMemoryStore(nil)
	spec := backgroundtask.Spec{
		ID: "task-1", ExecutorKey: "test", SpecVersion: "v1",
		Payload: []byte("{}"), PayloadEncoding: "application/json",
		SessionID: "session-1",
		Notify:    &backgroundtask.NotificationTarget{Kind: "session_inbox", TargetID: "session-1"},
		Recovery: backgroundtask.RecoveryPolicy{
			OnLeaseExpired:      backgroundtask.RecoveryFail,
			OnMissingCheckpoint: backgroundtask.RecoveryFail,
			MaxAttempts:         1,
		},
		Result: backgroundtask.ResultPolicy{ResultFormat: "text/plain"},
	}
	created, err := store.Create(context.Background(), &backgroundtask.CreateTaskRequest{Spec: spec})
	require.NoError(t, err)
	claimed, err := store.Claim(context.Background(), &backgroundtask.ClaimTaskRequest{
		TaskID: spec.ID, ExpectedVersion: created.TransitionVersion,
		WorkerID: "worker", LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	resultBytes := []byte("done")
	sum := sha256.Sum256(resultBytes)
	_, err = store.Commit(context.Background(), &backgroundtask.CommitTaskRequest{
		Lease: claimed.Lease,
		Mutation: backgroundtask.TaskMutation{
			ToStatus: backgroundtask.StatusCompleted,
			Result: &backgroundtask.ResultRef{
				Format: "text/plain",
				Value: backgroundtask.ArtifactValue{
					Payload: resultBytes, Encoding: "utf-8",
					Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(resultBytes)),
				},
			},
		},
	})
	require.NoError(t, err)

	inbox := NewMemoryInbox()
	activator := &recordingActivator{inbox: inbox}
	registry := backgroundtask.NewSinkRegistry()
	require.NoError(t, registry.Register("session_inbox", &Sink{
		Inbox: inbox, Activator: activator,
	}))
	dispatcher := &backgroundtask.Dispatcher{
		Outbox: store, Sinks: registry, ConsumerID: "dispatcher",
		BatchSize: 10, Visibility: time.Minute,
	}
	delivered, err := dispatcher.DispatchOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, delivered, "claim and completion updates are both session-visible")
	assert.Equal(t, spec.SessionID, activator.sessionID)

	pending, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: spec.SessionID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, pending, 2)
	var completion *backgroundtask.SessionInboxItem
	for _, item := range pending {
		if item.Notification.EventKind == backgroundtask.NotificationCompleted {
			completion = item
		}
	}
	require.NotNil(t, completion)
	assert.Equal(t, "done", string(completion.Notification.Result.Value.Payload))
}

func TestMemoryInboxDeduplicatesAcrossAckAndRedelivery_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	request := &backgroundtask.EnqueueSessionNotificationRequest{
		SessionID: "session-1",
		Notification: backgroundtask.TaskNotification{
			NotificationID: "notification-1", TaskID: "task-1",
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
		Notification: backgroundtask.TaskNotification{
			NotificationID: "notification-1",
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

func TestMemoryInboxDeepCopiesNotification_BitsUT(t *testing.T) {
	inbox := NewMemoryInbox()
	current := 1.0
	payload := []byte("result")
	notification := backgroundtask.TaskNotification{
		NotificationID: "notification-1",
		Progress:       &backgroundtask.Progress{Current: &current},
		Result: &backgroundtask.ResultRef{
			Format: "text/plain",
			Value:  backgroundtask.ArtifactValue{Payload: payload},
		},
	}
	_, err := inbox.Enqueue(context.Background(), &backgroundtask.EnqueueSessionNotificationRequest{
		SessionID: "session-1", Notification: notification,
	})
	require.NoError(t, err)
	current = 9
	payload[0] = 'X'

	pending, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, 1.0, *pending[0].Notification.Progress.Current)
	assert.Equal(t, "result", string(pending[0].Notification.Result.Value.Payload))

	pending[0].Notification.Result.Value.Payload[0] = 'Y'
	again, err := inbox.ListPending(context.Background(), &backgroundtask.ListSessionNotificationsRequest{
		SessionID: "session-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "result", string(again[0].Notification.Result.Value.Payload))
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
