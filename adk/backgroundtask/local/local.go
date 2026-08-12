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
	ForegroundTimeoutMs        *int
}

// NoticeInfo carries lifecycle facts for a background stream notice. Task may
// be nil when authoritative loading fails.
type NoticeInfo struct {
	Task             *backgroundtask.Task
	AutoBackgrounded bool
}

// Config configures process-local execution and ephemeral foreground
// projection. Policy callbacks may run concurrently and must not panic or
// mutate the supplied task. Nil ShouldAutoBackground disables automatic
// detachment; nil BackgroundNotice uses the default notice.
type Config struct {
	Manager              *backgroundtask.Manager
	Executors            *backgroundtask.ExecutorRegistry
	ForegroundTimeoutMs  *int
	ShouldAutoBackground func(context.Context, *backgroundtask.Task) bool
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

// Run executes buffered process-local work.
func (r *Runner) Run(ctx context.Context, input *Input, work WorkFunc) (*backgroundtask.Task, error) {
	task, err := r.submit(ctx, input, work)
	if err != nil {
		return nil, err
	}
	task, err = foreground.Run(ctx, r.manager, r.policy, &foreground.Request{
		TaskID: task.Spec.ID, RunInBackground: input.RunInBackground,
		TimeoutMs: input.ForegroundTimeoutMs,
	})
	if err != nil {
		r.removeUnstarted(task.Spec.ID)
	}
	return task, err
}

// RunStream executes streaming process-local work and returns its ephemeral
// caller-facing projection. Every chunk is also appended to the task-event
// feed. Closing the returned reader requests cancellation of this process-local
// operation; callers that want execution to continue must explicitly launch it
// in the background rather than abandoning the stream.
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
	task, err := r.submit(ctx, input, adapter)
	if err != nil {
		return nil, err
	}
	runDone := make(chan runResult, 1)
	go func() {
		result, runErr := foreground.Run(ctx, r.manager, r.policy, &foreground.Request{
			TaskID: task.Spec.ID, RunInBackground: input.RunInBackground,
			TimeoutMs: input.ForegroundTimeoutMs, ProjectionReady: projectionReady,
		})
		runDone <- runResult{task: result, err: runErr}
	}()
	var (
		readyErr    error
		earlyResult *runResult
	)
waitReady:
	for {
		select {
		case readyErr = <-ready:
			break waitReady
		case result := <-runDone:
			select {
			case readyErr = <-ready:
				earlyResult = &result
				break waitReady
			default:
			}
			if result.err != nil {
				r.removeUnstarted(task.Spec.ID)
				return nil, result.err
			}
			if result.task == nil || result.task.Status != backgroundtask.StatusRunning {
				r.removeUnstarted(task.Spec.ID)
				return nil, errors.New("backgroundtask/local: task ended before stream construction")
			}
			earlyResult = &result
			runDone = nil
		}
	}
	if readyErr != nil {
		if earlyResult == nil {
			result := <-runDone
			if result.err != nil {
				return nil, result.err
			}
		}
		return nil, readyErr
	}
	if earlyResult != nil {
		runDone = make(chan runResult, 1)
		runDone <- *earlyResult
	}
	reader, writer := schema.Pipe[string](streamBufferCap)
	go r.projectStream(ctx, input, task.Spec.ID, chunks, runDone, writer)
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
	id, err := r.manager.AllocateTaskID(ctx, &backgroundtask.AllocateTaskIDRequest{
		Kind: input.Kind,
	})
	if err != nil {
		return nil, err
	}
	if err = r.executor.register(id, work); err != nil {
		return nil, err
	}
	task, err := r.manager.Submit(ctx, backgroundtask.Spec{
		ID: id, ExecutorKey: executorKey, Kind: input.Kind,
		Payload: append([]byte(nil), input.Payload...), Description: input.Description,
		OutputFile: input.OutputFile, SessionID: input.SessionID, NotifySession: input.NotifySession,
		// Process-local tasks may complete in the foreground. Defer the
		// TaskCreated announcement until the task actually detaches into the
		// background so a foreground-completed run never appears as a background
		// task.
		EmitCreatedOnBackground: true,
	})
	if err != nil {
		r.executor.remove(id)
		return nil, err
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
	if info.Task != nil {
		id = info.Task.Spec.ID
		if info.Task.Spec.Kind != "" {
			kind = " (" + info.Task.Spec.Kind + ")"
		}
		outputFile = info.Task.Spec.OutputFile
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
	return fmt.Sprintf(
		"\n[task %s%s %s; you will be notified when it completes.%s]",
		id, kind, state, output,
	)
}
