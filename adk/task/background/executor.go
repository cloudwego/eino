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

package background

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
	taskcore "github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/internal/safe"
)

// ControlKind identifies a Manager control signal sent to an executor.
type ControlKind string

const (
	defaultCanceledReason = "task was canceled"
	defaultTimeoutReason  = "task timed out"

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

// ExecutionAction identifies the transition requested by an Executor.
type ExecutionAction string

const (
	// ExecutionActionComplete completes an idle task.
	ExecutionActionComplete ExecutionAction = "complete"
	// ExecutionActionFail fails a task.
	ExecutionActionFail ExecutionAction = "fail"
	// ExecutionActionCancel acknowledges task cancellation.
	ExecutionActionCancel ExecutionAction = "cancel"
	// ExecutionActionWaitInput waits when the mailbox is caught up.
	ExecutionActionWaitInput ExecutionAction = "wait_input"
	// ExecutionActionSuspend suspends when the mailbox is caught up.
	ExecutionActionSuspend ExecutionAction = "suspend"
	// ExecutionActionYield relinquishes a recoverable active attempt while
	// the logical operation continues outside the current Worker.
	ExecutionActionYield ExecutionAction = "yield"
)

// ExecutionResult describes one legal executor outcome:
//   - Complete: optional Data and the consumed InputCursor.
//   - Fail or Cancel: Error.
//   - WaitInput or Suspend: Checkpoint and the
//     consumed InputCursor.
//   - Yield: an optional Checkpoint.
//
// Completion and pauses atomically fall back to Pending when newer mailbox
// input exists.
type ExecutionResult struct {
	Action      ExecutionAction
	Checkpoint  []byte
	Data        []byte
	Error       string
	InputCursor int64
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
	// ListInputs reads durable inputs after the supplied sequence.
	ListInputs(context.Context, int64, int) (*taskcore.ListInputsResult, error)
	// WaitInputs blocks until durable input exists after the supplied sequence.
	WaitInputs(context.Context, int64) (*taskcore.ListInputsResult, error)
	// AdvanceInputCursor records input consumption without changing task lifecycle.
	AdvanceInputCursor(context.Context, int64, int64) error
	// CommitInput atomically persists an established operation together with the
	// consumed mailbox prefix.
	CommitInput(context.Context, int64, int64, []byte) error
	// CommitStart atomically records that an executor established its external
	// operation.
	CommitStart(context.Context, []byte) error
}

// CancellationAcknowledger performs idempotent business cleanup before durable
// cancellation is committed.
type CancellationAcknowledger interface {
	AcknowledgeCancellation(context.Context, *TaskSnapshot, string) error
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
	ValidateExecution(context.Context, *TaskSnapshot) error
	// SupportsDrain reports whether Execute handles ControlDrain by returning a
	// resumable suspended or yielded result.
	SupportsDrain() bool
	// Execute owns the attempt until it returns. It must observe ctx and runtime
	// controls and return exactly one legal ExecutionResult variant.
	Execute(context.Context, *TaskSnapshot, ExecutionRuntime) (*ExecutionResult, error)
}

type executorRegistry struct {
	mu        sync.RWMutex
	executors map[string]Executor
}

func newExecutorRegistry() *executorRegistry {
	return &executorRegistry{executors: make(map[string]Executor)}
}

func (r *executorRegistry) loadOrRegister(executor Executor) (Executor, bool, error) {
	if executor == nil {
		return nil, false, errors.New("task/background: executor and non-empty key are required")
	}
	key := executor.Key()
	if key == "" {
		return nil, false, errors.New("task/background: executor and non-empty key are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if actual, ok := r.executors[key]; ok {
		return actual, true, nil
	}
	r.executors[key] = executor
	return executor, false, nil
}

func (r *executorRegistry) resolve(key string) (Executor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	executor, ok := r.executors[key]
	return executor, ok
}

func (r *executorRegistry) keys() []string {
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
	mu                 sync.Mutex
	controlMu          sync.Mutex
	tasks              LifecycleStore
	taskEvents         TaskEventStore
	notificationWriter NotificationWriter
	taskID             string
	attempt            int64
	version            int64
	controls           chan ControlRequest
	poison             error
	cancelRequested    bool
	cancelReason       string
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

var errHeartbeatStopped = errors.New("task/background: heartbeat stopped")

func newTaskRuntime(
	tasks LifecycleStore,
	taskEvents TaskEventStore,
	taskID string,
	attempt, version int64,
	notificationWriter NotificationWriter,
) *taskRuntime {
	return &taskRuntime{
		tasks: tasks, taskEvents: taskEvents,
		notificationWriter: notificationWriter,
		taskID:             taskID, attempt: attempt, version: version,
		controls: make(chan ControlRequest, 1),
	}
}

func (r *taskRuntime) Controls() <-chan ControlRequest { return r.controls }

func (r *taskRuntime) ListInputs(
	ctx context.Context,
	afterSequence int64,
	limit int,
) (*taskcore.ListInputsResult, error) {
	if r.tasks == nil {
		return nil, taskcore.ErrMailboxStoreRequired
	}
	return r.tasks.ListInputs(ctx, &taskcore.ListInputsRequest{
		TaskID: r.taskID, AfterSequence: afterSequence, Limit: limit,
	})
}

func (r *taskRuntime) WaitInputs(
	ctx context.Context,
	afterSequence int64,
) (*taskcore.ListInputsResult, error) {
	if r.tasks == nil {
		return nil, taskcore.ErrMailboxStoreRequired
	}
	return r.tasks.WaitInputs(ctx, &taskcore.WaitInputsRequest{
		TaskID: r.taskID, AfterSequence: afterSequence,
	})
}

func (r *taskRuntime) AdvanceInputCursor(
	ctx context.Context,
	expectedCursor, cursor int64,
) error {
	if r.tasks == nil {
		return taskcore.ErrMailboxStoreRequired
	}
	mailbox, err := r.tasks.GetMailbox(ctx, r.taskID)
	if err != nil {
		return err
	}
	return r.tasks.AdvanceCursor(ctx, &taskcore.AdvanceCursorRequest{
		TaskID: r.taskID, ExpectedCursor: expectedCursor, Cursor: cursor,
		ExpectedGeneration: mailbox.Generation, Attempt: r.attempt,
	})
}

func (r *taskRuntime) CommitInput(
	ctx context.Context,
	expectedCursor, cursor int64,
	checkpoint []byte,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	committed, err := r.tasks.CommitInput(ctx, &CommitInputRequest{
		TaskID: r.taskID, ExpectedVersion: r.version, Attempt: r.attempt,
		ExpectedCursor: expectedCursor, InputCursor: cursor,
		Checkpoint: cloneBytes(checkpoint),
	})
	if err != nil {
		r.poison = err
		return err
	}
	r.version = committed.Version
	return nil
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
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	if r.notificationWriter == nil {
		return ErrNotificationUnavailable
	}
	cloned := *req
	cloned.Data = cloneBytes(req.Data)
	return r.notificationWriter.EnqueueTaskNotification(
		ctx,
		r.taskID,
		r.attempt,
		&cloned,
	)
}

func (r *taskRuntime) EmitProgress(
	ctx context.Context,
	eventID string,
	data []byte,
) (ProgressEmission, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return ProgressEmission{}, r.poison
	}
	if eventID == "" {
		eventID = uuid.NewString()
	}
	result, err := r.taskEvents.AppendTaskEvent(ctx, &AppendTaskEventRequest{
		TaskID: r.taskID, Attempt: r.attempt, EventID: eventID, Data: cloneBytes(data),
	})
	if err != nil {
		return ProgressEmission{}, err
	}
	if result == nil || result.Event == nil || result.Event.EventID == "" {
		return ProgressEmission{}, errors.New(
			"task/background: task event store returned an incomplete append result",
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	task, err := r.tasks.ReportTranscriptFailure(ctx, &ReportTranscriptFailureRequest{
		TaskID: r.taskID, ExpectedVersion: r.version, Error: boundedError(cause),
	})
	if err != nil {
		return err
	}
	r.version = task.Version
	return nil
}

func (r *taskRuntime) CommitStart(
	ctx context.Context,
	checkpoint []byte,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	task, err := r.tasks.CommitStart(ctx, &CommitStartRequest{
		TaskID: r.taskID, ExpectedVersion: r.version,
		Checkpoint: cloneBytes(checkpoint),
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
	task, err := r.tasks.Heartbeat(ctx, &HeartbeatRequest{
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
	task, err := r.tasks.Get(ctx, r.taskID)
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

func (r *taskRuntime) commit(ctx context.Context, result *ExecutionResult) (*TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return nil, r.poison
	}
	if result == nil {
		return nil, errors.New("task/background: executor returned nil result")
	}
	if r.cancelRequested {
		result = &ExecutionResult{
			Action: ExecutionActionCancel,
			Error:  r.cancelReason,
		}
	}
	task, err := r.commitResult(ctx, result)
	if errors.Is(err, ErrVersionConflict) {
		if reconcileErr := r.reconcileCancellationLocked(ctx); reconcileErr != nil {
			return nil, reconcileErr
		}
		task, err = r.commitResult(ctx, &ExecutionResult{
			Action: ExecutionActionCancel, Error: r.cancelReason,
		})
	}
	if err != nil {
		r.poison = err
		return nil, err
	}
	r.version = task.Version
	return task, nil
}

func (r *taskRuntime) commitResult(ctx context.Context, result *ExecutionResult) (*TaskSnapshot, error) {
	switch result.Action {
	case ExecutionActionComplete:
		if len(result.Checkpoint) != 0 || result.Error != "" ||
			result.InputCursor < 0 {
			return nil, fmt.Errorf(
				"%w: invalid completion",
				ErrInvalidExecutionResult,
			)
		}
		committed, err := r.tasks.CompleteIfNoInputs(ctx, &CompleteIfNoInputsRequest{
			TaskID: r.taskID, ExpectedVersion: r.version,
			Attempt: r.attempt, InputCursor: result.InputCursor,
			ResultData: cloneBytes(result.Data),
		})
		if errors.Is(err, taskcore.ErrInputsPending) {
			return committed, nil
		}
		return committed, err
	case ExecutionActionFail:
		if len(result.Checkpoint) != 0 || len(result.Data) != 0 ||
			result.InputCursor != 0 || result.Error == "" {
			return nil, fmt.Errorf("%w: failed result contains checkpoint or data", ErrInvalidExecutionResult)
		}
		return r.tasks.Fail(ctx, &FailTaskRequest{
			TaskID: r.taskID, ExpectedVersion: r.version, Error: result.Error,
		})
	case ExecutionActionCancel:
		if len(result.Checkpoint) != 0 || len(result.Data) != 0 ||
			result.InputCursor != 0 {
			return nil, fmt.Errorf("%w: canceled result contains checkpoint or data", ErrInvalidExecutionResult)
		}
		return r.tasks.AckCancel(ctx, &AckCancelRequest{
			TaskID: r.taskID, ExpectedVersion: r.version, Reason: result.Error,
		})
	case ExecutionActionWaitInput:
		if len(result.Checkpoint) == 0 || len(result.Data) != 0 ||
			result.Error != "" || result.InputCursor < 0 {
			return nil, fmt.Errorf(
				"%w: invalid input wait",
				ErrInvalidExecutionResult,
			)
		}
		committed, err := r.tasks.WaitInputIfNoInputs(
			ctx,
			&WaitInputIfNoInputsRequest{
				TaskID: r.taskID, ExpectedVersion: r.version,
				Attempt: r.attempt, InputCursor: result.InputCursor,
				Checkpoint: cloneBytes(result.Checkpoint),
			},
		)
		if errors.Is(err, taskcore.ErrInputsPending) {
			return committed, nil
		}
		return committed, err
	case ExecutionActionSuspend:
		if len(result.Checkpoint) == 0 || len(result.Data) != 0 ||
			result.Error != "" || result.InputCursor < 0 {
			return nil, fmt.Errorf(
				"%w: invalid input suspension",
				ErrInvalidExecutionResult,
			)
		}
		committed, err := r.tasks.SuspendIfNoInputs(ctx, &SuspendIfNoInputsRequest{
			TaskID: r.taskID, ExpectedVersion: r.version,
			Attempt: r.attempt, InputCursor: result.InputCursor,
			Checkpoint: cloneBytes(result.Checkpoint),
		})
		if errors.Is(err, taskcore.ErrInputsPending) {
			return committed, nil
		}
		return committed, err
	case ExecutionActionYield:
		if result.InputCursor != 0 || len(result.Data) != 0 {
			return nil, fmt.Errorf(
				"%w: yield contains input cursor or data",
				ErrInvalidExecutionResult,
			)
		}
		return r.tasks.Yield(ctx, &YieldTaskRequest{
			TaskID: r.taskID, ExpectedVersion: r.version,
			Checkpoint: cloneBytes(result.Checkpoint),
		})
	default:
		return nil, fmt.Errorf(
			"%w: unsupported executor action %q",
			ErrInvalidExecutionResult, result.Action,
		)
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
		return "", errors.New("task/background: allocate task id request is required")
	}
	if !validTaskIDKind(request.Kind) {
		return "", errors.New("task/background: task id kind is not a safe identifier segment")
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
			return "", fmt.Errorf("task/background: task id generator: %w", err)
		}
		if id == "" {
			return "", errors.New("task/background: task id generator returned empty id")
		}
		return id, nil
	}
	id, err := defaultTaskID(request.Kind)
	if err != nil {
		return "", fmt.Errorf("task/background: generate task id: %w", err)
	}
	return id, nil
}

func (m *Manager) captureContextSnapshot(ctx context.Context) ([]byte, bool, error) {
	if m.contextSnapshotter == nil {
		return nil, false, nil
	}
	snapshot, err := m.contextSnapshotter.CaptureContext(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("task/background: capture context snapshot: %w", err)
	}
	if snapshot == nil {
		snapshot = []byte{}
	}
	return cloneBytes(snapshot), true, nil
}

func (m *Manager) restoreExecutionContext(ctx context.Context, task *TaskSnapshot) (context.Context, error) {
	if task == nil || len(task.ContextSnapshot) == 0 {
		return ctx, nil
	}
	if m.contextSnapshotter == nil {
		return nil, errors.New(
			"task/background: context snapshotter is required to restore task context",
		)
	}
	restored, err := m.contextSnapshotter.RestoreContext(ctx, cloneBytes(task.ContextSnapshot))
	if err != nil {
		return nil, fmt.Errorf("task/background: restore context snapshot: %w", err)
	}
	if restored == nil {
		return nil, errors.New("task/background: restore context snapshot returned nil context")
	}
	return restored, nil
}

// Submit validates serialized intent, persists a pending task, and
// attempts to emit its low-latency TaskCreated parent-session event before
// returning. Create atomically writes the durable TaskCreated outbox record; if
// only the immediate event send fails, Submit returns the non-nil task with an
// error wrapping ErrTaskCreatedEventUndelivered. Callers must treat that case as
// ownership transferred and rely on the outbox for recovery.
func (m *Manager) Submit(ctx context.Context, req *SubmitRequest) (*TaskSnapshot, error) {
	if req == nil {
		return nil, errors.New("task/background: submit request is required")
	}
	spec := req.Spec
	if spec.ID == "" {
		return nil, errors.New("task/background: submit requires a pre-allocated task id")
	}
	var parentExecution *taskcore.ExecutionContext
	if execution, ok := taskcore.ExecutionContextFromContext(ctx); ok {
		if spec.ParentTaskID == "" {
			spec.ParentTaskID = execution.TaskID
		}
		if execution.RootSessionID != "" {
			spec.RootSessionID = execution.RootSessionID
		}
		if spec.ParentTaskID == execution.TaskID {
			copy := execution
			parentExecution = &copy
		}
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, m.closedError()
	}
	executor, ok := m.executors.resolve(spec.ExecutorKey)
	if !ok {
		return nil, fmt.Errorf("task/background: executor %q is unavailable", spec.ExecutorKey)
	}
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	if spec.ParentTaskID == "" && spec.RootSessionID != "" {
		if _, ok := m.tasks.(NotificationOutbox); !ok {
			return nil, errors.New(
				"task/background: task store must implement NotificationOutbox for parent-session tasks",
			)
		}
	}
	if spec.ParentTaskID == "" && spec.RootSessionID != "" &&
		m.sendTaskCreatedEvent == nil {
		return nil, errors.New(
			"task/background: task-created session event sender is required for parent-session tasks",
		)
	}
	if err := executor.ValidateSpec(cloneSpec(spec)); err != nil {
		return nil, fmt.Errorf("task/background: validate spec: %w", err)
	}
	contextSnapshot, _, err := m.captureContextSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	policy := executor.LeaseExpiryPolicy()
	task, err := m.tasks.Create(ctx, &CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: policy, Checkpoint: cloneBytes(req.InitialCheckpoint),
		ContextSnapshot: contextSnapshot, ParentExecution: parentExecution,
	})
	if err != nil {
		return nil, err
	}
	if spec.ParentTaskID == "" && spec.RootSessionID != "" {
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
func (m *Manager) Get(ctx context.Context, taskID string) (*TaskSnapshot, error) {
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
func (m *Manager) WaitForTaskVersion(ctx context.Context, req *WaitForTaskVersionRequest) (*TaskSnapshot, error) {
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
) (*TaskSnapshot, error) {
	cancelConfig := requestCancelOptions{}
	for _, option := range options {
		if option != nil {
			option(&cancelConfig)
		}
	}
	if len(cancelConfig.reason) > 4096 {
		return nil, errors.New("task/background: cancellation reason exceeds 4096 bytes")
	}

	m.attemptsMu.Lock()
	attempt := m.activeAttempts[taskID]
	m.attemptsMu.Unlock()

	var result *TaskSnapshot
	var err error
	for retry := 0; ; retry++ {
		task, getErr := m.tasks.Get(ctx, taskID)
		if getErr != nil {
			return nil, getErr
		}
		if terminalStatus(task.Status) {
			return nil, ErrAlreadyTerminal
		}
		reason := cancelConfig.reason
		if reason == "" {
			reason = defaultCanceledReason
		}
		if executor, ok := m.executors.resolve(task.Spec.ExecutorKey); ok {
			if acknowledger, supports := executor.(CancellationAcknowledger); supports {
				if ackErr := acknowledgeCancellation(
					ctx,
					acknowledger,
					task,
					reason,
				); ackErr != nil {
					return nil, ackErr
				}
			}
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

func acknowledgeCancellation(
	ctx context.Context,
	acknowledger CancellationAcknowledger,
	task *TaskSnapshot,
	reason string,
) (err error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			err = safe.NewPanicErr(panicValue, debug.Stack())
		}
	}()
	return acknowledger.AcknowledgeCancellation(ctx, cloneTask(task), reason)
}

// ReleaseSuspension returns a suspended task to pending so a worker can claim
// a new attempt from its persisted checkpoint.
func (m *Manager) ReleaseSuspension(ctx context.Context, taskID string) (*TaskSnapshot, error) {
	if taskID == "" {
		return nil, errors.New("task/background: release suspension task id is required")
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

// Execute claims and runs one pending task attempt on the current worker.
func (m *Manager) Execute(ctx context.Context, taskID string) error {
	return m.execute(ctx, taskID)
}

func (m *Manager) execute(
	ctx context.Context,
	taskID string,
) (returnErr error) {
	redispatch := false
	timeoutController := taskcontrol.FromContext(ctx)
	if timeoutController != nil {
		defer timeoutController.Close()
	}
	if taskID == "" {
		return errors.New("task/background: execute task id is required")
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
		return errors.New("task/background: task is already executing in this manager")
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
		if redispatch {
			go func() {
				_ = m.execute(detachedCtx{parent: ctx}, taskID)
			}()
		}
	}()

	task, err := m.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	ctx, err = m.restoreExecutionContext(ctx, task)
	if err != nil {
		return err
	}
	executor, ok := m.executors.resolve(task.Spec.ExecutorKey)
	if !ok {
		return fmt.Errorf("task/background: executor %q is unavailable", task.Spec.ExecutorKey)
	}
	if err = executor.ValidateSpec(cloneSpec(task.Spec)); err != nil {
		return fmt.Errorf("task/background: validate spec: %w", err)
	}
	if err = executor.ValidateExecution(ctx, cloneTask(task)); err != nil {
		return fmt.Errorf("task/background: validate execution: %w", err)
	}
	started, err := m.tasks.Start(ctx, &StartTaskRequest{
		TaskID: taskID, ExpectedVersion: task.Version,
	})
	if err != nil {
		return err
	}
	runtime := newTaskRuntime(
		m.tasks,
		m.taskEvents,
		taskID,
		started.Attempt,
		started.Version,
		m.tasks,
	)
	if started.CancelRequestedAt != nil {
		runtime.cancelRequested = true
		runtime.cancelReason = started.CancelReason
		runtime.requestControlWithReason(ControlStop, runtime.cancelReason)
	}
	runCtx, cancel := context.WithCancel(ctx)
	mailbox, mailboxErr := m.tasks.GetMailbox(runCtx, taskID)
	if mailboxErr != nil {
		cancel()
		return mailboxErr
	}
	runCtx = taskcore.WithExecutionContext(runCtx, taskcore.ExecutionContext{
		TaskID: taskID, Owner: taskcore.OwnerManager,
		Generation: mailbox.Generation, Attempt: started.Attempt,
		RootSessionID: started.Spec.RootSessionID,
	})
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
		result = &ExecutionResult{
			Action: ExecutionActionFail,
			Error:  boundedError(executeErr),
		}
	} else if result == nil {
		result = &ExecutionResult{
			Action: ExecutionActionFail,
			Error:  "executor returned nil result",
		}
	}
	committed, commitErr := runtime.commit(detachedCtx{parent: ctx}, result)
	inputBoundary := result.Action == ExecutionActionComplete ||
		result.Action == ExecutionActionWaitInput ||
		result.Action == ExecutionActionSuspend
	if commitErr == nil && committed != nil &&
		committed.Status == StatusPending &&
		inputBoundary {
		redispatch = true
	}
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
	claimed *TaskSnapshot,
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

var _ ExecutionRuntime = (*taskRuntime)(nil)
