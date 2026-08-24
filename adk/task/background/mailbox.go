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

	"github.com/cloudwego/eino/adk/task"
)

type childCursor struct {
	Version      int    `json:"v"`
	ParentTaskID string `json:"p"`
	LastTaskID   string `json:"l"`
}

// AdoptForeground atomically transfers mailbox ownership and creates a
// background lifecycle record with the same task ID.
func (s *InMemoryStore) AdoptForeground(
	_ context.Context,
	req *AdoptForegroundStoreRequest,
) (*TaskSnapshot, error) {
	if req == nil || req.Spec.ID == "" || req.ExpectedGeneration <= 0 ||
		req.InputCursor < 0 {
		return nil, errors.New("task/background: foreground adoption request is invalid")
	}
	if err := validateCreateTaskRequest(&CreateTaskRequest{
		Spec: req.Spec, LeaseExpiryPolicy: req.LeaseExpiryPolicy,
		Checkpoint: req.InitialCheckpoint, ContextSnapshot: req.ContextSnapshot,
	}); err != nil {
		return nil, err
	}
	if int64(len(req.InitialCheckpoint)) > s.maxValue ||
		int64(len(req.ContextSnapshot)) > s.maxValue {
		return nil, errors.New("task/background: adoption data exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.tasks[req.Spec.ID]; existing != nil {
		mailbox := s.mailboxes[req.Spec.ID]
		if mailbox != nil && mailbox.mailbox.State == task.MailboxBackground &&
			bytes.Equal(existing.Checkpoint, req.InitialCheckpoint) &&
			equalSpec(existing.Spec, req.Spec) {
			return cloneTask(existing), nil
		}
		return nil, ErrAlreadyExists
	}
	current := s.mailboxes[req.Spec.ID]
	if current == nil {
		return nil, task.ErrMailboxNotFound
	}
	if current.mailbox.State == task.MailboxSealed {
		return nil, task.ErrMailboxSealed
	}
	if current.mailbox.State != task.MailboxForeground ||
		current.mailbox.Generation != req.ExpectedGeneration {
		return nil, task.ErrOwnershipLost
	}
	if current.mailbox.ConsumedCursor != req.InputCursor {
		return nil, task.ErrCursorConflict
	}
	if !req.StartPending && current.mailbox.LatestSequence > req.InputCursor {
		return nil, task.ErrInputsPending
	}
	now := s.now()
	status := StatusSuspended
	if req.StartPending {
		status = StatusPending
	}
	backgroundTask := &TaskSnapshot{
		Spec: cloneSpec(req.Spec), LeaseExpiryPolicy: req.LeaseExpiryPolicy,
		Status: status, Checkpoint: cloneBytes(req.InitialCheckpoint),
		ContextSnapshot: cloneBytes(req.ContextSnapshot),
		Version:         1, CreatedAt: now, UpdatedAt: now,
	}
	s.tasks[req.Spec.ID] = backgroundTask
	current.mailbox.State = task.MailboxBackground
	current.mailbox.Generation++
	s.enqueueLocked(backgroundTask, NotificationTaskBackgrounded)
	s.signalLocked()
	return cloneTask(backgroundTask), nil
}

// CommitInput atomically advances the mailbox cursor and checkpoints an
// operation established from that input.
func (s *InMemoryStore) CommitInput(
	_ context.Context,
	req *CommitInputRequest,
) (*TaskSnapshot, error) {
	if req == nil || req.TaskID == "" || req.Attempt <= 0 ||
		req.ExpectedCursor < 0 || req.InputCursor <= req.ExpectedCursor ||
		len(req.Checkpoint) == 0 {
		return nil, errors.New("task/background: commit input request is invalid")
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("task/background: checkpoint exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backgroundTask, mailbox, err := s.activeMailboxLocked(
		req.TaskID, req.ExpectedVersion, req.Attempt,
	)
	if err != nil {
		return nil, err
	}
	if mailbox.mailbox.ConsumedCursor != req.ExpectedCursor ||
		req.InputCursor > mailbox.mailbox.LatestSequence {
		return nil, task.ErrCursorConflict
	}
	mailbox.mailbox.ConsumedCursor = req.InputCursor
	backgroundTask.Checkpoint = cloneBytes(req.Checkpoint)
	s.advanceLocked(backgroundTask)
	s.signalLocked()
	return cloneTask(backgroundTask), nil
}

// WaitInputIfNoInputs atomically waits only when the consumed cursor catches
// the mailbox up.
func (s *InMemoryStore) WaitInputIfNoInputs(
	_ context.Context,
	req *WaitInputIfNoInputsRequest,
) (*TaskSnapshot, error) {
	if req == nil || req.TaskID == "" || req.Attempt <= 0 ||
		req.InputCursor < 0 || len(req.Checkpoint) == 0 {
		return nil, errors.New("task/background: wait-input request is invalid")
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("task/background: checkpoint exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backgroundTask, mailbox, err := s.activeMailboxLocked(
		req.TaskID, req.ExpectedVersion, req.Attempt,
	)
	if err != nil {
		return nil, err
	}
	if mailbox.mailbox.ConsumedCursor > req.InputCursor ||
		req.InputCursor > mailbox.mailbox.LatestSequence {
		return nil, task.ErrCursorConflict
	}
	mailbox.mailbox.ConsumedCursor = req.InputCursor
	backgroundTask.Checkpoint = cloneBytes(req.Checkpoint)
	s.clearActiveLocked(backgroundTask)
	if mailbox.mailbox.LatestSequence > req.InputCursor {
		backgroundTask.Status = StatusPending
		s.advanceLocked(backgroundTask)
		s.signalLocked()
		return cloneTask(backgroundTask), task.ErrInputsPending
	}
	backgroundTask.Status = StatusWaitingInput
	s.advanceLocked(backgroundTask)
	s.enqueueLocked(backgroundTask, eventForStatus(backgroundTask.Status))
	s.signalLocked()
	return cloneTask(backgroundTask), nil
}

func (s *InMemoryStore) activeMailboxLocked(
	taskID string,
	expectedVersion, attempt int64,
) (*TaskSnapshot, *memoryMailbox, error) {
	backgroundTask, err := s.activeUncanceledTaskLocked(
		taskID, expectedVersion, StatusRunning,
	)
	if err != nil {
		return nil, nil, err
	}
	if backgroundTask.Attempt != attempt {
		return nil, nil, ErrLeaseLost
	}
	mailbox := s.mailboxes[taskID]
	if mailbox == nil {
		return nil, nil, task.ErrMailboxNotFound
	}
	if mailbox.mailbox.State != task.MailboxBackground {
		return nil, nil, task.ErrOwnershipLost
	}
	return backgroundTask, mailbox, nil
}

// SuspendIfNoInputs atomically suspends an active attempt at an idle mailbox.
func (s *InMemoryStore) SuspendIfNoInputs(
	_ context.Context,
	req *SuspendIfNoInputsRequest,
) (*TaskSnapshot, error) {
	if req == nil || req.TaskID == "" || req.Attempt <= 0 ||
		req.InputCursor < 0 || len(req.Checkpoint) == 0 {
		return nil, errors.New("task/background: suspend-if-no-inputs request is invalid")
	}
	if int64(len(req.Checkpoint)) > s.maxValue {
		return nil, errors.New("task/background: checkpoint exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backgroundTask, err := s.activeUncanceledTaskLocked(
		req.TaskID,
		req.ExpectedVersion,
		StatusRunning,
	)
	if err != nil {
		return nil, err
	}
	if backgroundTask.Attempt != req.Attempt {
		return nil, ErrLeaseLost
	}
	mailbox := s.mailboxes[req.TaskID]
	if mailbox == nil {
		return nil, task.ErrMailboxNotFound
	}
	if mailbox.mailbox.State != task.MailboxBackground {
		return nil, task.ErrOwnershipLost
	}
	if mailbox.mailbox.ConsumedCursor != req.InputCursor {
		return nil, task.ErrCursorConflict
	}
	backgroundTask.Checkpoint = cloneBytes(req.Checkpoint)
	s.clearActiveLocked(backgroundTask)
	if mailbox.mailbox.LatestSequence > req.InputCursor {
		backgroundTask.Status = StatusPending
		s.advanceLocked(backgroundTask)
		s.signalLocked()
		return cloneTask(backgroundTask), task.ErrInputsPending
	}
	backgroundTask.Status = StatusSuspended
	s.advanceLocked(backgroundTask)
	s.signalLocked()
	return cloneTask(backgroundTask), nil
}

// CompleteIfNoInputs atomically completes an active attempt at an idle mailbox.
func (s *InMemoryStore) CompleteIfNoInputs(
	_ context.Context,
	req *CompleteIfNoInputsRequest,
) (*TaskSnapshot, error) {
	if req == nil || req.TaskID == "" || req.Attempt <= 0 || req.InputCursor < 0 {
		return nil, errors.New("task/background: complete-if-no-inputs request is invalid")
	}
	if err := validateTaskSnapshot(StatusCompleted, req.ResultData, ""); err != nil {
		return nil, err
	}
	if int64(len(req.ResultData)) > s.maxValue {
		return nil, errors.New("task/background: result data exceeds configured limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backgroundTask, err := s.activeUncanceledTaskLocked(
		req.TaskID,
		req.ExpectedVersion,
		StatusRunning,
	)
	if err != nil {
		return nil, err
	}
	if backgroundTask.Attempt != req.Attempt {
		return nil, ErrLeaseLost
	}
	mailbox := s.mailboxes[req.TaskID]
	if mailbox == nil {
		return nil, task.ErrMailboxNotFound
	}
	if mailbox.mailbox.State != task.MailboxBackground {
		return nil, task.ErrOwnershipLost
	}
	if mailbox.mailbox.ConsumedCursor != req.InputCursor {
		return nil, task.ErrCursorConflict
	}
	s.clearActiveLocked(backgroundTask)
	if mailbox.mailbox.LatestSequence > req.InputCursor {
		backgroundTask.Status = StatusPending
		s.advanceLocked(backgroundTask)
		s.signalLocked()
		return cloneTask(backgroundTask), task.ErrInputsPending
	}
	backgroundTask.Status = StatusCompleted
	backgroundTask.ResultData = cloneBytes(req.ResultData)
	backgroundTask.ResultError = ""
	mailbox.mailbox.State = task.MailboxSealed
	mailbox.mailbox.Generation++
	if mailbox.mailbox.ChildSessionID != "" {
		delete(s.activeSessionTasks, mailbox.mailbox.ChildSessionID)
	}
	s.advanceLocked(backgroundTask)
	now := s.now()
	backgroundTask.DoneAt = &now
	s.enqueueLocked(backgroundTask, eventForStatus(backgroundTask.Status))
	s.signalLocked()
	return cloneTask(backgroundTask), nil
}

// Register creates or replays one foreground mailbox.
func (s *InMemoryStore) Register(
	_ context.Context,
	req *task.RegisterMailboxRequest,
) (*task.RegisterMailboxResult, error) {
	if req == nil || req.CandidateTaskID == "" || req.InvocationID == "" {
		return nil, errors.New("task/background: mailbox task and invocation IDs are required")
	}
	if int64(len(req.Identity)) > s.maxValue {
		return nil, errors.New("task/background: mailbox identity exceeds configured limit")
	}
	if req.ParentExecution != nil && req.RootSessionID != "" {
		return nil, errors.New(
			"task/background: nested mailbox root session is derived from its parent",
		)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parentTaskID := ""
	rootSessionID := req.RootSessionID
	if req.ParentExecution != nil {
		parentTaskID = req.ParentExecution.TaskID
		parent := s.mailboxes[parentTaskID]
		if parent == nil {
			return nil, task.ErrMailboxNotFound
		}
		rootSessionID = parent.mailbox.RootSessionID
	}
	invocationKey := mailboxInvocationKey{
		parentTaskID: parentTaskID, rootSessionID: rootSessionID,
		invocationID: req.InvocationID,
	}
	if taskID := s.mailboxInvocations[invocationKey]; taskID != "" {
		current := s.mailboxes[taskID]
		if current == nil || !bytes.Equal(current.mailbox.Identity, req.Identity) ||
			current.mailbox.ParentTaskID != parentTaskID ||
			current.mailbox.RootSessionID != rootSessionID ||
			current.mailbox.ChildSessionID != req.ChildSessionID {
			return nil, task.ErrMailboxIdentityConflict
		}
		return &task.RegisterMailboxResult{Mailbox: cloneMailbox(current.mailbox)}, nil
	}
	if _, ok := s.mailboxes[req.CandidateTaskID]; ok {
		return nil, ErrAlreadyExists
	}
	if req.ChildSessionID != "" {
		if activeTaskID := s.activeSessionTasks[req.ChildSessionID]; activeTaskID != "" {
			active := s.mailboxes[activeTaskID]
			if active != nil && active.mailbox.State != task.MailboxSealed {
				return nil, task.ErrSessionBusy
			}
			delete(s.activeSessionTasks, req.ChildSessionID)
		}
	}
	if parentTaskID != "" {
		if err := s.authorizeParentLocked(parentTaskID, req.ParentExecution); err != nil {
			return nil, err
		}
	}
	mailbox := &task.Mailbox{
		TaskID: req.CandidateTaskID, InvocationID: req.InvocationID,
		Identity: cloneBytes(req.Identity), ParentTaskID: parentTaskID,
		RootSessionID: rootSessionID, ChildSessionID: req.ChildSessionID,
		State:      task.MailboxForeground,
		Generation: 1,
	}
	s.mailboxes[mailbox.TaskID] = &memoryMailbox{
		mailbox: mailbox, byID: make(map[string]*task.InputRecord),
	}
	s.mailboxInvocations[invocationKey] = mailbox.TaskID
	if mailbox.ChildSessionID != "" {
		s.activeSessionTasks[mailbox.ChildSessionID] = mailbox.TaskID
	}
	s.signalLocked()
	return &task.RegisterMailboxResult{
		Mailbox: cloneMailbox(mailbox), Created: true,
	}, nil
}

// GetActiveMailboxBySession resolves the current nonterminal finite task.
func (s *InMemoryStore) GetActiveMailboxBySession(
	_ context.Context,
	childSessionID string,
) (*task.Mailbox, error) {
	if childSessionID == "" {
		return nil, errors.New("task/background: child session ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	taskID := s.activeSessionTasks[childSessionID]
	current := s.mailboxes[taskID]
	if current == nil || current.mailbox.State == task.MailboxSealed {
		return nil, task.ErrMailboxNotFound
	}
	return cloneMailbox(current.mailbox), nil
}

func (s *InMemoryStore) authorizeParentLocked(
	parentTaskID string,
	execution *task.ExecutionContext,
) error {
	parent := s.mailboxes[parentTaskID]
	if parent == nil {
		return task.ErrMailboxNotFound
	}
	if parent.mailbox.State == task.MailboxSealed {
		return task.ErrMailboxSealed
	}
	if execution == nil || execution.TaskID != parentTaskID ||
		execution.Generation != parent.mailbox.Generation {
		return task.ErrOwnershipLost
	}
	switch parent.mailbox.State {
	case task.MailboxForeground:
		if execution.Owner != task.OwnerParent || execution.Attempt != 0 {
			return task.ErrOwnershipLost
		}
	case task.MailboxBackground:
		if execution.Owner != task.OwnerManager || execution.Attempt <= 0 {
			return task.ErrOwnershipLost
		}
		if err := s.authorizeOutputLocked(parentTaskID, execution.Attempt); err != nil {
			return err
		}
	default:
		return task.ErrOwnershipLost
	}
	return nil
}

// GetMailbox returns an independently owned mailbox snapshot.
func (s *InMemoryStore) GetMailbox(
	_ context.Context,
	taskID string,
) (*task.Mailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.mailboxes[taskID]
	if current == nil {
		return nil, task.ErrMailboxNotFound
	}
	return cloneMailbox(current.mailbox), nil
}

// SendInput appends one input idempotently and wakes a background owner.
func (s *InMemoryStore) SendInput(
	_ context.Context,
	req *task.SendInputRequest,
) (*task.SendInputResult, error) {
	if req == nil || req.TaskID == "" ||
		req.Input.EventID == "" || req.Input.Kind == "" {
		return nil, errors.New("task/background: input identity is required")
	}
	if len(req.Input.EventID) > 1024 || len(req.Input.Kind) > 128 {
		return nil, errors.New("task/background: input identity exceeds configured limit")
	}
	if int64(len(req.Input.Data)) > s.maxValue {
		return nil, errors.New("task/background: input data exceeds configured limit")
	}
	if req.Input.Delivery != task.InputQueued &&
		req.Input.Delivery != task.InputPreempt {
		return nil, errors.New("task/background: input delivery is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.mailboxes[req.TaskID]
	if current == nil {
		return nil, task.ErrMailboxNotFound
	}
	if existing := current.byID[req.Input.EventID]; existing != nil {
		if existing.Kind != req.Input.Kind ||
			!bytes.Equal(existing.Data, req.Input.Data) ||
			existing.Delivery != req.Input.Delivery {
			return nil, task.ErrInputConflict
		}
		return &task.SendInputResult{Input: cloneInput(existing)}, nil
	}
	if current.mailbox.State == task.MailboxSealed {
		return nil, task.ErrMailboxSealed
	}
	current.mailbox.LatestSequence++
	input := &task.InputRecord{
		TaskID: req.TaskID, Sequence: current.mailbox.LatestSequence,
		Input: task.Input{
			EventID: req.Input.EventID, Kind: req.Input.Kind,
			Data: cloneBytes(req.Input.Data), Delivery: req.Input.Delivery,
		},
		CreatedAt: s.now(),
	}
	current.inputs = append(current.inputs, input)
	current.byID[input.EventID] = input
	if current.mailbox.State == task.MailboxBackground {
		if backgroundTask := s.tasks[req.TaskID]; backgroundTask != nil {
			if backgroundTask.Status == StatusSuspended ||
				backgroundTask.Status == StatusWaitingInput {
				backgroundTask.Status = StatusPending
				s.advanceLocked(backgroundTask)
			}
		}
	}
	s.signalLocked()
	return &task.SendInputResult{
		Input: cloneInput(input), Inserted: true,
	}, nil
}

// ListInputs returns a contiguous FIFO page.
func (s *InMemoryStore) ListInputs(
	_ context.Context,
	req *task.ListInputsRequest,
) (*task.ListInputsResult, error) {
	if req == nil || req.TaskID == "" || req.AfterSequence < 0 {
		return nil, errors.New("task/background: list inputs request is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listInputsLocked(req)
}

func (s *InMemoryStore) listInputsLocked(
	req *task.ListInputsRequest,
) (*task.ListInputsResult, error) {
	current := s.mailboxes[req.TaskID]
	if current == nil {
		return nil, task.ErrMailboxNotFound
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
	}
	result := &task.ListInputsResult{
		LatestSequence: current.mailbox.LatestSequence,
		ConsumedCursor: current.mailbox.ConsumedCursor,
		MailboxState:   current.mailbox.State,
		Generation:     current.mailbox.Generation,
	}
	for _, input := range current.inputs {
		if input.Sequence <= req.AfterSequence {
			continue
		}
		result.Inputs = append(result.Inputs, cloneInput(input))
		if len(result.Inputs) == limit {
			break
		}
	}
	return result, nil
}

// WaitInputs blocks until input exists after AfterSequence or the mailbox seals.
func (s *InMemoryStore) WaitInputs(
	ctx context.Context,
	req *task.WaitInputsRequest,
) (*task.ListInputsResult, error) {
	if req == nil || req.TaskID == "" || req.AfterSequence < 0 {
		return nil, errors.New("task/background: wait inputs request is invalid")
	}
	for {
		s.mu.Lock()
		current := s.mailboxes[req.TaskID]
		if current == nil {
			s.mu.Unlock()
			return nil, task.ErrMailboxNotFound
		}
		if current.mailbox.LatestSequence > req.AfterSequence ||
			current.mailbox.State == task.MailboxSealed {
			result, err := s.listInputsLocked(&task.ListInputsRequest{
				TaskID: req.TaskID, AfterSequence: req.AfterSequence,
			})
			s.mu.Unlock()
			return result, err
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

// AdvanceCursor commits contiguous input consumption under owner fencing.
func (s *InMemoryStore) AdvanceCursor(
	_ context.Context,
	req *task.AdvanceCursorRequest,
) error {
	if req == nil || req.TaskID == "" || req.ExpectedCursor < 0 ||
		req.Cursor < req.ExpectedCursor {
		return errors.New("task/background: advance cursor request is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.mailboxes[req.TaskID]
	if current == nil {
		return task.ErrMailboxNotFound
	}
	if current.mailbox.Generation != req.ExpectedGeneration {
		return task.ErrOwnershipLost
	}
	if current.mailbox.ConsumedCursor != req.ExpectedCursor {
		return task.ErrCursorConflict
	}
	if req.Cursor > current.mailbox.LatestSequence {
		return ErrInvalidCursor
	}
	switch current.mailbox.State {
	case task.MailboxForeground:
		if req.Attempt != 0 {
			return task.ErrOwnershipLost
		}
	case task.MailboxBackground:
		if req.Attempt <= 0 {
			return task.ErrOwnershipLost
		}
		if err := s.authorizeOutputLocked(req.TaskID, req.Attempt); err != nil {
			return err
		}
	default:
		return task.ErrMailboxSealed
	}
	current.mailbox.ConsumedCursor = req.Cursor
	s.signalLocked()
	return nil
}

// SealIfIdle closes a foreground mailbox only when no input is pending.
func (s *InMemoryStore) SealIfIdle(
	_ context.Context,
	req *task.SealMailboxRequest,
) (*task.Mailbox, error) {
	if req == nil || req.TaskID == "" || req.ExpectedCursor < 0 {
		return nil, errors.New("task/background: seal mailbox request is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.mailboxes[req.TaskID]
	if current == nil {
		return nil, task.ErrMailboxNotFound
	}
	if current.mailbox.State != task.MailboxForeground ||
		current.mailbox.Generation != req.ExpectedGeneration {
		return nil, task.ErrOwnershipLost
	}
	if current.mailbox.ConsumedCursor != req.ExpectedCursor {
		return nil, task.ErrCursorConflict
	}
	if current.mailbox.LatestSequence > req.ExpectedCursor {
		return cloneMailbox(current.mailbox), task.ErrInputsPending
	}
	current.mailbox.State = task.MailboxSealed
	current.mailbox.Generation++
	if current.mailbox.ChildSessionID != "" {
		delete(s.activeSessionTasks, current.mailbox.ChildSessionID)
	}
	s.signalLocked()
	return cloneMailbox(current.mailbox), nil
}

// Abandon seals a failed or canceled foreground mailbox and discards its
// unconsumed input.
func (s *InMemoryStore) Abandon(
	_ context.Context,
	req *task.AbandonMailboxRequest,
) (*task.Mailbox, error) {
	if req == nil || req.TaskID == "" || req.ExpectedGeneration <= 0 {
		return nil, errors.New("task/background: abandon mailbox request is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.mailboxes[req.TaskID]
	if current == nil {
		return nil, task.ErrMailboxNotFound
	}
	if current.mailbox.State == task.MailboxSealed {
		return cloneMailbox(current.mailbox), nil
	}
	if current.mailbox.State != task.MailboxForeground ||
		current.mailbox.Generation != req.ExpectedGeneration {
		return nil, task.ErrOwnershipLost
	}
	current.mailbox.ConsumedCursor = current.mailbox.LatestSequence
	current.mailbox.State = task.MailboxSealed
	current.mailbox.Generation++
	if current.mailbox.ChildSessionID != "" {
		delete(s.activeSessionTasks, current.mailbox.ChildSessionID)
	}
	s.signalLocked()
	return cloneMailbox(current.mailbox), nil
}

// ListChildren returns direct child mailboxes in stable task-ID order.
func (s *InMemoryStore) ListChildren(
	_ context.Context,
	req *task.ListChildrenRequest,
) (*task.ListChildrenResult, error) {
	if req == nil || req.ParentTaskID == "" {
		return nil, errors.New("task/background: parent task ID is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	startAfter := ""
	if req.Cursor != "" {
		decoded, err := decodeChildCursor(req.Cursor)
		if err != nil || decoded.ParentTaskID != req.ParentTaskID {
			return nil, ErrInvalidCursor
		}
		startAfter = decoded.LastTaskID
	}
	ids := make([]string, 0)
	for id, mailbox := range s.mailboxes {
		if mailbox.mailbox.ParentTaskID == req.ParentTaskID && id > startAfter {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	result := &task.ListChildrenResult{}
	if len(ids) > limit {
		next, err := encodeChildCursor(childCursor{
			Version: 1, ParentTaskID: req.ParentTaskID, LastTaskID: ids[limit-1],
		})
		if err != nil {
			return nil, err
		}
		result.NextCursor = next
		ids = ids[:limit]
	}
	for _, id := range ids {
		result.Mailboxes = append(result.Mailboxes, cloneMailbox(s.mailboxes[id].mailbox))
	}
	return result, nil
}

func encodeChildCursor(cursor childCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeChildCursor(value string) (childCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return childCursor{}, err
	}
	var cursor childCursor
	if err = json.Unmarshal(data, &cursor); err != nil || cursor.Version != 1 ||
		cursor.ParentTaskID == "" || cursor.LastTaskID == "" {
		return childCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

func cloneMailbox(mailbox *task.Mailbox) *task.Mailbox {
	if mailbox == nil {
		return nil
	}
	copy := *mailbox
	copy.Identity = cloneBytes(mailbox.Identity)
	return &copy
}

func cloneInput(input *task.InputRecord) *task.InputRecord {
	if input == nil {
		return nil
	}
	copy := *input
	copy.Data = cloneBytes(input.Data)
	return &copy
}

func equalSpec(left, right Spec) bool {
	return left.ID == right.ID &&
		left.ExecutorKey == right.ExecutorKey &&
		left.Kind == right.Kind &&
		bytes.Equal(left.Payload, right.Payload) &&
		left.Description == right.Description &&
		left.OutputFile == right.OutputFile &&
		left.ParentTaskID == right.ParentTaskID &&
		left.RootSessionID == right.RootSessionID &&
		left.NotifySession == right.NotifySession
}

var _ task.MailboxStore = (*InMemoryStore)(nil)

func validateMailboxOwnership(mailbox *task.Mailbox) error {
	if mailbox == nil || mailbox.TaskID == "" || mailbox.Generation <= 0 {
		return fmt.Errorf("task/background: invalid mailbox")
	}
	return nil
}
