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
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/foreground"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

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
	ShouldAutoBackground func(context.Context, *backgroundtask.Task) bool
	// RunInBackground requests explicit detachment from JSON arguments. Nil
	// never requests it and takes precedence over foreground timeout.
	RunInBackground func(context.Context, string) bool
	// InvocationTimeoutMs returns an optional operation timeout in milliseconds.
	// Nil or a nil result means no operation timeout.
	InvocationTimeoutMs func(context.Context, string) *int
	// SessionID resolves the parent session for notification. Nil reads request
	// identity from context.
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

// NewManagedTool creates a wrapper implementing both InvokableTool and
// StreamableTool. Invokable execution returns one NDJSON launch-result record;
// streaming execution emits progress records followed by launch-result.
// Detaching closes only the caller projection; durable persistence continues.
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
	arguments string,
	_ ...componenttool.Option,
) (string, error) {
	task, _, err := t.submit(ctx, arguments, false)
	if err != nil {
		return "", err
	}
	task, err = foreground.Run(ctx, t.manager, t.policy, &foreground.Request{
		TaskID: task.Spec.ID, RunInBackground: t.shouldRunInBackground(ctx, arguments),
		TimeoutMs: t.timeout(ctx, arguments),
	})
	if err != nil {
		return "", err
	}
	return t.encodeLaunchResult(ctx, task)
}

func (t *managedTool) StreamableRun(
	ctx context.Context,
	arguments string,
	_ ...componenttool.Option,
) (*schema.StreamReader[string], error) {
	task, projection, err := t.submit(ctx, arguments, true)
	if err != nil {
		return nil, err
	}
	runDone := make(chan launchResult, 1)
	go func() {
		result, runErr := foreground.Run(ctx, t.manager, t.policy, &foreground.Request{
			TaskID: task.Spec.ID, RunInBackground: t.shouldRunInBackground(ctx, arguments),
			TimeoutMs: t.timeout(ctx, arguments), ProjectionReady: projection.ready,
		})
		runDone <- launchResult{task: result, err: runErr}
	}()
	reader, writer := schema.Pipe[string](projectionBuffer)
	go t.project(ctx, task.Spec.ID, projection, runDone, writer)
	return reader, nil
}

type launchResult struct {
	task *backgroundtask.Task
	err  error
}

func (t *managedTool) project(
	ctx context.Context,
	taskID string,
	projection *liveProjection,
	runDone <-chan launchResult,
	writer *schema.StreamWriter[string],
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
			record, err := encodeEvent(&ManagedToolResponseEvent{Type: ManagedToolResponseEventUpdate, Update: update})
			if err != nil {
				t.registry.projections.remove(taskID)
				writer.Send("", err)
				return
			}
			if writer.Send(record, nil) {
				t.registry.projections.remove(taskID)
				return
			}
		case result := <-runDone:
			if result.err != nil {
				t.registry.projections.remove(taskID)
				writer.Send("", result.err)
				return
			}
			if result.task == nil {
				t.registry.projections.remove(taskID)
				writer.Send("", errors.New("backgroundtask/tool: foreground returned a nil task"))
				return
			}
			if result.task.Status != backgroundtask.StatusRunning && updates != nil {
				for update := range updates {
					record, encodeErr := encodeEvent(&ManagedToolResponseEvent{Type: ManagedToolResponseEventUpdate, Update: update})
					if encodeErr != nil {
						t.registry.projections.remove(taskID)
						writer.Send("", encodeErr)
						return
					}
					if writer.Send(record, nil) {
						t.registry.projections.remove(taskID)
						return
					}
				}
			}
			t.registry.projections.remove(taskID)
			final, encodeErr := t.encodeLaunchResult(ctx, result.task)
			if encodeErr != nil {
				writer.Send("", encodeErr)
				return
			}
			writer.Send(final, nil)
			return
		case <-ctx.Done():
			t.registry.projections.remove(taskID)
			writer.Send("", ctx.Err())
			return
		}
	}
}

func (t *managedTool) submit(
	ctx context.Context,
	arguments string,
	withProjection bool,
) (*backgroundtask.Task, *liveProjection, error) {
	if arguments == "" {
		return nil, nil, errors.New("backgroundtask/tool: arguments are required")
	}
	if len(arguments) > maxArgumentsBytes {
		return nil, nil, errors.New("backgroundtask/tool: arguments exceed configured bounds")
	}
	if err := t.registration.Tool.ValidateArguments(arguments); err != nil {
		return nil, nil, fmt.Errorf("backgroundtask/tool: validate arguments: %w", err)
	}
	sessionID, err := t.sessionID(ctx)
	if err != nil {
		return nil, nil, err
	}
	if sessionID == "" {
		return nil, nil, errors.New("backgroundtask/tool: parent session is required")
	}
	taskID, err := t.manager.AllocateTaskID(ctx, &backgroundtask.AllocateTaskIDRequest{
		Kind: "background_tool",
	})
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
	outputFile := ""
	if t.registration.Materializer != nil {
		outputFile, err = t.registration.Materializer.ReserveOutput(ctx, &ReserveOutputRequest{
			TaskID: taskID,
		})
		if err != nil {
			removeProjection()
			return nil, nil, fmt.Errorf("backgroundtask/tool: reserve output: %w", err)
		}
		if outputFile == "" {
			removeProjection()
			return nil, nil, errors.New("backgroundtask/tool: output materializer returned an empty path")
		}
	}
	payload, err := json.Marshal(&taskPayload{
		Version: payloadVersion, ToolName: t.registration.Info.Name,
		ToolCallID: compose.GetToolCallID(ctx), Arguments: arguments,
	})
	if err != nil {
		removeProjection()
		return nil, nil, fmt.Errorf("backgroundtask/tool: encode payload: %w", err)
	}
	description := t.registration.Info.Name
	if t.registration.Description != nil {
		description = t.registration.Description(arguments)
	}
	executorKey := ExecutorKey
	if t.recoverable {
		executorKey = RecoverableExecutorKey
	}
	task, err := t.manager.Submit(ctx, backgroundtask.Spec{
		ID: taskID, ExecutorKey: executorKey, Kind: "background_tool",
		Payload: payload, Description: description, OutputFile: outputFile,
		SessionID: sessionID, NotifySession: true,
	})
	if err != nil {
		removeProjection()
		return nil, nil, err
	}
	return task, projection, nil
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

func (t *managedTool) encodeLaunchResult(
	ctx context.Context,
	task *backgroundtask.Task,
) (string, error) {
	if task == nil || task.Spec.ID == "" {
		return "", errors.New("backgroundtask/tool: launch result requires a task id")
	}
	event := &ManagedToolResponseEvent{
		Type: ManagedToolResponseEventLaunchResult, TaskID: task.Spec.ID, Status: task.Status,
		Description: task.Spec.Description,
	}
	if task.Status == backgroundtask.StatusCompleted {
		if t.registration.LaunchOutput != nil {
			output, err := t.registration.LaunchOutput(ctx, task)
			if err != nil {
				return "", fmt.Errorf("backgroundtask/tool: build launch output: %w", err)
			}
			event.Output = output
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
	return encodeEvent(event)
}

func encodeEvent(event *ManagedToolResponseEvent) (string, error) {
	if err := validateManagedToolResponseEvent(event); err != nil {
		return "", err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("backgroundtask/tool: encode stream event: %w", err)
	}
	return string(data) + "\n", nil
}

func validateManagedToolResponseEvent(event *ManagedToolResponseEvent) error {
	if event == nil {
		return errors.New("backgroundtask/tool: stream event is required")
	}
	switch event.Type {
	case ManagedToolResponseEventUpdate:
		if event.Update == nil || event.TaskID != "" || event.Status != "" ||
			event.Description != "" || event.Output != nil || event.Error != "" {
			return errors.New("backgroundtask/tool: invalid update stream event")
		}
	case ManagedToolResponseEventLaunchResult:
		if event.TaskID == "" || event.Status == "" || event.Update != nil {
			return errors.New("backgroundtask/tool: invalid launch-result stream event")
		}
		if event.Status == backgroundtask.StatusCompleted {
			if event.Error != "" {
				return errors.New("backgroundtask/tool: completed launch result cannot contain error")
			}
		} else if event.Output != nil {
			return errors.New("backgroundtask/tool: non-completed launch result cannot contain output")
		}
	default:
		return errors.New("backgroundtask/tool: unknown stream event type")
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
	return "", errors.New("backgroundtask/tool: runner session is required")
}

var (
	_ componenttool.InvokableTool  = (*managedTool)(nil)
	_ componenttool.StreamableTool = (*managedTool)(nil)
)
