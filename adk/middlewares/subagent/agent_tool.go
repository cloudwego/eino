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
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
)

const (
	agentToolName = "agent"
	// TaskTypeSubagent is the backgroundtask Task.Type tag for sub-agent tasks
	// launched by the agent tool, letting a shared Manager distinguish them from
	// shell tasks.
	TaskTypeSubagent = "subagent"

	// MetadataKeySubagentType is the RunInput.Metadata / Task.Metadata key under
	// which the agent tool records the sub-agent type for a task. A
	// ShouldAutoBackground hook reads it (via TypeFromTask) to apply
	// agent-type-specific policy without parsing the human-readable Description. The
	// value is a string.
	MetadataKeySubagentType = "subagent_type"
)

// TypeFromTask returns the sub-agent type recorded in a sub-agent task's
// metadata under MetadataKeySubagentType, or "" if absent (e.g. the task is not a
// sub-agent run). It is the intended way for a ShouldAutoBackground hook to recover
// the agent type.
func TypeFromTask(t *backgroundtask.Task) string {
	if t == nil {
		return ""
	}
	st, _ := t.Metadata[MetadataKeySubagentType].(string)
	return st
}

// agentInput is the agent tool's input when no Manager is configured: spawn a
// sub-agent synchronously in the foreground.
type agentInput struct {
	SubagentType string `json:"subagent_type" jsonschema:"required" jsonschema_description:"The type of specialized agent to use for this task"`
	Prompt       string `json:"prompt" jsonschema:"required" jsonschema_description:"The task for the agent to perform"`
	Description  string `json:"description" jsonschema:"required" jsonschema_description:"A short (3-5 word) description of the task"`
}

// agentManagedInput is the agent tool's input when a Manager is configured: it adds
// run_in_background so the model can spawn the sub-agent in the background.
type agentManagedInput struct {
	agentInput
	RunInBackground bool `json:"run_in_background,omitempty" jsonschema_description:"Set to true to run this agent in the background. You will be notified when it completes."`
}

// newAgentTool builds the foreground-only agent tool (no Manager): it invokes the
// agent-as-tool adapter directly, forwarding opts so event forwarding, session
// sharing and interrupt/resume behave exactly as a normal agent-as-tool call.
func newAgentTool(subAgents map[string]tool.InvokableTool, name, desc string) (tool.BaseTool, error) {
	return utils.InferOptionableTool(name, desc,
		func(ctx context.Context, in agentInput, opts ...tool.Option) (string, error) {
			a, params, err := resolveSubAgent(subAgents, in.SubagentType, in.Prompt, in.Description)
			if err != nil {
				return "", err
			}
			return a.InvokableRun(ctx, params, opts...)
		})
}

// newManagedAgentTool builds the Manager-backed agent tool. It wraps the same
// agent-as-tool invocation in a managed task, so foreground behavior is identical
// and only lifecycle/background switching is layered on top.
//
// agentOutput bundles the output-file configuration for a managed agent tool: the
// AppendOpener to write through, the directory to reserve paths under, and the
// per-event encoder. A zero store or outputDir disables output files. A nil format
// selects the built-in default encoder (see defaultAgentEventFormat).
type agentOutput[M adk.MessageType] struct {
	store     filesystem.AppendOpener
	outputDir string
	format    AgentEventFormat[M]
}

// When sink.store and sink.outputDir are both set, each run is given an output file
// at outputDir/<uuid>.output, and the work callback lazily creates it and keeps one
// append writer open while the sub-agent runs, writing one line per materialized
// AgentEvent through sink.format (or the default encoder when it is nil). The path is
// allocated before the run. The Manager never writes — the tool owns the writer.
// sink.store is a filesystem.AppendOpener; output files require one (no rewrite
// fallback).
func newManagedAgentTool[M adk.MessageType](mgr *backgroundtask.Manager, subAgents map[string]tool.InvokableTool, sink agentOutput[M], name, desc string) (tool.BaseTool, error) {
	format := sink.format
	var formatHint string // set only for the default encoder, whose format we can describe
	if format == nil {
		format, formatHint = defaultAgentEventFormat[M], outputFileFormatHint
	}
	return utils.InferOptionableTool(name, desc,
		func(ctx context.Context, in agentManagedInput, opts ...tool.Option) (string, error) {
			a, params, err := resolveSubAgent(subAgents, in.SubagentType, in.Prompt, in.Description)
			if err != nil {
				return "", err
			}

			outputFile := agentOutputFilePath(ctx, sink.store, sink.outputDir)

			result, err := mgr.Run(ctx, &backgroundtask.RunInput{
				Description:     in.Description,
				Type:            TaskTypeSubagent,
				ToolUseID:       compose.GetToolCallID(ctx),
				RunInBackground: in.RunInBackground,
				Metadata:        map[string]any{MetadataKeySubagentType: in.SubagentType},
				OutputFile:      outputFile,
			}, func(workCtx context.Context, task backgroundtask.TaskInfo) (string, error) {
				var outputReceiver agenttool.EventReceiver[*adk.TypedAgentEvent[M]]
				if outputFile != "" {
					writer, openErr := sink.store.OpenAppend(workCtx, &filesystem.OpenAppendRequest{FilePath: outputFile})
					if openErr != nil {
						mgr.MarkOutputFileUnreliable(task.ID, openErr.Error())
					} else {
						fileReceiver := &agentEventFileReceiver[M]{
							ctx:    workCtx,
							writer: writer,
							format: format,
							onError: func(err error) {
								mgr.MarkOutputFileUnreliable(task.ID, err.Error())
							},
						}
						outputReceiver = fileReceiver.receive
						defer func() {
							if closeErr := writer.Close(); closeErr != nil {
								fileReceiver.fail(fmt.Errorf("close agent output file: %w", closeErr))
							}
						}()
					}
				}

				// Existing receivers came from the launching parent agent. Gate only
				// those receivers when the task is backgrounded, then append the task's
				// output-file receiver (nil when output files are off) after the gate so
				// it continues receiving events.
				runOpts := append(opts, agenttool.WithEventReceiverTransform(
					managedEventReceiverTransform(task.Backgrounded, outputReceiver)))
				out, runErr := a.InvokableRun(workCtx, params, runOpts...)
				if runErr != nil {
					return "", runErr
				}
				return out, nil
			})
			if err != nil {
				return "", err
			}

			switch result.Status {
			case backgroundtask.StatusCompleted:
				return result.Result, nil
			case backgroundtask.StatusRunning:
				msg := fmt.Sprintf("Agent running in background with ID: %s.", result.ID)
				if result.OutputFile != "" {
					msg += fmt.Sprintf(" Output is being written to: %s.", result.OutputFile)
				}
				msg += " You will be notified when it completes."
				if result.OutputFile != "" {
					if formatHint != "" {
						msg += fmt.Sprintf(" To check interim output, use Read on that file path (%s).", formatHint)
					} else {
						msg += " To check interim output, use Read on that file path."
					}
				}
				return msg, nil
			case backgroundtask.StatusFailed:
				return "", fmt.Errorf("subagent %q task %q (%s) failed: %s",
					in.SubagentType, result.ID, in.Description, result.Error)
			case backgroundtask.StatusCanceled:
				return "", fmt.Errorf("subagent %q task %q (%s) was canceled",
					in.SubagentType, result.ID, in.Description)
			default:
				return result.Result, nil
			}
		})
}

// managedEventReceiverTransform gates the parent receivers configured before
// it and then appends the task receiver. An empty current slice is valid: that
// is the EmitInternalEvents=false path, where only task output is needed.
func managedEventReceiverTransform[E any](backgrounded <-chan struct{}, taskReceiver agenttool.EventReceiver[E]) agenttool.EventReceiverTransform[E] {
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
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

type agentEventFileReceiver[M adk.MessageType] struct {
	ctx     context.Context
	writer  io.Writer
	format  AgentEventFormat[M]
	onError func(error)
	failed  bool
}

type agentEventRecord struct {
	AgentName string `json:"agent_name,omitempty"`
	Message   any    `json:"message"`
}

// receive encodes one event through the configured format and appends it as a line.
// An empty line skips the event — nothing is written; a non-nil error marks the file
// unreliable and stops further writes.
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
		return // skip: no line to write for this event
	}

	// Write the line and its newline in one call: a single write halves per-write
	// backend overhead (lock/RPC) and keeps the line and its terminator atomic at the
	// write boundary; the extra newline concat is cheap. io.WriteString uses the
	// backend's StringWriter fast path when available, avoiding a []byte copy. Treat a
	// short write as an error even when the writer reports nil, so a truncated line
	// marks the file unreliable rather than being trusted as authoritative.
	data := line + "\n"
	n, err := io.WriteString(r.writer, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		r.fail(fmt.Errorf("write agent output: %w", err))
	}
}

// defaultAgentEventFormat is the built-in AgentEventFormat: it emits one JSON object
// per message-bearing event — {agent_name, message} with the event's message (root
// Extra stripped) — and skips (returns "") events that carry no materialized message.
// The event kind is read from the message's own role and tool calls, so no separate
// type field is added. It does not use ctx.
func defaultAgentEventFormat[M adk.MessageType](_ context.Context, event *adk.TypedAgentEvent[M]) (string, error) {
	if event == nil || event.Output == nil || event.Output.MessageOutput == nil {
		return "", nil // skip: nothing to record
	}
	msg, err := event.Output.MessageOutput.GetMessage()
	if err != nil {
		return "", fmt.Errorf("materialize agent output message: %w", err)
	}
	data, err := sonic.Marshal(&agentEventRecord{
		AgentName: event.AgentName,
		Message:   sanitizedMessageValue(msg),
	})
	if err != nil {
		return "", fmt.Errorf("marshal agent output event: %w", err)
	}
	return string(data), nil
}

func (r *agentEventFileReceiver[M]) fail(err error) {
	if err == nil || r.failed {
		return
	}
	r.failed = true
	if r.onError != nil {
		r.onError(err)
	}
}

// sanitizedMessageValue makes a non-mutating copy of a schema message and
// removes its root Extra field before JSON serialization. Nested Extra fields,
// custom extensions, and provider extensions remain part of the output.
func sanitizedMessageValue[M adk.MessageType](msg M) any {
	switch m := any(msg).(type) {
	case adk.Message:
		if m == nil {
			return nil
		}
		cloned := *m
		cloned.Extra = nil
		return &cloned
	case adk.AgenticMessage:
		if m == nil {
			return nil
		}
		cloned := *m
		cloned.Extra = nil
		return &cloned
	}
	return msg
}

// agentOutputFilePath allocates an output-file path under outputDir without
// opening it. The work callback creates the file lazily through its single
// append session. The file is named after the launching tool-call id (so it
// matches Task.ToolUseID), falling back to a uuid when no tool-call id is in
// context. Returns "" when output files are not configured.
func agentOutputFilePath(ctx context.Context, store filesystem.AppendOpener, outputDir string) string {
	if store == nil || outputDir == "" {
		return ""
	}
	name := compose.GetToolCallID(ctx)
	if name == "" {
		name = uuid.NewString()
	}
	return filepath.Join(outputDir, name+".output")
}

// resolveSubAgent looks up the agent-as-tool adapter for subagentType and builds
// the marshaled request for it. If prompt is empty, description is used as the
// task request.
func resolveSubAgent(subAgents map[string]tool.InvokableTool, subagentType, prompt, description string) (tool.InvokableTool, string, error) {
	a, ok := subAgents[subagentType]
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
	return a, params, nil
}

// defaultAgentToolDescription generates the agent tool description with sub-agent list.
func defaultAgentToolDescription[M adk.MessageType](ctx context.Context, subAgents []adk.TypedAgent[M]) (string, error) {
	subAgentsDescBuilder := strings.Builder{}
	for _, a := range subAgents {
		name := a.Name(ctx)
		desc := a.Description(ctx)
		_, _ = fmt.Fprintf(&subAgentsDescBuilder, "- %s: %s\n", name, desc)
	}
	toolDesc := internal.SelectPrompt(internal.I18nPrompts{
		English: agentToolDescription,
		Chinese: agentToolDescriptionChinese,
	})
	return pyfmt.Fmt(toolDesc, map[string]any{
		"other_agents": subAgentsDescBuilder.String(),
	})
}
