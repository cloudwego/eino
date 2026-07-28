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
	"encoding/json"
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

// TypedConfig configures the background-task control middleware, parameterized by
// message type.
type TypedConfig[M adk.MessageType] struct {
	// Manager is the shared background-task Manager whose tasks the injected
	// task_output/task_stop tools inspect and cancel. Required.
	//
	// It is typically the same Manager the domain middlewares (subagent, filesystem)
	// were given, so a single task-ID space spans agent and shell runs.
	Manager *bgtask.Manager
	// Authorize runs before a control can reveal task existence or mutate state.
	// Hosts derive principals and tenancy from ctx; core task records remain neutral.
	Authorize AuthorizeFunc
	// ManagerScopeIsolated explicitly asserts that the injected tools and Manager
	// task-ID space are restricted to one already-authorized caller scope. Set this
	// only when no per-call Authorize hook is necessary.
	ManagerScopeIsolated bool

	// TaskOutputToolConfig configures the task_output tool. Optional.
	TaskOutputToolConfig *ToolConfig
	// TaskStopToolConfig configures the task_stop tool. Optional.
	TaskStopToolConfig *ToolConfig
}

type ControlOperation string

const (
	ControlRead ControlOperation = "read"
	ControlStop ControlOperation = "stop"
)

type AuthorizeFunc func(ctx context.Context, operation ControlOperation, taskID string) error

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
	if config.Authorize == nil && !config.ManagerScopeIsolated {
		return nil, fmt.Errorf("backgroundtask: Authorize or isolated Manager scope is required")
	}
	mgr := config.Manager

	outputEnabled := !disabled(config.TaskOutputToolConfig)
	stopEnabled := !disabled(config.TaskStopToolConfig)

	var tools []tool.BaseTool
	if outputEnabled {
		outputTool, err := newTaskOutputTool(mgr, config.Authorize, config.TaskOutputToolConfig)
		if err != nil {
			return nil, fmt.Errorf("backgroundtask: failed to create task_output tool: %w", err)
		}
		tools = append(tools, outputTool)
	}
	if stopEnabled {
		stopTool, err := newTaskStopTool(mgr, config.Authorize, config.TaskStopToolConfig)
		if err != nil {
			return nil, fmt.Errorf("backgroundtask: failed to create task_stop tool: %w", err)
		}
		tools = append(tools, stopTool)
	}

	instruction := buildInstruction(config.TaskOutputToolConfig, outputEnabled, config.TaskStopToolConfig, stopEnabled)

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
	Block         *bool `json:"block,omitempty" jsonschema_description:"Whether to wait for the task to complete. Defaults to true; set false to return the current status immediately."`
	Timeout       int   `json:"timeout,omitempty" jsonschema_description:"Maximum time to wait in milliseconds when blocking. Defaults to 30000; capped at 600000."`
	AfterSequence int64 `json:"after_sequence,omitempty" jsonschema_description:"Exclusive update cursor. Defaults to zero."`
	Limit         int   `json:"limit,omitempty" jsonschema_description:"Maximum updates to return. Defaults to 100."`
}

const (
	defaultTaskOutputTimeoutMs = 30000
	maxTaskOutputTimeoutMs     = 600000
)

func newTaskOutputTool(mgr *bgtask.Manager, authorize AuthorizeFunc, cfg *ToolConfig) (tool.InvokableTool, error) {
	name := selectToolName(cfg, taskOutputToolName)
	desc := selectToolDesc(cfg, taskOutputToolDescription, taskOutputToolDescriptionChinese)
	return utils.InferTool(name, desc, func(ctx context.Context, input taskOutputInput) (string, error) {
		if authorize != nil {
			if err := authorize(ctx, ControlRead, input.TaskID); err != nil {
				return "Task access denied", nil
			}
		}
		if task, err := mgr.GetTask(ctx, input.TaskID); err == nil {
			return resolveDurableTask(ctx, mgr, task, input)
		} else if errors.Is(err, bgtask.ErrNotFound) {
			return fmt.Sprintf("Task %q not found", input.TaskID), nil
		} else {
			return "", err
		}
	})
}

type taskOutputResponse struct {
	Task         *bgtask.Task      `json:"task"`
	Updates      []*bgtask.Update  `json:"updates"`
	NextSequence int64             `json:"next_sequence"`
	Result       *bgtask.ResultRef `json:"result,omitempty"`
}

func resolveDurableTask(ctx context.Context, mgr *bgtask.Manager, task *bgtask.Task, input taskOutputInput) (string, error) {
	limit := input.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	updates, err := mgr.ListTaskUpdates(ctx, &bgtask.ListTaskUpdatesRequest{
		TaskID: input.TaskID, AfterSequence: input.AfterSequence, Limit: limit,
	})
	if err != nil {
		return "", err
	}
	// Refresh after listing so the snapshot cannot be older than updates returned
	// by the same response.
	task, err = mgr.GetTask(ctx, input.TaskID)
	if err != nil {
		return "", err
	}
	block := input.Block == nil || *input.Block
	if block && len(updates.Updates) == 0 && !isTerminal(task.Status) {
		timeout := input.Timeout
		if timeout <= 0 {
			timeout = defaultTaskOutputTimeoutMs
		}
		if timeout > maxTaskOutputTimeoutMs {
			timeout = maxTaskOutputTimeoutMs
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer cancel()
		updates, err = mgr.WaitTaskUpdates(waitCtx, &bgtask.WaitTaskUpdatesRequest{
			TaskID: input.TaskID, AfterSequence: input.AfterSequence, Limit: limit,
		})
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return "", err
		}
		task, _ = mgr.GetTask(ctx, input.TaskID)
	}
	response := taskOutputResponse{Task: task, Updates: updates.Updates, NextSequence: updates.NextSequence}
	if task != nil && task.Status == bgtask.StatusCompleted {
		response.Result = task.ResultRef
	}
	data, err := json.Marshal(response)
	return string(data), err
}

func isTerminal(status bgtask.Status) bool {
	return status == bgtask.StatusCompleted || status == bgtask.StatusFailed || status == bgtask.StatusCanceled
}

type taskStopInput struct {
	TaskID string `json:"task_id" jsonschema:"required" jsonschema_description:"The ID of the background task to stop"`
}

func newTaskStopTool(mgr *bgtask.Manager, authorize AuthorizeFunc, cfg *ToolConfig) (tool.InvokableTool, error) {
	name := selectToolName(cfg, taskStopToolName)
	desc := selectToolDesc(cfg, taskStopToolDescription, taskStopToolDescriptionChinese)
	return utils.InferTool(name, desc, func(ctx context.Context, input taskStopInput) (string, error) {
		if authorize != nil {
			if err := authorize(ctx, ControlStop, input.TaskID); err != nil {
				return "Task access denied", nil
			}
		}
		if _, err := mgr.GetTask(ctx, input.TaskID); err != nil && !errors.Is(err, bgtask.ErrNotFound) {
			return "", err
		}
		task, err := mgr.RequestCancel(ctx, input.TaskID)
		if err != nil {
			return fmt.Sprintf("Failed to stop task %q: %s", input.TaskID, err.Error()), nil
		}
		return fmt.Sprintf("Stop requested for task %s (status: %s)", input.TaskID, task.Status), nil
	})
}
