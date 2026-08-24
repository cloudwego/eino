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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	taskcore "github.com/cloudwego/eino/adk/task"
)

func TestManagerDeferredPublicationSkipsImmediateCreatedEvent(t *testing.T) {
	store := NewInMemoryStore(nil)
	var sends int
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *TaskSnapshot) error {
			sends++
			return nil
		},
	})
	_, _, err := manager.LoadOrRegisterExecutor(&scriptedExecutor{})
	require.NoError(t, err)
	spec := validSpec("deferred")
	created, err := manager.Submit(context.Background(), &SubmitRequest{
		Spec: spec, Publication: PublicationDeferred,
	})
	require.NoError(t, err)
	require.Equal(t, PublicationDeferred, created.Publication)
	require.Zero(t, sends)
	require.Empty(t, receiveAllNotifications(t, store))

	published, err := manager.Publish(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, PublicationOnBackground, published.Publication)
	require.Equal(t, created.Version, published.Version)
	notifications := receiveAllNotifications(t, store)
	require.Len(t, notifications, 1)
	require.Equal(t, NotificationTaskBackgrounded, notifications[0].Kind)
}

func TestAttack_PublishAndCompletionHaveOneVisibilityOutcome(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		store := NewInMemoryStore(nil)
		created, err := store.Create(context.Background(), &CreateTaskRequest{
			Spec: Spec{
				ID: "race", ExecutorKey: "test", Kind: "test",
				RootSessionID: "session", NotifySession: true,
			},
			Publication:       PublicationDeferred,
			LeaseExpiryPolicy: LeaseExpiryRetry,
		})
		require.NoError(t, err)
		started, err := store.Start(context.Background(), &StartTaskRequest{
			TaskID: created.Spec.ID, ExpectedVersion: created.Version,
		})
		require.NoError(t, err)

		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var publishErr error
		var completeErr error
		go func() {
			defer group.Done()
			<-start
			_, publishErr = store.Publish(
				context.Background(),
				&PublishTaskRequest{
					TaskID: started.Spec.ID, ExpectedVersion: started.Version,
				},
			)
		}()
		go func() {
			defer group.Done()
			<-start
			_, completeErr = store.CompleteIfNoInputs(
				context.Background(),
				&CompleteIfNoInputsRequest{
					TaskID: started.Spec.ID, ExpectedVersion: started.Version,
					Attempt: started.Attempt, InputCursor: 0,
					ResultData: []byte("done"),
				},
			)
		}()
		close(start)
		group.Wait()

		current, err := store.Get(context.Background(), started.Spec.ID)
		require.NoError(t, err)
		require.Equal(t, StatusCompleted, current.Status)
		notifications := receiveAllNotifications(t, store)
		if publishErr == nil {
			require.NoError(t, completeErr)
			require.Equal(t, PublicationOnBackground, current.Publication)
			require.Equal(t, []NotificationKind{
				NotificationTaskBackgrounded,
				NotificationCompleted,
			}, notificationKinds(notifications))
		} else {
			require.True(t,
				errors.Is(publishErr, ErrVersionConflict) ||
					errors.Is(publishErr, ErrAlreadyTerminal),
			)
			require.NoError(t, completeErr)
			require.Equal(t, PublicationDeferred, current.Publication)
			require.Empty(t, notifications)
		}
	}
}

func TestAttack_PublishAndCancellationHaveOneVisibilityOutcome(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		store := NewInMemoryStore(nil)
		created, err := store.Create(context.Background(), &CreateTaskRequest{
			Spec: Spec{
				ID: "race", ExecutorKey: "test", Kind: "test",
				RootSessionID: "session", NotifySession: true,
			},
			Publication:       PublicationDeferred,
			LeaseExpiryPolicy: LeaseExpiryRetry,
		})
		require.NoError(t, err)
		started, err := store.Start(context.Background(), &StartTaskRequest{
			TaskID: created.Spec.ID, ExpectedVersion: created.Version,
		})
		require.NoError(t, err)

		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var publishErr error
		var cancelErr error
		go func() {
			defer group.Done()
			<-start
			_, publishErr = store.Publish(
				context.Background(),
				&PublishTaskRequest{
					TaskID: started.Spec.ID, ExpectedVersion: started.Version,
				},
			)
		}()
		go func() {
			defer group.Done()
			<-start
			_, cancelErr = store.RequestCancel(
				context.Background(),
				&RequestCancelRequest{
					TaskID: started.Spec.ID, ExpectedVersion: started.Version,
					Reason: "cancel",
				},
			)
		}()
		close(start)
		group.Wait()

		require.NoError(t, cancelErr)
		current, err := store.Get(context.Background(), started.Spec.ID)
		require.NoError(t, err)
		if current.Status == StatusRunning {
			current, err = store.AckCancel(
				context.Background(),
				&AckCancelRequest{
					TaskID: current.Spec.ID, ExpectedVersion: current.Version,
					Reason: "cancel",
				},
			)
			require.NoError(t, err)
		}
		require.Equal(t, StatusCanceled, current.Status)
		notifications := receiveAllNotifications(t, store)
		if publishErr == nil {
			require.Equal(t, PublicationOnBackground, current.Publication)
			require.Equal(t, []NotificationKind{
				NotificationTaskBackgrounded,
				NotificationCanceled,
			}, notificationKinds(notifications))
		} else {
			require.ErrorIs(t, publishErr, ErrVersionConflict)
			require.Equal(t, PublicationDeferred, current.Publication)
			require.Empty(t, notifications)
		}
	}
}

func TestPublishNestedTaskFailsBeforeParentMutation(t *testing.T) {
	store := NewInMemoryStore(nil)
	parent, err := store.Register(context.Background(), &taskcore.RegisterMailboxRequest{
		CandidateTaskID: "parent", InvocationID: "parent",
		RootSessionID: "root",
	})
	require.NoError(t, err)
	child, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: Spec{
			ID: "child", ExecutorKey: "test", Kind: "test",
			ParentTaskID: parent.Mailbox.TaskID, RootSessionID: "root",
		},
		Publication:       PublicationDeferred,
		LeaseExpiryPolicy: LeaseExpiryRetry,
		ParentExecution: &taskcore.ExecutionContext{
			TaskID: parent.Mailbox.TaskID, Owner: taskcore.OwnerParent,
			Generation: parent.Mailbox.Generation,
		},
	})
	require.NoError(t, err)
	_, err = store.SealIfIdle(context.Background(), &taskcore.SealMailboxRequest{
		TaskID: parent.Mailbox.TaskID, ExpectedGeneration: parent.Mailbox.Generation,
	})
	require.NoError(t, err)

	_, err = store.Publish(context.Background(), &PublishTaskRequest{
		TaskID: child.Spec.ID, ExpectedVersion: child.Version,
	})
	require.ErrorIs(t, err, taskcore.ErrMailboxSealed)
	current, err := store.Get(context.Background(), child.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, PublicationDeferred, current.Publication)
	require.Equal(t, child.Version, current.Version)
}

func receiveAllNotifications(
	t testing.TB,
	store NotificationOutbox,
) []Notification {
	t.Helper()
	result, err := store.Receive(
		context.Background(),
		&ReceiveNotificationsRequest{Limit: 100, LeaseDuration: time.Second},
	)
	require.NoError(t, err)
	notifications := make([]Notification, len(result.Deliveries))
	for index, delivery := range result.Deliveries {
		notifications[index] = delivery.Record
	}
	return notifications
}

func notificationKinds(notifications []Notification) []NotificationKind {
	kinds := make([]NotificationKind, len(notifications))
	for index, notification := range notifications {
		kinds[index] = notification.Kind
	}
	return kinds
}
