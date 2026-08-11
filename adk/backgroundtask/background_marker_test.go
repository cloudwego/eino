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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func receiveCreatedRecords(t *testing.T, store *InMemoryStore) []NotificationDelivery {
	t.Helper()
	result, err := store.Receive(
		context.Background(),
		&ReceiveNotificationsRequest{Limit: 100, LeaseDuration: time.Second},
	)
	require.NoError(t, err)
	created := make([]NotificationDelivery, 0, len(result.Deliveries))
	for _, delivery := range result.Deliveries {
		if delivery.Record.Kind == NotificationTaskCreated {
			created = append(created, delivery)
		}
	}
	return created
}

// TestInMemoryStoreCreateDefersTaskCreatedWhenEmitOnBackground verifies that a
// task whose Spec sets EmitCreatedOnBackground does not enqueue the durable
// NotificationTaskCreated record at creation. Such tasks are announced live by
// the Manager when they detach into the background, not durably at creation.
func TestInMemoryStoreCreateDefersTaskCreatedWhenEmitOnBackground_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	spec := validSpec("deferred-created")
	spec.EmitCreatedOnBackground = true

	_, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)
	require.Empty(t, receiveCreatedRecords(t, store),
		"created record must not be enqueued at creation for a deferred task")
}

// TestInMemoryStoreCreateEmitsTaskCreatedByDefault confirms the unchanged
// behavior for tasks that are background by construction: Create still enqueues
// the durable NotificationTaskCreated record immediately.
func TestInMemoryStoreCreateEmitsTaskCreatedByDefault_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	spec := validSpec("eager-created")

	_, err := store.Create(context.Background(), &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: LeaseExpiryRetry,
	})
	require.NoError(t, err)

	created := receiveCreatedRecords(t, store)
	require.Len(t, created, 1)
	require.Equal(t, spec.ID, created[0].Record.TaskID)
}

// TestManagerMarkBackgroundedEmitsLiveOnly verifies that a deferred task emits
// no created signal at Submit, and that MarkBackgrounded announces it live
// exactly once without writing any durable record (the announcement is
// live-only for process-local foreground runs).
func TestManagerMarkBackgroundedEmitsLiveOnly_BitsUT(t *testing.T) {
	registry := NewExecutorRegistry()
	require.NoError(t, registry.Register(&scriptedExecutor{}))
	store := NewInMemoryStore(nil)
	var sent []string
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks: store, Executors: registry,
		SendTaskCreatedEvent: func(_ context.Context, task *Task) error {
			sent = append(sent, task.Spec.ID)
			return nil
		},
	})
	spec := validSpec("manager-deferred")
	spec.EmitCreatedOnBackground = true

	_, err := manager.Submit(context.Background(), spec)
	require.NoError(t, err)
	require.Empty(t, sent, "Submit must not emit the created event for a deferred task")
	require.Empty(t, receiveCreatedRecords(t, store),
		"Submit must not enqueue the durable created record for a deferred task")

	_, err = manager.MarkBackgrounded(context.Background(), spec.ID)
	require.NoError(t, err)
	require.Equal(t, []string{spec.ID}, sent,
		"MarkBackgrounded must emit the live created event exactly once")
	require.Empty(t, receiveCreatedRecords(t, store),
		"MarkBackgrounded is live-only and must not enqueue a durable created record")
}

// TestManagerMarkBackgroundedNotFound verifies an unknown task id surfaces the
// store's ErrNotFound.
func TestManagerMarkBackgroundedNotFound_BitsUT(t *testing.T) {
	registry := NewExecutorRegistry()
	require.NoError(t, registry.Register(&scriptedExecutor{}))
	manager := mustNewManager(t, context.Background(), &Config{
		Executors: registry,
		SendTaskCreatedEvent: func(context.Context, *Task) error {
			return nil
		},
	})
	_, err := manager.MarkBackgrounded(context.Background(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
}
