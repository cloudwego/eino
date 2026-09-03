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

// PatchedToolResult is the value returned by PatchedToolResultGenerator.
//
// Content and ToolResult are mutually exclusive:
//   - Content: plain-text tool message (typical for InvokableTool users)
//   - ToolResult: EnhancedTool-style result (text and/or multimodal parts)
type PatchedToolResult struct {
	Content    string
	ToolResult *schema.ToolResult
}

const syntheticAgenticToolResultMarker = "_eino_patch_tool_calls_synthetic"

// Config defines the configuration options for the patch tool calls middleware.
type Config struct {
	// PatchedContentGenerator is an optional custom function to generate the content
	// of patched tool messages as a plain string.
	//
	// Deprecated: Use PatchedToolResultGenerator instead, which receives the original
	// tool-call arguments and can return either plain Content or an EnhancedTool
	// ToolResult. Kept for backward compatibility; ignored when
	// PatchedToolResultGenerator is set.
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

	// PatchedToolResultGenerator generates a patched tool result for a dangling tool call.
	// It is preferred over PatchedContentGenerator when both are set.
	//
	// Return PatchedToolResult.Content for plain text, or PatchedToolResult.ToolResult
	// for EnhancedTool-style multimodal results. The two fields are mutually exclusive.
	//
	// Parameters:
	//   - ctx: the context for the operation
	//   - toolName: the name of the tool that was called
	//   - toolCallID: the id of the tool call
	//   - toolArgument: the original tool-call arguments (Text is the JSON arguments string)
	//
	// Returns:
	//   - *PatchedToolResult: the patched tool result to insert into history
	//   - error: any error that occurred during generation
	PatchedToolResultGenerator func(
		ctx context.Context,
		toolName string,
		toolCallID string,
		toolArgument *schema.ToolArgument,
	) (*PatchedToolResult, error)

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
		for callIndex, tc := range msg.ToolCalls {
			if tc.ID == "" || hasCorrespondingKeptToolMessage(messages, keep, i+1, tc.ID) {
				continue
			}
			toolMsg, err := createPatchedToolMessage(ctx, cfg, tc)
			if err != nil {
				return nil, err
			}
			adk.EnsureMessageID(toolMsg)
			inserted = append(inserted, &adk.SessionEvent[*schema.Message]{
				Kind: adk.SessionEventMessageInserted,
				MessageInserted: &adk.MessageInsertedEvent[*schema.Message]{
					Message:         toolMsg,
					BeforeMessageID: messageToolResultInsertionAnchor(messages, keep, i+1, msg.ToolCalls[callIndex+1:]),
				},
			})
		}
	}

	patched, err := applyInsertionEvents(patched, inserted)
	if err != nil {
		return nil, err
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
	var scope *toolResultScope

	finishScope := func() {
		if scope == nil {
			return
		}
		for callID := range scope.callIDs {
			if _, ok := scope.seen[callID]; !ok {
				counts.missing++
			}
		}
		scope = nil
	}

	for _, msg := range messages {
		if msg.Role == schema.Tool {
			if scope == nil {
				counts.orphan++
				continue
			}
			if _, ok := scope.callIDs[msg.ToolCallID]; !ok {
				counts.orphan++
			} else if _, ok := scope.seen[msg.ToolCallID]; ok {
				counts.duplicate++
			} else {
				scope.seen[msg.ToolCallID] = struct{}{}
			}
			continue
		}
		finishScope()
		if msg.Role != schema.Assistant {
			continue
		}
		callIDs := make(map[string]struct{})
		for _, tc := range msg.ToolCalls {
			if tc.ID == "" {
				counts.emptyID++
				continue
			}
			callIDs[tc.ID] = struct{}{}
		}
		if len(callIDs) > 0 {
			scope = &toolResultScope{callIDs: callIDs, seen: make(map[string]struct{})}
		}
	}
	finishScope()

	return counts
}

func ensureMessageIDs[M adk.MessageType](messages []M) {
	for _, msg := range messages {
		adk.EnsureMessageID(msg)
	}
}

func keptMessages(messages []*schema.Message, cfg Config) []bool {
	keep := make([]bool, len(messages))
	var scope *toolResultScope

	for i, msg := range messages {
		keep[i] = true
		if msg.Role == schema.Tool {
			valid := false
			duplicate := false
			if scope != nil {
				_, valid = scope.callIDs[msg.ToolCallID]
				_, duplicate = scope.seen[msg.ToolCallID]
			}
			if !valid && cfg.RemoveOrphanResults {
				keep[i] = false
			} else if valid && duplicate && cfg.RemoveDuplicateResults {
				keep[i] = false
			}
			if valid && !duplicate {
				scope.seen[msg.ToolCallID] = struct{}{}
			}
			continue
		}
		scope = nil
		if msg.Role != schema.Assistant {
			continue
		}
		callIDs := make(map[string]struct{})
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				callIDs[tc.ID] = struct{}{}
			}
		}
		if len(callIDs) > 0 {
			scope = &toolResultScope{callIDs: callIDs, seen: make(map[string]struct{})}
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
		toolCalls := collectAgenticToolCalls(msg)
		for callIndex, tc := range toolCalls {
			if tc.callID == "" || hasCorrespondingRewrittenAgenticToolResult(rewrites, i+1, tc.callID) {
				continue
			}
			toolMsg, err := createPatchedAgenticToolMessage(ctx, cfg, tc.name, tc.callID, tc.args)
			if err != nil {
				return nil, err
			}
			if cfg.MarkSynthetic {
				markSyntheticAgenticToolResult(toolMsg)
			}
			adk.EnsureMessageID(toolMsg)
			inserted = append(inserted, &adk.SessionEvent[*schema.AgenticMessage]{
				Kind: adk.SessionEventMessageInserted,
				MessageInserted: &adk.MessageInsertedEvent[*schema.AgenticMessage]{
					Message:         toolMsg,
					BeforeMessageID: agenticToolResultInsertionAnchor(messages, rewrites, i+1, toolCalls[callIndex+1:]),
				},
			})
		}
	}

	patched, err := applyInsertionEvents(patched, inserted)
	if err != nil {
		return nil, err
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
	args   string
}

// toolResultScope is limited to one assistant tool-call message plus the
// immediately following contiguous tool-result window.
type toolResultScope struct {
	callIDs map[string]struct{}
	seen    map[string]struct{}
}

type agenticRewrite struct {
	message *schema.AgenticMessage
	keep    bool
	updated bool
}

func analyzeAgenticMessages(messages []*schema.AgenticMessage) mismatchCounts {
	var counts mismatchCounts
	var scope *toolResultScope

	finishScope := func() {
		if scope == nil {
			return
		}
		for callID := range scope.callIDs {
			if _, ok := scope.seen[callID]; !ok {
				counts.missing++
			}
		}
		scope = nil
	}

	for _, msg := range messages {
		hasToolResult := agenticMessageHasToolResult(msg)
		if msg.Role == schema.AgenticRoleTypeUser && hasToolResult {
			for _, block := range msg.ContentBlocks {
				callID, ok := agenticResultCallID(block)
				if !ok {
					continue
				}
				if scope == nil {
					counts.orphan++
					continue
				}
				if _, valid := scope.callIDs[callID]; !valid {
					counts.orphan++
				} else if _, duplicate := scope.seen[callID]; duplicate {
					counts.duplicate++
				} else {
					scope.seen[callID] = struct{}{}
				}
			}
			continue
		}

		finishScope()
		if hasToolResult {
			for _, block := range msg.ContentBlocks {
				if _, ok := agenticResultCallID(block); ok {
					counts.orphan++
				}
			}
		}
		if msg.Role != schema.AgenticRoleTypeAssistant {
			continue
		}
		callIDs := make(map[string]struct{})
		for _, tc := range collectAgenticToolCalls(msg) {
			if tc.callID == "" {
				counts.emptyID++
				continue
			}
			callIDs[tc.callID] = struct{}{}
		}
		if len(callIDs) > 0 {
			scope = &toolResultScope{callIDs: callIDs, seen: make(map[string]struct{})}
		}
	}
	finishScope()

	return counts
}

func agenticMessageRewrites(messages []*schema.AgenticMessage, cfg Config) []agenticRewrite {
	rewrites := make([]agenticRewrite, len(messages))
	var scope *toolResultScope

	for i, msg := range messages {
		rewrite := agenticRewrite{message: msg, keep: true}
		hasToolResult := agenticMessageHasToolResult(msg)

		switch {
		case msg.Role == schema.AgenticRoleTypeUser && hasToolResult:
			rewrite = rewriteAgenticResultBlocks(msg, scope, cfg)
			if scope != nil && rewrite.keep && !agenticMessageHasToolResult(rewrite.message) {
				scope = nil
			}
		default:
			if hasToolResult {
				rewrite = rewriteAgenticResultBlocks(msg, nil, cfg)
			}
			if rewrite.keep {
				scope = nil
			}
			if msg.Role == schema.AgenticRoleTypeAssistant && rewrite.keep {
				callIDs := make(map[string]struct{})
				for _, tc := range collectAgenticToolCalls(rewrite.message) {
					if tc.callID != "" {
						callIDs[tc.callID] = struct{}{}
					}
				}
				if len(callIDs) > 0 {
					scope = &toolResultScope{callIDs: callIDs, seen: make(map[string]struct{})}
				}
			}
		}

		rewrites[i] = rewrite
	}

	return rewrites
}

func rewriteAgenticResultBlocks(msg *schema.AgenticMessage, scope *toolResultScope, cfg Config) agenticRewrite {
	rewrite := agenticRewrite{message: msg, keep: true}
	blocks := make([]*schema.ContentBlock, 0, len(msg.ContentBlocks))
	removedBlock := false

	for _, block := range msg.ContentBlocks {
		callID, ok := agenticResultCallID(block)
		if !ok {
			blocks = append(blocks, block)
			continue
		}
		valid := false
		duplicate := false
		if scope != nil {
			_, valid = scope.callIDs[callID]
			_, duplicate = scope.seen[callID]
		}
		remove := (!valid && cfg.RemoveOrphanResults) || (valid && duplicate && cfg.RemoveDuplicateResults)
		if remove {
			removedBlock = true
		} else {
			blocks = append(blocks, block)
		}
		if valid && !duplicate {
			scope.seen[callID] = struct{}{}
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

	return rewrite
}

func agenticMessageHasToolResult(msg *schema.AgenticMessage) bool {
	for _, block := range msg.ContentBlocks {
		if _, ok := agenticResultCallID(block); ok {
			return true
		}
	}
	return false
}

func hasCorrespondingKeptToolMessage(messages []*schema.Message, keep []bool, start int, toolCallID string) bool {
	for i := start; i < len(messages); i++ {
		if !keep[i] {
			continue
		}
		msg := messages[i]
		if msg.Role != schema.Tool {
			return false
		}
		if msg.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}

func hasCorrespondingRewrittenAgenticToolResult(rewrites []agenticRewrite, start int, toolCallID string) bool {
	for i := start; i < len(rewrites); i++ {
		rewrite := rewrites[i]
		if !rewrite.keep {
			continue
		}
		msg := rewrite.message
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

func collectAgenticToolCalls(msg *schema.AgenticMessage) []agenticToolCall {
	toolCalls := make([]agenticToolCall, 0)
	for _, block := range msg.ContentBlocks {
		if block != nil && block.Type == schema.ContentBlockTypeFunctionToolCall && block.FunctionToolCall != nil {
			toolCalls = append(toolCalls, agenticToolCall{
				callID: block.FunctionToolCall.CallID,
				name:   block.FunctionToolCall.Name,
				args:   block.FunctionToolCall.Arguments,
			})
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

// Existing results arrive in tool-call order from the current run state or
// session reconstruction. Anchor a synthetic result before the first kept
// result for a later call, or before the next non-result message.
func messageToolResultInsertionAnchor(messages []*schema.Message, keep []bool, start int, laterCalls []schema.ToolCall) string {
	laterCallIDs := make(map[string]struct{}, len(laterCalls))
	for _, toolCall := range laterCalls {
		laterCallIDs[toolCall.ID] = struct{}{}
	}
	for i := start; i < len(messages); i++ {
		if !keep[i] {
			continue
		}
		if messages[i].Role != schema.Tool {
			return adk.GetMessageID(messages[i])
		}
		if _, later := laterCallIDs[messages[i].ToolCallID]; later {
			return adk.GetMessageID(messages[i])
		}
	}
	return ""
}

func agenticToolResultInsertionAnchor(
	messages []*schema.AgenticMessage,
	rewrites []agenticRewrite,
	start int,
	laterCalls []agenticToolCall,
) string {
	laterCallIDs := make(map[string]struct{}, len(laterCalls))
	for _, toolCall := range laterCalls {
		laterCallIDs[toolCall.callID] = struct{}{}
	}
	for i := start; i < len(messages); i++ {
		if !rewrites[i].keep {
			continue
		}
		message := rewrites[i].message
		if message.Role != schema.AgenticRoleTypeUser || !agenticMessageHasToolResult(message) {
			return adk.GetMessageID(messages[i])
		}
		for _, block := range message.ContentBlocks {
			callID, isResult := agenticResultCallID(block)
			if _, later := laterCallIDs[callID]; isResult && later {
				return adk.GetMessageID(messages[i])
			}
		}
	}
	return ""
}

func applyInsertionEvents[M adk.MessageType](
	messages []M,
	events []*adk.SessionEvent[M],
) ([]M, error) {
	for _, event := range events {
		insertion := event.MessageInserted
		if insertion.BeforeMessageID == "" {
			messages = append(messages, insertion.Message)
			continue
		}
		insertAt := -1
		for i, message := range messages {
			if adk.GetMessageID(message) == insertion.BeforeMessageID {
				insertAt = i
				break
			}
		}
		if insertAt < 0 {
			return nil, fmt.Errorf("patchtoolcalls: insertion anchor %q not found", insertion.BeforeMessageID)
		}
		var zero M
		messages = append(messages, zero)
		copy(messages[insertAt+1:], messages[insertAt:])
		messages[insertAt] = insertion.Message
	}
	return messages, nil
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
		err := adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{
			SessionEventVariant: &adk.SessionEventVariant[M]{Event: event},
		})
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

func createPatchedToolMessage(ctx context.Context, cfg Config, tc schema.ToolCall) (*schema.Message, error) {
	if cfg.PatchedToolResultGenerator != nil {
		arg := &schema.ToolArgument{Text: tc.Function.Arguments}
		result, err := cfg.PatchedToolResultGenerator(ctx, tc.Function.Name, tc.ID, arg)
		if err != nil {
			return nil, err
		}
		return patchedToolResultToMessage(tc.Function.Name, tc.ID, result)
	}
	if cfg.PatchedContentGenerator != nil {
		content, err := cfg.PatchedContentGenerator(ctx, tc.Function.Name, tc.ID)
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

func createPatchedAgenticToolMessage(ctx context.Context, cfg Config, toolName, callID, arguments string) (*schema.AgenticMessage, error) {
	if cfg.PatchedToolResultGenerator != nil {
		arg := &schema.ToolArgument{Text: arguments}
		result, err := cfg.PatchedToolResultGenerator(ctx, toolName, callID, arg)
		if err != nil {
			return nil, err
		}
		return patchedToolResultToAgenticMessage(toolName, callID, result)
	}

	var content string
	if cfg.PatchedContentGenerator != nil {
		var err error
		content, err = cfg.PatchedContentGenerator(ctx, toolName, callID)
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

	return agenticTextToolResultMessage(toolName, callID, content), nil
}

func patchedToolResultToMessage(toolName, callID string, result *PatchedToolResult) (*schema.Message, error) {
	if result == nil {
		return schema.ToolMessage("", callID, schema.WithToolName(toolName)), nil
	}
	if err := validatePatchedToolResult(result); err != nil {
		return nil, err
	}
	if result.ToolResult != nil {
		return toolResultToMessage(toolName, callID, result.ToolResult)
	}
	return schema.ToolMessage(result.Content, callID, schema.WithToolName(toolName)), nil
}

func patchedToolResultToAgenticMessage(toolName, callID string, result *PatchedToolResult) (*schema.AgenticMessage, error) {
	if result == nil {
		return agenticTextToolResultMessage(toolName, callID, ""), nil
	}
	if err := validatePatchedToolResult(result); err != nil {
		return nil, err
	}
	if result.ToolResult != nil {
		return toolResultToAgenticMessage(toolName, callID, result.ToolResult)
	}
	return agenticTextToolResultMessage(toolName, callID, result.Content), nil
}

func validatePatchedToolResult(result *PatchedToolResult) error {
	if result.Content != "" && result.ToolResult != nil {
		return fmt.Errorf("patchtoolcalls: PatchedToolResult.Content and ToolResult are mutually exclusive")
	}
	return nil
}

func agenticTextToolResultMessage(toolName, callID, content string) *schema.AgenticMessage {
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
	}
}

// toolResultToMessage converts a ToolResult into a tool-role Message.
//
// Pure-text results (single text part) only set Content. Setting both Content
// and UserInputMultiContent would break OpenAI-compatible serializers that
// reject ChatCompletionMessage with Content and MultiContent simultaneously.
// Multimodal / non-text parts go exclusively into UserInputMultiContent.
func toolResultToMessage(toolName, callID string, result *schema.ToolResult) (*schema.Message, error) {
	msg := schema.ToolMessage("", callID, schema.WithToolName(toolName))
	if result == nil || len(result.Parts) == 0 {
		return msg, nil
	}
	if text, ok := singleTextToolResult(result); ok {
		msg.Content = text
		return msg, nil
	}
	parts, err := result.ToMessageInputParts()
	if err != nil {
		return nil, err
	}
	msg.UserInputMultiContent = parts
	return msg, nil
}

func toolResultToAgenticMessage(toolName, callID string, result *schema.ToolResult) (*schema.AgenticMessage, error) {
	if result != nil && len(result.Parts) == 1 && result.Parts[0].Type == schema.ToolPartTypeToolSearchResult {
		if result.Parts[0].ToolSearchResult == nil {
			return nil, fmt.Errorf("tool search result is nil for tool part type %v", result.Parts[0].Type)
		}
		return &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.ToolSearchFunctionToolResult{
					CallID: callID,
					Name:   toolName,
					Result: result.Parts[0].ToolSearchResult,
				}),
			},
		}, nil
	}

	blocks, err := toolResultToFunctionBlocks(result)
	if err != nil {
		return nil, err
	}
	// Empty ToolResult is valid and maps to an empty text tool result, matching
	// toolResultToMessage which leaves Content empty for nil/empty results.
	if len(blocks) == 0 {
		blocks = []*schema.FunctionToolResultContentBlock{
			{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: ""}},
		}
	}
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolResult{
				CallID:  callID,
				Name:    toolName,
				Content: blocks,
			}),
		},
	}, nil
}

func singleTextToolResult(result *schema.ToolResult) (string, bool) {
	if result == nil || len(result.Parts) != 1 || result.Parts[0].Type != schema.ToolPartTypeText {
		return "", false
	}
	return result.Parts[0].Text, true
}

func toolResultToFunctionBlocks(result *schema.ToolResult) ([]*schema.FunctionToolResultContentBlock, error) {
	if result == nil || len(result.Parts) == 0 {
		return nil, nil
	}
	blocks := make([]*schema.FunctionToolResultContentBlock, 0, len(result.Parts))
	for _, p := range result.Parts {
		switch p.Type {
		case schema.ToolPartTypeText:
			blocks = append(blocks, &schema.FunctionToolResultContentBlock{
				Type:  schema.FunctionToolResultContentBlockTypeText,
				Text:  &schema.UserInputText{Text: p.Text},
				Extra: p.Extra,
			})
		case schema.ToolPartTypeImage:
			if p.Image == nil {
				return nil, fmt.Errorf("image content is nil for tool part type %v", p.Type)
			}
			blocks = append(blocks, &schema.FunctionToolResultContentBlock{
				Type: schema.FunctionToolResultContentBlockTypeImage,
				Image: &schema.UserInputImage{
					URL:        derefString(p.Image.URL),
					Base64Data: derefString(p.Image.Base64Data),
					MIMEType:   p.Image.MIMEType,
				},
				Extra: p.Extra,
			})
		case schema.ToolPartTypeAudio:
			if p.Audio == nil {
				return nil, fmt.Errorf("audio content is nil for tool part type %v", p.Type)
			}
			blocks = append(blocks, &schema.FunctionToolResultContentBlock{
				Type: schema.FunctionToolResultContentBlockTypeAudio,
				Audio: &schema.UserInputAudio{
					URL:        derefString(p.Audio.URL),
					Base64Data: derefString(p.Audio.Base64Data),
					MIMEType:   p.Audio.MIMEType,
				},
				Extra: p.Extra,
			})
		case schema.ToolPartTypeVideo:
			if p.Video == nil {
				return nil, fmt.Errorf("video content is nil for tool part type %v", p.Type)
			}
			blocks = append(blocks, &schema.FunctionToolResultContentBlock{
				Type: schema.FunctionToolResultContentBlockTypeVideo,
				Video: &schema.UserInputVideo{
					URL:        derefString(p.Video.URL),
					Base64Data: derefString(p.Video.Base64Data),
					MIMEType:   p.Video.MIMEType,
				},
				Extra: p.Extra,
			})
		case schema.ToolPartTypeFile:
			if p.File == nil {
				return nil, fmt.Errorf("file content is nil for tool part type %v", p.Type)
			}
			blocks = append(blocks, &schema.FunctionToolResultContentBlock{
				Type: schema.FunctionToolResultContentBlockTypeFile,
				File: &schema.UserInputFile{
					URL:        derefString(p.File.URL),
					Base64Data: derefString(p.File.Base64Data),
					MIMEType:   p.File.MIMEType,
				},
				Extra: p.Extra,
			})
		case schema.ToolPartTypeToolSearchResult:
			return nil, fmt.Errorf("tool search result must be the sole part of a ToolResult")
		default:
			return nil, fmt.Errorf("unknown tool part type: %v", p.Type)
		}
	}
	return blocks, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

const (
	defaultPatchedToolMessageTemplate        = "Tool call %s with id %s was canceled - another message came in before it could be completed."
	defaultPatchedToolMessageTemplateChinese = "工具调用 %s（ID 为 %s）已被取消——在其完成之前收到了另一条消息。"
)
