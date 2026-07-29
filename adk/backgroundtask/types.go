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

// Result is the terminal execution outcome. Pending and running tasks do not
// have a Result.
type Result struct {
	Data  []byte
	Error string
}

// PendingResume is a one-shot resume command fenced to the checkpoint it was
// validated against.
type PendingResume struct {
	CheckpointVersion int64
	Data              []byte
}

type NotificationEventKind string

const (
	NotificationWaitingInput NotificationEventKind = "waiting_input"
	NotificationCompleted    NotificationEventKind = "completed"
	NotificationFailed       NotificationEventKind = "failed"
	NotificationCanceled     NotificationEventKind = "canceled"
)

// NotificationOutboxRecord is only a durable wake-up and routing pointer.
// Authoritative task data is loaded by the consumer.
type NotificationOutboxRecord struct {
	NotificationID    string
	TaskID            string
	TransitionVersion int64
	EventKind         NotificationEventKind
	Target            NotificationTarget
	CreatedAt         time.Time
}

type ExecutorCapability struct {
	ExecutorKey string
}

type LeaseToken struct {
	TaskID          string
	ExpectedVersion int64
	LeaseOwnerID    string
	Generation      int64
}

type CreateTaskRequest struct {
	Spec Spec
}

type ListClaimableRequest struct {
	Capabilities []ExecutorCapability
	Cursor       string
	Limit        int
}

type ListClaimableResult struct {
	Tasks      []*Task
	NextCursor string
}

type ClaimTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	LeaseOwnerID    string
	LeaseDuration   time.Duration
}

type ClaimTaskResult struct {
	Task  *Task
	Lease LeaseToken
}

type RenewLeaseRequest struct {
	Lease         LeaseToken
	LeaseDuration time.Duration
}

// CommitTaskRequest atomically persists an executor-originated lifecycle
// transition under the current lease fence.
type CommitTaskRequest struct {
	Lease      LeaseToken
	Status     Status
	Checkpoint []byte
	Result     *Result
}

type CommitTaskResult struct {
	Task         *Task
	Notification *NotificationOutboxRecord
}

type RequestCancelRequest struct {
	TaskID          string
	ExpectedVersion int64
}

type RequestCancelResult struct {
	Task         *Task
	Notification *NotificationOutboxRecord
}

type ResumeTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	Data            []byte
}

type ReleaseSuspensionRequest struct {
	TaskID          string
	ExpectedVersion int64
}

type WaitTaskRequest struct {
	TaskID       string
	AfterVersion int64
}

type NotificationReceipt []byte

type ReceiveNotificationsRequest struct {
	ConsumerID     string
	Limit          int
	VisibilityTime time.Duration
}

type NotificationDelivery struct {
	Record  NotificationOutboxRecord
	Receipt NotificationReceipt
}

type ReceiveNotificationsResult struct {
	Deliveries []NotificationDelivery
}
