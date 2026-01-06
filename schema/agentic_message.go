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
	"sort"
	"strings"

	"github.com/eino-contrib/jsonschema"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/schema/claude"
	"github.com/cloudwego/eino/schema/gemini"
	"github.com/cloudwego/eino/schema/openai"
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

type StreamingMeta struct {
	// Index specifies the index position of this block in the final response.
	Index int
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

	StreamingMeta *StreamingMeta
	Extra         map[string]any
}

type UserInputText struct {
	Text string
}

type UserInputImage struct {
	URL        string
	Base64Data string
	MIMEType   string
	Detail     ImageURLDetail
}

type UserInputAudio struct {
	URL        string
	Base64Data string
	MIMEType   string
}

type UserInputVideo struct {
	URL        string
	Base64Data string
	MIMEType   string
}

type UserInputFile struct {
	URL        string
	Name       string
	Base64Data string
	MIMEType   string
}

type AssistantGenText struct {
	Text string

	OpenAIExtension *openai.AssistantGenTextExtension
	ClaudeExtension *claude.AssistantGenTextExtension
	Extension       any
}

type AssistantGenImage struct {
	URL        string
	Base64Data string
	MIMEType   string
}

type AssistantGenAudio struct {
	URL        string
	Base64Data string
	MIMEType   string
}

type AssistantGenVideo struct {
	URL        string
	Base64Data string
	MIMEType   string
}

type Reasoning struct {
	// Summary is the reasoning content summary.
	Summary []*ReasoningSummary

	// EncryptedContent is the encrypted reasoning content.
	EncryptedContent string
}

type ReasoningSummary struct {
	// Index specifies the index position of this summary in the final Reasoning.
	Index int

	Text string
}

type FunctionToolCall struct {
	// CallID is the unique identifier for the tool call.
	CallID string

	// Name specifies the function tool invoked.
	Name string

	// Arguments is the JSON string arguments for the function tool call.
	Arguments string
}

type FunctionToolResult struct {
	// CallID is the unique identifier for the tool call.
	CallID string

	// Name specifies the function tool invoked.
	Name string

	// Result is the function tool result returned by the user
	Result string
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
}

type MCPToolCall struct {
	// ServerLabel is the MCP server label used to identify it in tool calls
	ServerLabel string

	// ApprovalRequestID is the approval request ID.
	ApprovalRequestID string

	// CallID is the unique ID of the tool call.
	CallID string

	// Name is the name of the tool to run.
	Name string

	// Arguments is the JSON string arguments for the tool call.
	Arguments string
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
}

type MCPListToolsItem struct {
	// Name is the name of the tool.
	Name string

	// Description is the description of the tool.
	Description string

	// InputSchema is the JSON schema that describes the tool input parameters.
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
}

type MCPToolApprovalResponse struct {
	// ApprovalRequestID is the approval request ID being responded to.
	ApprovalRequestID string

	// Approve indicates whether the request is approved.
	Approve bool

	// Reason is the rationale for the decision.
	// Optional.
	Reason string
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

type contentBlockVariant interface {
	Reasoning | userInputVariant | assistantGenVariant | functionToolCallVariant | serverToolCallVariant | mcpToolCallVariant
}

type userInputVariant interface {
	UserInputText | UserInputImage | UserInputAudio | UserInputVideo | UserInputFile
}

type assistantGenVariant interface {
	AssistantGenText | AssistantGenImage | AssistantGenAudio | AssistantGenVideo
}

type functionToolCallVariant interface {
	FunctionToolCall | FunctionToolResult
}

type serverToolCallVariant interface {
	ServerToolCall | ServerToolResult
}

type mcpToolCallVariant interface {
	MCPToolCall | MCPToolResult | MCPListToolsResult | MCPToolApprovalRequest | MCPToolApprovalResponse
}

// NewContentBlock creates a new ContentBlock with the given content.
func NewContentBlock[T contentBlockVariant](content *T) *ContentBlock {
	switch b := any(content).(type) {
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

// NewContentBlockChunk creates a new ContentBlock with the given content and streaming metadata.
func NewContentBlockChunk[T contentBlockVariant](content *T, meta *StreamingMeta) *ContentBlock {
	block := NewContentBlock(content)
	block.StreamingMeta = meta
	return block
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

// ConcatAgenticMessagesArray concatenates multiple streams of AgenticMessage into a single slice of AgenticMessage.
func ConcatAgenticMessagesArray(mas [][]*AgenticMessage) ([]*AgenticMessage, error) {
	return buildConcatGenericArray[AgenticMessage](ConcatAgenticMessages)(mas)
}

// ConcatAgenticMessages concatenates a list of AgenticMessage chunks into a single AgenticMessage.
func ConcatAgenticMessages(msgs []*AgenticMessage) (*AgenticMessage, error) {
	var (
		role          AgenticRoleType
		blocks        []*ContentBlock
		metas         []*AgenticResponseMeta
		blockIndices  []int
		indexToBlocks = map[int][]*ContentBlock{}
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
			if block == nil {
				continue
			}
			if block.StreamingMeta == nil {
				// Non-streaming block
				if len(blockIndices) > 0 {
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
				if blocks_, ok := indexToBlocks[block.StreamingMeta.Index]; ok {
					indexToBlocks[block.StreamingMeta.Index] = append(blocks_, block)
				} else {
					blockIndices = append(blockIndices, block.StreamingMeta.Index)
					indexToBlocks[block.StreamingMeta.Index] = []*ContentBlock{block}
				}
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

	if len(blockIndices) > 0 {
		// All blocks are streaming, concat each group by index
		indexToBlock := map[int]*ContentBlock{}
		for idx, bs := range indexToBlocks {
			b, err := concatChunksOfSameContentBlock(bs)
			if err != nil {
				return nil, err
			}
			indexToBlock[idx] = b
		}
		blocks = make([]*ContentBlock, 0, len(blockIndices))
		sort.Slice(blockIndices, func(i, j int) bool {
			return blockIndices[i] < blockIndices[j]
		})
		for _, idx := range blockIndices {
			blocks = append(blocks, indexToBlock[idx])
		}
	}

	return &AgenticMessage{
		Role:          role,
		ResponseMeta:  meta,
		ContentBlocks: blocks,
	}, nil
}

func concatAgenticResponseMeta(metas []*AgenticResponseMeta) (ret *AgenticResponseMeta, err error) {
	if len(metas) == 0 {
		return nil, nil
	}

	openaiExtensions := make([]*openai.ResponseMetaExtension, 0, len(metas))
	claudeExtensions := make([]*claude.ResponseMetaExtension, 0, len(metas))
	geminiExtensions := make([]*gemini.ResponseMetaExtension, 0, len(metas))
	tokenUsages := make([]*TokenUsage, 0, len(metas))

	var (
		extType    reflect.Type
		extensions reflect.Value
	)

	for _, meta := range metas {
		if meta.TokenUsage != nil {
			tokenUsages = append(tokenUsages, meta.TokenUsage)
		}

		var isConsistent bool

		if meta.Extension != nil {
			extType, isConsistent = validateExtensionType(extType, meta.Extension)
			if !isConsistent {
				return nil, fmt.Errorf("inconsistent extension types in response meta chunks: '%s' vs '%s'",
					extType, reflect.TypeOf(meta.Extension))
			}
			if !extensions.IsValid() {
				extensions = reflect.MakeSlice(reflect.SliceOf(extType), 0, len(metas))
			}
			extensions = reflect.Append(extensions, reflect.ValueOf(meta.Extension))
		}

		if meta.OpenAIExtension != nil {
			extType, isConsistent = validateExtensionType(extType, meta.OpenAIExtension)
			if !isConsistent {
				return nil, fmt.Errorf("inconsistent extension types in response meta chunks: '%s' vs '%s'",
					extType, reflect.TypeOf(meta.OpenAIExtension))
			}
			openaiExtensions = append(openaiExtensions, meta.OpenAIExtension)
		}

		if meta.ClaudeExtension != nil {
			extType, isConsistent = validateExtensionType(extType, meta.ClaudeExtension)
			if !isConsistent {
				return nil, fmt.Errorf("inconsistent extension types in response meta chunks: '%s' vs '%s'",
					extType, reflect.TypeOf(meta.ClaudeExtension))
			}
			claudeExtensions = append(claudeExtensions, meta.ClaudeExtension)
		}

		if meta.GeminiExtension != nil {
			extType, isConsistent = validateExtensionType(extType, meta.GeminiExtension)
			if !isConsistent {
				return nil, fmt.Errorf("inconsistent extension types in response meta chunks: '%s' vs '%s'",
					extType, reflect.TypeOf(meta.GeminiExtension))
			}
			geminiExtensions = append(geminiExtensions, meta.GeminiExtension)
		}
	}

	ret = &AgenticResponseMeta{
		TokenUsage: concatTokenUsage(tokenUsages),
	}

	if extensions.IsValid() && !extensions.IsZero() {
		var extension reflect.Value
		extension, err = internal.ConcatSliceValue(extensions)
		if err != nil {
			return nil, fmt.Errorf("failed to concat extensions: %w", err)
		}
		ret.Extension = extension.Interface()
	}

	if len(openaiExtensions) > 0 {
		ret.OpenAIExtension, err = openai.ConcatResponseMetaExtensions(openaiExtensions)
		if err != nil {
			return nil, fmt.Errorf("failed to concat openai extensions: %w", err)
		}
	}

	if len(claudeExtensions) > 0 {
		ret.ClaudeExtension, err = claude.ConcatResponseMetaExtensions(claudeExtensions)
		if err != nil {
			return nil, fmt.Errorf("failed to concat claude extensions: %w", err)
		}
	}

	if len(geminiExtensions) > 0 {
		ret.GeminiExtension, err = gemini.ConcatResponseMetaExtensions(geminiExtensions)
		if err != nil {
			return nil, fmt.Errorf("failed to concat gemini extensions: %w", err)
		}
	}

	return ret, nil
}

func concatTokenUsage(usages []*TokenUsage) *TokenUsage {
	if len(usages) == 0 {
		return nil
	}

	ret := &TokenUsage{}

	for _, usage := range usages {
		if usage == nil {
			continue
		}
		ret.CompletionTokens += usage.CompletionTokens
		ret.CompletionTokensDetails.ReasoningTokens += usage.CompletionTokensDetails.ReasoningTokens
		ret.PromptTokens += usage.PromptTokens
		ret.PromptTokenDetails.CachedTokens += usage.PromptTokenDetails.CachedTokens
		ret.TotalTokens += usage.TotalTokens
	}

	return ret
}

func concatChunksOfSameContentBlock(blocks []*ContentBlock) (*ContentBlock, error) {
	if len(blocks) == 0 {
		return nil, fmt.Errorf("no content blocks to concat")
	}

	blockType := blocks[0].Type

	switch blockType {
	case ContentBlockTypeReasoning:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *Reasoning { return b.Reasoning },
			concatReasoning)

	case ContentBlockTypeUserInputText:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *UserInputText { return b.UserInputText },
			concatUserInputTexts)

	case ContentBlockTypeUserInputImage:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *UserInputImage { return b.UserInputImage },
			concatUserInputImages)

	case ContentBlockTypeUserInputAudio:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *UserInputAudio { return b.UserInputAudio },
			concatUserInputAudios)

	case ContentBlockTypeUserInputVideo:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *UserInputVideo { return b.UserInputVideo },
			concatUserInputVideos)

	case ContentBlockTypeUserInputFile:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *UserInputFile { return b.UserInputFile },
			concatUserInputFiles)

	case ContentBlockTypeAssistantGenText:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *AssistantGenText { return b.AssistantGenText },
			concatAssistantGenTexts)

	case ContentBlockTypeAssistantGenImage:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *AssistantGenImage { return b.AssistantGenImage },
			concatAssistantGenImages)

	case ContentBlockTypeAssistantGenAudio:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *AssistantGenAudio { return b.AssistantGenAudio },
			concatAssistantGenAudios)

	case ContentBlockTypeAssistantGenVideo:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *AssistantGenVideo { return b.AssistantGenVideo },
			concatAssistantGenVideos)

	case ContentBlockTypeFunctionToolCall:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *FunctionToolCall { return b.FunctionToolCall },
			concatFunctionToolCalls)

	case ContentBlockTypeFunctionToolResult:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *FunctionToolResult { return b.FunctionToolResult },
			concatFunctionToolResults)

	case ContentBlockTypeServerToolCall:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *ServerToolCall { return b.ServerToolCall },
			concatServerToolCalls)

	case ContentBlockTypeServerToolResult:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *ServerToolResult { return b.ServerToolResult },
			concatServerToolResults)

	case ContentBlockTypeMCPToolCall:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *MCPToolCall { return b.MCPToolCall },
			concatMCPToolCalls)

	case ContentBlockTypeMCPToolResult:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *MCPToolResult { return b.MCPToolResult },
			concatMCPToolResults)

	case ContentBlockTypeMCPListToolsResult:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *MCPListToolsResult { return b.MCPListToolsResult },
			concatMCPListToolsResults)

	case ContentBlockTypeMCPToolApprovalRequest:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *MCPToolApprovalRequest { return b.MCPToolApprovalRequest },
			concatMCPToolApprovalRequests)

	case ContentBlockTypeMCPToolApprovalResponse:
		return concatContentBlockHelper(blocks, blockType,
			func(b *ContentBlock) *MCPToolApprovalResponse { return b.MCPToolApprovalResponse },
			concatMCPToolApprovalResponses)

	default:
		return nil, fmt.Errorf("unknown content block type: %s", blockType)
	}
}

// concatContentBlockHelper is a generic helper function that reduces code duplication
// for concatenating content blocks of a specific type.
func concatContentBlockHelper[T contentBlockVariant](
	blocks []*ContentBlock,
	expectedType ContentBlockType,
	getter func(*ContentBlock) *T,
	concatFunc func([]*T) (*T, error),
) (*ContentBlock, error) {
	items, err := genericGetTFromContentBlocks(blocks, func(block *ContentBlock) (*T, error) {
		if block.Type != expectedType {
			return nil, fmt.Errorf("content block type mismatch: expected '%s', but got '%s'", expectedType, block.Type)
		}
		item := getter(block)
		if item == nil {
			return nil, fmt.Errorf("'%s' content is nil", expectedType)
		}
		return item, nil
	})
	if err != nil {
		return nil, err
	}

	concatenated, err := concatFunc(items)
	if err != nil {
		return nil, fmt.Errorf("failed to concat '%s' content blocks: %w", expectedType, err)
	}

	extras := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if len(block.Extra) > 0 {
			extras = append(extras, block.Extra)
		}
	}

	var extra map[string]any
	if len(extras) > 0 {
		extra, err = internal.ConcatItems(extras)
		if err != nil {
			return nil, fmt.Errorf("failed to concat content block extras: %w", err)
		}
	}

	block := NewContentBlock(concatenated)
	block.Extra = extra

	return block, nil
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

func concatReasoning(reasons []*Reasoning) (*Reasoning, error) {
	if len(reasons) == 0 {
		return nil, fmt.Errorf("no reasoning found")
	}

	ret := &Reasoning{}

	var allSummaries []*ReasoningSummary
	for _, r := range reasons {
		if r == nil {
			continue
		}
		allSummaries = append(allSummaries, r.Summary...)
		if r.EncryptedContent != "" {
			ret.EncryptedContent += r.EncryptedContent
		}
	}

	var (
		indices        []int
		indexToSummary = map[int]*ReasoningSummary{}
	)

	for _, s := range allSummaries {
		if s == nil {
			continue
		}
		if indexToSummary[s.Index] == nil {
			indexToSummary[s.Index] = &ReasoningSummary{}
			indices = append(indices, s.Index)
		}
		indexToSummary[s.Index].Text += s.Text
	}

	sort.Slice(indices, func(i, j int) bool {
		return indices[i] < indices[j]
	})

	ret.Summary = make([]*ReasoningSummary, 0, len(indices))
	for _, idx := range indices {
		ret.Summary = append(ret.Summary, indexToSummary[idx])
	}

	return ret, nil
}

func concatUserInputTexts(texts []*UserInputText) (*UserInputText, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("no user input text found")
	}
	if len(texts) == 1 {
		return texts[0], nil
	}
	return nil, fmt.Errorf("cannot concat multiple user input texts")
}

func concatUserInputImages(images []*UserInputImage) (*UserInputImage, error) {
	if len(images) == 0 {
		return nil, fmt.Errorf("no user input image found")
	}
	if len(images) == 1 {
		return images[0], nil
	}
	return nil, fmt.Errorf("cannot concat multiple user input images")
}

func concatUserInputAudios(audios []*UserInputAudio) (*UserInputAudio, error) {
	if len(audios) == 0 {
		return nil, fmt.Errorf("no user input audio found")
	}
	if len(audios) == 1 {
		return audios[0], nil
	}
	return nil, fmt.Errorf("cannot concat multiple user input audios")
}

func concatUserInputVideos(videos []*UserInputVideo) (*UserInputVideo, error) {
	if len(videos) == 0 {
		return nil, fmt.Errorf("no user input video found")
	}
	if len(videos) == 1 {
		return videos[0], nil
	}
	return nil, fmt.Errorf("cannot concat multiple user input videos")
}

func concatUserInputFiles(files []*UserInputFile) (*UserInputFile, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no user input file found")
	}
	if len(files) == 1 {
		return files[0], nil
	}
	return nil, fmt.Errorf("cannot concat multiple user input files")
}

func concatAssistantGenTexts(texts []*AssistantGenText) (ret *AssistantGenText, err error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("no assistant generated text found")
	}
	if len(texts) == 1 {
		return texts[0], nil
	}

	ret = &AssistantGenText{}

	openaiExtensions := make([]*openai.AssistantGenTextExtension, 0, len(texts))
	claudeExtensions := make([]*claude.AssistantGenTextExtension, 0, len(texts))

	var (
		extType    reflect.Type
		extensions reflect.Value
	)

	for _, t := range texts {
		if t == nil {
			continue
		}

		ret.Text += t.Text

		var isConsistent bool

		if t.Extension != nil {
			extType, isConsistent = validateExtensionType(extType, t.Extension)
			if !isConsistent {
				return nil, fmt.Errorf("inconsistent extension types in assistant generated text chunks: '%s' vs '%s'",
					extType, reflect.TypeOf(t.Extension))
			}
			if !extensions.IsValid() {
				extensions = reflect.MakeSlice(reflect.SliceOf(extType), 0, len(texts))
			}
			extensions = reflect.Append(extensions, reflect.ValueOf(t.Extension))
		}

		if t.OpenAIExtension != nil {
			extType, isConsistent = validateExtensionType(extType, t.OpenAIExtension)
			if !isConsistent {
				return nil, fmt.Errorf("inconsistent extension types in assistant generated text chunks: '%s' vs '%s'",
					extType, reflect.TypeOf(t.OpenAIExtension))
			}
			openaiExtensions = append(openaiExtensions, t.OpenAIExtension)
		}

		if t.ClaudeExtension != nil {
			extType, isConsistent = validateExtensionType(extType, t.ClaudeExtension)
			if !isConsistent {
				return nil, fmt.Errorf("inconsistent extension types in assistant generated text chunks: '%s' vs '%s'",
					extType, reflect.TypeOf(t.ClaudeExtension))
			}
			claudeExtensions = append(claudeExtensions, t.ClaudeExtension)
		}
	}

	if extensions.IsValid() && !extensions.IsZero() {
		ret.Extension, err = internal.ConcatSliceValue(extensions)
		if err != nil {
			return nil, err
		}
		ret.Extension = extensions.Interface()
	}

	if len(openaiExtensions) > 0 {
		ret.OpenAIExtension, err = openai.ConcatAssistantGenTextExtensions(openaiExtensions)
		if err != nil {
			return nil, err
		}
	}

	if len(claudeExtensions) > 0 {
		ret.ClaudeExtension, err = claude.ConcatAssistantGenTextExtensions(claudeExtensions)
		if err != nil {
			return nil, err
		}
	}

	return ret, nil
}

func concatAssistantGenImages(images []*AssistantGenImage) (*AssistantGenImage, error) {
	if len(images) == 0 {
		return nil, fmt.Errorf("no assistant gen image found")
	}
	if len(images) == 1 {
		return images[0], nil
	}

	ret := &AssistantGenImage{}

	for _, img := range images {
		if img == nil {
			continue
		}

		ret.Base64Data += img.Base64Data

		if ret.URL == "" {
			ret.URL = img.URL
		} else if img.URL != "" && ret.URL != img.URL {
			return nil, fmt.Errorf("inconsistent URLs in assistant generated image chunks: '%s' vs '%s'", ret.URL, img.URL)
		}

		if ret.MIMEType == "" {
			ret.MIMEType = img.MIMEType
		} else if img.MIMEType != "" && ret.MIMEType != img.MIMEType {
			return nil, fmt.Errorf("inconsistent MIME types in assistant generated image chunks: '%s' vs '%s'", ret.MIMEType, img.MIMEType)
		}
	}

	return ret, nil
}

func concatAssistantGenAudios(audios []*AssistantGenAudio) (*AssistantGenAudio, error) {
	if len(audios) == 0 {
		return nil, fmt.Errorf("no assistant gen audio found")
	}
	if len(audios) == 1 {
		return audios[0], nil
	}

	ret := &AssistantGenAudio{}

	for _, audio := range audios {
		if audio == nil {
			continue
		}

		ret.Base64Data += audio.Base64Data

		if ret.URL == "" {
			ret.URL = audio.URL
		} else if audio.URL != "" && ret.URL != audio.URL {
			return nil, fmt.Errorf("inconsistent URLs in assistant generated audio chunks: '%s' vs '%s'", ret.URL, audio.URL)
		}

		if ret.MIMEType == "" {
			ret.MIMEType = audio.MIMEType
		} else if audio.MIMEType != "" && ret.MIMEType != audio.MIMEType {
			return nil, fmt.Errorf("inconsistent MIME types in assistant generated audio chunks: '%s' vs '%s'", ret.MIMEType, audio.MIMEType)
		}
	}

	return ret, nil
}

func concatAssistantGenVideos(videos []*AssistantGenVideo) (*AssistantGenVideo, error) {
	if len(videos) == 0 {
		return nil, fmt.Errorf("no assistant gen video found")
	}
	if len(videos) == 1 {
		return videos[0], nil
	}

	ret := &AssistantGenVideo{}

	for _, video := range videos {
		if video == nil {
			continue
		}

		ret.Base64Data += video.Base64Data

		if ret.URL == "" {
			ret.URL = video.URL
		} else if video.URL != "" && ret.URL != video.URL {
			return nil, fmt.Errorf("inconsistent URLs in assistant generated video chunks: '%s' vs '%s'", ret.URL, video.URL)
		}

		if ret.MIMEType == "" {
			ret.MIMEType = video.MIMEType
		} else if video.MIMEType != "" && ret.MIMEType != video.MIMEType {
			return nil, fmt.Errorf("inconsistent MIME types in assistant generated video chunks: '%s' vs '%s'", ret.MIMEType, video.MIMEType)
		}
	}

	return ret, nil
}

func concatFunctionToolCalls(calls []*FunctionToolCall) (*FunctionToolCall, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("no function tool call found")
	}
	if len(calls) == 1 {
		return calls[0], nil
	}

	ret := &FunctionToolCall{}

	for _, c := range calls {
		if c == nil {
			continue
		}

		if ret.CallID == "" {
			ret.CallID = c.CallID
		} else if c.CallID != "" && c.CallID != ret.CallID {
			return nil, fmt.Errorf("expected call ID '%s' for function tool call, but got '%s'", ret.CallID, c.CallID)
		}

		if ret.Name == "" {
			ret.Name = c.Name
		} else if c.Name != "" && c.Name != ret.Name {
			return nil, fmt.Errorf("expected tool name '%s' for function tool call, but got '%s'", ret.Name, c.Name)
		}

		ret.Arguments += c.Arguments
	}

	return ret, nil
}

func concatFunctionToolResults(results []*FunctionToolResult) (*FunctionToolResult, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("no function tool result found")
	}
	if len(results) == 1 {
		return results[0], nil
	}

	ret := &FunctionToolResult{}

	for _, r := range results {
		if r == nil {
			continue
		}

		if ret.CallID == "" {
			ret.CallID = r.CallID
		} else if r.CallID != "" && r.CallID != ret.CallID {
			return nil, fmt.Errorf("expected call ID '%s' for function tool result, but got '%s'", ret.CallID, r.CallID)
		}

		if ret.Name == "" {
			ret.Name = r.Name
		} else if r.Name != "" && r.Name != ret.Name {
			return nil, fmt.Errorf("expected tool name '%s' for function tool result, but got '%s'", ret.Name, r.Name)
		}

		ret.Result += r.Result
	}

	return ret, nil
}

func concatServerToolCalls(calls []*ServerToolCall) (ret *ServerToolCall, err error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("no server tool call found")
	}
	if len(calls) == 1 {
		return calls[0], nil
	}

	ret = &ServerToolCall{}

	var (
		argsType   reflect.Type
		argsChunks reflect.Value
	)

	for _, c := range calls {
		if c == nil {
			continue
		}

		if ret.CallID == "" {
			ret.CallID = c.CallID
		} else if c.CallID != "" && c.CallID != ret.CallID {
			return nil, fmt.Errorf("expected call ID '%s' for server tool call, but got '%s'", ret.CallID, c.CallID)
		}

		if ret.Name == "" {
			ret.Name = c.Name
		} else if c.Name != "" && c.Name != ret.Name {
			return nil, fmt.Errorf("expected tool name '%s' for server tool call, but got '%s'", ret.Name, c.Name)
		}

		if c.Arguments != nil {
			argsType_ := reflect.TypeOf(c.Arguments)
			if argsType == nil {
				argsType = argsType_
				argsChunks = reflect.MakeSlice(reflect.SliceOf(argsType), 0, len(calls))
			} else if argsType != argsType_ {
				return nil, fmt.Errorf("expected type '%s' for server tool call arguments, but got '%s'", argsType, argsType_)
			}
			argsChunks = reflect.Append(argsChunks, reflect.ValueOf(c.Arguments))
		}
	}

	if argsChunks.IsValid() && !argsChunks.IsZero() {
		arguments, err := internal.ConcatSliceValue(argsChunks)
		if err != nil {
			return nil, err
		}
		ret.Arguments = arguments.Interface()
	}

	return ret, nil
}

func concatServerToolResults(results []*ServerToolResult) (ret *ServerToolResult, err error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("no server tool result found")
	}
	if len(results) == 1 {
		return results[0], nil
	}

	ret = &ServerToolResult{}

	var (
		resType   reflect.Type
		resChunks reflect.Value
	)

	for _, r := range results {
		if r == nil {
			continue
		}

		if ret.CallID == "" {
			ret.CallID = r.CallID
		} else if r.CallID != "" && r.CallID != ret.CallID {
			return nil, fmt.Errorf("expected call ID '%s' for server tool result, but got '%s'", ret.CallID, r.CallID)
		}

		if ret.Name == "" {
			ret.Name = r.Name
		} else if r.Name != "" && r.Name != ret.Name {
			return nil, fmt.Errorf("expected tool name '%s' for server tool result, but got '%s'", ret.Name, r.Name)
		}

		if r.Result != nil {
			resType_ := reflect.TypeOf(r.Result)
			if resType == nil {
				resType = resType_
				resChunks = reflect.MakeSlice(reflect.SliceOf(resType), 0, len(results))
			} else if resType != resType_ {
				return nil, fmt.Errorf("expected type '%s' for server tool result, but got '%s'", resType, resType_)
			}
			resChunks = reflect.Append(resChunks, reflect.ValueOf(r.Result))
		}
	}

	if resChunks.IsValid() && !resChunks.IsZero() {
		result, err := internal.ConcatSliceValue(resChunks)
		if err != nil {
			return nil, fmt.Errorf("failed to concat server tool result: %v", err)
		}
		ret.Result = result.Interface()
	}

	return ret, nil
}

func concatMCPToolCalls(calls []*MCPToolCall) (*MCPToolCall, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("no mcp tool call found")
	}
	if len(calls) == 1 {
		return calls[0], nil
	}

	ret := &MCPToolCall{}

	for _, c := range calls {
		if c == nil {
			continue
		}

		ret.Arguments += c.Arguments

		if ret.ServerLabel == "" {
			ret.ServerLabel = c.ServerLabel
		} else if c.ServerLabel != "" && c.ServerLabel != ret.ServerLabel {
			return nil, fmt.Errorf("expected server label '%s' for mcp tool call, but got '%s'", ret.ServerLabel, c.ServerLabel)
		}

		if ret.CallID == "" {
			ret.CallID = c.CallID
		} else if c.CallID != "" && c.CallID != ret.CallID {
			return nil, fmt.Errorf("expected call ID '%s' for mcp tool call, but got '%s'", ret.CallID, c.CallID)
		}

		if ret.Name == "" {
			ret.Name = c.Name
		} else if c.Name != "" && c.Name != ret.Name {
			return nil, fmt.Errorf("expected tool name '%s' for mcp tool call, but got '%s'", ret.Name, c.Name)
		}
	}

	return ret, nil
}

func concatMCPToolResults(results []*MCPToolResult) (*MCPToolResult, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("no mcp tool result found")
	}
	if len(results) == 1 {
		return results[0], nil
	}

	ret := &MCPToolResult{}

	for _, r := range results {
		if r == nil {
			continue
		}

		if r.Result != "" {
			ret.Result = r.Result
		}

		if ret.ServerLabel == "" {
			ret.ServerLabel = r.ServerLabel
		} else if r.ServerLabel != "" && r.ServerLabel != ret.ServerLabel {
			return nil, fmt.Errorf("expected server label '%s' for mcp tool result, but got '%s'", ret.ServerLabel, r.ServerLabel)
		}

		if ret.CallID == "" {
			ret.CallID = r.CallID
		} else if r.CallID != "" && r.CallID != ret.CallID {
			return nil, fmt.Errorf("expected call ID '%s' for mcp tool result, but got '%s'", ret.CallID, r.CallID)
		}

		if ret.Name == "" {
			ret.Name = r.Name
		} else if r.Name != "" && r.Name != ret.Name {
			return nil, fmt.Errorf("expected tool name '%s' for mcp tool result, but got '%s'", ret.Name, r.Name)
		}

		if r.Error != nil {
			ret.Error = r.Error
		}
	}

	return ret, nil
}

func concatMCPListToolsResults(results []*MCPListToolsResult) (*MCPListToolsResult, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("no mcp list tools result found")
	}
	if len(results) == 1 {
		return results[0], nil
	}

	ret := &MCPListToolsResult{}

	for _, r := range results {
		if r == nil {
			continue
		}

		ret.Tools = append(ret.Tools, r.Tools...)

		if r.Error != "" {
			ret.Error = r.Error
		}

		if ret.ServerLabel == "" {
			ret.ServerLabel = r.ServerLabel
		} else if r.ServerLabel != "" && r.ServerLabel != ret.ServerLabel {
			return nil, fmt.Errorf("expected server label '%s' for mcp list tools result, but got '%s'", ret.ServerLabel, r.ServerLabel)
		}
	}

	return ret, nil
}

func concatMCPToolApprovalRequests(requests []*MCPToolApprovalRequest) (*MCPToolApprovalRequest, error) {
	if len(requests) == 0 {
		return nil, fmt.Errorf("no mcp tool approval request found")
	}
	if len(requests) == 1 {
		return requests[0], nil
	}

	ret := &MCPToolApprovalRequest{}

	for _, r := range requests {
		if r == nil {
			continue
		}

		ret.Arguments += r.Arguments

		if ret.ID == "" {
			ret.ID = r.ID
		} else if r.ID != "" && r.ID != ret.ID {
			return nil, fmt.Errorf("expected request ID '%s' for mcp tool approval request, but got '%s'", ret.ID, r.ID)
		}

		if ret.Name == "" {
			ret.Name = r.Name
		} else if r.Name != "" && r.Name != ret.Name {
			return nil, fmt.Errorf("expected tool name '%s' for mcp tool approval request, but got '%s'", ret.Name, r.Name)
		}

		if ret.ServerLabel == "" {
			ret.ServerLabel = r.ServerLabel
		} else if r.ServerLabel != "" && r.ServerLabel != ret.ServerLabel {
			return nil, fmt.Errorf("expected server label '%s' for mcp tool approval request, but got '%s'", ret.ServerLabel, r.ServerLabel)
		}
	}

	return ret, nil
}

func concatMCPToolApprovalResponses(responses []*MCPToolApprovalResponse) (*MCPToolApprovalResponse, error) {
	if len(responses) == 0 {
		return nil, fmt.Errorf("no mcp tool approval response found")
	}
	if len(responses) == 1 {
		return responses[0], nil
	}
	return nil, fmt.Errorf("cannot concat multiple mcp tool approval responses")
}

// String returns the string representation of AgenticMessage.
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

// String returns the string representation of ContentBlock.
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

	if b.StreamingMeta != nil {
		sb.WriteString(fmt.Sprintf("      stream_index: %d\n", b.StreamingMeta.Index))
	}

	return sb.String()
}

// String returns the string representation of Reasoning.
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

// String returns the string representation of UserInputText.
func (u *UserInputText) String() string {
	return fmt.Sprintf("      text: %s\n", u.Text)
}

// String returns the string representation of UserInputImage.
func (u *UserInputImage) String() string {
	return formatMediaString(u.URL, u.Base64Data, u.MIMEType, u.Detail)
}

// String returns the string representation of UserInputAudio.
func (u *UserInputAudio) String() string {
	return formatMediaString(u.URL, u.Base64Data, u.MIMEType, "")
}

// String returns the string representation of UserInputVideo.
func (u *UserInputVideo) String() string {
	return formatMediaString(u.URL, u.Base64Data, u.MIMEType, "")
}

// String returns the string representation of UserInputFile.
func (u *UserInputFile) String() string {
	sb := &strings.Builder{}
	if u.Name != "" {
		sb.WriteString(fmt.Sprintf("      name: %s\n", u.Name))
	}
	sb.WriteString(formatMediaString(u.URL, u.Base64Data, u.MIMEType, ""))
	return sb.String()
}

// String returns the string representation of AssistantGenText.
func (a *AssistantGenText) String() string {
	return fmt.Sprintf("      text: %s\n", a.Text)
}

// String returns the string representation of AssistantGenImage.
func (a *AssistantGenImage) String() string {
	return formatMediaString(a.URL, a.Base64Data, a.MIMEType, "")
}

// String returns the string representation of AssistantGenAudio.
func (a *AssistantGenAudio) String() string {
	return formatMediaString(a.URL, a.Base64Data, a.MIMEType, "")
}

// String returns the string representation of AssistantGenVideo.
func (a *AssistantGenVideo) String() string {
	return formatMediaString(a.URL, a.Base64Data, a.MIMEType, "")
}

// String returns the string representation of FunctionToolCall.
func (f *FunctionToolCall) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      call_id: %s\n", f.CallID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", f.Name))
	sb.WriteString(fmt.Sprintf("      arguments: %s\n", f.Arguments))
	return sb.String()
}

// String returns the string representation of FunctionToolResult.
func (f *FunctionToolResult) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      call_id: %s\n", f.CallID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", f.Name))
	sb.WriteString(fmt.Sprintf("      result: %s\n", f.Result))
	return sb.String()
}

// String returns the string representation of ServerToolCall.
func (s *ServerToolCall) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      name: %s\n", s.Name))
	if s.CallID != "" {
		sb.WriteString(fmt.Sprintf("      call_id: %s\n", s.CallID))
	}
	sb.WriteString(fmt.Sprintf("      arguments: %v\n", s.Arguments))
	return sb.String()
}

// String returns the string representation of ServerToolResult.
func (s *ServerToolResult) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      name: %s\n", s.Name))
	if s.CallID != "" {
		sb.WriteString(fmt.Sprintf("      call_id: %s\n", s.CallID))
	}
	sb.WriteString(fmt.Sprintf("      result: %v\n", s.Result))
	return sb.String()
}

// String returns the string representation of MCPToolCall.
func (m *MCPToolCall) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      server_label: %s\n", m.ServerLabel))
	sb.WriteString(fmt.Sprintf("      call_id: %s\n", m.CallID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", m.Name))
	sb.WriteString(fmt.Sprintf("      arguments: %s\n", m.Arguments))
	return sb.String()
}

// String returns the string representation of MCPToolResult.
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

// String returns the string representation of MCPListToolsResult.
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

// String returns the string representation of MCPToolApprovalRequest.
func (m *MCPToolApprovalRequest) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      server_label: %s\n", m.ServerLabel))
	sb.WriteString(fmt.Sprintf("      id: %s\n", m.ID))
	sb.WriteString(fmt.Sprintf("      name: %s\n", m.Name))
	sb.WriteString(fmt.Sprintf("      arguments: %s\n", m.Arguments))
	return sb.String()
}

// String returns the string representation of MCPToolApprovalResponse.
func (m *MCPToolApprovalResponse) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("      approval_request_id: %s\n", m.ApprovalRequestID))
	sb.WriteString(fmt.Sprintf("      approve: %v\n", m.Approve))
	if m.Reason != "" {
		sb.WriteString(fmt.Sprintf("      reason: %s\n", m.Reason))
	}
	return sb.String()
}

// String returns the string representation of AgenticResponseMeta.
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

func validateExtensionType(expected reflect.Type, actual any) (reflect.Type, bool) {
	if actual == nil {
		return expected, true
	}
	actualType := reflect.TypeOf(actual)
	if expected == nil {
		return actualType, true
	}
	if expected != actualType {
		return expected, false
	}
	return expected, true
}
