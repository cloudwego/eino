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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Config configures the subagent middleware for the standard *schema.Message message type.
// It is the default specialization of TypedConfig.
type Config = TypedConfig[*schema.Message]

// TypedConfig configures the subagent middleware, parameterized by message type.
type TypedConfig[M adk.MessageType] struct {
	// SubAgents is the list of agents available for spawning.
	// Each agent must have a unique name. Required.
	SubAgents []adk.TypedAgent[M]

	// ToolName overrides the name of the agent-spawning tool.
	// When empty, defaults to "agent".
	ToolName string

	// ToolDescriptionGenerator overrides the default agent tool description generator.
	// The generator receives the list of sub-agents and should return a complete tool
	// description string. When nil, defaultAgentToolDescription is used.
	ToolDescriptionGenerator func(ctx context.Context, subAgents []adk.TypedAgent[M]) (string, error)

	// SystemPrompt overrides the default system prompt injected by BeforeAgent.
	// When nil, the built-in prompt (with i18n support) is used.
	// Defined as *string because an empty string may be an intentional user value.
	SystemPrompt *string

	// TaskMgr is an optional TaskMgr for background task support.
	// When nil, only foreground (blocking) agent execution is available, and agent runs
	// are NOT tracked. When set, ALL agent runs (foreground and background) are managed
	// by the TaskMgr, making them visible via Get/List/Notifications. The middleware also
	// injects TaskOutput and TaskStop tools, and the Agent tool gains a run_in_background parameter.
	TaskMgr *TypedTaskMgr[M]
}

// New creates a ChatModelAgentMiddleware that injects sub-agent tools into the agent context.
//
// The middleware injects an Agent tool for spawning sub-agents. When Config.TaskMgr is
// provided, it also injects TaskOutput and TaskStop tools for background task management.
//
// The middleware uses context-based recursion prevention: when the parent agent's BeforeAgent
// sets a context marker, sub-agents created through this middleware will not have the sub-agent
// tools re-injected, preventing infinite nesting.
func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped[*schema.Message](ctx, config)
}

// NewTyped creates a TypedChatModelAgentMiddleware that injects sub-agent tools into the
// agent context, parameterized by message type. See New for behavior details.
func NewTyped[M adk.MessageType](ctx context.Context, config *TypedConfig[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	if err := validate(ctx, config); err != nil {
		return nil, err
	}

	// Build sub-agent map: name → InvokableTool (for non-TaskMgr foreground path).
	subAgentMap := make(map[string]tool.InvokableTool, len(config.SubAgents))
	for _, a := range config.SubAgents {
		name := a.Name(ctx)
		bt := adk.NewTypedAgentTool[M](ctx, a)
		it, ok := bt.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("subagent: agent %q does not implement InvokableTool", name)
		}
		subAgentMap[name] = it

		// Register agents into TaskMgr for Run to resolve by name.
		if config.TaskMgr != nil {
			config.TaskMgr.RegisterAgent(name, a)
		}
	}

	enableRunInBackground := config.TaskMgr != nil

	toolName := config.ToolName
	if toolName == "" {
		toolName = agentToolName
	}

	descGen := defaultAgentToolDescription[M]
	if config.ToolDescriptionGenerator != nil {
		descGen = config.ToolDescriptionGenerator
	}

	at := &typedAgentTool[M]{
		name:                  toolName,
		subAgents:             subAgentMap,
		subAgentSlice:         config.SubAgents,
		descGen:               descGen,
		mgr:                   config.TaskMgr,
		enableRunInBackground: enableRunInBackground,
	}

	var tools []tool.BaseTool
	tools = append(tools, at)

	if enableRunInBackground {
		outputTool, err := newTaskOutputTool(config.TaskMgr)
		if err != nil {
			return nil, fmt.Errorf("subagent: failed to create task_output tool: %w", err)
		}
		stopTool, err := newTaskStopTool(config.TaskMgr)
		if err != nil {
			return nil, fmt.Errorf("subagent: failed to create task_stop tool: %w", err)
		}
		tools = append(tools, outputTool, stopTool)
	}

	// Build system prompt.
	var instruction string
	if config.SystemPrompt != nil {
		instruction = *config.SystemPrompt
	} else {
		instruction = internal.SelectPrompt(internal.I18nPrompts{
			English: agentToolPrompt,
			Chinese: agentToolPromptChinese,
		})
		if enableRunInBackground {
			instruction += internal.SelectPrompt(internal.I18nPrompts{
				English: agentToolBackgroundPrompt,
				Chinese: agentToolBackgroundPromptChinese,
			})
		}
	}

	return &typedSubagentMiddleware[M]{
		tools:       tools,
		instruction: instruction,
	}, nil
}

type typedSubagentMiddleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]
	tools       []tool.BaseTool
	instruction string
}

// BeforeAgent injects sub-agent tools and instructions into the agent context.
func (m *typedSubagentMiddleware[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	nRunCtx.Instruction += "\n" + m.instruction
	nRunCtx.Tools = append(nRunCtx.Tools, m.tools...)
	return ctx, &nRunCtx, nil
}

func validate[M adk.MessageType](ctx context.Context, c *TypedConfig[M]) error {
	if len(c.SubAgents) == 0 {
		return fmt.Errorf("subagent: SubAgents must not be empty")
	}

	names := make(map[string]struct{}, len(c.SubAgents))
	for _, a := range c.SubAgents {
		name := a.Name(ctx)
		if _, exists := names[name]; exists {
			return fmt.Errorf("subagent: duplicate agent name %q", name)
		}
		names[name] = struct{}{}
	}

	return nil
}
