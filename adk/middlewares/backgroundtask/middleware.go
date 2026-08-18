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

// Package backgroundtask provides the middleware that injects the background-task
// control tools (task_output, task_stop) into an agent.
//
// It is the single owner of these control tools: domain middlewares (subagent,
// filesystem) that launch background work register that work into a shared
// *backgroundtask.Manager, but they must NOT inject task_output/task_stop
// themselves. Wire this middleware exactly once per agent, bound to the same
// Manager the domain middlewares share, so the control tools are not duplicated.
package backgroundtask

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
	bgtask "github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

const (
	taskOutputToolName = "task_output"
	taskStopToolName   = "task_stop"
)

// ToolConfig configures one of the injected control tools (task_output, task_stop).
type ToolConfig struct {
	// Name overrides the tool name used in registration.
	// Optional; the default name ("task_output" / "task_stop") is used when empty.
	Name string

	// Desc overrides the tool description used in registration.
	// Optional; the built-in description (with i18n) is used when nil.
	Desc *string

	// Disable removes this tool from the injected set.
	// Optional; false by default. Use it to expose only one of the control tools.
	Disable bool
}

// Config configures the background-task control middleware for the standard
// *schema.Message message type. It is the default specialization of TypedConfig.
type Config = TypedConfig[*schema.Message]

// TaskProgressReader projects executor-specific progress without mutating task
// state. Implementations may be called concurrently.
type TaskProgressReader interface {
	ReadProgress(context.Context, *bgtask.Task) (string, error)
}

// TypedConfig configures the background-task control middleware, parameterized by
// message type.
type TypedConfig[M adk.MessageType] struct {
	// Manager is the shared background-task Manager whose tasks the injected
	// task_output/task_stop tools inspect and cancel. Required.
	//
	// It is typically the same Manager the domain middlewares (subagent, filesystem)
	// were given, so a single task-ID space spans agent and shell runs.
	Manager *bgtask.Manager
	// ProgressReadersByExecutorKey selects progress projections by persisted ExecutorKey.
	// Readers may be called concurrently and must not mutate task lifecycle state.
	ProgressReadersByExecutorKey map[string]TaskProgressReader

	// TaskOutputToolConfig configures the task_output tool. Optional.
	TaskOutputToolConfig *ToolConfig
	// TaskStopToolConfig configures the task_stop tool. Optional.
	TaskStopToolConfig *ToolConfig

	// CustomSystemPrompt fully replaces the default background-task instruction
	// appended to the agent instruction. Optional; when nil the built-in instruction
	// is used. It receives the default control-tool names so the returned text can
	// reference them; returning "" injects no instruction.
	CustomSystemPrompt func(ctx context.Context, in *SystemPromptInput) string
}

// SystemPromptInput carries the default control-tool names passed to CustomSystemPrompt.
type SystemPromptInput struct {
	// the default task_output tool name.
	DefaultTaskOutputToolName string
	// the default task_stop tool name.
	DefaultTaskStopToolName string
}

// New creates a middleware that injects the task_output and task_stop tools, bound
// to the Manager in config, for the standard *schema.Message message type.
func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped(ctx, config)
}

// NewTyped creates a background-task control middleware parameterized by message type.
// See New for behavior details.
func NewTyped[M adk.MessageType](ctx context.Context, config *TypedConfig[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	if config == nil || config.Manager == nil {
		return nil, fmt.Errorf("backgroundtask: Manager is required")
	}
	mgr := config.Manager

	outputEnabled := !disabled(config.TaskOutputToolConfig)
	stopEnabled := !disabled(config.TaskStopToolConfig)

	var tools []tool.BaseTool
	if outputEnabled {
		progressReaders := make(map[string]TaskProgressReader, len(config.ProgressReadersByExecutorKey))
		for key, reader := range config.ProgressReadersByExecutorKey {
			if key != "" && reader != nil {
				progressReaders[key] = reader
			}
		}
		outputTool, err := newTaskOutputTool(mgr, config.TaskOutputToolConfig, progressReaders)
		if err != nil {
			return nil, fmt.Errorf("backgroundtask: failed to create task_output tool: %w", err)
		}
		tools = append(tools, outputTool)
	}
	if stopEnabled {
		stopTool, err := newTaskStopTool(mgr, config.TaskStopToolConfig)
		if err != nil {
			return nil, fmt.Errorf("backgroundtask: failed to create task_stop tool: %w", err)
		}
		tools = append(tools, stopTool)
	}

	instruction := buildInstruction(config.TaskOutputToolConfig, outputEnabled, config.TaskStopToolConfig, stopEnabled)
	if config.CustomSystemPrompt != nil {
		instruction = config.CustomSystemPrompt(ctx, &SystemPromptInput{
			DefaultTaskOutputToolName: taskOutputToolName,
			DefaultTaskStopToolName:   taskStopToolName,
		})
	}

	return &typedMiddleware[M]{
		tools:       tools,
		instruction: instruction,
	}, nil
}

// disabled reports whether a tool config opts out of registering its tool.
func disabled(c *ToolConfig) bool {
	return c != nil && c.Disable
}

// buildInstruction assembles the background-task instruction so the per-tool
// sentences name the tools as actually registered and omit any disabled tool.
// It returns "" when no control tool is enabled, so a fully-disabled middleware
// injects nothing.
func buildInstruction(outputCfg *ToolConfig, outputEnabled bool, stopCfg *ToolConfig, stopEnabled bool) string {
	if !outputEnabled && !stopEnabled {
		return ""
	}

	instruction := internal.SelectPrompt(internal.I18nPrompts{
		English: backgroundTaskPromptHeader,
		Chinese: backgroundTaskPromptHeaderChinese,
	})
	if outputEnabled {
		line := internal.SelectPrompt(internal.I18nPrompts{
			English: backgroundTaskOutputLine,
			Chinese: backgroundTaskOutputLineChinese,
		})
		instruction += fmt.Sprintf(line, selectToolName(outputCfg, taskOutputToolName))
	}
	if stopEnabled {
		line := internal.SelectPrompt(internal.I18nPrompts{
			English: backgroundTaskStopLine,
			Chinese: backgroundTaskStopLineChinese,
		})
		instruction += fmt.Sprintf(line, selectToolName(stopCfg, taskStopToolName))
	}
	instruction += internal.SelectPrompt(internal.I18nPrompts{
		English: backgroundTaskPromptFooter,
		Chinese: backgroundTaskPromptFooterChinese,
	})
	return instruction
}

// selectToolName returns the configured name override, or the default when unset.
func selectToolName(c *ToolConfig, defaultName string) string {
	if c != nil && c.Name != "" {
		return c.Name
	}
	return defaultName
}

// selectToolDesc returns the configured description override, or the built-in
// i18n description when unset.
func selectToolDesc(c *ToolConfig, english, chinese string) string {
	if c != nil && c.Desc != nil {
		return *c.Desc
	}
	return internal.SelectPrompt(internal.I18nPrompts{English: english, Chinese: chinese})
}

type typedMiddleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]
	tools       []tool.BaseTool
	instruction string
}

// BeforeAgent injects the control tools and instruction into the agent context.
func (m *typedMiddleware[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	if m.instruction != "" {
		nRunCtx.Instruction += "\n" + m.instruction
	}
	nRunCtx.Tools = append(nRunCtx.Tools, m.tools...)
	return ctx, &nRunCtx, nil
}

type taskOutputInput struct {
	TaskID string `json:"task_id" jsonschema:"required" jsonschema_description:"The task ID to get output from"`
	// Block defaults to true (wait for the task to finish). A *bool distinguishes
	// "omitted" (wait) from an explicit false (return the current status now).
	Block   *bool `json:"block,omitempty" jsonschema_description:"Whether to wait for a lifecycle state change or completion. Defaults to true; progress events alone do not wake this wait. Set false to return current state immediately."`
	Timeout int   `json:"timeout,omitempty" jsonschema_description:"Maximum time to wait in milliseconds when blocking. Defaults to 30000; capped at 600000."`
}

const (
	defaultTaskOutputTimeoutMs = 30000
	maxTaskOutputTimeoutMs     = 600000
)

func newTaskOutputTool(
	mgr *bgtask.Manager,
	cfg *ToolConfig,
	progressReaders map[string]TaskProgressReader,
) (tool.InvokableTool, error) {
	name := selectToolName(cfg, taskOutputToolName)
	desc := selectToolDesc(cfg, taskOutputToolDescription, taskOutputToolDescriptionChinese)
	return utils.InferTool(name, desc, func(ctx context.Context, input taskOutputInput) (string, error) {
		if task, err := mgr.Get(ctx, input.TaskID); err == nil {
			return resolveDurableTaskWithReaders(
				ctx, mgr, task, input, progressReaders,
			)
		} else if errors.Is(err, bgtask.ErrNotFound) {
			return fmt.Sprintf("Task %q not found", input.TaskID), nil
		} else {
			return "", err
		}
	})
}

func resolveDurableTask(
	ctx context.Context,
	mgr *bgtask.Manager,
	task *bgtask.Task,
	input taskOutputInput,
	readProgress func(context.Context, *bgtask.Task) (string, error),
) (string, error) {
	if readProgress == nil {
		return resolveDurableTaskWithReaders(ctx, mgr, task, input, nil)
	}
	readers := map[string]TaskProgressReader{
		task.Spec.ExecutorKey: taskProgressReaderFunc(readProgress),
	}
	return resolveDurableTaskWithReaders(ctx, mgr, task, input, readers)
}

type taskProgressReaderFunc func(context.Context, *bgtask.Task) (string, error)

func (f taskProgressReaderFunc) ReadProgress(ctx context.Context, task *bgtask.Task) (string, error) {
	return f(ctx, task)
}

func resolveDurableTaskWithReaders(
	ctx context.Context,
	mgr *bgtask.Manager,
	task *bgtask.Task,
	input taskOutputInput,
	progressReaders map[string]TaskProgressReader,
) (string, error) {
	block := input.Block == nil || *input.Block
	if block && waitableStatus(task.Status) {
		timeout := input.Timeout
		if timeout <= 0 {
			timeout = defaultTaskOutputTimeoutMs
		}
		if timeout > maxTaskOutputTimeoutMs {
			timeout = maxTaskOutputTimeoutMs
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer cancel()
		for waitableStatus(task.Status) {
			next, waitErr := mgr.WaitForTaskVersion(waitCtx, &bgtask.WaitForTaskVersionRequest{
				TaskID: input.TaskID, AfterVersion: task.Version,
			})
			if waitErr != nil {
				if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
					break
				}
				return "", waitErr
			}
			task = next
		}
	}
	result := formatTask(task)
	if reader := progressReaders[task.Spec.ExecutorKey]; reader != nil {
		progress, progressErr := reader.ReadProgress(ctx, task)
		if progressErr != nil {
			result += fmt.Sprintf("\nTranscript unavailable: %s", progressErr)
		} else if progress != "" {
			result += "\n" + progress
		}
	}
	return result, nil
}

func waitableStatus(status bgtask.Status) bool {
	return status == bgtask.StatusPending ||
		status == bgtask.StatusRunning
}

type taskStopInput struct {
	TaskID string `json:"task_id" jsonschema:"required" jsonschema_description:"The ID of the background task to stop"`
	Reason string `json:"reason,omitempty" jsonschema_description:"Optional reason for stopping the background task"`
}

func newTaskStopTool(mgr *bgtask.Manager, cfg *ToolConfig) (tool.InvokableTool, error) {
	name := selectToolName(cfg, taskStopToolName)
	desc := selectToolDesc(cfg, taskStopToolDescription, taskStopToolDescriptionChinese)
	return utils.InferTool(name, desc, func(ctx context.Context, input taskStopInput) (string, error) {
		task, err := mgr.RequestCancel(
			ctx, input.TaskID, bgtask.WithCancellationReason(input.Reason),
		)
		if err != nil {
			return fmt.Sprintf("Failed to stop task %q: %s", input.TaskID, err.Error()), nil
		}
		if task.Status == bgtask.StatusCanceled {
			return fmt.Sprintf("Successfully stopped task: %s", input.TaskID), nil
		}
		return fmt.Sprintf("Stop requested for task %s", input.TaskID), nil
	})
}

func formatTask(task *bgtask.Task) string {
	result := fmt.Sprintf(
		"Task ID: %s\nDescription: %s\nStatus: %s",
		task.Spec.ID, task.Spec.Description, task.Status,
	)
	label := "Output transcript"
	switch task.Spec.Kind {
	case "subagent":
		label = "Event transcript (JSONL)"
	case "bash":
		label = "Command output transcript"
	}
	if task.Spec.OutputFile != "" && task.OutputFileErr == "" {
		result += fmt.Sprintf("\n%s: %s (use Read on this path for the output)", label, task.Spec.OutputFile)
	} else if task.Spec.OutputFile != "" {
		result += fmt.Sprintf(
			"\n%s: %s (incomplete — a write failed: %s; full transcript is unavailable. The terminal Result, if any, remains authoritative)",
			label, task.Spec.OutputFile, task.OutputFileErr,
		)
	}
	if len(task.ResultData) > 0 {
		result += fmt.Sprintf("\nResult: %s", string(task.ResultData))
	}
	if task.ResultError != "" {
		result += fmt.Sprintf("\nError: %s", task.ResultError)
	}
	if task.DoneAt != nil {
		result += fmt.Sprintf(
			"\nElapsed: %s",
			task.DoneAt.Sub(task.CreatedAt).Round(time.Millisecond),
		)
	}
	return result
}
