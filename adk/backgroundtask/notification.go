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
	RequestTurn(context.Context, *SessionActivationRequest) (*SessionActivationResult, error)
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

// SessionActivationDisposition describes how an activation request was handled.
type SessionActivationDisposition string

const (
	// SessionActivationStarted means the session turn started immediately.
	SessionActivationStarted SessionActivationDisposition = "started"
	// SessionActivationQueued means the session turn was queued for later execution.
	SessionActivationQueued SessionActivationDisposition = "queued"
)

// SessionActivationResult reports the activation disposition.
type SessionActivationResult struct {
	Disposition SessionActivationDisposition
}

// Dispatcher delivers task notifications from an outbox to session inboxes.
// All dependency fields are required when DispatchOnce is called. BatchSize
// defaults to the outbox default and LeaseDuration defaults to 30 seconds.
// Fields must not be mutated concurrently with DispatchOnce.
type Dispatcher struct {
	Outbox        NotificationOutbox
	Tasks         TaskStore
	Inbox         SessionNotificationInbox
	Activator     SessionActivator
	BatchSize     int
	LeaseDuration time.Duration
}

// DispatchOnce receives and dispatches one batch of visible notifications.
// Delivery and activation are at least once: inboxes must deduplicate enqueue
// and activators must coalesce repeated requests for the same pending work.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if d.Outbox == nil || d.Tasks == nil || d.Inbox == nil || d.Activator == nil {
		return 0, errors.New(
			"backgroundtask: dispatcher outbox, task store, session inbox, and activator are required",
		)
	}
	leaseDuration := d.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}
	deliveries, err := d.Outbox.Receive(ctx, &ReceiveNotificationsRequest{
		Limit: d.BatchSize, LeaseDuration: leaseDuration,
	})
	if err != nil {
		return 0, err
	}
	accepted := 0
	for _, delivery := range deliveries.Deliveries {
		record := delivery.Record
		task, loadErr := d.Tasks.Get(ctx, record.TaskID)
		if loadErr != nil {
			return accepted, loadErr
		}
		notification := record
		notification.Task = task
		item, enqueueErr := d.Inbox.Enqueue(ctx, &EnqueueSessionNotificationRequest{
			SessionID: task.Spec.SessionID, Notification: notification,
		})
		if enqueueErr != nil {
			return accepted, enqueueErr
		}
		if _, err = d.Activator.RequestTurn(ctx, &SessionActivationRequest{
			SessionID: item.SessionID,
		}); err != nil {
			return accepted, err
		}
		if err = d.Outbox.Ack(ctx, delivery.Receipt); err != nil {
			return accepted, err
		}
		accepted++
	}
	return accepted, nil
}
