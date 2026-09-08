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
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	backgroundlocal "github.com/cloudwego/eino/adk/task/local"
	durablesubagent "github.com/cloudwego/eino/adk/task/subagent"
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

func init() {
	schema.RegisterName[runtimeAgentInterruptState](
		"_eino_adk_subagent_runtime_interrupt_state",
	)
}

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

type runtimeAgentInterruptState struct {
	TaskID             string   `json:"task_id"`
	ChildSessionID     string   `json:"child_session_id"`
	InvocationID       string   `json:"invocation_id,omitempty"`
	TargetIDs          []string `json:"target_ids"`
	NextResumeSequence int64    `json:"next_resume_sequence"`
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
	persister background.TaskEventPersister[*adk.TypedAgentEvent[M], M]
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
	}
	persister := output.persister
	if persister == nil {
		persister = agentTaskEventPersister[M]{format: format}
		if output.format == nil {
			formatHint = outputFileFormatHint
		}
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
			}, func(workCtx context.Context, runtime background.ExecutionRuntime) (string, error) {
				fileReceiver := &agentEventPersistenceReceiver[M]{
					ctx: workCtx, format: format,
					runtime: runtime, persister: persister,
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
						backgroundlocal.ProjectionDetached(workCtx), outputReceiver,
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

func formatManagedAgentResult(
	agentType string,
	result *backgroundlocal.RunResult,
	formatHint string,
) (string, error) {
	if outcome, ok := result.Foreground(); ok {
		switch outcome.Status {
		case task.OutcomeCompleted:
			return string(outcome.Data), nil
		case task.OutcomeFailed:
			return "", fmt.Errorf(
				"subagent %q execution %q failed: %s",
				agentType,
				result.ID(),
				outcome.Error,
			)
		case task.OutcomeCanceled:
			return "", fmt.Errorf(
				"subagent %q execution %q was canceled: %s",
				agentType,
				result.ID(),
				outcome.Error,
			)
		default:
			return "", fmt.Errorf(
				"subagent %q execution %q returned unsupported foreground status %d",
				agentType,
				result.ID(),
				outcome.Status,
			)
		}
	}
	backgroundTask, ok := result.Task()
	if !ok {
		return "", errors.New("subagent: invalid local run result")
	}
	return formatManagedAgentTaskResult(agentType, backgroundTask, formatHint)
}

func formatManagedAgentTaskResult(
	agentType string,
	backgroundTask *background.TaskSnapshot,
	formatHint string,
) (string, error) {
	switch backgroundTask.Status {
	case background.StatusCompleted:
		return string(backgroundTask.ResultData), nil
	case background.StatusPending, background.StatusRunning:
		message := fmt.Sprintf("Agent running in background with ID: %s.", backgroundTask.Spec.ID)
		if backgroundTask.Spec.OutputFile != "" {
			message += fmt.Sprintf(" Output is being written to: %s.", backgroundTask.Spec.OutputFile)
		}
		message += " You will be notified when it completes."
		if backgroundTask.Spec.OutputFile != "" {
			message += " To check interim output, use Read on that file path"
			if formatHint != "" {
				message += fmt.Sprintf(" (%s)", formatHint)
			}
			message += "."
		}
		return message, nil
	case background.StatusWaitingInput:
		return fmt.Sprintf(
			"Agent task %s requires input. Use task_output to inspect the request.",
			backgroundTask.Spec.ID,
		), nil
	case background.StatusSuspended:
		return fmt.Sprintf(
			"Agent task %s is %s.",
			backgroundTask.Spec.ID,
			backgroundTask.Status,
		), nil
	case background.StatusCanceled:
		return "", fmt.Errorf(
			"subagent %q task %q (%s) was canceled",
			agentType, backgroundTask.Spec.ID, backgroundTask.Spec.Description,
		)
	case background.StatusFailed:
		return "", fmt.Errorf(
			"subagent %q task %q (%s) failed: %s",
			agentType,
			backgroundTask.Spec.ID,
			backgroundTask.Spec.Description,
			backgroundTask.ResultError,
		)
	default:
		return "", fmt.Errorf(
			"subagent %q task %q has unknown status %q",
			agentType,
			backgroundTask.Spec.ID,
			backgroundTask.Status,
		)
	}
}

type durableAgentToolResult struct {
	TaskID         string            `json:"task_id"`
	ChildSessionID string            `json:"child_session_id"`
	Status         background.Status `json:"status"`
	Result         string            `json:"result,omitempty"`
	OutputFile     string            `json:"output_file,omitempty"`
	Error          string            `json:"error,omitempty"`
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

type agentEventPersistenceReceiver[M adk.MessageType] struct {
	ctx       context.Context
	writer    io.Writer
	format    TranscriptFormat[M]
	runtime   background.ExecutionRuntime
	persister background.TaskEventPersister[*adk.TypedAgentEvent[M], M]
	onError   func(error) error
	failed    bool
	reportErr error
}

type agentEventRecord struct {
	AgentName string `json:"agent_name,omitempty"`
	Message   any    `json:"message"`
}

func (r *agentEventPersistenceReceiver[M]) receive(event *adk.TypedAgentEvent[M]) {
	if r.failed {
		return
	}
	if event == nil {
		return
	}
	if r.runtime == nil || r.persister == nil {
		if event.Output == nil || event.Output.MessageOutput == nil {
			return
		}
		if err := r.writeTranscript(event); err != nil {
			r.fail(err)
		}
		return
	}
	persistEvent, persistStream := splitAgentTaskEvent(event)
	persisted, err := background.PersistTaskEvent[*adk.TypedAgentEvent[M], M](
		r.ctx,
		r.runtime,
		agentTaskEventID(persistEvent),
		&background.TaskEventEnvelope[*adk.TypedAgentEvent[M], M]{
			Event: persistEvent, Stream: persistStream,
		},
		r.persister,
	)
	if persisted != nil {
		for _, part := range persisted.Appends {
			if r.writer == nil || part == nil || part.Part == nil ||
				!part.Inserted {
				continue
			}
			if writeErr := r.writeRecord(part.Part.Data); writeErr != nil {
				r.fail(writeErr)
				return
			}
		}
	}
	if err != nil {
		r.failed = true
		r.reportErr = err
		return
	}
}

func (r *agentEventPersistenceReceiver[M]) writeRecord(data []byte) error {
	n, err := r.writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("write agent output: %w", err)
	}
	return nil
}

func (r *agentEventPersistenceReceiver[M]) writeTranscript(
	event *adk.TypedAgentEvent[M],
) error {
	message, err := event.Output.MessageOutput.GetMessage()
	if err != nil {
		return fmt.Errorf("materialize agent output message: %w", err)
	}
	line, err := r.format(r.ctx, event.AgentName, message)
	if err != nil {
		return fmt.Errorf("encode agent output event: %w", err)
	}
	if line == "" {
		return nil
	}
	if r.writer == nil {
		return nil
	}
	return r.writeRecord([]byte(line + "\n"))
}

func (r *agentEventPersistenceReceiver[M]) fail(err error) {
	if err == nil || r.failed {
		return
	}
	r.failed = true
	if r.onError != nil {
		r.reportErr = r.onError(err)
	}
}

type agentTaskEventPersister[M adk.MessageType] struct {
	format TranscriptFormat[M]
}

func (p agentTaskEventPersister[M]) Persist(
	ctx context.Context,
	_ background.TaskEventScope,
	input *background.TaskEventEnvelope[*adk.TypedAgentEvent[M], M],
	writer background.TaskEventWriter,
) error {
	if input.Event == nil || input.Event.Output == nil ||
		input.Event.Output.MessageOutput == nil {
		return nil
	}
	event := withAgentTaskEventStream(input.Event, input.Stream)
	message, err := event.Output.MessageOutput.GetMessage()
	if err != nil {
		return fmt.Errorf("materialize agent output message: %w", err)
	}
	line, err := p.format(ctx, event.AgentName, message)
	if err != nil {
		return fmt.Errorf("encode agent output event: %w", err)
	}
	if line == "" {
		return nil
	}
	_, err = writer.Append(ctx, &background.TaskEventPartInput{
		PartID: "event", Data: []byte(line + "\n"), Final: true,
	})
	return err
}

func splitAgentTaskEvent[M adk.MessageType](
	event *adk.TypedAgentEvent[M],
) (
	persistEvent *adk.TypedAgentEvent[M],
	persistStream *schema.StreamReader[M],
) {
	if event.Output == nil || event.Output.MessageOutput == nil {
		return event, nil
	}
	persistEvent = cloneAgentTaskEvent(event, nil)
	message := event.Output.MessageOutput
	if !message.IsStreaming || message.MessageStream == nil {
		return persistEvent, nil
	}
	return persistEvent, message.MessageStream
}

func cloneAgentTaskEvent[M adk.MessageType](
	event *adk.TypedAgentEvent[M],
	stream *schema.StreamReader[M],
) *adk.TypedAgentEvent[M] {
	cloned := *event
	cloned.RunPath = append([]adk.RunStep(nil), event.RunPath...)
	output := *event.Output
	message := *event.Output.MessageOutput
	message.MessageStream = stream
	output.MessageOutput = &message
	cloned.Output = &output
	return &cloned
}

func withAgentTaskEventStream[M adk.MessageType](
	event *adk.TypedAgentEvent[M],
	stream *schema.StreamReader[M],
) *adk.TypedAgentEvent[M] {
	if stream == nil {
		return event
	}
	return cloneAgentTaskEvent(event, stream)
}

func agentTaskEventID[M adk.MessageType](
	event *adk.TypedAgentEvent[M],
) string {
	if event == nil || event.SessionEventVariant == nil {
		return ""
	}
	if event.SessionEventVariant.Event != nil {
		return event.SessionEventVariant.Event.EventID
	}
	if event.SessionEventVariant.MessageStreamRef != nil {
		return event.SessionEventVariant.MessageStreamRef.EventID
	}
	return ""
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
func NameFromTask(task *background.TaskSnapshot) string {
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
	config *TypedDurableTaskConfig[M],
	agents []adk.TypedAgent[M],
	name, desc string,
) (tool.BaseTool, error) {
	if config.Runtime == nil {
		return nil, errors.New("subagent: durable Controller is required")
	}
	for _, agent := range agents {
		resumable, ok := agent.(adk.TypedResumableAgent[M])
		if !ok {
			return nil, fmt.Errorf("subagent: agent %q is not resumable", agent.Name(ctx))
		}
		agentName := agent.Name(ctx)
		if err := config.Runtime.RegisterAgent(agentName, &durablesubagent.AgentRegistration[M]{
			Agent:             resumable,
			RunOptionsFactory: config.RunOptionsFactories[agentName],
		}); err != nil {
			return nil, err
		}
	}
	return newControllerAgentTool[M](
		ctx, config.Runtime, agents, name, desc,
	)
}

func newControllerAgentTool[M adk.MessageType](
	ctx context.Context,
	runtime *durablesubagent.Controller[M],
	agents []adk.TypedAgent[M],
	name, desc string,
) (tool.BaseTool, error) {
	available := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		available[agent.Name(ctx)] = struct{}{}
	}
	return utils.InferOptionableTool(name, desc, func(
		callCtx context.Context,
		in agentDurableInput,
		opts ...tool.Option,
	) (string, error) {
		if _, ok := available[in.SubagentType]; !ok {
			return "", fmt.Errorf("subagent type %q not found", in.SubagentType)
		}
		parentSessionID, ok := adk.RunnerSessionID(callCtx)
		if !ok {
			return "", errors.New(
				"subagent: runner session is required for durable runtime",
			)
		}
		toolCallID := compose.GetToolCallID(callCtx)
		if toolCallID == "" {
			// Direct programmatic tool calls do not have graph tool-call
			// identity and therefore cannot be replayed by the parent Runner.
			toolCallID = uuid.NewString()
		}
		invocationID := parentSessionID + ":" + toolCallID
		receivers, enableStreaming, runOptions := agenttool.ResolveInvocationOptions[
			*adk.TypedAgentEvent[M],
			adk.AgentRunOption,
		](in.SubagentType, opts...)
		if len(runOptions) > 0 {
			return "", errors.New(
				"subagent: runtime execution does not support invocation-scoped run options; " +
					"configure RunOptionsFactories",
			)
		}
		prompt := in.Prompt
		if prompt == "" {
			prompt = in.Description
		}
		startMode := task.StartModeForeground
		if in.RunInBackground {
			startMode = task.StartModeBackground
		}
		onEvent := func(event *adk.TypedAgentEvent[M]) {
			for _, receiver := range receivers {
				receiver(event)
			}
		}
		wasInterrupted, hasState, interruptState :=
			tool.GetInterruptState[runtimeAgentInterruptState](callCtx)
		nextResumeSequence := int64(1)
		var (
			handle *durablesubagent.Handle
			err    error
		)
		if wasInterrupted {
			if !hasState || interruptState.TaskID == "" ||
				interruptState.ChildSessionID == "" ||
				len(interruptState.TargetIDs) == 0 {
				return "", errors.New(
					"subagent: runtime interrupt state is unavailable",
				)
			}
			isTarget, hasData, resumeData := tool.GetResumeContext[any](callCtx)
			if !isTarget {
				return "", errors.New("subagent: runtime resume target is unavailable")
			}
			targets := make(map[string]any, len(interruptState.TargetIDs))
			for _, targetID := range interruptState.TargetIDs {
				if hasData {
					targets[targetID] = resumeData
				} else {
					targets[targetID] = nil
				}
			}
			data, marshalErr := sonic.Marshal(targets)
			if marshalErr != nil {
				return "", marshalErr
			}
			resumeSequence := interruptState.NextResumeSequence
			if resumeSequence <= 0 {
				resumeSequence = 1
			}
			eventID := "resume:" + uuid.NewSHA1(
				uuid.Nil,
				[]byte(fmt.Sprintf(
					"%s:%d", interruptState.TaskID, resumeSequence,
				)),
			).String()
			if sendErr := runtime.SendInput(
				callCtx,
				interruptState.TaskID,
				&task.Input{
					EventID: eventID, Kind: durablesubagent.ResumeInputKind,
					Data: data,
				},
			); sendErr != nil {
				return "", sendErr
			}
			in.ChildSessionID = interruptState.ChildSessionID
			if interruptState.InvocationID != "" {
				invocationID = interruptState.InvocationID
			}
			handle, err = runtime.Start(callCtx, &durablesubagent.StartRequest[M]{
				InvocationID:    invocationID,
				ParentSessionID: parentSessionID,
				ChildSessionID:  in.ChildSessionID,
				AgentName:       in.SubagentType,
				Description:     in.Description,
				Input:           newTypedUserInput[M](prompt, enableStreaming),
				StartMode:       startMode,
				OnEvent:         onEvent,
			})
			if err != nil {
				return "", err
			}
			nextResumeSequence = resumeSequence + 1
		}
		if !wasInterrupted {
			if in.ChildSessionID != "" {
				handle, err = runtime.Continue(
					callCtx,
					&durablesubagent.ContinueRequest[M]{
						ChildSessionID: in.ChildSessionID,
						InvocationID:   invocationID,
						Input:          newTypedUserInput[M](prompt, enableStreaming),
						IfIdle: &durablesubagent.StartOptions[M]{
							ParentSessionID: parentSessionID,
							AgentName:       in.SubagentType,
							Description:     in.Description,
							StartMode:       startMode,
							OnEvent:         onEvent,
						},
					},
				)
			} else {
				handle, err = runtime.Start(callCtx, &durablesubagent.StartRequest[M]{
					InvocationID:    invocationID,
					ParentSessionID: parentSessionID, ChildSessionID: in.ChildSessionID,
					AgentName: in.SubagentType, Description: in.Description,
					Input:     newTypedUserInput[M](prompt, enableStreaming),
					StartMode: startMode, OnEvent: onEvent,
				})
			}
		}
		if err != nil {
			return "", err
		}
		if in.RunInBackground {
			return formatRuntimeHandle(handle, background.StatusPending)
		}
		result, err := runtime.Wait(callCtx, handle.ID())
		if err != nil {
			return "", err
		}
		if result.Interrupted != nil {
			state := runtimeAgentInterruptState{
				TaskID: handle.ID(), ChildSessionID: handle.ChildSessionID(),
				InvocationID: invocationID, NextResumeSequence: nextResumeSequence,
			}
			for _, interruptContext := range result.Interrupted.InterruptContexts {
				if interruptContext.ID != "" {
					state.TargetIDs = append(state.TargetIDs, interruptContext.ID)
				}
			}
			return "", tool.StatefulInterrupt(callCtx, result.Interrupted, state)
		}
		content := agenttool.ExtractTextContent(result.FinalMessage)
		return sonic.MarshalString(&durableAgentToolResult{
			TaskID: handle.ID(), ChildSessionID: handle.ChildSessionID(),
			Status: background.StatusCompleted, Result: content,
		})
	})
}

func formatRuntimeHandle(
	handle *durablesubagent.Handle,
	status background.Status,
) (string, error) {
	if handle == nil || handle.ID() == "" || handle.ChildSessionID() == "" {
		return "", errors.New("subagent: runtime returned an invalid handle")
	}
	data, err := sonic.MarshalString(&durableAgentToolResult{
		TaskID: handle.ID(), ChildSessionID: handle.ChildSessionID(), Status: status,
	})
	if err != nil {
		return "", err
	}
	return data, nil
}

func newTypedUserInput[M adk.MessageType](
	query string,
	enableStreaming bool,
) *adk.TypedAgentInput[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return &adk.TypedAgentInput[M]{
			Messages:        []M{any(schema.UserMessage(query)).(M)},
			EnableStreaming: enableStreaming,
		}
	case *schema.AgenticMessage:
		return &adk.TypedAgentInput[M]{
			Messages:        []M{any(schema.UserAgenticMessage(query)).(M)},
			EnableStreaming: enableStreaming,
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
