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
// Manager coordinates Store-backed task submission, execution, control, and the
// process-local Run convenience adapter. It is deliberately non-generic so one
// instance can serve heterogeneous executor domains under one task-ID space.
//
// Durable executors reconstruct work from Spec. WorkFunc and StreamWorkFunc are
// retained only for explicitly process-local RecoveryFail tasks.
//
// The Store, outbox, and session-activation SPIs are provisional. Promotion
// requires conformance against external multi-process providers.
package backgroundtask

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

// Status represents the lifecycle status of a task.
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
	// StatusCanceling indicates durable stop intent is awaiting worker acknowledgement.
	StatusCanceling Status = "canceling"
	// StatusCompleted indicates the task finished successfully.
	StatusCompleted Status = "completed"
	// StatusFailed indicates the task terminated with an error.
	StatusFailed Status = "failed"
	// StatusCanceled indicates the task was stopped by an external request
	// (Cancel / Close) via context cancellation.
	StatusCanceled Status = "canceled"
)

// Task represents a single managed execution record.
type Task struct {
	// Spec is the immutable serialized intent for this task.
	Spec Spec
	// TransitionVersion is the CAS version of this durable record.
	TransitionVersion int64
	// Attempt counts successful claims.
	Attempt int64
	// LeaseOwner identifies the current worker.
	LeaseOwner string
	// LeaseGeneration fences writes from prior workers.
	LeaseGeneration int64
	// LeaseExpiresAt is written using the Store's clock.
	LeaseExpiresAt time.Time
	// Checkpoint is the latest durable executor checkpoint.
	Checkpoint *CheckpointRef
	// ResultRef is the authoritative terminal result descriptor.
	ResultRef *ResultRef
	// LatestUpdateSequence is the exclusive cursor for subsequent update reads.
	LatestUpdateSequence int64
	// LatestProgress is a cheap projection of the latest progress update.
	LatestProgress *Progress
	// ResumeData and ResumeEncoding are atomically attached by Resume.
	ResumeData     []byte
	ResumeEncoding string
	// CancelRequestedAt records durable explicit stop intent.
	CancelRequestedAt *time.Time
	// CancelTransitionVersion fences the adjacent cancel reconciliation.
	CancelTransitionVersion int64
	// TerminalReason is a bounded machine-readable terminal explanation.
	TerminalReason string
	// UpdatedAt is the Store mutation time.
	UpdatedAt time.Time

	// Status is the current lifecycle status.
	Status Status
	// DoneAt is the time the task reached a terminal state. Nil if still running.
	DoneAt *time.Time
}

// TaskEventType describes the lifecycle transition that caused a task event.
type TaskEventType string

const (
	// TaskEventCreated indicates a task was durably submitted.
	TaskEventCreated TaskEventType = "created"
	// TaskEventBackgrounded indicates a foreground task moved to the background.
	TaskEventBackgrounded TaskEventType = "backgrounded"
	// TaskEventCompleted indicates a task finished successfully.
	TaskEventCompleted TaskEventType = "completed"
	// TaskEventFailed indicates a task finished with an error.
	TaskEventFailed TaskEventType = "failed"
	// TaskEventCanceled indicates a task was canceled by Cancel / Close.
	TaskEventCanceled TaskEventType = "canceled"
)

// TaskEvent is a lifecycle event published by Manager.Subscribe.
type TaskEvent struct {
	// Type is the transition that caused this event.
	Type TaskEventType
	// Task is the task snapshot immediately after the transition.
	Task *Task
}

// RunInput is the execution-agnostic input for Run.
// Domain-specific parameters (which agent, which command, the prompt) are
// captured by the WorkFunc closure, not here.
type RunInput struct {
	// Description is a short human-readable title for the task, stored in Task.Description.
	Description string
	// Type is an optional tag for the task (e.g. "bash", "subagent"), stored in
	// Task.Type. See Task.Type.
	Type string
	// ToolUseID is the optional id of the tool call launching this task, stored in
	// Task.ToolUseID. See Task.ToolUseID.
	ToolUseID string
	// RunInBackground starts the task in the background. Run returns an initial
	// StatusRunning snapshot without waiting for the work. RunStream returns its
	// caller-facing stream without waiting; consuming that stream normally yields
	// the background notice, optionally preceded by a bounded startup preview. If
	// the work reaches a terminal state during the preview, the stream instead
	// ends after forwarding all work chunks, without a background notice.
	RunInBackground bool
	// BackgroundStartupPreviewMs keeps an explicit-background RunStream caller's
	// stream open for up to this many milliseconds and forwards work chunks emitted
	// during that startup window. When the window expires, RunStream appends the
	// normal background notice, closes the caller stream, and drains the remaining
	// work output in the background. This lets launch-time information such as an
	// OAuth URL remain visible without changing the task's background lifecycle. If
	// the work finishes during the window, all chunks are forwarded and the caller
	// stream closes without a background notice; the task is already terminal then.
	// A value <= 0 disables the preview. Ignored by Run and by foreground RunStream
	// executions (including their later auto-background transition).
	//
	// The window is measured from when the StreamWorkFunc returns its reader (see
	// StreamWorkFunc), so it bounds the preview of streamed output, not the work's
	// initialization; streaming work must return its reader promptly for the window
	// to be meaningful.
	BackgroundStartupPreviewMs int
	// ForegroundTimeoutMs optionally overrides the Manager's foreground timeout for
	// this run only. When nil, the Manager's configured default applies. When non-nil,
	// it bounds how long the run may occupy the foreground before its deadline fires
	// (see Config.ShouldAutoBackground for what happens at the deadline). A value <= 0
	// removes the deadline for this run (blocks until completion). Ignored when
	// RunInBackground is true.
	//
	// For Run the deadline is measured from when the work starts; for RunStream it is
	// measured from when the work returns its stream reader (see StreamWorkFunc), so
	// streaming work must return its reader promptly for the two to coincide.
	ForegroundTimeoutMs *int
}

// defaultForegroundTimeoutMs is the default foreground timeout (120 seconds).
const defaultForegroundTimeoutMs = 120_000

// IDGenerator returns the complete ID for a new task.
//
// The generator sees the run input before the task is registered and may return a
// business-side identifier. Manager does not add the task-type prefix when IDGen
// is configured; callers that want one should include it in the returned ID.
type IDGenerator func(ctx context.Context, input *RunInput) (string, error)

// Config configures a Manager.
type Config struct {
	// Store is the authoritative task state provider. When nil, New installs an
	// in-memory reference store.
	Store Store
	// Executors resolves serialized task intent to local implementations.
	Executors *ExecutorRegistry
	// WorkerID identifies this Manager instance when it claims tasks. It must be
	// unique among concurrently active Manager instances sharing a Store. When
	// empty, New generates a process-local identity.
	WorkerID string
	// LeaseDuration is the worker lease requested by Execute.
	LeaseDuration time.Duration
	// ForegroundTimeoutMs sets the foreground timeout: the time a foreground run is
	// allowed to occupy the foreground before its deadline fires.
	// When > 0, a foreground run that hasn't completed within this many
	// milliseconds reaches its deadline (see ShouldAutoBackground for what happens then).
	// When 0, there is no deadline (foreground runs block until completion).
	//
	// Default: 120000ms (120 seconds).
	ForegroundTimeoutMs *int

	// ShouldAutoBackground decides, at a foreground run's deadline, whether it may be
	// moved to the background (kept running) instead of being canceled. Applications
	// can use it to permit long-lived workloads such as servers and watchers. The
	// hook receives the canonical task, so a host can branch on Task.Spec.Type and
	// decode typed intent from Task.Spec.
	//
	// Deciding whether a workload is genuinely long-lived is inherently host- and
	// command-specific, so this package ships no built-in policy: the framework
	// cannot reliably infer "never exits" from a command string, and a wrong guess
	// either kills a useful run or keeps a doomed one. Hosts encode their own rules.
	//
	// It is consulted ONLY for the auto path — a foreground run that hits its
	// deadline. An explicit RunInBackground run always backgrounds immediately,
	// regardless of this hook.
	//
	// When nil (the default), it is treated as always returning false: a run that
	// hits its deadline is canceled and reported as timed out, never auto-backgrounded.
	ShouldAutoBackground func(ctx context.Context, task *Task) bool

	// IDGen, when set, decides the full ID of every task created by this Manager.
	// If nil, Manager uses its default task-type-prefixed base62 ID.
	//
	// IDGen may be called concurrently by concurrent Run / RunStream calls. It
	// must return a non-empty ID. The returned ID must be unique among this
	// Manager's registered tasks; a duplicate fails task creation.
	IDGen IDGenerator

	// BackgroundNotice customizes the chunk emitted on a RunStream caller's stream
	// when a task starts in the background or is auto-moved there. The Manager owns
	// only lifecycle facts (id, type, output file); how a host tells the model to
	// retrieve the result is host-specific — one host exposes a task_output tool,
	// another points at the output file — so that wording does not belong in this
	// type-erased layer.
	//
	// When nil, defaultBackgroundNotice is used: it announces the background launch
	// and, when an output file is reserved, directs the reader to Read that path for
	// interim output.
	//
	// The ctx passed to the hook is the run's context (detached from the caller's
	// cancellation, carrying its values); use it only for value lookup, not to gate
	// the notice on cancellation.
	BackgroundNotice func(ctx context.Context, info NoticeInfo) string
}

// NoticeInfo carries the lifecycle facts a BackgroundNotice hook may use to build
// the chunk shown when a run goes to the background.
type NoticeInfo struct {
	// Task is a snapshot of the task at the moment the notice is emitted.
	Task *Task
	// AutoBackgrounded is false when the run was launched directly in the background
	// (RunInBackground), and true when a foreground run was auto-moved to the
	// background at its deadline because the ShouldAutoBackground hook permitted it
	// (a deadline the hook declines becomes a timeout failure, which never reaches
	// this notice). The true case is the same transition reported to subscribers as
	// TaskEventBackgrounded.
	AutoBackgrounded bool
}

// Manager is a non-generic, in-memory registry that owns the lifecycle of
// managed executions: creation, foreground/background/auto-background
// switching, cancellation and terminal-state tracking.
//
// It is intentionally execution-agnostic: it does not know whether a task is an
// agent or a shell command. Callers launch work via the free function Run,
// passing a WorkFunc that performs the actual execution. A single Manager can
// therefore be shared across multiple domains under one task-ID space.
type Manager struct {
	store         Store
	executors     *ExecutorRegistry
	workerID      string
	leaseDuration time.Duration
	workersMu     sync.Mutex
	workers       map[string]*workerExecution
	submittedMu   sync.RWMutex
	submitted     map[string]struct{}
	local         *processLocalExecutor

	mu                   sync.Mutex
	seq                  int64
	lastMs               int64
	closed               bool
	foregroundTimeoutMs  int
	shouldAutoBackground func(ctx context.Context, task *Task) bool
	idGen                IDGenerator
	backgroundNoticeFn   func(ctx context.Context, info NoticeInfo) string

	subscribeOnce sync.Once
	eventCh       chan *TaskEvent
	eventBuf      *internal.UnboundedChan[*TaskEvent]
}

// New creates a new Manager.
// By default, the foreground timeout is 120 seconds; set Config.ForegroundTimeoutMs
// to 0 to remove the deadline (foreground runs block until completion). What
// happens when the timeout is reached is governed by Config.ShouldAutoBackground
// (default: cancel the run and report it timed out).
func New(_ context.Context, conf *Config) *Manager {
	m := &Manager{
		foregroundTimeoutMs: defaultForegroundTimeoutMs,
		workerID:            newManagerWorkerID(),
		leaseDuration:       30 * time.Second,
		workers:             make(map[string]*workerExecution),
		submitted:           make(map[string]struct{}),
	}
	m.store = NewMemoryStore(nil)
	m.executors = NewExecutorRegistry()
	m.local = newProcessLocalExecutor()
	if conf != nil && conf.ForegroundTimeoutMs != nil {
		m.foregroundTimeoutMs = *conf.ForegroundTimeoutMs
	}
	if conf != nil {
		if conf.Store != nil {
			m.store = conf.Store
		}
		if conf.Executors != nil {
			m.executors = conf.Executors
		}
		if conf.WorkerID != "" {
			m.workerID = conf.WorkerID
		}
		if conf.LeaseDuration > 0 {
			m.leaseDuration = conf.LeaseDuration
		}
		m.shouldAutoBackground = conf.ShouldAutoBackground
		m.idGen = conf.IDGen
		m.backgroundNoticeFn = conf.BackgroundNotice
	}
	if _, exists := m.executors.Resolve(processLocalExecutorKey); !exists {
		_ = m.executors.Register(m.local)
	}
	return m
}

// Subscribe returns a channel that receives TaskEvent values whenever the Manager
// changes a task's lifecycle state.
//
// The stream is forward-only: events generated before the first Subscribe call
// are not replayed (use Get/List to inspect current state). Multiple calls return
// the same shared stream, and Close closes it after buffered events are drained.
// The returned Task values are snapshots; mutating them does not mutate the
// Manager's registry.
func (m *Manager) Subscribe() <-chan *TaskEvent {
	m.subscribeOnce.Do(func() {
		buf := internal.NewUnboundedChan[*TaskEvent]()
		ch := make(chan *TaskEvent)

		m.mu.Lock()
		m.eventBuf = buf
		m.eventCh = ch
		closed := m.closed
		m.mu.Unlock()

		go m.relayEvents(buf, ch)
		if closed {
			buf.Close()
		}
	})
	return m.eventCh
}

// relayEvents pumps events from the unbounded buffer to the public channel,
// so publishing under the Manager lock never blocks on a slow subscriber.
func (m *Manager) relayEvents(buf *internal.UnboundedChan[*TaskEvent], ch chan<- *TaskEvent) {
	defer close(ch)
	for {
		event, ok := buf.Receive()
		if !ok {
			return
		}
		ch <- event
	}
}

// allowAutoBackground reports whether a run that has hit its foreground deadline
// may be moved to the background. With no configured hook, the answer is false.
func (m *Manager) allowAutoBackground(ctx context.Context, task *Task) bool {
	if m.shouldAutoBackground == nil {
		return false
	}
	return m.shouldAutoBackground(ctx, task)
}

// Get returns the current state of a task by ID.
// Returns (nil, false) if the task does not exist.
func (m *Manager) Get(id string) (*Task, bool) {
	if task, err := m.store.Get(context.Background(), id); err == nil {
		return task, true
	}
	return nil, false
}

// Wait blocks until the task with the given id reaches a terminal state, or until
// ctx is canceled, and returns the task's current snapshot together with whether it
// actually reached a terminal state. Callers bound the wait with ctx (e.g.
// context.WithTimeout).
//
// Return values:
//   - (nil, false): no task with this id exists.
//   - (task, true): the task reached a terminal state (task.Status is terminal).
//   - (task, false): ctx was canceled/timed out first; task is the latest
//     (still-running) snapshot.
//
// The wait is per-task: it selects on the task's own done channel rather than the
// shared condition, so it neither holds m.mu while waiting nor is woken when other
// tasks finish.
func (m *Manager) Wait(ctx context.Context, id string) (*Task, bool) {
	if task, err := m.store.Get(ctx, id); err == nil {
		for !terminalStatus(task.Status) {
			task, err = m.store.Wait(ctx, &WaitTaskRequest{
				TaskID: id, AfterVersion: task.TransitionVersion,
			})
			if err != nil {
				current, getErr := m.store.Get(context.Background(), id)
				return current, getErr == nil && current != nil && terminalStatus(current.Status)
			}
		}
		return task, true
	}
	return nil, false
}

// List returns a snapshot of all tasks (both running and completed).
func (m *Manager) List() []*Task {
	m.submittedMu.RLock()
	ids := make([]string, 0, len(m.submitted))
	for id := range m.submitted {
		ids = append(ids, id)
	}
	m.submittedMu.RUnlock()
	durable := make([]*Task, 0, len(ids))
	for _, id := range ids {
		if task, err := m.store.Get(context.Background(), id); err == nil {
			durable = append(durable, task)
		}
	}
	return durable
}

// Cancel stops a running task. The run's context is canceled and the task
// transitions to StatusCanceled.
// Returns an error if the task does not exist or is not running.
func (m *Manager) Cancel(id string) error {
	if _, err := m.store.Get(context.Background(), id); err != nil {
		return fmt.Errorf("no background task has id %q", id)
	}
	_, err := m.RequestCancel(context.Background(), id)
	return err
}

// Close performs graceful shutdown.
// It waits for all running tasks to complete (up to the ctx deadline),
// then cancels any remaining running tasks.
// After Close returns, Run will return an error.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var closeErr error
	for {
		m.workersMu.Lock()
		active := len(m.workers)
		for _, worker := range m.workers {
			if worker == nil {
				continue
			}
			worker.runtime.requestControl(ControlDrain)
		}
		m.workersMu.Unlock()
		if active == 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			m.workersMu.Lock()
			for _, worker := range m.workers {
				if worker != nil {
					worker.cancel()
				}
			}
			m.workersMu.Unlock()
			closeErr = ctx.Err()
			goto closed
		}
	}

closed:
	m.mu.Lock()
	if m.eventBuf != nil {
		m.eventBuf.Close()
	}
	m.mu.Unlock()

	return closeErr
}

func (m *Manager) closedError() error {
	return fmt.Errorf("the background task manager has shut down and is no longer accepting new tasks. " +
		"Do not retry this; finish using any results you already have")
}

const canceledError = "task was canceled"

func eventTypeForStatus(status Status) TaskEventType {
	switch status {
	case StatusCompleted:
		return TaskEventCompleted
	case StatusFailed:
		return TaskEventFailed
	case StatusCanceled:
		return TaskEventCanceled
	default:
		return ""
	}
}

func cloneTask(t *Task) *Task {
	clone := *t
	clone.Spec = cloneSpec(t.Spec)
	clone.Checkpoint = cloneCheckpoint(t.Checkpoint)
	clone.ResultRef = cloneResult(t.ResultRef)
	clone.LatestProgress = cloneProgress(t.LatestProgress)
	clone.ResumeData = append([]byte(nil), t.ResumeData...)
	clone.CancelRequestedAt = cloneTime(t.CancelRequestedAt)
	clone.DoneAt = cloneTime(t.DoneAt)
	return &clone
}

// TaskInfo is a read-only snapshot of the facts the Manager establishes about a
// task at creation, handed to the WorkFunc when it starts. It is not the live
// Task record: it carries only identity fixed at creation, never the mutable
// lifecycle fields, so work never races on them.
type TaskInfo struct {
	// ID is the Manager-generated task id. It is the one fact the work cannot
	// otherwise obtain: the id is assigned before the closure is registered.
	ID string
	// Backgrounded is closed when this task moves to background execution: before
	// the work starts for an explicit RunInBackground launch, or at the
	// auto-background transition (foreground deadline reached and permitted). It
	// stays open for a run that completes in the foreground. Work uses it to stop
	// side effects that are only valid while the run is foreground — most notably
	// forwarding events to the launching turn's live stream, which the turn closes
	// once it ends. It is never nil.
	Backgrounded <-chan struct{}
}

// WorkFunc performs a single managed execution. It is supplied by the caller
// (e.g. a subagent or filesystem adapter); the Manager itself never knows what
// the work is.
//
// task carries the Manager-assigned facts about this run (see TaskInfo).
//
// ctx carries the values of the Run call's context but is detached from its
// cancellation, so a backgrounded task outlives the turn that launched it. It is
// canceled when Cancel is invoked for this task, when a foreground deadline or an
// abandoned foreground wait stops it, or when the Manager is closed. Work should
// honor it.
//
// The returned result becomes the canonical ResultRef payload; a non-nil error
// transitions the task to failed.
type WorkFunc func(ctx context.Context, task TaskInfo) (result string, err error)

// detachedCtx carries its parent's values but is never canceled by the parent.
// It mirrors context.WithoutCancel (Go 1.21+); this package targets Go 1.18.
// Background work runs under a detachedCtx (wrapped by a fresh cancelable context)
// so it survives cancellation of the per-turn context that launched it, while
// still seeing that context's values.
type detachedCtx struct{ parent context.Context }

func (detachedCtx) Deadline() (deadline time.Time, ok bool) { return time.Time{}, false }

func (detachedCtx) Done() <-chan struct{} { return nil }

func (detachedCtx) Err() error { return nil }

func (c detachedCtx) Value(key any) any { return c.parent.Value(key) }

// Run executes work as a managed task on m.
//
// The execution mode depends on input.RunInBackground and the effective foreground
// timeout (input.ForegroundTimeoutMs if set, else the Manager's configured default):
//   - Foreground (RunInBackground=false, timeout<=0): blocks until completion
//   - Background (RunInBackground=true): returns an initial StatusRunning snapshot
//     without waiting for work. The task may complete immediately afterward; use
//     Get or task events to observe its current state.
//   - Deadline (timeout>0): runs in foreground up to the timeout, then — if still
//     running — consults the Manager's ShouldAutoBackground hook. If it permits,
//     the run is moved to the background (kept running) and Run returns
//     StatusRunning. Otherwise the run is canceled and reported as timed out
//     (StatusFailed).
//
// All runs are tracked in Manager state and visible via Get/List.
func (m *Manager) Run(ctx context.Context, input *RunInput, work WorkFunc) (*Task, error) {
	task, entry, err := m.submitProcessLocal(ctx, input, work)
	if err != nil {
		return nil, err
	}
	id := task.Spec.ID
	if input.RunInBackground {
		entry.markBackgrounded()
	}
	done := make(chan error, 1)
	go func() {
		defer m.local.remove(id)
		executeErr := m.Execute(detachedCtx{parent: ctx}, id)
		if current, getErr := m.store.Get(context.Background(), id); getErr == nil {
			m.sendTaskEvent(current, eventTypeForStatus(current.Status))
		}
		done <- executeErr
	}()
	select {
	case <-entry.started:
		if current, getErr := m.store.Get(context.Background(), id); getErr == nil {
			m.sendTaskEvent(current, TaskEventCreated)
		}
		entry.allowStart()
	case executeErr := <-done:
		if executeErr != nil {
			return nil, executeErr
		}
		return m.processLocalSnapshot(id), nil
	}

	if input.RunInBackground {
		current := m.processLocalSnapshot(id)
		m.sendTaskEvent(current, TaskEventBackgrounded)
		return current, nil
	}

	foregroundTimeoutMs := m.foregroundTimeoutMs
	if input.ForegroundTimeoutMs != nil {
		foregroundTimeoutMs = *input.ForegroundTimeoutMs
	}
	var timeout <-chan time.Time
	var timer *time.Timer
	if foregroundTimeoutMs > 0 {
		timer = time.NewTimer(time.Duration(foregroundTimeoutMs) * time.Millisecond)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case executeErr := <-done:
		if executeErr != nil {
			return nil, executeErr
		}
		current := m.processLocalSnapshot(id)
		return current, nil
	case <-ctx.Done():
		_, _ = m.RequestCancel(context.Background(), id)
		return m.processLocalSnapshot(id), nil
	case <-timeout:
		current := m.processLocalSnapshot(id)
		if current != nil && !terminalStatus(current.Status) && m.allowAutoBackground(ctx, current) {
			entry.markBackgrounded()
			m.sendTaskEvent(current, TaskEventBackgrounded)
			return current, nil
		}
		_, _ = m.RequestCancel(context.Background(), id)
		return m.processLocalSnapshot(id), nil
	}
}

func (m *Manager) processLocalSnapshot(id string) *Task {
	task, err := m.store.Get(context.Background(), id)
	if err != nil {
		return nil
	}
	return task
}

func (m *Manager) sendTaskEvent(task *Task, eventType TaskEventType) {
	if task == nil || eventType == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.eventBuf != nil {
		m.eventBuf.TrySend(&TaskEvent{Type: eventType, Task: cloneTask(task)})
	}
}

// StreamWorkFunc performs a single managed streaming execution. It is the
// streaming counterpart of WorkFunc: instead of returning the whole result at
// once, it returns output chunks. Manager projects those chunks live and
// concatenates them into the canonical ResultRef.
//
// task behaves exactly as for WorkFunc (see TaskInfo): it carries the task id the
// work uses to report an output-file write failure.
//
// ctx behaves exactly as for WorkFunc (see WorkFunc): detached from the caller's
// cancellation, stopped by Cancel/deadline/Close. Work should honor it and close
// the returned reader when ctx is done.
//
// Return the reader promptly. The Manager's foreground timeout and
// RunInput.BackgroundStartupPreviewMs windows are measured from when this function
// returns its reader, not from the RunStream call — RunStream calls it synchronously
// and only starts those timers afterward. So blocking initialization (spawning a
// process, waiting for a subprocess to be ready, dialing) must be done in the
// producer goroutine that writes to the reader and surfaced as its first chunks, not
// before returning. Work that blocks before returning makes the caller wait for
// init-time plus the window rather than the window alone, and that extra wait is not
// bounded by either timeout.
type StreamWorkFunc func(ctx context.Context, task TaskInfo) (*schema.StreamReader[string], error)

// RunStream executes streaming work as a managed task, returning a stream of
// output chunks to consume in real time.
//
// It mirrors Run's lifecycle (tracking, foreground timeout, auto-background) but
// preserves streaming for the foreground phase:
//   - Foreground completion: every chunk is forwarded live, then the stream closes.
//   - Auto-background at the deadline: chunks forwarded so far are kept; the
//     Manager appends a single notice chunk (task id) and closes the
//     caller's stream, while the work keeps running in the background — its
//     remaining output is drained into the task's ResultRef.
//   - Explicit background (input.RunInBackground): the work runs detached from the
//     start. By default no execution chunks reach the caller; when
//     input.BackgroundStartupPreviewMs is positive, chunks emitted during that
//     bounded startup window are forwarded. If work finishes during the preview,
//     all chunks are forwarded and the stream closes with no background notice.
//     Otherwise the notice ends the preview and the remaining output is drained
//     into the task's ResultRef.
//
// The foreground and preview windows are both measured from when the work returns
// its stream reader (see StreamWorkFunc), not from this call: RunStream's forwarding
// goroutine starts the timers only after work returns its reader. Blocking
// initialization performed before the reader is returned is therefore not counted
// against either window — streaming work must return its reader promptly for the
// windows to reflect output time rather than startup time.
//
// The returned reader is always non-nil on a nil error. The Manager is the sole
// writer of that stream, so there is never a write race with the work.
func (m *Manager) RunStream(ctx context.Context, input *RunInput, work StreamWorkFunc) (*schema.StreamReader[string], error) {
	if input == nil || work == nil {
		return nil, errors.New("backgroundtask: RunInput and StreamWorkFunc are required")
	}
	sr, sw := schema.Pipe[string](streamBufferCap)
	go m.runStreamProjection(ctx, input, work, sw)
	return sr, nil
}

func (m *Manager) runStreamProjection(
	ctx context.Context,
	input *RunInput,
	work StreamWorkFunc,
	writer *schema.StreamWriter[string],
) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	chunks := make(chan streamChunk, streamBufferCap)
	ready := make(chan struct{})
	var readyOnce sync.Once
	signalReady := func() { readyOnce.Do(func() { close(ready) }) }
	var chunksOnce sync.Once
	closeChunks := func() { chunksOnce.Do(func() { close(chunks) }) }
	taskResult := make(chan *Task, 1)
	go func() {
		task, _ := m.Run(runCtx, input, func(workCtx context.Context, info TaskInfo) (result string, resultErr error) {
			defer func() {
				if panicValue := recover(); panicValue != nil {
					resultErr = safe.NewPanicErr(panicValue, debug.Stack())
					signalReady()
					chunks <- streamChunk{err: resultErr}
					closeChunks()
				}
			}()
			reader, err := work(workCtx, info)
			if err != nil {
				signalReady()
				chunks <- streamChunk{err: err}
				closeChunks()
				return "", err
			}
			if reader == nil {
				err = errors.New("backgroundtask: StreamWorkFunc returned a nil reader")
				signalReady()
				chunks <- streamChunk{err: err}
				closeChunks()
				return "", err
			}
			signalReady()
			defer reader.Close()
			var output strings.Builder
			for {
				chunk, recvErr := reader.Recv()
				if recvErr == io.EOF {
					closeChunks()
					return output.String(), nil
				}
				if recvErr != nil {
					chunks <- streamChunk{err: recvErr}
					closeChunks()
					return "", recvErr
				}
				if reportErr := ReportUpdate(
					workCtx, "eino.dev/process-local-stream", "text/plain",
					[]byte(chunk), "text/plain",
				); reportErr != nil {
					chunks <- streamChunk{err: reportErr}
					closeChunks()
					return "", reportErr
				}
				output.WriteString(chunk)
				chunks <- streamChunk{text: chunk}
			}
		})
		taskResult <- task
	}()

	var task *Task
	var preview <-chan time.Time
	if input.RunInBackground && input.BackgroundStartupPreviewMs > 0 {
		<-ready
		timer := time.NewTimer(time.Duration(input.BackgroundStartupPreviewMs) * time.Millisecond)
		defer timer.Stop()
		preview = timer.C
	}
	forward := !input.RunInBackground || input.BackgroundStartupPreviewMs > 0
	callerOpen := true
	for chunks != nil || task == nil {
		select {
		case current := <-taskResult:
			task = current
			deferNotice := input.RunInBackground && preview != nil
			if task != nil && !terminalStatus(task.Status) && callerOpen && !deferNotice {
				forward = false
				notice := m.backgroundStartNotice(detachedCtx{parent: ctx}, task.Spec.ID)
				if !input.RunInBackground {
					notice = m.backgroundMoveNotice(detachedCtx{parent: ctx}, task.Spec.ID)
				}
				writer.Send(notice, nil)
				writer.Close()
				callerOpen = false
			}
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				if task != nil && callerOpen {
					if current, done := m.Wait(ctx, task.Spec.ID); done {
						task = current
					}
				}
				continue
			}
			if !callerOpen || !forward {
				continue
			}
			if chunk.err != nil {
				writer.Send("", chunk.err)
				writer.Close()
				callerOpen = false
				continue
			}
			if writer.Send(chunk.text, nil) && !input.RunInBackground {
				cancel()
				callerOpen = false
			}
		case <-preview:
			preview = nil
			forward = false
			if task == nil {
				task = <-taskResult
			}
			if task != nil {
				if current, ok := m.Get(task.Spec.ID); ok {
					task = current
				}
			}
			if task != nil && !terminalStatus(task.Status) && callerOpen {
				writer.Send(m.backgroundStartNotice(detachedCtx{parent: ctx}, task.Spec.ID), nil)
				writer.Close()
				callerOpen = false
			}
		case <-ctx.Done():
			if !input.RunInBackground {
				cancel()
			}
			if callerOpen {
				writer.Close()
				callerOpen = false
			}
		}
	}
	if callerOpen {
		writer.Close()
	}
}

type streamChunk struct {
	text string
	err  error
}

// backgroundStartNotice builds the chunk emitted for an explicit RunInBackground
// launch.
func (m *Manager) backgroundStartNotice(ctx context.Context, id string) string {
	return m.notice(ctx, id, false)
}

// backgroundMoveNotice builds the chunk appended when a foreground run is moved to
// the background by the auto-background policy.
func (m *Manager) backgroundMoveNotice(ctx context.Context, id string) string {
	return m.notice(ctx, id, true)
}

// notice produces the background-launch chunk: the configured BackgroundNotice
// hook when set, otherwise defaultBackgroundNotice. It snapshots the task so the
// hook sees the same lifecycle facts (id, type, output file) the default would.
func (m *Manager) notice(ctx context.Context, id string, autoBackgrounded bool) string {
	task, _ := m.Get(id)
	info := NoticeInfo{Task: task, AutoBackgrounded: autoBackgrounded}
	if m.backgroundNoticeFn != nil {
		return m.backgroundNoticeFn(ctx, info)
	}
	return defaultBackgroundNotice(info)
}

const noticeTemplate = "\n[task {id}{kind} {state}.]"

// defaultBackgroundNotice is the built-in BackgroundNotice. It announces the
// background launch and, when an output file is reserved, directs the reader to
// Read that path for interim output. It deliberately names no control tool, since
// the retrieval mechanism is host-specific (see Config.BackgroundNotice).
func defaultBackgroundNotice(info NoticeInfo) string {
	id, kind := "", ""
	if info.Task != nil {
		id = info.Task.Spec.ID
		if info.Task.Spec.Type != "" {
			kind = " (" + info.Task.Spec.Type + ")"
		}
	}

	state := "is running in the background"
	if info.AutoBackgrounded {
		state = "moved to the background"
	}

	return strings.NewReplacer(
		"{id}", id,
		"{kind}", kind,
		"{state}", state,
	).Replace(noticeTemplate)
}

// streamBufferCap is the buffer size of the caller-facing stream pipe.
const streamBufferCap = 16
