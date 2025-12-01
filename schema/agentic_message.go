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
	"github.com/cloudwego/eino/schema/claude"
	"github.com/cloudwego/eino/schema/gemini"
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
	ContentBlockTypeMCPListTools            ContentBlockType = "mcp_list_tools"
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
	Code  *int64
	Error string
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
	// CallID is the unique ID of the tool call.
	CallID string
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
		return &ContentBlock{Type: ContentBlockTypeMCPListTools, MCPListToolsResult: b}
	case *MCPToolApprovalRequest:
		return &ContentBlock{Type: ContentBlockTypeMCPToolApprovalRequest, MCPToolApprovalRequest: b}
	case *MCPToolApprovalResponse:
		return &ContentBlock{Type: ContentBlockTypeMCPToolApprovalResponse, MCPToolApprovalResponse: b}
	default:
		return nil
	}
}
