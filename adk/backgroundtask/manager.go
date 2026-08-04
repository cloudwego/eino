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

// Package backgroundtask provides a shared lifecycle registry for long-running
// executions (sub-agents, shell commands, ...) that may outlive the tool call
// that launched them.
//
// Manager coordinates Store-backed task submission, execution, and control. It
// is deliberately non-generic so one instance can serve heterogeneous executor
// domains under one task-ID space.
//
// The Store, outbox, and session-activation SPIs are provisional. Promotion
// requires conformance against external multi-process providers.
package backgroundtask

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Status represents the durable lifecycle status of a task.
type Status string

const (
	// StatusPending indicates the task is durable and available for claim.
	StatusPending Status = "pending"
	// StatusRunning indicates the task is currently executing.
	StatusRunning Status = "running"
	// StatusWaitingInput indicates execution is checkpointed pending external input.
	StatusWaitingInput Status = "waiting_input"
	// StatusSuspended indicates execution is checkpointed for a planned pause.
	StatusSuspended Status = "suspended"
	// StatusCompleted indicates the task finished successfully.
	StatusCompleted Status = "completed"
	// StatusFailed indicates the task terminated with an error.
	StatusFailed Status = "failed"
	// StatusCanceled indicates the task acknowledged an external stop request.
	StatusCanceled Status = "canceled"
)

// State aliases are kept for source compatibility while Status is the canonical
// lifecycle name.
type State = Status

const (
	StatePending      = StatusPending
	StateRunning      = StatusRunning
	StateWaitingInput = StatusWaitingInput
	StateSuspended    = StatusSuspended
	StateCompleted    = StatusCompleted
	StateFailed       = StatusFailed
	StateCanceled     = StatusCanceled
)

// Task represents a single managed execution record.
type Task struct {
	// Spec is the immutable serialized intent for this task.
	Spec Spec
	// LeaseExpiryPolicy is the immutable recovery policy selected by the
	// registered Executor when the task is created.
	LeaseExpiryPolicy LeaseExpiryPolicy
	// Status is the current lifecycle status.
	Status Status
	// Checkpoint is the latest durable executor checkpoint.
	Checkpoint []byte
	// ResultData is the terminal successful output. It is meaningful only when
	// Status is StatusCompleted.
	ResultData []byte
	// ResultError is the terminal failure or cancellation reason. It is meaningful
	// only when Status is StatusFailed or StatusCanceled.
	ResultError string
	// OutputFileErr records the first failure while producing the optional output transcript.
	OutputFileErr string
	// PendingResume is a one-shot resume command for the next active attempt.
	PendingResume []byte
	// Version is the CAS version of this durable record.
	Version int64
	// Attempt counts successful claims.
	Attempt int64
	// CancelRequestedAt records durable explicit stop intent while Status remains
	// StatusRunning until the active attempt acknowledges it as StatusCanceled.
	CancelRequestedAt *time.Time
	// UpdatedAt is the Store mutation time.
	UpdatedAt time.Time
	// DoneAt is the time the task reached a terminal state. Nil if still running.
	DoneAt *time.Time
}

// IDGenerator returns the complete ID for a new task.
//
// The generator sees the allocation request before the task is registered and may return a
// business-side identifier. Manager does not add the task-type prefix when IDGen
// is configured; callers that want one should include it in the returned ID.
type IDGenerator func(ctx context.Context, request *AllocateTaskIDRequest) (string, error)

// Config configures a Manager.
type Config struct {
	// Store is the authoritative task state provider. When nil, New installs an
	// in-memory reference store.
	Store Store
	// Executors resolves serialized task intent to local implementations.
	Executors *ExecutorRegistry
	// IDGen, when set, decides the full ID of every task created by this Manager.
	// If nil, Manager uses its default task-type-prefixed Base64URL ID.
	//
	// IDGen may be called concurrently by task submitters. It
	// must return a non-empty ID. The returned ID must be unique among this
	// Manager's registered tasks; a duplicate fails task creation.
	IDGen IDGenerator
}

// Manager owns Store-backed task lifecycle and worker coordination.
type Manager struct {
	store          Store
	executors      *ExecutorRegistry
	heartbeatEvery time.Duration
	attemptsMu     sync.Mutex
	activeAttempts map[string]*activeAttempt
	mu             sync.Mutex
	closed         bool
	idGen          IDGenerator
}

// New creates a new Manager.
func New(_ context.Context, conf *Config) *Manager {
	m := &Manager{
		heartbeatEvery: 10 * time.Second,
		activeAttempts: make(map[string]*activeAttempt),
	}
	m.store = NewInMemoryStore(nil)
	m.executors = NewExecutorRegistry()
	if conf != nil {
		if conf.Store != nil {
			m.store = conf.Store
		}
		if conf.Executors != nil {
			m.executors = conf.Executors
		}
		m.idGen = conf.IDGen
	}
	return m
}

// Close performs bounded graceful shutdown. When any attempt is active, ctx must
// have a deadline or Close returns ErrCloseDeadlineRequired without closing the
// Manager. Drainable attempts receive ControlDrain and may suspend or yield
// according to their executor contract; non-drainable attempts may finish until
// the deadline and are then durably canceled.
func (m *Manager) Close(ctx context.Context) error {
	m.attemptsMu.Lock()
	active := len(m.activeAttempts)
	m.attemptsMu.Unlock()
	if active > 0 {
		if _, ok := ctx.Deadline(); !ok {
			return ErrCloseDeadlineRequired
		}
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var closeErr error
	m.attemptsMu.Lock()
	closingAttempts := make([]*activeAttempt, 0, len(m.activeAttempts))
	for _, attempt := range m.activeAttempts {
		if attempt != nil {
			closingAttempts = append(closingAttempts, attempt)
		}
		if attempt != nil && attempt.supportsDrain {
			attempt.runtime.requestControl(ControlDrain)
		}
	}
	m.attemptsMu.Unlock()
	for {
		m.attemptsMu.Lock()
		active = len(m.activeAttempts)
		m.attemptsMu.Unlock()
		if active == 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			m.attemptsMu.Lock()
			var localTaskIDs []string
			for taskID, attempt := range m.activeAttempts {
				if attempt != nil && !attempt.supportsDrain {
					localTaskIDs = append(localTaskIDs, taskID)
				}
			}
			m.attemptsMu.Unlock()
			for _, taskID := range localTaskIDs {
				if _, err := m.RequestCancel(context.Background(), taskID); err != nil &&
					!errors.Is(err, ErrAlreadyTerminal) {
					closeErr = err
				}
			}
			for i := 0; i < 10; i++ {
				time.Sleep(time.Millisecond)
				m.attemptsMu.Lock()
				active = len(m.activeAttempts)
				m.attemptsMu.Unlock()
				if active == 0 {
					break
				}
			}
			if active != 0 && closeErr == nil {
				closeErr = ctx.Err()
			}
			goto closed
		}
	}

closed:
	for _, attempt := range closingAttempts {
		select {
		case attemptErr := <-attempt.done:
			if attemptErr != nil &&
				!errors.Is(attemptErr, ErrCheckpointUnavailable) &&
				closeErr == nil {
				closeErr = attemptErr
			}
		default:
		}
	}
	return closeErr
}

func (m *Manager) closedError() error {
	return fmt.Errorf("the background task manager has shut down and is no longer accepting new tasks. " +
		"Do not retry this; finish using any results you already have")
}

const canceledError = "task was canceled"

func cloneTask(t *Task) *Task {
	if t == nil {
		return nil
	}
	clone := *t
	clone.Spec = cloneSpec(t.Spec)
	clone.Checkpoint = cloneBytes(t.Checkpoint)
	clone.ResultData = cloneBytes(t.ResultData)
	clone.PendingResume = cloneBytes(t.PendingResume)
	clone.CancelRequestedAt = cloneTime(t.CancelRequestedAt)
	clone.DoneAt = cloneTime(t.DoneAt)
	return &clone
}
