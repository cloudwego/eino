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
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

// Sink durably enqueues before requesting activation. A delivery may be
// acknowledged only after Accept returns nil.
type Sink struct {
	Inbox     backgroundtask.SessionNotificationInbox
	Activator backgroundtask.SessionActivator
}

func (s *Sink) Accept(ctx context.Context, notification backgroundtask.TaskNotification) error {
	return errors.New("sessionnotify: routed notification target is required")
}

func (s *Sink) AcceptTarget(ctx context.Context, target backgroundtask.NotificationTarget, notification backgroundtask.TaskNotification) error {
	if s.Inbox == nil || s.Activator == nil {
		return errors.New("sessionnotify: inbox and activator are required")
	}
	if notification.NotificationID == "" || target.TargetID == "" {
		return errors.New("sessionnotify: notification id and target are required")
	}
	item, err := s.Inbox.Enqueue(ctx, &backgroundtask.EnqueueSessionNotificationRequest{
		SessionID: target.TargetID, Notification: notification,
	})
	if err != nil {
		return err
	}
	_, err = s.Activator.RequestTurn(ctx, &backgroundtask.SessionActivationRequest{SessionID: item.SessionID})
	return err
}

type MemoryInbox struct {
	mu     sync.Mutex
	byID   map[string]*backgroundtask.SessionInboxItem
	byNote map[string]*backgroundtask.SessionInboxItem
	now    func() time.Time
}

func NewMemoryInbox() *MemoryInbox {
	return &MemoryInbox{
		byID:   make(map[string]*backgroundtask.SessionInboxItem),
		byNote: make(map[string]*backgroundtask.SessionInboxItem), now: time.Now,
	}
}

func (i *MemoryInbox) Enqueue(_ context.Context, req *backgroundtask.EnqueueSessionNotificationRequest) (*backgroundtask.SessionInboxItem, error) {
	if req == nil || req.SessionID == "" || req.Notification.NotificationID == "" {
		return nil, errors.New("sessionnotify: session and notification id are required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if item, ok := i.byNote[req.Notification.NotificationID]; ok {
		return cloneItem(item), nil
	}
	item := &backgroundtask.SessionInboxItem{
		ItemID: req.Notification.NotificationID, ItemVersion: 1,
		SessionID: req.SessionID, Notification: cloneNotification(req.Notification), CreatedAt: i.now(),
	}
	i.byID[item.ItemID] = item
	i.byNote[req.Notification.NotificationID] = cloneItem(item)
	return cloneItem(item), nil
}

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

func cloneNotification(notification backgroundtask.TaskNotification) backgroundtask.TaskNotification {
	cloned := notification
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
	if task.Result != nil {
		result := *task.Result
		result.Data = append([]byte(nil), task.Result.Data...)
		cloned.Result = &result
	}
	cloned.Checkpoint = append([]byte(nil), task.Checkpoint...)
	if task.PendingResume != nil {
		pending := *task.PendingResume
		pending.Data = append([]byte(nil), task.PendingResume.Data...)
		cloned.PendingResume = &pending
	}
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
