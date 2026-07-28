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

type TaskNotification struct {
	NotificationID    string
	TaskID            string
	TransitionVersion int64
	UpdateSequence    int64
	EventKind         NotificationEventKind
	Status            Status
	Progress          *Progress
	Result            *ResultRef
	Reason            string
}

type NotificationSink interface {
	Accept(context.Context, TaskNotification) error
}

// RoutedNotificationSink is implemented by sinks whose durable destination is
// selected by the serialized target rather than by sink kind alone.
type RoutedNotificationSink interface {
	AcceptTarget(context.Context, NotificationTarget, TaskNotification) error
}

type NotificationSinkRegistry interface {
	Resolve(kind string) (NotificationSink, bool)
}

type SinkRegistry struct {
	mu    sync.RWMutex
	sinks map[string]NotificationSink
}

func NewSinkRegistry() *SinkRegistry {
	return &SinkRegistry{sinks: make(map[string]NotificationSink)}
}

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

func (r *SinkRegistry) Resolve(kind string) (NotificationSink, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sink, ok := r.sinks[kind]
	return sink, ok
}

type SessionNotificationInbox interface {
	Enqueue(context.Context, *EnqueueSessionNotificationRequest) (*SessionInboxItem, error)
	ListPending(context.Context, *ListSessionNotificationsRequest) ([]*SessionInboxItem, error)
	Ack(context.Context, *AckSessionNotificationRequest) error
}

type SessionActivator interface {
	RequestTurn(context.Context, *SessionActivationRequest) (*SessionActivationResult, error)
}

type EnqueueSessionNotificationRequest struct {
	SessionID    string
	Notification TaskNotification
}

type SessionInboxItem struct {
	ItemID       string
	ItemVersion  int64
	SessionID    string
	Notification TaskNotification
	CreatedAt    time.Time
}

type ListSessionNotificationsRequest struct {
	SessionID string
	Limit     int
}

type AckSessionNotificationRequest struct {
	SessionID       string
	ItemID          string
	ExpectedVersion int64
}

type SessionActivationRequest struct {
	SessionID string
}

type SessionActivationDisposition string

const (
	SessionActivationStarted SessionActivationDisposition = "started"
	SessionActivationQueued  SessionActivationDisposition = "queued"
)

type SessionActivationResult struct {
	Disposition SessionActivationDisposition
}

type Dispatcher struct {
	Outbox     NotificationOutbox
	Sinks      NotificationSinkRegistry
	ConsumerID string
	BatchSize  int
	Visibility time.Duration
}

func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if d.Outbox == nil || d.Sinks == nil || d.ConsumerID == "" {
		return 0, errors.New("backgroundtask: dispatcher outbox, sinks, and consumer id are required")
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
		notification := TaskNotification{
			NotificationID: record.NotificationID, TaskID: record.TaskID,
			TransitionVersion: record.TransitionVersion, UpdateSequence: record.UpdateSequence,
			EventKind: record.EventKind, Status: record.Status, Progress: cloneProgress(record.Progress),
			Result: cloneResult(record.Result), Reason: record.Reason,
		}
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
