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
// Manager coordinates TaskStore-backed submission, execution, and control. It
// is deliberately non-generic so one instance can serve heterogeneous executor
// domains under one task-ID space.
//
// TaskEvent is append-only progress. Spec.OutputFile and Task.OutputFileErr
// describe an optional derived transcript projection; transcript failure never
// changes authoritative lifecycle status or replaces terminal ResultData.
//
// Persistence providers should run the reusable suites in
// adk/backgroundtask/storetest before deployment.
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
	// It is not claimable until Manager.ReleaseSuspension returns it to pending.
	StatusSuspended Status = "suspended"
	// StatusCompleted indicates the task finished successfully.
	StatusCompleted Status = "completed"
	// StatusFailed indicates the task terminated with an error.
	StatusFailed Status = "failed"
	// StatusCanceled indicates the task acknowledged an external stop request.
	StatusCanceled Status = "canceled"
)

// Task represents one independently owned snapshot. Providers and callers must
// deep-copy mutable slices and time pointers when snapshots cross their
// boundary; mutating a returned Task must never alter persisted state.
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
	// PendingResume is the durable resume command for the current checkpoint.
	// Retry-capable attempt loss or yield preserves it for idempotent replay. A
	// subsequent wait-input or suspended checkpoint, or a terminal transition,
	// consumes it.
	PendingResume []byte
	// Version is the CAS version of this durable record.
	Version int64
	// Attempt counts successful claims.
	Attempt int64
	// CancelRequestedAt records durable explicit stop intent while Status remains
	// StatusRunning until the active attempt acknowledges it as StatusCanceled.
	CancelRequestedAt *time.Time
	// CancelReason is the optional first-write reason accompanying durable stop
	// intent. It becomes ResultError when the task reaches StatusCanceled.
	CancelReason string
	// CreatedAt is the TaskStore-assigned creation time.
	CreatedAt time.Time
	// UpdatedAt is the TaskStore mutation time.
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
	// Tasks is the authoritative task lifecycle provider. When nil, New installs
	// an in-memory reference provider. Manager also discovers the optional
	// NotificationWriter capability from this provider.
	Tasks TaskStore
	// TaskEvents persists append-only progress in the same task namespace and
	// must fence appends against the active attempt authorized by Tasks. When
	// nil, New reuses Tasks when it also implements TaskEventStore. If both are
	// nil, the same in-memory reference provider supplies both capabilities.
	TaskEvents TaskEventStore
	// Executors resolves serialized task intent to local implementations.
	Executors *ExecutorRegistry
	// SendTaskCreatedEvent emits a TaskCreated timeline event after a task is
	// durably created. It may be called concurrently. Tasks without a parent
	// SessionID do not emit this event. Use TaskCreatedSessionEventSender so the
	// active Runner assigns and persists the event in causal turn order.
	SendTaskCreatedEvent func(context.Context, *Task) error
	// IDGen, when set, decides the full ID of every task created by this Manager.
	// If nil, Manager uses its default task-type-prefixed Base64URL ID.
	//
	// IDGen may be called concurrently by task submitters. It
	// must return a non-empty ID. The returned ID must be unique among this
	// Manager's registered tasks; a duplicate fails task creation.
	IDGen IDGenerator
}

type closeOptions struct {
	drainReason string
}

// CloseOption configures Manager shutdown.
type CloseOption func(*closeOptions)

// WithDrainReason attaches an optional advisory reason to drain controls sent
// while closing a Manager. The reason is not persisted as terminal task state.
func WithDrainReason(reason string) CloseOption {
	return func(options *closeOptions) {
		options.drainReason = reason
	}
}

type requestCancelOptions struct {
	reason string
}

// RequestCancelOption configures durable cancellation intent.
type RequestCancelOption func(*requestCancelOptions)

// WithCancellationReason records an optional durable reason for stopping a
// task. The first cancellation request wins.
func WithCancellationReason(reason string) RequestCancelOption {
	return func(options *requestCancelOptions) {
		options.reason = reason
	}
}

// Manager owns TaskStore-backed lifecycle and worker coordination.
type Manager struct {
	tasks                TaskStore
	taskEvents           TaskEventStore
	notificationWriter   NotificationWriter
	executors            *ExecutorRegistry
	heartbeatEvery       time.Duration
	attemptsMu           sync.Mutex
	activeAttempts       map[string]*activeAttempt
	mu                   sync.Mutex
	closed               bool
	idGen                IDGenerator
	sendTaskCreatedEvent func(context.Context, *Task) error
}

// New creates a Manager. A nil Config installs the in-memory reference stores
// and a new executor registry. When Tasks is supplied without TaskEvents, Tasks
// must also implement TaskEventStore. The context is reserved for constructor
// symmetry; Manager does not retain it or derive task lifetime from it.
func New(_ context.Context, conf *Config) (*Manager, error) {
	defaults := NewInMemoryStore(nil)
	m := &Manager{
		heartbeatEvery: 10 * time.Second,
		activeAttempts: make(map[string]*activeAttempt),
		tasks:          defaults,
		taskEvents:     defaults,
	}
	m.executors = NewExecutorRegistry()
	if conf != nil {
		if conf.Tasks != nil {
			m.tasks = conf.Tasks
			if conf.TaskEvents == nil {
				if events, ok := conf.Tasks.(TaskEventStore); ok {
					m.taskEvents = events
				} else {
					return nil, errors.New(
						"backgroundtask: task event store is required when task store does not implement TaskEventStore",
					)
				}
			}
		}
		if conf.TaskEvents != nil {
			m.taskEvents = conf.TaskEvents
		}
		if conf.Executors != nil {
			m.executors = conf.Executors
		}
		m.sendTaskCreatedEvent = conf.SendTaskCreatedEvent
		m.idGen = conf.IDGen
	}
	m.notificationWriter, _ = m.tasks.(NotificationWriter)
	return m, nil
}

// Close performs bounded graceful shutdown. When any attempt is active, ctx must
// have a deadline or Close returns ErrCloseDeadlineRequired without closing the
// Manager. Drainable attempts receive ControlDrain and may suspend or yield
// according to their executor contract; non-drainable attempts may finish until
// the deadline and are then durably canceled. Deadline expiry cannot force an
// uncooperative executor to return; Manager remains closed to new submissions,
// while read and cancellation methods remain available.
func (m *Manager) Close(ctx context.Context, options ...CloseOption) error {
	closeConfig := closeOptions{}
	for _, option := range options {
		if option != nil {
			option(&closeConfig)
		}
	}
	if len(closeConfig.drainReason) > 4096 {
		return errors.New("backgroundtask: drain reason exceeds 4096 bytes")
	}
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
			if !attempt.drainOnReady {
				attempt.drainOnReady = true
				attempt.drainReason = closeConfig.drainReason
			}
			if attempt.supportsDrain && attempt.runtime != nil {
				attempt.runtime.requestControlWithReason(
					ControlDrain,
					attempt.drainReason,
				)
			}
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
				!errors.Is(attemptErr, ErrDrainCheckpointUnavailable) &&
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
