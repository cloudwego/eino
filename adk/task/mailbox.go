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

package task

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrMailboxStoreRequired reports missing durable mailbox support.
	ErrMailboxStoreRequired = errors.New("task: mailbox store is required")
	// ErrMailboxNotFound reports an unknown logical task ID.
	ErrMailboxNotFound = errors.New("task: mailbox not found")
	// ErrMailboxIdentityConflict reports incompatible invocation replay.
	ErrMailboxIdentityConflict = errors.New("task: mailbox identity conflict")
	// ErrMailboxSealed reports input addressed to a finished task execution.
	ErrMailboxSealed = errors.New("task: mailbox is sealed")
	// ErrInputRequired reports a nil task input.
	ErrInputRequired = errors.New("task: input is required")
	// ErrInputConflict reports EventID reuse with different content.
	ErrInputConflict = errors.New("task: input event conflict")
	// ErrInputsPending reports an idle transition racing with new input.
	ErrInputsPending = errors.New("task: inputs pending")
	// ErrCursorConflict reports a stale mailbox cursor.
	ErrCursorConflict = errors.New("task: mailbox cursor conflict")
	// ErrOwnershipLost reports use of an obsolete owner generation.
	ErrOwnershipLost = errors.New("task: mailbox ownership lost")
	// ErrSessionBusy reports a second active task for one persistent child session.
	ErrSessionBusy = errors.New("task: child session already has an active task")
)

// MailboxState identifies the owner that may consume mailbox input.
type MailboxState string

const (
	MailboxForeground MailboxState = "foreground"
	MailboxBackground MailboxState = "background"
	MailboxSealed     MailboxState = "sealed"
)

// Mailbox contains communication state only, never task lifecycle results.
type Mailbox struct {
	TaskID         string
	InvocationID   string
	Identity       []byte
	ParentTaskID   string
	RootSessionID  string
	ChildSessionID string
	State          MailboxState
	Generation     int64
	LatestSequence int64
	ConsumedCursor int64
}

// InputDelivery is a durable delivery intent interpreted by the active owner.
type InputDelivery uint8

const (
	// InputQueued delivers input at the next turn boundary.
	InputQueued InputDelivery = iota
	// InputPreempt requests delivery at the earliest runtime-approved safe point.
	InputPreempt
)

// Input is one immutable, idempotent mailbox item. Sending any input wakes a
// background task that is waiting or suspended.
type Input struct {
	TaskID    string
	Sequence  int64
	EventID   string
	Kind      string
	Data      []byte
	Delivery  InputDelivery
	CreatedAt time.Time
}

// RegisterMailboxRequest creates or replays a logical task mailbox.
type RegisterMailboxRequest struct {
	CandidateTaskID string
	InvocationID    string
	Identity        []byte
	ParentTaskID    string
	RootSessionID   string
	ChildSessionID  string
	ParentExecution *ExecutionContext
}

// RegisterMailboxResult returns the canonical logical task mailbox.
type RegisterMailboxResult struct {
	Mailbox *Mailbox
	Created bool
}

// SendInputRequest appends one input idempotently.
type SendInputRequest struct {
	TaskID   string
	EventID  string
	Kind     string
	Data     []byte
	Delivery InputDelivery
}

// SendInputResult describes one durable append.
type SendInputResult struct {
	Input    *Input
	Inserted bool
}

// ListInputsRequest reads inputs strictly after AfterSequence.
type ListInputsRequest struct {
	TaskID        string
	AfterSequence int64
	Limit         int
}

// ListInputsResult is one FIFO page plus authoritative mailbox cursors.
type ListInputsResult struct {
	Inputs          []*Input
	LatestSequence  int64
	ConsumedCursor  int64
	MailboxState    MailboxState
	OwnerGeneration int64
}

// WaitInputsRequest waits until input exists after AfterSequence.
type WaitInputsRequest struct {
	TaskID        string
	AfterSequence int64
}

// AdvanceCursorRequest advances consumption under owner fencing.
type AdvanceCursorRequest struct {
	TaskID             string
	ExpectedCursor     int64
	Cursor             int64
	ExpectedGeneration int64
	Attempt            int64
}

// SealMailboxRequest seals a foreground mailbox only when it is caught up.
type SealMailboxRequest struct {
	TaskID             string
	ExpectedCursor     int64
	ExpectedGeneration int64
}

// AbandonMailboxRequest seals a foreground mailbox and discards pending input
// after its parent-owned execution has failed or been canceled.
type AbandonMailboxRequest struct {
	TaskID             string
	ExpectedGeneration int64
}

// ListChildrenRequest lists direct child mailboxes in stable task-ID order.
type ListChildrenRequest struct {
	ParentTaskID string
	Cursor       string
	Limit        int
}

// ListChildrenResult contains one child mailbox page.
type ListChildrenResult struct {
	Mailboxes  []*Mailbox
	NextCursor string
}

// InputSender appends durable task input.
type InputSender interface {
	SendInput(context.Context, *SendInputRequest) (*SendInputResult, error)
}

// MailboxStore is the durable communication plane shared by foreground and
// background owners.
type MailboxStore interface {
	InputSender
	Register(context.Context, *RegisterMailboxRequest) (*RegisterMailboxResult, error)
	GetMailbox(context.Context, string) (*Mailbox, error)
	GetActiveMailboxBySession(context.Context, string) (*Mailbox, error)
	ListInputs(context.Context, *ListInputsRequest) (*ListInputsResult, error)
	WaitInputs(context.Context, *WaitInputsRequest) (*ListInputsResult, error)
	AdvanceCursor(context.Context, *AdvanceCursorRequest) error
	SealIfIdle(context.Context, *SealMailboxRequest) (*Mailbox, error)
	Abandon(context.Context, *AbandonMailboxRequest) (*Mailbox, error)
	ListChildren(context.Context, *ListChildrenRequest) (*ListChildrenResult, error)
}
