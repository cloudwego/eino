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

	"github.com/bytedance/sonic"
	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
)

const (
	agentToolName        = "agent"
	TaskTypeSubagent     = "subagent"
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
	format    AgentEventFormat[M]
}

func newManagedAgentTool[M adk.MessageType](
	manager *backgroundtask.Manager,
	subAgents map[string]tool.InvokableTool,
	output agentOutput[M],
	name, desc string,
) (tool.BaseTool, error) {
	format := output.format
	formatHint := ""
	if format == nil {
		format = defaultAgentEventFormat[M]
		formatHint = outputFileFormatHint
	}
	return utils.InferOptionableTool(name, desc,
		func(ctx context.Context, in agentManagedInput, opts ...tool.Option) (string, error) {
			agent, params, err := resolveSubAgent(subAgents, in.SubagentType, in.Prompt, in.Description)
			if err != nil {
				return "", err
			}
			outputFile := reserveAgentOutput(ctx, output.store, output.outputDir)
			payload, err := sonic.Marshal(&subagentPayloadV1{Version: 1, SubAgentName: in.SubagentType})
			if err != nil {
				return "", err
			}
			result, err := manager.Run(ctx, &backgroundtask.RunInput{
				Description: in.Description, Type: TaskTypeSubagent, Payload: payload,
				OutputFile: outputFile, RunInBackground: in.RunInBackground,
			}, func(workCtx context.Context, runtime backgroundtask.ExecutionRuntime) (string, error) {
				var outputReceiver agenttool.EventReceiver[*adk.TypedAgentEvent[M]]
				var fileReceiver *agentEventFileReceiver[M]
				var outputWriter io.WriteCloser
				if outputFile != "" {
					writer, openErr := output.store.OpenAppend(
						workCtx, &filesystem.OpenAppendRequest{FilePath: outputFile},
					)
					if openErr != nil {
						if reportErr := runtime.ReportOutputFailure(workCtx, openErr.Error()); reportErr != nil {
							return "", reportErr
						}
					} else {
						outputWriter = writer
						fileReceiver = &agentEventFileReceiver[M]{
							ctx: workCtx, writer: writer, format: format,
							onError: func(fileErr error) error {
								return runtime.ReportOutputFailure(workCtx, fileErr.Error())
							},
						}
						outputReceiver = fileReceiver.receive
					}
				}
				runOpts := append(opts, agenttool.WithEventReceiverTransform(
					managedEventReceiverTransform(runtime.Backgrounded(), outputReceiver),
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
				if fileReceiver != nil && fileReceiver.reportErr != nil {
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
	case backgroundtask.StatusSuspended, backgroundtask.StatusCanceling:
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

type agentEventFileReceiver[M adk.MessageType] struct {
	ctx       context.Context
	writer    io.Writer
	format    AgentEventFormat[M]
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
	line, err := r.format(r.ctx, event)
	if err != nil {
		r.fail(fmt.Errorf("encode agent output event: %w", err))
		return
	}
	if line == "" {
		return
	}
	data := line + "\n"
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

func defaultAgentEventFormat[M adk.MessageType](
	_ context.Context,
	event *adk.TypedAgentEvent[M],
) (string, error) {
	if event == nil || event.Output == nil || event.Output.MessageOutput == nil {
		return "", nil
	}
	message, err := event.Output.MessageOutput.GetMessage()
	if err != nil {
		return "", fmt.Errorf("materialize agent output message: %w", err)
	}
	data, err := sonic.Marshal(&agentEventRecord{
		AgentName: event.AgentName,
		Message:   sanitizedMessageValue(message),
	})
	if err != nil {
		return "", fmt.Errorf("marshal agent output event: %w", err)
	}
	return string(data), nil
}

func sanitizedMessageValue[M adk.MessageType](message M) any {
	switch typed := any(message).(type) {
	case *adk.Message:
		if typed == nil {
			return nil
		}
		cloned := *typed
		cloned.Extra = nil
		return &cloned
	case *adk.AgenticMessage:
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
	if task == nil || task.Spec.Kind != TaskTypeSubagent {
		return ""
	}
	var payload subagentPayloadV1
	if err := sonic.Unmarshal(task.Spec.Payload, &payload); err != nil ||
		payload.Version != 1 || payload.SubAgentName == "" {
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
	format := config.EventFormat
	formatHint := ""
	if format == nil {
		format = defaultAgentEventFormat[M]
		formatHint = outputFileFormatHint
	}
	executor := &durablesubagent.Executor[M]{}
	for _, agent := range agents {
		resumable, ok := agent.(adk.TypedResumableAgent[M])
		if !ok {
			return nil, fmt.Errorf("subagent: agent %q is not resumable", agent.Name(ctx))
		}
		if err := executor.Register(agent.Name(ctx), &durablesubagent.AgentRegistration[M]{
			Agent: resumable, OutputStore: config.OutputStore,
			EventFormat: durablesubagent.EventFormat[M](format),
		}); err != nil {
			return nil, err
		}
	}
	registeredExecutor := executor
	if existing, ok := config.Manager.Executors().Resolve(durablesubagent.ExecutorKey); ok {
		typed, typeOK := existing.(*durablesubagent.Executor[M])
		if !typeOK {
			return nil, errors.New("subagent: registered durable executor has incompatible message type")
		}
		registeredExecutor = typed
		for _, agent := range agents {
			if err := typed.Register(agent.Name(ctx), &durablesubagent.AgentRegistration[M]{
				Agent: agent.(adk.TypedResumableAgent[M]), OutputStore: config.OutputStore,
				EventFormat: durablesubagent.EventFormat[M](format),
			}); err != nil {
				return nil, err
			}
		}
	} else if err := config.Manager.Executors().Register(executor); err != nil {
		return nil, err
	}

	return utils.InferOptionableTool(name, desc, func(
		callCtx context.Context,
		in agentManagedInput,
		opts ...tool.Option,
	) (string, error) {
		environment, ok := adk.TypedRunnerEnvironmentFromContext[M](callCtx)
		if !ok || environment.SessionID() == "" ||
			environment.SessionStore() == nil || environment.CheckPointStore() == nil {
			return "", durablesubagent.ErrRunnerEnvironmentRequired
		}
		prompt := in.Prompt
		if prompt == "" {
			prompt = in.Description
		}
		taskID, err := config.Manager.AllocateTaskID(
			callCtx, &backgroundtask.AllocateTaskIDRequest{Kind: TaskTypeSubagent},
		)
		if err != nil {
			return "", err
		}
		if !in.RunInBackground {
			receivers, enableStreaming, runOptions := agenttool.ResolveInvocationOptions[
				*adk.TypedAgentEvent[M],
				adk.AgentRunOption,
			](in.SubagentType, opts...)
			if err = registeredExecutor.RegisterObserver(
				taskID, receivers, runOptions, enableStreaming,
			); err != nil {
				return "", err
			}
			defer registeredExecutor.DeactivateObserver(taskID)
		}
		outputFile := reserveAgentOutput(callCtx, config.OutputStore, config.OutputDir)
		task, err := durablesubagent.Submit(callCtx, config.Manager, &durablesubagent.SubmitRequest{
			TaskID: taskID, SubAgentName: in.SubagentType, Prompt: prompt, Description: in.Description,
			SessionID: environment.SessionID(), OutputFile: outputFile,
		})
		if err != nil {
			return "", err
		}
		task, err = config.Manager.RunSubmitted(callCtx, &backgroundtask.RunSubmittedRequest{
			TaskID: task.Spec.ID, RunInBackground: in.RunInBackground,
		})
		if err != nil {
			return "", err
		}
		return formatManagedAgentResult(in.SubagentType, task, formatHint)
	})
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
