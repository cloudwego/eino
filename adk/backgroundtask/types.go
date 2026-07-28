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

type RecoveryAction string

const (
	RecoveryFail             RecoveryAction = "fail"
	RecoveryResumeCheckpoint RecoveryAction = "resume_checkpoint"
	RecoveryRestartFromSpec  RecoveryAction = "restart_from_spec"
)

type RecoveryPolicy struct {
	OnLeaseExpired      RecoveryAction
	OnMissingCheckpoint RecoveryAction
	MaxAttempts         int
}

type ResultPolicy struct {
	ResultStoreKey string
	ResultFormat   string
}

type NotificationTarget struct {
	Kind     string
	TargetID string
	Metadata map[string]string
}

// Spec is immutable serialized task intent.
type Spec struct {
	ID              string
	ExecutorKey     string
	SpecVersion     string
	Payload         []byte
	PayloadEncoding string

	Type        string
	Description string
	SessionID   string
	Notify      *NotificationTarget

	Recovery RecoveryPolicy
	Result   ResultPolicy

	ToolUseID string
	TraceID   string
	Deadline  *time.Time
	CreatedAt time.Time
}

type ArtifactRef struct {
	StoreKey string
	Key      string
}

type ArtifactValue struct {
	Payload  []byte
	Encoding string
	Ref      *ArtifactRef
	Digest   string
	Size     int64
}

type CheckpointRef struct {
	ExecutorKey string
	Format      string
	Version     string
	Sequence    int64
	State       ArtifactValue
	CreatedAt   time.Time
}

type ResultRef struct {
	Format    string
	Value     ArtifactValue
	CreatedAt time.Time
}

type UpdateKind string

const (
	UpdateStatus        UpdateKind = "status"
	UpdateProgress      UpdateKind = "progress"
	UpdateMessage       UpdateKind = "message"
	UpdateInputRequired UpdateKind = "input_required"
)

type Progress struct {
	Current *float64
	Total   *float64
	Unit    string
	Message string
}

type UpdatePayload struct {
	Type  string
	Value ArtifactValue
}

type Update struct {
	UpdateID        string
	TaskID          string
	Sequence        int64
	Attempt         int64
	LeaseGeneration int64
	Kind            UpdateKind
	Status          *Status
	Progress        *Progress
	Payload         *UpdatePayload
	CreatedAt       time.Time
}

type NotificationEventKind string

const (
	NotificationUpdateAvailable NotificationEventKind = "update_available"
	NotificationWaitingInput    NotificationEventKind = "waiting_input"
	NotificationCompleted       NotificationEventKind = "completed"
	NotificationFailed          NotificationEventKind = "failed"
	NotificationCanceled        NotificationEventKind = "canceled"
)

type NotificationOutboxRecord struct {
	NotificationID    string
	TaskID            string
	TransitionVersion int64
	UpdateSequence    int64
	EventKind         NotificationEventKind
	Status            Status
	SessionID         string
	Target            NotificationTarget
	Progress          *Progress
	Checkpoint        *CheckpointRef
	Result            *ResultRef
	Reason            string
	CreatedAt         time.Time
}

type ExecutorCapability struct {
	ExecutorKey string
	SpecVersion string
}

type LeaseToken struct {
	TaskID          string
	ExpectedVersion int64
	WorkerID        string
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
	WorkerID        string
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

type TaskMutation struct {
	ToStatus       Status
	Checkpoint     *CheckpointRef
	Result         *ResultRef
	InputRequest   *UpdatePayload
	TerminalReason string
}

type CommitTaskRequest struct {
	Lease    LeaseToken
	Mutation TaskMutation
}

type CommitTaskResult struct {
	Task         *Task
	Updates      []*Update
	Notification *NotificationOutboxRecord
}

type AppendTaskUpdateRequest struct {
	Lease    LeaseToken
	Kind     UpdateKind
	Progress *Progress
	Payload  *UpdatePayload
}

type AppendTaskUpdateResult struct {
	Task         *Task
	Update       *Update
	Notification *NotificationOutboxRecord
}

type ListTaskUpdatesRequest struct {
	TaskID        string
	AfterSequence int64
	Limit         int
}

type ListTaskUpdatesResult struct {
	Updates      []*Update
	NextSequence int64
}

type RequestCancelRequest struct {
	TaskID          string
	ExpectedVersion int64
}

type RequestCancelResult struct {
	Task         *Task
	Update       *Update
	Notification *NotificationOutboxRecord
}

type ResumeTaskRequest struct {
	TaskID          string
	ExpectedVersion int64
	ResumeData      []byte
	ResumeEncoding  string
}

type ReleaseSuspensionRequest struct {
	TaskID          string
	ExpectedVersion int64
}

type WaitTaskRequest struct {
	TaskID       string
	AfterVersion int64
}

type WaitTaskUpdatesRequest struct {
	TaskID        string
	AfterSequence int64
	Limit         int
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
