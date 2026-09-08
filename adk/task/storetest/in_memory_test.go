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

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
)

func TestInMemoryMailboxStoreConformance(t *testing.T) {
	RunMailboxStoreConformance(t, MailboxStoreConfig{
		New: func(testing.TB) task.MailboxStore {
			return background.NewInMemoryStore(nil)
		},
	})
}

func TestInMemoryLifecycleStoreConformance(t *testing.T) {
	const attemptTimeout = 20 * time.Millisecond
	RunLifecycleStoreConformance(t, LifecycleStoreConfig{
		New: func(testing.TB) background.LifecycleStore {
			return background.NewInMemoryStore(&background.InMemoryStoreConfig{
				ActiveAttemptTimeout: attemptTimeout,
			})
		},
		ExpireActiveAttempt: func(
			_ testing.TB,
			_ background.LifecycleStore,
			_ *background.TaskSnapshot,
		) {
			time.Sleep(2 * attemptTimeout)
		},
	})
}

func TestInMemoryTaskEventStoreConformance(t *testing.T) {
	RunTaskEventStoreConformance(t, TaskEventStoreConfig{
		New: func(testing.TB) (background.TaskStore, background.TaskEventStore) {
			store := background.NewInMemoryStore(nil)
			return store, store
		},
	})
}

func TestInMemoryNotificationOutboxConformance(t *testing.T) {
	RunNotificationOutboxConformance(t, NotificationOutboxConfig{
		New: func(testing.TB) (background.TaskStore, background.NotificationOutbox) {
			store := background.NewInMemoryStore(nil)
			return store, store
		},
		ExpireLease: func(
			_ testing.TB,
			_ background.NotificationOutbox,
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
			background.TaskStore,
			background.NotificationOutbox,
		) {
			store := background.NewInMemoryStore(&background.InMemoryStoreConfig{
				ActiveAttemptTimeout: attemptTimeout,
			})
			return store, store
		},
		ExpireActiveAttempt: func(
			_ testing.TB,
			_ background.TaskStore,
			_ *background.TaskSnapshot,
		) {
			time.Sleep(2 * attemptTimeout)
		},
	})
}
