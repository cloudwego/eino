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

package background

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	taskcore "github.com/cloudwego/eino/adk/task"
)

// InMemoryStoreConfig configures the in-memory reference task provider.
type InMemoryStoreConfig struct {
	// ActiveAttemptTimeout defaults to 30 seconds.
	ActiveAttemptTimeout time.Duration
	// MaxValueBytes defaults to 1 MiB and bounds each checkpoint, successful
	// result, resume input, and task-event data value.
	MaxValueBytes int64
}

type memoryOutboxItem struct {
	record         *Notification
	receipt        NotificationReceipt
	leaseExpiresAt time.Time
}

type memoryActiveAttempt struct {
	expiresAt time.Time
}

type memoryMailbox struct {
	mailbox *taskcore.Mailbox
	inputs  []*taskcore.InputRecord
	byID    map[string]*taskcore.InputRecord
}

type mailboxInvocationKey struct {
	parentTaskID  string
	rootSessionID string
	invocationID  string
}

const (
	defaultTaskEventPageSize = 100
	maxTaskEventPageSize     = 1000
)

type taskEventCursor struct {
	Version     int    `json:"v"`
	TaskID      string `json:"t"`
	SnapshotEnd int    `json:"s"`
	Position    int    `json:"p"`
	NewestFirst bool   `json:"n"`
}

type taskListCursor struct {
	Version      int      `json:"v"`
	Status       Status   `json:"s"`
	ExecutorKeys []string `json:"e"`
	LastID       string   `json:"l"`
}

// InMemoryStore is a deterministic reference implementation of LifecycleStore,
// TaskEventStore, NotificationWriter, and NotificationOutbox. It is a
// state-machine test double, not a durable backend.
type InMemoryStore struct {
	mu                  sync.Mutex
	tasks               map[string]*TaskSnapshot
	active              map[string]memoryActiveAttempt
	taskEvents          map[string][]TaskEvent
	taskEventKeys       map[string]map[string]TaskEvent
	customNotifications map[string]map[string]Notification
	outbox              []*memoryOutboxItem
	outboxLeaseID       uint64
	mailboxes           map[string]*memoryMailbox
	mailboxInvocations  map[mailboxInvocationKey]string
	activeSessionTasks  map[string]string
	notify              chan struct{}
	now                 func() time.Time
	activeTimeout       time.Duration
	maxValue            int64
}

// NewInMemoryStore creates an in-memory reference task provider and outbox.
func NewInMemoryStore(config *InMemoryStoreConfig) *InMemoryStore {
	s := &InMemoryStore{
		tasks:               make(map[string]*TaskSnapshot),
		active:              make(map[string]memoryActiveAttempt),
		taskEvents:          make(map[string][]TaskEvent),
		taskEventKeys:       make(map[string]map[string]TaskEvent),
		customNotifications: make(map[string]map[string]Notification),
		mailboxes:           make(map[string]*memoryMailbox),
		mailboxInvocations:  make(map[mailboxInvocationKey]string),
		activeSessionTasks:  make(map[string]string),
		notify:              make(chan struct{}),
		now:                 time.Now,
		activeTimeout:       30 * time.Second,
		maxValue:            1 << 20,
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

// Create inserts one pending task and returns an independent snapshot.
func (s *InMemoryStore) Create(_ context.Context, req *CreateTaskRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: create request is required")
	}
	if err := validateCreateTaskRequest(req); err != nil {
		return nil, err
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("task/background: checkpoint exceeds configured bounds")
	}
	if int64(len(req.ContextSnapshot)) > s.maxValue {
		return nil, errors.New("task/background: context snapshot exceeds configured bounds")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[req.Spec.ID]; ok {
		return nil, ErrAlreadyExists
	}
	if _, ok := s.mailboxes[req.Spec.ID]; ok {
		return nil, ErrAlreadyExists
	}
	if req.Spec.ParentTaskID != "" {
		if err := s.authorizeParentLocked(
			req.Spec.ParentTaskID,
			req.ParentExecution,
		); err != nil {
			return nil, err
		}
	}
	now := s.now()
	spec := cloneSpec(req.Spec)
	task := &TaskSnapshot{
		Spec:              spec,
		LeaseExpiryPolicy: req.LeaseExpiryPolicy,
		Status:            StatusPending,
		Checkpoint:        cloneBytes(req.Checkpoint),
		ContextSnapshot:   cloneBytes(req.ContextSnapshot),
		Version:           1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	s.tasks[spec.ID] = task
	mailbox := &taskcore.Mailbox{
		TaskID: spec.ID, InvocationID: "background:" + spec.ID,
		Identity: cloneBytes(spec.Payload), ParentTaskID: spec.ParentTaskID,
		RootSessionID: spec.RootSessionID, State: taskcore.MailboxBackground,
		Generation: 1,
	}
	s.mailboxes[spec.ID] = &memoryMailbox{
		mailbox: mailbox, byID: make(map[string]*taskcore.InputRecord),
	}
	s.mailboxInvocations[mailboxInvocationKey{
		rootSessionID: mailbox.RootSessionID,
		invocationID:  mailbox.InvocationID,
	}] = spec.ID
	s.enqueueLocked(task, NotificationTaskCreated)
	s.signalLocked()
	return cloneTask(task), nil
}

// Get returns an independent authoritative snapshot and resolves expired leases.
func (s *InMemoryStore) Get(_ context.Context, taskID string) (*TaskSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, ErrNotFound
	}
	s.resolveExpiredLocked(t)
	return cloneTask(t), nil
}

// ListPending returns task-ID-ordered pending snapshots.
func (s *InMemoryStore) ListPending(_ context.Context, req *ListPendingRequest) (*ListPendingResult, error) {
	if req == nil {
		return nil, errors.New("task/background: list pending request is required")
	}
	tasks, nextCursor, err := s.listByStatus(
		req.ExecutorKeys, req.Cursor, req.Limit, StatusPending, "pending",
	)
	if err != nil {
		return nil, err
	}
	return &ListPendingResult{Tasks: tasks, NextCursor: nextCursor}, nil
}

// ListSuspended returns task-ID-ordered suspended snapshots.
func (s *InMemoryStore) ListSuspended(
	_ context.Context,
	req *ListSuspendedRequest,
) (*ListSuspendedResult, error) {
	if req == nil {
		return nil, errors.New("task/background: list suspended request is required")
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
) ([]*TaskSnapshot, string, error) {
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("task/background: list %s requires executor keys", name)
	}
	executorKeys := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			return nil, "", fmt.Errorf(
				"task/background: list %s executor key is required", name,
			)
		}
		executorKeys[key] = struct{}{}
	}
	normalizedKeys := make([]string, 0, len(executorKeys))
	for key := range executorKeys {
		normalizedKeys = append(normalizedKeys, key)
	}
	sort.Strings(normalizedKeys)
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	start := 0
	if cursor != "" {
		decoded, err := decodeTaskListCursor(cursor)
		if err != nil || decoded.Status != status ||
			!equalStrings(decoded.ExecutorKeys, normalizedKeys) {
			return nil, "", fmt.Errorf("%w: %s-task cursor", ErrInvalidCursor, name)
		}
		if _, ok := s.tasks[decoded.LastID]; !ok {
			return nil, "", fmt.Errorf("%w: %s-task cursor", ErrInvalidCursor, name)
		}
		start = sort.Search(len(ids), func(i int) bool { return ids[i] > decoded.LastID })
	}
	if limit <= 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
	}
	tasks := make([]*TaskSnapshot, 0, limit+1)
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
		if len(tasks) > limit {
			lastID := tasks[limit-1].Spec.ID
			encoded, encodeErr := encodeTaskListCursor(taskListCursor{
				Version: 1, Status: status,
				ExecutorKeys: normalizedKeys, LastID: lastID,
			})
			if encodeErr != nil {
				return nil, "", fmt.Errorf(
					"task/background: encode %s-task cursor: %w", name, encodeErr,
				)
			}
			nextCursor = encoded
			tasks = tasks[:limit]
			break
		}
	}
	return tasks, nextCursor, nil
}

func encodeTaskListCursor(cursor taskListCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeTaskListCursor(value string) (taskListCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return taskListCursor{}, err
	}
	var cursor taskListCursor
	if err = json.Unmarshal(data, &cursor); err != nil || cursor.Version != 1 ||
		cursor.Status == "" || len(cursor.ExecutorKeys) == 0 || cursor.LastID == "" {
		return taskListCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Start claims a pending task and creates a fenced active attempt.
func (s *InMemoryStore) Start(_ context.Context, req *StartTaskRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: start request is required")
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

// Heartbeat renews the current active attempt lease.
func (s *InMemoryStore) Heartbeat(_ context.Context, req *HeartbeatRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: heartbeat request is required")
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

// CommitStart records the external-start boundary for the running attempt.
func (s *InMemoryStore) CommitStart(
	_ context.Context,
	req *CommitStartRequest,
) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: commit start request is required")
	}
	if len(req.Checkpoint) == 0 {
		return nil, errors.New("task/background: checkpoint data is required")
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("task/background: checkpoint exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.activeUncanceledTaskLocked(
		req.TaskID,
		req.ExpectedVersion,
		StatusRunning,
	)
	if err != nil {
		return nil, err
	}
	if len(t.Checkpoint) != 0 {
		return nil, ErrIllegalTransition
	}
	t.Checkpoint = cloneBytes(req.Checkpoint)
	s.advanceLocked(t)
	s.signalLocked()
	return cloneTask(t), nil
}

// AppendTaskEvent fences by attempt before task-wide EventID deduplication.
func (s *InMemoryStore) AppendTaskEvent(
	_ context.Context,
	req *AppendTaskEventRequest,
) (*AppendTaskEventResult, error) {
	if req == nil || req.TaskID == "" || req.Attempt <= 0 || req.EventID == "" {
		return nil, errors.New("task/background: task event task id, attempt, and event id are required")
	}
	if len(req.Data) == 0 {
		return nil, errors.New("task/background: task event data is required")
	}
	if int64(len(req.Data)) > s.maxValue {
		return nil, errors.New("task/background: task event data exceeds configured limit")
	}
	if len(req.EventID) > 1024 {
		return nil, errors.New("task/background: task event id exceeds configured limit")
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

// EnqueueTaskNotification fences by attempt before task-wide EventID replay
// detection and atomically records replay metadata with its outbox item.
func (s *InMemoryStore) EnqueueTaskNotification(
	_ context.Context,
	taskID string,
	attempt int64,
	req *NotifyParentRequest,
) error {
	if taskID == "" || attempt <= 0 {
		return errors.New(
			"task/background: notification task id and attempt are required",
		)
	}
	if err := validateNotifyParentRequest(req); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorizeNotificationLocked(taskID, attempt); err != nil {
		return err
	}
	task := s.tasks[taskID]
	if task.Spec.ParentTaskID == "" && task.Spec.RootSessionID == "" {
		return ErrNotificationUnavailable
	}
	keyed := s.customNotifications[taskID]
	if existing, ok := keyed[req.EventID]; ok {
		if existing.Kind != req.Kind || !bytes.Equal(existing.Data, req.Data) ||
			existing.Delivery != req.Delivery {
			return ErrNotificationEventIDConflict
		}
		return nil
	}
	record := Notification{
		ID:     customNotificationID(taskID, req.EventID),
		TaskID: taskID, SessionID: task.Spec.RootSessionID,
		Version: task.Version, Kind: req.Kind,
		Data: cloneBytes(req.Data), Delivery: req.Delivery, CreatedAt: s.now(),
	}
	if task.Spec.ParentTaskID != "" {
		_, err := s.enqueueParentInputLocked(
			task,
			customNotificationID(taskID, req.EventID),
			string(req.Kind),
			req.Data,
			req.Delivery,
		)
		if err != nil {
			return err
		}
	} else {
		s.outbox = append(s.outbox, &memoryOutboxItem{
			record: cloneNotification(&record),
		})
	}
	if keyed == nil {
		keyed = make(map[string]Notification)
		s.customNotifications[taskID] = keyed
	}
	keyed[req.EventID] = *cloneNotification(&record)
	return nil
}

func customNotificationID(taskID string, eventID string) string {
	return "eino.custom:" +
		base64.RawURLEncoding.EncodeToString([]byte(taskID)) + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(eventID)) +
		":application-event"
}

// ListTaskEvents returns one snapshot-stable append-order page.
func (s *InMemoryStore) ListTaskEvents(
	_ context.Context,
	req *ListTaskEventsRequest,
) (*ListTaskEventsResult, error) {
	if req == nil || req.TaskID == "" {
		return nil, errors.New("task/background: list task events task id is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultTaskEventPageSize
	} else if limit > maxTaskEventPageSize {
		limit = maxTaskEventPageSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[req.TaskID]; !ok {
		return nil, ErrNotFound
	}
	events := s.taskEvents[req.TaskID]
	cursor := taskEventCursor{
		Version: 1, TaskID: req.TaskID, SnapshotEnd: len(events),
		NewestFirst: req.NewestFirst,
	}
	if req.NewestFirst {
		cursor.Position = cursor.SnapshotEnd
	}
	if req.Cursor != "" {
		decoded, err := decodeTaskEventCursor(req.Cursor)
		if err != nil || decoded.TaskID != req.TaskID ||
			decoded.NewestFirst != req.NewestFirst ||
			decoded.SnapshotEnd < 0 || decoded.SnapshotEnd > len(events) ||
			decoded.Position < 0 || decoded.Position > decoded.SnapshotEnd {
			return nil, ErrInvalidCursor
		}
		cursor = decoded
	}

	result := &ListTaskEventsResult{}
	if cursor.NewestFirst {
		start := cursor.Position - limit
		if start < 0 {
			start = 0
		}
		for i := cursor.Position - 1; i >= start; i-- {
			result.Events = append(result.Events, cloneTaskEvent(&events[i]))
		}
		if start > 0 {
			cursor.Position = start
			nextCursor, err := encodeTaskEventCursor(cursor)
			if err != nil {
				return nil, fmt.Errorf("task/background: encode task event cursor: %w", err)
			}
			result.NextCursor = nextCursor
		}
		return result, nil
	}

	end := cursor.Position + limit
	if end > cursor.SnapshotEnd {
		end = cursor.SnapshotEnd
	}
	for i := cursor.Position; i < end; i++ {
		result.Events = append(result.Events, cloneTaskEvent(&events[i]))
	}
	if end < cursor.SnapshotEnd {
		cursor.Position = end
		nextCursor, err := encodeTaskEventCursor(cursor)
		if err != nil {
			return nil, fmt.Errorf("task/background: encode task event cursor: %w", err)
		}
		result.NextCursor = nextCursor
	}
	return result, nil
}

func encodeTaskEventCursor(cursor taskEventCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeTaskEventCursor(value string) (taskEventCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return taskEventCursor{}, err
	}
	var cursor taskEventCursor
	if err = json.Unmarshal(data, &cursor); err != nil || cursor.Version != 1 {
		return taskEventCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

// ReportTranscriptFailure records the first derived-transcript error.
func (s *InMemoryStore) ReportTranscriptFailure(_ context.Context, req *ReportTranscriptFailureRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: report transcript failure request is required")
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

// Fail commits terminal failure.
func (s *InMemoryStore) Fail(_ context.Context, req *FailTaskRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: fail request is required")
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

// Yield relinquishes an attempt and returns the task to pending.
func (s *InMemoryStore) Yield(_ context.Context, req *YieldTaskRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: yield request is required")
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("task/background: checkpoint exceeds configured limit")
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
	s.clearActiveLocked(t)
	s.advanceLocked(t)
	s.signalLocked()
	return cloneTask(t), nil
}

// AckCancel commits terminal acknowledgement of durable cancellation intent.
func (s *InMemoryStore) AckCancel(_ context.Context, req *AckCancelRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: acknowledge cancellation request is required")
	}
	if len(req.Reason) > 4096 {
		return nil, errors.New("task/background: cancellation reason exceeds 4096 bytes")
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
	s.finishStoreOwnedLocked(t)
	return cloneTask(t), nil
}

// RequestCancel records first-write cancellation intent.
func (s *InMemoryStore) RequestCancel(_ context.Context, req *RequestCancelRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: cancel request is required")
	}
	if len(req.Reason) > 4096 {
		return nil, errors.New("task/background: cancellation reason exceeds 4096 bytes")
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
		s.finishStoreOwnedLocked(t)
		return cloneTask(t), nil
	}
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return cloneTask(t), nil
}

// ReleaseSuspension returns a suspended task to pending.
func (s *InMemoryStore) ReleaseSuspension(_ context.Context, req *ReleaseSuspensionRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: release request is required")
	}
	if int64(len(req.ContextSnapshot)) > s.maxValue {
		return nil, errors.New("task/background: context snapshot exceeds configured bounds")
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
	if req.ContextSnapshot != nil {
		t.ContextSnapshot = cloneBytes(req.ContextSnapshot)
	}
	t.Status = StatusPending
	s.advanceLocked(t)
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
	return cloneTask(t), nil
}

// WaitForTaskVersion blocks until the stored task has a Version greater than
// req.AfterVersion.
// WaitForTaskVersion waits until TaskSnapshot.Version exceeds AfterVersion.
func (s *InMemoryStore) WaitForTaskVersion(ctx context.Context, req *WaitForTaskVersionRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: wait for task version request is required")
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

// Receive leases visible notifications with fresh opaque receipts.
func (s *InMemoryStore) Receive(_ context.Context, req *ReceiveNotificationsRequest) (*ReceiveNotificationsResult, error) {
	if req == nil || req.LeaseDuration <= 0 {
		return nil, errors.New("task/background: positive lease duration is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
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
		item.receipt = NotificationReceipt([]byte(fmt.Sprintf("lease:%d", s.outboxLeaseID)))
		item.leaseExpiresAt = now.Add(req.LeaseDuration)
		result.Deliveries = append(result.Deliveries, NotificationDelivery{
			Record: *cloneNotification(item.record), Receipt: append(NotificationReceipt(nil), item.receipt...),
		})
	}
	return result, nil
}

// Ack removes the notification authorized by a current unexpired receipt.
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

func (s *InMemoryStore) taskVersionLocked(id string, version int64) (*TaskSnapshot, error) {
	t, ok := s.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	if t.Version != version {
		return nil, ErrVersionConflict
	}
	return t, nil
}

func (s *InMemoryStore) activeTaskLocked(id string, version int64, allowed ...Status) (*TaskSnapshot, error) {
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
) (*TaskSnapshot, error) {
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

func (s *InMemoryStore) authorizeNotificationLocked(
	taskID string,
	attempt int64,
) error {
	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	active, activeOK := s.active[taskID]
	if t.Status != StatusRunning || t.CancelRequestedAt != nil ||
		t.Attempt != attempt || !activeOK || !s.now().Before(active.expiresAt) {
		return ErrLeaseLost
	}
	return nil
}

func (s *InMemoryStore) advanceLocked(t *TaskSnapshot) {
	t.Version++
	t.UpdatedAt = s.now()
}

func (s *InMemoryStore) enqueueLocked(t *TaskSnapshot, kind NotificationKind) *Notification {
	if kind == "" ||
		(kind != NotificationTaskCreated &&
			kind != NotificationTaskBackgrounded &&
			!t.Spec.NotifySession) {
		return nil
	}
	if t.Spec.ParentTaskID != "" {
		_, err := s.enqueueParentInputLocked(
			t,
			fmt.Sprintf("%s:%d:%s", t.Spec.ID, t.Version, kind),
			string(kind),
			[]byte(t.Spec.ID),
			taskcore.InputQueued,
		)
		if err != nil {
			t.ParentNotificationError = err.Error()
		}
		return nil
	}
	if t.Spec.RootSessionID == "" {
		return nil
	}
	n := &Notification{
		ID:        fmt.Sprintf("%s:%d:%s", t.Spec.ID, t.Version, kind),
		TaskID:    t.Spec.ID,
		SessionID: t.Spec.RootSessionID,
		Version:   t.Version,
		Kind:      kind, CreatedAt: s.now(),
	}
	s.outbox = append(s.outbox, &memoryOutboxItem{record: n})
	return n
}

func (s *InMemoryStore) enqueueParentInputLocked(
	child *TaskSnapshot,
	eventID string,
	kind string,
	data []byte,
	delivery taskcore.InputDelivery,
) (*taskcore.InputRecord, error) {
	parent := s.mailboxes[child.Spec.ParentTaskID]
	if parent == nil {
		return nil, taskcore.ErrMailboxNotFound
	}
	if parent.mailbox.State == taskcore.MailboxSealed {
		return nil, taskcore.ErrMailboxSealed
	}
	if existing := parent.byID[eventID]; existing != nil {
		if existing.Kind != kind || !bytes.Equal(existing.Data, data) ||
			existing.Delivery != delivery {
			return nil, taskcore.ErrInputConflict
		}
		return existing, nil
	}
	parent.mailbox.LatestSequence++
	input := &taskcore.InputRecord{
		TaskID: child.Spec.ParentTaskID, Sequence: parent.mailbox.LatestSequence,
		Input: taskcore.Input{
			EventID: eventID, Kind: kind, Data: cloneBytes(data),
			Delivery: delivery,
		},
		CreatedAt: s.now(),
	}
	parent.inputs = append(parent.inputs, input)
	parent.byID[eventID] = input
	if parent.mailbox.State == taskcore.MailboxBackground {
		if parentTask := s.tasks[child.Spec.ParentTaskID]; parentTask != nil {
			if parentTask.Status == StatusSuspended ||
				parentTask.Status == StatusWaitingInput {
				parentTask.Status = StatusPending
				s.advanceLocked(parentTask)
			}
		}
	}
	s.signalLocked()
	return input, nil
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

func (s *InMemoryStore) clearActiveLocked(t *TaskSnapshot) {
	delete(s.active, t.Spec.ID)
}

func (s *InMemoryStore) resolveExpiredLocked(t *TaskSnapshot) {
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
		s.advanceLocked(t)
		s.signalLocked()
		return
	}
	t.Status = StatusFailed
	t.ResultError = "execution lease expired and retry is disabled"
	s.finishStoreOwnedLocked(t)
}

func (s *InMemoryStore) finishStoreOwnedLocked(t *TaskSnapshot) {
	s.clearActiveLocked(t)
	if mailbox := s.mailboxes[t.Spec.ID]; mailbox != nil &&
		mailbox.mailbox.State == taskcore.MailboxBackground {
		mailbox.mailbox.State = taskcore.MailboxSealed
		mailbox.mailbox.Generation++
		if mailbox.mailbox.ChildSessionID != "" {
			delete(s.activeSessionTasks, mailbox.mailbox.ChildSessionID)
		}
	}
	s.advanceLocked(t)
	now := s.now()
	t.DoneAt = &now
	s.enqueueLocked(t, eventForStatus(t.Status))
	s.signalLocked()
}

var (
	_ TaskStore          = (*InMemoryStore)(nil)
	_ LifecycleStore     = (*InMemoryStore)(nil)
	_ TaskEventStore     = (*InMemoryStore)(nil)
	_ NotificationWriter = (*InMemoryStore)(nil)
	_ NotificationOutbox = (*InMemoryStore)(nil)
)
