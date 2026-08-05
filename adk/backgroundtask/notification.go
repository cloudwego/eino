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
	"errors"
	"time"
)

// SessionNotificationInbox stores notifications awaiting parent session turns.
// Enqueue deduplicates by (SessionID, Notification.ID), ListPending returns
// creation order with provider-normalized bounds, and Ack uses ItemVersion CAS.
type SessionNotificationInbox interface {
	Enqueue(context.Context, *EnqueueSessionNotificationRequest) (*SessionInboxItem, error)
	ListPending(context.Context, *ListSessionNotificationsRequest) ([]*SessionInboxItem, error)
	Ack(context.Context, *AckSessionNotificationRequest) error
}

// SessionActivator requests that a session process pending notifications.
type SessionActivator interface {
	RequestTurn(context.Context, *SessionActivationRequest) error
}

// EnqueueSessionNotificationRequest enqueues a notification for a session.
type EnqueueSessionNotificationRequest struct {
	SessionID    string
	Notification Notification
}

// SessionInboxItem is a durable session notification inbox entry.
type SessionInboxItem struct {
	ItemID       string
	ItemVersion  int64
	SessionID    string
	Notification Notification
	CreatedAt    time.Time
}

// ListSessionNotificationsRequest lists pending notifications for a session in
// creation order. Limit defaults to 100 and is capped at 1000.
type ListSessionNotificationsRequest struct {
	SessionID string
	Limit     int
}

// AckSessionNotificationRequest acknowledges a session inbox item.
type AckSessionNotificationRequest struct {
	SessionID       string
	ItemID          string
	ExpectedVersion int64
}

// SessionActivationRequest asks the host to schedule a session turn.
type SessionActivationRequest struct {
	SessionID string
}

// DispatcherConfig configures a notification Dispatcher. All dependency fields
// are required. BatchSize defaults to the outbox default and LeaseDuration
// defaults to 30 seconds.
type DispatcherConfig struct {
	Outbox        NotificationOutbox
	Tasks         TaskStore
	Inbox         SessionNotificationInbox
	Activator     SessionActivator
	BatchSize     int
	LeaseDuration time.Duration
}

// Dispatcher delivers task notifications from an outbox to session inboxes.
// Its dependencies and delivery policy are immutable after construction.
type Dispatcher struct {
	outbox        NotificationOutbox
	tasks         TaskStore
	inbox         SessionNotificationInbox
	activator     SessionActivator
	batchSize     int
	leaseDuration time.Duration
}

// NewDispatcher validates config and creates an immutable Dispatcher.
func NewDispatcher(config *DispatcherConfig) (*Dispatcher, error) {
	if config == nil || config.Outbox == nil || config.Tasks == nil ||
		config.Inbox == nil || config.Activator == nil {
		return nil, errors.New(
			"backgroundtask: dispatcher outbox, task store, session inbox, and activator are required",
		)
	}
	leaseDuration := config.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}
	return &Dispatcher{
		outbox: config.Outbox, tasks: config.Tasks, inbox: config.Inbox,
		activator: config.Activator, batchSize: config.BatchSize,
		leaseDuration: leaseDuration,
	}, nil
}

// DispatchOnce receives and dispatches one batch of visible notifications.
// Delivery and activation are at least once: inboxes must deduplicate enqueue
// and activators must coalesce repeated requests for the same pending work.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if d == nil {
		return 0, errors.New("backgroundtask: dispatcher is required")
	}
	deliveries, err := d.outbox.Receive(ctx, &ReceiveNotificationsRequest{
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
		item, enqueueErr := d.inbox.Enqueue(ctx, &EnqueueSessionNotificationRequest{
			SessionID: task.Spec.SessionID, Notification: notification,
		})
		if enqueueErr != nil {
			return accepted, enqueueErr
		}
		if err = d.activator.RequestTurn(ctx, &SessionActivationRequest{
			SessionID: item.SessionID,
		}); err != nil {
			return accepted, err
		}
		if err = d.outbox.Ack(ctx, delivery.Receipt); err != nil {
			return accepted, err
		}
		accepted++
	}
	return accepted, nil
}
