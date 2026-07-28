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
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/internal/safe"
)

type ExecutionRequest struct {
	Task           Spec
	Attempt        int64
	Checkpoint     *CheckpointRef
	ResumeData     []byte
	ResumeEncoding string
}

type ValidateResumeRequest struct {
	Task           Spec
	Checkpoint     *CheckpointRef
	ResumeData     []byte
	ResumeEncoding string
}

type ValidateResumeResult struct {
	NormalizedData     []byte
	NormalizedEncoding string
}

type ReportUpdateRequest struct {
	Kind     UpdateKind
	Progress *Progress
	Payload  *UpdatePayload
}

type ControlKind string

const (
	ControlStop  ControlKind = "stop"
	ControlDrain ControlKind = "drain"
)

type ControlRequest struct {
	Kind ControlKind
}

type OutcomeKind string

const (
	OutcomeCompleted    OutcomeKind = "completed"
	OutcomeWaitingInput OutcomeKind = "waiting_input"
	OutcomeSuspended    OutcomeKind = "suspended"
	OutcomeCanceled     OutcomeKind = "canceled"
	OutcomeFailed       OutcomeKind = "failed"
)

type Outcome struct {
	Kind           OutcomeKind
	Result         *ResultRef
	Checkpoint     *CheckpointRef
	InputRequest   *UpdatePayload
	TerminalReason string
	Err            error
}

type Runtime interface {
	ReportUpdate(context.Context, *ReportUpdateRequest) error
	ReportCheckpoint(context.Context, CheckpointRef) error
	Controls() <-chan ControlRequest
}

type Executor interface {
	Key() string
	Validate(Spec) error
	ValidateResume(context.Context, *ValidateResumeRequest) (*ValidateResumeResult, error)
	Execute(context.Context, ExecutionRequest, Runtime) Outcome
}

type checkpointValidator interface {
	ValidateCheckpoint(Spec, *CheckpointRef) error
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
			if capability.ExecutorKey != executor.Key() || capability.PayloadVersion == "" {
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
		// Custom executors without explicit capability metadata remain subject to
		// Validate before claim.
		result = append(result, ExecutorCapability{ExecutorKey: key, PayloadVersion: "*"})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ExecutorKey == result[j].ExecutorKey {
			return result[i].PayloadVersion < result[j].PayloadVersion
		}
		return result[i].ExecutorKey < result[j].ExecutorKey
	})
	return result
}

type workerExecution struct {
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

func (r *taskRuntime) ReportUpdate(ctx context.Context, req *ReportUpdateRequest) error {
	if req == nil {
		return errors.New("backgroundtask: update request is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	result, err := r.store.AppendUpdate(ctx, &AppendTaskUpdateRequest{
		Lease: r.lease, Kind: req.Kind, Progress: req.Progress, Payload: req.Payload,
	})
	if err != nil {
		r.poison = err
		return err
	}
	r.lease.ExpectedVersion = result.Task.TransitionVersion
	return nil
}

func (r *taskRuntime) ReportCheckpoint(ctx context.Context, checkpoint CheckpointRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	result, err := r.store.Commit(ctx, &CommitTaskRequest{
		Lease:    r.lease,
		Mutation: TaskMutation{ToStatus: StatusRunning, Checkpoint: &checkpoint},
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
		task.LeaseOwner != r.lease.WorkerID || task.LeaseGeneration != r.lease.Generation ||
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

func (r *taskRuntime) commit(ctx context.Context, mutation TaskMutation) (*Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return nil, r.poison
	}
	result, err := r.store.Commit(ctx, &CommitTaskRequest{Lease: r.lease, Mutation: mutation})
	if err != nil {
		r.poison = err
		return nil, err
	}
	r.lease.ExpectedVersion = result.Task.TransitionVersion
	return result.Task, nil
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

func (m *Manager) ListTaskUpdates(ctx context.Context, req *ListTaskUpdatesRequest) (*ListTaskUpdatesResult, error) {
	return m.store.ListUpdates(ctx, req)
}

// ListClaimable is the read-only scheduler boundary. A scheduler may select and
// dispatch a task ID from this result; only Execute performs claim and invocation.
func (m *Manager) ListClaimable(ctx context.Context, req *ListClaimableRequest) (*ListClaimableResult, error) {
	return m.store.ListClaimable(ctx, req)
}

func (m *Manager) WaitTask(ctx context.Context, req *WaitTaskRequest) (*Task, error) {
	return m.store.Wait(ctx, req)
}

func (m *Manager) WaitTaskUpdates(ctx context.Context, req *WaitTaskUpdatesRequest) (*ListTaskUpdatesResult, error) {
	return m.store.WaitUpdates(ctx, req)
}

func (m *Manager) RequestCancel(ctx context.Context, taskID string) (*Task, error) {
	m.workersMu.Lock()
	worker := m.workers[taskID]
	m.workersMu.Unlock()
	if worker != nil {
		worker.runtime.mu.Lock()
		defer worker.runtime.mu.Unlock()
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
	if result.Task.Status == StatusCanceling && worker != nil && !worker.runtime.canceling {
		if err = worker.runtime.reconcileCancellationLocked(ctx); err != nil {
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
	executor, ok := m.executors.Resolve(task.Spec.ExecutorKey)
	if !ok {
		return nil, fmt.Errorf("backgroundtask: executor %q is unavailable", task.Spec.ExecutorKey)
	}
	normalized, err := executor.ValidateResume(ctx, &ValidateResumeRequest{
		Task: task.Spec, Checkpoint: task.Checkpoint,
		ResumeData: req.ResumeData, ResumeEncoding: req.ResumeEncoding,
	})
	if err != nil {
		return nil, err
	}
	if normalized == nil {
		return nil, errors.New("backgroundtask: executor returned nil resume validation result")
	}
	return m.store.Resume(ctx, &ResumeTaskRequest{
		TaskID: req.TaskID, ExpectedVersion: req.ExpectedVersion,
		ResumeData: normalized.NormalizedData, ResumeEncoding: normalized.NormalizedEncoding,
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
	m.workersMu.Lock()
	if _, exists := m.workers[taskID]; exists {
		m.workersMu.Unlock()
		m.mu.Unlock()
		return errors.New("backgroundtask: task is already executing in this manager")
	}
	m.workers[taskID] = nil
	m.workersMu.Unlock()
	m.mu.Unlock()
	defer func() {
		m.workersMu.Lock()
		delete(m.workers, taskID)
		m.workersMu.Unlock()
	}()

	task, err := m.store.Get(ctx, taskID)
	if err != nil {
		return err
	}
	executor, ok := m.executors.Resolve(task.Spec.ExecutorKey)
	if !ok {
		return fmt.Errorf("backgroundtask: executor %q is unavailable", task.Spec.ExecutorKey)
	}
	if err = executor.Validate(task.Spec); err != nil {
		return err
	}
	leaseDuration := m.leaseDuration
	claim, err := m.store.Claim(ctx, &ClaimTaskRequest{
		TaskID: taskID, ExpectedVersion: task.TransitionVersion,
		WorkerID: m.workerID, LeaseDuration: leaseDuration,
	})
	if err != nil {
		return err
	}
	runtime := newTaskRuntime(m.store, claim.Lease)
	runCtx, cancel := context.WithCancel(ctx)
	m.workersMu.Lock()
	m.workers[taskID] = &workerExecution{cancel: cancel, runtime: runtime}
	m.workersMu.Unlock()
	defer cancel()

	heartbeatDone := make(chan struct{})
	heartbeatStop := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		interval := leaseDuration / 3
		if interval <= 0 {
			interval = time.Nanosecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if renewErr := runtime.renew(runCtx, leaseDuration); renewErr != nil {
					if errors.Is(renewErr, errLeaseRenewalStopped) {
						return
					}
					cancel()
					return
				}
			case <-runCtx.Done():
				return
			case <-heartbeatStop:
				return
			}
		}
	}()

	outcome := func() (out Outcome) {
		defer func() {
			if p := recover(); p != nil {
				out = Outcome{Kind: OutcomeFailed, Err: safe.NewPanicErr(p, debug.Stack())}
			}
		}()
		checkpoint := claim.Task.Checkpoint
		resumeData := cloneBytes(claim.Task.ResumeData)
		resumeEncoding := claim.Task.ResumeEncoding
		if validator, ok := executor.(checkpointValidator); ok && checkpoint != nil {
			if checkpointErr := validator.ValidateCheckpoint(claim.Task.Spec, checkpoint); checkpointErr != nil {
				if claim.Task.Spec.Recovery.OnMissingCheckpoint != RecoveryRestartFromSpec {
					return Outcome{Kind: OutcomeFailed, Err: checkpointErr}
				}
				checkpoint = nil
				resumeData = nil
				resumeEncoding = ""
			}
		}
		return executor.Execute(runCtx, ExecutionRequest{
			Task: claim.Task.Spec, Attempt: claim.Task.Attempt,
			Checkpoint:     checkpoint,
			ResumeData:     resumeData,
			ResumeEncoding: resumeEncoding,
		}, runtime)
	}()
	close(heartbeatStop)
	<-heartbeatDone

	if outcome.Kind == OutcomeFailed &&
		errors.Is(outcome.Err, ErrCheckpointUnavailable) &&
		claim.Task.Spec.Recovery.OnLeaseExpired != RecoveryFail {
		// The attempt is no longer renewed. Store expiry applies MaxAttempts,
		// checkpoint availability, and OnMissingCheckpoint atomically.
		return outcome.Err
	}
	mutation := mutationForOutcome(outcome)
	_, err = runtime.commit(detachedCtx{parent: ctx}, mutation)
	return err
}

func mutationForOutcome(out Outcome) TaskMutation {
	reason := out.TerminalReason
	if reason == "" && out.Err != nil {
		reason = out.Err.Error()
	}
	switch out.Kind {
	case OutcomeCompleted:
		return TaskMutation{ToStatus: StatusCompleted, Result: out.Result}
	case OutcomeWaitingInput:
		return TaskMutation{ToStatus: StatusWaitingInput, Checkpoint: out.Checkpoint, InputRequest: out.InputRequest}
	case OutcomeSuspended:
		return TaskMutation{ToStatus: StatusSuspended, Checkpoint: out.Checkpoint}
	case OutcomeCanceled:
		return TaskMutation{ToStatus: StatusCanceled, TerminalReason: reason}
	default:
		return TaskMutation{ToStatus: StatusFailed, TerminalReason: reason}
	}
}
