/*
 * Copyright 2025 CloudWeGo Authors
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

// Package patchtoolcalls provides a middleware that patches dangling tool calls in the message history.
package patchtoolcalls

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/schema"
)

const syntheticAgenticToolResultMarker = "_eino_patch_tool_calls_synthetic"

// Config defines the configuration options for the patch tool calls middleware.
type Config struct {
	// PatchedContentGenerator is an optional custom function to generate the content
	// of patched tool messages. If not provided, a default message will be used.
	//
	// Parameters:
	//   - ctx: the context for the operation
	//   - toolName: the name of the tool that was called
	//   - toolCallID: the id of the tool call
	//
	// Returns:
	//   - string: the content to use for the patched tool message
	//   - error: any error that occurred during generation
	PatchedContentGenerator func(ctx context.Context, toolName, toolCallID string) (string, error)

	// RemoveOrphanResults removes tool result messages or result blocks whose call ID
	// does not match any previous assistant tool call. Disabled by default.
	RemoveOrphanResults bool

	// RemoveDuplicateResults removes duplicate tool result messages or result blocks
	// after the first result kept for a call ID. Disabled by default.
	RemoveDuplicateResults bool

	// Strict validates the history and returns an error without mutating state when
	// missing, orphan, duplicate, or empty-ID mismatches are found. Disabled by default.
	Strict bool

	// MarkSynthetic marks generated AgenticMessage tool results in Extra so callers
	// can identify mechanical repairs. Disabled by default.
	MarkSynthetic bool
}

// NewTyped creates a new generic patch tool calls middleware.
//
// The middleware scans the message history before each model invocation and inserts
// placeholder tool messages for any tool calls that don't have corresponding responses.
func NewTyped[M adk.MessageType](_ context.Context, cfg *Config) (adk.TypedChatModelAgentMiddleware[M], error) {
	if cfg == nil {
		cfg = &Config{}
	}
	cfgCopy := *cfg
	return &typedMiddleware[M]{
		cfg: cfgCopy,
	}, nil
}

// New creates a new patch tool calls middleware with the given configuration.
//
// The middleware scans the message history before each model invocation and inserts
// placeholder tool messages for any tool calls that don't have corresponding responses.
func New(ctx context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped[*schema.Message](ctx, cfg)
}

type typedMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	cfg Config
}

func (m *typedMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M],
	mc *adk.TypedModelContext[M],
) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	if len(state.Messages) == 0 {
		return ctx, state, nil
	}

	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return patchToolCallsForMessage(ctx, m.cfg, any(state).(*adk.TypedChatModelAgentState[*schema.Message]), mc)
	case *schema.AgenticMessage:
		return patchToolCallsForAgenticMessage(ctx, m.cfg, any(state).(*adk.TypedChatModelAgentState[*schema.AgenticMessage]), mc)
	default:
		panic("unreachable: unknown MessageType")
	}
}

func patchToolCallsForMessage[M adk.MessageType](ctx context.Context,
	cfg Config,
	state *adk.TypedChatModelAgentState[*schema.Message],
	_ *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {

	plan, err := buildMessageNormalizationPlan(ctx, cfg, state.Messages)
	if err != nil {
		return ctx, nil, err
	}
	if err := sendNormalizationEvents(ctx, plan.events); err != nil {
		return ctx, nil, err
	}

	nState := *state
	nState.Messages = plan.messages
	return ctx, any(&nState).(*adk.TypedChatModelAgentState[M]), nil
}

func patchToolCallsForAgenticMessage[M adk.MessageType](ctx context.Context,
	cfg Config,
	state *adk.TypedChatModelAgentState[*schema.AgenticMessage],
	_ *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {

	plan, err := buildAgenticNormalizationPlan(ctx, cfg, state.Messages)
	if err != nil {
		return ctx, nil, err
	}
	if err := sendNormalizationEvents(ctx, plan.events); err != nil {
		return ctx, nil, err
	}

	nState := *state
	nState.Messages = plan.messages
	return ctx, any(&nState).(*adk.TypedChatModelAgentState[M]), nil
}

type mismatchCounts struct {
	missing   int
	orphan    int
	duplicate int
	emptyID   int
}

func (c mismatchCounts) hasMismatch() bool {
	return c.missing > 0 || c.orphan > 0 || c.duplicate > 0 || c.emptyID > 0
}

func (c mismatchCounts) strictError() error {
	return fmt.Errorf("patchtoolcalls strict validation failed: missing=%d orphan=%d duplicate=%d empty_tool_call_id=%d",
		c.missing, c.orphan, c.duplicate, c.emptyID)
}

type normalizationPlan[M adk.MessageType] struct {
	messages []M
	events   []*adk.SessionEvent[M]
	counts   mismatchCounts
}

func buildMessageNormalizationPlan(ctx context.Context, cfg Config, messages []*schema.Message) (*normalizationPlan[*schema.Message], error) {
	ensureMessageIDs(messages)

	counts := analyzeMessages(messages)
	if cfg.Strict && counts.hasMismatch() {
		return nil, counts.strictError()
	}

	keep := keptMessages(messages, cfg)
	patched := make([]*schema.Message, 0, len(messages)+counts.missing)
	inserted := make([]*adk.SessionEvent[*schema.Message], 0, counts.missing)

	for i, msg := range messages {
		if keep[i] {
			patched = append(patched, msg)
		}
		if msg.Role != schema.Assistant || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID == "" || hasCorrespondingToolMessage(messages[i+1:], tc.ID) {
				continue
			}
			toolMsg, err := createPatchedToolMessage(ctx, cfg.PatchedContentGenerator, tc)
			if err != nil {
				return nil, err
			}
			adk.EnsureMessageID(toolMsg)
			patched = append(patched, toolMsg)
			inserted = append(inserted, &adk.SessionEvent[*schema.Message]{
				Kind: adk.SessionEventMessageInserted,
				MessageInserted: &adk.MessageInsertedEvent[*schema.Message]{
					Message:         toolMsg,
					BeforeMessageID: firstKeptMessageID(messages, keep, i+1),
				},
			})
		}
	}

	events := make([]*adk.SessionEvent[*schema.Message], 0, len(inserted)+1)
	events = append(events, inserted...)
	if deletedIDs := deletedMessageIDs(messages, keep); len(deletedIDs) > 0 {
		events = append(events, &adk.SessionEvent[*schema.Message]{
			Kind: adk.SessionEventMessagesDeleted,
			MessagesDeleted: &adk.MessagesDeletedEvent{
				MessageIDs: deletedIDs,
			},
		})
	}

	return &normalizationPlan[*schema.Message]{messages: patched, events: events, counts: counts}, nil
}

func analyzeMessages(messages []*schema.Message) mismatchCounts {
	var counts mismatchCounts
	previousCalls := make(map[string]struct{})
	seenResults := make(map[string]struct{})

	for i, msg := range messages {
		if msg.Role == schema.Tool {
			if _, ok := previousCalls[msg.ToolCallID]; !ok {
				counts.orphan++
			} else if _, ok := seenResults[msg.ToolCallID]; ok {
				counts.duplicate++
			} else {
				seenResults[msg.ToolCallID] = struct{}{}
			}
		}
		if msg.Role != schema.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID == "" {
				counts.emptyID++
				continue
			}
			previousCalls[tc.ID] = struct{}{}
			if !hasCorrespondingToolMessage(messages[i+1:], tc.ID) {
				counts.missing++
			}
		}
	}

	return counts
}

func ensureMessageIDs[M adk.MessageType](messages []M) {
	for _, msg := range messages {
		adk.EnsureMessageID(msg)
	}
}

func keptMessages(messages []*schema.Message, cfg Config) []bool {
	keep := make([]bool, len(messages))
	previousCalls := make(map[string]struct{})
	seenResults := make(map[string]struct{})

	for i, msg := range messages {
		keep[i] = true
		if msg.Role == schema.Tool {
			_, valid := previousCalls[msg.ToolCallID]
			_, duplicate := seenResults[msg.ToolCallID]
			if !valid && cfg.RemoveOrphanResults {
				keep[i] = false
			} else if valid && duplicate && cfg.RemoveDuplicateResults {
				keep[i] = false
			}
			if valid && !duplicate {
				seenResults[msg.ToolCallID] = struct{}{}
			}
		}
		if msg.Role != schema.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				previousCalls[tc.ID] = struct{}{}
			}
		}
	}

	return keep
}

func buildAgenticNormalizationPlan(ctx context.Context, cfg Config, messages []*schema.AgenticMessage) (*normalizationPlan[*schema.AgenticMessage], error) {
	ensureMessageIDs(messages)

	counts := analyzeAgenticMessages(messages)
	if cfg.Strict && counts.hasMismatch() {
		return nil, counts.strictError()
	}

	rewrites := agenticMessageRewrites(messages, cfg)
	patched := make([]*schema.AgenticMessage, 0, len(messages)+counts.missing)
	inserted := make([]*adk.SessionEvent[*schema.AgenticMessage], 0, counts.missing)
	updated := make([]*adk.SessionEvent[*schema.AgenticMessage], 0)

	for i, msg := range messages {
		rewrite := rewrites[i]
		if rewrite.keep {
			patched = append(patched, rewrite.message)
			if rewrite.updated {
				updated = append(updated, &adk.SessionEvent[*schema.AgenticMessage]{
					Kind: adk.SessionEventMessageUpdated,
					MessageUpdated: &adk.MessageUpdatedEvent[*schema.AgenticMessage]{
						MessageID: adk.GetMessageID(msg),
						Message:   rewrite.message,
					},
				})
			}
		}
		if msg.Role != schema.AgenticRoleTypeAssistant {
			continue
		}
		for _, tc := range collectAgenticToolCalls(msg) {
			if tc.callID == "" || hasCorrespondingAgenticToolResult(messages[i+1:], tc.callID) {
				continue
			}
			toolMsg, err := createPatchedAgenticToolMessage(ctx, cfg.PatchedContentGenerator, tc.name, tc.callID)
			if err != nil {
				return nil, err
			}
			if cfg.MarkSynthetic {
				markSyntheticAgenticToolResult(toolMsg)
			}
			adk.EnsureMessageID(toolMsg)
			patched = append(patched, toolMsg)
			inserted = append(inserted, &adk.SessionEvent[*schema.AgenticMessage]{
				Kind: adk.SessionEventMessageInserted,
				MessageInserted: &adk.MessageInsertedEvent[*schema.AgenticMessage]{
					Message:         toolMsg,
					BeforeMessageID: firstKeptAgenticMessageID(messages, rewrites, i+1),
				},
			})
		}
	}

	events := make([]*adk.SessionEvent[*schema.AgenticMessage], 0, len(inserted)+len(updated)+1)
	events = append(events, inserted...)
	events = append(events, updated...)
	if deletedIDs := deletedAgenticMessageIDs(messages, rewrites); len(deletedIDs) > 0 {
		events = append(events, &adk.SessionEvent[*schema.AgenticMessage]{
			Kind: adk.SessionEventMessagesDeleted,
			MessagesDeleted: &adk.MessagesDeletedEvent{
				MessageIDs: deletedIDs,
			},
		})
	}

	return &normalizationPlan[*schema.AgenticMessage]{messages: patched, events: events, counts: counts}, nil
}

type agenticToolCall struct {
	callID string
	name   string
}

type agenticRewrite struct {
	message *schema.AgenticMessage
	keep    bool
	updated bool
}

func analyzeAgenticMessages(messages []*schema.AgenticMessage) mismatchCounts {
	var counts mismatchCounts
	previousCalls := make(map[string]struct{})
	seenResults := make(map[string]struct{})

	for i, msg := range messages {
		for _, block := range msg.ContentBlocks {
			callID, ok := agenticResultCallID(block)
			if !ok {
				continue
			}
			if _, valid := previousCalls[callID]; !valid {
				counts.orphan++
			} else if _, duplicate := seenResults[callID]; duplicate {
				counts.duplicate++
			} else {
				seenResults[callID] = struct{}{}
			}
		}
		if msg.Role != schema.AgenticRoleTypeAssistant {
			continue
		}
		for _, tc := range collectAgenticToolCalls(msg) {
			if tc.callID == "" {
				counts.emptyID++
				continue
			}
			previousCalls[tc.callID] = struct{}{}
			if !hasCorrespondingAgenticToolResult(messages[i+1:], tc.callID) {
				counts.missing++
			}
		}
	}

	return counts
}

func agenticMessageRewrites(messages []*schema.AgenticMessage, cfg Config) []agenticRewrite {
	rewrites := make([]agenticRewrite, len(messages))
	previousCalls := make(map[string]struct{})
	seenResults := make(map[string]struct{})

	for i, msg := range messages {
		rewrite := agenticRewrite{message: msg, keep: true}
		blocks := make([]*schema.ContentBlock, 0, len(msg.ContentBlocks))
		removedBlock := false

		for _, block := range msg.ContentBlocks {
			callID, ok := agenticResultCallID(block)
			if !ok {
				blocks = append(blocks, block)
				continue
			}
			_, valid := previousCalls[callID]
			_, duplicate := seenResults[callID]
			remove := (!valid && cfg.RemoveOrphanResults) || (valid && duplicate && cfg.RemoveDuplicateResults)
			if remove {
				removedBlock = true
			} else {
				blocks = append(blocks, block)
			}
			if valid && !duplicate {
				seenResults[callID] = struct{}{}
			}
		}

		if removedBlock {
			if len(blocks) == 0 {
				rewrite.keep = false
			} else {
				adk.EnsureMessageID(msg)
				cp := *msg
				cp.ContentBlocks = blocks
				cp.Extra = copyStringAnyMap(msg.Extra)
				rewrite.message = &cp
				rewrite.updated = true
			}
		}

		if msg.Role == schema.AgenticRoleTypeAssistant {
			for _, tc := range collectAgenticToolCalls(msg) {
				if tc.callID != "" {
					previousCalls[tc.callID] = struct{}{}
				}
			}
		}
		rewrites[i] = rewrite
	}

	return rewrites
}

func collectAgenticToolCalls(msg *schema.AgenticMessage) []agenticToolCall {
	toolCalls := make([]agenticToolCall, 0)
	for _, block := range msg.ContentBlocks {
		if block != nil && block.Type == schema.ContentBlockTypeFunctionToolCall && block.FunctionToolCall != nil {
			toolCalls = append(toolCalls, agenticToolCall{callID: block.FunctionToolCall.CallID, name: block.FunctionToolCall.Name})
		}
	}
	return toolCalls
}

func agenticResultCallID(block *schema.ContentBlock) (string, bool) {
	if block == nil {
		return "", false
	}
	if block.Type == schema.ContentBlockTypeFunctionToolResult && block.FunctionToolResult != nil {
		return block.FunctionToolResult.CallID, true
	}
	if block.Type == schema.ContentBlockTypeToolSearchResult && block.ToolSearchFunctionToolResult != nil {
		return block.ToolSearchFunctionToolResult.CallID, true
	}
	return "", false
}

func hasCorrespondingToolMessage(messages []*schema.Message, toolCallID string) bool {
	for _, msg := range messages {
		// Only consider successive tool messages after the tool call message
		if msg.Role != schema.Tool {
			return false
		}
		if msg.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}

func hasCorrespondingAgenticToolResult(messages []*schema.AgenticMessage, toolCallID string) bool {
	for _, msg := range messages {
		// Only consider successive tool messages after the tool call message
		if msg.Role != schema.AgenticRoleTypeUser {
			return false
		}
		hasToolResult := false
		for _, block := range msg.ContentBlocks {
			callID, ok := agenticResultCallID(block)
			if ok {
				hasToolResult = true
				if callID == toolCallID {
					return true
				}
			}
		}
		if !hasToolResult {
			return false
		}
	}
	return false
}

func firstKeptMessageID(messages []*schema.Message, keep []bool, start int) string {
	for i := start; i < len(messages); i++ {
		if keep[i] {
			adk.EnsureMessageID(messages[i])
			return adk.GetMessageID(messages[i])
		}
	}
	return ""
}

func firstKeptAgenticMessageID(messages []*schema.AgenticMessage, rewrites []agenticRewrite, start int) string {
	for i := start; i < len(messages); i++ {
		if rewrites[i].keep {
			adk.EnsureMessageID(messages[i])
			return adk.GetMessageID(messages[i])
		}
	}
	return ""
}

func deletedMessageIDs(messages []*schema.Message, keep []bool) []string {
	ids := make([]string, 0)
	for i, msg := range messages {
		if keep[i] {
			continue
		}
		adk.EnsureMessageID(msg)
		ids = append(ids, adk.GetMessageID(msg))
	}
	return ids
}

func deletedAgenticMessageIDs(messages []*schema.AgenticMessage, rewrites []agenticRewrite) []string {
	ids := make([]string, 0)
	for i, msg := range messages {
		if rewrites[i].keep {
			continue
		}
		adk.EnsureMessageID(msg)
		ids = append(ids, adk.GetMessageID(msg))
	}
	return ids
}

func sendNormalizationEvents[M adk.MessageType](ctx context.Context, events []*adk.SessionEvent[M]) error {
	for _, event := range events {
		err := adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{SessionEvent: event})
		if isOutOfRunContextError(err) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func isOutOfRunContextError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "must be called within a ChatModelAgent Run() or Resume() execution context")
}

func markSyntheticAgenticToolResult(msg *schema.AgenticMessage) {
	if msg.Extra == nil {
		msg.Extra = make(map[string]any, 1)
	}
	msg.Extra[syntheticAgenticToolResultMarker] = true
}

func copyStringAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func createPatchedToolMessage(ctx context.Context, gen func(ctx context.Context, toolName, toolCallID string) (string, error), tc schema.ToolCall) (*schema.Message, error) {
	if gen != nil {
		content, err := gen(ctx, tc.Function.Name, tc.ID)
		if err != nil {
			return nil, err
		}
		return schema.ToolMessage(content, tc.ID, schema.WithToolName(tc.Function.Name)), nil
	}
	tpl := internal.SelectPrompt(internal.I18nPrompts{
		English: defaultPatchedToolMessageTemplate,
		Chinese: defaultPatchedToolMessageTemplateChinese,
	})

	return schema.ToolMessage(fmt.Sprintf(tpl, tc.Function.Name, tc.ID), tc.ID, schema.WithToolName(tc.Function.Name)), nil
}

func createPatchedAgenticToolMessage(ctx context.Context, gen func(ctx context.Context, toolName, toolCallID string) (string, error), toolName, callID string) (*schema.AgenticMessage, error) {
	var content string
	if gen != nil {
		var err error
		content, err = gen(ctx, toolName, callID)
		if err != nil {
			return nil, err
		}
	} else {
		tpl := internal.SelectPrompt(internal.I18nPrompts{
			English: defaultPatchedToolMessageTemplate,
			Chinese: defaultPatchedToolMessageTemplateChinese,
		})
		content = fmt.Sprintf(tpl, toolName, callID)
	}

	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolResult{
				CallID: callID,
				Name:   toolName,
				Content: []*schema.FunctionToolResultContentBlock{
					{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: content}},
				},
			}),
		},
	}, nil
}

const (
	defaultPatchedToolMessageTemplate        = "Tool call %s with id %s was canceled - another message came in before it could be completed."
	defaultPatchedToolMessageTemplateChinese = "工具调用 %s（ID 为 %s）已被取消——在其完成之前收到了另一条消息。"
)
