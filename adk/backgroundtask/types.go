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

// NotificationTarget describes where lifecycle notifications should be routed.
type NotificationTarget struct {
	Kind     string
	TargetID string
	Metadata map[string]string
}

// Spec is immutable serialized task intent.
type Spec struct {
	ID          string
	ExecutorKey string
	Payload     []byte

	Description string
	SessionID   string
	Notify      *NotificationTarget
	CreatedAt   time.Time
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

// Notification is only a durable wake-up and routing pointer.
// Authoritative task data is loaded by the consumer.
type Notification struct {
	ID        string
	TaskID    string
	Version   int64
	Kind      NotificationKind
	Target    NotificationTarget
	CreatedAt time.Time

	// Task is nil for pointer-only outbox storage and populated for delivered
	// notifications passed to sinks or stored in session inboxes.
	Task *Task
}

// CreateTaskRequest creates a task from immutable serialized intent.
type CreateTaskRequest struct {
	Spec Spec
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

// StartTaskRequest asks the Store to authorize a new active attempt.
type StartTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
}

// HeartbeatRequest reports liveness for an active attempt.
type HeartbeatRequest struct {
	TaskID          string
	ExpectedVersion int64
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

// CancelTaskRequest records active-attempt acknowledgement of cancellation.
type CancelTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
}

// RequestCancelRequest records durable cancellation intent.
type RequestCancelRequest struct {
	TaskID          string
	ExpectedVersion int64
}

// ResumeTaskRequest stores a one-shot resume command for a waiting task.
type ResumeTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	Data            []byte
}

// ReleaseSuspensionRequest returns a suspended task to pending.
type ReleaseSuspensionRequest struct {
	TaskID          string
	ExpectedVersion int64
}

// WaitTaskRequest waits until a task advances beyond a known version.
type WaitTaskRequest struct {
	TaskID       string
	AfterVersion int64
}

// NotificationReceipt identifies a received outbox notification for acknowledgement.
type NotificationReceipt []byte

// ReceiveNotificationsRequest leases visible notifications from an outbox.
type ReceiveNotificationsRequest struct {
	ConsumerID     string
	Limit          int
	VisibilityTime time.Duration
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
