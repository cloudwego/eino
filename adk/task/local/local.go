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

// Package local executes non-serializable process-local closures as managed tasks.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/internal/startwindow"
	"github.com/cloudwego/eino/adk/internal/taskfirst"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	taskforeground "github.com/cloudwego/eino/adk/task/foreground"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

const executorKey = "eino.dev/process-local"

// WorkFunc performs buffered process-local work. Runtime is nil for direct
// parent-owned foreground execution and non-nil for Manager-owned execution.
type WorkFunc func(
	ctx context.Context,
	runtime background.ExecutionRuntime,
) (string, error)

// StreamWorkFunc performs streaming process-local work. Runtime follows the
// same ownership rule as WorkFunc.
type StreamWorkFunc func(
	ctx context.Context,
	runtime background.ExecutionRuntime,
) (*schema.StreamReader[string], error)

// Input describes one process-local execution. ForegroundTimeoutMs overrides
// Config.ForegroundTimeoutMs. RunInBackground bypasses foreground timeout;
// BackgroundStartupPreviewMs applies only to its streaming startup preview.
type Input struct {
	Description                string
	Kind                       string
	Payload                    []byte
	OutputFile                 string
	SessionID                  string
	NotifySession              bool
	RunInBackground            bool
	BackgroundStartupPreviewMs int
	ForegroundTimeoutMs        *int
}

// NoticeInfo carries lifecycle facts for a background stream notice. Task may
// be nil when authoritative loading fails.
type NoticeInfo struct {
	Task             *background.TaskSnapshot
	AutoBackgrounded bool
}

// ProjectionDetached returns a stable signal that closes when the task-first
// local execution in ctx detaches from its caller-facing foreground
// projection. It returns nil when ctx does not belong to such an execution.
func ProjectionDetached(ctx context.Context) <-chan struct{} {
	return taskfirst.ProjectionDetached(ctx)
}

// Config configures process-local execution and ephemeral foreground
// projection. Policy callbacks may run concurrently and must not panic or
// mutate the supplied candidate. Nil ShouldAutoBackground disables automatic
// detachment; nil BackgroundNotice uses the default notice.
type Config struct {
	Manager                   *background.Manager
	ForegroundTimeoutMs       *int
	ShouldAutoBackground      taskforeground.ShouldAutoBackground
	ShouldCancelOnCallerAbort taskforeground.ShouldCancelOnCallerAbort
	BackgroundNotice          func(context.Context, NoticeInfo) string
	// EventPersister serializes each Manager-owned stream chunk for durable
	// progress. Each call receives one chunk as Event and a nil Stream. Nil
	// uses the raw UTF-8 chunk as one final event part.
	EventPersister background.TaskEventPersister[string, string]
}

// RunResult is the result of buffered process-local execution. Exactly one of
// Foreground or Task reports a value for results returned by Runner.Run.
//
// Foreground reports a direct, non-durable outcome. Task reports the
// authoritative durable snapshot for Manager-owned or explicit background
// execution. ID is available for either form.
type RunResult struct {
	id         string
	foreground *task.Outcome
	task       *background.TaskSnapshot
}

// ID returns the logical execution ID, or an empty string for an invalid
// RunResult.
func (r *RunResult) ID() string {
	if !r.valid() {
		return ""
	}
	return r.id
}

// Foreground returns the direct, non-durable outcome when this result
// represents parent-owned foreground execution.
func (r *RunResult) Foreground() (*task.Outcome, bool) {
	if !r.valid() || r.foreground == nil {
		return nil, false
	}
	return r.foreground, true
}

// Task returns the authoritative durable snapshot when this result represents
// Manager-owned or explicit background execution.
func (r *RunResult) Task() (*background.TaskSnapshot, bool) {
	if !r.valid() || r.task == nil {
		return nil, false
	}
	return r.task, true
}

func (r *RunResult) valid() bool {
	if r == nil || r.id == "" || (r.foreground == nil) == (r.task == nil) {
		return false
	}
	return r.task == nil || r.task.Spec.ID == r.id
}

func newForegroundRunResult(id string, outcome *task.Outcome) (*RunResult, error) {
	if id == "" || outcome == nil || outcome.Status == task.OutcomeUnknown {
		return nil, errors.New("task/local: invalid foreground run result")
	}
	return &RunResult{id: id, foreground: outcome}, nil
}

func newTaskRunResult(snapshot *background.TaskSnapshot) (*RunResult, error) {
	if snapshot == nil || snapshot.Spec.ID == "" {
		return nil, errors.New("task/local: invalid durable run result")
	}
	return &RunResult{id: snapshot.Spec.ID, task: snapshot}, nil
}

// Runner owns one process-local closure registry for a Manager.
type Runner struct {
	manager          *background.Manager
	executor         *executor
	policy           taskfirst.Policy
	backgroundNotice func(context.Context, NoticeInfo) string
	eventPersister   background.TaskEventPersister[string, string]
}

// New constructs a Runner and registers its process-local executor.
func New(config *Config) (*Runner, error) {
	if config == nil || config.Manager == nil {
		return nil, errors.New("task/local: manager is required")
	}
	timeoutMs := taskforeground.DefaultTimeoutMs
	if config.ForegroundTimeoutMs != nil {
		timeoutMs = *config.ForegroundTimeoutMs
	}
	registered, _, err := config.Manager.LoadOrRegisterExecutor(
		&executor{works: make(map[string]WorkFunc)},
	)
	if err != nil {
		return nil, err
	}
	localExecutor, compatible := registered.(*executor)
	if !compatible {
		return nil, fmt.Errorf(
			"task/local: executor key %q is already registered",
			executorKey,
		)
	}
	eventPersister := config.EventPersister
	if eventPersister == nil {
		eventPersister = localTaskEventPersister{}
	}
	runner := &Runner{
		manager:  config.Manager,
		executor: localExecutor,
		policy: taskfirst.Policy{
			TimeoutMs: timeoutMs, ShouldAutoBackground: config.ShouldAutoBackground,
			ShouldCancelOnCallerAbort: config.ShouldCancelOnCallerAbort,
		},
		backgroundNotice: config.BackgroundNotice,
		eventPersister:   eventPersister,
	}
	if runner.backgroundNotice == nil {
		runner.backgroundNotice = defaultBackgroundNotice
	}
	return runner, nil
}

// Manager returns the shared lifecycle Manager.
func (r *Runner) Manager() *background.Manager {
	if r == nil {
		return nil
	}
	return r.manager
}

// Run executes buffered process-local work. Direct parent-owned execution
// returns a non-durable Foreground outcome. Manager-owned execution, including
// explicit background work, returns an authoritative durable Task snapshot. A
// rejected foreground timeout returns a *task.ForegroundTimeoutError.
func (r *Runner) Run(ctx context.Context, input *Input, work WorkFunc) (*RunResult, error) {
	if input == nil || work == nil {
		return nil, errors.New("task/local: input and work are required")
	}
	spec, err := r.newSpec(ctx, input)
	if err != nil {
		return nil, err
	}
	if input.RunInBackground || r.policy.ShouldAutoBackground != nil ||
		r.policy.ShouldCancelOnCallerAbort != nil {
		execution, startErr := r.startTask(ctx, input, spec, work)
		if startErr != nil {
			return nil, startErr
		}
		if input.RunInBackground {
			return newTaskRunResult(execution.Initial())
		}
		outcome, awaitErr := execution.Await(ctx)
		if awaitErr != nil {
			return nil, awaitErr
		}
		if outcome == nil || outcome.Task == nil {
			return nil, errors.New("task/local: foreground execution returned no task")
		}
		return newTaskRunResult(outcome.Task)
	}
	var parentExecution *task.ExecutionContext
	if execution, ok := task.ExecutionContextFromContext(ctx); ok {
		spec.ParentTaskID = execution.TaskID
		copy := execution
		parentExecution = &copy
	}
	registerRequest := &task.RegisterMailboxRequest{
		CandidateTaskID: spec.ID, InvocationID: "local:" + spec.ID,
		Identity: append([]byte(nil), spec.Payload...),
	}
	if parentExecution == nil {
		registerRequest.RootSessionID = spec.RootSessionID
	} else {
		registerRequest.ParentExecution = parentExecution
	}
	mailbox, err := r.manager.RegisterMailbox(ctx, registerRequest)
	if err != nil {
		return nil, err
	}
	return r.runForeground(ctx, input, spec, mailbox.Mailbox, work)
}

func (r *Runner) startTask(
	ctx context.Context,
	input *Input,
	spec background.Spec,
	work WorkFunc,
) (*taskfirst.Execution, error) {
	if err := r.executor.register(spec.ID, work); err != nil {
		return nil, err
	}
	policy := r.policy
	if input.ForegroundTimeoutMs != nil {
		policy.TimeoutMs = *input.ForegroundTimeoutMs
	}
	execution, err := taskfirst.Start(
		ctx,
		r.manager,
		&policy,
		&taskfirst.StartRequest{
			Spec: spec, ExplicitBackground: input.RunInBackground,
		},
	)
	if err != nil {
		r.executor.remove(spec.ID)
		return nil, err
	}
	return execution, nil
}

func (r *Runner) runForeground(
	ctx context.Context,
	input *Input,
	spec background.Spec,
	mailbox *task.Mailbox,
	work WorkFunc,
) (*RunResult, error) {
	finalizer := taskfirst.NewForegroundMailboxFinalizer(
		r.manager,
		spec.ID,
		mailbox.Generation,
		mailbox.ConsumedCursor,
	)
	workCtx, cancel := context.WithCancel(ctx)
	workCtx = task.WithExecutionContext(workCtx, task.ExecutionContext{
		TaskID: spec.ID, Owner: task.OwnerParent,
		Generation: mailbox.Generation, RootSessionID: spec.RootSessionID,
	})
	defer cancel()
	resultCh := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		result := struct {
			value string
			err   error
		}{}
		defer func() {
			if panicValue := recover(); panicValue != nil {
				result.err = safe.NewPanicErr(panicValue, debug.Stack())
			}
			resultCh <- result
		}()
		result.value, result.err = work(
			workCtx,
			nil,
		)
	}()
	timeoutMs := r.policy.TimeoutMs
	if input.ForegroundTimeoutMs != nil {
		timeoutMs = *input.ForegroundTimeoutMs
	}
	var timeout <-chan time.Time
	var timer *time.Timer
	if timeoutMs > 0 {
		timer = time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case result := <-resultCh:
		runResult, resultErr := newForegroundRunResult(
			spec.ID,
			directOutcome(result.value, result.err),
		)
		if resultErr != nil {
			return nil, resultErr
		}
		if result.err != nil {
			if finalizeErr := finalizer.Abandon(); finalizeErr != nil {
				return runResult, fmt.Errorf(
					"task/local: abandon foreground mailbox: %w",
					finalizeErr,
				)
			}
		} else {
			if finalizeErr := finalizer.SealIfIdle(); finalizeErr != nil {
				return runResult, fmt.Errorf(
					"task/local: seal foreground mailbox: %w",
					finalizeErr,
				)
			}
		}
		return runResult, nil
	case <-ctx.Done():
		runResult, resultErr := newForegroundRunResult(spec.ID, &task.Outcome{
			Status: task.OutcomeCanceled,
			Error:  ctx.Err().Error(),
		})
		if resultErr != nil {
			return nil, resultErr
		}
		return runResult, taskfirst.CombineForegroundErrors(nil, finalizer.Abandon())
	case <-timeout:
		cancel()
		return nil, taskfirst.CombineForegroundErrors(
			&task.ForegroundTimeoutError{
				Timeout: time.Duration(timeoutMs) * time.Millisecond,
				TaskID:  spec.ID,
			},
			finalizer.Abandon(),
		)
	}
}

func directOutcome(value string, err error) *task.Outcome {
	if err == nil {
		return &task.Outcome{
			Status: task.OutcomeCompleted,
			Data:   []byte(value),
		}
	}
	status := task.OutcomeFailed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = task.OutcomeCanceled
	}
	return &task.Outcome{Status: status, Error: err.Error()}
}

// RunStream executes streaming process-local work and returns its ephemeral
// caller-facing projection. Auto-backgroundable work is task-owned from the
// beginning but remains unpublished until the projection detaches. Closing a
// task-first foreground reader follows the configured caller-abort policy. A
// foreground timeout is returned as a *task.ForegroundTimeoutError.
func (r *Runner) RunStream(
	ctx context.Context,
	input *Input,
	work StreamWorkFunc,
) (*schema.StreamReader[string], error) {
	if input == nil || work == nil {
		return nil, errors.New("task/local: input and stream work are required")
	}
	taskOwned := input.RunInBackground ||
		r.policy.ShouldAutoBackground != nil ||
		r.policy.ShouldCancelOnCallerAbort != nil
	chunks := make(chan streamChunk, streamBufferCap)
	ready := make(chan error, 1)
	var readyOnce sync.Once
	signalReady := func(err error) {
		readyOnce.Do(func() {
			ready <- err
		})
	}
	adapter := func(
		workCtx context.Context,
		runtime background.ExecutionRuntime,
	) (result string, resultErr error) {
		defer close(chunks)
		defer func() {
			if panicValue := recover(); panicValue != nil {
				resultErr = safe.NewPanicErr(panicValue, debug.Stack())
				signalReady(resultErr)
			}
		}()
		reader, err := work(workCtx, runtime)
		if err != nil {
			signalReady(err)
			return "", err
		}
		if reader == nil {
			err = errors.New("task/local: StreamWorkFunc returned a nil reader")
			signalReady(err)
			return "", err
		}
		signalReady(nil)
		defer reader.Close()
		var output strings.Builder
		for {
			chunk, recvErr := reader.Recv()
			if recvErr == io.EOF {
				return output.String(), nil
			}
			if recvErr != nil {
				chunks <- streamChunk{err: recvErr}
				return "", recvErr
			}
			if taskOwned {
				if _, appendErr := background.PersistTaskEvent[string, string](
					workCtx,
					runtime,
					"",
					&background.TaskEventEnvelope[string, string]{Event: chunk},
					r.eventPersister,
				); appendErr != nil {
					chunks <- streamChunk{err: appendErr}
					return "", appendErr
				}
			}
			output.WriteString(chunk)
			chunks <- streamChunk{text: chunk}
		}
	}
	spec, err := r.newSpec(ctx, input)
	if err != nil {
		return nil, err
	}
	if !taskOwned {
		var parentExecution *task.ExecutionContext
		if execution, ok := task.ExecutionContextFromContext(ctx); ok {
			spec.ParentTaskID = execution.TaskID
			copy := execution
			parentExecution = &copy
		}
		registerRequest := &task.RegisterMailboxRequest{
			CandidateTaskID: spec.ID, InvocationID: "local:" + spec.ID,
			Identity: append([]byte(nil), spec.Payload...),
		}
		if parentExecution == nil {
			registerRequest.RootSessionID = spec.RootSessionID
		} else {
			registerRequest.ParentExecution = parentExecution
		}
		registered, registerErr := r.manager.RegisterMailbox(
			ctx,
			registerRequest,
		)
		if registerErr != nil {
			return nil, registerErr
		}
		return r.runForegroundStream(ctx, &foregroundStreamRun{
			spec: spec, mailbox: registered.Mailbox,
			work: adapter, chunks: chunks, ready: ready,
			timeoutMs: r.foregroundTimeoutMs(input),
		})
	}
	execution, err := r.startTask(ctx, input, spec, adapter)
	if err != nil {
		return nil, err
	}
	task := execution.Initial()
	if !input.RunInBackground {
		return r.awaitTaskFirstStreamConstruction(
			ctx,
			input,
			execution,
			chunks,
			ready,
		)
	}
	runDone := make(chan runResult, 1)
	go func() {
		current, waitErr := execution.WaitBoundary(context.Background())
		runDone <- runResult{task: current, err: waitErr}
	}()
	reader, writer := schema.Pipe[string](streamBufferCap)
	// A successful submission is the acknowledgement boundary for an explicit
	// background run. In particular, a StreamingShell may block while creating
	// its reader (for example, when adapting a synchronous RPC), but that must
	// not delay returning the persisted task to the caller.
	go r.projectStream(ctx, input, task.Spec.ID, chunks, runDone, writer)
	return reader, nil
}

type foregroundStreamRun struct {
	spec      background.Spec
	mailbox   *task.Mailbox
	work      WorkFunc
	chunks    <-chan streamChunk
	ready     <-chan error
	timeoutMs int
}

func (r *Runner) runForegroundStream(
	ctx context.Context,
	run *foregroundStreamRun,
) (*schema.StreamReader[string], error) {
	spec, mailbox := run.spec, run.mailbox
	finalizer := taskfirst.NewForegroundMailboxFinalizer(
		r.manager,
		spec.ID,
		mailbox.Generation,
		mailbox.ConsumedCursor,
	)
	workCtx, cancel := context.WithCancel(ctx)
	workCtx = task.WithExecutionContext(workCtx, task.ExecutionContext{
		TaskID: spec.ID, Owner: task.OwnerParent,
		Generation: mailbox.Generation, RootSessionID: spec.RootSessionID,
	})
	resultCh := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		value, err := run.work(
			workCtx,
			nil,
		)
		resultCh <- struct {
			value string
			err   error
		}{value: value, err: err}
	}()
	select {
	case err := <-run.ready:
		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				cancel()
				return nil, taskfirst.CombineForegroundErrors(
					ctxErr,
					finalizer.Abandon(),
				)
			}
			break
		}
		cancel()
		return nil, taskfirst.CombineForegroundErrors(err, finalizer.Abandon())
	case <-ctx.Done():
		cancel()
		return nil, taskfirst.CombineForegroundErrors(ctx.Err(), finalizer.Abandon())
	}
	reader, writer := schema.Pipe[string](streamBufferCap)
	go r.projectForegroundStream(&foregroundStreamProjection{
		ctx: ctx, chunks: run.chunks,
		resultCh: resultCh, writer: writer, cancel: cancel,
		finalizer: finalizer, taskID: spec.ID, timeoutMs: run.timeoutMs,
	})
	return reader, nil
}

func (r *Runner) foregroundTimeoutMs(input *Input) int {
	timeoutMs := r.policy.TimeoutMs
	if input != nil && input.ForegroundTimeoutMs != nil {
		timeoutMs = *input.ForegroundTimeoutMs
	}
	return timeoutMs
}

func (r *Runner) submit(
	ctx context.Context,
	input *Input,
	work WorkFunc,
) (*background.TaskSnapshot, error) {
	if r == nil || r.manager == nil || r.executor == nil || input == nil || work == nil {
		return nil, errors.New("task/local: runner, input, and work are required")
	}
	spec, err := r.newSpec(ctx, input)
	if err != nil {
		return nil, err
	}
	return r.submitSpec(ctx, spec, work)
}

func (r *Runner) newSpec(ctx context.Context, input *Input) (background.Spec, error) {
	if r == nil || r.manager == nil || input == nil {
		return background.Spec{}, errors.New("task/local: runner and input are required")
	}
	id, err := r.manager.AllocateTaskID(ctx, &background.AllocateTaskIDRequest{
		Kind: input.Kind,
	})
	if err != nil {
		return background.Spec{}, err
	}
	spec := background.Spec{
		ID: id, ExecutorKey: executorKey, Kind: input.Kind,
		Payload: append([]byte(nil), input.Payload...), Description: input.Description,
		OutputFile: input.OutputFile, RootSessionID: input.SessionID,
		NotifySession: input.NotifySession,
	}
	if execution, ok := task.ExecutionContextFromContext(ctx); ok {
		spec.ParentTaskID = execution.TaskID
		if execution.RootSessionID != "" {
			spec.RootSessionID = execution.RootSessionID
		}
	}
	return spec, nil
}

func (r *Runner) submitSpec(
	ctx context.Context,
	spec background.Spec,
	work WorkFunc,
) (*background.TaskSnapshot, error) {
	if err := r.executor.register(spec.ID, work); err != nil {
		return nil, err
	}
	task, err := r.manager.Submit(ctx, &background.SubmitRequest{Spec: spec})
	if err != nil && !errors.Is(err, background.ErrTaskCreatedEventUndelivered) {
		r.executor.remove(spec.ID)
		return nil, err
	}
	if err != nil && task == nil {
		r.executor.remove(spec.ID)
		return nil, errors.New(
			"task/local: task-created delivery failed without persisted task",
		)
	}
	return task, nil
}

func (r *Runner) removeUnstarted(taskID string) {
	task, err := r.manager.Get(context.Background(), taskID)
	if err == nil && task.Attempt == 0 {
		r.executor.remove(taskID)
	}
}

type executor struct {
	mu    sync.Mutex
	works map[string]WorkFunc
}

type localTaskEventPersister struct{}

func (localTaskEventPersister) Persist(
	ctx context.Context,
	_ background.TaskEventScope,
	input *background.TaskEventEnvelope[string, string],
	writer background.TaskEventWriter,
) error {
	_, err := writer.Append(ctx, &background.TaskEventPartInput{
		PartID: "event", Data: []byte(input.Event), Final: true,
	})
	return err
}

func (*executor) Key() string { return executorKey }

func (*executor) LeaseExpiryPolicy() background.LeaseExpiryPolicy {
	return background.LeaseExpiryFail
}

func (*executor) ValidateSpec(spec background.Spec) error {
	if spec.ExecutorKey != executorKey {
		return errors.New("task/local: invalid process-local task spec")
	}
	return nil
}

func (e *executor) ValidateExecution(_ context.Context, task *background.TaskSnapshot) error {
	if task == nil {
		return errors.New("task/local: process-local task is required")
	}
	_, err := e.resolve(task.Spec)
	return err
}

// SupportsDrain is false because process-local closures cannot be reconstructed
// on another worker.
func (*executor) SupportsDrain() bool { return false }

func (e *executor) Execute(
	ctx context.Context,
	task *background.TaskSnapshot,
	runtime background.ExecutionRuntime,
) (*background.ExecutionResult, error) {
	if task.Attempt > 1 && len(task.Checkpoint) == 0 {
		return nil, errors.New("task/local: task cannot restart without a checkpoint")
	}
	work, err := e.resolve(task.Spec)
	if err != nil {
		return nil, err
	}
	startwindow.Signal(ctx)
	defer e.remove(task.Spec.ID)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type workResult struct {
		value string
		err   error
	}
	resultCh := make(chan workResult, 1)
	go func() {
		result := workResult{}
		defer func() {
			if panicValue := recover(); panicValue != nil {
				result.err = safe.NewPanicErr(panicValue, debug.Stack())
			}
			resultCh <- result
		}()
		result.value, result.err = work(workCtx, runtime)
	}()
	select {
	case result := <-resultCh:
		if result.err != nil {
			return nil, result.err
		}
		return &background.ExecutionResult{
			Action: background.ExecutionActionComplete, Data: []byte(result.value),
		}, nil
	case control := <-runtime.Controls():
		cancel()
		switch control.Kind {
		case background.ControlStop:
			reason := control.Reason
			if reason == "" {
				reason = "task was canceled"
			}
			return &background.ExecutionResult{
				Action: background.ExecutionActionCancel, Error: reason,
			}, nil
		case background.ControlTimeout:
			return &background.ExecutionResult{
				Action: background.ExecutionActionFail, Error: control.Reason,
			}, nil
		default:
			return nil, background.ErrDrainCheckpointUnavailable
		}
	}
}

func (e *executor) register(taskID string, work WorkFunc) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if taskID == "" || work == nil {
		return errors.New("task/local: task id and work are required")
	}
	if _, exists := e.works[taskID]; exists {
		return background.ErrAlreadyExists
	}
	e.works[taskID] = work
	return nil
}

func (e *executor) resolve(spec background.Spec) (WorkFunc, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	work, ok := e.works[spec.ID]
	if !ok {
		return nil, errors.New("task/local: work is unavailable after process loss")
	}
	return work, nil
}

func (e *executor) remove(taskID string) {
	e.mu.Lock()
	delete(e.works, taskID)
	e.mu.Unlock()
}

func defaultBackgroundNotice(_ context.Context, info NoticeInfo) string {
	id, kind, outputFile := "", "", ""
	notifySession := false
	if info.Task != nil {
		id = info.Task.Spec.ID
		if info.Task.Spec.Kind != "" {
			kind = " (" + info.Task.Spec.Kind + ")"
		}
		outputFile = info.Task.Spec.OutputFile
		notifySession = info.Task.Spec.NotifySession
	}
	state := "is running in the background"
	if info.AutoBackgrounded {
		state = "moved to the background"
	}
	output := ""
	if outputFile != "" {
		output = fmt.Sprintf(
			" Output is being written to: %s. To check interim output, use Read on that file path.",
			outputFile,
		)
	}
	completion := "; use task_output to check status and retrieve the result."
	if notifySession {
		completion = "; you will be notified when it completes."
	}
	return fmt.Sprintf("\n[task %s%s %s%s%s]", id, kind, state, completion, output)
}
