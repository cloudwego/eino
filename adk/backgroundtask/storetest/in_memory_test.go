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

package storetest

import (
	"testing"
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

func TestInMemoryTaskStoreConformance(t *testing.T) {
	const attemptTimeout = 20 * time.Millisecond
	RunTaskStoreConformance(t, TaskStoreConfig{
		New: func(testing.TB) backgroundtask.TaskStore {
			return backgroundtask.NewInMemoryStore(&backgroundtask.InMemoryStoreConfig{
				ActiveAttemptTimeout: attemptTimeout,
			})
		},
		ExpireActiveAttempt: func(
			_ testing.TB,
			_ backgroundtask.TaskStore,
			_ *backgroundtask.Task,
		) {
			time.Sleep(2 * attemptTimeout)
		},
	})
}

func TestInMemoryTaskEventStoreConformance(t *testing.T) {
	RunTaskEventStoreConformance(t, TaskEventStoreConfig{
		New: func(testing.TB) (backgroundtask.TaskStore, backgroundtask.TaskEventStore) {
			store := backgroundtask.NewInMemoryStore(nil)
			return store, store
		},
	})
}

func TestInMemoryNotificationOutboxConformance(t *testing.T) {
	RunNotificationOutboxConformance(t, NotificationOutboxConfig{
		New: func(testing.TB) (backgroundtask.TaskStore, backgroundtask.NotificationOutbox) {
			store := backgroundtask.NewInMemoryStore(nil)
			return store, store
		},
		ExpireLease: func(
			_ testing.TB,
			_ backgroundtask.NotificationOutbox,
			duration time.Duration,
		) {
			time.Sleep(2 * duration)
		},
	})
}

func TestInMemoryNotificationWriterConformance(t *testing.T) {
	const attemptTimeout = 20 * time.Millisecond
	RunNotificationWriterConformance(t, NotificationWriterConfig{
		New: func(testing.TB) (
			backgroundtask.TaskStore,
			backgroundtask.NotificationOutbox,
		) {
			store := backgroundtask.NewInMemoryStore(&backgroundtask.InMemoryStoreConfig{
				ActiveAttemptTimeout: attemptTimeout,
			})
			return store, store
		},
		ExpireActiveAttempt: func(
			_ testing.TB,
			_ backgroundtask.TaskStore,
			_ *backgroundtask.Task,
		) {
			time.Sleep(2 * attemptTimeout)
		},
	})
}
