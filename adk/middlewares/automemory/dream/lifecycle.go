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

package dream

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// This file defines the dream job data model and the in-process cancel plumbing the
// operations in api.go act on.

// Status is the lifecycle status of a dream job.
type Status string

const (
	// StatusPending means the job was created and queued.
	StatusPending Status = "pending"
	// StatusRunning means the consolidation pipeline is processing.
	StatusRunning Status = "running"
	// StatusCompleted means the job finished successfully and the output
	// directory holds the consolidated memory.
	StatusCompleted Status = "completed"
	// StatusFailed means the job terminated with an error.
	StatusFailed Status = "failed"
	// StatusCanceled means the job was canceled before completing.
	StatusCanceled Status = "canceled"
)

// IsTerminal reports whether the status is a terminal state.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

// Job is the observable record of one dream run. It is persisted to the
// KVStore so callers can poll GetDreamStatus and request CancelDream.
type Job struct {
	// ID is the unique job identifier returned by Run and by middleware-triggered runs.
	ID string `json:"id"`

	// Status is the current lifecycle status.
	Status Status `json:"status"`

	// InputDir is the source memory directory the run reads from. It is never modified.
	InputDir string `json:"input_dir"`

	// OutputDir is where consolidated memory is written on success.
	OutputDir string `json:"output_dir"`

	// StagingDir is the working copy the model edits during the run.
	StagingDir string `json:"staging_dir"`

	// SessionIDs are the sessions in scope for this run.
	SessionIDs []string `json:"session_ids,omitempty"`

	// CreatedAt is when the job record was created.
	CreatedAt time.Time `json:"created_at"`

	// EndedAt is when the job reached a terminal state. Zero while non-terminal.
	EndedAt time.Time `json:"ended_at,omitempty"`

	// ErrMsg is the failure reason when Status is failed. Empty otherwise.
	ErrMsg string `json:"err_msg,omitempty"`
}

// jobTTL is how long terminal job records are retained for status queries.
const jobTTL = 24 * time.Hour

// cancelRegistry tracks the cancel handle for jobs running in this process so
// CancelDream can abort an in-flight run. Cross-process cancellation is
// best-effort: a running node also polls its job record between agent iterations.
var cancelRegistry sync.Map // jobID -> *cancelHandle

var errDreamCanceled = fmt.Errorf("dream: canceled")

// cancelHandle pairs a context.CancelFunc with the cause it was canceled for.
// It stands in for Go 1.21's context.WithCancelCause/context.Cause, which are not
// available on the Go 1.18 baseline this module supports.
type cancelHandle struct {
	cancel context.CancelFunc

	mu    sync.Mutex
	cause error
}

// trigger records the cause (first writer wins) and cancels the context.
func (h *cancelHandle) trigger(cause error) {
	h.mu.Lock()
	if h.cause == nil {
		h.cause = cause
	}
	h.mu.Unlock()
	h.cancel()
}

// Cause returns the recorded cancel cause, or nil if none was set.
func (h *cancelHandle) Cause() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cause
}

func registerCancel(jobID string, h *cancelHandle) {
	cancelRegistry.Store(jobID, h)
}

func unregisterCancel(jobID string) {
	cancelRegistry.Delete(jobID)
}

func signalCancel(jobID string) {
	if v, ok := cancelRegistry.Load(jobID); ok {
		if h, ok := v.(*cancelHandle); ok {
			h.trigger(errDreamCanceled)
		}
	}
}

// jobCancelCause returns the cancel cause recorded for jobID if its run is still
// tracked in this process; otherwise it falls back to ctx.Err(). It lets a canceled
// run distinguish a dream cancellation (errDreamCanceled) from an external parent
// cancellation (context.Canceled) without context.Cause.
func jobCancelCause(jobID string, ctx context.Context) error {
	if v, ok := cancelRegistry.Load(jobID); ok {
		if h, ok := v.(*cancelHandle); ok {
			if cause := h.Cause(); cause != nil {
				return cause
			}
		}
	}
	return ctx.Err()
}

// detachedContext carries a parent's values but is never canceled and has no
// deadline. It replaces context.WithoutCancel (Go 1.21) on the Go 1.18 baseline,
// letting a background dream run outlive the request context while still reading its
// values (loggers, trace spans).
type detachedContext struct {
	parent context.Context
}

func withoutCancel(parent context.Context) context.Context {
	return detachedContext{parent: parent}
}

func (detachedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (detachedContext) Done() <-chan struct{}       { return nil }
func (detachedContext) Err() error                  { return nil }
func (c detachedContext) Value(key any) any {
	if c.parent == nil {
		return nil
	}
	return c.parent.Value(key)
}

// newJobID returns a unique-ish job id. It does not rely on time/random sources
// being deterministic; the random token plus the supplied seed make collisions
// negligible in practice.
func newJobID(seed string) string {
	return "drm_" + dirHash(seed+randToken())
}
