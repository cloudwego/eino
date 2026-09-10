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

	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk/internal/taskcontrol"
	"github.com/cloudwego/eino/internal/core"
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
	// ControlDrain asks the executor to relinquish gracefully. The executor may
	// checkpoint and suspend or yield without a checkpoint according to its
	// recovery model. Reason is optional advisory operational context.
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

// ExecutionResult describes one legal executor outcome:
//   - Yield: DirectiveYield plus an optional Checkpoint; all lifecycle fields empty.
//   - Completed: StatusCompleted plus optional Data.
//   - Failed or Canceled: the corresponding status plus Error.
//   - WaitingInput or Suspended: the corresponding status plus Checkpoint.
//
// Fields from different variants must not be combined.
type ExecutionResult struct {
	Directive  ExecutionDirective
	Status     Status
	Checkpoint []byte
	Data       []byte
	Error      string
}

// ProgressEmission reports the stable identity and replay status of one
// executor progress event.
type ProgressEmission struct {
	EventID string
	// FirstEmission is false for an idempotent replay of an event already
	// accepted for this task.
	FirstEmission bool
}

// ExecutionRuntime exposes concurrency-safe, attempt-scoped capabilities.
// Storage fencing fields remain private to the runtime.
type ExecutionRuntime interface {
	// Controls returns a runtime-owned channel. Signals may be coalesced; the
	// executor must stop selecting it when the attempt context ends.
	Controls() <-chan ControlRequest
	// EmitProgress appends replayable progress. An empty event ID requests a
	// framework-generated stable ID. FirstEmission is false when the same ID and
	// bytes were already accepted for the task.
	EmitProgress(context.Context, string, []byte) (ProgressEmission, error)
	// ReportTranscriptFailure records the first non-lifecycle failure of the
	// optional derived transcript.
	ReportTranscriptFailure(context.Context, error) error
}

// StartCommitRuntime is an optional execution capability for atomically
// recording that an executor established its external operation. Manager
// runtimes implement it; keeping it separate preserves ExecutionRuntime source
// compatibility for custom executors and test doubles.
type StartCommitRuntime interface {
	CommitStart(context.Context, []byte) error
}

// LeaseGateRuntime is an optional execution capability that waits until the
// Manager has confirmed that the current attempt still owns a safe lease.
// Built-in agent tool dispatch observes this gate automatically. Custom
// executors should call it before starting a new external side effect. Calls
// return promptly when transient-heartbeat tolerance is disabled.
type LeaseGateRuntime interface {
	WaitForLease(context.Context) error
}

// Executor reconstructs and runs durable work from a task Spec.
type Executor interface {
	// Key is a stable persisted routing key.
	Key() string
	// LeaseExpiryPolicy is immutable for tasks created by this executor.
	LeaseExpiryPolicy() LeaseExpiryPolicy
	// ValidateSpec is repeatable, side-effect free, and runs before persistence.
	ValidateSpec(Spec) error
	// ValidateExecution performs side-effect-free validation immediately before
	// an attempt is claimed.
	ValidateExecution(context.Context, *Task) error
	// SupportsDrain reports whether Execute handles ControlDrain. When drain
	// cancellation takes effect, Execute returns a resumable suspended or yielded
	// result; a run that terminates first may preserve its own outcome.
	SupportsDrain() bool
	// Execute owns the attempt until it returns. It must observe ctx and runtime
	// controls and return exactly one legal ExecutionResult variant.
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
	actual, loaded, err := r.LoadOrRegister(executor)
	if err != nil {
		return err
	}
	if loaded {
		return fmt.Errorf("%w: executor %q", ErrAlreadyExists, actual.Key())
	}
	return nil
}

// LoadOrRegister atomically returns the executor registered under the
// candidate's key, registering the candidate when the key is not yet present.
func (r *ExecutorRegistry) LoadOrRegister(executor Executor) (Executor, bool, error) {
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
	sort.Strings(result)
	return result
}

type activeAttempt struct {
	cancel        context.CancelFunc
	runtime       *taskRuntime
	supportsDrain bool
	ready         chan struct{}
	readyOnce     sync.Once
	done          chan error
	drainOnReady  bool
	drainReason   string
}

func (a *activeAttempt) signalReady() {
	a.readyOnce.Do(func() { close(a.ready) })
}

type taskRuntime struct {
	mu                      sync.Mutex
	controlMu               sync.Mutex
	tasks                   TaskStore
	taskEvents              TaskEventStore
	notificationWriter      NotificationWriter
	taskID                  string
	attempt                 int64
	version                 int64
	controls                chan ControlRequest
	poison                  error
	cancelRequested         bool
	cancelReason            string
	stateChanged            chan struct{}
	heartbeatAbort          chan struct{}
	heartbeatAborted        bool
	versionWriteActive      bool
	heartbeatPending        bool
	heartbeatToken          uint64
	heartbeatSequence       uint64
	leaseUncertain          bool
	unconfirmedAt           time.Time
	leaseExpiresAt          time.Time
	leaseDuration           time.Duration
	leaseSafetyMargin       time.Duration
	tolerateHeartbeatErrors bool
}

// detachedCtx preserves values while detaching worker execution from the
// request context that dispatched it.
type detachedCtx struct{ parent context.Context }

type notifyParentContextKey struct{}

type notifyParentCallback func(context.Context, *NotifyParentRequest) error

func (detachedCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (detachedCtx) Done() <-chan struct{}       { return nil }
func (detachedCtx) Err() error                  { return nil }
func (c detachedCtx) Value(key any) any         { return c.parent.Value(key) }

var (
	errHeartbeatRetry   = errors.New("backgroundtask: heartbeat retry at regular interval")
	errHeartbeatStopped = errors.New("backgroundtask: heartbeat stopped")
)

type taskRuntimeLeaseConfig struct {
	confirmedAt             time.Time
	duration                time.Duration
	safetyMargin            time.Duration
	tolerateHeartbeatErrors bool
}

type taskRuntimeConfig struct {
	tasks              TaskStore
	taskEvents         TaskEventStore
	notificationWriter NotificationWriter
	taskID             string
	attempt            int64
	version            int64
	lease              taskRuntimeLeaseConfig
}

func newTaskRuntime(
	tasks TaskStore,
	taskEvents TaskEventStore,
	taskID string,
	attempt, version int64,
	notificationWriter NotificationWriter,
) *taskRuntime {
	return newTaskRuntimeWithConfig(taskRuntimeConfig{
		tasks: tasks, taskEvents: taskEvents,
		notificationWriter: notificationWriter,
		taskID:             taskID, attempt: attempt, version: version,
	})
}

func newTaskRuntimeWithConfig(config taskRuntimeConfig) *taskRuntime {
	runtime := &taskRuntime{
		tasks: config.tasks, taskEvents: config.taskEvents,
		notificationWriter: config.notificationWriter,
		taskID:             config.taskID,
		attempt:            config.attempt,
		version:            config.version,
		controls:           make(chan ControlRequest, 1),
		stateChanged:       make(chan struct{}),
		heartbeatAbort:     make(chan struct{}),
	}
	runtime.leaseDuration = config.lease.duration
	runtime.leaseSafetyMargin = config.lease.safetyMargin
	runtime.tolerateHeartbeatErrors = config.lease.tolerateHeartbeatErrors
	if config.lease.tolerateHeartbeatErrors {
		runtime.leaseExpiresAt = config.lease.confirmedAt.Add(config.lease.duration)
	}
	return runtime
}

func (r *taskRuntime) Controls() <-chan ControlRequest { return r.controls }

func (r *taskRuntime) signalStateChangedLocked() {
	close(r.stateChanged)
	r.stateChanged = make(chan struct{})
}

func (r *taskRuntime) abortHeartbeatLocked() {
	if !r.heartbeatAborted {
		close(r.heartbeatAbort)
		r.heartbeatAborted = true
	}
}

// WaitForLease blocks new side effects while a heartbeat result is uncertain.
func (r *taskRuntime) WaitForLease(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.mu.Lock()
		if r.poison != nil {
			err := r.poison
			r.mu.Unlock()
			return err
		}
		if r.cancelRequested {
			r.mu.Unlock()
			return ErrLeaseLost
		}
		if !r.tolerateHeartbeatErrors ||
			(!r.heartbeatPending && r.heartbeatToken == 0 && !r.leaseUncertain) {
			r.mu.Unlock()
			return nil
		}
		changed := r.stateChanged
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *taskRuntime) beginVersionWrite(
	ctx context.Context,
	allowCanceled bool,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		r.mu.Lock()
		if r.poison != nil {
			err := r.poison
			r.mu.Unlock()
			return 0, err
		}
		if r.cancelRequested && !allowCanceled {
			r.mu.Unlock()
			return 0, ErrLeaseLost
		}
		leaseBlocked := r.heartbeatPending || r.heartbeatToken != 0 ||
			(r.tolerateHeartbeatErrors && r.leaseUncertain)
		if !r.versionWriteActive && !leaseBlocked {
			r.versionWriteActive = true
			version := r.version
			r.mu.Unlock()
			return version, nil
		}
		changed := r.stateChanged
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (r *taskRuntime) finishVersionWrite(task *Task) {
	r.mu.Lock()
	if task != nil && r.poison == nil && task.Version > r.version {
		r.version = task.Version
	}
	r.versionWriteActive = false
	r.signalStateChangedLocked()
	r.mu.Unlock()
}

func (r *taskRuntime) beginHeartbeat(
	ctx context.Context,
	startedAt time.Time,
) (uint64, int64, time.Time, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, time.Time{}, err
	}
	r.mu.Lock()
	if r.poison != nil {
		err := r.poison
		r.mu.Unlock()
		return 0, 0, time.Time{}, err
	}
	if r.cancelRequested {
		r.mu.Unlock()
		return 0, 0, time.Time{}, errHeartbeatStopped
	}
	r.heartbeatPending = true
	r.signalStateChangedLocked()
	for r.versionWriteActive || r.heartbeatToken != 0 {
		changed := r.stateChanged
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			r.mu.Lock()
			r.heartbeatPending = false
			r.signalStateChangedLocked()
			r.mu.Unlock()
			return 0, 0, time.Time{}, ctx.Err()
		}
		r.mu.Lock()
		if r.poison != nil {
			err := r.poison
			r.heartbeatPending = false
			r.signalStateChangedLocked()
			r.mu.Unlock()
			return 0, 0, time.Time{}, err
		}
		if r.cancelRequested {
			r.heartbeatPending = false
			r.signalStateChangedLocked()
			r.mu.Unlock()
			return 0, 0, time.Time{}, errHeartbeatStopped
		}
	}
	r.heartbeatSequence++
	token := r.heartbeatSequence
	r.heartbeatToken = token
	r.heartbeatPending = false
	unconfirmedAt := time.Time{}
	if r.tolerateHeartbeatErrors {
		if r.leaseUncertain {
			unconfirmedAt = r.unconfirmedAt
		} else {
			r.unconfirmedAt = startedAt
		}
		r.leaseUncertain = true
	}
	version := r.version
	r.signalStateChangedLocked()
	r.mu.Unlock()
	return token, version, unconfirmedAt, nil
}

func definitiveHeartbeatError(err error) bool {
	return errors.Is(err, ErrLeaseLost) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrIllegalTransition) ||
		errors.Is(err, ErrAlreadyTerminal)
}

func (r *taskRuntime) finishHeartbeatError(token uint64, heartbeatErr error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if token != r.heartbeatToken {
		return errHeartbeatStopped
	}
	r.heartbeatToken = 0
	if r.tolerateHeartbeatErrors && !definitiveHeartbeatError(heartbeatErr) {
		r.leaseUncertain = true
		r.signalStateChangedLocked()
		return errHeartbeatRetry
	}
	r.poison = heartbeatErr
	r.leaseUncertain = false
	r.unconfirmedAt = time.Time{}
	r.signalStateChangedLocked()
	return heartbeatErr
}

func (r *taskRuntime) confirmHeartbeat(
	token uint64,
	expectedVersion int64,
	confirmedAt time.Time,
	task *Task,
) error {
	if task == nil || task.Status != StatusRunning || task.Attempt != r.attempt ||
		task.CancelRequestedAt != nil || task.Version != expectedVersion+1 {
		return r.finishHeartbeatError(token, ErrLeaseLost)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if token != r.heartbeatToken {
		return errHeartbeatStopped
	}
	if r.tolerateHeartbeatErrors &&
		!time.Now().Before(r.leaseExpiresAt.Add(-r.leaseSafetyMargin)) {
		r.poison = ErrLeaseLost
		r.heartbeatToken = 0
		r.leaseUncertain = false
		r.unconfirmedAt = time.Time{}
		r.signalStateChangedLocked()
		return ErrLeaseLost
	}
	r.version = task.Version
	r.heartbeatToken = 0
	r.leaseUncertain = false
	r.unconfirmedAt = time.Time{}
	r.leaseExpiresAt = confirmedAt.Add(r.leaseDuration)
	r.signalStateChangedLocked()
	return nil
}

func (r *taskRuntime) acceptCancellation(task *Task, expectedVersion int64) error {
	if task == nil || task.Status != StatusRunning || task.Attempt != r.attempt ||
		task.CancelRequestedAt == nil ||
		(expectedVersion > 0 && task.Version != expectedVersion) {
		r.loseLease(ErrLeaseLost)
		return ErrLeaseLost
	}
	r.mu.Lock()
	if r.poison != nil {
		err := r.poison
		r.mu.Unlock()
		return err
	}
	if task.Version < r.version {
		r.mu.Unlock()
		r.loseLease(ErrLeaseLost)
		return ErrLeaseLost
	}
	r.version = task.Version
	r.cancelRequested = true
	r.cancelReason = task.CancelReason
	r.heartbeatToken = 0
	r.heartbeatPending = false
	r.leaseUncertain = false
	r.unconfirmedAt = time.Time{}
	r.abortHeartbeatLocked()
	r.signalStateChangedLocked()
	r.mu.Unlock()
	r.requestControlWithReason(ControlStop, task.CancelReason)
	return nil
}

func (r *taskRuntime) loseLease(cause error) {
	if cause == nil {
		cause = ErrLeaseLost
	}
	r.mu.Lock()
	if r.poison == nil {
		r.poison = cause
	}
	r.heartbeatSequence++
	r.heartbeatToken = 0
	r.heartbeatPending = false
	r.leaseUncertain = false
	r.unconfirmedAt = time.Time{}
	r.signalStateChangedLocked()
	r.mu.Unlock()
}

func (r *taskRuntime) stopHeartbeat() {
	r.mu.Lock()
	r.heartbeatSequence++
	r.heartbeatToken = 0
	r.heartbeatPending = false
	r.leaseUncertain = false
	r.unconfirmedAt = time.Time{}
	r.signalStateChangedLocked()
	r.mu.Unlock()
}

func (r *taskRuntime) leaseSafetyDeadline() (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.tolerateHeartbeatErrors || r.leaseExpiresAt.IsZero() ||
		r.poison != nil || r.cancelRequested {
		return time.Time{}, false
	}
	return r.leaseExpiresAt.Add(-r.leaseSafetyMargin), true
}

func (r *taskRuntime) leaseNeedsConfirmation() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.heartbeatPending || r.heartbeatToken != 0 || r.leaseUncertain
}

// NotifyParent emits one idempotent application notification using authority
// bound to the current managed attempt context. It returns
// ErrNotificationUnavailable outside a managed attempt or when the configured
// TaskStore lacks NotificationWriter. Store errors are returned unchanged.
func NotifyParent(ctx context.Context, req *NotifyParentRequest) error {
	if err := validateNotifyParentRequest(req); err != nil {
		return err
	}
	if ctx == nil {
		return ErrNotificationUnavailable
	}
	notify, ok := ctx.Value(notifyParentContextKey{}).(notifyParentCallback)
	if !ok || notify == nil {
		return ErrNotificationUnavailable
	}
	cloned := *req
	cloned.Data = cloneBytes(req.Data)
	return notify(ctx, &cloned)
}

func (r *taskRuntime) notifyParent(
	ctx context.Context,
	req *NotifyParentRequest,
) error {
	r.mu.Lock()
	if r.poison != nil {
		err := r.poison
		r.mu.Unlock()
		return err
	}
	writer := r.notificationWriter
	taskID := r.taskID
	attempt := r.attempt
	r.mu.Unlock()
	if writer == nil {
		return ErrNotificationUnavailable
	}
	cloned := *req
	cloned.Data = cloneBytes(req.Data)
	return writer.EnqueueTaskNotification(
		ctx,
		taskID,
		attempt,
		&cloned,
	)
}

func (r *taskRuntime) EmitProgress(
	ctx context.Context,
	eventID string,
	data []byte,
) (ProgressEmission, error) {
	r.mu.Lock()
	if r.poison != nil {
		err := r.poison
		r.mu.Unlock()
		return ProgressEmission{}, err
	}
	taskEvents := r.taskEvents
	taskID := r.taskID
	attempt := r.attempt
	r.mu.Unlock()
	if eventID == "" {
		eventID = uuid.NewString()
	}
	result, err := taskEvents.AppendTaskEvent(ctx, &AppendTaskEventRequest{
		TaskID: taskID, Attempt: attempt, EventID: eventID, Data: cloneBytes(data),
	})
	if err != nil {
		return ProgressEmission{}, err
	}
	if result == nil || result.Event == nil || result.Event.EventID == "" {
		return ProgressEmission{}, errors.New(
			"backgroundtask: task event store returned an incomplete append result",
		)
	}
	return ProgressEmission{
		EventID: result.Event.EventID, FirstEmission: result.Inserted,
	}, nil
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

func (r *taskRuntime) ReportTranscriptFailure(ctx context.Context, cause error) error {
	version, err := r.beginVersionWrite(ctx, false)
	if err != nil {
		return err
	}
	task, err := r.tasks.ReportTranscriptFailure(ctx, &ReportTranscriptFailureRequest{
		TaskID: r.taskID, ExpectedVersion: version, Error: boundedError(cause),
	})
	r.finishVersionWrite(task)
	if err != nil {
		return err
	}
	return nil
}

func (r *taskRuntime) CommitStart(
	ctx context.Context,
	checkpoint []byte,
) error {
	version, err := r.beginVersionWrite(ctx, false)
	if err != nil {
		return err
	}
	task, err := r.tasks.CommitStart(ctx, &CommitStartRequest{
		TaskID: r.taskID, ExpectedVersion: version,
		Checkpoint: cloneBytes(checkpoint),
	})
	r.finishVersionWrite(task)
	if err != nil {
		return err
	}
	return nil
}

func (r *taskRuntime) heartbeat(ctx context.Context) error {
	requestStartedAt := time.Now()
	token, version, unconfirmedAt, err := r.beginHeartbeat(ctx, requestStartedAt)
	if err != nil {
		return err
	}
	task, err := r.tasks.Heartbeat(ctx, &HeartbeatRequest{
		TaskID: r.taskID, ExpectedVersion: version,
	})
	if err == nil {
		return r.confirmHeartbeat(token, version, requestStartedAt, task)
	}
	if errors.Is(err, ErrVersionConflict) {
		current, getErr := r.tasks.Get(ctx, r.taskID)
		if getErr != nil {
			return r.finishHeartbeatError(token, getErr)
		}
		if current != nil && current.Status == StatusRunning &&
			current.Attempt == r.attempt &&
			current.CancelRequestedAt != nil {
			expectedCancellationVersion := version + 1
			if r.tolerateHeartbeatErrors {
				expectedCancellationVersion = 0
			}
			if acceptErr := r.acceptCancellation(
				current,
				expectedCancellationVersion,
			); acceptErr != nil {
				return acceptErr
			}
			return errHeartbeatStopped
		}
		if current != nil && r.tolerateHeartbeatErrors && !unconfirmedAt.IsZero() &&
			current.Status == StatusRunning &&
			current.Attempt == r.attempt && current.CancelRequestedAt == nil &&
			current.Version == version+1 {
			return r.confirmHeartbeat(token, version, unconfirmedAt, current)
		}
		return r.finishHeartbeatError(token, ErrLeaseLost)
	}
	return r.finishHeartbeatError(token, err)
}

func (r *taskRuntime) reconcileCancellation(ctx context.Context, expectedVersion int64) error {
	task, err := r.tasks.Get(ctx, r.taskID)
	if err != nil {
		r.loseLease(err)
		return err
	}
	return r.acceptCancellation(task, expectedVersion+1)
}

func (r *taskRuntime) commit(ctx context.Context, result *ExecutionResult) (*Task, error) {
	if result == nil {
		return nil, errors.New("backgroundtask: executor returned nil result")
	}
	version, err := r.beginVersionWrite(ctx, true)
	if err != nil {
		return nil, err
	}
	var committed *Task
	defer func() { r.finishVersionWrite(committed) }()
	r.mu.Lock()
	if r.cancelRequested {
		result = &ExecutionResult{Status: StatusCanceled, Error: r.cancelReason}
	}
	r.mu.Unlock()
	task, err := r.commitResult(ctx, version, result)
	if errors.Is(err, ErrVersionConflict) {
		if reconcileErr := r.reconcileCancellation(ctx, version); reconcileErr != nil {
			return nil, reconcileErr
		}
		r.mu.Lock()
		version = r.version
		cancelReason := r.cancelReason
		r.mu.Unlock()
		task, err = r.commitResult(ctx, version, &ExecutionResult{
			Status: StatusCanceled, Error: cancelReason,
		})
	}
	if err != nil {
		r.loseLease(err)
		return nil, err
	}
	committed = task
	return task, nil
}

func (r *taskRuntime) commitResult(
	ctx context.Context,
	version int64,
	result *ExecutionResult,
) (*Task, error) {
	if result.Directive != "" {
		if result.Directive != ExecutionDirectiveYield || result.Status != "" ||
			len(result.Data) != 0 || result.Error != "" {
			return nil, fmt.Errorf("%w: conflicting executor directive and lifecycle result", ErrInvalidExecutionResult)
		}
		return r.tasks.Yield(ctx, &YieldTaskRequest{
			TaskID: r.taskID, ExpectedVersion: version,
			Checkpoint: cloneBytes(result.Checkpoint),
		})
	}
	switch result.Status {
	case StatusCompleted:
		if len(result.Checkpoint) != 0 || result.Error != "" {
			return nil, fmt.Errorf("%w: completed result contains checkpoint or error", ErrInvalidExecutionResult)
		}
		return r.tasks.Complete(ctx, &CompleteTaskRequest{
			TaskID: r.taskID, ExpectedVersion: version, Data: cloneBytes(result.Data),
		})
	case StatusFailed:
		if len(result.Checkpoint) != 0 || len(result.Data) != 0 {
			return nil, fmt.Errorf("%w: failed result contains checkpoint or data", ErrInvalidExecutionResult)
		}
		return r.tasks.Fail(ctx, &FailTaskRequest{
			TaskID: r.taskID, ExpectedVersion: version, Error: result.Error,
		})
	case StatusCanceled:
		if len(result.Checkpoint) != 0 || len(result.Data) != 0 {
			return nil, fmt.Errorf("%w: canceled result contains checkpoint or data", ErrInvalidExecutionResult)
		}
		return r.tasks.AckCancel(ctx, &AckCancelRequest{
			TaskID: r.taskID, ExpectedVersion: version, Reason: result.Error,
		})
	case StatusWaitingInput:
		if len(result.Data) != 0 || result.Error != "" {
			return nil, fmt.Errorf("%w: waiting-input result contains data or error", ErrInvalidExecutionResult)
		}
		return r.tasks.WaitInput(ctx, &WaitInputTaskRequest{
			TaskID: r.taskID, ExpectedVersion: version, Checkpoint: cloneBytes(result.Checkpoint),
		})
	case StatusSuspended:
		if len(result.Data) != 0 || result.Error != "" {
			return nil, fmt.Errorf("%w: suspended result contains data or error", ErrInvalidExecutionResult)
		}
		return r.tasks.Suspend(ctx, &SuspendTaskRequest{
			TaskID: r.taskID, ExpectedVersion: version, Checkpoint: cloneBytes(result.Checkpoint),
		})
	default:
		return nil, fmt.Errorf("%w: unsupported executor result status %q", ErrInvalidExecutionResult, result.Status)
	}
}

// AllocateTaskIDRequest describes the task category used by the default ID
// generator. Kind is not persisted independently and must be empty or a
// 64-byte ASCII identifier segment containing letters, digits, '-' or '_'.
type AllocateTaskIDRequest struct {
	Kind string
}

// AllocateTaskID allocates an opaque ID for a task category.
func (m *Manager) AllocateTaskID(ctx context.Context, request *AllocateTaskIDRequest) (string, error) {
	if request == nil {
		return "", errors.New("backgroundtask: allocate task id request is required")
	}
	if !validTaskIDKind(request.Kind) {
		return "", errors.New("backgroundtask: task id kind is not a safe identifier segment")
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

func (m *Manager) captureContextSnapshot(ctx context.Context) ([]byte, bool, error) {
	if m.contextSnapshotter == nil {
		return nil, false, nil
	}
	snapshot, err := m.contextSnapshotter.CaptureContext(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("backgroundtask: capture context snapshot: %w", err)
	}
	if snapshot == nil {
		snapshot = []byte{}
	}
	return cloneBytes(snapshot), true, nil
}

func (m *Manager) restoreExecutionContext(ctx context.Context, task *Task) (context.Context, error) {
	if task == nil || len(task.ContextSnapshot) == 0 {
		return ctx, nil
	}
	if m.contextSnapshotter == nil {
		return nil, errors.New(
			"backgroundtask: context snapshotter is required to restore task context",
		)
	}
	restored, err := m.contextSnapshotter.RestoreContext(ctx, cloneBytes(task.ContextSnapshot))
	if err != nil {
		return nil, fmt.Errorf("backgroundtask: restore context snapshot: %w", err)
	}
	if restored == nil {
		return nil, errors.New("backgroundtask: restore context snapshot returned nil context")
	}
	return restored, nil
}

// Submit validates serialized intent, persists a pending background task, and
// attempts to emit its low-latency TaskCreated parent-session event before
// returning. Create atomically writes the durable TaskCreated outbox record; if
// only the immediate event send fails, Submit returns the non-nil task with an
// error wrapping ErrTaskCreatedEventUndelivered. Callers must treat that case as
// ownership transferred and rely on the outbox for recovery.
func (m *Manager) Submit(ctx context.Context, req *SubmitRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: submit request is required")
	}
	spec := req.Spec
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
	if spec.SessionID != "" {
		if _, ok := m.tasks.(NotificationOutbox); !ok {
			return nil, errors.New(
				"backgroundtask: task store must implement NotificationOutbox for parent-session tasks",
			)
		}
	}
	if spec.SessionID != "" && m.sendTaskCreatedEvent == nil {
		return nil, errors.New(
			"backgroundtask: task-created session event sender is required for parent-session tasks",
		)
	}
	if err := executor.ValidateSpec(cloneSpec(spec)); err != nil {
		return nil, fmt.Errorf("backgroundtask: validate spec: %w", err)
	}
	contextSnapshot, _, err := m.captureContextSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	policy := executor.LeaseExpiryPolicy()
	task, err := m.tasks.Create(ctx, &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: policy, Checkpoint: cloneBytes(req.InitialCheckpoint),
		ContextSnapshot: contextSnapshot,
	})
	if err != nil {
		return nil, err
	}
	if spec.SessionID != "" {
		if sendErr := m.sendTaskCreatedEvent(ctx, cloneTask(task)); sendErr != nil {
			return task, &taskCreatedEventUndeliveredError{
				taskID: task.Spec.ID,
				cause:  sendErr,
			}
		}
	}
	return task, nil
}

// Get returns the authoritative task snapshot.
func (m *Manager) Get(ctx context.Context, taskID string) (*Task, error) {
	return m.tasks.Get(ctx, taskID)
}

// ListPending is the read-only dispatch boundary. A worker may select and
// dispatch a task ID from this result; only Execute performs start authorization.
// Ordering, cursor, limit, and snapshot ownership follow ListPendingRequest.
func (m *Manager) ListPending(ctx context.Context, req *ListPendingRequest) (*ListPendingResult, error) {
	return m.tasks.ListPending(ctx, req)
}

// ListSuspended returns checkpointed tasks that require an explicit release
// before workers may claim them again. Pagination follows ListPendingRequest.
func (m *Manager) ListSuspended(
	ctx context.Context,
	req *ListSuspendedRequest,
) (*ListSuspendedResult, error) {
	return m.tasks.ListSuspended(ctx, req)
}

// WaitForTaskVersion blocks until the authoritative task snapshot has a
// Version greater than req.AfterVersion. Task progress events do not advance
// Version and therefore do not satisfy the wait.
func (m *Manager) WaitForTaskVersion(ctx context.Context, req *WaitForTaskVersionRequest) (*Task, error) {
	return m.tasks.WaitForTaskVersion(ctx, req)
}

// ListTaskEvents reads one snapshot-stable page of task events.
func (m *Manager) ListTaskEvents(
	ctx context.Context,
	req *ListTaskEventsRequest,
) (*ListTaskEventsResult, error) {
	return m.taskEvents.ListTaskEvents(ctx, req)
}

// RequestCancel records cancellation intent and signals a local active attempt.
// An optional reason is durable and first-write across repeated requests.
// Process-local non-recoverable work may wait for terminal acknowledgement;
// recoverable work may return the still-running snapshot after intent is durable.
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
		task, getErr := m.tasks.Get(ctx, taskID)
		if getErr != nil {
			return nil, getErr
		}
		result, err = m.tasks.RequestCancel(ctx, &RequestCancelRequest{
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
			if err = attempt.runtime.acceptCancellation(result, 0); err != nil {
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
		terminal, getErr := m.tasks.Get(ctx, taskID)
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

// ReleaseSuspension returns a suspended task to pending so a worker can claim
// a new attempt from its persisted checkpoint.
func (m *Manager) ReleaseSuspension(ctx context.Context, taskID string) (*Task, error) {
	if taskID == "" {
		return nil, errors.New("backgroundtask: release suspension task id is required")
	}
	contextSnapshot, capturedContextSnapshot, err := m.captureContextSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for retry := 0; ; retry++ {
		task, err := m.tasks.Get(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task.Status != StatusSuspended {
			return nil, ErrIllegalTransition
		}
		req := &ReleaseSuspensionRequest{
			TaskID: taskID, ExpectedVersion: task.Version,
		}
		if capturedContextSnapshot {
			req.ContextSnapshot = contextSnapshot
		}
		released, err := m.tasks.ReleaseSuspension(ctx, req)
		if !errors.Is(err, ErrVersionConflict) {
			return released, err
		}
		if retry >= 7 {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

// Resume persists opaque input for a task waiting on external input. The
// concrete executor must defensively validate the persisted input before use;
// Manager intentionally does not know executor-specific resume schemas.
func (m *Manager) Resume(ctx context.Context, req *ResumeRequest) (*Task, error) {
	if req == nil {
		return nil, errors.New("backgroundtask: resume request is required")
	}
	contextSnapshot, capturedContextSnapshot, err := m.captureContextSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	next := &ResumeRequest{
		TaskID: req.TaskID, ExpectedVersion: req.ExpectedVersion, Data: cloneBytes(req.Data),
	}
	if capturedContextSnapshot {
		next.ContextSnapshot = contextSnapshot
	}
	return m.tasks.Resume(ctx, next)
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

	task, err := m.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	ctx, err = m.restoreExecutionContext(ctx, task)
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
	leaseConfirmedAt := time.Now()
	started, err := m.tasks.Start(ctx, &StartTaskRequest{
		TaskID: taskID, ExpectedVersion: task.Version,
	})
	if err != nil {
		return err
	}
	runtime := newTaskRuntimeWithConfig(taskRuntimeConfig{
		tasks:              m.tasks,
		taskEvents:         m.taskEvents,
		taskID:             taskID,
		attempt:            started.Attempt,
		version:            started.Version,
		notificationWriter: m.notificationWriter,
		lease: taskRuntimeLeaseConfig{
			confirmedAt:             leaseConfirmedAt,
			duration:                m.leaseDuration,
			safetyMargin:            m.heartbeatSafetyMargin,
			tolerateHeartbeatErrors: m.tolerateHeartbeatErrors,
		},
	})
	if started.CancelRequestedAt != nil {
		if err = runtime.acceptCancellation(started, 0); err != nil {
			return err
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	if m.tolerateHeartbeatErrors {
		runCtx = core.WithExecutionGate(runCtx, runtime.WaitForLease)
	}
	runCtx = context.WithValue(
		runCtx,
		notifyParentContextKey{},
		notifyParentCallback(runtime.notifyParent),
	)
	m.attemptsMu.Lock()
	attempt.cancel = cancel
	attempt.runtime = runtime
	attempt.supportsDrain = executor.SupportsDrain()
	if attempt.drainOnReady && attempt.supportsDrain {
		runtime.requestControlWithReason(ControlDrain, attempt.drainReason)
	}
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

	if errors.Is(executeErr, ErrDrainCheckpointUnavailable) {
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
	if !runtime.tolerateHeartbeatErrors {
		m.heartbeatLegacy(ctx, cancel, runtime, stop)
		return
	}
	interval := m.heartbeatEvery
	if interval <= 0 {
		interval = time.Nanosecond
	}
	heartbeatTimer := time.NewTimer(interval)
	defer heartbeatTimer.Stop()
	heartbeatC := heartbeatTimer.C
	var resultC <-chan error
	stopC := stop
	stopRequested := false
	for {
		var safetyTimer *time.Timer
		var safetyC <-chan time.Time
		if deadline, ok := runtime.leaseSafetyDeadline(); ok {
			delay := time.Until(deadline)
			if delay < 0 {
				delay = 0
			}
			safetyTimer = time.NewTimer(delay)
			safetyC = safetyTimer.C
		}
		select {
		case <-heartbeatC:
			heartbeatC = nil
			result := make(chan error, 1)
			resultC = result
			go func() {
				result <- runtime.heartbeat(ctx)
			}()
		case err := <-resultC:
			resultC = nil
			switch {
			case err == nil:
				if stopRequested {
					if safetyTimer != nil {
						safetyTimer.Stop()
					}
					return
				}
			case errors.Is(err, errHeartbeatRetry):
			case errors.Is(err, errHeartbeatStopped):
				if safetyTimer != nil {
					safetyTimer.Stop()
				}
				return
			default:
				if !errors.Is(err, context.Canceled) {
					cancel()
				}
				if safetyTimer != nil {
					safetyTimer.Stop()
				}
				return
			}
			heartbeatTimer.Reset(interval)
			heartbeatC = heartbeatTimer.C
		case <-ctx.Done():
			runtime.stopHeartbeat()
			if safetyTimer != nil {
				safetyTimer.Stop()
			}
			return
		case <-runtime.heartbeatAbort:
			if safetyTimer != nil {
				safetyTimer.Stop()
			}
			return
		case <-stopC:
			stopRequested = true
			stopC = nil
			if resultC == nil && !runtime.leaseNeedsConfirmation() {
				if safetyTimer != nil {
					safetyTimer.Stop()
				}
				return
			}
		case <-safetyC:
			runtime.loseLease(ErrLeaseLost)
			cancel()
			return
		}
		if safetyTimer != nil {
			safetyTimer.Stop()
		}
	}
}

func (m *Manager) heartbeatLegacy(
	ctx context.Context,
	cancel context.CancelFunc,
	runtime *taskRuntime,
	stop <-chan struct{},
) {
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
	runtime ExecutionRuntime,
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

var (
	_ ExecutionRuntime   = (*taskRuntime)(nil)
	_ LeaseGateRuntime   = (*taskRuntime)(nil)
	_ StartCommitRuntime = (*taskRuntime)(nil)
)
