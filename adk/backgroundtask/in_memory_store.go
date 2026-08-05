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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// InMemoryStoreConfig configures the in-memory reference task provider.
type InMemoryStoreConfig struct {
	ActiveAttemptTimeout time.Duration
	MaxValueBytes        int64
}

type memoryOutboxItem struct {
	record         *Notification
	receipt        NotificationReceipt
	leaseExpiresAt time.Time
}

type memoryActiveAttempt struct {
	expiresAt time.Time
}

// InMemoryStore is a deterministic reference implementation of TaskStore,
// TaskEventStore, and NotificationOutbox. It is a state-machine test double,
// not a durable backend.
type InMemoryStore struct {
	mu            sync.Mutex
	tasks         map[string]*Task
	active        map[string]memoryActiveAttempt
	taskEvents    map[string][]TaskEvent
	taskEventKeys map[string]map[string]TaskEvent
	outbox        []*memoryOutboxItem
	outboxLeaseID uint64
	notify        chan struct{}
	now           func() time.Time
	activeTimeout time.Duration
	maxValue      int64
}

// NewInMemoryStore creates an in-memory reference task provider and outbox.
func NewInMemoryStore(config *InMemoryStoreConfig) *InMemoryStore {
	s := &InMemoryStore{
		tasks:         make(map[string]*Task),
		active:        make(map[string]memoryActiveAttempt),
		taskEvents:    make(map[string][]TaskEvent),
		taskEventKeys: make(map[string]map[string]TaskEvent),
		notify:        make(chan struct{}),
		now:           time.Now,
		activeTimeout: 30 * time.Second,
		maxValue:      1 << 20,
	}
	if config != nil {
		if config.ActiveAttemptTimeout > 0 {
			s.activeTimeout = config.ActiveAttemptTimeout
		}
		if config.MaxValueBytes > 0 {
			s.maxValue = config.MaxValueBytes
		}
	}
	return s
}

func (s *InMemoryStore) signalLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

func (s *InMemoryStore) Create(_ context.Context, req *CreateTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: create request is required")
	}
	if err := validateCreateTaskRequest(req); err != nil {
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
		LeaseExpiryPolicy: req.LeaseExpiryPolicy,
		Status:            StatusPending,
		Version:           1,
		UpdatedAt:         now,
	}
	s.tasks[spec.ID] = task
	s.signalLocked()
	return cloneTask(task), nil
}

func (s *InMemoryStore) Get(_ context.Context, taskID string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, ErrNotFound
	}
	s.resolveExpiredLocked(t)
	return cloneTask(t), nil
}

func (s *InMemoryStore) ListPending(_ context.Context, req *ListPendingRequest) (*ListPendingResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: list pending request is required")
	}
	tasks, nextCursor, err := s.listByStatus(
		req.ExecutorKeys, req.Cursor, req.Limit, StatusPending, "pending",
	)
	if err != nil {
		return nil, err
	}
	return &ListPendingResult{Tasks: tasks, NextCursor: nextCursor}, nil
}

func (s *InMemoryStore) ListSuspended(
	_ context.Context,
	req *ListSuspendedRequest,
) (*ListSuspendedResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: list suspended request is required")
	}
	tasks, nextCursor, err := s.listByStatus(
		req.ExecutorKeys, req.Cursor, req.Limit, StatusSuspended, "suspended",
	)
	if err != nil {
		return nil, err
	}
	return &ListSuspendedResult{Tasks: tasks, NextCursor: nextCursor}, nil
}

func (s *InMemoryStore) listByStatus(
	keys []string,
	cursor string,
	limit int,
	status Status,
	name string,
) ([]*Task, string, error) {
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("backgroundtask: list %s requires executor keys", name)
	}
	executorKeys := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			return nil, "", fmt.Errorf(
				"backgroundtask: list %s executor key is required", name,
			)
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
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("backgroundtask: invalid %s-task cursor", name)
		}
		lastID := string(decoded)
		start = sort.Search(len(ids), func(i int) bool { return ids[i] > lastID })
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var tasks []*Task
	var nextCursor string
	for i := start; i < len(ids); i++ {
		t := s.tasks[ids[i]]
		s.resolveExpiredLocked(t)
		if t.Status != status {
			continue
		}
		if _, ok := executorKeys[t.Spec.ExecutorKey]; !ok {
			continue
		}
		tasks = append(tasks, cloneTask(t))
		if len(tasks) == limit {
			nextCursor = base64.RawURLEncoding.EncodeToString([]byte(ids[i]))
			break
		}
	}
	return tasks, nextCursor, nil
}

func (s *InMemoryStore) Start(_ context.Context, req *StartTaskRequest) (*Task, error) {
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

func (s *InMemoryStore) Heartbeat(_ context.Context, req *HeartbeatRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: heartbeat request is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeUncanceledTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	s.advanceLocked(t)
	s.active[t.Spec.ID] = memoryActiveAttempt{expiresAt: s.now().Add(s.activeTimeout)}
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *InMemoryStore) AppendTaskEvent(
	_ context.Context,
	req *AppendTaskEventRequest,
) (*AppendTaskEventResult, error) {
	if req == nil || req.TaskID == "" || req.Attempt <= 0 || req.EventID == "" {
		return nil, errors.New("backgroundtask: task event task id, attempt, and event id are required")
	}
	if len(req.Data) == 0 {
		return nil, errors.New("backgroundtask: task event data is required")
	}
	if int64(len(req.Data)) > s.maxValue {
		return nil, errors.New("backgroundtask: task event data exceeds configured limit")
	}
	if len(req.EventID) > 1024 {
		return nil, errors.New("backgroundtask: task event id exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorizeOutputLocked(req.TaskID, req.Attempt); err != nil {
		return nil, err
	}
	keyed := s.taskEventKeys[req.TaskID]
	if existing, ok := keyed[req.EventID]; ok {
		if !bytes.Equal(existing.Data, req.Data) {
			return nil, ErrTaskEventIDConflict
		}
		return &AppendTaskEventResult{
			Event: cloneTaskEvent(&existing),
		}, nil
	}
	events := s.taskEvents[req.TaskID]
	event := TaskEvent{
		EventID: req.EventID, TaskID: req.TaskID,
		Data: cloneBytes(req.Data), CreatedAt: s.now(),
	}
	s.taskEvents[req.TaskID] = append(events, event)
	if keyed == nil {
		keyed = make(map[string]TaskEvent)
		s.taskEventKeys[req.TaskID] = keyed
	}
	keyed[req.EventID] = event
	return &AppendTaskEventResult{
		Event: cloneTaskEvent(&event), Inserted: true,
	}, nil
}

func (s *InMemoryStore) ReadRecentTaskEvents(
	_ context.Context,
	req *ReadRecentTaskEventsRequest,
) (*ReadRecentTaskEventsResult, error) {
	if req == nil || req.TaskID == "" {
		return nil, errors.New("backgroundtask: recent task events task id is required")
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[req.TaskID]; !ok {
		return nil, ErrNotFound
	}
	events := s.taskEvents[req.TaskID]
	start := len(events) - limit
	if start < 0 {
		start = 0
	}
	result := &ReadRecentTaskEventsResult{}
	for i := start; i < len(events); i++ {
		result.Events = append(result.Events, cloneTaskEvent(&events[i]))
	}
	return result, nil
}

func (s *InMemoryStore) ReportTranscriptFailure(_ context.Context, req *ReportTranscriptFailureRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: report transcript failure request is required")
	}
	if err := validateTranscriptFailure(req.Error); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
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

func (s *InMemoryStore) Complete(_ context.Context, req *CompleteTaskRequest) (*Task, error) {
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
	t, err := s.activeUncanceledTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	t.Status = StatusCompleted
	t.ResultData = cloneBytes(req.Data)
	t.ResultError = ""
	s.finishStoreOwnedLocked(t)
	return cloneTask(t), nil
}

func (s *InMemoryStore) Fail(_ context.Context, req *FailTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: fail request is required")
	}
	if err := validateTaskSnapshot(StatusFailed, nil, req.Error); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeUncanceledTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	t.Status = StatusFailed
	t.ResultData = nil
	t.ResultError = req.Error
	s.finishStoreOwnedLocked(t)
	return cloneTask(t), nil
}

func (s *InMemoryStore) WaitInput(_ context.Context, req *WaitInputTaskRequest) (*Task, error) {
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
	t, err := s.activeUncanceledTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
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

func (s *InMemoryStore) Suspend(_ context.Context, req *SuspendTaskRequest) (*Task, error) {
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
	t, err := s.activeUncanceledTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
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

func (s *InMemoryStore) Yield(_ context.Context, req *YieldTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: yield request is required")
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("backgroundtask: checkpoint exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeUncanceledTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	t.Status = StatusPending
	if len(req.Checkpoint) > 0 {
		t.Checkpoint = cloneBytes(req.Checkpoint)
	}
	t.PendingResume = nil
	s.clearActiveLocked(t)
	s.advanceLocked(t)
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *InMemoryStore) AckCancel(_ context.Context, req *AckCancelRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: acknowledge cancellation request is required")
	}
	if len(req.Reason) > 4096 {
		return nil, errors.New("backgroundtask: cancellation reason exceeds 4096 bytes")
	}
	if err := validateTaskSnapshot(StatusCanceled, nil, req.Reason); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeTaskLocked(req.TaskID, req.ExpectedVersion, StatusRunning)
	if err != nil {
		return nil, err
	}
	reason := t.CancelReason
	if reason == "" {
		reason = req.Reason
	}
	if reason == "" {
		reason = defaultCanceledReason
	}
	t.Status = StatusCanceled
	t.ResultData = nil
	t.ResultError = reason
	t.PendingResume = nil
	s.finishStoreOwnedLocked(t)
	return cloneTask(t), nil
}

func (s *InMemoryStore) RequestCancel(_ context.Context, req *RequestCancelRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: cancel request is required")
	}
	if len(req.Reason) > 4096 {
		return nil, errors.New("backgroundtask: cancellation reason exceeds 4096 bytes")
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
	if t.CancelRequestedAt != nil {
		return cloneTask(t), nil
	}
	now := s.now()
	t.CancelRequestedAt = &now
	t.CancelReason = req.Reason
	if t.Status == StatusRunning {
		s.advanceLocked(t)
		if _, ok := s.active[t.Spec.ID]; ok {
			s.active[t.Spec.ID] = memoryActiveAttempt{expiresAt: now.Add(s.activeTimeout)}
		}
	} else if t.Status == StatusPending &&
		t.LeaseExpiryPolicy == LeaseExpiryRetry && t.Attempt > 0 {
		s.advanceLocked(t)
	} else {
		t.Status = StatusCanceled
		t.ResultError = t.CancelReason
		if t.ResultError == "" {
			t.ResultError = defaultCanceledReason
		}
		t.PendingResume = nil
		s.finishStoreOwnedLocked(t)
		return cloneTask(t), nil
	}
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *InMemoryStore) Resume(_ context.Context, req *ResumeRequest) (*Task, error) {
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

func (s *InMemoryStore) ReleaseSuspension(_ context.Context, req *ReleaseSuspensionRequest) (*Task, error) {
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

// WaitForTaskVersion blocks until the stored task has a Version greater than
// req.AfterVersion.
func (s *InMemoryStore) WaitForTaskVersion(ctx context.Context, req *WaitForTaskVersionRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: wait for task version request is required")
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

func (s *InMemoryStore) Receive(_ context.Context, req *ReceiveNotificationsRequest) (*ReceiveNotificationsResult, error) {
	if req == nil || req.LeaseDuration <= 0 {
		return nil, errors.New("backgroundtask: positive lease duration is required")
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	result := &ReceiveNotificationsResult{}
	for _, item := range s.outbox {
		if len(result.Deliveries) == limit {
			break
		}
		if item.leaseExpiresAt.After(now) {
			continue
		}
		s.outboxLeaseID++
		item.receipt = NotificationReceipt(fmt.Appendf(nil, "lease:%d", s.outboxLeaseID))
		item.leaseExpiresAt = now.Add(req.LeaseDuration)
		result.Deliveries = append(result.Deliveries, NotificationDelivery{
			Record: *cloneNotification(item.record), Receipt: append(NotificationReceipt(nil), item.receipt...),
		})
	}
	return result, nil
}

func (s *InMemoryStore) Ack(_ context.Context, receipt NotificationReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.outbox {
		if len(receipt) > 0 && bytes.Equal(item.receipt, receipt) {
			if !s.now().Before(item.leaseExpiresAt) {
				return ErrLeaseLost
			}
			s.outbox = append(s.outbox[:i], s.outbox[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (s *InMemoryStore) taskVersionLocked(id string, version int64) (*Task, error) {
	t, ok := s.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	if t.Version != version {
		return nil, ErrVersionConflict
	}
	return t, nil
}

func (s *InMemoryStore) activeTaskLocked(id string, version int64, allowed ...Status) (*Task, error) {
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

func (s *InMemoryStore) activeUncanceledTaskLocked(
	id string,
	version int64,
	allowed ...Status,
) (*Task, error) {
	t, err := s.activeTaskLocked(id, version, allowed...)
	if err != nil {
		return nil, err
	}
	if t.CancelRequestedAt != nil {
		return nil, ErrLeaseLost
	}
	return t, nil
}

func (s *InMemoryStore) authorizeOutputLocked(taskID string, attempt int64) error {
	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	s.resolveExpiredLocked(t)
	active, activeOK := s.active[taskID]
	if t.Status != StatusRunning || t.CancelRequestedAt != nil ||
		t.Attempt != attempt || !activeOK || !s.now().Before(active.expiresAt) {
		return ErrLeaseLost
	}
	return nil
}

func (s *InMemoryStore) advanceLocked(t *Task) {
	t.Version++
	t.UpdatedAt = s.now()
}

func (s *InMemoryStore) enqueueLocked(t *Task, kind NotificationKind) *Notification {
	if !t.Spec.NotifySession || kind == "" {
		return nil
	}
	n := &Notification{
		ID:     fmt.Sprintf("%s:%d:%s", t.Spec.ID, t.Version, kind),
		TaskID: t.Spec.ID, Version: t.Version,
		Kind: kind, CreatedAt: s.now(),
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

func (s *InMemoryStore) clearActiveLocked(t *Task) {
	delete(s.active, t.Spec.ID)
}

func (s *InMemoryStore) resolveExpiredLocked(t *Task) {
	if t.Status != StatusRunning {
		return
	}
	active, ok := s.active[t.Spec.ID]
	if ok && s.now().Before(active.expiresAt) {
		return
	}
	s.clearActiveLocked(t)
	if t.CancelRequestedAt != nil {
		if t.LeaseExpiryPolicy == LeaseExpiryRetry {
			t.Status = StatusPending
			t.PendingResume = nil
			s.advanceLocked(t)
			s.signalLocked()
			return
		}
		t.Status = StatusCanceled
		t.ResultError = t.CancelReason
		if t.ResultError == "" {
			t.ResultError = defaultCanceledReason
		}
		s.finishStoreOwnedLocked(t)
		return
	}
	if t.LeaseExpiryPolicy == LeaseExpiryRetry {
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

func (s *InMemoryStore) finishStoreOwnedLocked(t *Task) {
	s.clearActiveLocked(t)
	s.advanceLocked(t)
	now := s.now()
	t.DoneAt = &now
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
}

var (
	_ TaskStore      = (*InMemoryStore)(nil)
	_ TaskEventStore = (*InMemoryStore)(nil)
)
