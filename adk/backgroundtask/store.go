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
	ErrNotFound          = errors.New("backgroundtask: task not found")
	ErrAlreadyExists     = errors.New("backgroundtask: task already exists")
	ErrVersionConflict   = errors.New("backgroundtask: transition version conflict")
	ErrLeaseLost         = errors.New("backgroundtask: lease lost")
	ErrIllegalTransition = errors.New("backgroundtask: illegal state transition")
	ErrInvalidResult     = errors.New("backgroundtask: invalid result")
	ErrAlreadyTerminal   = errors.New("backgroundtask: task is already terminal")
	// ErrCheckpointUnavailable reports that a planned drain reached no safe,
	// compatible checkpoint. Manager stops renewing the current lease so expiry
	// can redispatch from the last durable checkpoint.
	ErrCheckpointUnavailable = errors.New("backgroundtask: checkpoint unavailable")
)

type Store interface {
	Create(context.Context, *CreateTaskRequest) (*Task, error)
	Get(context.Context, string) (*Task, error)
	ListClaimable(context.Context, *ListClaimableRequest) (*ListClaimableResult, error)
	Claim(context.Context, *ClaimTaskRequest) (*ClaimTaskResult, error)
	Renew(context.Context, *RenewLeaseRequest) (*Task, error)
	Commit(context.Context, *CommitTaskRequest) (*CommitTaskResult, error)
	RequestCancel(context.Context, *RequestCancelRequest) (*RequestCancelResult, error)
	Resume(context.Context, *ResumeTaskRequest) (*Task, error)
	ReleaseSuspension(context.Context, *ReleaseSuspensionRequest) (*Task, error)
	Wait(context.Context, *WaitTaskRequest) (*Task, error)
}

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

func validateTaskSnapshot(status Status, result *Result) error {
	switch status {
	case StatusPending, StatusRunning, StatusWaitingInput, StatusSuspended, StatusCanceling:
		if result != nil {
			return fmt.Errorf("%w: non-terminal task cannot have a result", ErrInvalidResult)
		}
	case StatusCompleted, StatusFailed, StatusCanceled:
		if result == nil {
			return fmt.Errorf("%w: terminal task requires a result", ErrInvalidResult)
		}
	default:
		return fmt.Errorf("%w: unsupported status %q", ErrInvalidResult, status)
	}
	if result == nil {
		return nil
	}
	if len(result.Error) > 4096 {
		return errors.New("backgroundtask: result error exceeds configured bounds")
	}
	switch status {
	case StatusCompleted:
		if result.Error != "" {
			return fmt.Errorf("%w: completed result cannot carry an error", ErrInvalidResult)
		}
	case StatusFailed:
		if result.Error == "" {
			return fmt.Errorf("%w: failed result requires an error", ErrInvalidResult)
		}
	case StatusCanceled:
		if len(result.Data) != 0 {
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

func cloneResult(v *Result) *Result {
	if v == nil {
		return nil
	}
	c := *v
	c.Data = cloneBytes(v.Data)
	return &c
}

func clonePendingResume(v *PendingResume) *PendingResume {
	if v == nil {
		return nil
	}
	c := *v
	c.Data = cloneBytes(v.Data)
	return &c
}

func cloneNotification(v *NotificationOutboxRecord) *NotificationOutboxRecord {
	if v == nil {
		return nil
	}
	c := *v
	c.Target.Metadata = make(map[string]string, len(v.Target.Metadata))
	for k, value := range v.Target.Metadata {
		c.Target.Metadata[k] = value
	}
	return &c
}
