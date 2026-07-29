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
	"sync"
	"time"
)

// NotificationSink accepts dispatcher-enriched task notifications.
type NotificationSink interface {
	Accept(context.Context, Notification) error
}

// RoutedNotificationSink is implemented by sinks whose durable destination is
// selected by the serialized target rather than by sink kind alone.
type RoutedNotificationSink interface {
	AcceptTarget(context.Context, NotificationTarget, Notification) error
}

// NotificationSinkRegistry resolves sinks by notification target kind.
type NotificationSinkRegistry interface {
	Resolve(kind string) (NotificationSink, bool)
}

// SinkRegistry is an in-memory NotificationSinkRegistry implementation.
type SinkRegistry struct {
	mu    sync.RWMutex
	sinks map[string]NotificationSink
}

// NewSinkRegistry creates an empty sink registry.
func NewSinkRegistry() *SinkRegistry {
	return &SinkRegistry{sinks: make(map[string]NotificationSink)}
}

// Register associates a target kind with a notification sink.
func (r *SinkRegistry) Register(kind string, sink NotificationSink) error {
	if kind == "" || sink == nil {
		return errors.New("backgroundtask: sink kind and implementation are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sinks[kind]; exists {
		return ErrAlreadyExists
	}
	r.sinks[kind] = sink
	return nil
}

// Resolve returns the sink registered for kind.
func (r *SinkRegistry) Resolve(kind string) (NotificationSink, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sink, ok := r.sinks[kind]
	return sink, ok
}

// SessionNotificationInbox stores notifications awaiting parent session turns.
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

// ListSessionNotificationsRequest lists pending notifications for a session.
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

// Dispatcher delivers notifications from an outbox to registered sinks.
type Dispatcher struct {
	Outbox     NotificationOutbox
	Store      Store
	Sinks      NotificationSinkRegistry
	ConsumerID string
	BatchSize  int
	Visibility time.Duration
}

// DispatchOnce receives and dispatches one batch of visible notifications.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if d.Outbox == nil || d.Store == nil || d.Sinks == nil || d.ConsumerID == "" {
		return 0, errors.New("backgroundtask: dispatcher outbox, store, sinks, and consumer id are required")
	}
	visibility := d.Visibility
	if visibility <= 0 {
		visibility = 30 * time.Second
	}
	deliveries, err := d.Outbox.Receive(ctx, &ReceiveNotificationsRequest{
		ConsumerID: d.ConsumerID, Limit: d.BatchSize, VisibilityTime: visibility,
	})
	if err != nil {
		return 0, err
	}
	accepted := 0
	for _, delivery := range deliveries.Deliveries {
		sink, ok := d.Sinks.Resolve(delivery.Record.Target.Kind)
		if !ok {
			return accepted, errors.New("backgroundtask: notification sink is unavailable")
		}
		record := delivery.Record
		task, loadErr := d.Store.Get(ctx, record.TaskID)
		if loadErr != nil {
			return accepted, loadErr
		}
		notification := record
		notification.Task = task
		if routed, routedOK := sink.(RoutedNotificationSink); routedOK {
			err = routed.AcceptTarget(ctx, record.Target, notification)
		} else {
			err = sink.Accept(ctx, notification)
		}
		if err != nil {
			return accepted, err
		}
		if err = d.Outbox.Ack(ctx, delivery.Receipt); err != nil {
			return accepted, err
		}
		accepted++
	}
	return accepted, nil
}
