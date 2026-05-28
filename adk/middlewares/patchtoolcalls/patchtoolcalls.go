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
	"maps"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/schema"
)

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
}

// NewTyped creates a new generic patch tool calls middleware.
//
// The middleware scans the message history before each model invocation and inserts
// placeholder tool messages for any tool calls that don't have corresponding responses.
func NewTyped[M adk.MessageType](_ context.Context, cfg *Config) (adk.TypedChatModelAgentMiddleware[M], error) {
	if cfg == nil {
		cfg = &Config{}
	}
	return &typedMiddleware[M]{
		gen: cfg.PatchedContentGenerator,
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
	gen func(ctx context.Context, toolName, toolCallID string) (string, error)
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
		return patchToolCallsForMessage(ctx, m.gen, any(state).(*adk.TypedChatModelAgentState[*schema.Message]), mc)
	case *schema.AgenticMessage:
		return patchToolCallsForAgenticMessage(ctx, m.gen, any(state).(*adk.TypedChatModelAgentState[*schema.AgenticMessage]), mc)
	default:
		panic("unreachable: unknown MessageType")
	}
}

func patchToolCallsForMessage[M adk.MessageType](ctx context.Context,
	gen func(ctx context.Context, toolName, toolCallID string) (string, error),
	state *adk.TypedChatModelAgentState[*schema.Message],
	_ *adk.TypedModelContext[M],
) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	// seenIDs stores unique tool call IDs collected by reverse traversal
	seenIDs := make(map[string]struct{})
	groupedMessages := make([][]*schema.Message, 0, len(state.Messages))
	totalMsgCount := 0

	// Iterate messages in reverse order to track existing tool call IDs
	for i := len(state.Messages) - 1; i >= 0; i-- {
		msg := state.Messages[i]
		currentMessages := []*schema.Message{msg}

		if msg.Role == schema.Tool {
			seenIDs[msg.ToolCallID] = struct{}{}
		}

		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if _, exists := seenIDs[tc.ID]; !exists {
					toolMsg, err := createPatchedToolMessage(ctx, gen, tc)
					if err != nil {
						return ctx, nil, err
					}
					currentMessages = append(currentMessages, toolMsg)
				}
			}
		}

		groupedMessages = append(groupedMessages, currentMessages)
		totalMsgCount += len(currentMessages)
	}

	patched := make([]*schema.Message, 0, totalMsgCount)
	for i := len(groupedMessages) - 1; i >= 0; i-- {
		patched = append(patched, groupedMessages[i]...)
	}

	nState := *state
	nState.Messages = patched
	return ctx, any(&nState).(*adk.TypedChatModelAgentState[M]), nil
}

func patchToolCallsForAgenticMessage[M adk.MessageType](ctx context.Context,
	gen func(ctx context.Context, toolName, toolCallID string) (string, error),
	state *adk.TypedChatModelAgentState[*schema.AgenticMessage],
	_ *adk.TypedModelContext[M],
) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	// seenIDs stores unique tool call IDs collected by reverse traversal
	seenIDs := make(map[string]struct{})
	groupedMessages := make([][]*schema.AgenticMessage, 0, len(state.Messages))
	totalMsgCount := 0

	// Iterate messages in reverse order to track existing tool call IDs
	for i := len(state.Messages) - 1; i >= 0; i-- {
		msg := state.Messages[i]
		currentMessages := []*schema.AgenticMessage{msg}
		currentToolIDs := make(map[string]struct{})

		for _, block := range msg.ContentBlocks {
			if block == nil {
				continue
			}
			if block.Type == schema.ContentBlockTypeFunctionToolResult && block.FunctionToolResult != nil {
				currentToolIDs[block.FunctionToolResult.CallID] = struct{}{}
			}
			if block.Type == schema.ContentBlockTypeToolSearchResult && block.ToolSearchFunctionToolResult != nil {
				currentToolIDs[block.ToolSearchFunctionToolResult.CallID] = struct{}{}
			}
			if block.Type == schema.ContentBlockTypeFunctionToolCall && block.FunctionToolCall != nil {
				if _, exists := seenIDs[block.FunctionToolCall.CallID]; !exists {
					toolMsg, err := createPatchedAgenticToolMessage(ctx, gen, block.FunctionToolCall.Name, block.FunctionToolCall.CallID)
					if err != nil {
						return ctx, nil, err
					}
					currentMessages = append(currentMessages, toolMsg)
				}
			}
		}
		maps.Copy(seenIDs, currentToolIDs)
		groupedMessages = append(groupedMessages, currentMessages)
		totalMsgCount += len(currentMessages)
	}

	patched := make([]*schema.AgenticMessage, 0, totalMsgCount)
	for i := len(groupedMessages) - 1; i >= 0; i-- {
		patched = append(patched, groupedMessages[i]...)
	}

	nState := *state
	nState.Messages = patched
	return ctx, any(&nState).(*adk.TypedChatModelAgentState[M]), nil
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
