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

	"github.com/cloudwego/eino/internal/safe"
)

// ControlKind identifies a Manager control signal sent to an executor.
type ControlKind string

const (
	// ControlStop asks the executor to stop as soon as practical.
	ControlStop ControlKind = "stop"
	// ControlDrain asks the executor to checkpoint and suspend if possible.
	ControlDrain ControlKind = "drain"
	// ControlTimeout asks the executor to fail with the supplied deterministic reason.
	ControlTimeout ControlKind = "timeout"
)

// ControlRequest carries a Manager control signal to an executor.
type ControlRequest struct {
	Kind   ControlKind
	Reason string
}

// ExecutionResult describes the lifecycle outcome returned by an executor.
type ExecutionResult struct {
	Status     Status
	Checkpoint []byte
	Data       []byte
	Error      string
}

// ExecutionRuntime exposes attempt-scoped coordination capabilities to an executor.
type ExecutionRuntime interface {
	TaskID() string
	Controls() <-chan ControlRequest
	Backgrounded() <-chan struct{}
	ReportOutputFailure(context.Context, string) error
}

// Executor reconstructs and runs durable work from a task Spec.
type Executor interface {
	Key() string
	ValidateSpec(Spec) error
	ValidateExecution(context.Context, *Task) error
	ValidateCheckpoint(context.Context, Spec, []byte) error
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
	if executor == nil || executor.Key() == "" {
		return errors.New("backgroundtask: executor and non-empty key are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.executors[executor.Key()]; ok {
		return fmt.Errorf("%w: executor %q", ErrAlreadyExists, executor.Key())
	}
	r.executors[executor.Key()] = executor
	return nil
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
	done          chan error
}

type taskRuntime struct {
	mu             sync.Mutex
	controlMu      sync.Mutex
	store          Store
	taskID         string
	version        int64
	controls       chan ControlRequest
	backgrounded   chan struct{}
	backgroundOnce sync.Once
	poison         error
	canceling      bool
}

var errHeartbeatStopped = errors.New("backgroundtask: heartbeat stopped")

func newTaskRuntime(store Store, taskID string, version int64) *taskRuntime {
	return &taskRuntime{
		store: store, taskID: taskID, version: version,
		controls: make(chan ControlRequest, 1), backgrounded: make(chan struct{}),
	}
}

func (r *taskRuntime) TaskID() string { return r.taskID }

func (r *taskRuntime) Controls() <-chan ControlRequest { return r.controls }

func (r *taskRuntime) Backgrounded() <-chan struct{} { return r.backgrounded }

func (r *taskRuntime) markBackgrounded() {
	r.backgroundOnce.Do(func() { close(r.backgrounded) })
}

func (r *taskRuntime) requestControl(kind ControlKind) {
	r.requestControlWithReason(kind, "")
}

func (r *taskRuntime) requestControlWithReason(kind ControlKind, reason string) {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	if kind == ControlStop {
		select {
		case <-r.controls:
		default:
		}
	}
	select {
	case r.controls <- ControlRequest{Kind: kind, Reason: reason}:
	default:
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
	if r.canceling {
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
	if task.Status != StatusCanceling || task.CancelRequestedAt == nil ||
		task.Version != r.version+1 {
		r.poison = ErrLeaseLost
		return r.poison
	}
	r.version = task.Version
	r.canceling = true
	r.requestControl(ControlStop)
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
	task, err := r.commitResult(ctx, result)
	if err != nil {
		r.poison = err
		return nil, err
	}
	r.version = task.Version
	return task, nil
}

func (r *taskRuntime) commitResult(ctx context.Context, result *ExecutionResult) (*Task, error) {
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
			TaskID: r.taskID, ExpectedVersion: r.version,
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

func (m *Manager) Store() Store { return m.store }

func (m *Manager) Executors() *ExecutorRegistry { return m.executors }

// AllocateTaskIDRequest describes the task category used by the default ID generator.
type AllocateTaskIDRequest struct {
	Kind string
}

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
	task, err := m.store.Create(ctx, &CreateTaskRequest{Spec: spec})
	if err != nil {
		return nil, err
	}
	m.submittedMu.Lock()
	m.submitted[task.Spec.ID] = struct{}{}
	m.submittedMu.Unlock()
	return task, nil
}

func (m *Manager) GetTask(ctx context.Context, taskID string) (*Task, error) {
	return m.store.Get(ctx, taskID)
}

// ListPending is the read-only dispatch boundary. A worker may select and
// dispatch a task ID from this result; only Execute performs start authorization.
func (m *Manager) ListPending(ctx context.Context, req *ListPendingRequest) (*ListPendingResult, error) {
	return m.store.ListPending(ctx, req)
}

func (m *Manager) WaitTask(ctx context.Context, req *WaitTaskRequest) (*Task, error) {
	return m.store.Wait(ctx, req)
}

func (m *Manager) RequestCancel(ctx context.Context, taskID string) (*Task, error) {
	m.attemptsMu.Lock()
	attempt := m.activeAttempts[taskID]
	m.attemptsMu.Unlock()

	task, err := m.store.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Spec.ExecutorKey == processLocalExecutorKey && task.Status != StatusCanceling {
		return m.cancelProcessLocal(ctx, task, attempt)
	}

	if attempt != nil && attempt.runtime != nil {
		attempt.runtime.mu.Lock()
		defer attempt.runtime.mu.Unlock()
	}

	var result *Task
	for retry := 0; ; retry++ {
		task, getErr := m.store.Get(ctx, taskID)
		if getErr != nil {
			return nil, getErr
		}
		result, err = m.store.RequestCancel(ctx, &RequestCancelRequest{
			TaskID: taskID, ExpectedVersion: task.Version,
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
	if result.Status == StatusCanceling && attempt != nil && attempt.runtime != nil &&
		!attempt.runtime.canceling {
		if err = attempt.runtime.reconcileCancellationLocked(ctx); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (m *Manager) cancelProcessLocal(
	ctx context.Context,
	task *Task,
	attempt *activeAttempt,
) (*Task, error) {
	if terminalStatus(task.Status) {
		return nil, ErrAlreadyTerminal
	}
	if task.Status != StatusRunning {
		return nil, ErrIllegalTransition
	}
	if attempt == nil || attempt.runtime == nil {
		return m.store.Cancel(ctx, &CancelTaskRequest{
			TaskID: task.Spec.ID, ExpectedVersion: task.Version,
		})
	}

	attempt.runtime.requestControl(ControlStop)
	select {
	case attemptErr := <-attempt.done:
		if attemptErr != nil {
			return nil, attemptErr
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	result, err := m.store.Get(ctx, task.Spec.ID)
	if err != nil {
		return nil, err
	}
	if result.Status != StatusCanceled {
		if terminalStatus(result.Status) {
			return nil, ErrAlreadyTerminal
		}
		return nil, ErrIllegalTransition
	}
	return result, nil
}

func (m *Manager) ResumeTask(ctx context.Context, req *ResumeTaskRequest) (*Task, error) {
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
	return m.store.Resume(ctx, &ResumeTaskRequest{
		TaskID: req.TaskID, ExpectedVersion: req.ExpectedVersion, Data: normalized,
	})
}

func (m *Manager) Execute(ctx context.Context, taskID string) error {
	return m.execute(ctx, taskID, nil)
}

func (m *Manager) execute(
	ctx context.Context,
	taskID string,
	onStarted func(*Task, *taskRuntime) error,
) (returnErr error) {
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
	attempt := &activeAttempt{done: make(chan error, 1)}
	m.activeAttempts[taskID] = attempt
	m.attemptsMu.Unlock()
	m.mu.Unlock()
	defer func() {
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
	runtime := newTaskRuntime(m.store, taskID, started.Version)
	runCtx, cancel := context.WithCancel(ctx)
	m.attemptsMu.Lock()
	attempt.cancel = cancel
	attempt.runtime = runtime
	attempt.supportsDrain = executor.SupportsDrain()
	m.attemptsMu.Unlock()
	defer cancel()
	if onStarted != nil {
		if err = onStarted(cloneTask(started), runtime); err != nil {
			return err
		}
	}

	heartbeatDone := make(chan struct{})
	heartbeatStop := make(chan struct{})
	go m.heartbeat(runCtx, cancel, runtime, heartbeatStop, heartbeatDone)

	result, executeErr := m.executeClaim(runCtx, executor, started, runtime)
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
	committed, err := runtime.commit(detachedCtx{parent: ctx}, result)
	if err == nil {
		m.sendTaskEvent(committed, eventTypeForState(committed.Status))
	}
	return err
}

func (m *Manager) executeStarted(
	ctx context.Context,
	started *Task,
	executor Executor,
	runtime *taskRuntime,
	onStarted func(*Task, *taskRuntime) error,
) (returnErr error) {
	taskID := started.Spec.ID
	runCtx, cancel := context.WithCancel(ctx)
	m.attemptsMu.Lock()
	if _, exists := m.activeAttempts[taskID]; exists {
		m.attemptsMu.Unlock()
		cancel()
		return errors.New("backgroundtask: task is already executing in this manager")
	}
	attempt := &activeAttempt{
		cancel: cancel, runtime: runtime, supportsDrain: executor.SupportsDrain(),
		done: make(chan error, 1),
	}
	m.activeAttempts[taskID] = attempt
	m.attemptsMu.Unlock()
	defer func() {
		cancel()
		attempt.done <- returnErr
		close(attempt.done)
		m.attemptsMu.Lock()
		delete(m.activeAttempts, taskID)
		m.attemptsMu.Unlock()
	}()

	if onStarted != nil {
		if err := onStarted(cloneTask(started), runtime); err != nil {
			return err
		}
	}
	heartbeatDone := make(chan struct{})
	heartbeatStop := make(chan struct{})
	go m.heartbeat(runCtx, cancel, runtime, heartbeatStop, heartbeatDone)

	result, executeErr := m.executeClaim(runCtx, executor, started, runtime)
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
	committed, err := runtime.commit(detachedCtx{parent: ctx}, result)
	if err == nil {
		m.sendTaskEvent(committed, eventTypeForState(committed.Status))
	}
	return err
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
	executionTask := cloneTask(claimed)
	if len(executionTask.Checkpoint) > 0 {
		if checkpointErr := executor.ValidateCheckpoint(
			ctx, cloneSpec(executionTask.Spec), cloneBytes(executionTask.Checkpoint),
		); checkpointErr != nil {
			executionTask.Checkpoint = nil
			executionTask.PendingResume = nil
		}
	}
	return executor.Execute(ctx, executionTask, runtime)
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
