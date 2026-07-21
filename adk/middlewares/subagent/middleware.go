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
	// The output file is line-oriented: one line per materialized AgentEvent,
	// appended as events arrive so a backgrounded run's interim output is visible
	// before it completes. EventFormat encodes each event into its line (see
	// AgentEventFormat) and therefore defines the file's actual format. When
	// EventFormat is nil, the default encoder writes one JSON object per line (JSONL):
	// {agent_name, message}, with the event's message (root Extra stripped) carrying
	// its own role and any tool calls/results — no separate type field. A custom
	// EventFormat may emit any per-line text and may skip events — e.g. skipping every
	// event but the final assistant answer to get a final-result-only file.
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

	backgroundEnabled := config.Background != nil && config.Background.Manager != nil

	// The background-run note now lives in the tool description (not the system prompt),
	// filled into the {back_ground_prompt} placeholder only when background is enabled.
	bgPrompt := ""
	if backgroundEnabled {
		bgPrompt = internal.SelectPrompt(internal.I18nPrompts{
			English: agentToolBackgroundPrompt,
			Chinese: agentToolBackgroundPromptChinese,
		})
	}
	desc, err = pyfmt.Fmt(desc, map[string]any{"back_ground_prompt": bgPrompt})
	if err != nil {
		return nil, err
	}

	// With a Manager, the tool exposes run_in_background and routes through the
	// Manager; without one it is a plain foreground spawn.
	var at tool.BaseTool
	if backgroundEnabled {
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

	// Build system prompt. The background-run note is no longer appended here; it now
	// lives in the tool description via the {back_ground_prompt} placeholder.
	var instruction string
	if config.SystemPrompt != nil {
		instruction = *config.SystemPrompt
	} else {
		instruction = internal.SelectPrompt(internal.I18nPrompts{
			English: agentToolPrompt,
			Chinese: agentToolPromptChinese,
		})
	}

	return &typedSubagentMiddleware[M]{
		tools:       tools,
		instruction: instruction,
		subAgents:   config.SubAgents,
	}, nil
}

type typedSubagentMiddleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]
	tools       []tool.BaseTool
	instruction string
	subAgents   []adk.TypedAgent[M]
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

const agentTypesReminderExtraKey = "__eino_subagent_available_agent_types__"

// buildAgentTypesSection renders the available-agent-types section. Returns ok=false
// when there are no sub-agents.
func (m *typedSubagentMiddleware[M]) buildAgentTypesSection(ctx context.Context) (string, bool) {
	if len(m.subAgents) == 0 {
		return "", false
	}
	preamble := internal.SelectPrompt(internal.I18nPrompts{
		English: availableAgentTypesPreamble,
		Chinese: availableAgentTypesPreambleChinese,
	})
	var sb strings.Builder
	sb.WriteString(preamble)
	for _, a := range m.subAgents {
		sb.WriteString(fmt.Sprintf("\n- %s: %s", a.Name(ctx), a.Description(ctx)))
	}
	return sb.String(), true
}

// BeforeModelRewriteState publishes the available agent types as a system
// reminder. Sub-agents can change between turns, so the list is rebuilt every
// invocation and a fresh reminder is appended whenever it changes (see
// upsertSystemReminder).
func (m *typedSubagentMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M], _ *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	if state == nil {
		return ctx, state, nil
	}
	section, ok := m.buildAgentTypesSection(ctx)
	if !ok {
		return ctx, state, nil
	}
	nState := *state
	nState.Messages = upsertSystemReminder(state.Messages, agentTypesReminderExtraKey, section)
	return ctx, &nState, nil
}

// upsertSystemReminder appends a fresh system reminder carrying section when it
// differs from the most recent reminder this middleware previously emitted.
// Reminders are identified by extraKey (each middleware owns its own key).
//
// Following the mid-conversation system message pattern, existing reminders are
// never mutated or removed: rewriting history would invalidate the model's KV
// cache for every message after the edit. Appending at the end keeps the prefix
// intact while ensuring the latest reminder reflects the current state — later
// reminders supersede earlier ones. When the content is unchanged nothing is
// added, so repeated invocations within a turn are idempotent.
func upsertSystemReminder[M adk.MessageType](messages []M, extraKey string, section string) []M {
	// Reverse scan: the reminder closest to the end reflects the last state this
	// middleware published.
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !isSystemMessage(msg) || !hasExtraKey(msg, extraKey) {
			continue
		}
		if systemMessageContent(msg) == section {
			// Unchanged since the last reminder: idempotent no-op.
			return messages
		}
		// Content changed (e.g. a sub-agent was added): stop scanning and append a
		// new reminder below rather than editing the stale one in place.
		break
	}
	result := make([]M, len(messages), len(messages)+1)
	copy(result, messages)
	return append(result, newSystemReminder[M](extraKey, section))
}

func isSystemMessage[M adk.MessageType](msg M) bool {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Role == schema.System
	case *schema.AgenticMessage:
		return v.Role == schema.AgenticRoleTypeSystem
	}
	return false
}

func hasExtraKey[M adk.MessageType](msg M, extraKey string) bool {
	switch v := any(msg).(type) {
	case *schema.Message:
		_, ok := v.Extra[extraKey]
		return ok
	case *schema.AgenticMessage:
		_, ok := v.Extra[extraKey]
		return ok
	}
	return false
}

// systemMessageContent returns the text carried by a reminder created via
// newSystemReminder, used to detect whether the section changed since the last
// reminder. It mirrors newSystemReminder's single-text-block layout.
func systemMessageContent[M adk.MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Content
	case *schema.AgenticMessage:
		if len(v.ContentBlocks) == 1 {
			if b := v.ContentBlocks[0]; b != nil && b.UserInputText != nil {
				return b.UserInputText.Text
			}
		}
	}
	return ""
}

func newSystemReminder[M adk.MessageType](extraKey string, content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		msg := schema.SystemMessage(content)
		msg.Extra = map[string]any{extraKey: true}
		return any(msg).(M)
	case *schema.AgenticMessage:
		msg := schema.SystemAgenticMessage(content)
		msg.Extra = map[string]any{extraKey: true}
		return any(msg).(M)
	}
	panic("unreachable")
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
