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

	outputTool, err := newTaskOutputTool(mgr)
	if err != nil {
		return nil, fmt.Errorf("backgroundtask: failed to create task_output tool: %w", err)
	}
	stopTool, err := newTaskStopTool(mgr)
	if err != nil {
		return nil, fmt.Errorf("backgroundtask: failed to create task_stop tool: %w", err)
	}

	instruction := internal.SelectPrompt(internal.I18nPrompts{
		English: backgroundTaskPrompt,
		Chinese: backgroundTaskPromptChinese,
	})

	return &typedMiddleware[M]{
		tools:       []tool.BaseTool{outputTool, stopTool},
		instruction: instruction,
	}, nil
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

const (
	defaultTaskOutputTimeoutMs = 30000
	maxTaskOutputTimeoutMs     = 600000
)

func newTaskOutputTool(mgr *bgtask.Manager) (tool.InvokableTool, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskOutputToolDescription,
		Chinese: taskOutputToolDescriptionChinese,
	})
	return utils.InferTool(taskOutputToolName, desc, func(ctx context.Context, input taskOutputInput) (string, error) {
		task, ok := resolveTask(ctx, mgr, input)
		if !ok {
			return fmt.Sprintf("Task %q not found", input.TaskID), nil
		}

		// Only mark the result as consumed once the task has actually finished.
		// A still-running task has no final result yet, so polling its status
		// must not flip ResultQueried — otherwise a never-read result would look
		// consumed.
		if task.Status != bgtask.StatusRunning {
			mgr.MarkQueried(input.TaskID)
		}

		return formatTask(task), nil
	})
}

// resolveTask fetches the task, optionally blocking until it finishes. Blocking is
// the default; it is bounded by input.Timeout (clamped to [0, max], default 30s).
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
	return mgr.WaitForTask(waitCtx, input.TaskID)
}

type taskStopInput struct {
	TaskID string `json:"task_id" jsonschema:"required" jsonschema_description:"The ID of the background task to stop"`
}

func newTaskStopTool(mgr *bgtask.Manager) (tool.InvokableTool, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskStopToolDescription,
		Chinese: taskStopToolDescriptionChinese,
	})
	return utils.InferTool(taskStopToolName, desc, func(ctx context.Context, input taskStopInput) (string, error) {
		if err := mgr.Cancel(input.TaskID); err != nil {
			return fmt.Sprintf("Failed to stop task %q: %s", input.TaskID, err.Error()), nil
		}
		return fmt.Sprintf("Successfully stopped task: %s", input.TaskID), nil
	})
}

func formatTask(task *bgtask.Task) string {
	result := fmt.Sprintf("Task ID: %s\nDescription: %s\nStatus: %s",
		task.ID, task.Description, task.Status)

	if task.Result != "" {
		result += fmt.Sprintf("\nResult: %s", task.Result)
	}
	if task.OutputFile != "" {
		result += fmt.Sprintf("\nOutput file: %s", task.OutputFile)
	}
	if task.Error != "" {
		result += fmt.Sprintf("\nError: %s", task.Error)
	}
	if task.DoneAt != nil {
		result += fmt.Sprintf("\nCompleted at: %s", task.DoneAt.Format("2006-01-02 15:04:05"))
	}

	return result
}
