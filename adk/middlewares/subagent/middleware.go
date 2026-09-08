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
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/middlewares/internal/systemreminder"
	"github.com/cloudwego/eino/adk/task/background"
	backgroundlocal "github.com/cloudwego/eino/adk/task/local"
	durablesubagent "github.com/cloudwego/eino/adk/task/subagent"
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

	// CustomFormatReminder customizes the mid-conversation system reminder that advertises
	// the available agent types. When nil, the default reminder is used. Returning an error
	// aborts construction; returning a nil output keeps the default reminder; returning an
	// output whose Reminder is "" suppresses the reminder message entirely.
	CustomFormatReminder func(ctx context.Context, in *FormatReminderInput[M]) (*FormatReminderOutput, error)

	// Tasks configures task execution for sub-agent runs. When nil,
	// only foreground (blocking) agent execution is available and runs are NOT
	// tracked. See TaskConfig.
	Tasks *TypedTaskConfig[M]
}

// FormatReminderInput is the input to TypedConfig.CustomFormatReminder.
type FormatReminderInput[M adk.MessageType] struct {
	// SubAgents are the sub-agents advertised by the reminder.
	SubAgents []adk.TypedAgent[M]
}

// FormatReminderOutput is the result of TypedConfig.CustomFormatReminder.
type FormatReminderOutput struct {
	// Reminder is the reminder text to insert. An empty string suppresses the reminder message.
	Reminder string
}

// TaskConfig enables background-task execution for the standard
// *schema.Message agent tool.
type TaskConfig = TypedTaskConfig[*schema.Message]

// TypedTaskConfig enables background-task execution for the agent tool.
//
// When set, the Agent tool gains a run_in_background parameter. Local mode
// manages all runs through its Runner. Durable Controller execution keeps
// foreground work parent-owned until its completion barrier requests handoff.
type TypedTaskConfig[M adk.MessageType] struct {
	Local   *TypedLocalTaskConfig[M]
	Durable *TypedDurableTaskConfig[M]
	// TranscriptFormat formats one materialized sub-agent message for both local
	// output persistence and durable task_output session views.
	TranscriptFormat TranscriptFormat[M]
}

type LocalTaskConfig = TypedLocalTaskConfig[*schema.Message]

// TypedLocalTaskConfig configures process-local managed sub-agent runs.
type TypedLocalTaskConfig[M adk.MessageType] struct {
	Runner      *backgroundlocal.Runner
	OutputStore filesystem.AppendOpener
	OutputDir   string
	// EventPersister receives the original AgentEvent metadata. For a streaming
	// Sub-agent event, Stream is an independent persistence-owned copy; otherwise
	// Stream is nil. Nil uses TranscriptFormat.
	EventPersister background.TaskEventPersister[*adk.TypedAgentEvent[M], M]
}

type DurableTaskConfig = TypedDurableTaskConfig[*schema.Message]

// TypedDurableTaskConfig configures reconstructable sub-agent runs.
type TypedDurableTaskConfig[M adk.MessageType] struct {
	// Runtime owns foreground execution and background handoff.
	Runtime *durablesubagent.Controller[M]
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
// The middleware injects an Agent tool for spawning sub-agents. When Tasks
// is configured, agent runs are tracked by the shared background-task Manager and
// the Agent tool gains a run_in_background parameter. The task_output/task_stop
// control tools are NOT injected here; wire the task control middleware
// (adk/middlewares/task) once, bound to the same Manager.
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
	if config.Tasks != nil {
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
	if config.Tasks != nil {
		if config.Tasks.Local != nil {
			at, err = newManagedAgentTool[M](
				config.Tasks.Local.Runner, subAgentToolMap,
				agentOutput[M]{
					store:     config.Tasks.Local.OutputStore,
					outputDir: config.Tasks.Local.OutputDir,
					format:    config.Tasks.TranscriptFormat,
					persister: config.Tasks.Local.EventPersister,
				},
				toolName, desc,
			)
		} else {
			at, err = newDurableAgentTool[M](ctx, config.Tasks.Durable, config.SubAgents, toolName, desc)
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
	if config.CustomFormatReminder != nil {
		out, ferr := config.CustomFormatReminder(ctx, &FormatReminderInput[M]{SubAgents: config.SubAgents})
		if ferr != nil {
			return nil, fmt.Errorf("subagent middleware: CustomFormatReminder failed: %w", ferr)
		}
		if out != nil {
			reminder = out.Reminder
		}
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
	if c.Tasks != nil {
		if (c.Tasks.Local == nil) == (c.Tasks.Durable == nil) {
			return fmt.Errorf("subagent: exactly one of Tasks.Local or Tasks.Durable is required")
		}
		if c.Tasks.Local != nil && c.Tasks.Local.Runner == nil {
			return fmt.Errorf("subagent: local background Runner is required")
		}
		if c.Tasks.Durable != nil {
			if c.Tasks.Durable.Runtime == nil {
				return fmt.Errorf("subagent: durable Controller is required")
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
