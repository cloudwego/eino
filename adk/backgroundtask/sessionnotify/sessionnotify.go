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

// Package sessionnotify routes background task notifications into session inboxes.
package sessionnotify

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

// Runtime is the construction-time proof that session notification delivery is
// configured and operationally owned by the host.
type Runtime struct {
	sinks backgroundtask.NotificationSinkRegistry
	// CheckReady must verify that outbox dispatch and session inbox consumption
	// are running or otherwise owned by the host. It may be called repeatedly
	// during composition and must be side-effect-free.
	checkReady func(context.Context) error
}

// NewRuntime constructs a session notification delivery runtime.
func NewRuntime(
	sinks backgroundtask.NotificationSinkRegistry,
	checkReady func(context.Context) error,
) (*Runtime, error) {
	if sinks == nil || checkReady == nil {
		return nil, errors.New("sessionnotify: sink registry and readiness check are required")
	}
	return &Runtime{sinks: sinks, checkReady: checkReady}, nil
}

// ValidateNotificationDelivery validates the complete session-inbox route.
func (r *Runtime) ValidateNotificationDelivery(
	ctx context.Context,
	req *backgroundtask.NotificationDeliveryValidation,
) error {
	if r == nil || req == nil || req.Store == nil {
		return errors.New("sessionnotify: runtime and task store are required")
	}
	if req.TargetKind != backgroundtask.SessionInboxNotificationKind {
		return errors.New("sessionnotify: unsupported notification target kind")
	}
	if _, ok := req.Store.(backgroundtask.NotificationOutbox); !ok {
		return errors.New("sessionnotify: task store must implement NotificationOutbox")
	}
	if r.sinks == nil {
		return errors.New("sessionnotify: sink registry is required")
	}
	sink, ok := r.sinks.Resolve(req.TargetKind)
	if !ok {
		return errors.New("sessionnotify: session inbox sink is unavailable")
	}
	if _, ok = sink.(backgroundtask.RoutedNotificationSink); !ok {
		return errors.New("sessionnotify: session inbox sink must support routed delivery")
	}
	validating, ok := sink.(backgroundtask.ValidatingNotificationSink)
	if !ok {
		return errors.New("sessionnotify: session inbox sink must support validation")
	}
	if err := validating.ValidateNotificationSink(); err != nil {
		return err
	}
	if r.checkReady == nil {
		return errors.New("sessionnotify: delivery readiness check is required")
	}
	return r.checkReady(ctx)
}

// Sink durably enqueues before requesting activation. A delivery may be
// acknowledged only after Accept returns nil.
type Sink struct {
	inbox     backgroundtask.SessionNotificationInbox
	activator backgroundtask.SessionActivator
}

// NewSink constructs a validated session notification sink.
func NewSink(
	inbox backgroundtask.SessionNotificationInbox,
	activator backgroundtask.SessionActivator,
) (*Sink, error) {
	sink := &Sink{inbox: inbox, activator: activator}
	if err := sink.ValidateNotificationSink(); err != nil {
		return nil, err
	}
	return sink, nil
}

// ValidateNotificationSink validates the sink's durable inbox and activation path.
func (s *Sink) ValidateNotificationSink() error {
	if s == nil || s.inbox == nil || s.activator == nil {
		return errors.New("sessionnotify: inbox and activator are required")
	}
	return nil
}

// Accept rejects unrouted deliveries because Sink requires the serialized target.
func (s *Sink) Accept(ctx context.Context, notification backgroundtask.Notification) error {
	return errors.New("sessionnotify: routed notification target is required")
}

// AcceptTarget enqueues notification for target and requests a session turn.
func (s *Sink) AcceptTarget(ctx context.Context, target backgroundtask.NotificationTarget, notification backgroundtask.Notification) error {
	if err := s.ValidateNotificationSink(); err != nil {
		return err
	}
	if notification.ID == "" || target.TargetID == "" {
		return errors.New("sessionnotify: notification id and target are required")
	}
	item, err := s.inbox.Enqueue(ctx, &backgroundtask.EnqueueSessionNotificationRequest{
		SessionID: target.TargetID, Notification: notification,
	})
	if err != nil {
		return err
	}
	_, err = s.activator.RequestTurn(ctx, &backgroundtask.SessionActivationRequest{SessionID: item.SessionID})
	return err
}

// MemoryInbox is a process-local SessionNotificationInbox implementation.
type MemoryInbox struct {
	mu     sync.Mutex
	byID   map[string]*backgroundtask.SessionInboxItem
	byNote map[string]*backgroundtask.SessionInboxItem
	now    func() time.Time
}

// NewMemoryInbox creates an empty process-local session notification inbox.
func NewMemoryInbox() *MemoryInbox {
	return &MemoryInbox{
		byID:   make(map[string]*backgroundtask.SessionInboxItem),
		byNote: make(map[string]*backgroundtask.SessionInboxItem), now: time.Now,
	}
}

// Enqueue stores a notification unless the same notification was already seen.
func (i *MemoryInbox) Enqueue(_ context.Context, req *backgroundtask.EnqueueSessionNotificationRequest) (*backgroundtask.SessionInboxItem, error) {
	if req == nil || req.SessionID == "" || req.Notification.ID == "" {
		return nil, errors.New("sessionnotify: session and notification id are required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if item, ok := i.byNote[req.Notification.ID]; ok {
		return cloneItem(item), nil
	}
	item := &backgroundtask.SessionInboxItem{
		ItemID: req.Notification.ID, ItemVersion: 1,
		SessionID: req.SessionID, Notification: cloneNotification(req.Notification), CreatedAt: i.now(),
	}
	i.byID[item.ItemID] = item
	i.byNote[req.Notification.ID] = cloneItem(item)
	return cloneItem(item), nil
}

// ListPending lists pending inbox items for a session in creation order.
func (i *MemoryInbox) ListPending(_ context.Context, req *backgroundtask.ListSessionNotificationsRequest) ([]*backgroundtask.SessionInboxItem, error) {
	if req == nil || req.SessionID == "" {
		return nil, errors.New("sessionnotify: session id is required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	result := make([]*backgroundtask.SessionInboxItem, 0)
	for _, item := range i.byID {
		if item.SessionID == req.SessionID {
			result = append(result, cloneItem(item))
		}
	}
	sort.Slice(result, func(a, b int) bool { return result[a].CreatedAt.Before(result[b].CreatedAt) })
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// Ack removes a pending inbox item if the expected version matches.
func (i *MemoryInbox) Ack(_ context.Context, req *backgroundtask.AckSessionNotificationRequest) error {
	if req == nil {
		return errors.New("sessionnotify: ack request is required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	item, ok := i.byID[req.ItemID]
	if !ok {
		return nil
	}
	if item.SessionID != req.SessionID || item.ItemVersion != req.ExpectedVersion {
		return backgroundtask.ErrVersionConflict
	}
	delete(i.byID, req.ItemID)
	return nil
}

func cloneItem(item *backgroundtask.SessionInboxItem) *backgroundtask.SessionInboxItem {
	if item == nil {
		return nil
	}
	c := *item
	c.Notification = cloneNotification(item.Notification)
	return &c
}

func cloneNotification(notification backgroundtask.Notification) backgroundtask.Notification {
	cloned := notification
	cloned.Target.Metadata = make(map[string]string, len(notification.Target.Metadata))
	for key, value := range notification.Target.Metadata {
		cloned.Target.Metadata[key] = value
	}
	cloned.Task = cloneTaskSnapshot(notification.Task)
	return cloned
}

func cloneTaskSnapshot(task *backgroundtask.Task) *backgroundtask.Task {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.Spec.Payload = append([]byte(nil), task.Spec.Payload...)
	if task.Spec.Notify != nil {
		target := *task.Spec.Notify
		target.Metadata = make(map[string]string, len(task.Spec.Notify.Metadata))
		for key, value := range task.Spec.Notify.Metadata {
			target.Metadata[key] = value
		}
		cloned.Spec.Notify = &target
	}
	cloned.ResultData = append([]byte(nil), task.ResultData...)
	cloned.Checkpoint = append([]byte(nil), task.Checkpoint...)
	cloned.PendingResume = append([]byte(nil), task.PendingResume...)
	cloned.CancelRequestedAt = cloneTime(task.CancelRequestedAt)
	cloned.DoneAt = cloneTime(task.DoneAt)
	return &cloned
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
