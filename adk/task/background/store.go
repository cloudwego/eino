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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk/task"
)

var (
	// ErrNotFound reports that a task or notification record does not exist.
	ErrNotFound = errors.New("task/background: task not found")
	// ErrAlreadyExists reports that a task or registry entry already exists.
	ErrAlreadyExists = errors.New("task/background: task already exists")
	// ErrVersionConflict reports that ExpectedVersion no longer matches the stored record.
	ErrVersionConflict = errors.New("task/background: task version conflict")
	// ErrLeaseLost reports that an operation is no longer authorized by its lease.
	ErrLeaseLost = errors.New("task/background: lease lost")
	// ErrIllegalTransition reports that a requested lifecycle transition is invalid.
	ErrIllegalTransition = errors.New("task/background: illegal state transition")
	// ErrInvalidExecutionResult reports that an executor result or TaskStore
	// transition payload violates lifecycle result invariants.
	ErrInvalidExecutionResult = errors.New("task/background: invalid execution result")
	// ErrAlreadyTerminal reports that a task has already reached a terminal status.
	ErrAlreadyTerminal = errors.New("task/background: task is already terminal")
	// ErrDrainCheckpointUnavailable reports that a planned drain could not
	// produce or locate a safe compatible checkpoint. Manager stops renewing the
	// current lease so expiry can redispatch from the last durable checkpoint.
	ErrDrainCheckpointUnavailable = errors.New("task/background: drain checkpoint unavailable")
	// ErrCloseDeadlineRequired reports that Manager.Close was called with active
	// attempts but its context has no deadline. The Manager remains open.
	ErrCloseDeadlineRequired = errors.New("task/background: close deadline is required while tasks are active")
	// ErrUnsupportedExecutorPayloadVersion reports that the selected executor
	// cannot decode the version of the persisted Spec.Payload envelope.
	ErrUnsupportedExecutorPayloadVersion = errors.New("task/background: unsupported executor payload version")
	// ErrTaskEventIDConflict reports that one task-local EventID was replayed
	// with bytes different from the originally persisted event.
	ErrTaskEventIDConflict = errors.New("task/background: task event id conflict")
	// ErrInvalidCursor reports that a pagination cursor is malformed or cannot
	// continue the requested task-event snapshot and ordering.
	ErrInvalidCursor = errors.New("task/background: invalid cursor")
	// ErrNotificationUnavailable reports that the current context or task store
	// cannot route an application notification to a parent session.
	ErrNotificationUnavailable = errors.New(
		"task/background: parent notification unavailable",
	)
	// ErrNotificationEventIDConflict reports that a task-local notification
	// EventID was replayed with different Kind or Data.
	ErrNotificationEventIDConflict = errors.New(
		"task/background: notification event id conflict",
	)
	// ErrTaskCreatedEventUndelivered reports that Submit durably created the
	// task but failed to send the immediate parent-session TaskCreated event.
	// The durable notification outbox remains authoritative for recovery, so
	// callers must treat ownership as transferred when this wraps the error.
	ErrTaskCreatedEventUndelivered = errors.New(
		"task/background: immediate task-created event was not delivered",
	)
)

// SubmitRequest describes one task submission. InitialCheckpoint is
// copied into TaskSnapshot.Checkpoint in the same atomic create operation as
// the task record and TaskCreated outbox entry. It is opaque to Manager.
type SubmitRequest struct {
	Spec              Spec
	InitialCheckpoint []byte
}

// AdoptForegroundRequest atomically transfers one foreground mailbox to a
// task with the same ID.
type AdoptForegroundRequest struct {
	Spec               Spec
	ExpectedGeneration int64
	InputCursor        int64
	InitialCheckpoint  []byte
	StartPending       bool
}

// AdoptForegroundStoreRequest contains manager-owned creation fields.
type AdoptForegroundStoreRequest struct {
	AdoptForegroundRequest
	LeaseExpiryPolicy LeaseExpiryPolicy
	ContextSnapshot   []byte
}

// SuspendIfNoInputsRequest atomically suspends an active attempt only when its
// mailbox cursor is caught up.
type SuspendIfNoInputsRequest struct {
	TaskID          string
	ExpectedVersion int64
	Attempt         int64
	InputCursor     int64
	Checkpoint      []byte
}

// WaitInputIfNoInputsRequest atomically returns an active attempt to
// waiting-input after consuming a mailbox prefix.
type WaitInputIfNoInputsRequest struct {
	TaskID          string
	ExpectedVersion int64
	Attempt         int64
	InputCursor     int64
	Checkpoint      []byte
}

// CompleteIfNoInputsRequest atomically completes an active attempt only when
// its mailbox cursor is caught up.
type CompleteIfNoInputsRequest struct {
	TaskID          string
	ExpectedVersion int64
	Attempt         int64
	InputCursor     int64
	ResultData      []byte
}

type taskCreatedEventUndeliveredError struct {
	taskID string
	cause  error
}

func (e *taskCreatedEventUndeliveredError) Error() string {
	return fmt.Sprintf(
		"%s: send task-created session event for %q: %v",
		ErrTaskCreatedEventUndelivered,
		e.taskID,
		e.cause,
	)
}

func (e *taskCreatedEventUndeliveredError) Unwrap() error {
	return e.cause
}

func (e *taskCreatedEventUndeliveredError) Is(target error) bool {
	return target == ErrTaskCreatedEventUndelivered
}

// TaskStore persists authoritative task snapshots and semantic lifecycle
// transitions.
//
// Every returned Task and mutable field is independently owned by the caller.
// ListPending and ListSuspended follow their request ordering, cursor, and limit
// contracts; malformed cursors return ErrInvalidCursor.
// When the provider also implements NotificationOutbox, Create atomically
// enqueues NotificationTaskCreated for every task with a parent SessionID.
//
// RequestCancel on active work keeps StatusRunning, sets CancelRequestedAt and
// the first-write optional CancelReason, and advances Version. Once
// cancellation is requested, Heartbeat, Fail, Yield, and all mailbox-aware
// lifecycle transitions must reject the attempt; only AckCancel may terminally
// acknowledge it. CommitStart records the successful external-start boundary
// while retaining StatusRunning, requires a non-empty initial checkpoint
// envelope, advances Version, and must reject a second start commit while that
// checkpoint remains present. Yield changes running to pending, stores its
// optional checkpoint atomically and emits no lifecycle notification. On
// retry-capable work, cancel intent that outlives an attempt remains pending so
// a recovery attempt can stop the external operation before acknowledging
// cancellation. Non-recoverable lease expiry resolves cancellation directly.
type TaskStore interface {
	Create(context.Context, *CreateTaskRequest) (*TaskSnapshot, error)
	Get(context.Context, string) (*TaskSnapshot, error)
	ListPending(context.Context, *ListPendingRequest) (*ListPendingResult, error)
	ListSuspended(context.Context, *ListSuspendedRequest) (*ListSuspendedResult, error)
	Start(context.Context, *StartTaskRequest) (*TaskSnapshot, error)
	Heartbeat(context.Context, *HeartbeatRequest) (*TaskSnapshot, error)
	CommitStart(context.Context, *CommitStartRequest) (*TaskSnapshot, error)
	ReportTranscriptFailure(context.Context, *ReportTranscriptFailureRequest) (*TaskSnapshot, error)
	Fail(context.Context, *FailTaskRequest) (*TaskSnapshot, error)
	Yield(context.Context, *YieldTaskRequest) (*TaskSnapshot, error)
	AckCancel(context.Context, *AckCancelRequest) (*TaskSnapshot, error)
	RequestCancel(context.Context, *RequestCancelRequest) (*TaskSnapshot, error)
	ReleaseSuspension(context.Context, *ReleaseSuspensionRequest) (*TaskSnapshot, error)
	WaitForTaskVersion(context.Context, *WaitForTaskVersionRequest) (*TaskSnapshot, error)
}

// LifecycleStore is the atomic provider contract for background ownership and
// task mailbox transitions.
type LifecycleStore interface {
	TaskStore
	task.MailboxStore
	NotificationWriter
	AdoptForeground(context.Context, *AdoptForegroundStoreRequest) (*TaskSnapshot, error)
	CommitInput(context.Context, *CommitInputRequest) (*TaskSnapshot, error)
	WaitInputIfNoInputs(context.Context, *WaitInputIfNoInputsRequest) (*TaskSnapshot, error)
	SuspendIfNoInputs(context.Context, *SuspendIfNoInputsRequest) (*TaskSnapshot, error)
	CompleteIfNoInputs(context.Context, *CompleteIfNoInputsRequest) (*TaskSnapshot, error)
}

// TaskEventStore persists append-ordered task progress independently from
// lifecycle snapshots. AppendTaskEvent must fence writes by the active attempt
// before task-wide EventID replay detection, retain replay metadata across
// attempts for at least the task lifetime, and not advance TaskSnapshot.Version.
// ListTaskEvents must keep each cursor on the snapshot captured by its first
// page and order events by append position, reversed when NewestFirst is true.
// Event data and result pages are independently owned. Successful events and
// cursor positions remain readable for at least the lifetime of their task.
type TaskEventStore interface {
	AppendTaskEvent(context.Context, *AppendTaskEventRequest) (*AppendTaskEventResult, error)
	ListTaskEvents(context.Context, *ListTaskEventsRequest) (*ListTaskEventsResult, error)
}

// NotificationWriter atomically authorizes and enqueues application
// notifications from the exact active task attempt. Implementations derive the
// immutable parent SessionID from the stored Spec, fence attempt, lease, and
// cancellation before replay lookup, and retain replay metadata for at least
// the task lifetime. Notification.Version captures the current Task version
// without advancing it. Implementations copy request Data before retaining it.
type NotificationWriter interface {
	EnqueueTaskNotification(
		ctx context.Context,
		taskID string,
		attempt int64,
		req *NotifyParentRequest,
	) error
}

// NotificationOutbox leases task notifications for dispatch. The
// NotificationTaskCreated record is the durable recovery source for reconciling
// a TaskCreated parent-session event if the creating process exits before
// Runner persists its immediate timeline emission. Ack must
// accept only the opaque receipt for the notification's current unexpired
// lease; an expired or superseded receipt must not acknowledge the notification.
// Receive normalizes limits to default 100 and maximum 1000. Both sides copy
// receipt bytes, and successful notification records remain until acknowledged.
type NotificationOutbox interface {
	Receive(context.Context, *ReceiveNotificationsRequest) (*ReceiveNotificationsResult, error)
	Ack(context.Context, NotificationReceipt) error
}

func terminalStatus(status Status) bool {
	return status == StatusCompleted || status == StatusFailed || status == StatusCanceled
}

func validateSpec(spec Spec) error {
	if spec.ID == "" || spec.ExecutorKey == "" {
		return fmt.Errorf("task/background: id and executor key are required")
	}
	if spec.NotifySession && spec.SessionID == "" {
		return fmt.Errorf("task/background: notification session id is required")
	}
	return nil
}

func validateCreateTaskRequest(req *CreateTaskRequest) error {
	if err := validateSpec(req.Spec); err != nil {
		return err
	}
	if req.LeaseExpiryPolicy != LeaseExpiryRetry && req.LeaseExpiryPolicy != LeaseExpiryFail {
		return fmt.Errorf("task/background: lease expiry policy must be %q or %q", LeaseExpiryRetry, LeaseExpiryFail)
	}
	return nil
}

func validateTranscriptFailure(message string) error {
	if message == "" {
		return errors.New("task/background: transcript failure requires an error")
	}
	if len(message) > 4096 {
		return errors.New("task/background: transcript failure exceeds configured bounds")
	}
	return nil
}

func validateNotifyParentRequest(req *NotifyParentRequest) error {
	if req == nil {
		return errors.New("task/background: parent notification request is required")
	}
	if req.EventID == "" {
		return errors.New("task/background: notification event id is required")
	}
	if len(req.EventID) > 1024 {
		return errors.New("task/background: notification event id exceeds configured bounds")
	}
	if req.Kind == "" {
		return errors.New("task/background: notification kind is required")
	}
	if len(req.Kind) > 64 {
		return errors.New("task/background: notification kind exceeds configured bounds")
	}
	if strings.HasPrefix(string(req.Kind), "eino.") || lifecycleNotificationKind(req.Kind) {
		return errors.New("task/background: notification kind is reserved")
	}
	if len(req.Data) > 256<<10 {
		return errors.New("task/background: notification data exceeds configured bounds")
	}
	if req.Delivery != task.InputQueued && req.Delivery != task.InputPreempt {
		return errors.New("task/background: notification delivery is invalid")
	}
	return nil
}

func lifecycleNotificationKind(kind NotificationKind) bool {
	switch kind {
	case NotificationTaskCreated,
		NotificationTaskBackgrounded,
		NotificationWaitingInput,
		NotificationCompleted,
		NotificationFailed,
		NotificationCanceled:
		return true
	default:
		return false
	}
}

func validateTaskSnapshot(status Status, data []byte, resultError string) error {
	switch status {
	case StatusPending, StatusRunning, StatusWaitingInput, StatusSuspended:
		if len(data) != 0 || resultError != "" {
			return fmt.Errorf("%w: non-terminal task cannot have a result", ErrInvalidExecutionResult)
		}
	case StatusCompleted, StatusFailed, StatusCanceled:
	default:
		return fmt.Errorf("%w: unsupported status %q", ErrInvalidExecutionResult, status)
	}
	if len(resultError) > 4096 {
		return errors.New("task/background: result error exceeds configured bounds")
	}
	switch status {
	case StatusCompleted:
		if resultError != "" {
			return fmt.Errorf("%w: completed result cannot carry an error", ErrInvalidExecutionResult)
		}
	case StatusFailed:
		if resultError == "" {
			return fmt.Errorf("%w: failed result requires an error", ErrInvalidExecutionResult)
		}
	case StatusCanceled:
		if len(data) != 0 {
			return fmt.Errorf("%w: canceled result cannot carry data", ErrInvalidExecutionResult)
		}
	}
	return nil
}

func cloneTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneBytes(v []byte) []byte {
	if v == nil {
		return nil
	}
	c := make([]byte, len(v))
	copy(c, v)
	return c
}

func cloneTaskEvent(v *TaskEvent) *TaskEvent {
	if v == nil {
		return nil
	}
	c := *v
	c.Data = cloneBytes(v.Data)
	return &c
}

func cloneSpec(v Spec) Spec {
	c := v
	c.Payload = cloneBytes(v.Payload)
	return c
}

func cloneNotification(v *Notification) *Notification {
	if v == nil {
		return nil
	}
	c := *v
	c.Data = cloneBytes(v.Data)
	return &c
}
