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
	"testing"
	"time"
)

func TestInMemoryTaskStoreConformance(t *testing.T) {
	const attemptTimeout = 20 * time.Millisecond
	runTaskStoreConformance(t, taskStoreConformanceConfig{
		New: func(testing.TB) TaskStore {
			return NewInMemoryStore(&InMemoryStoreConfig{
				ActiveAttemptTimeout: attemptTimeout,
			})
		},
		ExpireActiveAttempt: func(_ testing.TB, _ TaskStore, _ *Task) {
			time.Sleep(2 * attemptTimeout)
		},
	})
}

func TestInMemoryTaskEventStoreConformance(t *testing.T) {
	runTaskEventStoreConformance(t, taskEventStoreConformanceConfig{
		New: func(testing.TB) (TaskStore, TaskEventStore) {
			store := NewInMemoryStore(nil)
			return store, store
		},
	})
}

func TestInMemoryNotificationOutboxConformance(t *testing.T) {
	runNotificationOutboxConformance(t, notificationOutboxConformanceConfig{
		New: func(testing.TB) (TaskStore, NotificationOutbox) {
			store := NewInMemoryStore(nil)
			return store, store
		},
		ExpireLease: func(_ testing.TB, _ NotificationOutbox, duration time.Duration) {
			time.Sleep(2 * duration)
		},
	})
}

func TestInMemoryNotificationWriterConformance(t *testing.T) {
	const attemptTimeout = 20 * time.Millisecond
	runNotificationWriterConformance(t, notificationWriterConformanceConfig{
		New: func(testing.TB) (TaskStore, NotificationOutbox) {
			store := NewInMemoryStore(&InMemoryStoreConfig{
				ActiveAttemptTimeout: attemptTimeout,
			})
			return store, store
		},
		ExpireActiveAttempt: func(_ testing.TB, _ TaskStore, _ *Task) {
			time.Sleep(2 * attemptTimeout)
		},
	})
}
