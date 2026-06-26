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
	"fmt"
	"sync"
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

// TypedConfig configures the background-task control middleware, parameterized by
// message type.
type TypedConfig[M adk.MessageType] struct {
	// Manager is the shared background-task Manager whose tasks the injected
	// task_output/task_stop tools inspect and cancel. Required.
	//
	// It is typically the same Manager the domain middlewares (subagent, filesystem)
	// were given, so a single task-ID space spans agent and shell runs.
	Manager *bgtask.Manager

	// TaskOutputToolConfig configures the task_output tool. Optional.
	TaskOutputToolConfig *ToolConfig
	// TaskStopToolConfig configures the task_stop tool. Optional.
	TaskStopToolConfig *ToolConfig
}

// New creates a middleware that injects the task_output and task_stop tools, bound
// to the Manager in config, for the standard *schema.Message message type.
func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped[*schema.Message](ctx, config)
}

// NewTyped creates a background-task control middleware parameterized by message type.
// See New for behavior details.
func NewTyped[M adk.MessageType](_ context.Context, config *TypedConfig[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	if config == nil || config.Manager == nil {
		return nil, fmt.Errorf("backgroundtask: Manager is required")
	}
	mgr := config.Manager
	queried := newQueryTracker()

	var tools []tool.BaseTool
	if !disabled(config.TaskOutputToolConfig) {
		outputTool, err := newTaskOutputTool(mgr, queried, config.TaskOutputToolConfig)
		if err != nil {
			return nil, fmt.Errorf("backgroundtask: failed to create task_output tool: %w", err)
		}
		tools = append(tools, outputTool)
	}
	if !disabled(config.TaskStopToolConfig) {
		stopTool, err := newTaskStopTool(mgr, config.TaskStopToolConfig)
		if err != nil {
			return nil, fmt.Errorf("backgroundtask: failed to create task_stop tool: %w", err)
		}
		tools = append(tools, stopTool)
	}

	instruction := internal.SelectPrompt(internal.I18nPrompts{
		English: backgroundTaskPrompt,
		Chinese: backgroundTaskPromptChinese,
	})

	return &typedMiddleware[M]{
		tools:       tools,
		instruction: instruction,
	}, nil
}

// disabled reports whether a tool config opts out of registering its tool.
func disabled(c *ToolConfig) bool {
	return c != nil && c.Disable
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
	nRunCtx.Instruction += "\n" + m.instruction
	nRunCtx.Tools = append(nRunCtx.Tools, m.tools...)
	return ctx, &nRunCtx, nil
}

type taskOutputInput struct {
	TaskID string `json:"task_id" jsonschema:"required" jsonschema_description:"The task ID to get output from"`
	// Block defaults to true (wait for the task to finish). A *bool distinguishes
	// "omitted" (wait) from an explicit false (return the current status now).
	Block   *bool `json:"block,omitempty" jsonschema_description:"Whether to wait for the task to complete. Defaults to true; set false to return the current status immediately."`
	Timeout int   `json:"timeout,omitempty" jsonschema_description:"Maximum time to wait in milliseconds when blocking. Defaults to 30000; capped at 600000."`
}

// queryTracker is owned by the task_output middleware, not by Manager. It keeps
// consumption bookkeeping out of the lifecycle registry.
type queryTracker struct {
	mu      sync.Mutex
	queried map[string]struct{}
}

func newQueryTracker() *queryTracker {
	return &queryTracker{queried: make(map[string]struct{})}
}

func (q *queryTracker) mark(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queried[id] = struct{}{}
}

const (
	defaultTaskOutputTimeoutMs = 30000
	maxTaskOutputTimeoutMs     = 600000
)

func newTaskOutputTool(mgr *bgtask.Manager, queried *queryTracker, cfg *ToolConfig) (tool.InvokableTool, error) {
	name := selectToolName(cfg, taskOutputToolName)
	desc := selectToolDesc(cfg, taskOutputToolDescription, taskOutputToolDescriptionChinese)
	return utils.InferTool(name, desc, func(ctx context.Context, input taskOutputInput) (string, error) {
		task, ok := resolveTask(ctx, mgr, input)
		if !ok {
			return fmt.Sprintf("Task %q not found", input.TaskID), nil
		}

		// Only mark the result as consumed once the task has actually finished.
		// A still-running task has no final result yet, so polling its status
		// must not mark a never-read result as consumed.
		if task.Status != bgtask.StatusRunning {
			queried.mark(input.TaskID)
		}

		return formatTask(task), nil
	})
}

// resolveTask fetches the task, optionally blocking until it finishes. Blocking is
// the default; it is bounded by input.Timeout (clamped to [0, max], default 30s).
// The returned bool reports whether the task exists (not whether it finished).
func resolveTask(ctx context.Context, mgr *bgtask.Manager, input taskOutputInput) (*bgtask.Task, bool) {
	if input.Block != nil && !*input.Block {
		return mgr.Get(input.TaskID)
	}

	timeoutMs := input.Timeout
	if timeoutMs <= 0 {
		timeoutMs = defaultTaskOutputTimeoutMs
	}
	if timeoutMs > maxTaskOutputTimeoutMs {
		timeoutMs = maxTaskOutputTimeoutMs
	}

	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	// Wait's bool reports whether the task reached a terminal state; for the tool we
	// only care whether the task exists, so translate via the returned snapshot.
	task, _ := mgr.Wait(waitCtx, input.TaskID)
	return task, task != nil
}

type taskStopInput struct {
	TaskID string `json:"task_id" jsonschema:"required" jsonschema_description:"The ID of the background task to stop"`
}

func newTaskStopTool(mgr *bgtask.Manager, cfg *ToolConfig) (tool.InvokableTool, error) {
	name := selectToolName(cfg, taskStopToolName)
	desc := selectToolDesc(cfg, taskStopToolDescription, taskStopToolDescriptionChinese)
	return utils.InferTool(name, desc, func(ctx context.Context, input taskStopInput) (string, error) {
		if err := mgr.Cancel(input.TaskID); err != nil {
			return fmt.Sprintf("Failed to stop task %q: %s", input.TaskID, err.Error()), nil
		}
		return fmt.Sprintf("Successfully stopped task: %s", input.TaskID), nil
	})
}

func formatTask(task *bgtask.Task) string {
	result := fmt.Sprintf("Task ID: %s\nDescription: %s\nStatus: %s",
		task.ID, task.Description, task.Status)

	// When the task has an output file, the file is authoritative — point at it and
	// do not inline Result. The file carries the same (or interim) output and may be
	// large, so Read'ing it selectively avoids inlining the whole blob. Without an
	// output file, Result is the only copy, so inline it.
	if task.OutputFile != "" {
		result += fmt.Sprintf("\nOutput file: %s (use Read on this path for the output)", task.OutputFile)
	} else if task.Result != "" {
		result += fmt.Sprintf("\nResult: %s", task.Result)
	}
	if task.Error != "" {
		result += fmt.Sprintf("\nError: %s", task.Error)
	}
	if task.DoneAt != nil {
		result += fmt.Sprintf("\nCompleted at: %s", task.DoneAt.Format("2006-01-02 15:04:05"))
	}

	return result
}
