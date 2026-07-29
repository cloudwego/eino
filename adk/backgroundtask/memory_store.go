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
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Clock func() time.Time

type MemoryStoreConfig struct {
	Clock            Clock
	MinLeaseDuration time.Duration
	MaxLeaseDuration time.Duration
	MaxValueBytes    int64
}

type memoryOutboxItem struct {
	record       *NotificationOutboxRecord
	receipt      NotificationReceipt
	visibleAfter time.Time
}

// MemoryStore is a deterministic reference implementation of Store and
// NotificationOutbox. It is a state-machine test double, not a durable backend.
type MemoryStore struct {
	mu       sync.Mutex
	tasks    map[string]*Task
	outbox   []*memoryOutboxItem
	notify   chan struct{}
	now      Clock
	minLease time.Duration
	maxLease time.Duration
	maxValue int64
}

func NewMemoryStore(config *MemoryStoreConfig) *MemoryStore {
	s := &MemoryStore{
		tasks:    make(map[string]*Task),
		notify:   make(chan struct{}),
		now:      time.Now,
		minLease: time.Millisecond,
		maxLease: 24 * time.Hour,
		maxValue: 1 << 20,
	}
	if config != nil {
		if config.Clock != nil {
			s.now = config.Clock
		}
		if config.MinLeaseDuration > 0 {
			s.minLease = config.MinLeaseDuration
		}
		if config.MaxLeaseDuration > 0 {
			s.maxLease = config.MaxLeaseDuration
		}
		if config.MaxValueBytes > 0 {
			s.maxValue = config.MaxValueBytes
		}
	}
	return s
}

func (s *MemoryStore) signalLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

func (s *MemoryStore) Create(_ context.Context, req *CreateTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: create request is required")
	}
	if err := validateSpec(req.Spec); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[req.Spec.ID]; ok {
		return nil, ErrAlreadyExists
	}
	now := s.now()
	spec := cloneSpec(req.Spec)
	if spec.CreatedAt.IsZero() {
		spec.CreatedAt = now
	}
	task := &Task{
		Spec:              spec,
		Status:            StatusPending,
		TransitionVersion: 1,
		UpdatedAt:         now,
	}
	s.tasks[spec.ID] = task
	s.signalLocked()
	return cloneTask(task), nil
}

func (s *MemoryStore) Get(_ context.Context, taskID string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, ErrNotFound
	}
	s.resolveExpiredLocked(t)
	return cloneTask(t), nil
}

func (s *MemoryStore) ListClaimable(_ context.Context, req *ListClaimableRequest) (*ListClaimableResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: list claimable request is required")
	}
	caps := make(map[string]struct{}, len(req.Capabilities))
	for _, c := range req.Capabilities {
		caps[c.ExecutorKey] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	start := 0
	if req.Cursor != "" {
		start, _ = strconv.Atoi(req.Cursor)
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	result := &ListClaimableResult{}
	for i := start; i < len(ids); i++ {
		t := s.tasks[ids[i]]
		s.resolveExpiredLocked(t)
		if t.Status != StatusPending {
			continue
		}
		if _, ok := caps[t.Spec.ExecutorKey]; !ok {
			continue
		}
		result.Tasks = append(result.Tasks, cloneTask(t))
		if len(result.Tasks) == limit {
			result.NextCursor = strconv.Itoa(i + 1)
			break
		}
	}
	return result, nil
}

func (s *MemoryStore) Claim(_ context.Context, req *ClaimTaskRequest) (*ClaimTaskResult, error) {
	if req == nil || req.LeaseOwnerID == "" {
		return nil, errors.New("backgroundtask: claim request and lease owner id are required")
	}
	if err := s.validateLeaseDuration(req.LeaseDuration); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.taskVersionLocked(req.TaskID, req.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	s.resolveExpiredLocked(t)
	if t.TransitionVersion != req.ExpectedVersion {
		return nil, ErrVersionConflict
	}
	if t.Status != StatusPending {
		return nil, ErrIllegalTransition
	}
	if t.PendingResume != nil && t.PendingResume.CheckpointVersion != t.CheckpointVersion {
		t.PendingResume = nil
		s.advanceLocked(t)
	}
	t.Status = StatusRunning
	t.Attempt++
	t.LeaseOwner = req.LeaseOwnerID
	t.LeaseGeneration++
	t.LeaseExpiresAt = s.now().Add(req.LeaseDuration)
	s.advanceLocked(t)
	s.signalLocked()
	return &ClaimTaskResult{
		Task: cloneTask(t),
		Lease: LeaseToken{
			TaskID: t.Spec.ID, ExpectedVersion: t.TransitionVersion,
			LeaseOwnerID: t.LeaseOwner, Generation: t.LeaseGeneration,
		},
	}, nil
}

func (s *MemoryStore) Renew(_ context.Context, req *RenewLeaseRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: renew request is required")
	}
	if err := s.validateLeaseDuration(req.LeaseDuration); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.leasedTaskLocked(req.Lease, StatusRunning)
	if err != nil {
		return nil, err
	}
	if !s.now().Before(t.LeaseExpiresAt) {
		return nil, ErrLeaseLost
	}
	t.LeaseExpiresAt = s.now().Add(req.LeaseDuration)
	s.advanceLocked(t)
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) Commit(_ context.Context, req *CommitTaskRequest) (*CommitTaskResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: commit request is required")
	}
	if err := validateTaskSnapshot(req.Status, req.Result); err != nil {
		return nil, err
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("backgroundtask: checkpoint exceeds configured limit")
	}
	if req.Result != nil && int64(len(req.Result.Data)) > s.maxValue {
		return nil, errors.New("backgroundtask: result data exceeds configured limit")
	}
	if (req.Status == StatusWaitingInput || req.Status == StatusSuspended) && len(req.Checkpoint) == 0 {
		return nil, errors.New("backgroundtask: checkpointed pause requires checkpoint data")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.leasedTaskLocked(req.Lease, StatusRunning, StatusCanceling)
	if err != nil {
		return nil, err
	}
	if !legalWorkerTransition(t.Status, req.Status) {
		return nil, ErrIllegalTransition
	}
	t.Status = req.Status
	if req.Checkpoint != nil {
		t.Checkpoint = cloneBytes(req.Checkpoint)
		t.CheckpointVersion++
		t.PendingResume = nil
	}
	t.Result = cloneResult(req.Result)
	if terminalStatus(t.Status) {
		now := s.now()
		t.DoneAt = &now
	}
	if t.Status != StatusRunning && t.Status != StatusCanceling {
		s.clearLeaseLocked(t)
	}
	s.advanceLocked(t)
	n := s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return &CommitTaskResult{Task: cloneTask(t), Notification: cloneNotification(n)}, nil
}

func (s *MemoryStore) RequestCancel(_ context.Context, req *RequestCancelRequest) (*RequestCancelResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: cancel request is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.taskVersionLocked(req.TaskID, req.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	if terminalStatus(t.Status) {
		return nil, ErrAlreadyTerminal
	}
	if t.Status == StatusCanceling {
		return &RequestCancelResult{Task: cloneTask(t)}, nil
	}
	now := s.now()
	t.CancelRequestedAt = &now
	if t.Status == StatusRunning && now.Before(t.LeaseExpiresAt) {
		t.Status = StatusCanceling
		s.advanceLocked(t)
		t.CancelTransitionVersion = t.TransitionVersion
	} else {
		t.Status = StatusCanceled
		t.Result = &Result{Error: "canceled"}
		t.PendingResume = nil
		s.clearLeaseLocked(t)
		s.advanceLocked(t)
		t.DoneAt = cloneTime(&now)
	}
	n := s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return &RequestCancelResult{Task: cloneTask(t), Notification: cloneNotification(n)}, nil
}

func (s *MemoryStore) Resume(_ context.Context, req *ResumeTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: resume request is required")
	}
	if int64(len(req.Data)) > s.maxValue {
		return nil, errors.New("backgroundtask: resume data exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.taskVersionLocked(req.TaskID, req.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	if t.Status != StatusWaitingInput {
		return nil, ErrIllegalTransition
	}
	t.PendingResume = &PendingResume{
		CheckpointVersion: t.CheckpointVersion,
		Data:              cloneBytes(req.Data),
	}
	t.Status = StatusPending
	s.advanceLocked(t)
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) ReleaseSuspension(_ context.Context, req *ReleaseSuspensionRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: release request is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.taskVersionLocked(req.TaskID, req.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	if t.Status != StatusSuspended {
		return nil, ErrIllegalTransition
	}
	t.Status = StatusPending
	s.advanceLocked(t)
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) Wait(ctx context.Context, req *WaitTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: wait request is required")
	}
	for {
		s.mu.Lock()
		t, ok := s.tasks[req.TaskID]
		if !ok {
			s.mu.Unlock()
			return nil, ErrNotFound
		}
		s.resolveExpiredLocked(t)
		if t.TransitionVersion > req.AfterVersion {
			out := cloneTask(t)
			s.mu.Unlock()
			return out, nil
		}
		wait := s.notify
		s.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *MemoryStore) Receive(_ context.Context, req *ReceiveNotificationsRequest) (*ReceiveNotificationsResult, error) {
	if req == nil || req.ConsumerID == "" || req.VisibilityTime <= 0 {
		return nil, errors.New("backgroundtask: consumer and positive visibility time are required")
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	result := &ReceiveNotificationsResult{}
	for i, item := range s.outbox {
		if len(result.Deliveries) == limit {
			break
		}
		if item.visibleAfter.After(now) {
			continue
		}
		item.receipt = NotificationReceipt(base64.RawURLEncoding.EncodeToString(
			[]byte(fmt.Sprintf("%s:%d:%d", req.ConsumerID, i, now.UnixNano()))))
		item.visibleAfter = now.Add(req.VisibilityTime)
		result.Deliveries = append(result.Deliveries, NotificationDelivery{
			Record: *cloneNotification(item.record), Receipt: append(NotificationReceipt(nil), item.receipt...),
		})
	}
	return result, nil
}

func (s *MemoryStore) Ack(_ context.Context, receipt NotificationReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.outbox {
		if string(item.receipt) == string(receipt) && len(receipt) > 0 {
			s.outbox = append(s.outbox[:i], s.outbox[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) taskVersionLocked(id string, version int64) (*Task, error) {
	t, ok := s.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	if t.TransitionVersion != version {
		return nil, ErrVersionConflict
	}
	return t, nil
}

func (s *MemoryStore) leasedTaskLocked(token LeaseToken, allowed ...Status) (*Task, error) {
	t, ok := s.tasks[token.TaskID]
	if !ok {
		return nil, ErrNotFound
	}
	if t.TransitionVersion != token.ExpectedVersion {
		return nil, ErrVersionConflict
	}
	if t.LeaseOwner != token.LeaseOwnerID || t.LeaseGeneration != token.Generation {
		return nil, ErrLeaseLost
	}
	ok = false
	for _, status := range allowed {
		ok = ok || t.Status == status
	}
	if !ok || !s.now().Before(t.LeaseExpiresAt) {
		return nil, ErrLeaseLost
	}
	return t, nil
}

func (s *MemoryStore) validateLeaseDuration(d time.Duration) error {
	if d < s.minLease || d > s.maxLease {
		return fmt.Errorf("backgroundtask: lease duration must be within [%s, %s]", s.minLease, s.maxLease)
	}
	return nil
}

func (s *MemoryStore) advanceLocked(t *Task) {
	t.TransitionVersion++
	t.UpdatedAt = s.now()
}

func (s *MemoryStore) enqueueLocked(t *Task, kind NotificationEventKind) *NotificationOutboxRecord {
	if t.Spec.Notify == nil || kind == "" {
		return nil
	}
	n := &NotificationOutboxRecord{
		NotificationID: fmt.Sprintf("%s:%d:%s", t.Spec.ID, t.TransitionVersion, kind),
		TaskID:         t.Spec.ID, TransitionVersion: t.TransitionVersion,
		EventKind: kind, Target: *cloneSpec(t.Spec).Notify, CreatedAt: s.now(),
	}
	s.outbox = append(s.outbox, &memoryOutboxItem{record: n})
	return n
}

func legalWorkerTransition(from, to Status) bool {
	if from == StatusRunning {
		switch to {
		case StatusRunning, StatusWaitingInput, StatusSuspended, StatusCompleted, StatusFailed:
			return true
		case StatusCanceled:
			return false
		}
	}
	return from == StatusCanceling && (to == StatusCanceled || to == StatusCompleted || to == StatusFailed)
}

func eventForStatus(status Status) NotificationEventKind {
	switch status {
	case StatusWaitingInput:
		return NotificationWaitingInput
	case StatusCompleted:
		return NotificationCompleted
	case StatusFailed:
		return NotificationFailed
	case StatusCanceled:
		return NotificationCanceled
	default:
		return ""
	}
}

func (s *MemoryStore) clearLeaseLocked(t *Task) {
	t.LeaseOwner = ""
	t.LeaseExpiresAt = time.Time{}
}

func (s *MemoryStore) resolveExpiredLocked(t *Task) {
	if t.Status != StatusRunning && t.Status != StatusCanceling {
		return
	}
	if t.LeaseExpiresAt.IsZero() || s.now().Before(t.LeaseExpiresAt) {
		return
	}
	if t.Status == StatusCanceling {
		t.Status = StatusCanceled
		t.Result = &Result{Error: "canceled"}
		s.finishStoreOwnedLocked(t)
		return
	}
	t.Status = StatusPending
	t.PendingResume = nil
	s.clearLeaseLocked(t)
	s.advanceLocked(t)
	s.signalLocked()
}

func (s *MemoryStore) failStoreOwnedLocked(t *Task, reason string) {
	t.Status = StatusFailed
	t.Result = &Result{Error: reason}
	s.finishStoreOwnedLocked(t)
}

func (s *MemoryStore) finishStoreOwnedLocked(t *Task) {
	s.clearLeaseLocked(t)
	s.advanceLocked(t)
	now := s.now()
	t.DoneAt = &now
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
}
