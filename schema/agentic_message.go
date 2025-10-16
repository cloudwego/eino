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
	"fmt"
	"sort"

	"github.com/eino-contrib/jsonschema"
)

type ContentBlockType string

const (
	BlockTypeReasoning                              ContentBlockType = "reasoning"
	BlockTypeUserInputText                          ContentBlockType = "user_input_text"
	BlockTypeUserInputImage                         ContentBlockType = "user_input_image"
	BlockTypeUserInputAudio                         ContentBlockType = "user_input_audio"
	BlockTypeUserInputVideo                         ContentBlockType = "user_input_video"
	BlockTypeUserInputFile                          ContentBlockType = "user_input_file"
	BlockTypeAssistantGenText                       ContentBlockType = "assistant_gen_text"
	BlockTypeAssistantGenImage                      ContentBlockType = "assistant_gen_image"
	BlockTypeAssistantGenAudio                      ContentBlockType = "assistant_gen_audio"
	BlockTypeAssistantGenVideo                      ContentBlockType = "assistant_gen_video"
	BlockTypeFunctionToolCall                       ContentBlockType = "function_tool_call"
	BlockTypeFunctionToolResult                     ContentBlockType = "function_tool_result"
	BlockTypeProviderBuiltinToolCall                ContentBlockType = "provider_builtin_tool_call"
	BlockTypeProviderBuiltinToolResult              ContentBlockType = "provider_builtin_tool_result"
	BlockTypeProviderBuiltinMCPToolCall             ContentBlockType = "provider_builtin_mcp_tool_call"
	BlockTypeProviderBuiltinMCPToolResult           ContentBlockType = "provider_builtin_mcp_tool_result"
	BlockTypeProviderBuiltinMCPListTools            ContentBlockType = "provider_builtin_mcp_list_tools"
	BlockTypeProviderBuiltinMCPToolApprovalRequest  ContentBlockType = "provider_builtin_mcp_tool_approval_request"
	BlockTypeProviderBuiltinMCPToolApprovalResponse ContentBlockType = "provider_builtin_mcp_tool_approval_response"
)

type AgenticMessage struct {
	FinishReason *FinishReason
	Usage        *TokenUsageMeta

	Role   RoleType
	Blocks []*ContentBlock
}

type FinishStatus string

const (
	FinishStatusCompleted  FinishStatus = "completed"
	FinishStatusIncomplete FinishStatus = "incomplete"
)

type FinishReason struct {
	Status FinishStatus
	Reason string
}

type TokenUsageMeta struct {
	InputTokens         int64
	InputTokensDetails  InputTokensUsageDetails
	OutputTokens        int64
	OutputTokensDetails OutputTokensUsageDetails
	TotalTokens         int64
}

type InputTokensUsageDetails struct {
	CachedTokens int64
}

type OutputTokensUsageDetails struct {
	ReasoningTokens int64
}

type ContentBlock struct {
	// Index is used for streaming to identify the chunk of the block for merging.
	Index *int
	// Stage of this block in streaming.
	Stage *ContentBlockStage

	Type ContentBlockType

	Reasoning *BlockReasoning

	UserInputText  *BlockUserInputText
	UserInputImage *BlockUserInputImage
	UserInputAudio *BlockUserInputAudio
	UserInputVideo *BlockUserInputVideo
	UserInputFile  *BlockUserInputFile

	AssistantGenText  *BlockAssistantGenText
	AssistantGenImage *BlockAssistantGenImage
	AssistantGenAudio *BlockAssistantGenAudio
	AssistantGenVideo *BlockAssistantGenVideo

	// FunctionToolCall holds invocation details for a user-defined tool.
	FunctionToolCall *BlockFunctionToolCall

	// FunctionToolResult is the result from a user-defined tool call.
	FunctionToolResult *BlockFunctionToolResult

	// ProviderBuiltinToolCall holds invocation details for a provider built-in tool run on the model server.
	ProviderBuiltinToolCall *BlockProviderBuiltinToolCall

	// ProviderBuiltinToolResult is the result from a provider built-in tool run on the model server.
	ProviderBuiltinToolResult *BlockProviderBuiltinToolResult

	// ProviderBuiltinMCPToolCall holds invocation details for an MCP tool managed by the model server.
	ProviderBuiltinMCPToolCall *BlockProviderBuiltinMCPToolCall

	// ProviderBuiltinMCPToolResult is the result from an MCP tool managed by the model server.
	ProviderBuiltinMCPToolResult *BlockProviderBuiltinMCPToolResult

	// ProviderBuiltinMCPListToolsResult lists available MCP tools reported by the model server.
	ProviderBuiltinMCPListToolsResult *BlockProviderBuiltinMCPListToolsResult

	// ProviderBuiltinMCPToolApprovalRequest requests user approval for an MCP tool call when required.
	ProviderBuiltinMCPToolApprovalRequest *BlockProviderBuiltinMCPToolApprovalRequest

	// ProviderBuiltinMCPToolApprovalResponse records the user's approval decision for an MCP tool call.
	ProviderBuiltinMCPToolApprovalResponse *BlockProviderBuiltinMCPToolApprovalResponse
}

type ContentBlockStage string

const (
	ContentBlockStageStart ContentBlockStage = "start"
	ContentBlockStageDelta ContentBlockStage = "delta"
	ContentBlockStageStop  ContentBlockStage = "stop"
)

type BlockUserInputText struct {
	Text string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockUserInputImage struct {
	URL        *string
	Base64Data *string
	MIMEType   string
	Detail     ImageURLDetail

	// Extra stores additional information.
	Extra map[string]any
}

type BlockUserInputAudio struct {
	URL        *string
	Base64Data *string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockUserInputVideo struct {
	URL        *string
	Base64Data *string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockUserInputFile struct {
	URL        *string
	Name       *string
	Base64Data *string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockAssistantGenText struct {
	Text string

	Annotations []BaseGeneratedTextAnnotation

	// Extra stores additional information.
	Extra map[string]any
}

type BaseGeneratedTextAnnotation interface {
	AnnotationProvider() string
}

type TextURLCitationAccessor interface {
	BaseGeneratedTextAnnotation
	GetTextURLCitation() *TextURLCitation
}

type TextURLCitation struct {
	URL   string
	Title string

	// StartIndex is the first character index of the citation in the assistant generated text.
	StartIndex *int64

	// EndIndex is the last character index of the citation in the assistant generated text.
	EndIndex *int64
}

type BlockAssistantGenImage struct {
	URL        *string
	Base64Data *string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockAssistantGenAudio struct {
	URL        *string
	Base64Data *string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockAssistantGenVideo struct {
	URL        *string
	Base64Data *string
	MIMEType   string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockReasoning struct {
	// SummaryIndex is the index of the ReasoningSummary delta is associated with.
	SummaryIndex *int

	// Summary is the reasoning content summary.
	Summary []*ReasoningSummary
	// EncryptedContent is the encrypted reasoning content.
	EncryptedContent string

	// Extra stores additional information.
	Extra map[string]any
}

type ReasoningSummary struct {
	Text string
}

type BlockFunctionToolCall struct {
	// CallID is the unique ID of the tool call.
	CallID string
	// Name is the name of the function tool to run.
	Name string
	// Arguments is the JSON string arguments for the function tool call.
	Arguments string

	// Extra stores additional information
	Extra map[string]any
}

type BlockFunctionToolResult struct {
	// CallID is the unique ID of the tool call.
	CallID string
	// Name is the name of the function tool to run.
	Name string
	// Result is the function tool result returned by the user
	Result string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockProviderBuiltinToolCall struct {
	// Type is the type of the tool call. If model server is not specified, it is empty.
	Type string
	// CallID is the unique ID of the tool call. If model server is not specified, it is empty.
	CallID string
	// Name is the name of the tool to run. If model server is not specified, it is empty.
	Name string

	// Arguments is the JSON string arguments for the tool call.
	Arguments string

	// Extra stores additional information.
	Extra map[string]any
}

type ProviderBuiltinToolCallStatus string

const (
	ProviderBuiltinToolCallStatusCompleted ProviderBuiltinToolCallStatus = "completed"
	ProviderBuiltinToolCallStatusFailed    ProviderBuiltinToolCallStatus = "failed"
)

type BlockProviderBuiltinToolResult struct {
	// Type is the type of the tool call. If model server is not specified, it is empty.
	Type string
	// CallID specifies the unique identifier for the tool call, which is optional.
	CallID string
	// Name is the name of the tool to run. May be empty.
	Name string
	// Status is the tool call status.
	Status ProviderBuiltinToolCallStatus

	Result BaseBuiltinToolResult

	// Extra stores additional information.
	Extra map[string]any
}

type BaseBuiltinToolResult interface {
	BuiltinToolResultProvider() string
}

type WebSearchResultAccessor interface {
	BaseBuiltinToolResult
	GetWebSearchResult() *WebSearchResult
}

type WebSearchResult struct {
	Sources []*WebSearchSource
}

type WebSearchSource struct {
	URL   string
	Title string
}

type BlockProviderBuiltinMCPToolCall struct {
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

type BlockProviderBuiltinMCPToolResult struct {
	// CallID is the unique ID of the tool call.
	CallID string
	// Name is the name of the tool to run.
	Name string
	// Status is the tool call status.
	Status ProviderBuiltinToolCallStatus
	// Result is the JSON string with the tool result.
	Result string
	// Error returned when the server fails to run the tool.
	Error string

	// Extra stores additional information.
	Extra map[string]any
}

type BlockProviderBuiltinMCPListToolsResult struct {
	// ServerLabel is the MCP server label used to identify it in tool calls.
	ServerLabel string
	// Tools is the list of tools available on the server.
	Tools []MCPListToolsItem
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

type BlockProviderBuiltinMCPToolApprovalRequest struct {
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

type BlockProviderBuiltinMCPToolApprovalResponse struct {
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

func ConcatAgenticMessages(messages []*AgenticMessage) (ret *AgenticMessage, err error) {
	ret = &AgenticMessage{}

	if len(messages) == 0 {
		return ret, nil
	}

	var (
		genTextBlocks        []*BlockAssistantGenText
		genTextBlocksIndex   []int
		genTextBlocksInIndex = map[int][]*BlockAssistantGenText{}
	)

	for _, msg := range messages {
		for _, block := range msg.Blocks {
			switch block.Type {
			case BlockTypeAssistantGenText:
				if block.AssistantGenText == nil {
					continue
				}
				if idx := block.Index; idx != nil {
					genTextBlocksIndex = append(genTextBlocksIndex, *idx)
					genTextBlocksInIndex[*idx] = append(genTextBlocksInIndex[*idx], block.AssistantGenText)
				} else {
					genTextBlocks = append(genTextBlocks, block.AssistantGenText)
				}
			}
		}
	}

	if len(genTextBlocks) > 0 && len(genTextBlocksInIndex) > 0 {
		return nil, fmt.Errorf("genTextBlocks and genTextBlocksInIndex must not be empty at the same time")
	}

	sort.Ints(genTextBlocksIndex)

	for _, idx := range genTextBlocksIndex {
		genText, err := concatBlockAssistantGenText(genTextBlocksInIndex[idx])
		if err != nil {
			return nil, err
		}
		if genText == nil {
			continue
		}
		ret.Blocks = append(ret.Blocks, &ContentBlock{
			Type:             BlockTypeAssistantGenText,
			AssistantGenText: genText,
		})
	}

	if len(genTextBlocks) > 0 {
		genText, err := concatBlockAssistantGenText(genTextBlocks)
		if err != nil {
			return nil, err
		}
		ret.Blocks = append(ret.Blocks, &ContentBlock{
			Type:             BlockTypeAssistantGenText,
			AssistantGenText: genText,
		})
	}

	return ret, nil
}

func concatBlockAssistantGenText(blocks []*BlockAssistantGenText) (*BlockAssistantGenText, error) {
	if len(blocks) == 0 {
		return nil, nil
	}

	block := &BlockAssistantGenText{}
	annotations := make([]BaseGeneratedTextAnnotation, 0, len(blocks))

	for _, b := range blocks {
		block.Text += b.Text
		if b.Annotations != nil {
			annotations = append(annotations, b.Annotations...)
		}
	}

	block.Annotations = annotations

	return block, nil
}
