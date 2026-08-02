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

// Clock returns the Store's current time.
type Clock func() time.Time

// MemoryStoreConfig configures the in-memory reference Store.
type MemoryStoreConfig struct {
	Clock                Clock
	ActiveAttemptTimeout time.Duration
	MaxValueBytes        int64
}

type memoryOutboxItem struct {
	record       *Notification
	receipt      NotificationReceipt
	visibleAfter time.Time
}

type memoryActiveAttempt struct {
	expiresAt time.Time
}

// MemoryStore is a deterministic reference implementation of Store and
// NotificationOutbox. It is a state-machine test double, not a durable backend.
type MemoryStore struct {
	mu            sync.Mutex
	tasks         map[string]*Task
	active        map[string]memoryActiveAttempt
	outbox        []*memoryOutboxItem
	notify        chan struct{}
	now           Clock
	activeTimeout time.Duration
	maxValue      int64
}

// NewMemoryStore creates an in-memory reference Store and NotificationOutbox.
func NewMemoryStore(config *MemoryStoreConfig) *MemoryStore {
	s := &MemoryStore{
		tasks:         make(map[string]*Task),
		active:        make(map[string]memoryActiveAttempt),
		notify:        make(chan struct{}),
		now:           time.Now,
		activeTimeout: 30 * time.Second,
		maxValue:      1 << 20,
	}
	if config != nil {
		if config.Clock != nil {
			s.now = config.Clock
		}
		if config.ActiveAttemptTimeout > 0 {
			s.activeTimeout = config.ActiveAttemptTimeout
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
		Spec:      spec,
		Status:    StatusPending,
		Version:   1,
		UpdatedAt: now,
	}
	s.tasks[spec.ID] = task
	s.signalLocked()
	return cloneTask(task), nil
}

func (s *MemoryStore) CreateAndStart(_ context.Context, req *CreateTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: create and start request is required")
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
		Spec: spec, Status: StatusRunning, Version: 1, Attempt: 1, UpdatedAt: now,
	}
	s.tasks[spec.ID] = task
	s.active[spec.ID] = memoryActiveAttempt{expiresAt: now.Add(s.activeTimeout)}
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

func (s *MemoryStore) ListPending(_ context.Context, req *ListPendingRequest) (*ListPendingResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: list pending request is required")
	}
	if len(req.ExecutorKeys) == 0 {
		return nil, errors.New("backgroundtask: list pending requires executor keys")
	}
	executorKeys := make(map[string]struct{}, len(req.ExecutorKeys))
	for _, key := range req.ExecutorKeys {
		if key == "" {
			return nil, errors.New("backgroundtask: list pending executor key is required")
		}
		executorKeys[key] = struct{}{}
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
	result := &ListPendingResult{}
	for i := start; i < len(ids); i++ {
		t := s.tasks[ids[i]]
		s.resolveExpiredLocked(t)
		if t.Status != StatusPending {
			continue
		}
		if _, ok := executorKeys[t.Spec.ExecutorKey]; !ok {
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

func (s *MemoryStore) Start(_ context.Context, req *StartTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: start request is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.taskVersionLocked(req.TaskID, req.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	s.resolveExpiredLocked(t)
	if t.Version != req.ExpectedVersion {
		return nil, ErrVersionConflict
	}
	if t.Status != StatusPending {
		return nil, ErrIllegalTransition
	}
	t.Status = StatusRunning
	t.Attempt++
	s.advanceLocked(t)
	s.active[t.Spec.ID] = memoryActiveAttempt{expiresAt: s.now().Add(s.activeTimeout)}
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) Heartbeat(_ context.Context, req *HeartbeatRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: heartbeat request is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	s.advanceLocked(t)
	s.active[t.Spec.ID] = memoryActiveAttempt{expiresAt: s.now().Add(s.activeTimeout)}
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) ReportOutputFailure(_ context.Context, req *ReportOutputFailureRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: report output failure request is required")
	}
	if err := validateOutputFailure(req.Error); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning, StatusCanceling)
	if err != nil {
		return nil, err
	}
	if t.OutputFileErr != "" {
		return cloneTask(t), nil
	}
	t.OutputFileErr = req.Error
	s.advanceLocked(t)
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) Complete(_ context.Context, req *CompleteTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: complete request is required")
	}
	if err := validateTaskSnapshot(StatusCompleted, req.Data, ""); err != nil {
		return nil, err
	}
	if int64(len(req.Data)) > s.maxValue {
		return nil, errors.New("backgroundtask: result data exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	t.Status = StatusCompleted
	t.ResultData = cloneBytes(req.Data)
	t.ResultError = ""
	s.finishStoreOwnedLocked(t)
	return cloneTask(t), nil
}

func (s *MemoryStore) Fail(_ context.Context, req *FailTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: fail request is required")
	}
	if err := validateTaskSnapshot(StatusFailed, nil, req.Error); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	t.Status = StatusFailed
	t.ResultData = nil
	t.ResultError = req.Error
	s.finishStoreOwnedLocked(t)
	return cloneTask(t), nil
}

func (s *MemoryStore) WaitInput(_ context.Context, req *WaitInputTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: wait input request is required")
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("backgroundtask: checkpoint exceeds configured limit")
	}
	if len(req.Checkpoint) == 0 {
		return nil, errors.New("backgroundtask: checkpointed pause requires checkpoint data")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	t.Status = StatusWaitingInput
	t.Checkpoint = cloneBytes(req.Checkpoint)
	t.PendingResume = nil
	s.clearActiveLocked(t)
	s.advanceLocked(t)
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) Suspend(_ context.Context, req *SuspendTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: suspend request is required")
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("backgroundtask: checkpoint exceeds configured limit")
	}
	if len(req.Checkpoint) == 0 {
		return nil, errors.New("backgroundtask: checkpointed pause requires checkpoint data")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	t.Status = StatusSuspended
	t.Checkpoint = cloneBytes(req.Checkpoint)
	t.PendingResume = nil
	s.clearActiveLocked(t)
	s.advanceLocked(t)
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) Cancel(_ context.Context, req *CancelTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: cancel request is required")
	}
	if err := validateTaskSnapshot(StatusCanceled, nil, canceledError); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning, StatusCanceling)
	if err != nil {
		return nil, err
	}
	t.Status = StatusCanceled
	t.ResultData = nil
	t.ResultError = canceledError
	t.PendingResume = nil
	s.finishStoreOwnedLocked(t)
	return cloneTask(t), nil
}

func (s *MemoryStore) RequestCancel(_ context.Context, req *RequestCancelRequest) (*Task, error) {
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
		return cloneTask(t), nil
	}
	now := s.now()
	t.CancelRequestedAt = &now
	if t.Status == StatusRunning {
		t.Status = StatusCanceling
		s.advanceLocked(t)
		if _, ok := s.active[t.Spec.ID]; ok {
			s.active[t.Spec.ID] = memoryActiveAttempt{expiresAt: now.Add(s.activeTimeout)}
		}
	} else {
		t.Status = StatusCanceled
		t.ResultError = "canceled"
		t.PendingResume = nil
		s.finishStoreOwnedLocked(t)
		return cloneTask(t), nil
	}
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return cloneTask(t), nil
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
	t.PendingResume = cloneBytes(req.Data)
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
		if t.Version > req.AfterVersion {
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
	if t.Version != version {
		return nil, ErrVersionConflict
	}
	return t, nil
}

func (s *MemoryStore) activeTaskLocked(id string, version int64, allowed ...Status) (*Task, error) {
	t, ok := s.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	s.resolveExpiredLocked(t)
	if t.Version != version {
		return nil, ErrVersionConflict
	}
	ok = false
	for _, status := range allowed {
		ok = ok || t.Status == status
	}
	active, activeOK := s.active[id]
	if !ok || !activeOK || !s.now().Before(active.expiresAt) {
		return nil, ErrLeaseLost
	}
	return t, nil
}

func (s *MemoryStore) advanceLocked(t *Task) {
	t.Version++
	t.UpdatedAt = s.now()
}

func (s *MemoryStore) enqueueLocked(t *Task, kind NotificationKind) *Notification {
	if t.Spec.Notify == nil || kind == "" {
		return nil
	}
	n := &Notification{
		ID:     fmt.Sprintf("%s:%d:%s", t.Spec.ID, t.Version, kind),
		TaskID: t.Spec.ID, Version: t.Version,
		Kind: kind, Target: *cloneSpec(t.Spec).Notify, CreatedAt: s.now(),
	}
	s.outbox = append(s.outbox, &memoryOutboxItem{record: n})
	return n
}

func eventForStatus(status Status) NotificationKind {
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

func (s *MemoryStore) clearActiveLocked(t *Task) {
	delete(s.active, t.Spec.ID)
}

func (s *MemoryStore) resolveExpiredLocked(t *Task) {
	if t.Status != StatusRunning && t.Status != StatusCanceling {
		return
	}
	active, ok := s.active[t.Spec.ID]
	if ok && s.now().Before(active.expiresAt) {
		return
	}
	s.clearActiveLocked(t)
	if t.Status == StatusCanceling {
		t.Status = StatusCanceled
		t.ResultError = "canceled"
		s.finishStoreOwnedLocked(t)
		return
	}
	if t.Spec.LeaseExpiryPolicy == LeaseExpiryRetry {
		t.Status = StatusPending
		t.PendingResume = nil
		s.advanceLocked(t)
		s.signalLocked()
		return
	}
	t.Status = StatusFailed
	t.ResultError = "execution lease expired and retry is disabled"
	s.finishStoreOwnedLocked(t)
}

func (s *MemoryStore) finishStoreOwnedLocked(t *Task) {
	s.clearActiveLocked(t)
	s.advanceLocked(t)
	now := s.now()
	t.DoneAt = &now
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
}
