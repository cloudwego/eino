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

type ControlKind string

const (
	ControlStop  ControlKind = "stop"
	ControlDrain ControlKind = "drain"
)

type ControlRequest struct {
	Kind ControlKind
}

type Runtime interface {
	ReportCheckpoint(context.Context, []byte) error
	Controls() <-chan ControlRequest
}

type ExecutionResult struct {
	Status     Status
	Checkpoint []byte
	Result     *Result
}

type Executor interface {
	Key() string
	Validate(Spec) error
	ValidateCheckpoint(context.Context, Spec, []byte) error
	ValidateResume(context.Context, Spec, []byte, []byte) ([]byte, error)
	Execute(context.Context, *Task, Runtime) (*ExecutionResult, error)
}

type executorCapabilityProvider interface {
	Capabilities() []ExecutorCapability
}

type ExecutorRegistry struct {
	mu        sync.RWMutex
	executors map[string]Executor
}

func NewExecutorRegistry() *ExecutorRegistry {
	return &ExecutorRegistry{executors: make(map[string]Executor)}
}

func (r *ExecutorRegistry) Register(executor Executor) error {
	if executor == nil || executor.Key() == "" {
		return errors.New("backgroundtask: executor and non-empty key are required")
	}
	if provider, ok := executor.(executorCapabilityProvider); ok {
		capabilities := provider.Capabilities()
		if len(capabilities) == 0 {
			return errors.New("backgroundtask: executor capabilities are empty")
		}
		for _, capability := range capabilities {
			if capability.ExecutorKey != executor.Key() {
				return errors.New("backgroundtask: executor capability does not match executor identity")
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.executors[executor.Key()]; ok {
		return fmt.Errorf("%w: executor %q", ErrAlreadyExists, executor.Key())
	}
	r.executors[executor.Key()] = executor
	return nil
}

func (r *ExecutorRegistry) Resolve(key string) (Executor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	executor, ok := r.executors[key]
	return executor, ok
}

func (r *ExecutorRegistry) Capabilities() []ExecutorCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ExecutorCapability, 0, len(r.executors))
	for key, executor := range r.executors {
		if provider, ok := executor.(executorCapabilityProvider); ok {
			result = append(result, provider.Capabilities()...)
			continue
		}
		result = append(result, ExecutorCapability{ExecutorKey: key})
	}
	return result
}

type activeAttempt struct {
	cancel  context.CancelFunc
	runtime *taskRuntime
}

type taskRuntime struct {
	mu        sync.Mutex
	controlMu sync.Mutex
	store     Store
	lease     LeaseToken
	controls  chan ControlRequest
	poison    error
	canceling bool
}

var errLeaseRenewalStopped = errors.New("backgroundtask: lease renewal stopped")

func newTaskRuntime(store Store, lease LeaseToken) *taskRuntime {
	return &taskRuntime{store: store, lease: lease, controls: make(chan ControlRequest, 1)}
}

func (r *taskRuntime) Controls() <-chan ControlRequest { return r.controls }

func (r *taskRuntime) requestControl(kind ControlKind) {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	if kind == ControlStop {
		select {
		case <-r.controls:
		default:
		}
	}
	select {
	case r.controls <- ControlRequest{Kind: kind}:
	default:
	}
}

func (r *taskRuntime) ReportCheckpoint(ctx context.Context, checkpoint []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	result, err := r.store.Commit(ctx, &CommitTaskRequest{
		Lease:      r.lease,
		Status:     StatusRunning,
		Checkpoint: cloneBytes(checkpoint),
	})
	if err != nil {
		r.poison = err
		return err
	}
	r.lease.ExpectedVersion = result.Task.TransitionVersion
	return nil
}

func (r *taskRuntime) renew(ctx context.Context, duration time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	if r.canceling {
		return errLeaseRenewalStopped
	}
	task, err := r.store.Renew(ctx, &RenewLeaseRequest{Lease: r.lease, LeaseDuration: duration})
	if err != nil {
		if errors.Is(err, ErrVersionConflict) {
			if reconcileErr := r.reconcileCancellationLocked(ctx); reconcileErr != nil {
				return reconcileErr
			}
			return errLeaseRenewalStopped
		}
		r.poison = err
		return err
	}
	r.lease.ExpectedVersion = task.TransitionVersion
	return nil
}

func (r *taskRuntime) reconcileCancellationLocked(ctx context.Context) error {
	task, err := r.store.Get(ctx, r.lease.TaskID)
	if err != nil {
		r.poison = err
		return err
	}
	if task.Status != StatusCanceling || task.CancelRequestedAt == nil ||
		task.LeaseOwner != r.lease.LeaseOwnerID || task.LeaseGeneration != r.lease.Generation ||
		task.CancelTransitionVersion != task.TransitionVersion ||
		task.CancelTransitionVersion != r.lease.ExpectedVersion+1 {
		r.poison = ErrLeaseLost
		return r.poison
	}
	r.lease.ExpectedVersion = task.TransitionVersion
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
	commit, err := r.store.Commit(ctx, &CommitTaskRequest{
		Lease: r.lease, Status: result.Status,
		Checkpoint: cloneBytes(result.Checkpoint), Result: cloneResult(result.Result),
	})
	if err != nil {
		r.poison = err
		return nil, err
	}
	r.lease.ExpectedVersion = commit.Task.TransitionVersion
	return commit.Task, nil
}

func (m *Manager) Store() Store { return m.store }

func (m *Manager) Executors() *ExecutorRegistry { return m.executors }

func (m *Manager) AllocateTaskID(ctx context.Context) (string, error) {
	input := &RunInput{}
	if m.idGen != nil {
		id, err := m.idGen(ctx, input)
		if err != nil {
			return "", fmt.Errorf("backgroundtask: task id generator: %w", err)
		}
		if id == "" {
			return "", errors.New("backgroundtask: task id generator returned empty id")
		}
		return id, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", m.closedError()
	}
	return base62(m.nextRawID()), nil
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
	if err := executor.Validate(cloneSpec(spec)); err != nil {
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

// ListClaimable is the read-only scheduler boundary. A scheduler may select and
// dispatch a task ID from this result; only Execute performs claim and invocation.
func (m *Manager) ListClaimable(ctx context.Context, req *ListClaimableRequest) (*ListClaimableResult, error) {
	return m.store.ListClaimable(ctx, req)
}

func (m *Manager) WaitTask(ctx context.Context, req *WaitTaskRequest) (*Task, error) {
	return m.store.Wait(ctx, req)
}

func (m *Manager) RequestCancel(ctx context.Context, taskID string) (*Task, error) {
	m.attemptsMu.Lock()
	attempt := m.activeAttempts[taskID]
	m.attemptsMu.Unlock()
	if attempt != nil {
		attempt.runtime.mu.Lock()
		defer attempt.runtime.mu.Unlock()
	}

	var result *RequestCancelResult
	var err error
	for attempt := 0; ; attempt++ {
		task, getErr := m.store.Get(ctx, taskID)
		if getErr != nil {
			return nil, getErr
		}
		result, err = m.store.RequestCancel(ctx, &RequestCancelRequest{
			TaskID: taskID, ExpectedVersion: task.TransitionVersion,
		})
		if !errors.Is(err, ErrVersionConflict) {
			break
		}
		if attempt >= 7 {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if result.Task.Status == StatusCanceling && attempt != nil && !attempt.runtime.canceling {
		if err = attempt.runtime.reconcileCancellationLocked(ctx); err != nil {
			return result.Task, err
		}
	}
	return result.Task, nil
}

func (m *Manager) ResumeTask(ctx context.Context, req *ResumeTaskRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: resume request is required")
	}
	task, err := m.store.Get(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	if task.TransitionVersion != req.ExpectedVersion {
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
	m.activeAttempts[taskID] = nil
	m.attemptsMu.Unlock()
	m.mu.Unlock()
	defer func() {
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
	claim, err := m.store.Claim(ctx, &ClaimTaskRequest{
		TaskID: taskID, ExpectedVersion: task.TransitionVersion,
		LeaseOwnerID: m.leaseOwnerID, LeaseDuration: m.leaseDuration,
	})
	if err != nil {
		return err
	}
	runtime := newTaskRuntime(m.store, claim.Lease)
	runCtx, cancel := context.WithCancel(ctx)
	m.attemptsMu.Lock()
	m.activeAttempts[taskID] = &activeAttempt{cancel: cancel, runtime: runtime}
	m.attemptsMu.Unlock()
	defer cancel()

	heartbeatDone := make(chan struct{})
	heartbeatStop := make(chan struct{})
	go m.renewLease(runCtx, cancel, runtime, heartbeatStop, heartbeatDone)

	result, executeErr := m.executeClaim(runCtx, executor, claim.Task, runtime)
	close(heartbeatStop)
	<-heartbeatDone

	if errors.Is(executeErr, ErrCheckpointUnavailable) {
		return executeErr
	}
	if executeErr != nil {
		result = &ExecutionResult{Status: StatusFailed, Result: &Result{Error: boundedError(executeErr)}}
	} else if result == nil {
		result = &ExecutionResult{Status: StatusFailed, Result: &Result{Error: "executor returned nil result"}}
	}
	_, err = runtime.commit(detachedCtx{parent: ctx}, result)
	return err
}

func (m *Manager) renewLease(
	ctx context.Context,
	cancel context.CancelFunc,
	runtime *taskRuntime,
	stop <-chan struct{},
	done chan<- struct{},
) {
	defer close(done)
	interval := m.leaseDuration / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := runtime.renew(ctx, m.leaseDuration); err != nil {
				if !errors.Is(err, errLeaseRenewalStopped) {
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
	runtime Runtime,
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
