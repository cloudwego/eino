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

import "time"

// LeaseExpiryPolicy controls how TaskStore resolves an expired active attempt.
type LeaseExpiryPolicy string

const (
	// LeaseExpiryRetry returns the task to pending for another durable attempt.
	LeaseExpiryRetry LeaseExpiryPolicy = "retry"
	// LeaseExpiryFail terminally fails work that cannot be reconstructed after process loss.
	LeaseExpiryFail LeaseExpiryPolicy = "fail"
)

// Spec is immutable serialized task intent.
type Spec struct {
	ID          string
	ExecutorKey string
	Kind        string
	Payload     []byte

	Description   string
	OutputFile    string
	SessionID     string
	NotifySession bool
	CreatedAt     time.Time
}

// NotificationKind identifies the lifecycle transition that created a notification.
type NotificationKind string

const (
	// NotificationWaitingInput reports that a task is paused waiting for resume input.
	NotificationWaitingInput NotificationKind = "waiting_input"
	// NotificationCompleted reports that a task completed successfully.
	NotificationCompleted NotificationKind = "completed"
	// NotificationFailed reports that a task failed.
	NotificationFailed NotificationKind = "failed"
	// NotificationCanceled reports that a task was canceled.
	NotificationCanceled NotificationKind = "canceled"
)

// Notification is only a durable wake-up pointer.
// Dispatcher loads authoritative task data before session inbox delivery.
type Notification struct {
	ID        string
	TaskID    string
	Version   int64
	Kind      NotificationKind
	CreatedAt time.Time

	// Task is nil for pointer-only outbox storage and populated for delivered
	// notifications stored in session inboxes.
	Task *Task
}

// CreateTaskRequest creates a task from immutable serialized intent and the
// recovery policy owned by its registered Executor.
type CreateTaskRequest struct {
	Spec              Spec
	LeaseExpiryPolicy LeaseExpiryPolicy
}

// ListPendingRequest lists pending task candidates for the given executor keys.
type ListPendingRequest struct {
	ExecutorKeys []string
	Cursor       string
	Limit        int
}

// ListPendingResult contains pending task candidates and an optional cursor.
type ListPendingResult struct {
	Tasks      []*Task
	NextCursor string
}

// ListSuspendedRequest lists suspended tasks for the given executor keys.
type ListSuspendedRequest struct {
	ExecutorKeys []string
	Cursor       string
	Limit        int
}

// ListSuspendedResult contains suspended task snapshots and an optional cursor.
type ListSuspendedResult struct {
	Tasks      []*Task
	NextCursor string
}

// StartTaskRequest asks the TaskStore to authorize a new active attempt.
type StartTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
}

// HeartbeatRequest reports liveness for an active attempt.
type HeartbeatRequest struct {
	TaskID          string
	ExpectedVersion int64
}

// ReportTranscriptFailureRequest records the first failure of an optional output transcript.
type ReportTranscriptFailureRequest struct {
	TaskID          string
	ExpectedVersion int64
	Error           string
}

// CompleteTaskRequest records successful task completion.
type CompleteTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	Data            []byte
}

// FailTaskRequest records failed task completion.
type FailTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	Error           string
}

// WaitInputTaskRequest checkpoints a task that is waiting for external input.
type WaitInputTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	Checkpoint      []byte
}

// SuspendTaskRequest checkpoints a task for a planned suspension.
type SuspendTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	Checkpoint      []byte
}

// YieldTaskRequest relinquishes an active recoverable attempt and returns the
// task to pending without implying that the underlying operation was suspended.
// An empty Checkpoint retains the task's latest boundary checkpoint.
type YieldTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	Checkpoint      []byte
}

// AckCancelRequest records active-attempt acknowledgement of cancellation.
type AckCancelRequest struct {
	TaskID          string
	ExpectedVersion int64
	// Reason is used when no durable cancellation reason was previously recorded.
	Reason string
}

// RequestCancelRequest records durable cancellation intent. Active work remains
// running until its attempt acknowledges cancellation. Retry-capable work whose
// lease was lost remains pending until a recovery attempt stops the operation.
type RequestCancelRequest struct {
	TaskID          string
	ExpectedVersion int64
	// Reason is optional and first-write for repeated cancellation requests.
	Reason string
}

// ResumeRequest stores a one-shot resume command for a waiting task.
type ResumeRequest struct {
	TaskID          string
	ExpectedVersion int64
	Data            []byte
}

// ReleaseSuspensionRequest returns a suspended task to pending.
type ReleaseSuspensionRequest struct {
	TaskID          string
	ExpectedVersion int64
}

// WaitForTaskVersionRequest identifies a task and the latest snapshot version
// observed by the caller. Task progress events do not advance Version.
type WaitForTaskVersionRequest struct {
	TaskID       string
	AfterVersion int64
}

// TaskEvent is one immutable task-progress event. EventID is an opaque,
// task-local replay identity and does not encode event chronology.
type TaskEvent struct {
	EventID   string
	TaskID    string
	Data      []byte
	CreatedAt time.Time
}

// AppendTaskEventRequest appends one identified progress event for the active
// task attempt. EventID uniqueness is task-wide across attempts.
type AppendTaskEventRequest struct {
	TaskID  string
	Attempt int64
	EventID string
	Data    []byte
}

// AppendTaskEventResult reports whether the event was newly inserted. A
// byte-identical replay returns the original Event with Inserted false.
type AppendTaskEventResult struct {
	Event    *TaskEvent
	Inserted bool
}

// ListTaskEventsRequest requests one snapshot-stable page of task events.
// NewestFirst selects reverse append order; Cursor continues the same task,
// direction, and snapshot established by the first page. Limit defaults to 100
// and is capped at 1000.
type ListTaskEventsRequest struct {
	TaskID      string
	Cursor      string
	Limit       int
	NewestFirst bool
}

// ListTaskEventsResult contains one page and an opaque continuation cursor.
// NextCursor is empty when the snapshot has been exhausted.
type ListTaskEventsResult struct {
	Events     []*TaskEvent
	NextCursor string
}

// NotificationReceipt is an opaque token authorizing acknowledgement of one
// notification during its current lease.
type NotificationReceipt []byte

// ReceiveNotificationsRequest leases visible notifications from an outbox.
type ReceiveNotificationsRequest struct {
	Limit         int
	LeaseDuration time.Duration
}

// NotificationDelivery contains a notification and its acknowledgement receipt.
type NotificationDelivery struct {
	Record  Notification
	Receipt NotificationReceipt
}

// ReceiveNotificationsResult contains leased notification deliveries.
type ReceiveNotificationsResult struct {
	Deliveries []NotificationDelivery
}
