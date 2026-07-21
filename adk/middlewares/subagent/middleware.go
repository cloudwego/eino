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

	// DisableMidConversationSystemMessage controls the role of the mid-conversation
	// available-agent-types reminder injected by BeforeModelRewriteState. When false
	// (the default) the reminder is a system message, matching the historical behavior.
	// Set it to true for models that reject mid-conversation system messages: the
	// reminder is then wrapped as a user message placed immediately before the latest
	// turn's user-input message. The setting is read every turn, so it may be changed
	// mid-session (e.g. after a model swap); prior reminders are re-aligned to the
	// current role in place.
	DisableMidConversationSystemMessage bool
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
		tools:          tools,
		instruction:    instruction,
		subAgents:      config.SubAgents,
		reminderAsUser: config.DisableMidConversationSystemMessage,
	}, nil
}

type typedSubagentMiddleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]
	tools          []tool.BaseTool
	instruction    string
	subAgents      []adk.TypedAgent[M]
	reminderAsUser bool
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

const agentTypesReminderExtraKey = "__eino_subagent_available_agent_types__"

func buildAgentTypesSectionFromEntries(entries []reminderEntry) string {
	preamble := internal.SelectPrompt(internal.I18nPrompts{
		English: availableAgentTypesPreamble,
		Chinese: availableAgentTypesPreambleChinese,
	})
	var sb strings.Builder
	sb.WriteString(preamble)
	for _, entry := range entries {
		sb.WriteString(fmt.Sprintf("\n- %s: %s", entry.Name, entry.Description))
	}
	return sb.String()
}

// BeforeModelRewriteState publishes the available agent types as a system
// reminder. Sub-agents can change between turns, so the list is rebuilt every
// invocation and a fresh reminder is appended whenever it changes (see
// appendReminderIfChanged).
//
// When a reminder is appended, it is also emitted as a durable MessageInserted
// session event so it is reconstructed in place on the next turn instead of
// being dropped from the event log and re-injected at a new offset (which would
// break cross-turn prefix caching). TypedSendEvent is a no-op outside a Runner
// session.
func (m *typedSubagentMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M], _ *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	if state == nil {
		return ctx, state, nil
	}
	if len(m.subAgents) == 0 {
		return ctx, state, nil
	}
	entries := make([]reminderEntry, 0, len(m.subAgents))
	for _, a := range m.subAgents {
		entries = append(entries, reminderEntry{Name: a.Name(ctx), Description: a.Description(ctx)})
	}
	section := buildAgentTypesSectionFromEntries(entries)
	nState := *state
	// Re-align the role of prior reminders to the current setting, in case the
	// reminder role was switched mid-session (e.g. after a model swap). This is
	// applied only in-memory and recomputed every turn — it is not persisted.
	msgs := migrateReminderRole(state.Messages, agentTypesReminderExtraKey, m.reminderAsUser)
	newMsgs, insertedMsg, beforeMessageID, didInsert := appendReminderIfChanged(msgs, agentTypesReminderExtraKey, section, entries, buildAgentTypesSectionFromEntries, m.reminderAsUser)
	nState.Messages = newMsgs

	if didInsert {
		_ = adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{
			SessionEventVariant: &adk.SessionEventVariant[M]{
				Event: &adk.SessionEvent[M]{
					Kind: adk.SessionEventMessageInserted,
					MessageInserted: &adk.MessageInsertedEvent[M]{
						Message:         insertedMsg,
						BeforeMessageID: beforeMessageID,
					},
				},
			},
		})
	}

	return ctx, &nState, nil
}

// migrateReminderRole re-aligns the role of this middleware's previously inserted
// reminders (identified by extraKey) to the current setting, so a mid-session switch
// of the reminder role also updates reminders accumulated in earlier turns. When
// asUser is true reminders are aligned to user role, otherwise to system role.
//
// It rewrites only in the returned slice: each mismatched reminder is replaced with
// a fresh message carrying the same content and message ID but the target role,
// leaving the original (persisted) message objects untouched. The change is applied
// in-memory only — it is recomputed from the reconstructed messages every turn and
// never emitted as a session event. Returns the input slice unchanged when nothing
// needs migrating.
func migrateReminderRole[M adk.MessageType](messages []M, extraKey string, asUser bool) []M {
	var result []M
	for i, msg := range messages {
		if !hasExtraKey(msg, extraKey) || reminderIsUser(msg) == asUser {
			continue
		}
		if result == nil {
			result = make([]M, len(messages))
			copy(result, messages)
		}
		replacement := newReminder[M](extraKey, reminderContent(msg), asUser)
		setReminderMessageID(replacement, adk.GetMessageID(msg))
		result[i] = replacement
	}
	if result == nil {
		return messages
	}
	return result
}

type reminderEntry struct {
	Name        string
	Description string
}

// appendReminderIfChanged inserts a fresh reminder carrying changed entries when
// they differ from entries present in prior reminders from this middleware.
// Reminders are identified by extraKey (each middleware owns its own key) regardless
// of role. It returns the resulting messages plus, when a reminder was inserted, the
// inserted message, the ID of the message it was inserted before (empty when it
// lands at the tail), and didInsert=true — so the caller can persist the reminder as
// a MessageInserted session event that reconstructs at the same spot.
//
// Placement depends on role. In system mode (asUser=false) the reminder is placed
// at a clean turn boundary — right after the latest user message or final assistant
// answer (see isReminderAnchor) — rather than at the absolute tail, so it never lands
// in the middle of pending tool-call/tool-result scaffolding; a stale reminder already
// sitting there is superseded by placing the fresh one after it. In user mode
// (asUser=true) the reminder is a user message placed immediately before the latest
// turn's user-input message.
//
// Following the mid-conversation reminder pattern, existing reminders are never
// mutated or removed here: rewriting history would invalidate the model's KV cache
// for every message after the edit. Inserting keeps the prefix intact while ensuring
// the latest reminder reflects the current state — later reminders add or refresh
// only changed entries. When every current entry already appears in prior reminders
// with the same name and description, nothing is added, so repeated invocations
// within a turn are idempotent.
func appendReminderIfChanged[M adk.MessageType](messages []M, extraKey string, section string, entries []reminderEntry, buildSection func([]reminderEntry) string, asUser bool) (result []M, insertedMsg M, beforeMessageID string, didInsert bool) {
	latestByName := make(map[string]string, len(entries))
	for _, msg := range messages {
		if !hasExtraKey(msg, extraKey) {
			continue
		}
		for _, entry := range parseReminderEntries(reminderContent(msg)) {
			latestByName[entry.Name] = entry.Description
		}
	}

	changedEntries := make([]reminderEntry, 0, len(entries))
	for _, entry := range entries {
		if desc, ok := latestByName[entry.Name]; ok && desc == entry.Description {
			continue
		}
		changedEntries = append(changedEntries, entry)
	}
	if len(changedEntries) == 0 {
		return messages, insertedMsg, "", false
	}
	if len(changedEntries) < len(entries) {
		section = buildSection(changedEntries)
	}

	insertAt := reminderInsertIndex(messages, asUser)

	insertedMsg = newReminder[M](extraKey, section, asUser)
	adk.EnsureMessageID(insertedMsg)

	result = make([]M, 0, len(messages)+1)
	result = append(result, messages[:insertAt]...)
	result = append(result, insertedMsg)
	result = append(result, messages[insertAt:]...)

	// Pin the reconstruction position to the message the reminder now precedes. A
	// tail insert leaves this empty, which reconstructs as an append. The anchored
	// message is always one the framework persists (user/assistant/tool/other
	// reminder), never the regenerated leading system instruction.
	if insertAt < len(messages) {
		beforeMessageID = adk.GetMessageID(messages[insertAt])
	}
	return result, insertedMsg, beforeMessageID, true
}

func parseReminderEntries(content string) []reminderEntry {
	lines := strings.Split(content, "\n")
	entries := make([]reminderEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		name, desc, ok := strings.Cut(body, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		entries = append(entries, reminderEntry{Name: name, Description: strings.TrimSpace(desc)})
	}
	return entries
}

// reminderInsertIndex returns the index at which a fresh reminder should be inserted.
//
//   - System mode (asUser=false): right after the latest turn boundary (user message
//     or final assistant answer), then past any settled system messages already
//     sitting there — sibling or stale reminders — so the fresh reminder lands after
//     them without disturbing their positions, yet never after pending
//     tool-call/tool-result scaffolding.
//   - User mode (asUser=true): immediately before the latest turn's user-input
//     message, so the user-role reminder reads as context preceding the user's ask.
//     Falls back to the tail when there is no user-input message.
func reminderInsertIndex[M adk.MessageType](messages []M, asUser bool) int {
	if asUser {
		for i := len(messages) - 1; i >= 0; i-- {
			if isUserInputAnchor(messages[i]) {
				return i
			}
		}
		return len(messages)
	}

	insertAt := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if isReminderAnchor(messages[i]) {
			insertAt = i + 1
			break
		}
	}
	for insertAt < len(messages) && isSystemMessage(messages[insertAt]) {
		insertAt++
	}
	return insertAt
}

// isReminderAnchor reports whether msg is a clean turn boundary that a reminder
// may be inserted after: a user message or a final assistant answer (one without
// tool calls). It mirrors the anchor rule used by the tool-search middleware.
func isReminderAnchor[M adk.MessageType](msg M) bool {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Role == schema.User || (v.Role == schema.Assistant && len(v.ToolCalls) == 0)
	case *schema.AgenticMessage:
		switch v.Role {
		case schema.AgenticRoleTypeUser:
			return !internal.HasToolResult(v.ContentBlocks)
		case schema.AgenticRoleTypeAssistant:
			return !internal.HasToolCall(v.ContentBlocks)
		}
	}
	return false
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

// isUserInputAnchor reports whether msg is a user-input message (a user turn, not a
// tool result carried on a user-role message). It is the user-mode counterpart to
// isReminderAnchor: it marks the boundary a user-role reminder is inserted before.
func isUserInputAnchor[M adk.MessageType](msg M) bool {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Role == schema.User
	case *schema.AgenticMessage:
		return v.Role == schema.AgenticRoleTypeUser && !internal.HasToolResult(v.ContentBlocks)
	}
	return false
}

// reminderIsUser reports whether a reminder message currently carries the user
// role. A reminder is always either a system or a user message, so anything that is
// not a system message is treated as user role.
func reminderIsUser[M adk.MessageType](msg M) bool {
	return !isSystemMessage(msg)
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

// reminderContent returns the text carried by a reminder created via
// newReminder, used to detect whether the section changed since the last reminder.
// It mirrors newReminder's single-text-block layout and is role-agnostic.
func reminderContent[M adk.MessageType](msg M) string {
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

// newReminder builds a reminder message carrying content and the given extraKey. It
// is a user message when asUser is true, otherwise a system message (the default).
func newReminder[M adk.MessageType](extraKey string, content string, asUser bool) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		var msg *schema.Message
		if asUser {
			msg = schema.UserMessage(content)
		} else {
			msg = schema.SystemMessage(content)
		}
		msg.Extra = map[string]any{extraKey: true}
		return any(msg).(M)
	case *schema.AgenticMessage:
		var msg *schema.AgenticMessage
		if asUser {
			msg = schema.UserAgenticMessage(content)
		} else {
			msg = schema.SystemAgenticMessage(content)
		}
		msg.Extra = map[string]any{extraKey: true}
		return any(msg).(M)
	}
	panic("unreachable")
}

// setReminderMessageID stamps id onto msg's Extra, preserving its other keys.
func setReminderMessageID[M adk.MessageType](msg M, id string) {
	switch v := any(msg).(type) {
	case *schema.Message:
		v.Extra = internal.SetMessageID(v.Extra, id)
	case *schema.AgenticMessage:
		v.Extra = internal.SetMessageID(v.Extra, id)
	}
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
