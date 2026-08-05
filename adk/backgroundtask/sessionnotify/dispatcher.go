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
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

// DispatcherConfig configures delivery from the task notification outbox to
// parent sessions. All dependency fields are required. BatchSize defaults to
// the outbox default and LeaseDuration defaults to 30 seconds.
type DispatcherConfig struct {
	Outbox          backgroundtask.NotificationOutbox
	Tasks           backgroundtask.TaskStore
	Inbox           Inbox
	ActivateSession func(context.Context, string) error
	BatchSize       int
	LeaseDuration   time.Duration
}

// Dispatcher durably enqueues task notifications for parent sessions, requests
// a session wake-up, then acknowledges the outbox lease. Its dependencies and
// policy are immutable after construction.
type Dispatcher struct {
	outbox          backgroundtask.NotificationOutbox
	tasks           backgroundtask.TaskStore
	inbox           Inbox
	activateSession func(context.Context, string) error
	batchSize       int
	leaseDuration   time.Duration
}

// NewDispatcher validates config and creates an immutable Dispatcher.
func NewDispatcher(config *DispatcherConfig) (*Dispatcher, error) {
	if config == nil || config.Outbox == nil || config.Tasks == nil ||
		config.Inbox == nil || config.ActivateSession == nil {
		return nil, errors.New(
			"sessionnotify: dispatcher outbox, task store, inbox, and session activation are required",
		)
	}
	leaseDuration := config.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}
	return &Dispatcher{
		outbox: config.Outbox, tasks: config.Tasks, inbox: config.Inbox,
		activateSession: config.ActivateSession, batchSize: config.BatchSize,
		leaseDuration: leaseDuration,
	}, nil
}

// DispatchOnce delivers one batch of visible notifications. Delivery is at
// least once: Inbox must deduplicate Enqueue and ActivateSession must tolerate
// repeated wake requests for the same pending work.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if d == nil {
		return 0, errors.New("sessionnotify: dispatcher is required")
	}
	deliveries, err := d.outbox.Receive(ctx, &backgroundtask.ReceiveNotificationsRequest{
		Limit: d.batchSize, LeaseDuration: d.leaseDuration,
	})
	if err != nil {
		return 0, err
	}
	accepted := 0
	for _, delivery := range deliveries.Deliveries {
		record := delivery.Record
		task, loadErr := d.tasks.Get(ctx, record.TaskID)
		if loadErr != nil {
			return accepted, loadErr
		}
		notification := record
		notification.Task = task
		item, enqueueErr := d.inbox.Enqueue(ctx, &EnqueueRequest{
			SessionID: task.Spec.SessionID, Notification: notification,
		})
		if enqueueErr != nil {
			return accepted, enqueueErr
		}
		if err = d.activateSession(ctx, item.SessionID); err != nil {
			return accepted, err
		}
		if err = d.outbox.Ack(ctx, delivery.Receipt); err != nil {
			return accepted, err
		}
		accepted++
	}
	return accepted, nil
}
