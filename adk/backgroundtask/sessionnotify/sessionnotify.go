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

// Package sessionnotify provides durable session delivery for background task
// notifications.
package sessionnotify

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

// Inbox stores notifications awaiting parent session turns. Enqueue
// deduplicates by (SessionID, Notification.ID), ListPending returns creation
// order with provider-normalized bounds, and Ack uses ItemVersion CAS.
type Inbox interface {
	Enqueue(context.Context, *EnqueueRequest) (*InboxItem, error)
	ListPending(context.Context, *ListRequest) ([]*InboxItem, error)
	Ack(context.Context, *AckRequest) error
}

// EnqueueRequest enqueues a notification for a session.
type EnqueueRequest struct {
	SessionID    string
	Notification backgroundtask.Notification
}

// InboxItem is a durable session notification inbox entry.
type InboxItem struct {
	ItemID       string
	ItemVersion  int64
	SessionID    string
	Notification backgroundtask.Notification
	CreatedAt    time.Time
}

// ListRequest lists pending notifications for a session in creation order.
// Limit defaults to 100 and is capped at 1000.
type ListRequest struct {
	SessionID string
	Limit     int
}

// AckRequest acknowledges a session inbox item.
type AckRequest struct {
	SessionID       string
	ItemID          string
	ExpectedVersion int64
}

// MemoryInbox is a process-local Inbox implementation.
type MemoryInbox struct {
	mu     sync.Mutex
	byID   map[string]*InboxItem
	byNote map[string]*InboxItem
	now    func() time.Time
}

// NewMemoryInbox creates an empty process-local session notification inbox.
func NewMemoryInbox() *MemoryInbox {
	return &MemoryInbox{
		byID:   make(map[string]*InboxItem),
		byNote: make(map[string]*InboxItem), now: time.Now,
	}
}

// Enqueue stores a notification unless the same notification ID was already
// seen in that session. Deduplication survives acknowledgement for the lifetime
// of this process-local inbox.
func (i *MemoryInbox) Enqueue(_ context.Context, req *EnqueueRequest) (*InboxItem, error) {
	if req == nil || req.SessionID == "" || req.Notification.ID == "" {
		return nil, errors.New("sessionnotify: session and notification id are required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	key := inboxKey(req.SessionID, req.Notification.ID)
	if item, ok := i.byNote[key]; ok {
		return cloneItem(item), nil
	}
	item := &InboxItem{
		ItemID: req.Notification.ID, ItemVersion: 1,
		SessionID: req.SessionID, Notification: cloneNotification(req.Notification), CreatedAt: i.now(),
	}
	i.byID[key] = item
	i.byNote[key] = cloneItem(item)
	return cloneItem(item), nil
}

// ListPending lists pending inbox items for a session in creation order. Limit
// defaults to 100 and is capped at 1000.
func (i *MemoryInbox) ListPending(_ context.Context, req *ListRequest) ([]*InboxItem, error) {
	if req == nil || req.SessionID == "" {
		return nil, errors.New("sessionnotify: session id is required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	result := make([]*InboxItem, 0)
	for _, item := range i.byID {
		if item.SessionID == req.SessionID {
			result = append(result, cloneItem(item))
		}
	}
	sort.Slice(result, func(a, b int) bool {
		if result[a].CreatedAt.Equal(result[b].CreatedAt) {
			return result[a].ItemID < result[b].ItemID
		}
		return result[a].CreatedAt.Before(result[b].CreatedAt)
	})
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// Ack removes a pending inbox item if the expected version matches.
func (i *MemoryInbox) Ack(_ context.Context, req *AckRequest) error {
	if req == nil {
		return errors.New("sessionnotify: ack request is required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	key := inboxKey(req.SessionID, req.ItemID)
	item, ok := i.byID[key]
	if !ok {
		return nil
	}
	if item.SessionID != req.SessionID || item.ItemVersion != req.ExpectedVersion {
		return backgroundtask.ErrVersionConflict
	}
	delete(i.byID, key)
	return nil
}

func inboxKey(sessionID, itemID string) string {
	return sessionID + "\x00" + itemID
}

func cloneItem(item *InboxItem) *InboxItem {
	if item == nil {
		return nil
	}
	c := *item
	c.Notification = cloneNotification(item.Notification)
	return &c
}

func cloneNotification(notification backgroundtask.Notification) backgroundtask.Notification {
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
