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
	"path/filepath"

	"github.com/google/uuid"

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
// When the filesystem middleware is configured with both a Backend and an
// OutputDir, the managed execute tool writes each task's output to a file under
// that directory and records the path on Task.OutputFile (streaming runs tee
// chunks as interim output; buffered runs write the result on completion). The
// Manager itself never writes — the execute tool owns it.
const ExecuteTaskType = "bash"

// defaultBashStartupPreviewMs is the caller-visible startup window for an
// explicitly backgrounded StreamingShell run. The task is backgrounded from the
// start; only its initial output remains attached briefly so launch-time prompts
// such as OAuth URLs are not hidden in the background output file.
const defaultBashStartupPreviewMs = 1_000

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

// outputSink bundles the output-file configuration for a managed execute tool: a
// AppendOpener to open append streams through and the directory to reserve paths
// under. Both must be set to enable output files; a zero outputSink disables them.
type outputSink struct {
	store     filesystem.AppendOpener
	outputDir string
}

// bashOutputWriter tees a managed execute task's output to a file via a
// filesystem.AppendOpener. It is built per invocation: when both an AppendOpener
// and an outputDir are configured it reserves outputDir/<uuid>.output (created empty
// up front) and opens an append stream when the work starts; otherwise it is
// disabled and every method is a no-op, so the task has no output file. There is no
// rewrite fallback — output files require an AppendOpener.
//
// The execute tool — not the Manager — owns writing, so streaming runs tee interim
// output as chunks arrive. It is single-consumer: append is called serially on
// the StreamReaderWithConvert Recv stack, so no synchronization is needed.
//
// On the first append failure the file is left with a gap, so the writer records
// the failure via mgr.MarkOutputFileUnreliable (keyed by taskID, which the work
// func receives from the Manager and sets on the writer before its first append)
// and stops attempting further writes.
type bashOutputWriter struct {
	mgr    *backgroundtask.Manager
	store  filesystem.AppendOpener // nil => disabled
	path   string
	stream io.WriteCloser // opened lazily by the streaming work; nil => not open
	taskID string         // set by the work func once the Manager assigns it
	failed bool           // set after the first append error: the file is now partial
}

// reserveBashOutput builds a writer that tees under the sink, or a disabled writer
// when output files are not configured (no store / no dir). It creates the file
// empty up front (open+close, which creates the file on open) so the advertised path
// exists before any output; if even that reservation fails, it returns a disabled
// writer so the task advertises no output file and consumers fall back to the
// in-memory Result. The file is named after the launching tool-call id (so it matches
// Task.ToolUseID), falling back to a uuid when no tool-call id is in context.
//
// The streaming append session is not opened here: it is opened by the work func
// (see open) under the work's context, so it survives the run being backgrounded.
func reserveBashOutput(ctx context.Context, mgr *backgroundtask.Manager, sink outputSink) *bashOutputWriter {
	if sink.store == nil || sink.outputDir == "" {
		return &bashOutputWriter{}
	}
	path := filepath.Join(sink.outputDir, outputFileName(ctx)+".output")
	s, err := sink.store.OpenAppend(ctx, &filesystem.OpenAppendRequest{FilePath: path})
	if err != nil {
		return &bashOutputWriter{}
	}
	if err := s.Close(); err != nil {
		return &bashOutputWriter{}
	}
	return &bashOutputWriter{
		mgr:   mgr,
		store: sink.store,
		path:  path,
	}
}

// fail records that a write to the output file failed: it stops further writes and
// marks the file unreliable (by task id) so task_output reports the file's failed
// state instead of trusting the partial file.
func (w *bashOutputWriter) fail(err error) {
	w.failed = true
	w.mgr.MarkOutputFileUnreliable(w.taskID, err.Error())
}

// open opens the streaming append session for a streaming run. ctx is the work's
// context (detached from the caller), so the session outlives a backgrounded run.
// On failure the writer is marked failed and the file unreliable, so subsequent
// appends are no-ops. It is a no-op for a disabled writer.
func (w *bashOutputWriter) open(ctx context.Context) {
	if w.store == nil || w.failed {
		return
	}
	stream, err := w.store.OpenAppend(ctx, &filesystem.OpenAppendRequest{FilePath: w.path})
	if err != nil {
		w.fail(err)
		return
	}
	w.stream = stream
}

// append tees content to the open streaming session. The session binds its context
// at open, so no ctx is needed here.
func (w *bashOutputWriter) append(content string) {
	if w.stream == nil || w.failed {
		return
	}
	if _, err := io.WriteString(w.stream, content); err != nil {
		w.fail(err)
	}
}

// closeStream finalizes the streaming session, flushing buffered content and
// releasing the backend handle. It always attempts Close (even when a prior write
// already broke the stream) so the handle is released promptly rather than waiting
// on context cancellation; only a fresh Close error marks the file unreliable, since
// a stream broken by an earlier write was already marked there. No-op for a disabled
// writer.
func (w *bashOutputWriter) closeStream() {
	if w.stream == nil {
		return
	}
	if err := w.stream.Close(); err != nil && !w.failed {
		w.mgr.MarkOutputFileUnreliable(w.taskID, err.Error())
	}
	w.stream = nil
}

// appendResult writes the full buffered result to the output file. Used by the
// buffered (non-streaming) work, which produces no incremental chunks: it is just a
// single-chunk stream, so it reuses the same open→append→close session as the
// streaming path. It is a no-op for a disabled writer.
func (w *bashOutputWriter) appendResult(ctx context.Context, content string) {
	w.open(ctx)
	w.append(content)
	w.closeStream()
}

// outputFileName returns the base name (without extension) for a task's output
// file: the launching tool-call id when present (so the file matches
// Task.ToolUseID), or a uuid fallback when no tool-call id is in context — the
// fallback keeps names unique so concurrent untagged tasks don't collide.
func outputFileName(ctx context.Context) string {
	if id := compose.GetToolCallID(ctx); id != "" {
		return id
	}
	return uuid.NewString()
}

// bashWork adapts a blocking shell execution into a backgroundtask.WorkFunc.
// The request carries only the command; the Manager is the sole owner of
// foreground/background/auto-background switching, so no background hint is
// pushed down to the backend. On success it appends the result to the output file
// (when one is configured) before returning, so the file matches Task.Result.
func bashWork(sb filesystem.Shell, req *filesystem.ExecuteRequest, w *bashOutputWriter) backgroundtask.WorkFunc {
	return func(ctx context.Context, task backgroundtask.TaskInfo) (string, error) {
		w.taskID = task.ID
		result, err := sb.Execute(ctx, req)
		if err != nil {
			return "", err
		}
		out := convExecuteResponse(result)
		w.appendResult(ctx, out)
		return out, nil
	}
}

// bashStreamWork adapts a streaming shell execution into a backgroundtask.StreamWorkFunc.
// It returns a stream of formatted output chunks; the Manager forwards them to the
// caller in real time (for the foreground phase) and accumulates them into the
// task's final result. The terminal note (exit code / no-output) is emitted as a
// final chunk so it is part of both the live stream and the persisted result.
//
// Each emitted chunk (and the terminal note) is also teed to the output file via w,
// so the file carries interim output while the task runs. The append session is
// opened once under the work's context (so it survives backgrounding); teeing then
// happens inside the convert callback, which runs on the Recv stack for both the
// foreground loop and the background drain, and the session is closed in the OnEOF
// hook on clean exhaustion and via the WithErrWrapper hook on a source error — the
// two terminal outcomes the convert reader observes — so the handle is flushed and
// released on both. On the rarer caller-abandon / timeout paths the source ends
// without either hook firing; the session is then released by the work context's
// cancellation (the file is already incomplete in that case), which the AppendOpener
// contract requires a resource-holding backend to honor.
func bashStreamWork(sb filesystem.StreamingShell, req *filesystem.ExecuteRequest, w *bashOutputWriter) backgroundtask.StreamWorkFunc {
	return func(ctx context.Context, task backgroundtask.TaskInfo) (*schema.StreamReader[string], error) {
		w.taskID = task.ID
		stream, err := sb.ExecuteStreaming(ctx, req)
		if err != nil {
			return nil, err
		}
		// Open the streaming append session under the work's (detached) context, so a
		// backgrounded run keeps writing after the launching turn ends.
		w.open(ctx)

		// exitCode/hasContent accumulate across chunks: convert writes them per
		// chunk, the OnEOF hook reads them to build the terminal note. The convert
		// model has no per-stream state of its own, so they live in this closure.
		// Safe without synchronization because StreamReaderWithConvert is pull-driven
		// and single-consumer — convert, OnEOF and the error wrapper run serially on
		// the same Recv stack.
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
				w.append(text)
				return text, nil
			},
			schema.WithOnEOF(func() (any, error) {
				note := execTerminalNote(exitCode, hasContent)
				if note != "" {
					w.append(note)
				}
				// Source exhausted: flush and finalize the file before ending the stream.
				w.closeStream()
				if note != "" {
					return note, nil
				}
				return nil, io.EOF
			}),
			schema.WithErrWrapper(func(err error) error {
				// Source errored (including ctx-cancellation surfaced as an error):
				// finalize the file so the handle is released rather than leaked to
				// context cancellation. closeStream is idempotent, so this never
				// races OnEOF (a stream ends with either EOF or an error).
				w.closeStream()
				return err
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
// foreground phase streams output to the caller in real time. An explicit
// background launch first exposes a bounded startup preview, then caps the stream
// with a notice; a run moved to the background after its foreground timeout caps
// the stream immediately. In both cases the rest is drained into the task result.
// With a plain Shell backend the tool is buffered and has no startup preview.
//
// Exactly one of sb / streaming must be non-nil. sink, when fully configured
// (AppendOpener + dir), enables per-task output files (the tool appends output to
// outputDir/<id>.output); otherwise runs have no output file.
func newManagedExecuteTool(
	mgr *backgroundtask.Manager,
	sb filesystem.Shell,
	streaming filesystem.StreamingShell,
	sink outputSink,
	name string,
	desc string,
) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameExecute)
	d, err := selectToolDesc(desc, ManagedExecuteToolDesc, ManagedExecuteToolDescChinese)
	if err != nil {
		return nil, err
	}

	if streaming != nil {
		return newManagedStreamingExecuteTool(mgr, streaming, sink, toolName, d)
	}
	return newManagedBufferedExecuteTool(mgr, sb, sink, toolName, d)
}

// managedRunInput builds the RunInput shared by the buffered and streaming managed
// execute tools. w supplies the reserved output-file path (empty when output files
// are not configured), which the work funcs write to.
func managedRunInput(ctx context.Context, input executeManagedArgs, w *bashOutputWriter) *backgroundtask.RunInput {
	runInput := &backgroundtask.RunInput{
		Description:     input.Command,
		Type:            ExecuteTaskType,
		ToolUseID:       compose.GetToolCallID(ctx),
		RunInBackground: input.RunInBackground,
		Metadata:        map[string]any{MetadataKeyCommand: input.Command},
		OutputFile:      w.path,
	}
	// A positive timeout overrides the Manager's default foreground timeout for
	// this command. When the deadline expires, the Manager's policy decides
	// whether to move the task to the background or stop it.
	if input.TimeoutMS > 0 {
		runInput.ForegroundTimeoutMs = &input.TimeoutMS
	}
	return runInput
}

func newManagedBufferedExecuteTool(mgr *backgroundtask.Manager, sb filesystem.Shell, sink outputSink, toolName, desc string) (tool.BaseTool, error) {
	return utils.InferTool(toolName, desc, func(ctx context.Context, input executeManagedArgs) (string, error) {
		req := &filesystem.ExecuteRequest{Command: input.Command}
		w := reserveBashOutput(ctx, mgr, sink)
		result, err := mgr.Run(ctx, managedRunInput(ctx, input, w), bashWork(sb, req, w))
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

func newManagedStreamingExecuteTool(mgr *backgroundtask.Manager, streaming filesystem.StreamingShell, sink outputSink, toolName, desc string) (tool.BaseTool, error) {
	return utils.InferStreamTool(toolName, desc, func(ctx context.Context, input executeManagedArgs) (*schema.StreamReader[string], error) {
		req := &filesystem.ExecuteRequest{Command: input.Command}
		w := reserveBashOutput(ctx, mgr, sink)
		runInput := managedRunInput(ctx, input, w)
		runInput.BackgroundStartupPreviewMs = defaultBashStartupPreviewMs
		// RunStream owns the returned stream: it forwards work chunks to this caller
		// in real time. An explicit background launch exposes only its bounded startup
		// preview before the notice; auto-background caps the stream at the transition.
		// In either case the remaining output is drained into the task result.
		return mgr.RunStream(ctx, runInput, bashStreamWork(streaming, req, w))
	})
}
