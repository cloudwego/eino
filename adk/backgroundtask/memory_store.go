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
	Clock              Clock
	MinLeaseDuration   time.Duration
	MaxLeaseDuration   time.Duration
	MaxUpdatePayload   int64
	MaxReportedUpdates int
	ArtifactVerifiers  map[string]ArtifactVerifier
}

type memoryOutboxItem struct {
	record       *NotificationOutboxRecord
	receipt      NotificationReceipt
	visibleAfter time.Time
}

// MemoryStore is a deterministic reference implementation of Store and
// NotificationOutbox. It is a state-machine test double, not a durable backend.
type MemoryStore struct {
	mu                 sync.Mutex
	tasks              map[string]*Task
	updates            map[string][]*Update
	reportedUpdates    map[string]int
	outbox             []*memoryOutboxItem
	notify             chan struct{}
	now                Clock
	minLease           time.Duration
	maxLease           time.Duration
	maxValue           int64
	maxReportedUpdates int
	artifactStores     map[string]ArtifactVerifier
}

func NewMemoryStore(config *MemoryStoreConfig) *MemoryStore {
	s := &MemoryStore{
		tasks:              make(map[string]*Task),
		updates:            make(map[string][]*Update),
		reportedUpdates:    make(map[string]int),
		notify:             make(chan struct{}),
		now:                time.Now,
		minLease:           time.Millisecond,
		maxLease:           24 * time.Hour,
		maxValue:           1 << 20,
		maxReportedUpdates: 10_000,
		artifactStores:     make(map[string]ArtifactVerifier),
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
		if config.MaxUpdatePayload > 0 {
			s.maxValue = config.MaxUpdatePayload
		}
		if config.MaxReportedUpdates > 0 {
			s.maxReportedUpdates = config.MaxReportedUpdates
		}
		for key, verifier := range config.ArtifactVerifiers {
			if key != "" && verifier != nil {
				s.artifactStores[key] = verifier
			}
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
	s.appendStatusLocked(task, StatusPending)
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

func (s *MemoryStore) ListUpdates(_ context.Context, req *ListTaskUpdatesRequest) (*ListTaskUpdatesResult, error) {
	if req == nil || req.TaskID == "" {
		return nil, errors.New("backgroundtask: task id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[req.TaskID]
	if !ok {
		return nil, ErrNotFound
	}
	s.resolveExpiredLocked(task)
	return s.listUpdatesLocked(req), nil
}

func (s *MemoryStore) listUpdatesLocked(req *ListTaskUpdatesRequest) *ListTaskUpdatesResult {
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	result := &ListTaskUpdatesResult{NextSequence: req.AfterSequence}
	for _, u := range s.updates[req.TaskID] {
		if u.Sequence <= req.AfterSequence {
			continue
		}
		if len(result.Updates) == limit {
			break
		}
		result.Updates = append(result.Updates, cloneUpdate(u))
		result.NextSequence = u.Sequence
	}
	return result
}

func (s *MemoryStore) ListClaimable(_ context.Context, req *ListClaimableRequest) (*ListClaimableResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: list claimable request is required")
	}
	caps := make(map[string]struct{}, len(req.Capabilities))
	for _, c := range req.Capabilities {
		caps[c.ExecutorKey+"\x00"+c.SpecVersion] = struct{}{}
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
		_, exact := caps[t.Spec.ExecutorKey+"\x00"+t.Spec.SpecVersion]
		_, wildcard := caps[t.Spec.ExecutorKey+"\x00*"]
		if !exact && !wildcard {
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
	if req == nil || req.WorkerID == "" {
		return nil, errors.New("backgroundtask: claim request and worker id are required")
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
	if s.deadlineElapsedLocked(t) {
		s.failStoreOwnedLocked(t, "deadline_exceeded")
		return nil, ErrIllegalTransition
	}
	t.Status = StatusRunning
	t.Attempt++
	t.LeaseOwner = req.WorkerID
	t.LeaseGeneration++
	t.LeaseExpiresAt = s.now().Add(req.LeaseDuration)
	s.advanceLocked(t)
	u := s.appendStatusLocked(t, StatusRunning)
	s.enqueueLocked(t, u, eventForStatus(StatusRunning))
	s.signalLocked()
	return &ClaimTaskResult{
		Task: cloneTask(t),
		Lease: LeaseToken{
			TaskID: t.Spec.ID, ExpectedVersion: t.TransitionVersion,
			WorkerID: t.LeaseOwner, Generation: t.LeaseGeneration,
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
	if s.deadlineElapsedLocked(t) {
		s.failStoreOwnedLocked(t, "deadline_exceeded")
		return nil, ErrLeaseLost
	}
	t.LeaseExpiresAt = s.now().Add(req.LeaseDuration)
	s.advanceLocked(t)
	s.signalLocked()
	return cloneTask(t), nil
}

func (s *MemoryStore) AppendUpdate(ctx context.Context, req *AppendTaskUpdateRequest) (*AppendTaskUpdateResult, error) {
	if err := validateAppend(req); err != nil {
		return nil, err
	}
	if req.Payload != nil && req.Payload.Value.Size > s.maxValue && req.Payload.Value.Ref == nil {
		return nil, errors.New("backgroundtask: inline update exceeds configured limit")
	}
	if req.Payload != nil {
		if err := s.validateArtifact(ctx, req.Payload.Value); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.leasedTaskLocked(req.Lease, StatusRunning)
	if err != nil {
		return nil, err
	}
	if s.reportedUpdates[t.Spec.ID] >= s.maxReportedUpdates {
		return nil, errors.New("backgroundtask: reported update quota exceeded")
	}
	s.advanceLocked(t)
	u := s.appendUpdateLocked(t, req.Kind, nil, req.Progress, req.Payload)
	s.reportedUpdates[t.Spec.ID]++
	n := s.enqueueLocked(t, u, NotificationUpdateAvailable)
	s.signalLocked()
	return &AppendTaskUpdateResult{Task: cloneTask(t), Update: cloneUpdate(u), Notification: cloneNotification(n)}, nil
}

func (s *MemoryStore) Commit(ctx context.Context, req *CommitTaskRequest) (*CommitTaskResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: commit request is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.leasedTaskLocked(req.Lease, StatusRunning, StatusCanceling)
	if err != nil {
		return nil, err
	}
	if !legalWorkerTransition(t.Status, req.Mutation.ToStatus) {
		return nil, ErrIllegalTransition
	}
	if err := s.validateMutationLocked(ctx, t, req.Mutation); err != nil {
		return nil, err
	}
	t.Status = req.Mutation.ToStatus
	if req.Mutation.Checkpoint != nil {
		t.Checkpoint = cloneCheckpoint(req.Mutation.Checkpoint)
		if t.Checkpoint.CreatedAt.IsZero() {
			t.Checkpoint.CreatedAt = s.now()
		}
	}
	if req.Mutation.Result != nil {
		t.ResultRef = cloneResult(req.Mutation.Result)
		if t.ResultRef.CreatedAt.IsZero() {
			t.ResultRef.CreatedAt = s.now()
		}
	}
	t.TerminalReason = req.Mutation.TerminalReason
	if t.Status == StatusWaitingInput {
		t.ResumeData = nil
		t.ResumeEncoding = ""
	}
	s.advanceLocked(t)
	updates := []*Update{s.appendStatusLocked(t, t.Status)}
	if t.Status == StatusWaitingInput {
		updates = append(updates, s.appendUpdateLocked(t, UpdateInputRequired, nil, nil, req.Mutation.InputRequest))
	}
	if t.Status != StatusRunning && t.Status != StatusCanceling {
		s.clearLeaseLocked(t)
	}
	if terminalStatus(t.Status) {
		done := s.now()
		t.DoneAt = &done
	}
	n := s.enqueueLocked(t, updates[len(updates)-1], eventForStatus(t.Status))
	s.signalLocked()
	out := make([]*Update, len(updates))
	for i := range updates {
		out[i] = cloneUpdate(updates[i])
	}
	return &CommitTaskResult{Task: cloneTask(t), Updates: out, Notification: cloneNotification(n)}, nil
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
		t.TerminalReason = "canceled"
		s.clearLeaseLocked(t)
		s.advanceLocked(t)
		t.DoneAt = cloneTime(&now)
	}
	u := s.appendStatusLocked(t, t.Status)
	n := s.enqueueLocked(t, u, eventForStatus(t.Status))
	s.signalLocked()
	return &RequestCancelResult{Task: cloneTask(t), Update: cloneUpdate(u), Notification: cloneNotification(n)}, nil
}

func (s *MemoryStore) Resume(_ context.Context, req *ResumeTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: resume request is required")
	}
	if len(req.ResumeData) > 0 && req.ResumeEncoding == "" {
		return nil, errors.New("backgroundtask: resume encoding is required")
	}
	if int64(len(req.ResumeData)) > s.maxValue {
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
	if s.deadlineElapsedLocked(t) {
		s.failStoreOwnedLocked(t, "deadline_exceeded")
		return nil, ErrIllegalTransition
	}
	t.ResumeData = append([]byte(nil), req.ResumeData...)
	t.ResumeEncoding = req.ResumeEncoding
	t.Status = StatusPending
	s.advanceLocked(t)
	u := s.appendStatusLocked(t, t.Status)
	s.enqueueLocked(t, u, eventForStatus(t.Status))
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
	if s.deadlineElapsedLocked(t) {
		s.failStoreOwnedLocked(t, "deadline_exceeded")
		return nil, ErrIllegalTransition
	}
	t.Status = StatusPending
	s.advanceLocked(t)
	u := s.appendStatusLocked(t, t.Status)
	s.enqueueLocked(t, u, eventForStatus(t.Status))
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

func (s *MemoryStore) WaitUpdates(ctx context.Context, req *WaitTaskUpdatesRequest) (*ListTaskUpdatesResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: wait updates request is required")
	}
	for {
		s.mu.Lock()
		t, ok := s.tasks[req.TaskID]
		if !ok {
			s.mu.Unlock()
			return nil, ErrNotFound
		}
		s.resolveExpiredLocked(t)
		if t.LatestUpdateSequence > req.AfterSequence {
			out := s.listUpdatesLocked(&ListTaskUpdatesRequest{
				TaskID: req.TaskID, AfterSequence: req.AfterSequence, Limit: req.Limit,
			})
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
	if t.LeaseOwner != token.WorkerID || t.LeaseGeneration != token.Generation {
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

func (s *MemoryStore) appendStatusLocked(t *Task, status Status) *Update {
	return s.appendUpdateLocked(t, UpdateStatus, &status, nil, nil)
}

func (s *MemoryStore) appendUpdateLocked(t *Task, kind UpdateKind, status *Status, progress *Progress, payload *UpdatePayload) *Update {
	t.LatestUpdateSequence++
	u := &Update{
		UpdateID: fmt.Sprintf("%s:%d", t.Spec.ID, t.LatestUpdateSequence), TaskID: t.Spec.ID,
		Sequence: t.LatestUpdateSequence, Attempt: t.Attempt,
		LeaseGeneration: t.LeaseGeneration, Kind: kind, CreatedAt: s.now(),
	}
	if status != nil {
		value := *status
		u.Status = &value
	}
	u.Progress = cloneProgress(progress)
	u.Payload = clonePayload(payload)
	if progress != nil {
		t.LatestProgress = cloneProgress(progress)
	}
	s.updates[t.Spec.ID] = append(s.updates[t.Spec.ID], u)
	return u
}

func (s *MemoryStore) enqueueLocked(t *Task, u *Update, kind NotificationEventKind) *NotificationOutboxRecord {
	if t.Spec.Notify == nil {
		return nil
	}
	n := &NotificationOutboxRecord{
		NotificationID: fmt.Sprintf("%s:%d:%d:%s", t.Spec.ID, t.TransitionVersion, u.Sequence, kind),
		TaskID:         t.Spec.ID, TransitionVersion: t.TransitionVersion, UpdateSequence: u.Sequence,
		EventKind: kind, Status: t.Status, SessionID: t.Spec.SessionID,
		Target: *cloneSpec(t.Spec).Notify, Progress: cloneProgress(t.LatestProgress),
		Checkpoint: cloneCheckpoint(t.Checkpoint), Result: cloneResult(t.ResultRef),
		CreatedAt: s.now(),
	}
	if terminalStatus(t.Status) {
		n.Reason = t.TerminalReason
	}
	s.outbox = append(s.outbox, &memoryOutboxItem{record: n})
	return n
}

func (s *MemoryStore) validateMutationLocked(ctx context.Context, t *Task, m TaskMutation) error {
	if len(m.TerminalReason) > 4096 {
		return errors.New("backgroundtask: terminal reason exceeds configured bounds")
	}
	if (m.ToStatus == StatusFailed || m.ToStatus == StatusCanceled) && m.TerminalReason == "" {
		return errors.New("backgroundtask: failed and canceled transitions require a terminal reason")
	}
	if m.ToStatus != StatusFailed && m.ToStatus != StatusCanceled && m.TerminalReason != "" {
		return errors.New("backgroundtask: terminal reason is only valid for failed or canceled transitions")
	}
	if m.Checkpoint != nil {
		if m.ToStatus != StatusRunning && m.ToStatus != StatusWaitingInput && m.ToStatus != StatusSuspended {
			return errors.New("backgroundtask: checkpoint is invalid for this transition")
		}
		if m.Checkpoint.ExecutorKey != t.Spec.ExecutorKey || m.Checkpoint.Format == "" ||
			m.Checkpoint.Version == "" || m.Checkpoint.Sequence <= 0 ||
			(t.Checkpoint != nil && m.Checkpoint.Sequence <= t.Checkpoint.Sequence) {
			return ErrInvalidArtifact
		}
		if err := s.validateArtifact(ctx, m.Checkpoint.State); err != nil {
			return err
		}
		if m.Checkpoint.State.Ref == nil && m.Checkpoint.State.Size > s.maxValue {
			return errors.New("backgroundtask: inline checkpoint exceeds configured limit")
		}
	}
	if m.ToStatus == StatusRunning && m.Checkpoint == nil {
		return errors.New("backgroundtask: running transition requires a checkpoint")
	}
	if m.ToStatus == StatusWaitingInput {
		if m.Checkpoint == nil || m.InputRequest == nil || m.InputRequest.Value.Payload == nil {
			return errors.New("backgroundtask: waiting_input requires checkpoint and inline input request")
		}
		if m.InputRequest.Type == "" || m.InputRequest.Value.Size > s.maxValue {
			return errors.New("backgroundtask: invalid input request")
		}
		if err := s.validateArtifact(ctx, m.InputRequest.Value); err != nil {
			return err
		}
		if m.InputRequest.Value.Size > s.maxValue {
			return errors.New("backgroundtask: inline input request exceeds configured limit")
		}
	} else if m.InputRequest != nil {
		return errors.New("backgroundtask: input request is only valid for waiting_input")
	}
	if m.ToStatus == StatusCompleted {
		if m.Result == nil || m.Result.Format == "" {
			return errors.New("backgroundtask: completed requires a result")
		}
		if t.Spec.Result.ResultFormat != "" && m.Result.Format != t.Spec.Result.ResultFormat {
			return errors.New("backgroundtask: result format does not match task policy")
		}
		if err := s.validateArtifact(ctx, m.Result.Value); err != nil {
			return err
		}
		if m.Result.Value.Ref != nil && m.Result.Value.Ref.StoreKey != t.Spec.Result.ResultStoreKey {
			return errors.New("backgroundtask: result store does not match task policy")
		}
		if m.Result.Value.Ref == nil && m.Result.Value.Size > s.maxValue {
			return errors.New("backgroundtask: inline result exceeds configured limit")
		}
	} else if m.Result != nil {
		return errors.New("backgroundtask: result is only valid for completed")
	}
	if (m.ToStatus == StatusWaitingInput || m.ToStatus == StatusSuspended) && m.Checkpoint == nil {
		return errors.New("backgroundtask: resumable transition requires checkpoint")
	}
	return nil
}

func (s *MemoryStore) validateArtifact(ctx context.Context, value ArtifactValue) error {
	if err := validateArtifact(value); err != nil {
		return err
	}
	if value.Ref != nil {
		verifier, ok := s.artifactStores[value.Ref.StoreKey]
		if !ok {
			return fmt.Errorf("%w: unknown artifact store %q", ErrInvalidArtifact, value.Ref.StoreKey)
		}
		if err := verifier.Verify(ctx, value.Ref.Key, value.Digest, value.Size); err != nil {
			return fmt.Errorf("%w: verify external artifact: %v", ErrInvalidArtifact, err)
		}
	}
	return nil
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
		return NotificationUpdateAvailable
	}
}

func (s *MemoryStore) clearLeaseLocked(t *Task) {
	t.LeaseOwner = ""
	t.LeaseExpiresAt = time.Time{}
}

func (s *MemoryStore) deadlineElapsedLocked(t *Task) bool {
	return t.Spec.Deadline != nil && !s.now().Before(*t.Spec.Deadline)
}

func (s *MemoryStore) resolveExpiredLocked(t *Task) {
	if s.deadlineElapsedLocked(t) && !terminalStatus(t.Status) &&
		(t.Status != StatusRunning || !s.now().Before(t.LeaseExpiresAt)) {
		s.failStoreOwnedLocked(t, "deadline_exceeded")
		return
	}
	if t.Status != StatusRunning && t.Status != StatusCanceling {
		return
	}
	if t.LeaseExpiresAt.IsZero() || s.now().Before(t.LeaseExpiresAt) {
		return
	}
	if t.Status == StatusCanceling {
		t.Status = StatusCanceled
		t.TerminalReason = "canceled"
		s.finishStoreOwnedLocked(t)
		return
	}
	recoverable := t.Attempt < int64(t.Spec.Recovery.MaxAttempts) &&
		t.Spec.Recovery.OnLeaseExpired != RecoveryFail
	if t.Spec.Recovery.OnLeaseExpired == RecoveryResumeCheckpoint && t.Checkpoint == nil {
		recoverable = t.Spec.Recovery.OnMissingCheckpoint == RecoveryRestartFromSpec
	}
	if recoverable {
		if t.Spec.Recovery.OnLeaseExpired == RecoveryRestartFromSpec ||
			(t.Spec.Recovery.OnLeaseExpired == RecoveryResumeCheckpoint && t.Checkpoint == nil) {
			t.Checkpoint = nil
			t.ResumeData = nil
			t.ResumeEncoding = ""
		}
		t.Status = StatusPending
		s.clearLeaseLocked(t)
		s.advanceLocked(t)
		u := s.appendStatusLocked(t, t.Status)
		s.enqueueLocked(t, u, eventForStatus(t.Status))
		s.signalLocked()
		return
	}
	s.failStoreOwnedLocked(t, "lease_expired")
}

func (s *MemoryStore) failStoreOwnedLocked(t *Task, reason string) {
	t.Status = StatusFailed
	t.TerminalReason = reason
	s.finishStoreOwnedLocked(t)
}

func (s *MemoryStore) finishStoreOwnedLocked(t *Task) {
	s.clearLeaseLocked(t)
	s.advanceLocked(t)
	now := s.now()
	t.DoneAt = &now
	u := s.appendStatusLocked(t, t.Status)
	s.enqueueLocked(t, u, eventForStatus(t.Status))
	s.signalLocked()
}
