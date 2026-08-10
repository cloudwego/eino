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
	"strings"

	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	backgroundlocal "github.com/cloudwego/eino/adk/backgroundtask/local"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/middlewares/internal/systemreminder"
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
	Background *TypedBackgroundConfig[M]
}

// BackgroundConfig enables background-task execution for the standard
// *schema.Message agent tool.
type BackgroundConfig = TypedBackgroundConfig[*schema.Message]

// TypedBackgroundConfig enables background-task execution for the agent tool.
//
// When set, ALL agent runs (foreground and background) are managed by the Manager,
// making them visible via Get/List, and the Agent tool gains a run_in_background
// parameter.
type TypedBackgroundConfig[M adk.MessageType] struct {
	Local   *TypedLocalBackgroundConfig[M]
	Durable *TypedDurableBackgroundConfig[M]
	// TranscriptFormat formats one materialized sub-agent message for both local
	// output persistence and durable task_output session views.
	TranscriptFormat TranscriptFormat[M]
}

type LocalBackgroundConfig = TypedLocalBackgroundConfig[*schema.Message]

// TypedLocalBackgroundConfig configures process-local managed sub-agent runs.
type TypedLocalBackgroundConfig[M adk.MessageType] struct {
	Runner      *backgroundlocal.Runner
	OutputStore filesystem.AppendOpener
	OutputDir   string
}

type DurableBackgroundConfig = TypedDurableBackgroundConfig[*schema.Message]

// TypedDurableBackgroundConfig configures reconstructable sub-agent runs.
type TypedDurableBackgroundConfig[M adk.MessageType] struct {
	Manager   *backgroundtask.Manager
	Executors *backgroundtask.ExecutorRegistry
	// Executor owns the durable session dependencies and must be the same
	// instance on every middleware sharing Manager.
	Executor             *durablesubagent.Executor[M]
	ForegroundTimeoutMs  *int
	ShouldAutoBackground func(context.Context, *backgroundtask.Task) bool
	// RunOptionsFactories reconstructs deployment-owned run options by sub-agent
	// name for every execution attempt. Every worker serving a name must configure
	// a semantically equivalent factory for the full lifetime of resumable tasks.
	// Incompatible changes require draining those tasks or using a new sub-agent name.
	RunOptionsFactories map[string]durablesubagent.RunOptionsFactory
}

// TranscriptFormat formats one materialized sub-agent message as one transcript
// record. Returning an empty string skips the message. It may be called
// concurrently and must not mutate the message.
type TranscriptFormat[M adk.MessageType] func(
	ctx context.Context,
	agentName string,
	message M,
) (string, error)

// New creates a ChatModelAgentMiddleware that injects sub-agent tools into the agent context.
//
// The middleware injects an Agent tool for spawning sub-agents. When Background
// is configured, agent runs are tracked by the shared background-task Manager and
// the Agent tool gains a run_in_background parameter. The task_output/task_stop
// control tools are NOT injected here; wire the backgroundtask control middleware
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

	backgroundPrompt := ""
	if config.Background != nil {
		backgroundPrompt = internal.SelectPrompt(internal.I18nPrompts{
			English: agentToolBackgroundPrompt,
			Chinese: agentToolBackgroundPromptChinese,
		})
	}
	desc, err = pyfmt.Fmt(desc, map[string]any{"background_prompt": backgroundPrompt})
	if err != nil {
		return nil, err
	}

	// With a Manager, the tool exposes run_in_background and routes through the
	// Manager; without one it is a plain foreground spawn.
	var at tool.BaseTool
	if config.Background != nil {
		if config.Background.Local != nil {
			at, err = newManagedAgentTool[M](
				config.Background.Local.Runner, subAgentToolMap,
				agentOutput[M]{
					store:     config.Background.Local.OutputStore,
					outputDir: config.Background.Local.OutputDir,
					format:    config.Background.TranscriptFormat,
				},
				toolName, desc,
			)
		} else {
			at, err = newDurableAgentTool[M](ctx, config.Background.Durable, config.SubAgents, toolName, desc)
		}
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
	}

	entries := make([]agentTypeEntry, 0, len(config.SubAgents))
	for _, agent := range config.SubAgents {
		entries = append(entries, agentTypeEntry{
			Name:        agent.Name(ctx),
			Description: agent.Description(ctx),
		})
	}
	reminder := ""
	if len(entries) > 0 {
		reminder = buildAgentTypesSectionFromEntries(entries)
	}

	return &typedSubagentMiddleware[M]{
		tools:       tools,
		reminder:    reminder,
		instruction: instruction,
	}, nil
}

type typedSubagentMiddleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]
	tools       []tool.BaseTool
	instruction string
	reminder    string
}

// BeforeAgent injects sub-agent tools and instructions into the agent context.
func (m *typedSubagentMiddleware[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	nRunCtx.Instruction += "\n\n" + m.instruction
	nRunCtx.Tools = append(nRunCtx.Tools, m.tools...)
	return ctx, &nRunCtx, nil
}

func (m *typedSubagentMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M], _ *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	if state == nil || m.reminder == "" {
		return ctx, state, nil
	}
	state.Messages = systemreminder.NormalizeReminderRoles(state.Messages)
	if !systemreminder.Has(state.Messages, agentTypesReminderExtraKey) {
		state.Messages = systemreminder.Insert(ctx, state.Messages, agentTypesReminderExtraKey, m.reminder, nil)
	}
	return ctx, state, nil
}

const agentTypesReminderExtraKey = "__eino_subagent_available_agent_types__"

func buildAgentTypesSectionFromEntries(entries []agentTypeEntry) string {
	preamble := internal.SelectPrompt(internal.I18nPrompts{
		English: availableAgentTypesPreamble,
		Chinese: availableAgentTypesPreambleChinese,
	})
	var builder strings.Builder
	builder.WriteString(preamble)
	for _, entry := range entries {
		_, _ = fmt.Fprintf(&builder, "\n- %s: %s", entry.Name, entry.Description)
	}
	return builder.String()
}

type agentTypeEntry struct {
	Name        string
	Description string
}

func validate[M adk.MessageType](ctx context.Context, c *TypedConfig[M]) error {
	if c == nil {
		return fmt.Errorf("subagent: config is required")
	}
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
	if c.Background != nil {
		if (c.Background.Local == nil) == (c.Background.Durable == nil) {
			return fmt.Errorf("subagent: exactly one of Background.Local or Background.Durable is required")
		}
		if c.Background.Local != nil && c.Background.Local.Runner == nil {
			return fmt.Errorf("subagent: local background Runner is required")
		}
		if c.Background.Durable != nil {
			if c.Background.Durable.Manager == nil ||
				c.Background.Durable.Executors == nil ||
				c.Background.Durable.Executor == nil {
				return fmt.Errorf(
					"subagent: durable background Manager, executor registry, and Executor are required",
				)
			}
			for _, agent := range c.SubAgents {
				if _, ok := agent.(adk.TypedResumableAgent[M]); !ok {
					return fmt.Errorf("subagent: durable agent %q is not resumable", agent.Name(ctx))
				}
			}
		}
	}

	return nil
}
