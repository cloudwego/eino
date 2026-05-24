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
}

type patchedGenerators struct {
	content func(ctx context.Context, toolName, toolCallID string) (string, error)
	result  func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*PatchedToolResult, error)
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
		gens: patchedGenerators{
			content: cfg.PatchedContentGenerator,
			result:  cfg.PatchedToolResultGenerator,
		},
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
	gens patchedGenerators
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
		return patchToolCallsForMessage(ctx, m.gens, any(state).(*adk.TypedChatModelAgentState[*schema.Message]), mc)
	case *schema.AgenticMessage:
		return patchToolCallsForAgenticMessage(ctx, m.gens, any(state).(*adk.TypedChatModelAgentState[*schema.AgenticMessage]), mc)
	default:
		panic("unreachable: unknown MessageType")
	}
}

func patchToolCallsForMessage[M adk.MessageType](ctx context.Context,
	gens patchedGenerators,
	state *adk.TypedChatModelAgentState[*schema.Message],
	_ *adk.TypedModelContext[M],
) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	patched := make([]*schema.Message, 0, len(state.Messages))

	for i, msg := range state.Messages {
		patched = append(patched, msg)

		if msg.Role != schema.Assistant || len(msg.ToolCalls) == 0 {
			continue
		}

		for _, tc := range msg.ToolCalls {
			if hasCorrespondingToolMessage(state.Messages[i+1:], tc.ID) {
				continue
			}

			toolMsg, err := createPatchedToolMessage(ctx, gens, tc)
			if err != nil {
				return ctx, nil, err
			}
			adk.EnsureMessageID(toolMsg)
			patched = append(patched, toolMsg)

			// Emit MessageInserted so the synthetic tool result is persisted to the
			// session event log. On reconstruction it will be present, and the
			// dangling-call check below will skip re-insertion.
			if msgEvent, ok := any(&adk.TypedAgentEvent[*schema.Message]{
				SessionEvent: &adk.SessionEvent[*schema.Message]{
					Kind: adk.SessionEventMessageInserted,
					MessageInserted: &adk.MessageInsertedEvent[*schema.Message]{
						Message:         toolMsg,
						BeforeMessageID: "",
					},
				},
			}).(*adk.TypedAgentEvent[M]); ok {
				_ = adk.TypedSendEvent(ctx, msgEvent)
			}
		}
	}

	nState := *state
	nState.Messages = patched
	return ctx, any(&nState).(*adk.TypedChatModelAgentState[M]), nil
}

func patchToolCallsForAgenticMessage[M adk.MessageType](ctx context.Context,
	gens patchedGenerators,
	state *adk.TypedChatModelAgentState[*schema.AgenticMessage],
	_ *adk.TypedModelContext[M],
) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	patched := make([]*schema.AgenticMessage, 0, len(state.Messages))

	for i, msg := range state.Messages {
		patched = append(patched, msg)

		if msg.Role != schema.AgenticRoleTypeAssistant {
			continue
		}

		// Collect tool call IDs from this assistant message.
		var toolCalls []struct {
			callID string
			name   string
			args   string
		}
		for _, block := range msg.ContentBlocks {
			if block != nil && block.Type == schema.ContentBlockTypeFunctionToolCall && block.FunctionToolCall != nil {
				toolCalls = append(toolCalls, struct {
					callID string
					name   string
					args   string
				}{
					callID: block.FunctionToolCall.CallID,
					name:   block.FunctionToolCall.Name,
					args:   block.FunctionToolCall.Arguments,
				})
			}
		}
		if len(toolCalls) == 0 {
			continue
		}

		for _, tc := range toolCalls {
			if hasCorrespondingAgenticToolResult(state.Messages[i+1:], tc.callID) {
				continue
			}

			toolMsg, err := createPatchedAgenticToolMessage(ctx, gens, tc.name, tc.callID, tc.args)
			if err != nil {
				return ctx, nil, err
			}
			adk.EnsureMessageID(toolMsg)
			patched = append(patched, toolMsg)

			if msgEvent, ok := any(&adk.TypedAgentEvent[*schema.AgenticMessage]{
				SessionEvent: &adk.SessionEvent[*schema.AgenticMessage]{
					Kind: adk.SessionEventMessageInserted,
					MessageInserted: &adk.MessageInsertedEvent[*schema.AgenticMessage]{
						Message:         toolMsg,
						BeforeMessageID: "",
					},
				},
			}).(*adk.TypedAgentEvent[M]); ok {
				_ = adk.TypedSendEvent(ctx, msgEvent)
			}
		}
	}

	nState := *state
	nState.Messages = patched
	return ctx, any(&nState).(*adk.TypedChatModelAgentState[M]), nil
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
			if block == nil {
				continue
			}
			if block.Type == schema.ContentBlockTypeFunctionToolResult {
				hasToolResult = true
				if block.FunctionToolResult != nil && block.FunctionToolResult.CallID == toolCallID {
					return true
				}
			}
			if block.Type == schema.ContentBlockTypeToolSearchResult {
				hasToolResult = true
				if block.ToolSearchFunctionToolResult != nil && block.ToolSearchFunctionToolResult.CallID == toolCallID {
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

func createPatchedToolMessage(ctx context.Context, gens patchedGenerators, tc schema.ToolCall) (*schema.Message, error) {
	if gens.result != nil {
		arg := &schema.ToolArgument{Text: tc.Function.Arguments}
		result, err := gens.result(ctx, tc.Function.Name, tc.ID, arg)
		if err != nil {
			return nil, err
		}
		return patchedToolResultToMessage(tc.Function.Name, tc.ID, result)
	}
	if gens.content != nil {
		content, err := gens.content(ctx, tc.Function.Name, tc.ID)
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

func createPatchedAgenticToolMessage(ctx context.Context, gens patchedGenerators, toolName, callID, arguments string) (*schema.AgenticMessage, error) {
	if gens.result != nil {
		arg := &schema.ToolArgument{Text: arguments}
		result, err := gens.result(ctx, toolName, callID, arg)
		if err != nil {
			return nil, err
		}
		return patchedToolResultToAgenticMessage(toolName, callID, result)
	}

	var content string
	if gens.content != nil {
		var err error
		content, err = gens.content(ctx, toolName, callID)
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
