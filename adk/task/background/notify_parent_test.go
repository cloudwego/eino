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
)

func TestNotifyParentRequiresManagedAttemptContext_BitsUT(t *testing.T) {
	valid := &NotifyParentRequest{
		EventID: "event", Kind: "application.update", Data: []byte("data"),
	}
	require.ErrorIs(
		t,
		NotifyParent(context.Background(), valid),
		ErrNotificationUnavailable,
	)
	require.Error(t, NotifyParent(context.Background(), nil))
	require.Error(t, NotifyParent(nil, &NotifyParentRequest{
		Kind: "application.update",
	}))
}

func TestNotifyParentValidatesRequestBoundsBeforeAuthority_BitsUT(t *testing.T) {
	tests := []struct {
		name string
		req  *NotifyParentRequest
		err  string
	}{
		{
			name: "nil request",
			err:  "task/background: parent notification request is required",
		},
		{
			name: "empty event id",
			req:  &NotifyParentRequest{Kind: "application.update"},
			err:  "task/background: notification event id is required",
		},
		{
			name: "long event id",
			req: &NotifyParentRequest{
				EventID: string(make([]byte, 1025)), Kind: "application.update",
			},
			err: "task/background: notification event id exceeds configured bounds",
		},
		{
			name: "empty kind",
			req:  &NotifyParentRequest{EventID: "event"},
			err:  "task/background: notification kind is required",
		},
		{
			name: "long kind",
			req: &NotifyParentRequest{
				EventID: "event", Kind: NotificationKind(string(make([]byte, 65))),
			},
			err: "task/background: notification kind exceeds configured bounds",
		},
		{
			name: "reserved prefix",
			req: &NotifyParentRequest{
				EventID: "event", Kind: "eino.application",
			},
			err: "task/background: notification kind is reserved",
		},
		{
			name: "lifecycle kind",
			req: &NotifyParentRequest{
				EventID: "event", Kind: NotificationCompleted,
			},
			err: "task/background: notification kind is reserved",
		},
		{
			name: "large data",
			req: &NotifyParentRequest{
				EventID: "event", Kind: "application.update",
				Data: make([]byte, (256<<10)+1),
			},
			err: "task/background: notification data exceeds configured bounds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.EqualError(t, NotifyParent(context.Background(), test.req), test.err)
		})
	}
}

func TestManagerBindsFreshNotifyParentAuthorityPerAttempt_BitsUT(t *testing.T) {
	var firstContext context.Context
	executor := &scriptedExecutor{
		execute: func(
			ctx context.Context,
			task *TaskSnapshot,
			_ ExecutionRuntime,
		) (*ExecutionResult, error) {
			switch task.Attempt {
			case 1:
				firstContext = ctx
				require.NoError(t, NotifyParent(ctx, &NotifyParentRequest{
					EventID: "first", Kind: "application.progress",
					Data: []byte("first"),
				}))
				return &ExecutionResult{Action: ExecutionActionYield}, nil
			case 2:
				require.NoError(t, NotifyParent(ctx, &NotifyParentRequest{
					EventID: "second", Kind: "application.progress",
					Data: []byte("second"),
				}))
				return &ExecutionResult{
					Action: ExecutionActionComplete, Data: []byte("done"),
				}, nil
			default:
				return nil, errors.New("unexpected attempt")
			}
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("notify-attempt")})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	require.NotNil(t, firstContext)
	require.ErrorIs(t, NotifyParent(firstContext, &NotifyParentRequest{
		EventID: "stale", Kind: "application.progress",
	}), ErrLeaseLost)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))

	result, err := store.Receive(
		context.Background(),
		&ReceiveNotificationsRequest{Limit: 100, LeaseDuration: time.Second},
	)
	require.NoError(t, err)
	var custom []Notification
	for _, delivery := range result.Deliveries {
		if delivery.Record.Kind == "application.progress" {
			custom = append(custom, delivery.Record)
		}
	}
	require.Len(t, custom, 2)
	require.Equal(t, "first", string(custom[0].Data))
	require.Equal(t, "second", string(custom[1].Data))
}

type notificationWriterStub struct {
	err      error
	received *NotifyParentRequest
}

func (w *notificationWriterStub) EnqueueTaskNotification(
	_ context.Context,
	_ string,
	_ int64,
	req *NotifyParentRequest,
) error {
	copy := *req
	copy.Data = cloneBytes(req.Data)
	w.received = &copy
	if len(req.Data) > 0 {
		req.Data[0] = 'X'
	}
	return w.err
}

func TestNotifyParentRuntimePoisonAndWriterErrorSemantics_BitsUT(t *testing.T) {
	writerErr := errors.New("writer failed")
	writer := &notificationWriterStub{err: writerErr}
	runtime := newTaskRuntime(nil, nil, "task", 1, 1, writer)
	ctx := context.WithValue(
		context.Background(),
		notifyParentContextKey{},
		notifyParentCallback(runtime.notifyParent),
	)
	data := []byte("data")
	require.ErrorIs(t, NotifyParent(ctx, &NotifyParentRequest{
		EventID: "event", Kind: "application.progress", Data: data,
	}), writerErr)
	require.Nil(t, runtime.poison)
	require.Equal(t, "data", string(data))
	require.Equal(t, "data", string(writer.received.Data))

	runtime.poison = ErrLeaseLost
	require.Error(t, NotifyParent(ctx, &NotifyParentRequest{
		Kind: "application.progress",
	}))
	require.ErrorIs(t, NotifyParent(ctx, &NotifyParentRequest{
		EventID: "poisoned", Kind: "application.progress",
	}), ErrLeaseLost)
}
