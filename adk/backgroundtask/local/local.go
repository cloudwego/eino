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

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/foreground"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

const executorKey = "eino.dev/process-local"

// WorkFunc performs buffered process-local work.
type WorkFunc func(
	ctx context.Context,
	runtime backgroundtask.ExecutionRuntime,
) (string, error)

// StreamWorkFunc performs streaming process-local work.
type StreamWorkFunc func(
	ctx context.Context,
	runtime backgroundtask.ExecutionRuntime,
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
	// ForegroundTimeoutMs bounds how long this run is observed in the foreground.
	// Nil falls back to Config.ForegroundTimeoutMs. A non-positive value disables
	// the timer: the caller waits until the work returns or ctx is canceled, and
	// Config.ShouldAutoBackground is never consulted, since it is only evaluated on
	// timer expiry. Ignored when RunInBackground is set.
	ForegroundTimeoutMs *int
}

// NoticeInfo carries lifecycle facts for a background stream notice. Task may
// be nil when authoritative loading fails.
type NoticeInfo struct {
	Task             *backgroundtask.Task
	AutoBackgrounded bool
}

// Config configures process-local execution and ephemeral foreground
// projection. Policy callbacks may run concurrently and must not panic or
// mutate the supplied candidate. Nil ShouldAutoBackground disables automatic
// detachment; nil BackgroundNotice uses the default notice.
type Config struct {
	Manager   *backgroundtask.Manager
	Executors *backgroundtask.ExecutorRegistry
	// ForegroundTimeoutMs is the default foreground observation timeout for runs
	// that do not set Input.ForegroundTimeoutMs. Nil uses the framework default
	// (foreground.DefaultTimeoutMs); a non-positive value disables the timer, so
	// runs wait until the work returns or ctx is canceled and ShouldAutoBackground
	// is never consulted.
	ForegroundTimeoutMs  *int
	ShouldAutoBackground func(context.Context, *foreground.CandidateInfo) bool
	BackgroundNotice     func(context.Context, NoticeInfo) string
}

// Runner owns one process-local closure registry for a Manager.
type Runner struct {
	manager          *backgroundtask.Manager
	executor         *executor
	policy           foreground.Policy
	backgroundNotice func(context.Context, NoticeInfo) string
}

// New constructs a Runner and registers its process-local executor.
func New(config *Config) (*Runner, error) {
	if config == nil || config.Manager == nil || config.Executors == nil {
		return nil, errors.New("backgroundtask/local: manager and executor registry are required")
	}
	timeoutMs := foreground.DefaultTimeoutMs
	if config.ForegroundTimeoutMs != nil {
		timeoutMs = *config.ForegroundTimeoutMs
	}
	registered, _, err := config.Executors.LoadOrRegister(
		&executor{works: make(map[string]WorkFunc)},
	)
	if err != nil {
		return nil, err
	}
	localExecutor, compatible := registered.(*executor)
	if !compatible {
		return nil, fmt.Errorf(
			"backgroundtask/local: executor key %q is already registered",
			executorKey,
		)
	}
	runner := &Runner{
		manager:  config.Manager,
		executor: localExecutor,
		policy: foreground.Policy{
			TimeoutMs: timeoutMs, ShouldAutoBackground: config.ShouldAutoBackground,
		},
		backgroundNotice: config.BackgroundNotice,
	}
	if runner.backgroundNotice == nil {
		runner.backgroundNotice = defaultBackgroundNotice
	}
	return runner, nil
}

// Manager returns the shared lifecycle Manager.
func (r *Runner) Manager() *backgroundtask.Manager {
	if r == nil {
		return nil
	}
	return r.manager
}

// Run executes buffered process-local work. If the configured foreground
// timeout expires without a successful background handoff, Run returns a
// *backgroundtask.ForegroundTimeoutError.
func (r *Runner) Run(ctx context.Context, input *Input, work WorkFunc) (*backgroundtask.Task, error) {
	if input == nil || work == nil {
		return nil, errors.New("backgroundtask/local: input and work are required")
	}
	spec, err := r.newSpec(ctx, input)
	if err != nil {
		return nil, err
	}
	if input.RunInBackground {
		task, err := r.submitSpec(ctx, spec, work)
		if err != nil {
			return nil, err
		}
		go func() {
			_ = r.manager.Execute(detachedContext{parent: ctx}, task.Spec.ID)
		}()
		return task, nil
	}
	return r.runForeground(ctx, input, spec, work)
}

func (r *Runner) runForeground(
	ctx context.Context,
	input *Input,
	spec backgroundtask.Spec,
	work WorkFunc,
) (*backgroundtask.Task, error) {
	startedAt := time.Now()
	workCtx, cancel := context.WithCancel(ctx)
	adopted := false
	defer func() {
		if !adopted {
			cancel()
		}
	}()
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
		result.value, result.err = work(workCtx, foregroundRuntime{})
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
		return r.resultTask(spec, result.value, result.err), nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-timeout:
		candidate := &foreground.CandidateInfo{
			TaskID: spec.ID, Kind: spec.Kind, Description: spec.Description,
			OutputFile: spec.OutputFile, StartedAt: startedAt,
			Elapsed: time.Since(startedAt),
		}
		if r.policy.ShouldAutoBackground != nil &&
			r.policy.ShouldAutoBackground(ctx, candidate) {
			task, err := r.adoptForeground(ctx, spec, resultCh)
			if err != nil {
				cancel()
				return r.failedTask(spec, fmt.Sprintf("handoff failed after %dms: %v", timeoutMs, err)), nil
			}
			adopted = true
			return task, nil
		}
		cancel()
		return nil, &backgroundtask.ForegroundTimeoutError{
			Timeout: time.Duration(timeoutMs) * time.Millisecond,
			TaskID:  spec.ID,
		}
	}
}

func (r *Runner) adoptForeground(
	ctx context.Context,
	spec backgroundtask.Spec,
	resultCh <-chan struct {
		value string
		err   error
	},
) (*backgroundtask.Task, error) {
	waitWork := func(context.Context, backgroundtask.ExecutionRuntime) (string, error) {
		result := <-resultCh
		return result.value, result.err
	}
	if err := r.executor.register(spec.ID, waitWork); err != nil {
		return nil, err
	}
	task, err := r.manager.Submit(ctx, &backgroundtask.SubmitRequest{Spec: spec})
	if err != nil {
		if errors.Is(err, backgroundtask.ErrTaskCreatedEventUndelivered) && task != nil {
			go func() {
				_ = r.manager.Execute(detachedContext{parent: ctx}, task.Spec.ID)
			}()
			return task, nil
		}
		r.executor.remove(spec.ID)
		return nil, err
	}
	go func() {
		_ = r.manager.Execute(detachedContext{parent: ctx}, task.Spec.ID)
	}()
	return task, nil
}

func (r *Runner) resultTask(spec backgroundtask.Spec, value string, err error) *backgroundtask.Task {
	if err != nil {
		return r.failedTask(spec, err.Error())
	}
	return &backgroundtask.Task{
		Spec:       spec,
		Status:     backgroundtask.StatusCompleted,
		ResultData: []byte(value),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
}

func (r *Runner) failedTask(spec backgroundtask.Spec, reason string) *backgroundtask.Task {
	now := time.Now()
	return &backgroundtask.Task{
		Spec:        spec,
		Status:      backgroundtask.StatusFailed,
		ResultError: reason,
		CreatedAt:   now,
		UpdatedAt:   now,
		DoneAt:      &now,
	}
}

// RunStream executes streaming process-local work and returns its ephemeral
// caller-facing projection. Foreground chunks are not task events because no
// durable task exists until explicit background launch or successful handoff.
// Closing a foreground reader requests cancellation of this process-local
// operation; closing a background preview only closes the caller projection.
// A foreground timeout is sent through the reader as a
// *backgroundtask.ForegroundTimeoutError.
func (r *Runner) RunStream(
	ctx context.Context,
	input *Input,
	work StreamWorkFunc,
) (*schema.StreamReader[string], error) {
	if input == nil || work == nil {
		return nil, errors.New("backgroundtask/local: input and stream work are required")
	}
	chunks := make(chan streamChunk, streamBufferCap)
	ready := make(chan error, 1)
	projectionReady := make(chan struct{})
	var readyOnce sync.Once
	signalReady := func(err error) {
		readyOnce.Do(func() {
			ready <- err
			close(projectionReady)
		})
	}
	adapter := func(
		workCtx context.Context,
		runtime backgroundtask.ExecutionRuntime,
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
			err = errors.New("backgroundtask/local: StreamWorkFunc returned a nil reader")
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
			if _, appendErr := runtime.EmitProgress(workCtx, "", []byte(chunk)); appendErr != nil {
				chunks <- streamChunk{err: appendErr}
				return "", appendErr
			}
			output.WriteString(chunk)
			chunks <- streamChunk{text: chunk}
		}
	}
	spec, err := r.newSpec(ctx, input)
	if err != nil {
		return nil, err
	}
	if !input.RunInBackground {
		return r.runForegroundStream(ctx, input, spec, adapter, chunks, ready)
	}
	task, err := r.submitSpec(ctx, spec, adapter)
	if err != nil {
		return nil, err
	}
	runDone := make(chan runResult, 1)
	go func() {
		runErr := r.manager.Execute(detachedContext{parent: ctx}, task.Spec.ID)
		current, getErr := r.manager.Get(context.Background(), task.Spec.ID)
		if runErr == nil {
			runErr = getErr
		}
		runDone <- runResult{task: current, err: runErr}
	}()
	reader, writer := schema.Pipe[string](streamBufferCap)
	// A successful submission is the acknowledgement boundary for an explicit
	// background run. In particular, a StreamingShell may block while creating
	// its reader (for example, when adapting a synchronous RPC), but that must
	// not delay returning the persisted task to the caller.
	go r.projectStream(ctx, input, task.Spec.ID, chunks, runDone, writer)
	return reader, nil
}

func (r *Runner) runForegroundStream(
	ctx context.Context,
	input *Input,
	spec backgroundtask.Spec,
	work WorkFunc,
	chunks <-chan streamChunk,
	ready <-chan error,
) (*schema.StreamReader[string], error) {
	workCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		value, err := work(workCtx, foregroundRuntime{})
		resultCh <- struct {
			value string
			err   error
		}{value: value, err: err}
	}()
	if err := <-ready; err != nil {
		cancel()
		return nil, err
	}
	reader, writer := schema.Pipe[string](streamBufferCap)
	go r.projectForegroundStream(&foregroundStreamProjection{
		ctx: ctx, input: input, spec: spec, chunks: chunks,
		resultCh: resultCh, writer: writer, cancel: cancel,
	})
	return reader, nil
}

func (r *Runner) submit(
	ctx context.Context,
	input *Input,
	work WorkFunc,
) (*backgroundtask.Task, error) {
	if r == nil || r.manager == nil || r.executor == nil || input == nil || work == nil {
		return nil, errors.New("backgroundtask/local: runner, input, and work are required")
	}
	spec, err := r.newSpec(ctx, input)
	if err != nil {
		return nil, err
	}
	return r.submitSpec(ctx, spec, work)
}

func (r *Runner) newSpec(ctx context.Context, input *Input) (backgroundtask.Spec, error) {
	if r == nil || r.manager == nil || input == nil {
		return backgroundtask.Spec{}, errors.New("backgroundtask/local: runner and input are required")
	}
	id, err := r.manager.AllocateTaskID(ctx, &backgroundtask.AllocateTaskIDRequest{
		Kind: input.Kind,
	})
	if err != nil {
		return backgroundtask.Spec{}, err
	}
	return backgroundtask.Spec{
		ID: id, ExecutorKey: executorKey, Kind: input.Kind,
		Payload: append([]byte(nil), input.Payload...), Description: input.Description,
		OutputFile: input.OutputFile, SessionID: input.SessionID, NotifySession: input.NotifySession,
	}, nil
}

func (r *Runner) submitSpec(
	ctx context.Context,
	spec backgroundtask.Spec,
	work WorkFunc,
) (*backgroundtask.Task, error) {
	if err := r.executor.register(spec.ID, work); err != nil {
		return nil, err
	}
	task, err := r.manager.Submit(ctx, &backgroundtask.SubmitRequest{Spec: spec})
	if err != nil && !errors.Is(err, backgroundtask.ErrTaskCreatedEventUndelivered) {
		r.executor.remove(spec.ID)
		return nil, err
	}
	if err != nil && task == nil {
		r.executor.remove(spec.ID)
		return nil, errors.New(
			"backgroundtask/local: task-created delivery failed without persisted task",
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

type detachedContext struct {
	parent context.Context
}

func (detachedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (detachedContext) Done() <-chan struct{}       { return nil }
func (detachedContext) Err() error                  { return nil }
func (c detachedContext) Value(key any) any         { return c.parent.Value(key) }

type foregroundRuntime struct{}

func (foregroundRuntime) Controls() <-chan backgroundtask.ControlRequest { return nil }

func (foregroundRuntime) EmitProgress(_ context.Context, eventID string, _ []byte) (backgroundtask.ProgressEmission, error) {
	return backgroundtask.ProgressEmission{EventID: eventID, FirstEmission: true}, nil
}

func (foregroundRuntime) ReportTranscriptFailure(context.Context, error) error {
	return nil
}

func (*executor) Key() string { return executorKey }

func (*executor) LeaseExpiryPolicy() backgroundtask.LeaseExpiryPolicy {
	return backgroundtask.LeaseExpiryFail
}

func (*executor) ValidateSpec(spec backgroundtask.Spec) error {
	if spec.ExecutorKey != executorKey {
		return errors.New("backgroundtask/local: invalid process-local task spec")
	}
	return nil
}

func (e *executor) ValidateExecution(_ context.Context, task *backgroundtask.Task) error {
	if task == nil {
		return errors.New("backgroundtask/local: process-local task is required")
	}
	_, err := e.resolve(task.Spec)
	return err
}

// SupportsDrain is false because process-local closures cannot be reconstructed
// on another worker.
func (*executor) SupportsDrain() bool { return false }

func (e *executor) Execute(
	ctx context.Context,
	task *backgroundtask.Task,
	runtime backgroundtask.ExecutionRuntime,
) (*backgroundtask.ExecutionResult, error) {
	if task.Attempt > 1 && len(task.Checkpoint) == 0 {
		return nil, errors.New("backgroundtask/local: task cannot restart without a checkpoint")
	}
	work, err := e.resolve(task.Spec)
	if err != nil {
		return nil, err
	}
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
		return &backgroundtask.ExecutionResult{
			Status: backgroundtask.StatusCompleted, Data: []byte(result.value),
		}, nil
	case control := <-runtime.Controls():
		cancel()
		switch control.Kind {
		case backgroundtask.ControlStop:
			reason := control.Reason
			if reason == "" {
				reason = "task was canceled"
			}
			return &backgroundtask.ExecutionResult{
				Status: backgroundtask.StatusCanceled, Error: reason,
			}, nil
		case backgroundtask.ControlTimeout:
			return &backgroundtask.ExecutionResult{
				Status: backgroundtask.StatusFailed, Error: control.Reason,
			}, nil
		default:
			return nil, backgroundtask.ErrDrainCheckpointUnavailable
		}
	}
}

func (e *executor) register(taskID string, work WorkFunc) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if taskID == "" || work == nil {
		return errors.New("backgroundtask/local: task id and work are required")
	}
	if _, exists := e.works[taskID]; exists {
		return backgroundtask.ErrAlreadyExists
	}
	e.works[taskID] = work
	return nil
}

func (e *executor) resolve(spec backgroundtask.Spec) (WorkFunc, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	work, ok := e.works[spec.ID]
	if !ok {
		return nil, errors.New("backgroundtask/local: work is unavailable after process loss")
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
