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

package subagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	backgroundlocal "github.com/cloudwego/eino/adk/backgroundtask/local"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	"github.com/cloudwego/eino/adk/internal/foreground"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	agentToolName        = "agent"
	TaskKindSubagent     = "subagent"
	outputFileFormatHint = `JSONL — one JSON object per line, each a materialized event {agent_name, message}.`
)

type subagentPayloadV1 struct {
	Version      int    `json:"version"`
	SubAgentName string `json:"subagent_name"`
}

type agentInput struct {
	SubagentType string `json:"subagent_type" jsonschema:"required" jsonschema_description:"The type of specialized agent to use for this task"`
	Prompt       string `json:"prompt" jsonschema:"required" jsonschema_description:"The task for the agent to perform"`
	Description  string `json:"description" jsonschema:"required" jsonschema_description:"A short (3-5 word) description of the task"`
}

type agentManagedInput struct {
	agentInput
	RunInBackground bool `json:"run_in_background,omitempty" jsonschema_description:"Set to true to run this agent in the background. You will be notified when it completes."`
}

type agentDurableInput struct {
	agentManagedInput
	ChildSessionID string `json:"child_session_id,omitempty" jsonschema_description:"Continue a previous child session by ID and inherit its history. Omit to create a new child session."`
}

func newAgentTool(subAgents map[string]tool.InvokableTool, name, desc string) (tool.BaseTool, error) {
	return utils.InferOptionableTool(name, desc,
		func(ctx context.Context, in agentInput, opts ...tool.Option) (string, error) {
			agent, params, err := resolveSubAgent(subAgents, in.SubagentType, in.Prompt, in.Description)
			if err != nil {
				return "", err
			}
			return agent.InvokableRun(ctx, params, opts...)
		})
}

type agentOutput[M adk.MessageType] struct {
	store     filesystem.AppendOpener
	outputDir string
	format    TranscriptFormat[M]
}

func newManagedAgentTool[M adk.MessageType](
	runner *backgroundlocal.Runner,
	subAgents map[string]tool.InvokableTool,
	output agentOutput[M],
	name, desc string,
) (tool.BaseTool, error) {
	format := output.format
	formatHint := ""
	if format == nil {
		format = defaultTranscriptFormat[M]
		formatHint = outputFileFormatHint
	}
	return utils.InferOptionableTool(name, desc,
		func(ctx context.Context, in agentManagedInput, opts ...tool.Option) (string, error) {
			agent, params, err := resolveSubAgent(subAgents, in.SubagentType, in.Prompt, in.Description)
			if err != nil {
				return "", err
			}
			sessionID, ok := adk.RunnerSessionID(ctx)
			if !ok {
				return "", errors.New("subagent: runner session is required for background notification")
			}
			outputFile := reserveAgentOutput(ctx, output.store, output.outputDir)
			payload, err := sonic.Marshal(&subagentPayloadV1{Version: 1, SubAgentName: in.SubagentType})
			if err != nil {
				return "", err
			}
			result, err := runner.Run(ctx, &backgroundlocal.Input{
				Description: in.Description, Kind: TaskKindSubagent, Payload: payload,
				OutputFile: outputFile, RunInBackground: in.RunInBackground,
				SessionID: sessionID, NotifySession: true,
			}, func(workCtx context.Context, runtime backgroundtask.ExecutionRuntime) (string, error) {
				fileReceiver := &agentEventFileReceiver[M]{
					ctx: workCtx, format: format,
					onRecord: func(data []byte) error {
						_, appendErr := runtime.EmitProgress(workCtx, "", data)
						return appendErr
					},
					onError: func(fileErr error) error {
						return runtime.ReportTranscriptFailure(workCtx, fileErr)
					},
				}
				var outputReceiver agenttool.EventReceiver[*adk.TypedAgentEvent[M]] = fileReceiver.receive
				var outputWriter io.WriteCloser
				if outputFile != "" {
					writer, openErr := output.store.OpenAppend(
						workCtx, &filesystem.OpenAppendRequest{FilePath: outputFile},
					)
					if openErr != nil {
						if reportErr := runtime.ReportTranscriptFailure(workCtx, openErr); reportErr != nil {
							return "", reportErr
						}
					} else {
						outputWriter = writer
						fileReceiver.writer = writer
					}
				}
				runOpts := append(opts, agenttool.WithEventReceiverTransform(
					managedEventReceiverTransform(
						foreground.ProjectionDetached(workCtx), outputReceiver,
					),
				))
				out, runErr := agent.InvokableRun(workCtx, params, runOpts...)
				if outputWriter != nil {
					if closeErr := outputWriter.Close(); closeErr != nil {
						fileReceiver.fail(fmt.Errorf("close agent output file: %w", closeErr))
					}
				}
				if runErr != nil {
					return "", runErr
				}
				if fileReceiver.reportErr != nil {
					return "", fileReceiver.reportErr
				}
				return out, nil
			})
			if err != nil {
				return "", err
			}
			return formatManagedAgentResult(in.SubagentType, result, formatHint)
		})
}

func formatManagedAgentResult(agentType string, task *backgroundtask.Task, formatHint string) (string, error) {
	switch task.Status {
	case backgroundtask.StatusCompleted:
		return string(task.ResultData), nil
	case backgroundtask.StatusPending, backgroundtask.StatusRunning:
		message := fmt.Sprintf("Agent running in background with ID: %s.", task.Spec.ID)
		if task.Spec.OutputFile != "" {
			message += fmt.Sprintf(" Output is being written to: %s.", task.Spec.OutputFile)
		}
		message += " You will be notified when it completes."
		if task.Spec.OutputFile != "" {
			message += " To check interim output, use Read on that file path"
			if formatHint != "" {
				message += fmt.Sprintf(" (%s)", formatHint)
			}
			message += "."
		}
		return message, nil
	case backgroundtask.StatusWaitingInput:
		return fmt.Sprintf("Agent task %s requires input. Use task_output to inspect the request.", task.Spec.ID), nil
	case backgroundtask.StatusSuspended:
		return fmt.Sprintf("Agent task %s is %s.", task.Spec.ID, task.Status), nil
	case backgroundtask.StatusCanceled:
		return "", fmt.Errorf(
			"subagent %q task %q (%s) was canceled",
			agentType, task.Spec.ID, task.Spec.Description,
		)
	case backgroundtask.StatusFailed:
		return "", fmt.Errorf(
			"subagent %q task %q (%s) failed: %s",
			agentType, task.Spec.ID, task.Spec.Description, task.ResultError,
		)
	default:
		return "", fmt.Errorf("subagent %q task %q has unknown status %q", agentType, task.Spec.ID, task.Status)
	}
}

type durableAgentToolResult struct {
	TaskID         string                `json:"task_id"`
	ChildSessionID string                `json:"child_session_id"`
	Status         backgroundtask.Status `json:"status"`
	Result         string                `json:"result,omitempty"`
	OutputFile     string                `json:"output_file,omitempty"`
	Error          string                `json:"error,omitempty"`
}

func formatDurableAgentResult(
	agentType string,
	task *backgroundtask.Task,
) (string, error) {
	if task == nil {
		return "", errors.New("subagent: durable task result is required")
	}
	childSessionID, err := durablesubagent.ChildSessionIDFromTask(task)
	if err != nil {
		return "", err
	}
	switch task.Status {
	case backgroundtask.StatusCompleted,
		backgroundtask.StatusPending,
		backgroundtask.StatusRunning,
		backgroundtask.StatusWaitingInput,
		backgroundtask.StatusSuspended,
		backgroundtask.StatusCanceled,
		backgroundtask.StatusFailed:
		result := &durableAgentToolResult{
			TaskID: task.Spec.ID, ChildSessionID: childSessionID,
			Status: task.Status, OutputFile: task.Spec.OutputFile,
		}
		if task.Status == backgroundtask.StatusCompleted {
			result.Result = string(task.ResultData)
		}
		if task.Status == backgroundtask.StatusCanceled ||
			task.Status == backgroundtask.StatusFailed {
			result.Error = task.ResultError
		}
		data, marshalErr := sonic.MarshalString(result)
		if marshalErr != nil {
			return "", marshalErr
		}
		return data, nil
	default:
		return "", fmt.Errorf(
			"subagent %q task %q has unknown status %q",
			agentType, task.Spec.ID, task.Status,
		)
	}
}

func managedEventReceiverTransform[E any](
	backgrounded <-chan struct{},
	taskReceiver agenttool.EventReceiver[E],
) agenttool.EventReceiverTransform[E] {
	return func(current []agenttool.EventReceiver[E]) []agenttool.EventReceiver[E] {
		for i := range current {
			receiver := current[i]
			current[i] = func(event E) {
				if !signalClosed(backgrounded) {
					receiver(event)
				}
			}
		}
		if taskReceiver != nil {
			current = append(current, taskReceiver)
		}
		return current
	}
}

func signalClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

type detachedExecutionContext struct {
	parent context.Context
}

func (detachedExecutionContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (detachedExecutionContext) Done() <-chan struct{}       { return nil }
func (detachedExecutionContext) Err() error                  { return nil }
func (c detachedExecutionContext) Value(key any) any         { return c.parent.Value(key) }

type agentEventFileReceiver[M adk.MessageType] struct {
	ctx       context.Context
	writer    io.Writer
	format    TranscriptFormat[M]
	onRecord  func([]byte) error
	onError   func(error) error
	failed    bool
	reportErr error
}

type agentEventRecord struct {
	AgentName string `json:"agent_name,omitempty"`
	Message   any    `json:"message"`
}

func (r *agentEventFileReceiver[M]) receive(event *adk.TypedAgentEvent[M]) {
	if r.failed {
		return
	}
	if event == nil || event.Output == nil || event.Output.MessageOutput == nil {
		return
	}
	message, err := event.Output.MessageOutput.GetMessage()
	if err != nil {
		r.fail(fmt.Errorf("materialize agent output message: %w", err))
		return
	}
	line, err := r.format(r.ctx, event.AgentName, message)
	if err != nil {
		r.fail(fmt.Errorf("encode agent output event: %w", err))
		return
	}
	if line == "" {
		return
	}
	data := line + "\n"
	if r.onRecord != nil {
		if err = r.onRecord([]byte(data)); err != nil {
			r.failed = true
			r.reportErr = err
			return
		}
	}
	if r.writer == nil {
		return
	}
	n, err := io.WriteString(r.writer, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		r.fail(fmt.Errorf("write agent output: %w", err))
	}
}

func (r *agentEventFileReceiver[M]) fail(err error) {
	if err == nil || r.failed {
		return
	}
	r.failed = true
	if r.onError != nil {
		r.reportErr = r.onError(err)
	}
}

func defaultTranscriptFormat[M adk.MessageType](
	_ context.Context,
	agentName string,
	message M,
) (string, error) {
	data, err := sonic.Marshal(&agentEventRecord{
		AgentName: agentName,
		Message:   sanitizedMessageValue(message),
	})
	if err != nil {
		return "", fmt.Errorf("marshal agent output event: %w", err)
	}
	return string(data), nil
}

func sanitizedMessageValue[M adk.MessageType](message M) any {
	switch typed := any(message).(type) {
	case adk.Message:
		if typed == nil {
			return nil
		}
		cloned := *typed
		cloned.Extra = nil
		return &cloned
	case adk.AgenticMessage:
		if typed == nil {
			return nil
		}
		cloned := *typed
		cloned.Extra = nil
		return &cloned
	default:
		return message
	}
}

func reserveAgentOutput(
	ctx context.Context,
	store filesystem.AppendOpener,
	outputDir string,
) string {
	if store == nil || outputDir == "" {
		return ""
	}
	name := compose.GetToolCallID(ctx)
	if name == "" {
		name = uuid.NewString()
	}
	path := filepath.Join(outputDir, name+".output")
	writer, err := store.OpenAppend(ctx, &filesystem.OpenAppendRequest{FilePath: path})
	if err != nil {
		return ""
	}
	if err = writer.Close(); err != nil {
		return ""
	}
	return path
}

// NameFromTask returns the persisted sub-agent routing name.
func NameFromTask(task *backgroundtask.Task) string {
	if task == nil || task.Spec.Kind != TaskKindSubagent {
		return ""
	}
	var payload subagentPayloadV1
	if err := sonic.Unmarshal(task.Spec.Payload, &payload); err != nil ||
		payload.Version <= 0 || payload.SubAgentName == "" {
		return ""
	}
	return payload.SubAgentName
}

func newDurableAgentTool[M adk.MessageType](
	ctx context.Context,
	config *TypedDurableBackgroundConfig[M],
	agents []adk.TypedAgent[M],
	name, desc string,
) (tool.BaseTool, error) {
	if config.ShouldAutoBackground != nil {
		return nil, errors.New(
			"subagent: durable auto-background requires checkpoint handoff support",
		)
	}
	executor := config.Executor
	foregroundTools := make(map[string]tool.InvokableTool, len(agents))
	for _, agent := range agents {
		resumable, ok := agent.(adk.TypedResumableAgent[M])
		if !ok {
			return nil, fmt.Errorf("subagent: agent %q is not resumable", agent.Name(ctx))
		}
		agentName := agent.Name(ctx)
		if err := executor.Register(agentName, &durablesubagent.AgentRegistration[M]{
			Agent:             resumable,
			RunOptionsFactory: config.RunOptionsFactories[agentName],
		}); err != nil {
			return nil, err
		}
		foregroundTool, ok := adk.NewTypedAgentTool(ctx, agent).(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("subagent: agent %q foreground tool is not invokable", agentName)
		}
		foregroundTools[agentName] = foregroundTool
	}
	registered, _, err := config.Executors.LoadOrRegister(executor)
	if err != nil {
		return nil, err
	}
	typed, typeOK := registered.(*durablesubagent.Executor[M])
	if !typeOK || typed != executor {
		return nil, errors.New(
			"subagent: Manager is already bound to a different durable Executor",
		)
	}

	return utils.InferOptionableTool(name, desc, func(
		callCtx context.Context,
		in agentDurableInput,
		opts ...tool.Option,
	) (string, error) {
		prompt := in.Prompt
		if prompt == "" {
			prompt = in.Description
		}
		_, _, invocationRunOptions := agenttool.ResolveInvocationOptions[
			*adk.TypedAgentEvent[M],
			adk.AgentRunOption,
		](in.SubagentType, opts...)
		if !in.RunInBackground && config.ShouldAutoBackground == nil {
			agent, params, err := resolveSubAgent(
				foregroundTools,
				in.SubagentType,
				prompt,
				in.Description,
			)
			if err != nil {
				return "", err
			}
			return agent.InvokableRun(callCtx, params, opts...)
		}
		sessionID, ok := adk.RunnerSessionID(callCtx)
		if !ok {
			return "", errors.New(
				"subagent: runner session is required for background notification",
			)
		}
		if len(invocationRunOptions) > 0 {
			return "", errors.New(
				"subagent: durable execution does not support invocation-scoped run options; " +
					"configure RunOptionsFactories",
			)
		}
		task, err := durablesubagent.Submit(callCtx, config.Manager, &durablesubagent.SubmitRequest[M]{
			SubAgentName: in.SubagentType, Input: newTypedUserInput[M](prompt), Description: in.Description,
			SessionID: sessionID, ChildSessionID: in.ChildSessionID,
		})
		if err != nil && !(errors.Is(err, backgroundtask.ErrTaskCreatedEventUndelivered) && task != nil) {
			return "", err
		}
		go func() {
			_ = config.Manager.Execute(detachedExecutionContext{parent: callCtx}, task.Spec.ID)
		}()
		return formatDurableAgentResult(in.SubagentType, task)
	})
}

func newTypedUserInput[M adk.MessageType](query string) *adk.TypedAgentInput[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return &adk.TypedAgentInput[M]{
			Messages: []M{any(schema.UserMessage(query)).(M)},
		}
	case *schema.AgenticMessage:
		return &adk.TypedAgentInput[M]{
			Messages: []M{any(schema.UserAgenticMessage(query)).(M)},
		}
	default:
		panic("unreachable: unsupported message type")
	}
}

func resolveSubAgent(subAgents map[string]tool.InvokableTool, subagentType, prompt, description string) (tool.InvokableTool, string, error) {
	agent, ok := subAgents[subagentType]
	if !ok {
		return nil, "", fmt.Errorf("subagent type %q not found", subagentType)
	}
	if prompt == "" {
		prompt = description
	}
	params, err := sonic.MarshalString(map[string]string{"request": prompt})
	if err != nil {
		return nil, "", err
	}
	return agent, params, nil
}

// defaultAgentToolDescription returns the agent tool description. Available
// agent types are injected as a mid-conversation system message.
func defaultAgentToolDescription[M adk.MessageType](context.Context, []adk.TypedAgent[M]) (string, error) {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: agentToolDescription,
		Chinese: agentToolDescriptionChinese,
	}), nil
}
