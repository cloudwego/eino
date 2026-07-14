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
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/filesystem"
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

	// Background configures background-task execution for sub-agent runs. When nil,
	// only foreground (blocking) agent execution is available and runs are NOT
	// tracked. See BackgroundConfig.
	Background *BackgroundConfig[M]
}

// BackgroundConfig enables background-task execution for the agent tool.
//
// When set, ALL agent runs (foreground and background) are managed by the Manager,
// making them visible via Get/List, and the Agent tool gains a run_in_background
// parameter.
type BackgroundConfig[M adk.MessageType] struct {
	// Manager is the shared background-task Manager. Required (a nil Manager is the
	// same as no BackgroundConfig). It may be shared with other middlewares (e.g.
	// filesystem) so a single task-ID space spans agent and shell runs. The
	// task_output/task_stop control tools are NOT injected here; wire the
	// backgroundtask control middleware (adk/middlewares/backgroundtask) once, bound
	// to the same Manager.
	Manager *backgroundtask.Manager

	// OutputStore and OutputDir, when both set, give every managed sub-agent run an
	// output file at OutputDir/<id>.output and record the path on Task.OutputFile, so
	// a backgrounded run's output is retrievable by path (and large results need not
	// be inlined). The path is allocated before the run and the file is created
	// lazily by the work callback, so a newly returned background task may briefly
	// advertise the path before it exists. The Manager itself never writes.
	//
	// The file is JSON Lines: one record per line, appended as each AgentEvent
	// materializes, so a backgrounded run's interim output is visible before it
	// completes. EventFormat encodes each event into its line (see AgentEventFormat);
	// when nil the default encoder is used, which writes {"type","agent_name",
	// "message"} with the event's message (root Extra stripped) and a "type" naming
	// the event kind. A custom EventFormat may reshape or skip events — e.g. skipping
	// everything but the final assistant answer to get a final-result-only file.
	//
	// OutputStore is a filesystem.AppendOpener (filesystem.InMemoryBackend
	// implements it); output files require one. When either is unset, runs have no
	// output file.
	OutputStore filesystem.AppendOpener
	OutputDir   string
	EventFormat AgentEventFormat[M]
}

// AgentEventFormat encodes one materialized AgentEvent into the text of a single
// output-file line (the framework appends the newline). It runs once per event, on
// the run's Recv stack (serially, single-consumer), so it needs no synchronization.
// ctx is the run's (detached) context; honor it for cancellation and read request
// values from it as needed.
//
// Returns:
//   - (line, nil) with line != "": the line is written, followed by a newline.
//   - ("", nil): skip — the event contributes no line. Skipping every event but the
//     final answer yields a final-result-only file.
//   - (_, err): the write is abandoned and the output file is marked unreliable, so
//     task_output reports the file's failed state instead of trusting a partial file.
type AgentEventFormat[M adk.MessageType] func(ctx context.Context, event *adk.TypedAgentEvent[M]) (string, error)

// outputFileFormatHint is the human-readable description of the default encoder's
// output, surfaced to the launcher in the managed agent tool's background-run message
// so the reader knows to interpret the file as JSONL.
const outputFileFormatHint = `JSONL — one JSON object per line, each a materialized event {agent_name, message}; the message carries its own role and any tool calls/results.`

// New creates a ChatModelAgentMiddleware that injects sub-agent tools into the agent context.
//
// The middleware injects an Agent tool for spawning sub-agents. When Config.Manager is
// provided, agent runs are tracked by the shared background-task Manager and the Agent
// tool gains a run_in_background parameter. The task_output/task_stop control tools are
// NOT injected here; wire the backgroundtask control middleware
// (adk/middlewares/backgroundtask) once, bound to the same Manager.
func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped[*schema.Message](ctx, config)
}

// NewTyped creates a TypedChatModelAgentMiddleware that injects sub-agent tools into the
// agent context, parameterized by message type. See New for behavior details.
func NewTyped[M adk.MessageType](ctx context.Context, config *TypedConfig[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	if err := validate(ctx, config); err != nil {
		return nil, err
	}

	// Build subAgentToolMap: name → the agent-as-tool adapter that runs the agent.
	// Both the foreground and the Manager-backed paths invoke this same adapter.
	subAgentToolMap := make(map[string]tool.InvokableTool, len(config.SubAgents))
	for _, a := range config.SubAgents {
		name := a.Name(ctx)
		bt := adk.NewTypedAgentTool[M](ctx, a)
		it, ok := bt.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("subagent: agent %q does not implement InvokableTool", name)
		}
		subAgentToolMap[name] = it
	}

	toolName := config.ToolName
	if toolName == "" {
		toolName = agentToolName
	}

	descGen := defaultAgentToolDescription[M]
	if config.ToolDescriptionGenerator != nil {
		descGen = config.ToolDescriptionGenerator
	}
	// The sub-agent set is fixed at construction, so the description is computed once.
	desc, err := descGen(ctx, config.SubAgents)
	if err != nil {
		return nil, err
	}

	// With a Manager, the tool exposes run_in_background and routes through the
	// Manager; without one it is a plain foreground spawn.
	var at tool.BaseTool
	if config.Background != nil && config.Background.Manager != nil {
		at, err = newManagedAgentTool[M](config.Background.Manager, subAgentToolMap, agentOutput[M]{
			store:     config.Background.OutputStore,
			outputDir: config.Background.OutputDir,
			format:    config.Background.EventFormat,
		}, toolName, desc)
	} else {
		at, err = newAgentTool(subAgentToolMap, toolName, desc)
	}
	if err != nil {
		return nil, err
	}

	tools := []tool.BaseTool{at}

	// Build system prompt.
	var instruction string
	if config.SystemPrompt != nil {
		instruction = *config.SystemPrompt
	} else {
		instruction = internal.SelectPrompt(internal.I18nPrompts{
			English: agentToolPrompt,
			Chinese: agentToolPromptChinese,
		})
		if config.Background != nil && config.Background.Manager != nil {
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
