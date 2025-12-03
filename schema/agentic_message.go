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

package schema

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/cloudwego/eino/schema/claude"
	"github.com/cloudwego/eino/schema/gemini"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/schema/openai"

	"github.com/eino-contrib/jsonschema"
)

type ContentBlockType string

const (
	ContentBlockTypeReasoning               ContentBlockType = "reasoning"
	ContentBlockTypeUserInputText           ContentBlockType = "user_input_text"
	ContentBlockTypeUserInputImage          ContentBlockType = "user_input_image"
	ContentBlockTypeUserInputAudio          ContentBlockType = "user_input_audio"
	ContentBlockTypeUserInputVideo          ContentBlockType = "user_input_video"
	ContentBlockTypeUserInputFile           ContentBlockType = "user_input_file"
	ContentBlockTypeAssistantGenText        ContentBlockType = "assistant_gen_text"
	ContentBlockTypeAssistantGenImage       ContentBlockType = "assistant_gen_image"
	ContentBlockTypeAssistantGenAudio       ContentBlockType = "assistant_gen_audio"
	ContentBlockTypeAssistantGenVideo       ContentBlockType = "assistant_gen_video"
	ContentBlockTypeFunctionToolCall        ContentBlockType = "function_tool_call"
	ContentBlockTypeFunctionToolResult      ContentBlockType = "function_tool_result"
	ContentBlockTypeServerToolCall          ContentBlockType = "server_tool_call"
	ContentBlockTypeServerToolResult        ContentBlockType = "server_tool_result"
	ContentBlockTypeMCPToolCall             ContentBlockType = "mcp_tool_call"
	ContentBlockTypeMCPToolResult           ContentBlockType = "mcp_tool_result"
	ContentBlockTypeMCPListToolsResult      ContentBlockType = "mcp_list_tools_result"
	ContentBlockTypeMCPToolApprovalRequest  ContentBlockType = "mcp_tool_approval_request"
	ContentBlockTypeMCPToolApprovalResponse ContentBlockType = "mcp_tool_approval_response"
)

type AgenticRoleType string

const (
	AgenticRoleTypeDeveloper AgenticRoleType = "developer"
	AgenticRoleTypeSystem    AgenticRoleType = "system"
	AgenticRoleTypeUser      AgenticRoleType = "user"
	AgenticRoleTypeAssistant AgenticRoleType = "assistant"
)

type AgenticMessage struct {
	ResponseMeta *AgenticResponseMeta

	Role          AgenticRoleType
	ContentBlocks []*ContentBlock

	Extra map[string]any
}

type AgenticResponseMeta struct {
	TokenUsage *TokenUsage

	OpenAIExtension *openai.ResponseMetaExtension
	GeminiExtension *gemini.ResponseMetaExtension
	ClaudeExtension *claude.ResponseMetaExtension
	Extension       any
}

type StreamMeta struct {
	// Index specifies the index position of this block in the final response.
	Index int64
}

type ContentBlock struct {
	Type ContentBlockType

	Reasoning *Reasoning

	UserInputText  *UserInputText
	UserInputImage *UserInputImage
	UserInputAudio *UserInputAudio
	UserInputVideo *UserInputVideo
	UserInputFile  *UserInputFile

	AssistantGenText  *AssistantGenText
	AssistantGenImage *AssistantGenImage
	AssistantGenAudio *AssistantGenAudio
	AssistantGenVideo *AssistantGenVideo

	// FunctionToolCall holds invocation details for a user-defined tool.
	FunctionToolCall *FunctionToolCall
	// FunctionToolResult is the result from a user-defined tool call.
	FunctionToolResult *FunctionToolResult
	// ServerToolCall holds invocation details for a provider built-in tool run on the model server.
	ServerToolCall *ServerToolCall
	// ServerToolResult is the result from a provider built-in tool run on the model server.
	ServerToolResult *ServerToolResult

	// MCPToolCall holds invocation details for an MCP tool managed by the model server.
	MCPToolCall *MCPToolCall
	// MCPToolResult is the result from an MCP tool managed by the model server.
	MCPToolResult *MCPToolResult
	// MCPListToolsResult lists available MCP tools reported by the model server.
	MCPListToolsResult *MCPListToolsResult
	// MCPToolApprovalRequest requests user approval for an MCP tool call when required.
	MCPToolApprovalRequest *MCPToolApprovalRequest
	// MCPToolApprovalResponse records the user's approval decision for an MCP tool call.
	MCPToolApprovalResponse *MCPToolApprovalResponse

	StreamMeta *StreamMeta
}

type UserInputText struct {
	Text string

	// Extra stores additional information.
	Extra map[string]any
}

type UserInputImage struct {
	URL        string
	Base64Data string
	MIMEType   string
	Detail     ImageURLDetail

	// Extra stores additional information.
	Extra map[string]any
}

type UserInputAudio struct {
	URL        string
	Base64Data string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type UserInputVideo struct {
	URL        string
	Base64Data string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type UserInputFile struct {
	URL        string
	Name       string
	Base64Data string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type AssistantGenText struct {
	Text string

	OpenAIExtension *openai.AssistantGenTextExtension
	ClaudeExtension *claude.AssistantGenTextExtension
	Extension       any

	// Extra stores additional information.
	Extra map[string]any
}

type AssistantGenImage struct {
	URL        string
	Base64Data string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type AssistantGenAudio struct {
	URL        string
	Base64Data string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type AssistantGenVideo struct {
	URL        string
	Base64Data string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type Reasoning struct {
	// Summary is the reasoning content summary.
	Summary []*ReasoningSummary
	// EncryptedContent is the encrypted reasoning content.
	EncryptedContent string

	// Extra stores additional information.
	Extra map[string]any
}

type ReasoningSummary struct {
	// Index specifies the index position of this summary in the final Reasoning.
	Index int64

	Text string
}

type FunctionToolCall struct {
	// CallID is the unique identifier for the tool call.
	CallID string
	// Name specifies the function tool invoked.
	Name string
	// Arguments is the JSON string arguments for the function tool call.
	Arguments string

	// Extra stores additional information
	Extra map[string]any
}

type FunctionToolResult struct {
	// CallID is the unique identifier for the tool call.
	CallID string
	// Name specifies the function tool invoked.
	Name string
	// Result is the function tool result returned by the user
	Result string

	// Extra stores additional information.
	Extra map[string]any
}

type ServerToolCall struct {
	// Name specifies the server-side tool invoked.
	// Supplied by the model server (e.g., `web_search` for OpenAI, `googleSearch` for Gemini).
	Name string
	// CallID is the unique identifier for the tool call.
	// Empty if not provided by the model server.
	CallID string
	// Arguments are the raw inputs to the server-side tool,
	// supplied by the component implementer.
	Arguments any
	// Extra stores additional information.
	Extra map[string]any
}

type ServerToolResult struct {
	// Name specifies the server-side tool invoked.
	// Supplied by the model server (e.g., `web_search` for OpenAI, `googleSearch` for Gemini).
	Name string

	// CallID is the unique identifier for the tool call.
	// Empty if not provided by the model server.
	CallID string

	// Result refers to the raw output generated by the server-side tool,
	// supplied by the component implementer.
	Result any

	// Extra stores additional information.
	Extra map[string]any
}

type MCPToolCall struct {
	// ServerLabel is the MCP server label used to identify it in tool calls
	ServerLabel string
	// ApprovalRequestID is the unique ID of the approval request.
	ApprovalRequestID string
	// CallID is the unique ID of the tool call.
	CallID string
	// Name is the name of the tool to run.
	Name string
	// Arguments is the JSON string arguments for the tool call.
	Arguments string

	// Extra stores additional information.
	Extra map[string]any
}

type MCPToolResult struct {
	// ServerLabel is the MCP server label used to identify it in tool calls
	ServerLabel string
	// CallID is the unique ID of the tool call.
	CallID string
	// Name is the name of the tool to run.
	Name string
	// Result is the JSON string with the tool result.
	Result string
	// Error returned when the server fails to run the tool.
	Error *MCPToolCallError

	// Extra stores additional information.
	Extra map[string]any
}

type MCPToolCallError struct {
	Code    *int64
	Message string
}

type MCPListToolsResult struct {
	// ServerLabel is the MCP server label used to identify it in tool calls.
	ServerLabel string
	// Tools is the list of tools available on the server.
	Tools []*MCPListToolsItem
	// Error returned when the server fails to list tools.
	Error string

	// Extra stores additional information.
	Extra map[string]any
}

type MCPListToolsItem struct {
	// Name is the name of the tool.
	Name string
	// Description is the description of the tool.
	Description string
	// InputSchema is the JSON schema that describes the tool input.
	InputSchema *jsonschema.Schema
}

type MCPToolApprovalRequest struct {
	// ID is the approval request ID.
	ID string
	// Name is the name of the tool to run.
	Name string
	// Arguments is the JSON string arguments for the tool call.
	Arguments string
	// ServerLabel is the MCP server label used to identify it in tool calls.
	ServerLabel string

	// Extra stores additional information.
	Extra map[string]any
}

type MCPToolApprovalResponse struct {
	// ApprovalRequestID is the approval request ID being responded to.
	ApprovalRequestID string
	// Approve indicates whether the request is approved.
	Approve bool
	// Reason is the rationale for the decision.
	// Optional.
	Reason string

	// Extra stores additional information.
	Extra map[string]any
}

// DeveloperAgenticMessage represents a message with AgenticRoleType "developer".
func DeveloperAgenticMessage(text string) *AgenticMessage {
	return &AgenticMessage{
		Role:          AgenticRoleTypeDeveloper,
		ContentBlocks: []*ContentBlock{NewContentBlock(&UserInputText{Text: text})},
	}
}

// SystemAgenticMessage represents a message with AgenticRoleType "system".
func SystemAgenticMessage(text string) *AgenticMessage {
	return &AgenticMessage{
		Role:          AgenticRoleTypeSystem,
		ContentBlocks: []*ContentBlock{NewContentBlock(&UserInputText{Text: text})},
	}
}

// UserAgenticMessage represents a message with AgenticRoleType "user".
func UserAgenticMessage(text string) *AgenticMessage {
	return &AgenticMessage{
		Role:          AgenticRoleTypeUser,
		ContentBlocks: []*ContentBlock{NewContentBlock(&UserInputText{Text: text})},
	}
}

// FunctionToolResultAgenticMessage represents a function tool result message with AgenticRoleType "user".
func FunctionToolResultAgenticMessage(callID, name, result string) *AgenticMessage {
	return &AgenticMessage{
		Role: AgenticRoleTypeUser,
		ContentBlocks: []*ContentBlock{
			NewContentBlock(&FunctionToolResult{
				CallID: callID,
				Name:   name,
				Result: result,
			}),
		},
	}
}

func NewContentBlock(block any) *ContentBlock {
	switch b := block.(type) {
	case *Reasoning:
		return &ContentBlock{Type: ContentBlockTypeReasoning, Reasoning: b}
	case *UserInputText:
		return &ContentBlock{Type: ContentBlockTypeUserInputText, UserInputText: b}
	case *UserInputImage:
		return &ContentBlock{Type: ContentBlockTypeUserInputImage, UserInputImage: b}
	case *UserInputAudio:
		return &ContentBlock{Type: ContentBlockTypeUserInputAudio, UserInputAudio: b}
	case *UserInputVideo:
		return &ContentBlock{Type: ContentBlockTypeUserInputVideo, UserInputVideo: b}
	case *UserInputFile:
		return &ContentBlock{Type: ContentBlockTypeUserInputFile, UserInputFile: b}
	case *AssistantGenText:
		return &ContentBlock{Type: ContentBlockTypeAssistantGenText, AssistantGenText: b}
	case *AssistantGenImage:
		return &ContentBlock{Type: ContentBlockTypeAssistantGenImage, AssistantGenImage: b}
	case *AssistantGenAudio:
		return &ContentBlock{Type: ContentBlockTypeAssistantGenAudio, AssistantGenAudio: b}
	case *AssistantGenVideo:
		return &ContentBlock{Type: ContentBlockTypeAssistantGenVideo, AssistantGenVideo: b}
	case *FunctionToolCall:
		return &ContentBlock{Type: ContentBlockTypeFunctionToolCall, FunctionToolCall: b}
	case *FunctionToolResult:
		return &ContentBlock{Type: ContentBlockTypeFunctionToolResult, FunctionToolResult: b}
	case *ServerToolCall:
		return &ContentBlock{Type: ContentBlockTypeServerToolCall, ServerToolCall: b}
	case *ServerToolResult:
		return &ContentBlock{Type: ContentBlockTypeServerToolResult, ServerToolResult: b}
	case *MCPToolCall:
		return &ContentBlock{Type: ContentBlockTypeMCPToolCall, MCPToolCall: b}
	case *MCPToolResult:
		return &ContentBlock{Type: ContentBlockTypeMCPToolResult, MCPToolResult: b}
	case *MCPListToolsResult:
		return &ContentBlock{Type: ContentBlockTypeMCPListToolsResult, MCPListToolsResult: b}
	case *MCPToolApprovalRequest:
		return &ContentBlock{Type: ContentBlockTypeMCPToolApprovalRequest, MCPToolApprovalRequest: b}
	case *MCPToolApprovalResponse:
		return &ContentBlock{Type: ContentBlockTypeMCPToolApprovalResponse, MCPToolApprovalResponse: b}
	default:
		return nil
	}
}

// AgenticMessagesTemplate is the interface for messages template.
// It's used to render a template to a list of agentic messages.
// e.g.
//
//	chatTemplate := prompt.FromAgenticMessages(
//		&schema.AgenticMessage{
//			Role: schema.AgenticRoleTypeSystem,
//			ContentBlocks: []*schema.ContentBlock{
//				{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "you are an eino helper"}},
//			},
//		},
//		schema.AgenticMessagesPlaceholder("history", false), // <= this will use the value of "history" in params
//	)
//	msgs, err := chatTemplate.Format(ctx, params)
type AgenticMessagesTemplate interface {
	Format(ctx context.Context, vs map[string]any, formatType FormatType) ([]*AgenticMessage, error)
}

var _ AgenticMessagesTemplate = &AgenticMessage{}
var _ AgenticMessagesTemplate = AgenticMessagesPlaceholder("", false)

type agenticMessagesPlaceholder struct {
	key      string
	optional bool
}

// AgenticMessagesPlaceholder can render a placeholder to a list of agentic messages in params.
// e.g.
//
//	placeholder := AgenticMessagesPlaceholder("history", false)
//	params := map[string]any{
//		"history": []*schema.AgenticMessage{
//			&schema.AgenticMessage{
//				Role: schema.AgenticRoleTypeSystem,
//				ContentBlocks: []*schema.ContentBlock{
//					{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "you are an eino helper"}},
//				},
//			},
//		},
//	}
//	chatTemplate := chatTpl := prompt.FromMessages(
//		schema.AgenticMessagesPlaceholder("history", false), // <= this will use the value of "history" in params
//	)
//	msgs, err := chatTemplate.Format(ctx, params)
func AgenticMessagesPlaceholder(key string, optional bool) AgenticMessagesTemplate {
	return &agenticMessagesPlaceholder{
		key:      key,
		optional: optional,
	}
}

func (p *agenticMessagesPlaceholder) Format(_ context.Context, vs map[string]any, _ FormatType) ([]*AgenticMessage, error) {
	v, ok := vs[p.key]
	if !ok {
		if p.optional {
			return []*AgenticMessage{}, nil
		}

		return nil, fmt.Errorf("message placeholder format: %s not found", p.key)
	}

	msgs, ok := v.([]*AgenticMessage)
	if !ok {
		return nil, fmt.Errorf("only agentic messages can be used to format message placeholder, key: %v, actual type: %v", p.key, reflect.TypeOf(v))
	}

	return msgs, nil
}

// Format returns the agentic messages after rendering by the given formatType.
// It formats only the user input fields (UserInputText, UserInputImage, UserInputAudio, UserInputVideo, UserInputFile).
// e.g.
//
//	msg := &schema.AgenticMessage{
//		Role: schema.AgenticRoleTypeUser,
//		ContentBlocks: []*schema.ContentBlock{
//			{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "hello {name}"}},
//		},
//	}
//	msgs, err := msg.Format(ctx, map[string]any{"name": "eino"}, schema.FString)
//	// msgs[0].ContentBlocks[0].UserInputText.Text will be "hello eino"
func (m *AgenticMessage) Format(_ context.Context, vs map[string]any, formatType FormatType) ([]*AgenticMessage, error) {
	copied := *m

	if len(m.ContentBlocks) > 0 {
		copiedBlocks := make([]*ContentBlock, len(m.ContentBlocks))
		for i, block := range m.ContentBlocks {
			if block == nil {
				copiedBlocks[i] = nil
				continue
			}

			copiedBlock := *block
			var err error

			switch block.Type {
			case ContentBlockTypeUserInputText:
				if block.UserInputText != nil {
					copiedBlock.UserInputText, err = formatUserInputText(block.UserInputText, vs, formatType)
					if err != nil {
						return nil, err
					}
				}
			case ContentBlockTypeUserInputImage:
				if block.UserInputImage != nil {
					copiedBlock.UserInputImage, err = formatUserInputImage(block.UserInputImage, vs, formatType)
					if err != nil {
						return nil, err
					}
				}
			case ContentBlockTypeUserInputAudio:
				if block.UserInputAudio != nil {
					copiedBlock.UserInputAudio, err = formatUserInputAudio(block.UserInputAudio, vs, formatType)
					if err != nil {
						return nil, err
					}
				}
			case ContentBlockTypeUserInputVideo:
				if block.UserInputVideo != nil {
					copiedBlock.UserInputVideo, err = formatUserInputVideo(block.UserInputVideo, vs, formatType)
					if err != nil {
						return nil, err
					}
				}
			case ContentBlockTypeUserInputFile:
				if block.UserInputFile != nil {
					copiedBlock.UserInputFile, err = formatUserInputFile(block.UserInputFile, vs, formatType)
					if err != nil {
						return nil, err
					}
				}
			}

			copiedBlocks[i] = &copiedBlock
		}
		copied.ContentBlocks = copiedBlocks
	}

	return []*AgenticMessage{&copied}, nil
}

func formatUserInputText(uit *UserInputText, vs map[string]any, formatType FormatType) (*UserInputText, error) {
	text, err := formatContent(uit.Text, vs, formatType)
	if err != nil {
		return nil, err
	}
	copied := *uit
	copied.Text = text
	return &copied, nil
}

func formatUserInputImage(uii *UserInputImage, vs map[string]any, formatType FormatType) (*UserInputImage, error) {
	copied := *uii
	if uii.URL != "" {
		url, err := formatContent(uii.URL, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.URL = url
	}
	if uii.Base64Data != "" {
		base64data, err := formatContent(uii.Base64Data, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.Base64Data = base64data
	}
	return &copied, nil
}

func formatUserInputAudio(uia *UserInputAudio, vs map[string]any, formatType FormatType) (*UserInputAudio, error) {
	copied := *uia
	if uia.URL != "" {
		url, err := formatContent(uia.URL, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.URL = url
	}
	if uia.Base64Data != "" {
		base64data, err := formatContent(uia.Base64Data, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.Base64Data = base64data
	}
	return &copied, nil
}

func formatUserInputVideo(uiv *UserInputVideo, vs map[string]any, formatType FormatType) (*UserInputVideo, error) {
	copied := *uiv
	if uiv.URL != "" {
		url, err := formatContent(uiv.URL, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.URL = url
	}
	if uiv.Base64Data != "" {
		base64data, err := formatContent(uiv.Base64Data, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.Base64Data = base64data
	}
	return &copied, nil
}

func formatUserInputFile(uif *UserInputFile, vs map[string]any, formatType FormatType) (*UserInputFile, error) {
	copied := *uif
	if uif.URL != "" {
		url, err := formatContent(uif.URL, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.URL = url
	}
	if uif.Name != "" {
		name, err := formatContent(uif.Name, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.Name = name
	}
	if uif.Base64Data != "" {
		base64data, err := formatContent(uif.Base64Data, vs, formatType)
		if err != nil {
			return nil, err
		}
		copied.Base64Data = base64data
	}
	return &copied, nil
}

func ConcatAgenticMessagesArray(mas [][]*AgenticMessage) ([]*AgenticMessage, error) {
	return buildConcatGenericArray[AgenticMessage](ConcatAgenticMessages)(mas)
}

func ConcatAgenticMessages(msgs []*AgenticMessage) (*AgenticMessage, error) {
	var (
		role       AgenticRoleType
		blocksList [][]*ContentBlock
		blocks     []*ContentBlock
		metas      []*AgenticResponseMeta
	)

	if len(msgs) == 1 {
		return msgs[0], nil
	}

	for idx, msg := range msgs {
		if msg == nil {
			return nil, fmt.Errorf("message at index %d is nil", idx)
		}

		if msg.Role != "" {
			if role == "" {
				role = msg.Role
			} else if role != msg.Role {
				return nil, fmt.Errorf("cannot concat messages with different roles: got '%s' and '%s'", role, msg.Role)
			}
		}

		for _, block := range msg.ContentBlocks {
			if block.StreamMeta == nil {
				// Non-streaming block
				if len(blocksList) > 0 {
					// Cannot mix streaming and non-streaming blocks
					return nil, fmt.Errorf("found non-streaming block after streaming blocks")
				}
				// Collect non-streaming block
				blocks = append(blocks, block)
			} else {
				// Streaming block
				if len(blocks) > 0 {
					// Cannot mix non-streaming and streaming blocks
					return nil, fmt.Errorf("found streaming block after non-streaming blocks")
				}
				// Collect streaming block by index
				blocksList = expandSlice(int(block.StreamMeta.Index), blocksList)
				blocksList[block.StreamMeta.Index] = append(blocksList[block.StreamMeta.Index], block)
			}
		}

		if msg.ResponseMeta != nil {
			metas = append(metas, msg.ResponseMeta)
		}
	}

	meta, err := concatAgenticResponseMeta(metas)
	if err != nil {
		return nil, fmt.Errorf("failed to concat agentic response meta: %w", err)
	}

	if len(blocksList) > 0 {
		// All blocks are streaming, concat each group by index
		blocks = make([]*ContentBlock, len(blocksList))
		for i, bs := range blocksList {
			if len(bs) == 0 {
				continue
			}
			b, err := concatAgenticContentBlocks(bs)
			if err != nil {
				return nil, fmt.Errorf("failed to concat content blocks at index %d: %w", i, err)
			}
			blocks[i] = b
		}
	}

	for i := 0; i < len(blocks); i++ {
		if blocks[i] == nil {
			blocks = append(blocks[:i], blocks[i+1:]...)
		}
	}

	return &AgenticMessage{
		ResponseMeta:  meta,
		Role:          role,
		ContentBlocks: blocks,
	}, nil
}

func concatAgenticResponseMeta(metas []*AgenticResponseMeta) (*AgenticResponseMeta, error) {
	if len(metas) == 0 {
		return nil, nil
	}
	ret := &AgenticResponseMeta{
		TokenUsage:      &TokenUsage{},
		OpenAIExtension: nil,
		ClaudeExtension: nil,
		GeminiExtension: nil,
		Extension:       nil,
	}
	for _, meta := range metas {
		ret.Extension = meta.Extension
		ret.OpenAIExtension = meta.OpenAIExtension
		ret.ClaudeExtension = meta.ClaudeExtension
		ret.GeminiExtension = meta.GeminiExtension
		if meta.TokenUsage != nil {
			ret.TokenUsage.CompletionTokens += meta.TokenUsage.CompletionTokens
			ret.TokenUsage.CompletionTokenDetails.ReasoningTokens += meta.TokenUsage.CompletionTokenDetails.ReasoningTokens
			ret.TokenUsage.PromptTokens += meta.TokenUsage.PromptTokens
			ret.TokenUsage.PromptTokenDetails.CachedTokens += meta.TokenUsage.PromptTokenDetails.CachedTokens
			ret.TokenUsage.TotalTokens += meta.TokenUsage.TotalTokens
		}
	}
	return ret, nil
}

func concatAgenticContentBlocks(blocks []*ContentBlock) (*ContentBlock, error) {
	if len(blocks) == 0 {
		return nil, fmt.Errorf("no content blocks to concat")
	}
	blockType := blocks[0].Type
	index := blocks[0].StreamMeta.Index
	switch blockType {
	case ContentBlockTypeReasoning:
		return concatContentBlockHelper(blocks, blockType, "reasoning",
			func(b *ContentBlock) *Reasoning { return b.Reasoning },
			concatReasoning,
			func(r *Reasoning) *ContentBlock {
				return &ContentBlock{Type: blockType, Reasoning: r, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeUserInputText:
		return concatContentBlockHelper(blocks, blockType, "user input text",
			func(b *ContentBlock) *UserInputText { return b.UserInputText },
			concatUserInputText,
			func(t *UserInputText) *ContentBlock {
				return &ContentBlock{Type: blockType, UserInputText: t, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeUserInputImage:
		return concatContentBlockHelper(blocks, blockType, "user input image",
			func(b *ContentBlock) *UserInputImage { return b.UserInputImage },
			concatUserInputImage,
			func(i *UserInputImage) *ContentBlock {
				return &ContentBlock{Type: blockType, UserInputImage: i, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeUserInputAudio:
		return concatContentBlockHelper(blocks, blockType, "user input audio",
			func(b *ContentBlock) *UserInputAudio { return b.UserInputAudio },
			concatUserInputAudio,
			func(a *UserInputAudio) *ContentBlock {
				return &ContentBlock{Type: blockType, UserInputAudio: a, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeUserInputVideo:
		return concatContentBlockHelper(blocks, blockType, "user input video",
			func(b *ContentBlock) *UserInputVideo { return b.UserInputVideo },
			concatUserInputVideo,
			func(v *UserInputVideo) *ContentBlock {
				return &ContentBlock{Type: blockType, UserInputVideo: v, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeUserInputFile:
		return concatContentBlockHelper(blocks, blockType, "user input file",
			func(b *ContentBlock) *UserInputFile { return b.UserInputFile },
			concatUserInputFile,
			func(f *UserInputFile) *ContentBlock {
				return &ContentBlock{Type: blockType, UserInputFile: f, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeAssistantGenText:
		return concatContentBlockHelper(blocks, blockType, "assistant gen text",
			func(b *ContentBlock) *AssistantGenText { return b.AssistantGenText },
			concatAssistantGenText,
			func(t *AssistantGenText) *ContentBlock {
				return &ContentBlock{Type: blockType, AssistantGenText: t, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeAssistantGenImage:
		return concatContentBlockHelper(blocks, blockType, "assistant gen image",
			func(b *ContentBlock) *AssistantGenImage { return b.AssistantGenImage },
			concatAssistantGenImage,
			func(i *AssistantGenImage) *ContentBlock {
				return &ContentBlock{Type: blockType, AssistantGenImage: i, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeAssistantGenAudio:
		return concatContentBlockHelper(blocks, blockType, "assistant gen audio",
			func(b *ContentBlock) *AssistantGenAudio { return b.AssistantGenAudio },
			concatAssistantGenAudio,
			func(a *AssistantGenAudio) *ContentBlock {
				return &ContentBlock{Type: blockType, AssistantGenAudio: a, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeAssistantGenVideo:
		return concatContentBlockHelper(blocks, blockType, "assistant gen video",
			func(b *ContentBlock) *AssistantGenVideo { return b.AssistantGenVideo },
			concatAssistantGenVideo,
			func(v *AssistantGenVideo) *ContentBlock {
				return &ContentBlock{Type: blockType, AssistantGenVideo: v, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeFunctionToolCall:
		return concatContentBlockHelper(blocks, blockType, "function tool call",
			func(b *ContentBlock) *FunctionToolCall { return b.FunctionToolCall },
			concatFunctionToolCall,
			func(c *FunctionToolCall) *ContentBlock {
				return &ContentBlock{Type: blockType, FunctionToolCall: c, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeFunctionToolResult:
		return concatContentBlockHelper(blocks, blockType, "function tool result",
			func(b *ContentBlock) *FunctionToolResult { return b.FunctionToolResult },
			concatFunctionToolResult,
			func(r *FunctionToolResult) *ContentBlock {
				return &ContentBlock{Type: blockType, FunctionToolResult: r, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeServerToolCall:
		return concatContentBlockHelper(blocks, blockType, "server tool call",
			func(b *ContentBlock) *ServerToolCall { return b.ServerToolCall },
			concatServerToolCall,
			func(c *ServerToolCall) *ContentBlock {
				return &ContentBlock{Type: blockType, ServerToolCall: c, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeServerToolResult:
		return concatContentBlockHelper(blocks, blockType, "server tool result",
			func(b *ContentBlock) *ServerToolResult { return b.ServerToolResult },
			concatServerToolResult,
			func(r *ServerToolResult) *ContentBlock {
				return &ContentBlock{Type: blockType, ServerToolResult: r, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeMCPToolCall:
		return concatContentBlockHelper(blocks, blockType, "MCP tool call",
			func(b *ContentBlock) *MCPToolCall { return b.MCPToolCall },
			concatMCPToolCall,
			func(c *MCPToolCall) *ContentBlock {
				return &ContentBlock{Type: blockType, MCPToolCall: c, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeMCPToolResult:
		return concatContentBlockHelper(blocks, blockType, "MCP tool result",
			func(b *ContentBlock) *MCPToolResult { return b.MCPToolResult },
			concatMCPToolResult,
			func(r *MCPToolResult) *ContentBlock {
				return &ContentBlock{Type: blockType, MCPToolResult: r, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeMCPListToolsResult:
		return concatContentBlockHelper(blocks, blockType, "MCP list tools",
			func(b *ContentBlock) *MCPListToolsResult { return b.MCPListToolsResult },
			concatMCPListToolsResult,
			func(r *MCPListToolsResult) *ContentBlock {
				return &ContentBlock{Type: blockType, MCPListToolsResult: r, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeMCPToolApprovalRequest:
		return concatContentBlockHelper(blocks, blockType, "MCP tool approval request",
			func(b *ContentBlock) *MCPToolApprovalRequest { return b.MCPToolApprovalRequest },
			concatMCPToolApprovalRequest,
			func(r *MCPToolApprovalRequest) *ContentBlock {
				return &ContentBlock{Type: blockType, MCPToolApprovalRequest: r, StreamMeta: &StreamMeta{Index: index}}
			})

	case ContentBlockTypeMCPToolApprovalResponse:
		return concatContentBlockHelper(blocks, blockType, "MCP tool approval response",
			func(b *ContentBlock) *MCPToolApprovalResponse { return b.MCPToolApprovalResponse },
			concatMCPToolApprovalResponse,
			func(r *MCPToolApprovalResponse) *ContentBlock {
				return &ContentBlock{Type: blockType, MCPToolApprovalResponse: r, StreamMeta: &StreamMeta{Index: index}}
			})

	default:
		return nil, fmt.Errorf("unknown content block type: %s", blockType)
	}
}

// concatContentBlockHelper is a generic helper function that reduces code duplication
// for concatenating content blocks of a specific type.
func concatContentBlockHelper[T any](
	blocks []*ContentBlock,
	expectedType ContentBlockType,
	typeName string,
	getter func(*ContentBlock) *T,
	concatFunc func([]*T) (*T, error),
	constructor func(*T) *ContentBlock,
) (*ContentBlock, error) {
	items, err := genericGetTFromContentBlocks(blocks, func(block *ContentBlock) (*T, error) {
		if block.Type != expectedType {
			return nil, fmt.Errorf("expected %s block, got %s", typeName, block.Type)
		}
		item := getter(block)
		if item == nil {
			return nil, fmt.Errorf("%s content is nil", typeName)
		}
		return item, nil
	})
	if err != nil {
		return nil, err
	}

	concatenated, err := concatFunc(items)
	if err != nil {
		return nil, err
	}

	return constructor(concatenated), nil
}

func genericGetTFromContentBlocks[T any](blocks []*ContentBlock, checkAndGetter func(block *ContentBlock) (T, error)) ([]T, error) {
	ret := make([]T, 0, len(blocks))
	for _, block := range blocks {
		t, err := checkAndGetter(block)
		if err != nil {
			return nil, err
		}
		ret = append(ret, t)
	}
	return ret, nil
}

// Concatenation strategies for different content block types:
//
// String concatenation (incremental streaming):
//   - Reasoning: Summary texts are concatenated, grouped by Index if present
//   - UserInputText: Text fields are concatenated
//   - AssistantGenText: Text fields are concatenated, annotations/citations are merged
//   - FunctionToolCall: Arguments (JSON strings) are concatenated incrementally
//   - FunctionToolResult: Result strings are concatenated
//   - ServerToolCall: Arguments are merged (last non-nil value for any type)
//   - ServerToolResult: Results are merged using internal.ConcatItems
//   - MCPToolCall: Arguments (JSON strings) are concatenated incrementally
//   - MCPToolResult: Result strings are concatenated
//   - MCPListToolsResult: Tools arrays are merged
//   - MCPToolApprovalRequest: Arguments are concatenated
//
// Take last block (non-streaming content):
//   - UserInputImage, UserInputAudio, UserInputVideo, UserInputFile: Return last block
//   - AssistantGenImage, AssistantGenAudio, AssistantGenVideo: Return last block
//   - MCPToolApprovalResponse: Return last block
//

func concatReasoning(reasons []*Reasoning) (*Reasoning, error) {
	if len(reasons) == 0 {
		return nil, fmt.Errorf("no reasoning found")
	}
	if len(reasons) == 1 {
		return reasons[0], nil
	}

	ret := &Reasoning{
		Summary:          make([]*ReasoningSummary, 0),
		EncryptedContent: "",
		Extra:            make(map[string]any),
	}

	// Collect all summaries from all reasons
	allSummaries := make([]*ReasoningSummary, 0)
	for _, r := range reasons {
		if r == nil {
			continue
		}
		allSummaries = append(allSummaries, r.Summary...)
		if r.EncryptedContent != "" {
			ret.EncryptedContent += r.EncryptedContent
		}
		for k, v := range r.Extra {
			ret.Extra[k] = v
		}
	}

	// Group by Index and concatenate Text for same Index
	// Use dynamic array that expands as needed
	var summaryArray []*ReasoningSummary
	for _, s := range allSummaries {
		idx := s.Index
		// Expand array if needed
		summaryArray = expandSlice(int(idx), summaryArray)
		if summaryArray[idx] == nil {
			// Create new entry with a copy of Index
			summaryArray[idx] = &ReasoningSummary{
				Index: idx,
				Text:  s.Text,
			}
		} else {
			// Concatenate text for same index
			summaryArray[idx].Text += s.Text
		}
	}

	// Convert array to slice, filtering out nil entries
	ret.Summary = make([]*ReasoningSummary, 0, len(summaryArray))
	for _, summary := range summaryArray {
		if summary != nil {
			ret.Summary = append(ret.Summary, summary)
		}
	}

	return ret, nil
}

func concatUserInputText(texts []*UserInputText) (*UserInputText, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("no user input text found")
	}
	if len(texts) == 1 {
		return texts[0], nil
	}

	ret := &UserInputText{
		Text:  "",
		Extra: make(map[string]any),
	}

	for _, t := range texts {
		if t == nil {
			continue
		}
		ret.Text += t.Text
		for k, v := range t.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatUserInputImage(images []*UserInputImage) (*UserInputImage, error) {
	if len(images) == 0 {
		return nil, fmt.Errorf("no user input image found")
	}
	return images[len(images)-1], nil
}

func concatUserInputAudio(audios []*UserInputAudio) (*UserInputAudio, error) {
	if len(audios) == 0 {
		return nil, fmt.Errorf("no user input audio found")
	}
	return audios[len(audios)-1], nil
}

func concatUserInputVideo(videos []*UserInputVideo) (*UserInputVideo, error) {
	if len(videos) == 0 {
		return nil, fmt.Errorf("no user input video found")
	}
	return videos[len(videos)-1], nil
}

func concatUserInputFile(files []*UserInputFile) (*UserInputFile, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no user input file found")
	}
	return files[len(files)-1], nil
}

func concatAssistantGenText(texts []*AssistantGenText) (*AssistantGenText, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("no assistant gen text found")
	}
	if len(texts) == 1 {
		return texts[0], nil
	}

	ret := &AssistantGenText{
		Text:            "",
		OpenAIExtension: nil,
		ClaudeExtension: nil,
		Extra:           make(map[string]any),
	}

	for _, t := range texts {
		if t == nil {
			continue
		}
		ret.Text += t.Text
		if t.OpenAIExtension != nil {
			if ret.OpenAIExtension == nil {
				ret.OpenAIExtension = &openai.AssistantGenTextExtension{}
			}
			ret.OpenAIExtension.Annotations = append(ret.OpenAIExtension.Annotations, t.OpenAIExtension.Annotations...)
		}
		if t.ClaudeExtension != nil {
			if ret.ClaudeExtension == nil {
				ret.ClaudeExtension = &claude.AssistantGenTextExtension{}
			}
			ret.ClaudeExtension.Citations = append(ret.ClaudeExtension.Citations, t.ClaudeExtension.Citations...)
		}
		for k, v := range t.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatAssistantGenImage(images []*AssistantGenImage) (*AssistantGenImage, error) {
	if len(images) == 0 {
		return nil, fmt.Errorf("no assistant gen image found")
	}
	return images[len(images)-1], nil
}

func concatAssistantGenAudio(audios []*AssistantGenAudio) (*AssistantGenAudio, error) {
	if len(audios) == 0 {
		return nil, fmt.Errorf("no assistant gen audio found")
	}
	return audios[len(audios)-1], nil
}

func concatAssistantGenVideo(videos []*AssistantGenVideo) (*AssistantGenVideo, error) {
	if len(videos) == 0 {
		return nil, fmt.Errorf("no assistant gen video found")
	}
	return videos[len(videos)-1], nil
}

func concatFunctionToolCall(calls []*FunctionToolCall) (*FunctionToolCall, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("no function tool call found")
	}
	if len(calls) == 1 {
		return calls[0], nil
	}

	// For tool calls, arguments are typically built incrementally during streaming
	ret := &FunctionToolCall{
		Extra: make(map[string]any),
	}

	for _, c := range calls {
		if c == nil {
			continue
		}
		if ret.CallID == "" {
			ret.CallID = c.CallID
		}
		if ret.Name == "" {
			ret.Name = c.Name
		}
		ret.Arguments += c.Arguments
		for k, v := range c.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatFunctionToolResult(results []*FunctionToolResult) (*FunctionToolResult, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("no function tool result found")
	}
	if len(results) == 1 {
		return results[0], nil
	}

	ret := &FunctionToolResult{
		Extra: make(map[string]any),
	}

	for _, r := range results {
		if r == nil {
			continue
		}
		if ret.CallID == "" {
			ret.CallID = r.CallID
		}
		if ret.Name == "" {
			ret.Name = r.Name
		}
		ret.Result += r.Result
		for k, v := range r.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatServerToolCall(calls []*ServerToolCall) (*ServerToolCall, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("no server tool call found")
	}
	if len(calls) == 1 {
		return calls[0], nil
	}

	// ServerToolCall Arguments is of type any; merge strategy uses the last non-nil value
	ret := &ServerToolCall{
		Extra: make(map[string]any),
	}

	for _, c := range calls {
		if c == nil {
			continue
		}
		if ret.Name == "" {
			ret.Name = c.Name
		}
		if ret.CallID == "" {
			ret.CallID = c.CallID
		}
		if c.Arguments != nil {
			ret.Arguments = c.Arguments
		}
		for k, v := range c.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatServerToolResult(results []*ServerToolResult) (*ServerToolResult, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("no server tool result found")
	}
	if len(results) == 1 {
		return results[0], nil
	}

	// ServerToolResult Result is of type any; merge strategy uses the last non-nil value
	ret := &ServerToolResult{
		Extra: make(map[string]any),
	}

	tZeroResult := reflect.TypeOf(results[0].Result)
	data := reflect.MakeSlice(reflect.SliceOf(tZeroResult), 0, 0)
	for _, r := range results {
		if r == nil {
			continue
		}
		if ret.Name == "" {
			ret.Name = r.Name
		}
		if ret.CallID == "" {
			ret.CallID = r.CallID
		}
		if r.Result != nil {
			vResult := reflect.ValueOf(r.Result)
			if tZeroResult != vResult.Type() {
				return nil, fmt.Errorf("tool result types are different: %v  %v", tZeroResult, vResult.Type())
			}
			data = reflect.Append(data, vResult)
		}
		for k, v := range r.Extra {
			ret.Extra[k] = v
		}
	}

	d, err := internal.ConcatSliceValue(data)
	if err != nil {
		return nil, fmt.Errorf("failed to concat server tool result: %v", err)
	}
	ret.Result = d

	return ret, nil
}

func concatMCPToolCall(calls []*MCPToolCall) (*MCPToolCall, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("no mcp tool call found")
	}
	if len(calls) == 1 {
		return calls[0], nil
	}

	ret := &MCPToolCall{
		Extra: make(map[string]any),
	}

	for _, c := range calls {
		if c == nil {
			continue
		}
		if ret.ServerLabel == "" {
			ret.ServerLabel = c.ServerLabel
		}
		if ret.ApprovalRequestID == "" {
			ret.ApprovalRequestID = c.ApprovalRequestID
		}
		if ret.CallID == "" {
			ret.CallID = c.CallID
		}
		if ret.Name == "" {
			ret.Name = c.Name
		}
		ret.Arguments += c.Arguments
		for k, v := range c.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatMCPToolResult(results []*MCPToolResult) (*MCPToolResult, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("no mcp tool result found")
	}
	if len(results) == 1 {
		return results[0], nil
	}

	ret := &MCPToolResult{
		Extra: make(map[string]any),
	}

	for _, r := range results {
		if r == nil {
			continue
		}
		if ret.CallID == "" {
			ret.CallID = r.CallID
		}
		if ret.Name == "" {
			ret.Name = r.Name
		}
		ret.Result += r.Result
		if r.Error != nil {
			ret.Error = r.Error // Use the last error
		}
		for k, v := range r.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatMCPListToolsResult(results []*MCPListToolsResult) (*MCPListToolsResult, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("no mcp list tools result found")
	}
	if len(results) == 1 {
		return results[0], nil
	}

	ret := &MCPListToolsResult{
		Tools: make([]*MCPListToolsItem, 0),
		Extra: make(map[string]any),
	}

	for _, r := range results {
		if r == nil {
			continue
		}
		if ret.ServerLabel == "" {
			ret.ServerLabel = r.ServerLabel
		}
		ret.Tools = append(ret.Tools, r.Tools...)
		if r.Error != "" {
			ret.Error = r.Error // Use the last error
		}
		for k, v := range r.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatMCPToolApprovalRequest(requests []*MCPToolApprovalRequest) (*MCPToolApprovalRequest, error) {
	if len(requests) == 0 {
		return nil, fmt.Errorf("no mcp tool approval request found")
	}
	if len(requests) == 1 {
		return requests[0], nil
	}

	ret := &MCPToolApprovalRequest{
		Extra: make(map[string]any),
	}

	for _, r := range requests {
		if r == nil {
			continue
		}
		if ret.ID == "" {
			ret.ID = r.ID
		}
		if ret.Name == "" {
			ret.Name = r.Name
		}
		ret.Arguments += r.Arguments
		if ret.ServerLabel == "" {
			ret.ServerLabel = r.ServerLabel
		}
		for k, v := range r.Extra {
			ret.Extra[k] = v
		}
	}

	return ret, nil
}

func concatMCPToolApprovalResponse(responses []*MCPToolApprovalResponse) (*MCPToolApprovalResponse, error) {
	if len(responses) == 0 {
		return nil, fmt.Errorf("no mcp tool approval response found")
	}
	if len(responses) == 1 {
		return responses[0], nil
	}

	return responses[len(responses)-1], nil
}

func expandSlice[T any](idx int, s []T) []T {
	if len(s) > idx {
		return s
	}
	return append(s, make([]T, idx-len(s)+1)...)
}

func (m *AgenticMessage) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("role: %s\n", m.Role))

	if len(m.ContentBlocks) > 0 {
		sb.WriteString("content_blocks:\n")
		for i, block := range m.ContentBlocks {
			if block == nil {
				continue
			}
			sb.WriteString(fmt.Sprintf("  [%d] %s", i, block.String()))
		}
	}

	if m.ResponseMeta != nil {
		sb.WriteString(m.ResponseMeta.String())
	}

	return sb.String()
}

func (b *ContentBlock) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("type: %s\n", b.Type))

	switch b.Type {
	case ContentBlockTypeReasoning:
		if b.Reasoning != nil {
			sb.WriteString(b.Reasoning.String())
		}
	case ContentBlockTypeUserInputText:
		if b.UserInputText != nil {
			sb.WriteString(b.UserInputText.String())
		}
	case ContentBlockTypeUserInputImage:
		if b.UserInputImage != nil {
			sb.WriteString(b.UserInputImage.String())
		}
	case ContentBlockTypeUserInputAudio:
		if b.UserInputAudio != nil {
			sb.WriteString(b.UserInputAudio.String())
		}
	case ContentBlockTypeUserInputVideo:
		if b.UserInputVideo != nil {
			sb.WriteString(b.UserInputVideo.String())
		}
	case ContentBlockTypeUserInputFile:
		if b.UserInputFile != nil {
			sb.WriteString(b.UserInputFile.String())
		}
	case ContentBlockTypeAssistantGenText:
		if b.AssistantGenText != nil {
			sb.WriteString(b.AssistantGenText.String())
		}
	case ContentBlockTypeAssistantGenImage:
		if b.AssistantGenImage != nil {
			sb.WriteString(b.AssistantGenImage.String())
		}
	case ContentBlockTypeAssistantGenAudio:
		if b.AssistantGenAudio != nil {
			sb.WriteString(b.AssistantGenAudio.String())
		}
	case ContentBlockTypeAssistantGenVideo:
		if b.AssistantGenVideo != nil {
			sb.WriteString(b.AssistantGenVideo.String())
		}
	case ContentBlockTypeFunctionToolCall:
		if b.FunctionToolCall != nil {
			sb.WriteString(b.FunctionToolCall.String())
		}
	case ContentBlockTypeFunctionToolResult:
		if b.FunctionToolResult != nil {
			sb.WriteString(b.FunctionToolResult.String())
		}
	case ContentBlockTypeServerToolCall:
		if b.ServerToolCall != nil {
			sb.WriteString(b.ServerToolCall.String())
		}
	case ContentBlockTypeServerToolResult:
		if b.ServerToolResult != nil {
			sb.WriteString(b.ServerToolResult.String())
		}
	case ContentBlockTypeMCPToolCall:
		if b.MCPToolCall != nil {
			sb.WriteString(b.MCPToolCall.String())
		}
	case ContentBlockTypeMCPToolResult:
		if b.MCPToolResult != nil {
			sb.WriteString(b.MCPToolResult.String())
		}
	case ContentBlockTypeMCPListToolsResult:
		if b.MCPListToolsResult != nil {
			sb.WriteString(b.MCPListToolsResult.String())
		}
	case ContentBlockTypeMCPToolApprovalRequest:
		if b.MCPToolApprovalRequest != nil {
			sb.WriteString(b.MCPToolApprovalRequest.String())
		}
	case ContentBlockTypeMCPToolApprovalResponse:
		if b.MCPToolApprovalResponse != nil {
			sb.WriteString(b.MCPToolApprovalResponse.String())
		}
	}

	if b.StreamMeta != nil {
		sb.WriteString(fmt.Sprintf("      stream_index: %d\n", b.StreamMeta.Index))
	}

	return sb.String()
}

func (r *Reasoning) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      summary: %d items\n", len(r.Summary)))
	for _, s := range r.Summary {
		sb.WriteString(fmt.Sprintf("        [%d] %s\n", s.Index, s.Text))
	}
	if r.EncryptedContent != "" {
		sb.WriteString(fmt.Sprintf("      encrypted_content: %s\n", truncateString(r.EncryptedContent, 50)))
	}
	return sb.String()
}

func (u *UserInputText) String() string {
	return fmt.Sprintf("      text: %s\n", u.Text)
}

func (u *UserInputImage) String() string {
	return formatMediaString(u.URL, u.Base64Data, u.MIMEType, u.Detail)
}

func (u *UserInputAudio) String() string {
	return formatMediaString(u.URL, u.Base64Data, u.MIMEType, "")
}

func (u *UserInputVideo) String() string {
	return formatMediaString(u.URL, u.Base64Data, u.MIMEType, "")
}

func (u *UserInputFile) String() string {
	sb := &strings.Builder{}
	if u.Name != "" {
		sb.WriteString(fmt.Sprintf("      name: %s\n", u.Name))
	}
	sb.WriteString(formatMediaString(u.URL, u.Base64Data, u.MIMEType, ""))
	return sb.String()
}

func (a *AssistantGenText) String() string {
	return fmt.Sprintf("      text: %s\n", a.Text)
}

func (a *AssistantGenImage) String() string {
	return formatMediaString(a.URL, a.Base64Data, a.MIMEType, "")
}

func (a *AssistantGenAudio) String() string {
	return formatMediaString(a.URL, a.Base64Data, a.MIMEType, "")
}

func (a *AssistantGenVideo) String() string {
	return formatMediaString(a.URL, a.Base64Data, a.MIMEType, "")
}

func (f *FunctionToolCall) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      call_id: %s\n", f.CallID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", f.Name))
	sb.WriteString(fmt.Sprintf("      arguments: %s\n", f.Arguments))
	return sb.String()
}

func (f *FunctionToolResult) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      call_id: %s\n", f.CallID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", f.Name))
	sb.WriteString(fmt.Sprintf("      result: %s\n", f.Result))
	return sb.String()
}

func (s *ServerToolCall) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      name: %s\n", s.Name))
	if s.CallID != "" {
		sb.WriteString(fmt.Sprintf("      call_id: %s\n", s.CallID))
	}
	sb.WriteString(fmt.Sprintf("      arguments: %v\n", s.Arguments))
	return sb.String()
}

func (s *ServerToolResult) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      name: %s\n", s.Name))
	if s.CallID != "" {
		sb.WriteString(fmt.Sprintf("      call_id: %s\n", s.CallID))
	}
	sb.WriteString(fmt.Sprintf("      result: %v\n", s.Result))
	return sb.String()
}

func (m *MCPToolCall) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      server_label: %s\n", m.ServerLabel))
	sb.WriteString(fmt.Sprintf("      call_id: %s\n", m.CallID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", m.Name))
	sb.WriteString(fmt.Sprintf("      arguments: %s\n", m.Arguments))
	if m.ApprovalRequestID != "" {
		sb.WriteString(fmt.Sprintf("      approval_request_id: %s\n", m.ApprovalRequestID))
	}
	return sb.String()
}

func (m *MCPToolResult) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      call_id: %s\n", m.CallID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", m.Name))
	sb.WriteString(fmt.Sprintf("      result: %s\n", m.Result))
	if m.Error != nil {
		sb.WriteString(fmt.Sprintf("      error: [%d] %s\n", *m.Error.Code, m.Error.Message))
	}
	return sb.String()
}

func (m *MCPListToolsResult) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      server_label: %s\n", m.ServerLabel))
	sb.WriteString(fmt.Sprintf("      tools: %d items\n", len(m.Tools)))
	for _, tool := range m.Tools {
		sb.WriteString(fmt.Sprintf("        - %s: %s\n", tool.Name, tool.Description))
	}
	if m.Error != "" {
		sb.WriteString(fmt.Sprintf("      error: %s\n", m.Error))
	}
	return sb.String()
}

func (m *MCPToolApprovalRequest) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      server_label: %s\n", m.ServerLabel))
	sb.WriteString(fmt.Sprintf("      id: %s\n", m.ID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", m.Name))
	sb.WriteString(fmt.Sprintf("      arguments: %s\n", m.Arguments))
	return sb.String()
}

func (m *MCPToolApprovalResponse) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      approval_request_id: %s\n", m.ApprovalRequestID))
	sb.WriteString(fmt.Sprintf("      approve: %v\n", m.Approve))
	if m.Reason != "" {
		sb.WriteString(fmt.Sprintf("      reason: %s\n", m.Reason))
	}
	return sb.String()
}

func (a *AgenticResponseMeta) String() string {
	sb := &strings.Builder{}
	sb.WriteString("response_meta:\n")
	if a.TokenUsage != nil {
		sb.WriteString(fmt.Sprintf("  token_usage: prompt=%d, completion=%d, total=%d\n",
			a.TokenUsage.PromptTokens,
			a.TokenUsage.CompletionTokens,
			a.TokenUsage.TotalTokens))
	}
	return sb.String()
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// formatMediaString formats URL, Base64Data, MIMEType and Detail for media content
func formatMediaString(url, base64Data string, mimeType string, detail any) string {
	sb := &strings.Builder{}
	if url != "" {
		sb.WriteString(fmt.Sprintf("      url: %s\n", truncateString(url, 100)))
	}
	if base64Data != "" {
		// Only show first few characters of base64 data
		sb.WriteString(fmt.Sprintf("      base64_data: %s... (%d bytes)\n", truncateString(base64Data, 20), len(base64Data)))
	}
	if mimeType != "" {
		sb.WriteString(fmt.Sprintf("      mime_type: %s\n", mimeType))
	}
	if detail != nil && detail != "" {
		sb.WriteString(fmt.Sprintf("      detail: %v\n", detail))
	}
	return sb.String()
}
