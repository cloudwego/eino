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
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk/internal/taskcontrol"
	"github.com/cloudwego/eino/internal/safe"
)

// ControlKind identifies a Manager control signal sent to an executor.
type ControlKind string

const (
	defaultCanceledReason = "task was canceled"
	defaultTimeoutReason  = "background task timed out"

	// ControlStop asks the executor to stop as soon as practical. Reason is the
	// optional durable cancellation reason.
	ControlStop ControlKind = "stop"
	// ControlDrain asks the executor to checkpoint and suspend if possible.
	// Reason is optional advisory operational context.
	ControlDrain ControlKind = "drain"
	// ControlTimeout asks the executor to fail with a non-empty deterministic reason.
	ControlTimeout ControlKind = "timeout"
)

// ControlRequest carries a Manager control signal to an executor. For
// ControlStop, Reason is optional and sourced from durable cancellation intent.
// It is optional and advisory for ControlDrain, and always non-empty for
// ControlTimeout.
type ControlRequest struct {
	Kind   ControlKind
	Reason string
}

// ExecutionDirective is a non-lifecycle instruction returned by an Executor.
type ExecutionDirective string

const (
	// ExecutionDirectiveYield relinquishes a recoverable active attempt while
	// the logical operation continues outside the current Worker.
	ExecutionDirectiveYield ExecutionDirective = "yield"
)

// ExecutionResult describes the lifecycle outcome returned by an executor.
type ExecutionResult struct {
	Directive  ExecutionDirective
	Status     Status
	Checkpoint []byte
	Data       []byte
	Error      string
}

// ExecutionRuntime exposes attempt-scoped coordination capabilities to an executor.
type ExecutionRuntime interface {
	TaskID() string
	Controls() <-chan ControlRequest
	AppendTaskEvent(context.Context, string, []byte) (*AppendTaskEventResult, error)
	ReportOutputFailure(context.Context, string) error
}

// Executor reconstructs and runs durable work from a task Spec.
type Executor interface {
	Key() string
	LeaseExpiryPolicy() LeaseExpiryPolicy
	ValidateSpec(Spec) error
	ValidateExecution(context.Context, *Task) error
	ValidateResume(context.Context, Spec, []byte, []byte) ([]byte, error)
	SupportsDrain() bool
	Execute(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error)
}

// ExecutorRegistry resolves executors by ExecutorKey.
type ExecutorRegistry struct {
	mu        sync.RWMutex
	executors map[string]Executor
}

// NewExecutorRegistry creates an empty executor registry.
func NewExecutorRegistry() *ExecutorRegistry {
	return &ExecutorRegistry{executors: make(map[string]Executor)}
}

// Register adds an executor keyed by executor.Key().
func (r *ExecutorRegistry) Register(executor Executor) error {
	actual, loaded, err := r.loadOrRegister(executor)
	if err != nil {
		return err
	}
	if loaded {
		return fmt.Errorf("%w: executor %q", ErrAlreadyExists, actual.Key())
	}
	return nil
}

func (r *ExecutorRegistry) loadOrRegister(executor Executor) (Executor, bool, error) {
	if executor == nil {
		return nil, false, errors.New("backgroundtask: executor and non-empty key are required")
	}
	key := executor.Key()
	if key == "" {
		return nil, false, errors.New("backgroundtask: executor and non-empty key are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if actual, ok := r.executors[key]; ok {
		return actual, true, nil
	}
	r.executors[key] = executor
	return executor, false, nil
}

// Resolve returns the executor registered for key.
func (r *ExecutorRegistry) Resolve(key string) (Executor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	executor, ok := r.executors[key]
	return executor, ok
}

// Keys returns the registered executor keys.
func (r *ExecutorRegistry) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, 0, len(r.executors))
	for key := range r.executors {
		result = append(result, key)
	}
	return result
}

type activeAttempt struct {
	cancel        context.CancelFunc
	runtime       *taskRuntime
	supportsDrain bool
	ready         chan struct{}
	readyOnce     sync.Once
	done          chan error
}

func (a *activeAttempt) signalReady() {
	a.readyOnce.Do(func() { close(a.ready) })
}

type taskRuntime struct {
	mu              sync.Mutex
	controlMu       sync.Mutex
	store           Store
	taskID          string
	attempt         int64
	version         int64
	controls        chan ControlRequest
	poison          error
	cancelRequested bool
	cancelReason    string
}

// detachedCtx preserves values while detaching worker execution from the
// request context that dispatched it.
type detachedCtx struct{ parent context.Context }

func (detachedCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (detachedCtx) Done() <-chan struct{}       { return nil }
func (detachedCtx) Err() error                  { return nil }
func (c detachedCtx) Value(key any) any         { return c.parent.Value(key) }

var errHeartbeatStopped = errors.New("backgroundtask: heartbeat stopped")

func newTaskRuntime(store Store, taskID string, attempt, version int64) *taskRuntime {
	return &taskRuntime{
		store: store, taskID: taskID, attempt: attempt, version: version,
		controls: make(chan ControlRequest, 1),
	}
}

func (r *taskRuntime) TaskID() string { return r.taskID }

func (r *taskRuntime) Controls() <-chan ControlRequest { return r.controls }

func (r *taskRuntime) AppendTaskEvent(
	ctx context.Context,
	eventID string,
	data []byte,
) (*AppendTaskEventResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return nil, r.poison
	}
	if eventID == "" {
		eventID = uuid.NewString()
	}
	return r.store.AppendTaskEvent(ctx, &AppendTaskEventRequest{
		TaskID: r.taskID, Attempt: r.attempt, EventID: eventID, Data: cloneBytes(data),
	})
}

func (r *taskRuntime) requestControl(kind ControlKind) bool {
	return r.requestControlWithReason(kind, "")
}

func (r *taskRuntime) requestControlWithReason(kind ControlKind, reason string) bool {
	if kind == ControlTimeout && reason == "" {
		reason = defaultTimeoutReason
	}
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	request := ControlRequest{Kind: kind, Reason: reason}
	select {
	case queued := <-r.controls:
		if controlPriority(kind) > controlPriority(queued.Kind) {
			r.controls <- request
			return true
		}
		r.controls <- queued
		return queued == request
	default:
		r.controls <- request
		return true
	}
}

func controlPriority(kind ControlKind) int {
	switch kind {
	case ControlStop:
		return 3
	case ControlTimeout:
		return 2
	case ControlDrain:
		return 1
	default:
		return 0
	}
}

func (r *taskRuntime) ReportOutputFailure(ctx context.Context, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	task, err := r.store.ReportOutputFailure(ctx, &ReportOutputFailureRequest{
		TaskID: r.taskID, ExpectedVersion: r.version, Error: boundedError(errors.New(message)),
	})
	if err != nil {
		return err
	}
	r.version = task.Version
	return nil
}

func (r *taskRuntime) heartbeat(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	if r.cancelRequested {
		return errHeartbeatStopped
	}
	task, err := r.store.Heartbeat(ctx, &HeartbeatRequest{
		TaskID: r.taskID, ExpectedVersion: r.version,
	})
	if err != nil {
		if errors.Is(err, ErrVersionConflict) {
			if reconcileErr := r.reconcileCancellationLocked(ctx); reconcileErr != nil {
				return reconcileErr
			}
			return errHeartbeatStopped
		}
		r.poison = err
		return err
	}
	r.version = task.Version
	return nil
}

func (r *taskRuntime) reconcileCancellationLocked(ctx context.Context) error {
	task, err := r.store.Get(ctx, r.taskID)
	if err != nil {
		r.poison = err
		return err
	}
	if task.Status != StatusRunning || task.CancelRequestedAt == nil ||
		task.Version != r.version+1 {
		r.poison = ErrLeaseLost
		return r.poison
	}
	r.version = task.Version
	r.cancelRequested = true
	r.cancelReason = task.CancelReason
	r.requestControlWithReason(ControlStop, r.cancelReason)
	return nil
}

func (r *taskRuntime) commit(ctx context.Context, result *ExecutionResult) (*Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return nil, r.poison
	}
	if result == nil {
		return nil, errors.New("backgroundtask: executor returned nil result")
	}
	if r.cancelRequested {
		result = &ExecutionResult{Status: StatusCanceled, Error: r.cancelReason}
	}
	task, err := r.commitResult(ctx, result)
	if errors.Is(err, ErrVersionConflict) {
		if reconcileErr := r.reconcileCancellationLocked(ctx); reconcileErr != nil {
			return nil, reconcileErr
		}
		task, err = r.commitResult(ctx, &ExecutionResult{
			Status: StatusCanceled, Error: r.cancelReason,
		})
	}
	if err != nil {
		r.poison = err
		return nil, err
	}
	r.version = task.Version
	return task, nil
}

func (r *taskRuntime) commitResult(ctx context.Context, result *ExecutionResult) (*Task, error) {
	if result.Directive != "" {
		if result.Directive != ExecutionDirectiveYield || result.Status != "" ||
			len(result.Data) != 0 || result.Error != "" {
			return nil, fmt.Errorf("%w: conflicting executor directive and lifecycle result", ErrInvalidResult)
		}
		return r.store.Yield(ctx, &YieldTaskRequest{
			TaskID: r.taskID, ExpectedVersion: r.version,
			Checkpoint: cloneBytes(result.Checkpoint),
		})
	}
	switch result.Status {
	case StatusCompleted:
		return r.store.Complete(ctx, &CompleteTaskRequest{
			TaskID: r.taskID, ExpectedVersion: r.version, Data: cloneBytes(result.Data),
		})
	case StatusFailed:
		return r.store.Fail(ctx, &FailTaskRequest{
			TaskID: r.taskID, ExpectedVersion: r.version, Error: result.Error,
		})
	case StatusCanceled:
		return r.store.Cancel(ctx, &CancelTaskRequest{
			TaskID: r.taskID, ExpectedVersion: r.version, Reason: result.Error,
		})
	case StatusWaitingInput:
		return r.store.WaitInput(ctx, &WaitInputTaskRequest{
			TaskID: r.taskID, ExpectedVersion: r.version, Checkpoint: cloneBytes(result.Checkpoint),
		})
	case StatusSuspended:
		return r.store.Suspend(ctx, &SuspendTaskRequest{
			TaskID: r.taskID, ExpectedVersion: r.version, Checkpoint: cloneBytes(result.Checkpoint),
		})
	default:
		return nil, fmt.Errorf("%w: unsupported executor result status %q", ErrInvalidResult, result.Status)
	}
}

// AllocateTaskIDRequest describes the task category used by the default ID generator.
type AllocateTaskIDRequest struct {
	Kind string
}

// LoadOrRegisterExecutor atomically returns the executor registered under the
// candidate's key, registering the candidate when the key is not yet present.
func (m *Manager) LoadOrRegisterExecutor(executor Executor) (actual Executor, loaded bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false, m.closedError()
	}
	return m.executors.loadOrRegister(executor)
}

// AllocateTaskID allocates an opaque ID for a task category.
func (m *Manager) AllocateTaskID(ctx context.Context, request *AllocateTaskIDRequest) (string, error) {
	if request == nil {
		return "", errors.New("backgroundtask: allocate task id request is required")
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return "", m.closedError()
	}
	if m.idGen != nil {
		id, err := m.idGen(ctx, request)
		if err != nil {
			return "", fmt.Errorf("backgroundtask: task id generator: %w", err)
		}
		if id == "" {
			return "", errors.New("backgroundtask: task id generator returned empty id")
		}
		return id, nil
	}
	id, err := defaultTaskID(request.Kind)
	if err != nil {
		return "", fmt.Errorf("backgroundtask: generate task id: %w", err)
	}
	return id, nil
}

// Submit validates serialized intent and persists a pending task.
func (m *Manager) Submit(ctx context.Context, spec Spec) (*Task, error) {
	if spec.ID == "" {
		return nil, errors.New("backgroundtask: submit requires a pre-allocated task id")
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, m.closedError()
	}
	executor, ok := m.executors.Resolve(spec.ExecutorKey)
	if !ok {
		return nil, fmt.Errorf("backgroundtask: executor %q is unavailable", spec.ExecutorKey)
	}
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	if err := executor.ValidateSpec(cloneSpec(spec)); err != nil {
		return nil, fmt.Errorf("backgroundtask: validate spec: %w", err)
	}
	task, err := m.store.Create(ctx, &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: executor.LeaseExpiryPolicy(),
	})
	if err != nil {
		return nil, err
	}
	return task, nil
}

// Get returns the authoritative task snapshot.
func (m *Manager) Get(ctx context.Context, taskID string) (*Task, error) {
	return m.store.Get(ctx, taskID)
}

// ListPending is the read-only dispatch boundary. A worker may select and
// dispatch a task ID from this result; only Execute performs start authorization.
func (m *Manager) ListPending(ctx context.Context, req *ListPendingRequest) (*ListPendingResult, error) {
	return m.store.ListPending(ctx, req)
}

// WaitUpdate waits until a task advances beyond the requested version.
func (m *Manager) WaitUpdate(ctx context.Context, req *WaitUpdateRequest) (*Task, error) {
	return m.store.Wait(ctx, req)
}

// ReadRecentTaskEvents reads the newest bounded progress events in chronological order.
func (m *Manager) ReadRecentTaskEvents(
	ctx context.Context,
	req *ReadRecentTaskEventsRequest,
) (*ReadRecentTaskEventsResult, error) {
	return m.store.ReadRecentTaskEvents(ctx, req)
}

// RequestCancel records cancellation intent and signals a local active attempt.
// An optional reason is durable and first-write across repeated requests.
func (m *Manager) RequestCancel(
	ctx context.Context,
	taskID string,
	options ...RequestCancelOption,
) (*Task, error) {
	cancelConfig := requestCancelOptions{}
	for _, option := range options {
		if option != nil {
			option(&cancelConfig)
		}
	}
	if len(cancelConfig.reason) > 4096 {
		return nil, errors.New("backgroundtask: cancellation reason exceeds 4096 bytes")
	}

	m.attemptsMu.Lock()
	attempt := m.activeAttempts[taskID]
	m.attemptsMu.Unlock()

	var result *Task
	var err error
	for retry := 0; ; retry++ {
		task, getErr := m.store.Get(ctx, taskID)
		if getErr != nil {
			return nil, getErr
		}
		result, err = m.store.RequestCancel(ctx, &RequestCancelRequest{
			TaskID: taskID, ExpectedVersion: task.Version, Reason: cancelConfig.reason,
		})
		if !errors.Is(err, ErrVersionConflict) {
			break
		}
		if retry >= 7 {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if result.Status == StatusRunning && result.CancelRequestedAt != nil &&
		attempt != nil {
		select {
		case <-attempt.ready:
		case <-ctx.Done():
			return result, ctx.Err()
		}
		if attempt.runtime != nil {
			attempt.runtime.mu.Lock()
			if !attempt.runtime.cancelRequested {
				err = attempt.runtime.reconcileCancellationLocked(ctx)
			}
			attempt.runtime.mu.Unlock()
			if err != nil {
				return result, err
			}
		}
	}
	if result.LeaseExpiryPolicy == LeaseExpiryFail && result.Status == StatusRunning &&
		attempt != nil {
		select {
		case attemptErr := <-attempt.done:
			if attemptErr != nil {
				return nil, attemptErr
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		terminal, getErr := m.store.Get(ctx, taskID)
		if getErr != nil {
			return nil, getErr
		}
		if terminal.Status != StatusCanceled {
			if terminalStatus(terminal.Status) {
				return nil, ErrAlreadyTerminal
			}
			return nil, ErrIllegalTransition
		}
		return terminal, nil
	}
	return result, nil
}

// Resume validates and persists input for a task waiting on external input.
func (m *Manager) Resume(ctx context.Context, req *ResumeRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: resume request is required")
	}
	task, err := m.store.Get(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	if task.Version != req.ExpectedVersion {
		return nil, ErrVersionConflict
	}
	if task.Status != StatusWaitingInput {
		return nil, ErrIllegalTransition
	}
	executor, ok := m.executors.Resolve(task.Spec.ExecutorKey)
	if !ok {
		return nil, fmt.Errorf("backgroundtask: executor %q is unavailable", task.Spec.ExecutorKey)
	}
	normalized, err := executor.ValidateResume(
		ctx, cloneSpec(task.Spec), cloneBytes(task.Checkpoint), cloneBytes(req.Data),
	)
	if err != nil {
		return nil, err
	}
	return m.store.Resume(ctx, &ResumeRequest{
		TaskID: req.TaskID, ExpectedVersion: req.ExpectedVersion, Data: normalized,
	})
}

// Execute claims and runs one pending task attempt on the current worker.
func (m *Manager) Execute(ctx context.Context, taskID string) error {
	return m.execute(ctx, taskID)
}

func (m *Manager) execute(
	ctx context.Context,
	taskID string,
) (returnErr error) {
	timeoutController := taskcontrol.FromContext(ctx)
	if timeoutController != nil {
		defer timeoutController.Close()
	}
	if taskID == "" {
		return errors.New("backgroundtask: execute task id is required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return m.closedError()
	}
	m.attemptsMu.Lock()
	if _, exists := m.activeAttempts[taskID]; exists {
		m.attemptsMu.Unlock()
		m.mu.Unlock()
		return errors.New("backgroundtask: task is already executing in this manager")
	}
	attempt := &activeAttempt{ready: make(chan struct{}), done: make(chan error, 1)}
	m.activeAttempts[taskID] = attempt
	m.attemptsMu.Unlock()
	m.mu.Unlock()
	defer func() {
		attempt.signalReady()
		attempt.done <- returnErr
		close(attempt.done)
		m.attemptsMu.Lock()
		delete(m.activeAttempts, taskID)
		m.attemptsMu.Unlock()
	}()

	task, err := m.store.Get(ctx, taskID)
	if err != nil {
		return err
	}
	executor, ok := m.executors.Resolve(task.Spec.ExecutorKey)
	if !ok {
		return fmt.Errorf("backgroundtask: executor %q is unavailable", task.Spec.ExecutorKey)
	}
	if err = executor.ValidateSpec(cloneSpec(task.Spec)); err != nil {
		return fmt.Errorf("backgroundtask: validate spec: %w", err)
	}
	if err = executor.ValidateExecution(ctx, cloneTask(task)); err != nil {
		return fmt.Errorf("backgroundtask: validate execution: %w", err)
	}
	started, err := m.store.Start(ctx, &StartTaskRequest{
		TaskID: taskID, ExpectedVersion: task.Version,
	})
	if err != nil {
		return err
	}
	runtime := newTaskRuntime(m.store, taskID, started.Attempt, started.Version)
	if started.CancelRequestedAt != nil {
		runtime.cancelRequested = true
		runtime.cancelReason = started.CancelReason
		runtime.requestControlWithReason(ControlStop, runtime.cancelReason)
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.attemptsMu.Lock()
	attempt.cancel = cancel
	attempt.runtime = runtime
	attempt.supportsDrain = executor.SupportsDrain()
	attempt.signalReady()
	m.attemptsMu.Unlock()
	defer cancel()
	heartbeatDone := make(chan struct{})
	heartbeatStop := make(chan struct{})
	go m.heartbeat(runCtx, cancel, runtime, heartbeatStop, heartbeatDone)

	timeoutStop := make(chan struct{})
	timeoutDone := make(chan struct{})
	if timeoutController == nil {
		close(timeoutDone)
	} else {
		go serveTimeoutRequests(runtime, timeoutController, timeoutStop, timeoutDone)
	}
	result, executeErr := m.executeClaim(runCtx, executor, started, runtime)
	close(timeoutStop)
	<-timeoutDone
	if timeoutController != nil {
		timeoutController.Close()
	}
	close(heartbeatStop)
	<-heartbeatDone

	if errors.Is(executeErr, ErrCheckpointUnavailable) {
		return executeErr
	}
	if executeErr != nil {
		result = &ExecutionResult{Status: StatusFailed, Error: boundedError(executeErr)}
	} else if result == nil {
		result = &ExecutionResult{Status: StatusFailed, Error: "executor returned nil result"}
	}
	_, commitErr := runtime.commit(detachedCtx{parent: ctx}, result)
	return commitErr
}

func serveTimeoutRequests(
	runtime *taskRuntime,
	controller *taskcontrol.TimeoutController,
	stop <-chan struct{},
	done chan<- struct{},
) {
	defer close(done)
	for {
		select {
		case request := <-controller.Requests():
			if runtime.requestControlWithReason(ControlTimeout, request.Reason) {
				request.Complete(nil)
			} else {
				request.Complete(taskcontrol.ErrClosed)
			}
		case <-controller.Done():
			return
		case <-stop:
			return
		}
	}
}

func (m *Manager) heartbeat(
	ctx context.Context,
	cancel context.CancelFunc,
	runtime *taskRuntime,
	stop <-chan struct{},
	done chan<- struct{},
) {
	defer close(done)
	interval := m.heartbeatEvery
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := runtime.heartbeat(ctx); err != nil {
				if !errors.Is(err, errHeartbeatStopped) {
					cancel()
				}
				return
			}
		case <-ctx.Done():
			return
		case <-stop:
			return
		}
	}
}

func (m *Manager) executeClaim(
	ctx context.Context,
	executor Executor,
	claimed *Task,
	runtime *taskRuntime,
) (result *ExecutionResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			result = nil
			err = safe.NewPanicErr(p, debug.Stack())
		}
	}()
	return executor.Execute(ctx, cloneTask(claimed), runtime)
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	const max = 4096
	message := err.Error()
	if len(message) <= max {
		return message
	}
	return message[:max]
}
