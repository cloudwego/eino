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

package filesystem

import (
	"context"
	"fmt"
	"io"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ExecuteTaskType is the backgroundtask Task.Type tag for shell-command tasks
// launched by the execute tool. A shared Manager's ShouldAutoBackground hook can
// match on it to apply shell-specific policy, recovering the command via
// CommandFromTask.
//
// A filesystem.Backend satisfies backgroundtask.OutputStore directly, so a
// background Manager can persist task output through the same backend used for the
// file tools: backgroundtask.Config{OutputStore: backend, OutputDir: ...}.
const ExecuteTaskType = "bash"

// MetadataKeyCommand is the RunInput.Metadata / Task.Metadata key under which the
// execute tool records the shell command for a task. A ShouldAutoBackground hook
// reads it (via CommandFromTask) to apply command-specific policy without parsing
// the human-readable Description. The value is a string.
const MetadataKeyCommand = "command"

// CommandFromTask returns the shell command recorded in a shell task's metadata
// under MetadataKeyCommand. The execute tool always records it for shell tasks, so
// a hook receiving a task of ExecuteTaskType can rely on a non-empty result; it
// returns "" only when given a nil or non-shell task.
func CommandFromTask(t *backgroundtask.Task) string {
	if t == nil {
		return ""
	}
	cmd, _ := t.Metadata[MetadataKeyCommand].(string)
	return cmd
}

// bashWork adapts a blocking shell execution into a backgroundtask.WorkFunc.
// The request carries only the command; the Manager is the sole owner of
// foreground/background/auto-background switching, so no background hint is
// pushed down to the backend.
func bashWork(sb filesystem.Shell, req *filesystem.ExecuteRequest) backgroundtask.WorkFunc {
	return func(ctx context.Context) (string, error) {
		result, err := sb.Execute(ctx, req)
		if err != nil {
			return "", err
		}
		return convExecuteResponse(result), nil
	}
}

// bashStreamWork adapts a streaming shell execution into a backgroundtask.StreamWorkFunc.
// It returns a stream of formatted output chunks; the Manager forwards them to the
// caller in real time (for the foreground phase) and accumulates them into the
// task's final result. The terminal note (exit code / no-output) is emitted as a
// final chunk so it is part of both the live stream and the persisted result.
func bashStreamWork(sb filesystem.StreamingShell, req *filesystem.ExecuteRequest) backgroundtask.StreamWorkFunc {
	return func(ctx context.Context) (*schema.StreamReader[string], error) {
		stream, err := sb.ExecuteStreaming(ctx, req)
		if err != nil {
			return nil, err
		}

		// exitCode/hasContent accumulate across chunks: convert writes them per
		// chunk, the OnEOF hook reads them to build the terminal note. The convert
		// model has no per-stream state of its own, so they live in this closure.
		// Safe without synchronization because StreamReaderWithConvert is pull-driven
		// and single-consumer — convert and OnEOF run serially on the same Recv stack.
		var exitCode *int
		var hasContent bool
		return schema.StreamReaderWithConvert(stream,
			func(chunk *filesystem.ExecuteResponse) (string, error) {
				if chunk == nil {
					return "", schema.ErrNoValue
				}
				if chunk.ExitCode != nil {
					exitCode = chunk.ExitCode
				}
				text := formatExecChunk(chunk.Output, chunk.Truncated)
				if text == "" {
					return "", schema.ErrNoValue
				}
				hasContent = true
				return text, nil
			},
			schema.WithOnEOF(func() (any, error) {
				if note := execTerminalNote(exitCode, hasContent); note != "" {
					return note, nil
				}
				return nil, io.EOF
			}),
		), nil
	}
}

// newManagedExecuteTool builds an execute tool whose runs are tracked by a shared
// background-task Manager. The model controls background execution via the
// run_in_background field; auto-background switching is handled transparently by
// the Manager. On a background launch the tool returns the task ID so the agent
// can later query it via task_output.
//
// With a StreamingShell backend the tool is itself a StreamableTool: the
// foreground phase streams output to the caller in real time, and a run that moves
// to the background caps the stream with a notice (the rest is drained into the
// task result). With a plain Shell backend the tool is buffered.
//
// Exactly one of sb / streaming must be non-nil.
func newManagedExecuteTool(
	mgr *backgroundtask.Manager,
	sb filesystem.Shell,
	streaming filesystem.StreamingShell,
	name string,
	desc string,
) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameExecute)
	d, err := selectToolDesc(desc, ManagedExecuteToolDesc, ManagedExecuteToolDescChinese)
	if err != nil {
		return nil, err
	}

	if streaming != nil {
		return newManagedStreamingExecuteTool(mgr, streaming, toolName, d)
	}
	return newManagedBufferedExecuteTool(mgr, sb, toolName, d)
}

// managedRunInput builds the RunInput shared by the buffered and streaming managed
// execute tools.
func managedRunInput(ctx context.Context, input executeManagedArgs) *backgroundtask.RunInput {
	runInput := &backgroundtask.RunInput{
		Description:     input.Command,
		Type:            ExecuteTaskType,
		ToolUseID:       compose.GetToolCallID(ctx),
		RunInBackground: input.RunInBackground,
		Metadata:        map[string]any{MetadataKeyCommand: input.Command},
	}
	// A positive timeout overrides the Manager's default foreground budget for
	// this command. When the deadline expires, the Manager's policy decides
	// whether to move the task to the background or stop it.
	if input.TimeoutMS > 0 {
		runInput.ForegroundTimeoutMs = &input.TimeoutMS
	}
	return runInput
}

func newManagedBufferedExecuteTool(mgr *backgroundtask.Manager, sb filesystem.Shell, toolName, desc string) (tool.BaseTool, error) {
	return utils.InferTool(toolName, desc, func(ctx context.Context, input executeManagedArgs) (string, error) {
		req := &filesystem.ExecuteRequest{Command: input.Command}
		result, err := mgr.Run(ctx, managedRunInput(ctx, input), bashWork(sb, req))
		if err != nil {
			return "", err
		}

		switch result.Status {
		case backgroundtask.StatusCompleted:
			return result.Result, nil
		case backgroundtask.StatusRunning:
			msg := fmt.Sprintf("Command running in background with ID: %s.", result.ID)
			if result.OutputFile != "" {
				msg += fmt.Sprintf(" Output is being written to: %s.", result.OutputFile)
			}
			msg += " You will be notified when it completes."
			if result.OutputFile != "" {
				msg += " To check interim output, use Read on that file path."
			}
			return msg, nil
		case backgroundtask.StatusFailed:
			return "", fmt.Errorf("execute task %q failed: %s", result.ID, result.Error)
		case backgroundtask.StatusCanceled:
			return "", fmt.Errorf("execute task %q was canceled", result.ID)
		default:
			return result.Result, nil
		}
	})
}

func newManagedStreamingExecuteTool(mgr *backgroundtask.Manager, streaming filesystem.StreamingShell, toolName, desc string) (tool.BaseTool, error) {
	return utils.InferStreamTool(toolName, desc, func(ctx context.Context, input executeManagedArgs) (*schema.StreamReader[string], error) {
		req := &filesystem.ExecuteRequest{Command: input.Command}
		// RunStream owns the returned stream: it forwards work chunks to this caller
		// in real time, and on auto-background caps the stream with a notice while
		// draining the rest into the task result. A background launch (or timeout)
		// is therefore surfaced inline as a final chunk, not as an error.
		return mgr.RunStream(ctx, managedRunInput(ctx, input), bashStreamWork(streaming, req))
	})
}
