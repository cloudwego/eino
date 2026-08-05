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
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNotFound reports that a task or notification record does not exist.
	ErrNotFound = errors.New("backgroundtask: task not found")
	// ErrAlreadyExists reports that a task or registry entry already exists.
	ErrAlreadyExists = errors.New("backgroundtask: task already exists")
	// ErrVersionConflict reports that ExpectedVersion no longer matches the stored record.
	ErrVersionConflict = errors.New("backgroundtask: task version conflict")
	// ErrLeaseLost reports that an active attempt is no longer authorized.
	ErrLeaseLost = errors.New("backgroundtask: lease lost")
	// ErrIllegalTransition reports that a requested lifecycle transition is invalid.
	ErrIllegalTransition = errors.New("backgroundtask: illegal state transition")
	// ErrInvalidResult reports an invalid terminal result or lifecycle output.
	ErrInvalidResult = errors.New("backgroundtask: invalid result")
	// ErrAlreadyTerminal reports that a task has already reached a terminal status.
	ErrAlreadyTerminal = errors.New("backgroundtask: task is already terminal")
	// ErrCheckpointUnavailable reports that a planned drain reached no safe,
	// compatible checkpoint. Manager stops renewing the current lease so expiry
	// can redispatch from the last durable checkpoint.
	ErrCheckpointUnavailable = errors.New("backgroundtask: checkpoint unavailable")
	// ErrCloseDeadlineRequired reports that active tasks require a bounded Close context.
	ErrCloseDeadlineRequired = errors.New("backgroundtask: close deadline is required while tasks are active")
	// ErrUnsupportedPayloadVersion reports an executor payload version it cannot decode.
	ErrUnsupportedPayloadVersion = errors.New("backgroundtask: unsupported payload version")
	// ErrTaskEventConflict reports reuse of an event ID with different bytes.
	ErrTaskEventConflict = errors.New("backgroundtask: task event id conflict")
)

// Store persists task snapshots, append-ordered progress events, and semantic
// lifecycle transitions. AppendTaskEvent must fence writes by the active
// attempt before task-wide EventID replay detection, retain replay metadata
// across attempts for at least the task lifetime, and not advance lifecycle
// Version.
//
// RequestCancel on active work keeps StatusRunning, sets CancelRequestedAt and
// the first-write optional CancelReason, and advances Version. Once
// cancellation is requested, Heartbeat, Complete, Fail,
// WaitInput, Suspend, and Yield must reject the attempt; only Cancel may
// terminally acknowledge it. Yield changes running to pending, stores its
// optional checkpoint atomically, and emits no lifecycle notification. On
// retry-capable work, cancel intent that outlives an attempt remains pending so
// a recovery attempt can stop the external operation before acknowledging
// cancellation. Non-recoverable lease expiry resolves cancellation directly.
type Store interface {
	Create(context.Context, *CreateTaskRequest) (*Task, error)
	CreateAndStart(context.Context, *CreateTaskRequest) (*Task, error)
	Get(context.Context, string) (*Task, error)
	ListPending(context.Context, *ListPendingRequest) (*ListPendingResult, error)
	Start(context.Context, *StartTaskRequest) (*Task, error)
	Heartbeat(context.Context, *HeartbeatRequest) (*Task, error)
	AppendTaskEvent(context.Context, *AppendTaskEventRequest) (*AppendTaskEventResult, error)
	ReadRecentTaskEvents(context.Context, *ReadRecentTaskEventsRequest) (*ReadRecentTaskEventsResult, error)
	ReportOutputFailure(context.Context, *ReportOutputFailureRequest) (*Task, error)
	Complete(context.Context, *CompleteTaskRequest) (*Task, error)
	Fail(context.Context, *FailTaskRequest) (*Task, error)
	WaitInput(context.Context, *WaitInputTaskRequest) (*Task, error)
	Suspend(context.Context, *SuspendTaskRequest) (*Task, error)
	Yield(context.Context, *YieldTaskRequest) (*Task, error)
	Cancel(context.Context, *CancelTaskRequest) (*Task, error)
	RequestCancel(context.Context, *RequestCancelRequest) (*Task, error)
	Resume(context.Context, *ResumeRequest) (*Task, error)
	ReleaseSuspension(context.Context, *ReleaseSuspensionRequest) (*Task, error)
	Wait(context.Context, *WaitUpdateRequest) (*Task, error)
}

// NotificationOutbox leases lifecycle notifications for dispatch.
type NotificationOutbox interface {
	Receive(context.Context, *ReceiveNotificationsRequest) (*ReceiveNotificationsResult, error)
	Ack(context.Context, NotificationReceipt) error
}

func terminalStatus(status Status) bool {
	return status == StatusCompleted || status == StatusFailed || status == StatusCanceled
}

func validateSpec(spec Spec) error {
	if spec.ID == "" || spec.ExecutorKey == "" {
		return fmt.Errorf("backgroundtask: id and executor key are required")
	}
	if spec.Notify != nil {
		if spec.Notify.Kind == "" || spec.Notify.TargetID == "" {
			return fmt.Errorf("backgroundtask: notification kind and target id are required")
		}
		if len(spec.Notify.Kind) > 128 || len(spec.Notify.TargetID) > 512 || len(spec.Notify.Metadata) > 32 {
			return fmt.Errorf("backgroundtask: notification target exceeds bounds")
		}
		for key, value := range spec.Notify.Metadata {
			if !strings.Contains(key, "/") || len(key) > 256 || len(value) > 1024 {
				return fmt.Errorf("backgroundtask: notification metadata must use bounded namespaced keys")
			}
		}
		if spec.Notify.Kind == "session_inbox" && spec.Notify.TargetID != spec.SessionID {
			return fmt.Errorf("backgroundtask: session inbox target must match task session")
		}
	}
	return nil
}

func validateCreateTaskRequest(req *CreateTaskRequest) error {
	if err := validateSpec(req.Spec); err != nil {
		return err
	}
	if req.LeaseExpiryPolicy != LeaseExpiryRetry && req.LeaseExpiryPolicy != LeaseExpiryFail {
		return fmt.Errorf("backgroundtask: lease expiry policy must be %q or %q", LeaseExpiryRetry, LeaseExpiryFail)
	}
	return nil
}

func validateOutputFailure(message string) error {
	if message == "" {
		return errors.New("backgroundtask: output failure requires an error")
	}
	if len(message) > 4096 {
		return errors.New("backgroundtask: output failure exceeds configured bounds")
	}
	return nil
}

func validateTaskSnapshot(status Status, data []byte, resultError string) error {
	switch status {
	case StatusPending, StatusRunning, StatusWaitingInput, StatusSuspended:
		if len(data) != 0 || resultError != "" {
			return fmt.Errorf("%w: non-terminal task cannot have a result", ErrInvalidResult)
		}
	case StatusCompleted, StatusFailed, StatusCanceled:
	default:
		return fmt.Errorf("%w: unsupported status %q", ErrInvalidResult, status)
	}
	if len(resultError) > 4096 {
		return errors.New("backgroundtask: result error exceeds configured bounds")
	}
	switch status {
	case StatusCompleted:
		if resultError != "" {
			return fmt.Errorf("%w: completed result cannot carry an error", ErrInvalidResult)
		}
	case StatusFailed:
		if resultError == "" {
			return fmt.Errorf("%w: failed result requires an error", ErrInvalidResult)
		}
	case StatusCanceled:
		if len(data) != 0 {
			return fmt.Errorf("%w: canceled result cannot carry data", ErrInvalidResult)
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
	if v.Notify != nil {
		n := *v.Notify
		n.Metadata = make(map[string]string, len(v.Notify.Metadata))
		for k, value := range v.Notify.Metadata {
			n.Metadata[k] = value
		}
		c.Notify = &n
	}
	return c
}

func cloneNotification(v *Notification) *Notification {
	if v == nil {
		return nil
	}
	c := *v
	c.Target.Metadata = make(map[string]string, len(v.Target.Metadata))
	for k, value := range v.Target.Metadata {
		c.Target.Metadata[k] = value
	}
	c.Task = cloneTask(v.Task)
	return &c
}
