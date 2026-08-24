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
	"time"

	"github.com/cloudwego/eino/adk/task"
)

// LeaseExpiryPolicy controls how TaskStore resolves an expired active attempt.
type LeaseExpiryPolicy string

const (
	// LeaseExpiryRetry returns the task to pending for another durable attempt.
	LeaseExpiryRetry LeaseExpiryPolicy = "retry"
	// LeaseExpiryFail terminally fails work that cannot be reconstructed after process loss.
	LeaseExpiryFail LeaseExpiryPolicy = "fail"
)

// Spec is immutable serialized task intent supplied by the caller. Payload and
// all returned byte slices must be copied across provider boundaries.
// OutputFile names an optional derived transcript destination; it is not
// authoritative task output. Providers may impose documented size bounds such
// as InMemoryStoreConfig.
type Spec struct {
	ID          string
	ExecutorKey string
	Kind        string
	Payload     []byte

	Description  string
	OutputFile   string
	ParentTaskID string
	// SessionID is an optional session notification target. It is routing
	// metadata, not the task's execution identity and not necessarily the
	// session of an agent executing the task.
	SessionID string
	// NotifySession enables waiting and terminal lifecycle notifications when
	// SessionID is non-empty. Tasks do not otherwise require a session.
	NotifySession bool
}

// NotificationKind identifies the lifecycle transition that created a notification.
type NotificationKind string

const (
	// NotificationTaskCreated reports that a parent-owned task was created. It
	// is the durable recovery source for TaskCreated session-event delivery.
	NotificationTaskCreated NotificationKind = "task_created"
	// NotificationTaskBackgrounded reports foreground ownership transfer.
	NotificationTaskBackgrounded NotificationKind = "task_backgrounded"
	// NotificationWaitingInput reports that a task is paused waiting for resume input.
	NotificationWaitingInput NotificationKind = "waiting_input"
	// NotificationCompleted reports that a task completed successfully.
	NotificationCompleted NotificationKind = "completed"
	// NotificationFailed reports that a task failed.
	NotificationFailed NotificationKind = "failed"
	// NotificationCanceled reports that a task was canceled.
	NotificationCanceled NotificationKind = "canceled"
)

// Notification is one durable session-routed lifecycle or application event.
// Lifecycle records have empty Data; consumers load authoritative task state
// by TaskID when needed. Application Data is opaque and independently owned.
type Notification struct {
	ID        string
	TaskID    string
	SessionID string
	Version   int64
	Kind      NotificationKind
	Data      []byte
	Delivery  task.InputDelivery
	CreatedAt time.Time
}

// NotifyParentRequest describes one application notification emitted by the
// current durable attempt. EventID is task-local, idempotent, required, and
// limited to 1024 bytes. Kind is required, limited to 64 bytes, and must not
// use a lifecycle kind or the reserved "eino." prefix. Data is opaque and
// limited to 256 KiB. Bounds are measured in bytes.
type NotifyParentRequest struct {
	EventID  string
	Kind     NotificationKind
	Data     []byte
	Delivery task.InputDelivery
}

// CreateTaskRequest creates a task from immutable serialized intent and the
// recovery policy owned by its registered Executor. TaskStore assigns all
// lifecycle timestamps, including TaskSnapshot.CreatedAt.
type CreateTaskRequest struct {
	Spec              Spec
	LeaseExpiryPolicy LeaseExpiryPolicy
	Checkpoint        []byte
	// ParentExecution authorizes creation under Spec.ParentTaskID.
	ParentExecution *task.ExecutionContext
	// ContextSnapshot is an opaque manager-owned snapshot of selected context
	// values captured at submission time.
	ContextSnapshot []byte
}

// ListPendingRequest lists pending task candidates for the given executor keys.
// Results use stable task-ID order. Cursor continues after the previous page;
// it is scoped to the same provider and filter. Limit defaults to 100 and is
// capped at 1000.
type ListPendingRequest struct {
	ExecutorKeys []string
	Cursor       string
	Limit        int
}

// ListPendingResult contains independent task snapshots. NextCursor is empty
// when the current traversal is exhausted.
type ListPendingResult struct {
	Tasks      []*TaskSnapshot
	NextCursor string
}

// ListSuspendedRequest lists suspended tasks with the same ordering, cursor,
// filter, and limit rules as ListPendingRequest.
type ListSuspendedRequest struct {
	ExecutorKeys []string
	Cursor       string
	Limit        int
}

// ListSuspendedResult contains independent task snapshots. NextCursor is empty
// when the current traversal is exhausted.
type ListSuspendedResult struct {
	Tasks      []*TaskSnapshot
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

// ReportTranscriptFailureRequest records the first failure of an optional
// derived transcript. It does not change task lifecycle status; later reports
// preserve the first recorded error.
type ReportTranscriptFailureRequest struct {
	TaskID          string
	ExpectedVersion int64
	Error           string
}

// CommitStartRequest records that the external operation for the current
// running attempt was established and persists its initial checkpoint.
type CommitStartRequest struct {
	TaskID          string
	ExpectedVersion int64
	Checkpoint      []byte
}

// CommitInputRequest records an established resumed operation together with
// the mailbox cursor that supplied its input.
type CommitInputRequest struct {
	TaskID          string
	ExpectedVersion int64
	Attempt         int64
	ExpectedCursor  int64
	InputCursor     int64
	Checkpoint      []byte
}

// FailTaskRequest records failed task completion.
type FailTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	Error           string
}

// YieldTaskRequest relinquishes an active recoverable attempt and returns the
// task to pending without implying that the underlying operation was suspended.
// An empty Checkpoint retains the task's latest boundary checkpoint. Any
// pending resume command is also retained for idempotent replay.
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

// ReleaseSuspensionRequest returns a suspended task to pending.
type ReleaseSuspensionRequest struct {
	TaskID          string
	ExpectedVersion int64
	// ContextSnapshot is Manager-owned. Manager callers should leave it empty;
	// Manager captures it from Config.ContextSnapshotter. TaskStore
	// implementations replace the task's stored context snapshot when this field
	// is non-nil. An empty non-nil slice clears it.
	ContextSnapshot []byte
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
// and is capped at 1000; callers may change Limit between pages. An empty first
// page has no continuation cursor.
type ListTaskEventsRequest struct {
	TaskID      string
	Cursor      string
	Limit       int
	NewestFirst bool
}

// ListTaskEventsResult contains an independently owned page and an opaque
// continuation cursor. NextCursor is empty when the captured snapshot has been
// exhausted.
type ListTaskEventsResult struct {
	Events     []*TaskEvent
	NextCursor string
}

// NotificationReceipt is an opaque token authorizing acknowledgement of one
// notification during its current lease. Callers and providers must copy its
// bytes and must not mutate a receipt after passing it across the SPI.
type NotificationReceipt []byte

// ReceiveNotificationsRequest leases visible notifications from an outbox.
// Limit defaults to 100 and is capped at 1000. LeaseDuration must be positive.
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
