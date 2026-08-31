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

package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/foreground"
	"github.com/cloudwego/eino/adk/internal/startwindow"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[foregroundToolInterruptState](
		"_eino_adk_backgroundtool_foreground_interrupt_state",
	)
}

// ManagedToolConfig configures the framework-owned model-facing wrapper.
type ManagedToolConfig struct {
	// Manager and Executors are required; Executors must be the registry used by
	// Manager's workers.
	Manager   *backgroundtask.Manager
	Executors *backgroundtask.ExecutorRegistry
	// Registry and ToolName select a required registered implementation.
	Registry *Registry
	ToolName string

	// ForegroundTimeoutMs overrides the default foreground observation timeout.
	// Nil uses the framework default; non-positive disables the timer.
	ForegroundTimeoutMs *int
	// ShouldAutoBackground is evaluated after foreground timeout. Nil means
	// timeout the operation instead of detaching. It may be called concurrently.
	// A non-positive ForegroundTimeoutMs therefore also disables this policy:
	// with no timer there is no expiry to evaluate it on.
	ShouldAutoBackground func(context.Context, *foreground.CandidateInfo) bool
	// RunInBackground requests explicit detachment from JSON arguments. Nil
	// never requests it and takes precedence over foreground timeout.
	RunInBackground func(context.Context, string) bool
	// InvocationTimeoutMs returns an optional operation timeout in milliseconds.
	// Nil or a nil result means no operation timeout.
	InvocationTimeoutMs func(context.Context, string) *int
	// SessionID resolves the optional session notification target. An empty
	// result disables session-routed lifecycle notifications. Nil uses the
	// current Runner session when one exists and otherwise disables notification.
	SessionID func(context.Context) (string, error)
}

type managedTool struct {
	manager           *backgroundtask.Manager
	registry          *Registry
	registration      *Registration
	recoverable       bool
	info              *schema.ToolInfo
	policy            foreground.Policy
	runInBackground   func(context.Context, string) bool
	invocationTimeout func(context.Context, string) *int
	sessionID         func(context.Context) (string, error)
}

// NewManagedTool creates a wrapper implementing EnhancedInvokableTool and
// EnhancedStreamableTool. Every result includes a text control envelope;
// completed foreground results may append rich parts through
// Registration.RenderResult. Detaching closes only the caller projection;
// durable persistence continues. A foreground timeout without a successful
// handoff returns a *backgroundtask.ForegroundTimeoutError.
func NewManagedTool(
	ctx context.Context,
	config *ManagedToolConfig,
) (componenttool.BaseTool, error) {
	if config == nil || config.Manager == nil || config.Executors == nil ||
		config.Registry == nil ||
		config.ToolName == "" {
		return nil, errors.New(
			"backgroundtask/tool: manager, executor registry, tool registry, and tool name are required",
		)
	}
	if err := RegisterExecutors(config.Executors, config.Registry); err != nil {
		return nil, err
	}
	registration, recoverable, ok := config.Registry.resolveAny(config.ToolName)
	if !ok {
		return nil, fmt.Errorf("backgroundtask/tool: tool %q is not registered", config.ToolName)
	}
	if config.ShouldAutoBackground != nil {
		if _, ok = registration.Tool.(ForegroundHandoffTool); !ok {
			return nil, fmt.Errorf(
				"backgroundtask/tool: tool %q does not support foreground handoff",
				config.ToolName,
			)
		}
	}
	info, err := cloneToolInfo(registration.Info)
	if err != nil {
		return nil, fmt.Errorf("backgroundtask/tool: clone tool info: %w", err)
	}
	info.Desc += "\nSuccessful execution returns an Eino task_id for task_output and task_stop."
	timeoutMs := foreground.DefaultTimeoutMs
	if config.ForegroundTimeoutMs != nil {
		timeoutMs = *config.ForegroundTimeoutMs
	}
	sessionID := config.SessionID
	if sessionID == nil {
		sessionID = sessionIDFromContext
	}
	return &managedTool{
		manager: config.Manager, registry: config.Registry, registration: registration,
		recoverable: recoverable, info: info,
		policy: foreground.Policy{
			TimeoutMs: timeoutMs, ShouldAutoBackground: config.ShouldAutoBackground,
		},
		runInBackground: config.RunInBackground, invocationTimeout: config.InvocationTimeoutMs,
		sessionID: sessionID,
	}, nil
}

func (t *managedTool) Info(context.Context) (*schema.ToolInfo, error) {
	return cloneToolInfo(t.info)
}

func (t *managedTool) InvokableRun(
	ctx context.Context,
	toolArgument *schema.ToolArgument,
	_ ...componenttool.Option,
) (*schema.ToolResult, error) {
	if toolArgument == nil {
		return nil, errors.New("backgroundtask/tool: tool argument is required")
	}
	if wasInterrupted, hasState, state := componenttool.GetInterruptState[foregroundToolInterruptState](ctx); wasInterrupted && hasState {
		return t.resumeForeground(ctx, state)
	}
	arguments := toolArgument.Text
	arguments, err := t.prepareInput(ctx, arguments)
	if err != nil {
		return nil, err
	}
	if t.shouldRunInBackground(ctx, arguments) {
		task, _, err := t.submit(ctx, arguments, false)
		if err != nil {
			return nil, err
		}
		t.executeBackgroundUntilStart(ctx, task.Spec.ID)
		return t.renderLaunchResult(ctx, task)
	}
	return t.runForeground(ctx, arguments)
}

func (t *managedTool) StreamableRun(
	ctx context.Context,
	toolArgument *schema.ToolArgument,
	_ ...componenttool.Option,
) (*schema.StreamReader[*schema.ToolResult], error) {
	if toolArgument == nil {
		return nil, errors.New("backgroundtask/tool: tool argument is required")
	}
	if wasInterrupted, hasState, state := componenttool.GetInterruptState[foregroundToolInterruptState](ctx); wasInterrupted && hasState {
		reader, writer := schema.Pipe[*schema.ToolResult](projectionBuffer)
		go func() {
			defer writer.Close()
			result, err := t.resumeForeground(ctx, state)
			writer.Send(result, err)
		}()
		return reader, nil
	}
	arguments := toolArgument.Text
	arguments, err := t.prepareInput(ctx, arguments)
	if err != nil {
		return nil, err
	}
	reader, writer := schema.Pipe[*schema.ToolResult](projectionBuffer)
	if t.shouldRunInBackground(ctx, arguments) {
		task, projection, err := t.submit(ctx, arguments, true)
		if err != nil {
			return nil, err
		}
		runDone := make(chan launchResult, 1)
		_, window := t.startBackgroundExecution(ctx, task.Spec.ID)
		// The projection must drain updates before waiting on Start; otherwise
		// the executor can stall on the projection buffer before detach.
		go t.project(context.Background(), task.Spec.ID, projection, runDone, writer)
		_ = window.Wait(ctx, t.startWindowTimeout())
		runDone <- launchResult{task: task}
		return reader, nil
	}
	go t.streamForeground(ctx, arguments, writer)
	return reader, nil
}

type launchResult struct {
	task *backgroundtask.Task
	err  error
}

type foregroundStart struct {
	taskID         string
	arguments      string
	spec           backgroundtask.Spec
	run            Run
	toolCheckpoint []byte
}

type detachedContext struct {
	parent context.Context
}

func (detachedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (detachedContext) Done() <-chan struct{}       { return nil }
func (detachedContext) Err() error                  { return nil }
func (c detachedContext) Value(key any) any         { return c.parent.Value(key) }

func (t *managedTool) executeBackgroundUntilStart(ctx context.Context, taskID string) {
	_, window := t.startBackgroundExecution(ctx, taskID)
	_ = window.Wait(ctx, t.startWindowTimeout())
}

func (t *managedTool) startBackgroundExecution(ctx context.Context, taskID string) (context.Context, *startwindow.Window) {
	backgroundCtx, window := startwindow.Open(detachedContext{parent: ctx})
	go func() {
		defer startwindow.Signal(backgroundCtx)
		_ = t.manager.Execute(backgroundCtx, taskID)
	}()
	return backgroundCtx, window
}

func (t *managedTool) startWindowTimeout() time.Duration {
	return time.Duration(t.policy.TimeoutMs) * time.Millisecond
}

func (t *managedTool) runForeground(
	ctx context.Context,
	arguments string,
) (*schema.ToolResult, error) {
	start, err := t.startForeground(ctx, arguments)
	if err != nil {
		return nil, err
	}
	outcome, task, err := t.waitForeground(ctx, arguments, start)
	if err != nil {
		return nil, err
	}
	if task != nil {
		return t.renderLaunchResult(ctx, task)
	}
	return t.finishForeground(ctx, start.spec, start.arguments, start.toolCheckpoint, outcome)
}

type foregroundToolInterruptState struct {
	TaskID         string
	ToolName       string
	Arguments      string
	RequestID      string
	ToolCheckpoint []byte
	OutputFile     string
}

func (t *managedTool) streamForeground(
	ctx context.Context,
	arguments string,
	writer *schema.StreamWriter[*schema.ToolResult],
) {
	defer writer.Close()
	start, err := t.startForeground(ctx, arguments)
	if err != nil {
		result, encodeErr := t.renderForegroundFailure(ctx, "", err.Error())
		writer.Send(result, encodeErr)
		return
	}
	var updates *schema.StreamReader[*Update]
	var updateResults <-chan updateResult
	var stopUpdates chan struct{}
	if source, ok := start.run.(UpdateSource); ok && t.policy.ShouldAutoBackground == nil {
		updates = source.Updates()
		if updates == nil {
			result, encodeErr := t.renderForegroundFailure(
				ctx,
				start.spec.Description,
				"backgroundtask/tool: update source returned a nil reader",
			)
			writer.Send(result, encodeErr)
			return
		}
		defer updates.Close()
		results := make(chan updateResult, 1)
		updateResults = results
		stopUpdates = make(chan struct{})
		defer close(stopUpdates)
		go receiveUpdates(updates, results, stopUpdates)
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	waitResult := make(chan struct {
		outcome *Outcome
		err     error
	}, 1)
	seenUpdates := make(map[string][]byte)
	go func() {
		outcome, waitErr := start.run.Wait(waitCtx)
		waitResult <- struct {
			outcome *Outcome
			err     error
		}{outcome: outcome, err: waitErr}
	}()
	timeout, timeoutDuration := t.foregroundTimeout(ctx, arguments)
	for {
		select {
		case update, ok := <-updateResults:
			if !ok {
				updateResults = nil
				continue
			}
			if update.err != nil {
				if errors.Is(update.err, io.EOF) {
					updateResults = nil
					continue
				}
				writer.Send(nil, update.err)
				return
			}
			first, err := t.processForegroundUpdate(ctx, start.spec, update.update, seenUpdates)
			if err != nil {
				writer.Send(nil, err)
				return
			}
			if !first {
				continue
			}
			record, encodeErr := renderEvent(&ManagedToolResponseEvent{
				Type: ManagedToolResponseEventUpdate, Update: update.update,
			})
			if writer.Send(record, encodeErr) {
				_ = start.run.Stop(context.Background())
				return
			}
		case result := <-waitResult:
			if err := t.drainForegroundUpdatesToWriter(ctx, start.spec, updateResults, seenUpdates, writer); err != nil {
				final, encodeErr := t.renderForegroundFailure(
					ctx, start.spec.Description, err.Error(),
				)
				writer.Send(final, encodeErr)
				return
			}
			if result.err != nil {
				final, encodeErr := t.renderForegroundFailure(
					ctx, start.spec.Description, result.err.Error(),
				)
				writer.Send(final, encodeErr)
				return
			}
			final, encodeErr := t.finishForeground(
				ctx,
				start.spec,
				start.arguments,
				start.toolCheckpoint,
				result.outcome,
			)
			writer.Send(final, encodeErr)
			return
		case <-timeout:
			task, handoffErr := t.tryHandoff(ctx, start)
			if handoffErr == nil && task != nil {
				final, encodeErr := t.renderLaunchResult(ctx, task)
				writer.Send(final, encodeErr)
				return
			}
			_ = start.run.Stop(context.Background())
			writer.Send(nil, &backgroundtask.ForegroundTimeoutError{
				Timeout: timeoutDuration,
				TaskID:  start.taskID,
			})
			return
		case <-ctx.Done():
			_ = start.run.Stop(context.Background())
			writer.Send(nil, ctx.Err())
			return
		}
	}
}

func (t *managedTool) drainForegroundUpdates(
	ctx context.Context,
	spec backgroundtask.Spec,
	results <-chan updateResult,
	seen map[string][]byte,
) error {
	if results == nil {
		return nil
	}
	timer := time.NewTimer(terminalUpdateDrainTime)
	defer timer.Stop()
	for {
		select {
		case received, ok := <-results:
			if !ok || errors.Is(received.err, io.EOF) {
				return nil
			}
			if received.err != nil {
				return received.err
			}
			if _, err := t.processForegroundUpdate(ctx, spec, received.update, seen); err != nil {
				return err
			}
		case <-timer.C:
			return errors.New("backgroundtask/tool: update stream did not close after terminal outcome")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *managedTool) drainForegroundUpdatesToWriter(
	ctx context.Context,
	spec backgroundtask.Spec,
	results <-chan updateResult,
	seen map[string][]byte,
	writer *schema.StreamWriter[*schema.ToolResult],
) error {
	if results == nil {
		return nil
	}
	timer := time.NewTimer(terminalUpdateDrainTime)
	defer timer.Stop()
	for {
		select {
		case received, ok := <-results:
			if !ok || errors.Is(received.err, io.EOF) {
				return nil
			}
			if received.err != nil {
				return received.err
			}
			first, err := t.processForegroundUpdate(ctx, spec, received.update, seen)
			if err != nil {
				return err
			}
			if first {
				record, encodeErr := renderEvent(&ManagedToolResponseEvent{
					Type: ManagedToolResponseEventUpdate, Update: received.update,
				})
				if encodeErr != nil {
					return encodeErr
				}
				if writer.Send(record, nil) {
					return nil
				}
			}
		case <-timer.C:
			return errors.New("backgroundtask/tool: update stream did not close after terminal outcome")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *managedTool) processForegroundUpdate(
	ctx context.Context,
	spec backgroundtask.Spec,
	update *Update,
	seen map[string][]byte,
) (bool, error) {
	if update == nil {
		return false, errors.New("backgroundtask/tool: update must not be nil")
	}
	if err := validateUpdate(update); err != nil {
		return false, err
	}
	if t.recoverable && update.EventID == "" {
		return false, errors.New("backgroundtask/tool: recoverable update event id is required")
	}
	first := true
	if update.EventID != "" {
		if previous, ok := seen[update.EventID]; ok {
			if !bytes.Equal(previous, update.Data) {
				return false, backgroundtask.ErrTaskEventIDConflict
			}
			first = false
		} else {
			seen[update.EventID] = append([]byte(nil), update.Data...)
		}
	}
	if t.registration.Materializer != nil && spec.OutputFile != "" && update.EventID != "" {
		if err := t.registration.Materializer.AppendOutput(ctx, &MaterializeOutputRequest{
			TaskID: spec.ID, EventID: update.EventID,
			Path: spec.OutputFile, Data: append([]byte(nil), update.Data...),
		}); err != nil {
			return false, err
		}
	}
	return first, nil
}

func (t *managedTool) startForeground(ctx context.Context, arguments string) (*foregroundStart, error) {
	taskID, spec, err := t.newSpec(ctx, arguments)
	if err != nil {
		return nil, err
	}
	startResult, err := t.registration.Tool.Start(ctx, &StartRequest{
		TaskID: taskID, Arguments: arguments, Attempt: 0,
	})
	if err != nil {
		return nil, err
	}
	if startResult == nil || startResult.Run == nil {
		return nil, errors.New("backgroundtask/tool: implementation returned a nil start result")
	}
	if !t.recoverable && len(startResult.Checkpoint) > 0 {
		_ = startResult.Run.Stop(context.Background())
		return nil, errors.New("backgroundtask/tool: plain tool cannot return a checkpoint")
	}
	return &foregroundStart{
		taskID: taskID, arguments: arguments, spec: spec, run: startResult.Run,
		toolCheckpoint: append([]byte(nil), startResult.Checkpoint...),
	}, nil
}

func (t *managedTool) waitForeground(
	ctx context.Context,
	arguments string,
	start *foregroundStart,
) (*Outcome, *backgroundtask.Task, error) {
	var updates *schema.StreamReader[*Update]
	var updateResults <-chan updateResult
	var stopUpdates chan struct{}
	if source, ok := start.run.(UpdateSource); ok {
		updates = source.Updates()
		if updates == nil {
			return &Outcome{
				Status: backgroundtask.StatusFailed,
				Error:  "backgroundtask/tool: update source returned a nil reader",
			}, nil, nil
		}
		defer updates.Close()
		results := make(chan updateResult, 1)
		updateResults = results
		stopUpdates = make(chan struct{})
		defer close(stopUpdates)
		go receiveUpdates(updates, results, stopUpdates)
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	waitResult := make(chan struct {
		outcome *Outcome
		err     error
	}, 1)
	seenUpdates := make(map[string][]byte)
	go func() {
		outcome, waitErr := start.run.Wait(waitCtx)
		waitResult <- struct {
			outcome *Outcome
			err     error
		}{outcome: outcome, err: waitErr}
	}()
	timeout, timeoutDuration := t.foregroundTimeout(ctx, arguments)
	for {
		select {
		case update, ok := <-updateResults:
			if !ok || errors.Is(update.err, io.EOF) {
				updateResults = nil
				continue
			}
			if update.err != nil {
				_ = start.run.Stop(context.Background())
				return &Outcome{Status: backgroundtask.StatusFailed, Error: update.err.Error()}, nil, nil
			}
			_, err := t.processForegroundUpdate(ctx, start.spec, update.update, seenUpdates)
			if err != nil {
				_ = start.run.Stop(context.Background())
				return &Outcome{Status: backgroundtask.StatusFailed, Error: err.Error()}, nil, nil
			}
		case result := <-waitResult:
			if err := t.drainForegroundUpdates(ctx, start.spec, updateResults, seenUpdates); err != nil {
				return &Outcome{Status: backgroundtask.StatusFailed, Error: err.Error()}, nil, nil
			}
			if result.err != nil {
				return &Outcome{Status: backgroundtask.StatusFailed, Error: result.err.Error()}, nil, nil
			}
			return result.outcome, nil, nil
		case <-timeout:
			task, err := t.tryHandoff(ctx, start)
			if err == nil && task != nil {
				return nil, task, nil
			}
			_ = start.run.Stop(context.Background())
			return nil, nil, &backgroundtask.ForegroundTimeoutError{
				Timeout: timeoutDuration,
				TaskID:  start.taskID,
			}
		case <-ctx.Done():
			_ = start.run.Stop(context.Background())
			return nil, nil, ctx.Err()
		}
	}
}

func (t *managedTool) finishForeground(
	ctx context.Context,
	spec backgroundtask.Spec,
	arguments string,
	toolCheckpoint []byte,
	outcome *Outcome,
) (*schema.ToolResult, error) {
	if outcome != nil && outcome.Status == backgroundtask.StatusWaitingInput {
		if _, ok := t.registration.Tool.(ResumableBackgroundTool); !ok {
			return t.renderForegroundFailure(
				ctx,
				spec.Description,
				"backgroundtask/tool: foreground waiting-input requires a resumable tool",
			)
		}
		return nil, t.interruptForeground(ctx, spec, arguments, toolCheckpoint, outcome)
	}
	result, err := t.renderForegroundOutcome(ctx, spec, outcome)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (t *managedTool) interruptForeground(
	ctx context.Context,
	spec backgroundtask.Spec,
	arguments string,
	toolCheckpoint []byte,
	outcome *Outcome,
) error {
	if outcome == nil || outcome.InputRequest == nil {
		return errors.New("backgroundtask/tool: foreground wait-input requires an input request")
	}
	effectiveCheckpoint := toolCheckpoint
	if len(outcome.Checkpoint) > 0 {
		effectiveCheckpoint = outcome.Checkpoint
	}
	if _, err := encodeManagedCheckpoint(outcome.InputRequest, effectiveCheckpoint); err != nil {
		return err
	}
	state := foregroundToolInterruptState{
		TaskID: spec.ID, ToolName: t.registration.Info.Name,
		Arguments: arguments, RequestID: outcome.InputRequest.ID,
		ToolCheckpoint: append([]byte(nil), effectiveCheckpoint...),
		OutputFile:     spec.OutputFile,
	}
	return componenttool.StatefulInterrupt(ctx, outcome.InputRequest.Data, state)
}

func (t *managedTool) resumeForeground(
	ctx context.Context,
	state foregroundToolInterruptState,
) (*schema.ToolResult, error) {
	if state.ToolName != t.registration.Info.Name || state.TaskID == "" ||
		state.Arguments == "" || state.RequestID == "" {
		return nil, errors.New("backgroundtask/tool: invalid foreground interrupt state")
	}
	isTarget, hasData, data := componenttool.GetResumeContext[json.RawMessage](ctx)
	if !isTarget {
		return nil, componenttool.StatefulInterrupt(ctx, nil, state)
	}
	resumable, ok := t.registration.Tool.(ResumableBackgroundTool)
	if !ok {
		return nil, errors.New("backgroundtask/tool: foreground resume requires a resumable tool")
	}
	resumeData := []byte(nil)
	if hasData {
		resumeData = append([]byte(nil), data...)
	}
	run, err := resumable.Resume(ctx, &ResumeRequest{
		TaskID: state.TaskID, Arguments: state.Arguments, Attempt: 0,
		RequestID: state.RequestID, Data: resumeData,
		Checkpoint: append([]byte(nil), state.ToolCheckpoint...),
	})
	if errors.Is(err, ErrResumeInputRejected) {
		return nil, componenttool.StatefulInterrupt(ctx, nil, state)
	}
	if err != nil {
		return t.renderForegroundFailure(ctx, "", err.Error())
	}
	if run == nil {
		return t.renderForegroundFailure(ctx, "", "backgroundtask/tool: resume returned a nil run")
	}
	_, spec, err := t.specForTaskID(ctx, state.TaskID, state.Arguments, state.OutputFile)
	if err != nil {
		return nil, err
	}
	start := &foregroundStart{
		taskID: state.TaskID, arguments: state.Arguments, spec: spec,
		run: run, toolCheckpoint: append([]byte(nil), state.ToolCheckpoint...),
	}
	outcome, task, err := t.waitForeground(ctx, state.Arguments, start)
	if err != nil {
		return nil, err
	}
	if task != nil {
		return t.renderLaunchResult(ctx, task)
	}
	return t.finishForeground(ctx, spec, state.Arguments, state.ToolCheckpoint, outcome)
}

func (t *managedTool) tryHandoff(
	ctx context.Context,
	start *foregroundStart,
) (*backgroundtask.Task, error) {
	candidate := &foreground.CandidateInfo{
		TaskID: start.taskID, Kind: start.spec.Kind,
		Description: start.spec.Description, OutputFile: start.spec.OutputFile,
	}
	if t.policy.ShouldAutoBackground == nil ||
		!t.policy.ShouldAutoBackground(ctx, candidate) {
		return nil, errors.New("backgroundtask/tool: foreground timeout")
	}
	handoffTool, ok := t.registration.Tool.(ForegroundHandoffTool)
	if !ok {
		return nil, errors.New("backgroundtask/tool: foreground handoff is unsupported")
	}
	adopted, err := handoffTool.Adopt(ctx, &AdoptRequest{
		TaskID: start.taskID, Arguments: start.arguments, Run: start.run,
		ToolCheckpoint: append([]byte(nil), start.toolCheckpoint...),
	})
	if err != nil {
		return nil, err
	}
	if adopted == nil || adopted.Run == nil {
		return nil, errors.New("backgroundtask/tool: handoff returned a nil run")
	}
	checkpoint, err := encodeManagedCheckpoint(nil, adopted.ToolCheckpoint)
	if err != nil {
		_ = adopted.Run.Stop(context.Background())
		return nil, err
	}
	if err = t.registry.adopted.register(start.taskID, adopted.Run, adopted.ToolCheckpoint); err != nil {
		_ = adopted.Run.Stop(context.Background())
		return nil, err
	}
	task, err := t.manager.Submit(ctx, &backgroundtask.SubmitRequest{
		Spec:              start.spec,
		InitialCheckpoint: checkpoint,
	})
	if err != nil && !(errors.Is(err, backgroundtask.ErrTaskCreatedEventUndelivered) && task != nil) {
		t.registry.adopted.remove(start.taskID)
		_ = adopted.Run.Stop(context.Background())
		return nil, err
	}
	go func() {
		_ = t.manager.Execute(detachedContext{parent: ctx}, task.Spec.ID)
	}()
	return task, nil
}

func (t *managedTool) project(
	ctx context.Context,
	taskID string,
	projection *liveProjection,
	runDone <-chan launchResult,
	writer *schema.StreamWriter[*schema.ToolResult],
) {
	defer writer.Close()
	updates := projection.updates
	for {
		select {
		case update, open := <-updates:
			if !open {
				updates = nil
				continue
			}
			record, err := renderEvent(&ManagedToolResponseEvent{Type: ManagedToolResponseEventUpdate, Update: update})
			if err != nil {
				t.registry.projections.remove(taskID)
				writer.Send(nil, err)
				return
			}
			if writer.Send(record, nil) {
				t.registry.projections.remove(taskID)
				return
			}
		case result := <-runDone:
			if result.err != nil {
				t.registry.projections.remove(taskID)
				writer.Send(nil, result.err)
				return
			}
			if result.task == nil {
				t.registry.projections.remove(taskID)
				writer.Send(nil, errors.New("backgroundtask/tool: foreground returned a nil task"))
				return
			}
			// runDone is the explicit detach command for the caller-side
			// projection; durable progress continues on the background task.
			t.registry.projections.remove(taskID)
			final, encodeErr := t.renderLaunchResult(ctx, result.task)
			if encodeErr != nil {
				writer.Send(nil, encodeErr)
				return
			}
			writer.Send(final, nil)
			return
		case <-ctx.Done():
			t.registry.projections.remove(taskID)
			writer.Send(nil, ctx.Err())
			return
		}
	}
}

func (t *managedTool) submit(
	ctx context.Context,
	arguments string,
	withProjection bool,
) (*backgroundtask.Task, *liveProjection, error) {
	taskID, spec, err := t.newSpec(ctx, arguments)
	if err != nil {
		return nil, nil, err
	}
	var projection *liveProjection
	if withProjection {
		projection, err = t.registry.projections.register(taskID)
		if err != nil {
			return nil, nil, err
		}
	}
	removeProjection := func() {
		if projection != nil {
			t.registry.projections.remove(taskID)
		}
	}
	task, err := t.manager.Submit(ctx, &backgroundtask.SubmitRequest{Spec: spec})
	if err != nil && !errors.Is(err, backgroundtask.ErrTaskCreatedEventUndelivered) {
		removeProjection()
		return nil, nil, err
	}
	if err != nil && task == nil {
		removeProjection()
		return nil, nil, errors.New(
			"backgroundtask/tool: task-created delivery failed without persisted task",
		)
	}
	return task, projection, nil
}

func (t *managedTool) newSpec(ctx context.Context, arguments string) (string, backgroundtask.Spec, error) {
	sessionID, err := t.sessionID(ctx)
	if err != nil {
		return "", backgroundtask.Spec{}, err
	}
	taskID, err := t.manager.AllocateTaskID(ctx, &backgroundtask.AllocateTaskIDRequest{
		Kind: "background_tool",
	})
	if err != nil {
		return "", backgroundtask.Spec{}, err
	}
	outputFile := ""
	if t.registration.Materializer != nil {
		outputFile, err = t.registration.Materializer.ReserveOutput(ctx, &ReserveOutputRequest{
			TaskID: taskID,
		})
		if err != nil {
			return "", backgroundtask.Spec{}, fmt.Errorf("backgroundtask/tool: reserve output: %w", err)
		}
		if outputFile == "" {
			return "", backgroundtask.Spec{}, errors.New("backgroundtask/tool: output materializer returned an empty path")
		}
	}
	spec, err := buildTaskSpec(
		ctx,
		t.registration,
		t.recoverable,
		&taskSpecInput{
			taskID: taskID, arguments: arguments, outputFile: outputFile,
			sessionID: sessionID, notifySession: sessionID != "",
		},
	)
	if err != nil {
		return "", backgroundtask.Spec{}, err
	}
	return taskID, spec, nil
}

func (t *managedTool) specForTaskID(
	ctx context.Context,
	taskID string,
	arguments string,
	outputFile string,
) (string, backgroundtask.Spec, error) {
	sessionID, err := t.sessionID(ctx)
	if err != nil {
		return "", backgroundtask.Spec{}, err
	}
	spec, err := buildTaskSpec(
		ctx,
		t.registration,
		t.recoverable,
		&taskSpecInput{
			taskID: taskID, arguments: arguments, outputFile: outputFile,
			sessionID: sessionID, notifySession: sessionID != "",
		},
	)
	if err != nil {
		return "", backgroundtask.Spec{}, err
	}
	return taskID, spec, nil
}

func (t *managedTool) prepareInput(
	ctx context.Context,
	arguments string,
) (string, error) {
	prepared := arguments
	if preparer, ok := t.registration.Tool.(InputPreparer); ok {
		if arguments == "" {
			return "", errors.New("backgroundtask/tool: arguments are required")
		}
		if len(arguments) > maxArgumentsBytes {
			return "", errors.New("backgroundtask/tool: arguments exceed configured bounds")
		}
		var err error
		prepared, err = preparer.PrepareInput(ctx, arguments)
		if err != nil {
			return "", err
		}
		if prepared == "" {
			return "", errors.New(
				"backgroundtask/tool: input preparer returned empty arguments",
			)
		}
		if len(prepared) > maxArgumentsBytes {
			return "", errors.New(
				"backgroundtask/tool: prepared arguments exceed configured bounds",
			)
		}
	}
	if err := validateArguments(t.registration, prepared); err != nil {
		return "", err
	}
	return prepared, nil
}

func (t *managedTool) shouldRunInBackground(ctx context.Context, arguments string) bool {
	return t.runInBackground != nil && t.runInBackground(ctx, arguments)
}

func (t *managedTool) timeout(ctx context.Context, arguments string) *int {
	if t.invocationTimeout == nil {
		return nil
	}
	return t.invocationTimeout(ctx, arguments)
}

func (t *managedTool) timeoutMs(ctx context.Context, arguments string) int {
	if timeout := t.timeout(ctx, arguments); timeout != nil {
		return *timeout
	}
	return t.policy.TimeoutMs
}

func (t *managedTool) foregroundTimeout(
	ctx context.Context,
	arguments string,
) (<-chan time.Time, time.Duration) {
	timeout := time.Duration(t.timeoutMs(ctx, arguments)) * time.Millisecond
	if timeout <= 0 {
		return nil, timeout
	}
	return time.After(timeout), timeout
}

func (t *managedTool) renderForegroundFailure(
	ctx context.Context,
	description string,
	reason string,
) (*schema.ToolResult, error) {
	return renderEvent(&ManagedToolResponseEvent{
		Type:        ManagedToolResponseEventForegroundResult,
		Status:      backgroundtask.StatusFailed,
		Description: description,
		Error:       reason,
	})
}

func (t *managedTool) renderForegroundOutcome(
	ctx context.Context,
	spec backgroundtask.Spec,
	outcome *Outcome,
) (*schema.ToolResult, error) {
	result, err := validateOutcome(outcome, t.recoverable, nil)
	if err != nil {
		return nil, err
	}
	event := &ManagedToolResponseEvent{
		Type:        ManagedToolResponseEventForegroundResult,
		Status:      result.Status,
		Description: spec.Description,
	}
	var rich *schema.ToolResult
	if result.Status == backgroundtask.StatusCompleted {
		if t.registration.RenderResult != nil {
			task := &backgroundtask.Task{
				Spec: spec, Status: backgroundtask.StatusCompleted,
				ResultData: append([]byte(nil), result.Data...),
			}
			rich, err = t.registration.RenderResult(ctx, task)
			if err != nil {
				return nil, fmt.Errorf("backgroundtask/tool: render completed result: %w", err)
			}
			if rich == nil {
				return nil, errors.New(
					"backgroundtask/tool: result renderer returned nil",
				)
			}
		} else if len(result.Data) > 0 {
			var output any
			if json.Unmarshal(result.Data, &output) == nil {
				event.Output = output
			} else {
				event.Output = string(result.Data)
			}
		}
	}
	if result.Status == backgroundtask.StatusFailed || result.Status == backgroundtask.StatusCanceled {
		event.Error = result.Error
	}
	if result.Status == backgroundtask.StatusWaitingInput {
		event.InputRequest = outcome.InputRequest
	}
	rendered, err := renderEvent(event)
	if err != nil {
		return nil, err
	}
	if rich != nil {
		rendered.Parts = append(rendered.Parts, rich.Parts...)
	}
	return rendered, nil
}

func (t *managedTool) renderLaunchResult(
	ctx context.Context,
	task *backgroundtask.Task,
) (*schema.ToolResult, error) {
	if task == nil || task.Spec.ID == "" {
		return nil, errors.New("backgroundtask/tool: launch result requires a task id")
	}
	event := &ManagedToolResponseEvent{
		Type: ManagedToolResponseEventLaunchResult, TaskID: task.Spec.ID, Status: task.Status,
		Description: task.Spec.Description,
	}
	var rich *schema.ToolResult
	if task.Status == backgroundtask.StatusCompleted {
		if t.registration.RenderResult != nil {
			var err error
			rich, err = t.registration.RenderResult(ctx, task)
			if err != nil {
				return nil, fmt.Errorf("backgroundtask/tool: render completed result: %w", err)
			}
			if rich == nil {
				return nil, errors.New(
					"backgroundtask/tool: result renderer returned nil",
				)
			}
		} else if len(task.ResultData) > 0 {
			var output any
			if json.Unmarshal(task.ResultData, &output) == nil {
				event.Output = output
			} else {
				event.Output = string(task.ResultData)
			}
		}
	}
	if task.Status == backgroundtask.StatusFailed || task.Status == backgroundtask.StatusCanceled {
		event.Error = task.ResultError
	}
	if task.Status == backgroundtask.StatusWaitingInput {
		var err error
		event.InputRequest, err = ReadInputRequest(task)
		if err != nil {
			return nil, fmt.Errorf("backgroundtask/tool: render input request: %w", err)
		}
	}
	result, err := renderEvent(event)
	if err != nil {
		return nil, err
	}
	if rich != nil {
		result.Parts = append(result.Parts, rich.Parts...)
	}
	return result, nil
}

func renderEvent(event *ManagedToolResponseEvent) (*schema.ToolResult, error) {
	record, err := encodeEvent(event)
	if err != nil {
		return nil, err
	}
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{{
		Type: schema.ToolPartTypeText,
		Text: record,
	}}}, nil
}

func encodeEvent(event *ManagedToolResponseEvent) (string, error) {
	if err := validateManagedToolResponseEvent(event); err != nil {
		return "", err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("backgroundtask/tool: encode response event: %w", err)
	}
	return string(data) + "\n", nil
}

func validateManagedToolResponseEvent(event *ManagedToolResponseEvent) error {
	if event == nil {
		return errors.New("backgroundtask/tool: response event is required")
	}
	switch event.Type {
	case ManagedToolResponseEventUpdate:
		if event.Update == nil || event.TaskID != "" || event.Status != "" ||
			event.Description != "" || event.Output != nil || event.Error != "" ||
			event.InputRequest != nil {
			return errors.New("backgroundtask/tool: invalid update response event")
		}
	case ManagedToolResponseEventLaunchResult:
		if event.TaskID == "" || event.Status == "" || event.Update != nil {
			return errors.New("backgroundtask/tool: invalid launch-result response event")
		}
		return validateResultPayload(event, "launch result")
	case ManagedToolResponseEventForegroundResult:
		if event.TaskID != "" || event.Status == "" || event.Update != nil {
			return errors.New("backgroundtask/tool: invalid foreground-result response event")
		}
		return validateResultPayload(event, "foreground result")
	default:
		return errors.New("backgroundtask/tool: unknown response event type")
	}
	return nil
}

func validateResultPayload(event *ManagedToolResponseEvent, name string) error {
	if event.Status == backgroundtask.StatusCompleted {
		if event.Error != "" {
			return fmt.Errorf("backgroundtask/tool: completed %s cannot contain error", name)
		}
	} else if event.Output != nil {
		return fmt.Errorf("backgroundtask/tool: non-completed %s cannot contain output", name)
	}
	if event.Status == backgroundtask.StatusWaitingInput {
		if event.InputRequest == nil || event.Error != "" {
			return fmt.Errorf(
				"backgroundtask/tool: waiting %s requires only an input request",
				name,
			)
		}
	} else if event.InputRequest != nil {
		return fmt.Errorf(
			"backgroundtask/tool: non-waiting %s cannot contain an input request",
			name,
		)
	}
	return nil
}

func cloneToolInfo(info *schema.ToolInfo) (*schema.ToolInfo, error) {
	if info == nil {
		return nil, errors.New("backgroundtask/tool: tool info is required")
	}
	data, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	var clone schema.ToolInfo
	if err = json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func sessionIDFromContext(ctx context.Context) (string, error) {
	if sessionID, ok := adk.RunnerSessionID(ctx); ok {
		return sessionID, nil
	}
	return "", nil
}

var (
	_ componenttool.EnhancedInvokableTool  = (*managedTool)(nil)
	_ componenttool.EnhancedStreamableTool = (*managedTool)(nil)
)
