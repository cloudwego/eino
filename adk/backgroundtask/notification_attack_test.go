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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAttack_DisabledLifecycleStillAllowsExplicitParentNotification(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(
			ctx context.Context,
			_ *Task,
			_ ExecutionRuntime,
		) (*ExecutionResult, error) {
			err := NotifyParent(ctx, &NotifyParentRequest{
				EventID: "explicit", Kind: "application.audit",
				Data: []byte("explicit data"),
			})
			require.NoError(t, err)
			return &ExecutionResult{
				Status: StatusCompleted, Data: []byte("result"),
			}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	spec := validSpec("explicit-without-lifecycle")
	spec.NotifySession = false
	task, err := manager.Submit(context.Background(), spec)
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))

	result, err := store.Receive(
		context.Background(),
		&ReceiveNotificationsRequest{Limit: 100, LeaseDuration: time.Second},
	)
	require.NoError(t, err)
	kinds := make([]NotificationKind, len(result.Deliveries))
	for i, delivery := range result.Deliveries {
		kinds[i] = delivery.Record.Kind
	}
	t.Logf("delivered kinds with lifecycle disabled: %v", kinds)
	require.Equal(t, []NotificationKind{
		NotificationTaskCreated,
		"application.audit",
	}, kinds)
	require.Equal(t, "explicit data", string(result.Deliveries[1].Record.Data))
}

func TestAttack_CustomNotificationIdentityIsOutsideLifecycleNamespace(t *testing.T) {
	taskID := string([]byte{0xff, ':', 0x00, '1'})
	eventID := string([]byte{0xfe, ':', 0x00, 'e'})
	custom := customNotificationID(taskID, eventID)
	require.Equal(t, custom, customNotificationID(taskID, eventID))
	require.True(t, strings.HasSuffix(custom, ":application-event"))

	for _, kind := range []NotificationKind{
		NotificationTaskCreated,
		NotificationWaitingInput,
		NotificationCompleted,
		NotificationFailed,
		NotificationCanceled,
		"eino.future",
	} {
		for _, version := range []int64{1, 9, 1<<62 - 1} {
			lifecycle := fmt.Sprintf("%s:%d:%s", taskID, version, kind)
			require.NotEqual(t, lifecycle, custom)
		}
	}
	t.Logf("custom identity %q uses a fixed non-lifecycle terminal domain", custom)
}
