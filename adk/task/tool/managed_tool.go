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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal/taskfirst"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	taskforeground "github.com/cloudwego/eino/adk/task/foreground"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[foregroundToolInterruptState](
		"_eino_adk_backgroundtool_foreground_interrupt_state",
	)
	schema.RegisterName[taskFirstToolInterruptState](
		"_eino_adk_task_tool_task_first_interrupt_state",
	)
}

// ManagedToolConfig configures the framework-owned model-facing wrapper.
type ManagedToolConfig struct {
	// Manager owns task lifecycle and executor registration.
	Manager *background.Manager
	// Registry and ToolName select a required registered implementation.
	Registry *Registry
	ToolName string

	// ForegroundTimeoutMs overrides the default foreground observation timeout.
	// Nil uses the framework default; non-positive disables the timer.
	ForegroundTimeoutMs *int
	// ShouldAutoBackground is evaluated after foreground timeout. Nil means
	// timeout the operation instead of detaching. It may be called concurrently.
	ShouldAutoBackground taskforeground.ShouldAutoBackground
	// ShouldCancelOnCallerAbort overrides the default detach-only behavior.
	ShouldCancelOnCallerAbort taskforeground.ShouldCancelOnCallerAbort
	// RunInBackground requests explicit detachment from JSON arguments. Nil
	// never requests it and takes precedence over foreground timeout.
	RunInBackground func(context.Context, string) bool
	// ForegroundTimeoutMsForInvocation overrides the foreground observation
	// timeout for one invocation. Nil or a nil result uses ForegroundTimeoutMs.
	ForegroundTimeoutMsForInvocation func(context.Context, string) *int
	// SessionID resolves the optional session notification target. An empty
	// result disables session-routed lifecycle notifications. Nil uses the
	// current Runner session when one exists and otherwise disables notification.
	SessionID func(context.Context) (string, error)
}

type managedTool struct {
	manager                        *background.Manager
	registry                       *Registry
	registration                   *Registration
	recoverable                    bool
	info                           *schema.ToolInfo
	policy                         taskfirst.Policy
	runInBackground                func(context.Context, string) bool
	foregroundTimeoutForInvocation func(context.Context, string) *int
	sessionID                      func(context.Context) (string, error)
}

// NewManagedTool creates a wrapper implementing EnhancedInvokableTool and
// EnhancedStreamableTool. Every result includes a text control envelope;
// completed foreground results may append rich parts through
// Registration.RenderResult. Detaching closes only the caller projection;
// durable persistence continues. A foreground timeout without a successful
// handoff returns a *task.ForegroundTimeoutError.
func NewManagedTool(
	ctx context.Context,
	config *ManagedToolConfig,
) (componenttool.BaseTool, error) {
	if config == nil || config.Manager == nil || config.Registry == nil ||
		config.ToolName == "" {
		return nil, errors.New(
			"task/tool: manager, tool registry, and tool name are required",
		)
	}
	if err := registerExecutors(config.Manager, config.Registry); err != nil {
		return nil, err
	}
	registration, recoverable, ok := config.Registry.resolveAny(config.ToolName)
	if !ok {
		return nil, fmt.Errorf("task/tool: tool %q is not registered", config.ToolName)
	}
	info, err := cloneToolInfo(registration.Info)
	if err != nil {
		return nil, fmt.Errorf("task/tool: clone tool info: %w", err)
	}
	info.Desc += "\nA published background handle returns a launch_result " +
		"containing an Eino task_id for task_output and task_stop. Synchronous " +
		"completion returns a foreground_result without a task_id, whether it " +
		"came from direct execution or an unpublished deferred task."
	timeoutMs := taskforeground.DefaultTimeoutMs
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
		policy: taskfirst.Policy{
			TimeoutMs: timeoutMs, ShouldAutoBackground: config.ShouldAutoBackground,
			ShouldCancelOnCallerAbort: config.ShouldCancelOnCallerAbort,
		},
		runInBackground:                config.RunInBackground,
		foregroundTimeoutForInvocation: config.ForegroundTimeoutMsForInvocation,
		sessionID:                      sessionID,
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
		return nil, errors.New("task/tool: tool argument is required")
	}
	if wasInterrupted, hasState, state := componenttool.GetInterruptState[taskFirstToolInterruptState](ctx); wasInterrupted && hasState {
		return t.resumeTaskFirst(ctx, state)
	}
	if wasInterrupted, hasState, state := componenttool.GetInterruptState[foregroundToolInterruptState](ctx); wasInterrupted && hasState {
		return t.resumeForeground(ctx, state)
	}
	arguments := toolArgument.Text
	arguments, err := t.prepareInput(ctx, arguments)
	if err != nil {
		return nil, err
	}
	explicitBackground := t.shouldRunInBackground(ctx, arguments)
	if explicitBackground || t.policy.ShouldAutoBackground != nil ||
		t.policy.ShouldCancelOnCallerAbort != nil {
		return t.runTaskFirst(ctx, arguments, explicitBackground)
	}
	return t.runForeground(ctx, arguments)
}

func (t *managedTool) StreamableRun(
	ctx context.Context,
	toolArgument *schema.ToolArgument,
	_ ...componenttool.Option,
) (*schema.StreamReader[*schema.ToolResult], error) {
	if toolArgument == nil {
		return nil, errors.New("task/tool: tool argument is required")
	}
	if wasInterrupted, hasState, state := componenttool.GetInterruptState[taskFirstToolInterruptState](ctx); wasInterrupted && hasState {
		reader, writer := schema.Pipe[*schema.ToolResult](projectionBuffer)
		go func() {
			defer writer.Close()
			result, err := t.resumeTaskFirst(ctx, state)
			writer.Send(result, err)
		}()
		return reader, nil
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
	explicitBackground := t.shouldRunInBackground(ctx, arguments)
	if explicitBackground || t.policy.ShouldAutoBackground != nil ||
		t.policy.ShouldCancelOnCallerAbort != nil {
		execution, projection, startErr := t.startTaskFirst(
			ctx,
			arguments,
			true,
			explicitBackground,
		)
		if startErr != nil {
			return nil, startErr
		}
		go t.projectTaskFirst(
			ctx,
			arguments,
			execution,
			projection,
			explicitBackground,
			writer,
		)
		return reader, nil
	}
	go t.streamForeground(ctx, arguments, writer)
	return reader, nil
}

type foregroundStart struct {
	taskID            string
	arguments         string
	spec              background.Spec
	run               Run
	toolCheckpoint    []byte
	mailboxGeneration int64
	mailboxCursor     int64
	mailboxFinalizer  *taskfirst.ForegroundMailboxFinalizer
}

type taskFirstToolInterruptState struct {
	TaskID    string
	ToolName  string
	RequestID string
}

func (t *managedTool) startWindowTimeout() time.Duration {
	return time.Duration(t.policy.TimeoutMs) * time.Millisecond
}

func (t *managedTool) runTaskFirst(
	ctx context.Context,
	arguments string,
	explicitBackground bool,
) (*schema.ToolResult, error) {
	execution, _, err := t.startTaskFirst(
		ctx,
		arguments,
		false,
		explicitBackground,
	)
	if err != nil {
		return nil, err
	}
	if explicitBackground {
		snapshot := execution.Initial()
		if current, getErr := t.manager.Get(
			context.Background(),
			execution.TaskID(),
		); getErr == nil {
			snapshot = current
		}
		return t.renderLaunchResult(ctx, snapshot)
	}
	outcome, err := execution.Await(ctx)
	if err != nil {
		return nil, err
	}
	if outcome.Backgrounded {
		return t.renderLaunchResult(ctx, outcome.Task)
	}
	return t.renderForegroundTask(ctx, arguments, outcome.Task)
}

func (t *managedTool) projectTaskFirst(
	ctx context.Context,
	arguments string,
	execution *taskfirst.Execution,
	projection *liveProjection,
	explicitBackground bool,
	writer *schema.StreamWriter[*schema.ToolResult],
) {
	defer writer.Close()
	defer t.registry.projections.remove(execution.TaskID())
	if explicitBackground {
		task := execution.Initial()
		select {
		case <-projection.ready:
			if current, err := t.manager.Get(
				context.Background(),
				execution.TaskID(),
			); err == nil {
				task = current
			}
		case <-execution.Boundary():
			current, err := execution.WaitBoundary(context.Background())
			if err != nil {
				writer.Send(nil, err)
				return
			}
			task = current
		case <-ctx.Done():
		}
		result, err := t.renderLaunchResult(ctx, task)
		writer.Send(result, err)
		return
	}
	updates := projection.updates
	boundary := execution.Boundary()
	timeout := execution.Timeout()
	for {
		select {
		case update, open := <-updates:
			if !open {
				updates = nil
				continue
			}
			record, err := renderEvent(&ManagedToolResponseEvent{
				Type: ManagedToolResponseEventUpdate, Update: update,
			})
			if writer.Send(record, err) {
				_, _ = execution.ResolveCallerAbort(
					ctx,
					context.Canceled,
				)
				return
			}
		case <-boundary:
			task, err := execution.WaitBoundary(context.Background())
			if err != nil {
				writer.Send(nil, err)
				return
			}
			result, err := t.renderForegroundTask(ctx, arguments, task)
			writer.Send(result, err)
			return
		case <-timeout:
			outcome, err := execution.ResolveTimeout(ctx)
			if err != nil {
				writer.Send(nil, err)
				return
			}
			if outcome.Backgrounded {
				result, renderErr := t.renderLaunchResult(ctx, outcome.Task)
				writer.Send(result, renderErr)
				return
			}
			writer.Send(nil, execution.ForegroundTimeoutError())
			return
		case <-ctx.Done():
			_, _ = execution.ResolveCallerAbort(
				ctx,
				ctx.Err(),
			)
			return
		}
	}
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
	return t.finishForeground(ctx, start, outcome)
}

type foregroundToolInterruptState struct {
	TaskID            string
	ToolName          string
	Arguments         string
	RequestID         string
	ToolCheckpoint    []byte
	OutputFile        string
	MailboxGeneration int64
	MailboxCursor     int64
}

type foregroundWaitResult struct {
	outcome *Outcome
	err     error
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
			_ = start.run.Stop(context.Background())
			finalizeErr := start.mailboxFinalizer.Abandon()
			result, encodeErr := t.renderForegroundFailure(
				ctx,
				start.spec.Description,
				"task/tool: update source returned a nil reader",
			)
			writer.Send(
				result,
				taskfirst.CombineForegroundErrors(encodeErr, finalizeErr),
			)
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
	waitResult := make(chan foregroundWaitResult, 1)
	seenUpdates := make(map[string][]byte)
	go func() {
		outcome, waitErr := start.run.Wait(waitCtx)
		waitResult <- foregroundWaitResult{outcome: outcome, err: waitErr}
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
				_ = start.run.Stop(context.Background())
				writer.Send(
					nil,
					taskfirst.CombineForegroundErrors(
						update.err,
						start.mailboxFinalizer.Abandon(),
					),
				)
				return
			}
			first, err := t.processForegroundUpdate(ctx, start.spec, update.update, seenUpdates)
			if err != nil {
				_ = start.run.Stop(context.Background())
				writer.Send(
					nil,
					taskfirst.CombineForegroundErrors(
						err,
						start.mailboxFinalizer.Abandon(),
					),
				)
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
				_ = start.mailboxFinalizer.Abandon()
				return
			}
		case result := <-waitResult:
			outcome, waitErr := resolveForegroundWaitResult(ctx, start, result)
			if waitErr != nil {
				writer.Send(nil, waitErr)
				return
			}
			if err := t.drainForegroundUpdatesToWriter(ctx, start.spec, updateResults, seenUpdates, writer); err != nil {
				_ = start.run.Stop(context.Background())
				finalizeErr := start.mailboxFinalizer.Abandon()
				final, encodeErr := t.renderForegroundFailure(
					ctx, start.spec.Description, err.Error(),
				)
				writer.Send(
					final,
					taskfirst.CombineForegroundErrors(encodeErr, finalizeErr),
				)
				return
			}
			if outcome == nil ||
				outcome.Status != task.OutcomeInterrupted {
				final, encodeErr := t.renderForegroundOutcome(
					ctx,
					start.spec,
					outcome,
				)
				if encodeErr != nil {
					writer.Send(
						nil,
						taskfirst.CombineForegroundErrors(
							encodeErr,
							start.mailboxFinalizer.Abandon(),
						),
					)
					return
				}
				var finalizeErr error
				if outcome != nil &&
					outcome.Status == task.OutcomeCompleted {
					finalizeErr = start.mailboxFinalizer.SealIfIdle()
				} else {
					finalizeErr = start.mailboxFinalizer.Abandon()
				}
				writer.Send(final, finalizeErr)
				return
			}
			final, encodeErr := t.finishForeground(
				ctx,
				start,
				outcome,
			)
			writer.Send(final, encodeErr)
			return
		case <-timeout:
			_ = start.run.Stop(context.Background())
			finalizeErr := start.mailboxFinalizer.Abandon()
			writer.Send(
				nil,
				taskfirst.CombineForegroundErrors(
					&task.ForegroundTimeoutError{
						Timeout: timeoutDuration,
						TaskID:  start.taskID,
					},
					finalizeErr,
				),
			)
			return
		case <-ctx.Done():
			writer.Send(nil, cancelForegroundRun(start, ctx.Err()))
			return
		}
	}
}

func (t *managedTool) drainForegroundUpdates(
	ctx context.Context,
	spec background.Spec,
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
			return errors.New("task/tool: update stream did not close after terminal outcome")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *managedTool) drainForegroundUpdatesToWriter(
	ctx context.Context,
	spec background.Spec,
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
			return errors.New("task/tool: update stream did not close after terminal outcome")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *managedTool) processForegroundUpdate(
	ctx context.Context,
	spec background.Spec,
	update *Update,
	seen map[string][]byte,
) (bool, error) {
	if update == nil {
		return false, errors.New("task/tool: update must not be nil")
	}
	if err := validateUpdate(update); err != nil {
		return false, err
	}
	if t.recoverable && update.EventID == "" {
		return false, errors.New("task/tool: recoverable update event id is required")
	}
	first := true
	if update.EventID != "" {
		if previous, ok := seen[update.EventID]; ok {
			if !bytes.Equal(previous, update.Data) {
				return false, background.ErrTaskEventPartConflict
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
	invocationID := spec.RootSessionID + ":" + compose.GetToolCallID(ctx)
	if compose.GetToolCallID(ctx) == "" {
		invocationID = "tool:" + taskID
	}
	var parentExecution *task.ExecutionContext
	if execution, ok := task.ExecutionContextFromContext(ctx); ok {
		spec.ParentTaskID = execution.TaskID
		copy := execution
		parentExecution = &copy
	}
	registerRequest := &task.RegisterMailboxRequest{
		CandidateTaskID: taskID, InvocationID: invocationID,
		Identity: append([]byte(nil), spec.Payload...),
	}
	if parentExecution == nil {
		registerRequest.RootSessionID = spec.RootSessionID
	} else {
		registerRequest.ParentExecution = parentExecution
	}
	registered, err := t.manager.RegisterMailbox(ctx, registerRequest)
	if err != nil {
		return nil, err
	}
	taskID = registered.Mailbox.TaskID
	spec.ID = taskID
	finalizer := taskfirst.NewForegroundMailboxFinalizer(
		t.manager,
		taskID,
		registered.Mailbox.Generation,
		registered.Mailbox.ConsumedCursor,
	)
	runCtx := task.WithExecutionContext(ctx, task.ExecutionContext{
		TaskID: taskID, Owner: task.OwnerParent,
		Generation:    registered.Mailbox.Generation,
		RootSessionID: spec.RootSessionID,
	})
	startResult, err := t.registration.Tool.Start(runCtx, &StartRequest{
		TaskID: taskID, Arguments: arguments, Attempt: 0,
	})
	if err != nil {
		return nil, taskfirst.CombineForegroundErrors(err, finalizer.Abandon())
	}
	if startResult == nil || startResult.Run == nil {
		return nil, taskfirst.CombineForegroundErrors(
			errors.New("task/tool: implementation returned a nil start result"),
			finalizer.Abandon(),
		)
	}
	if !t.recoverable && len(startResult.Checkpoint) > 0 {
		_ = startResult.Run.Stop(context.Background())
		return nil, taskfirst.CombineForegroundErrors(
			errors.New("task/tool: plain tool cannot return a checkpoint"),
			finalizer.Abandon(),
		)
	}
	return &foregroundStart{
		taskID: taskID, arguments: arguments, spec: spec, run: startResult.Run,
		toolCheckpoint:    append([]byte(nil), startResult.Checkpoint...),
		mailboxGeneration: registered.Mailbox.Generation,
		mailboxCursor:     registered.Mailbox.ConsumedCursor,
		mailboxFinalizer:  finalizer,
	}, nil
}

func (t *managedTool) waitForeground(
	ctx context.Context,
	arguments string,
	start *foregroundStart,
) (*Outcome, *background.TaskSnapshot, error) {
	var updates *schema.StreamReader[*Update]
	var updateResults <-chan updateResult
	var stopUpdates chan struct{}
	if source, ok := start.run.(UpdateSource); ok {
		updates = source.Updates()
		if updates == nil {
			return &Outcome{
				Status: task.OutcomeFailed,
				Error:  "task/tool: update source returned a nil reader",
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
	waitResult := make(chan foregroundWaitResult, 1)
	seenUpdates := make(map[string][]byte)
	go func() {
		outcome, waitErr := start.run.Wait(waitCtx)
		waitResult <- foregroundWaitResult{outcome: outcome, err: waitErr}
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
				return &Outcome{Status: task.OutcomeFailed, Error: update.err.Error()}, nil, nil
			}
			_, err := t.processForegroundUpdate(ctx, start.spec, update.update, seenUpdates)
			if err != nil {
				_ = start.run.Stop(context.Background())
				return &Outcome{Status: task.OutcomeFailed, Error: err.Error()}, nil, nil
			}
		case result := <-waitResult:
			outcome, waitErr := resolveForegroundWaitResult(ctx, start, result)
			if waitErr != nil {
				return nil, nil, waitErr
			}
			if err := t.drainForegroundUpdates(ctx, start.spec, updateResults, seenUpdates); err != nil {
				return &Outcome{Status: task.OutcomeFailed, Error: err.Error()}, nil, nil
			}
			return outcome, nil, nil
		case <-timeout:
			_ = start.run.Stop(context.Background())
			return nil, nil, taskfirst.CombineForegroundErrors(
				&task.ForegroundTimeoutError{
					Timeout: timeoutDuration,
					TaskID:  start.taskID,
				},
				start.mailboxFinalizer.Abandon(),
			)
		case <-ctx.Done():
			return nil, nil, cancelForegroundRun(start, ctx.Err())
		}
	}
}

func resolveForegroundWaitResult(
	ctx context.Context,
	start *foregroundStart,
	result foregroundWaitResult,
) (*Outcome, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, cancelForegroundRun(start, ctxErr)
	}
	if result.err != nil {
		return &Outcome{Status: task.OutcomeFailed, Error: result.err.Error()}, nil
	}
	return result.outcome, nil
}

func cancelForegroundRun(start *foregroundStart, err error) error {
	_ = start.run.Stop(context.Background())
	return taskfirst.CombineForegroundErrors(
		err,
		start.mailboxFinalizer.Abandon(),
	)
}

func (t *managedTool) finishForeground(
	ctx context.Context,
	start *foregroundStart,
	outcome *Outcome,
) (*schema.ToolResult, error) {
	if outcome != nil && outcome.Status == task.OutcomeInterrupted {
		if _, ok := t.registration.Tool.(ResumableTool); !ok {
			result, err := t.renderForegroundFailure(
				ctx,
				start.spec.Description,
				"task/tool: foreground waiting-input requires a resumable tool",
			)
			return result, taskfirst.CombineForegroundErrors(
				err,
				start.mailboxFinalizer.Abandon(),
			)
		}
		_, err := validateOutcome(
			outcome,
			true,
			start.toolCheckpoint,
			0,
		)
		if err != nil {
			return nil, taskfirst.CombineForegroundErrors(
				err,
				start.mailboxFinalizer.Abandon(),
			)
		}
		return nil, t.interruptForeground(ctx, start, outcome)
	}
	result, err := t.renderForegroundOutcome(ctx, start.spec, outcome)
	if err != nil {
		return nil, taskfirst.CombineForegroundErrors(
			err,
			start.mailboxFinalizer.Abandon(),
		)
	}
	var finalizeErr error
	if outcome.Status == task.OutcomeCompleted {
		finalizeErr = start.mailboxFinalizer.SealIfIdle()
	} else {
		finalizeErr = start.mailboxFinalizer.Abandon()
	}
	return result, finalizeErr
}

func lifecycleStatusForOutcome(status task.OutcomeStatus) (background.Status, error) {
	switch status {
	case task.OutcomeCompleted:
		return background.StatusCompleted, nil
	case task.OutcomeInterrupted:
		return background.StatusWaitingInput, nil
	case task.OutcomeFailed:
		return background.StatusFailed, nil
	case task.OutcomeCanceled:
		return background.StatusCanceled, nil
	default:
		return "", fmt.Errorf("task/tool: unsupported outcome status %v", status)
	}
}

func (t *managedTool) interruptForeground(
	ctx context.Context,
	start *foregroundStart,
	outcome *Outcome,
) error {
	if outcome == nil || outcome.InputRequest == nil {
		return errors.New("task/tool: foreground wait-input requires an input request")
	}
	effectiveCheckpoint := start.toolCheckpoint
	if len(outcome.Checkpoint) > 0 {
		effectiveCheckpoint = outcome.Checkpoint
	}
	if _, err := encodeManagedCheckpoint(outcome.InputRequest, effectiveCheckpoint); err != nil {
		return err
	}
	state := foregroundToolInterruptState{
		TaskID: start.spec.ID, ToolName: t.registration.Info.Name,
		Arguments: start.arguments, RequestID: outcome.InputRequest.ID,
		ToolCheckpoint:    append([]byte(nil), effectiveCheckpoint...),
		OutputFile:        start.spec.OutputFile,
		MailboxGeneration: start.mailboxGeneration,
		MailboxCursor:     start.mailboxCursor,
	}
	return componenttool.StatefulInterrupt(ctx, outcome.InputRequest.Data, state)
}

func (t *managedTool) resumeForeground(
	ctx context.Context,
	state foregroundToolInterruptState,
) (*schema.ToolResult, error) {
	if state.ToolName != t.registration.Info.Name || state.TaskID == "" ||
		state.Arguments == "" || state.RequestID == "" ||
		state.MailboxGeneration <= 0 {
		return nil, errors.New("task/tool: invalid foreground interrupt state")
	}
	isTarget, hasData, data := componenttool.GetResumeContext[json.RawMessage](ctx)
	if !isTarget {
		return nil, componenttool.StatefulInterrupt(ctx, nil, state)
	}
	finalizer := taskfirst.NewForegroundMailboxFinalizer(
		t.manager,
		state.TaskID,
		state.MailboxGeneration,
		state.MailboxCursor,
	)
	resumable, ok := t.registration.Tool.(ResumableTool)
	if !ok {
		return nil, taskfirst.CombineForegroundErrors(
			errors.New("task/tool: foreground resume requires a resumable tool"),
			finalizer.Abandon(),
		)
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
		result, renderErr := t.renderForegroundFailure(ctx, "", err.Error())
		return result, taskfirst.CombineForegroundErrors(
			renderErr,
			finalizer.Abandon(),
		)
	}
	if run == nil {
		result, renderErr := t.renderForegroundFailure(
			ctx,
			"",
			"task/tool: resume returned a nil run",
		)
		return result, taskfirst.CombineForegroundErrors(
			renderErr,
			finalizer.Abandon(),
		)
	}
	_, spec, err := t.specForTaskID(ctx, state.TaskID, state.Arguments, state.OutputFile)
	if err != nil {
		return nil, taskfirst.CombineForegroundErrors(err, finalizer.Abandon())
	}
	start := &foregroundStart{
		taskID: state.TaskID, arguments: state.Arguments, spec: spec,
		run: run, toolCheckpoint: append([]byte(nil), state.ToolCheckpoint...),
		mailboxGeneration: state.MailboxGeneration,
		mailboxCursor:     state.MailboxCursor,
		mailboxFinalizer:  finalizer,
	}
	outcome, task, err := t.waitForeground(ctx, state.Arguments, start)
	if err != nil {
		return nil, taskfirst.CombineForegroundErrors(err, finalizer.Abandon())
	}
	if task != nil {
		return t.renderLaunchResult(ctx, task)
	}
	return t.finishForeground(ctx, start, outcome)
}

func (t *managedTool) startTaskFirst(
	ctx context.Context,
	arguments string,
	withProjection bool,
	explicitBackground bool,
) (*taskfirst.Execution, *liveProjection, error) {
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
	policy := t.policy
	policy.TimeoutMs = t.foregroundTimeoutMs(ctx, arguments)
	request := &taskfirst.StartRequest{
		Spec: spec, ExplicitBackground: explicitBackground,
	}
	if explicitBackground && !withProjection {
		request.WaitForStart = t.startWindowTimeout()
	}
	execution, err := taskfirst.Start(ctx, t.manager, &policy, request)
	if err != nil {
		removeProjection()
		return nil, nil, err
	}
	return execution, projection, nil
}

func (t *managedTool) newSpec(ctx context.Context, arguments string) (string, background.Spec, error) {
	sessionID, err := t.sessionID(ctx)
	if err != nil {
		return "", background.Spec{}, err
	}
	taskID, err := t.manager.AllocateTaskID(ctx, &background.AllocateTaskIDRequest{
		Kind: "background_tool",
	})
	if err != nil {
		return "", background.Spec{}, err
	}
	outputFile := ""
	if t.registration.Materializer != nil {
		outputFile, err = t.registration.Materializer.ReserveOutput(ctx, &ReserveOutputRequest{
			TaskID: taskID,
		})
		if err != nil {
			return "", background.Spec{}, fmt.Errorf("task/tool: reserve output: %w", err)
		}
		if outputFile == "" {
			return "", background.Spec{}, errors.New("task/tool: output materializer returned an empty path")
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
		return "", background.Spec{}, err
	}
	if execution, ok := task.ExecutionContextFromContext(ctx); ok {
		spec.ParentTaskID = execution.TaskID
		if execution.RootSessionID != "" {
			spec.RootSessionID = execution.RootSessionID
		}
	}
	return taskID, spec, nil
}

func (t *managedTool) specForTaskID(
	ctx context.Context,
	taskID string,
	arguments string,
	outputFile string,
) (string, background.Spec, error) {
	sessionID, err := t.sessionID(ctx)
	if err != nil {
		return "", background.Spec{}, err
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
		return "", background.Spec{}, err
	}
	if execution, ok := task.ExecutionContextFromContext(ctx); ok {
		spec.ParentTaskID = execution.TaskID
		if execution.RootSessionID != "" {
			spec.RootSessionID = execution.RootSessionID
		}
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
			return "", errors.New("task/tool: arguments are required")
		}
		if len(arguments) > maxArgumentsBytes {
			return "", errors.New("task/tool: arguments exceed configured bounds")
		}
		var err error
		prepared, err = preparer.PrepareInput(ctx, arguments)
		if err != nil {
			return "", err
		}
		if prepared == "" {
			return "", errors.New(
				"task/tool: input preparer returned empty arguments",
			)
		}
		if len(prepared) > maxArgumentsBytes {
			return "", errors.New(
				"task/tool: prepared arguments exceed configured bounds",
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

func (t *managedTool) foregroundTimeoutOverride(
	ctx context.Context,
	arguments string,
) *int {
	if t.foregroundTimeoutForInvocation == nil {
		return nil
	}
	return t.foregroundTimeoutForInvocation(ctx, arguments)
}

func (t *managedTool) foregroundTimeoutMs(
	ctx context.Context,
	arguments string,
) int {
	if timeout := t.foregroundTimeoutOverride(ctx, arguments); timeout != nil {
		return *timeout
	}
	return t.policy.TimeoutMs
}

func (t *managedTool) foregroundTimeout(
	ctx context.Context,
	arguments string,
) (<-chan time.Time, time.Duration) {
	timeout := time.Duration(t.foregroundTimeoutMs(ctx, arguments)) * time.Millisecond
	if timeout <= 0 {
		return nil, timeout
	}
	return time.After(timeout), timeout
}

func (t *managedTool) renderForegroundTask(
	ctx context.Context,
	arguments string,
	snapshot *background.TaskSnapshot,
) (*schema.ToolResult, error) {
	if snapshot == nil {
		return nil, errors.New("task/tool: foreground task result is required")
	}
	switch snapshot.Status {
	case background.StatusWaitingInput:
		request, err := ReadInputRequest(snapshot)
		if err != nil {
			return nil, err
		}
		return nil, componenttool.StatefulInterrupt(
			ctx,
			request.Data,
			taskFirstToolInterruptState{
				TaskID: snapshot.Spec.ID, ToolName: t.registration.Info.Name,
				RequestID: request.ID,
			},
		)
	case background.StatusCompleted:
		return t.renderForegroundOutcome(ctx, snapshot.Spec, &Outcome{
			Status: task.OutcomeCompleted,
			Data:   append([]byte(nil), snapshot.ResultData...),
		})
	case background.StatusFailed:
		return t.renderForegroundOutcome(ctx, snapshot.Spec, &Outcome{
			Status: task.OutcomeFailed, Error: snapshot.ResultError,
		})
	case background.StatusCanceled:
		if strings.HasPrefix(snapshot.ResultError, "timed out after ") {
			return t.renderForegroundFailure(
				ctx,
				snapshot.Spec.Description,
				snapshot.ResultError,
			)
		}
		return t.renderForegroundOutcome(ctx, snapshot.Spec, &Outcome{
			Status: task.OutcomeCanceled, Error: snapshot.ResultError,
		})
	default:
		return nil, fmt.Errorf(
			"task/tool: foreground task reached non-boundary status %q",
			snapshot.Status,
		)
	}
}

func (t *managedTool) resumeTaskFirst(
	ctx context.Context,
	state taskFirstToolInterruptState,
) (*schema.ToolResult, error) {
	if state.TaskID == "" || state.ToolName != t.registration.Info.Name ||
		state.RequestID == "" {
		return nil, errors.New("task/tool: invalid task-first interrupt state")
	}
	isTarget, hasData, data := componenttool.GetResumeContext[json.RawMessage](ctx)
	if !isTarget {
		return nil, componenttool.StatefulInterrupt(ctx, nil, state)
	}
	var resumeData []byte
	if hasData {
		resumeData = append([]byte(nil), data...)
	}
	eventID := fmt.Sprintf(
		"resume:%s:%x",
		state.RequestID,
		sha256.Sum256(resumeData),
	)
	_, err := t.manager.SendInput(ctx, &task.SendInputRequest{
		TaskID: state.TaskID,
		Input: task.Input{
			EventID: eventID, Kind: ResumeInputKind, Data: resumeData,
		},
	})
	if err != nil {
		return nil, err
	}
	policy := t.policy
	execution, err := taskfirst.Observe(ctx, t.manager, &policy, state.TaskID)
	if err != nil {
		return nil, err
	}
	outcome, err := execution.Await(ctx)
	if err != nil {
		return nil, err
	}
	if outcome.Backgrounded {
		return t.renderLaunchResult(ctx, outcome.Task)
	}
	return t.renderForegroundTask(ctx, "", outcome.Task)
}

func (t *managedTool) renderForegroundFailure(
	ctx context.Context,
	description string,
	reason string,
) (*schema.ToolResult, error) {
	return renderEvent(&ManagedToolResponseEvent{
		Type:        ManagedToolResponseEventForegroundResult,
		Status:      background.StatusFailed,
		Description: description,
		Error:       reason,
	})
}

func (t *managedTool) renderForegroundOutcome(
	ctx context.Context,
	spec background.Spec,
	outcome *Outcome,
) (*schema.ToolResult, error) {
	result, err := validateOutcome(outcome, t.recoverable, nil, 0)
	if err != nil {
		return nil, err
	}
	status, err := lifecycleStatusForOutcome(outcome.Status)
	if err != nil {
		return nil, err
	}
	event := &ManagedToolResponseEvent{
		Type:        ManagedToolResponseEventForegroundResult,
		Status:      status,
		Description: spec.Description,
	}
	var rich *schema.ToolResult
	if outcome.Status == task.OutcomeCompleted {
		if t.registration.RenderResult != nil {
			task := &background.TaskSnapshot{
				Spec: spec, Status: background.StatusCompleted,
				ResultData: append([]byte(nil), result.Data...),
			}
			rich, err = t.registration.RenderResult(ctx, task)
			if err != nil {
				return nil, fmt.Errorf("task/tool: render completed result: %w", err)
			}
			if rich == nil {
				return nil, errors.New(
					"task/tool: result renderer returned nil",
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
	if outcome.Status == task.OutcomeFailed || outcome.Status == task.OutcomeCanceled {
		event.Error = result.Error
	}
	if outcome.Status == task.OutcomeInterrupted {
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
	task *background.TaskSnapshot,
) (*schema.ToolResult, error) {
	if task == nil || task.Spec.ID == "" {
		return nil, errors.New("task/tool: launch result requires a task id")
	}
	event := &ManagedToolResponseEvent{
		Type: ManagedToolResponseEventLaunchResult, TaskID: task.Spec.ID, Status: task.Status,
		Description: task.Spec.Description,
	}
	var rich *schema.ToolResult
	if task.Status == background.StatusCompleted {
		if t.registration.RenderResult != nil {
			var err error
			rich, err = t.registration.RenderResult(ctx, task)
			if err != nil {
				return nil, fmt.Errorf("task/tool: render completed result: %w", err)
			}
			if rich == nil {
				return nil, errors.New(
					"task/tool: result renderer returned nil",
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
	if task.Status == background.StatusFailed || task.Status == background.StatusCanceled {
		event.Error = task.ResultError
	}
	if task.Status == background.StatusWaitingInput {
		var err error
		event.InputRequest, err = ReadInputRequest(task)
		if err != nil {
			return nil, fmt.Errorf("task/tool: render input request: %w", err)
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
		return "", fmt.Errorf("task/tool: encode response event: %w", err)
	}
	return string(data) + "\n", nil
}

func validateManagedToolResponseEvent(event *ManagedToolResponseEvent) error {
	if event == nil {
		return errors.New("task/tool: response event is required")
	}
	switch event.Type {
	case ManagedToolResponseEventUpdate:
		if event.Update == nil || event.TaskID != "" || event.Status != "" ||
			event.Description != "" || event.Output != nil || event.Error != "" ||
			event.InputRequest != nil {
			return errors.New("task/tool: invalid update response event")
		}
	case ManagedToolResponseEventLaunchResult:
		if event.TaskID == "" || event.Status == "" || event.Update != nil {
			return errors.New("task/tool: invalid launch-result response event")
		}
		return validateResultPayload(event, "launch result")
	case ManagedToolResponseEventForegroundResult:
		if event.TaskID != "" || event.Status == "" || event.Update != nil {
			return errors.New("task/tool: invalid foreground-result response event")
		}
		return validateResultPayload(event, "foreground result")
	default:
		return errors.New("task/tool: unknown response event type")
	}
	return nil
}

func validateResultPayload(event *ManagedToolResponseEvent, name string) error {
	if event.Status == background.StatusCompleted {
		if event.Error != "" {
			return fmt.Errorf("task/tool: completed %s cannot contain error", name)
		}
	} else if event.Output != nil {
		return fmt.Errorf("task/tool: non-completed %s cannot contain output", name)
	}
	if event.Status == background.StatusWaitingInput {
		if event.InputRequest == nil || event.Error != "" {
			return fmt.Errorf(
				"task/tool: waiting %s requires only an input request",
				name,
			)
		}
	} else if event.InputRequest != nil {
		return fmt.Errorf(
			"task/tool: non-waiting %s cannot contain an input request",
			name,
		)
	}
	return nil
}

func cloneToolInfo(info *schema.ToolInfo) (*schema.ToolInfo, error) {
	if info == nil {
		return nil, errors.New("task/tool: tool info is required")
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
