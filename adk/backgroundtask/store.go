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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var (
	ErrNotFound          = errors.New("backgroundtask: task not found")
	ErrAlreadyExists     = errors.New("backgroundtask: task already exists")
	ErrVersionConflict   = errors.New("backgroundtask: transition version conflict")
	ErrLeaseLost         = errors.New("backgroundtask: lease lost")
	ErrIllegalTransition = errors.New("backgroundtask: illegal state transition")
	ErrInvalidArtifact   = errors.New("backgroundtask: invalid artifact")
	ErrAlreadyTerminal   = errors.New("backgroundtask: task is already terminal")
	// ErrCheckpointUnavailable reports that a planned drain reached no safe,
	// compatible checkpoint. Manager stops renewing and lets persisted recovery
	// policy resolve the expired attempt instead of claiming suspension.
	ErrCheckpointUnavailable = errors.New("backgroundtask: checkpoint unavailable")
)

type Store interface {
	Create(context.Context, *CreateTaskRequest) (*Task, error)
	Get(context.Context, string) (*Task, error)
	ListUpdates(context.Context, *ListTaskUpdatesRequest) (*ListTaskUpdatesResult, error)
	ListClaimable(context.Context, *ListClaimableRequest) (*ListClaimableResult, error)
	Claim(context.Context, *ClaimTaskRequest) (*ClaimTaskResult, error)
	Renew(context.Context, *RenewLeaseRequest) (*Task, error)
	AppendUpdate(context.Context, *AppendTaskUpdateRequest) (*AppendTaskUpdateResult, error)
	Commit(context.Context, *CommitTaskRequest) (*CommitTaskResult, error)
	RequestCancel(context.Context, *RequestCancelRequest) (*RequestCancelResult, error)
	Resume(context.Context, *ResumeTaskRequest) (*Task, error)
	ReleaseSuspension(context.Context, *ReleaseSuspensionRequest) (*Task, error)
	Wait(context.Context, *WaitTaskRequest) (*Task, error)
	WaitUpdates(context.Context, *WaitTaskUpdatesRequest) (*ListTaskUpdatesResult, error)
}

// ArtifactVerifier confirms that an externally referenced immutable artifact is
// durable and matches the descriptor about to be committed.
type ArtifactVerifier interface {
	Verify(context.Context, string, string, int64) error
}

type NotificationOutbox interface {
	Receive(context.Context, *ReceiveNotificationsRequest) (*ReceiveNotificationsResult, error)
	Ack(context.Context, NotificationReceipt) error
}

func terminalStatus(s Status) bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCanceled
}

func validateSpec(s Spec) error {
	if s.ID == "" || s.ExecutorKey == "" || s.PayloadVersion == "" {
		return fmt.Errorf("backgroundtask: id, executor key, and payload version are required")
	}
	if s.Recovery.MaxAttempts <= 0 {
		return fmt.Errorf("backgroundtask: recovery max attempts must be positive")
	}
	switch s.Recovery.OnLeaseExpired {
	case RecoveryFail, RecoveryResumeCheckpoint, RecoveryRestartFromSpec:
	default:
		return fmt.Errorf("backgroundtask: invalid lease-expiry recovery action %q", s.Recovery.OnLeaseExpired)
	}
	switch s.Recovery.OnMissingCheckpoint {
	case RecoveryFail, RecoveryRestartFromSpec:
	default:
		return fmt.Errorf("backgroundtask: invalid missing-checkpoint recovery action %q", s.Recovery.OnMissingCheckpoint)
	}
	if s.Notify != nil {
		if s.Notify.Kind == "" || s.Notify.TargetID == "" {
			return fmt.Errorf("backgroundtask: notification kind and target id are required")
		}
		if len(s.Notify.Kind) > 128 || len(s.Notify.TargetID) > 512 || len(s.Notify.Metadata) > 32 {
			return fmt.Errorf("backgroundtask: notification target exceeds bounds")
		}
		for key, value := range s.Notify.Metadata {
			if !strings.Contains(key, "/") || len(key) > 256 || len(value) > 1024 {
				return fmt.Errorf("backgroundtask: notification metadata must use bounded namespaced keys")
			}
		}
		if s.Notify.Kind == "session_inbox" && s.Notify.TargetID != s.SessionID {
			return fmt.Errorf("backgroundtask: session inbox target must match task session")
		}
	}
	return nil
}

func validateArtifact(v ArtifactValue) error {
	inline := v.Payload != nil
	external := v.Ref != nil
	if inline == external {
		return fmt.Errorf("%w: exactly one payload or reference is required", ErrInvalidArtifact)
	}
	const digestPrefix = "sha256:"
	if !strings.HasPrefix(v.Digest, digestPrefix) {
		return fmt.Errorf("%w: digest must use sha256", ErrInvalidArtifact)
	}
	decodedDigest, digestErr := hex.DecodeString(strings.TrimPrefix(v.Digest, digestPrefix))
	if digestErr != nil || len(decodedDigest) != sha256.Size {
		return fmt.Errorf("%w: invalid sha256 digest", ErrInvalidArtifact)
	}
	if inline {
		if v.Encoding == "" {
			return fmt.Errorf("%w: inline encoding is required", ErrInvalidArtifact)
		}
		if v.Size != int64(len(v.Payload)) {
			return fmt.Errorf("%w: inline size mismatch", ErrInvalidArtifact)
		}
		sum := sha256.Sum256(v.Payload)
		if !equalBytes(decodedDigest, sum[:]) {
			return fmt.Errorf("%w: inline digest mismatch", ErrInvalidArtifact)
		}
	} else if v.Ref.StoreKey == "" || v.Ref.Key == "" {
		return fmt.Errorf("%w: external store key and artifact key are required", ErrInvalidArtifact)
	}
	if v.Size < 0 {
		return fmt.Errorf("%w: digest and non-negative size are required", ErrInvalidArtifact)
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}

func validateProgress(p *Progress) error {
	if p == nil {
		return errors.New("backgroundtask: progress is required")
	}
	for _, n := range []*float64{p.Current, p.Total} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0) {
			return errors.New("backgroundtask: progress values must be finite and non-negative")
		}
	}
	if p.Current != nil && p.Total != nil && *p.Current > *p.Total {
		return errors.New("backgroundtask: progress current exceeds total")
	}
	if len(p.Unit) > 64 || len(p.Message) > 2048 {
		return errors.New("backgroundtask: progress text exceeds bounds")
	}
	return nil
}

func validateAppend(req *AppendTaskUpdateRequest) error {
	if req == nil {
		return errors.New("backgroundtask: append update request is required")
	}
	switch req.Kind {
	case UpdateProgress:
		if err := validateProgress(req.Progress); err != nil {
			return err
		}
	case UpdateMessage:
		if req.Progress != nil || req.Payload == nil {
			return errors.New("backgroundtask: message requires payload only")
		}
	case UpdateStatus, UpdateInputRequired:
		return errors.New("backgroundtask: reserved update kind is Store-generated")
	default:
		if !strings.Contains(string(req.Kind), "/") || req.Progress != nil || req.Payload == nil {
			return errors.New("backgroundtask: custom update kind must be namespaced and carry payload")
		}
	}
	if req.Payload != nil {
		if req.Payload.Type == "" {
			return errors.New("backgroundtask: update payload type is required")
		}
		if err := validateArtifact(req.Payload.Value); err != nil {
			return err
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
	c.Deadline = cloneTime(v.Deadline)
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

func cloneArtifact(v ArtifactValue) ArtifactValue {
	c := v
	c.Payload = cloneBytes(v.Payload)
	if v.Ref != nil {
		r := *v.Ref
		c.Ref = &r
	}
	return c
}

func cloneCheckpoint(v *CheckpointRef) *CheckpointRef {
	if v == nil {
		return nil
	}
	c := *v
	c.State = cloneArtifact(v.State)
	return &c
}

func cloneResult(v *ResultRef) *ResultRef {
	if v == nil {
		return nil
	}
	c := *v
	c.Value = cloneArtifact(v.Value)
	return &c
}

func cloneProgress(v *Progress) *Progress {
	if v == nil {
		return nil
	}
	c := *v
	if v.Current != nil {
		n := *v.Current
		c.Current = &n
	}
	if v.Total != nil {
		n := *v.Total
		c.Total = &n
	}
	return &c
}

func clonePayload(v *UpdatePayload) *UpdatePayload {
	if v == nil {
		return nil
	}
	c := *v
	c.Value = cloneArtifact(v.Value)
	return &c
}

func cloneUpdate(v *Update) *Update {
	if v == nil {
		return nil
	}
	c := *v
	if v.Status != nil {
		s := *v.Status
		c.Status = &s
	}
	c.Progress = cloneProgress(v.Progress)
	c.Payload = clonePayload(v.Payload)
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
	c.Progress = cloneProgress(v.Progress)
	c.Checkpoint = cloneCheckpoint(v.Checkpoint)
	c.Result = cloneResult(v.Result)
	return &c
}
