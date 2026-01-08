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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConcatAgenticMessages(t *testing.T) {
	t.Run("single message", func(t *testing.T) {
		msg := &AgenticMessage{
			Role: AgenticRoleTypeAssistant,
			ContentBlocks: []*ContentBlock{
				{
					Type: ContentBlockTypeAssistantGenText,
					AssistantGenText: &AssistantGenText{
						Text: "Hello",
					},
				},
			},
		}

		result, err := ConcatAgenticMessages([]*AgenticMessage{msg})
		assert.NoError(t, err)
		assert.Equal(t, msg, result)
	})

	t.Run("nil message in stream", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{Role: AgenticRoleTypeAssistant},
			nil,
			{Role: AgenticRoleTypeAssistant},
		}

		_, err := ConcatAgenticMessages(msgs)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "message at index 1 is nil")
	})

	t.Run("different roles", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{Role: AgenticRoleTypeUser},
			{Role: AgenticRoleTypeAssistant},
		}

		_, err := ConcatAgenticMessages(msgs)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "cannot concat messages with different roles")
	})

	t.Run("concat text blocks", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Hello ",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "World!",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Equal(t, AgenticRoleTypeAssistant, result.Role)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "Hello World!", result.ContentBlocks[0].AssistantGenText.Text)
	})

	t.Run("concat reasoning with nil index", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeReasoning,
						Reasoning: &Reasoning{
							Summary: []*ReasoningSummary{
								{Index: 0, Text: "First "},
							},
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeReasoning,
						Reasoning: &Reasoning{
							Summary: []*ReasoningSummary{
								{Index: 0, Text: "Second"},
							},
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Len(t, result.ContentBlocks[0].Reasoning.Summary, 1)
		assert.Equal(t, "First Second", result.ContentBlocks[0].Reasoning.Summary[0].Text)
		assert.Equal(t, 0, result.ContentBlocks[0].Reasoning.Summary[0].Index)
	})

	t.Run("concat reasoning with index", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeReasoning,
						Reasoning: &Reasoning{
							Summary: []*ReasoningSummary{
								{Index: 0, Text: "Part1-"},
								{Index: 1, Text: "Part2-"},
							},
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeReasoning,
						Reasoning: &Reasoning{
							Summary: []*ReasoningSummary{
								{Index: 0, Text: "Part3"},
								{Index: 1, Text: "Part4"},
							},
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Len(t, result.ContentBlocks[0].Reasoning.Summary, 2)
		assert.Equal(t, "Part1-Part3", result.ContentBlocks[0].Reasoning.Summary[0].Text)
		assert.Equal(t, "Part2-Part4", result.ContentBlocks[0].Reasoning.Summary[1].Text)
	})

	t.Run("concat user input text", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Hello ",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "World!",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "Hello World!", result.ContentBlocks[0].AssistantGenText.Text)
	})

	t.Run("concat assistant gen image", func(t *testing.T) {
		base1 := "1"
		base2 := "2"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenImage,
						AssistantGenImage: &AssistantGenImage{
							Base64Data: base1,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenImage,
						AssistantGenImage: &AssistantGenImage{
							Base64Data: base2,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "12", result.ContentBlocks[0].AssistantGenImage.Base64Data)
	})

	t.Run("concat user input audio - should error", func(t *testing.T) {
		url1 := "https://example.com/audio1.mp3"
		url2 := "https://example.com/audio2.mp3"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputAudio,
						UserInputAudio: &UserInputAudio{
							URL: url1,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputAudio,
						UserInputAudio: &UserInputAudio{
							URL: url2,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		_, err := ConcatAgenticMessages(msgs)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "cannot concat multiple user input audios")
	})

	t.Run("concat user input video - should error", func(t *testing.T) {
		url1 := "https://example.com/video1.mp4"
		url2 := "https://example.com/video2.mp4"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputVideo,
						UserInputVideo: &UserInputVideo{
							URL: url1,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputVideo,
						UserInputVideo: &UserInputVideo{
							URL: url2,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		_, err := ConcatAgenticMessages(msgs)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "cannot concat multiple user input videos")
	})

	t.Run("concat assistant gen text", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Generated ",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Text",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "Generated Text", result.ContentBlocks[0].AssistantGenText.Text)
	})

	t.Run("concat assistant gen image", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenImage,
						AssistantGenImage: &AssistantGenImage{
							Base64Data: "part1",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenImage,
						AssistantGenImage: &AssistantGenImage{
							Base64Data: "part2",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "part1part2", result.ContentBlocks[0].AssistantGenImage.Base64Data)
	})

	t.Run("concat assistant gen audio", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenAudio,
						AssistantGenAudio: &AssistantGenAudio{
							Base64Data: "audio1",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenAudio,
						AssistantGenAudio: &AssistantGenAudio{
							Base64Data: "audio2",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "audio1audio2", result.ContentBlocks[0].AssistantGenAudio.Base64Data)
	})

	t.Run("concat assistant gen video", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenVideo,
						AssistantGenVideo: &AssistantGenVideo{
							Base64Data: "video1",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenVideo,
						AssistantGenVideo: &AssistantGenVideo{
							Base64Data: "video2",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "video1video2", result.ContentBlocks[0].AssistantGenVideo.Base64Data)
	})

	t.Run("concat function tool call", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeFunctionToolCall,
						FunctionToolCall: &FunctionToolCall{
							CallID:    "call_123",
							Name:      "get_weather",
							Arguments: `{"location`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeFunctionToolCall,
						FunctionToolCall: &FunctionToolCall{
							Arguments: `":"NYC"}`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "call_123", result.ContentBlocks[0].FunctionToolCall.CallID)
		assert.Equal(t, "get_weather", result.ContentBlocks[0].FunctionToolCall.Name)
		assert.Equal(t, `{"location":"NYC"}`, result.ContentBlocks[0].FunctionToolCall.Arguments)
	})

	t.Run("concat function tool result", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeFunctionToolResult,
						FunctionToolResult: &FunctionToolResult{
							CallID: "call_123",
							Name:   "get_weather",
							Result: `{"temp`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeFunctionToolResult,
						FunctionToolResult: &FunctionToolResult{
							Result: `":72}`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "call_123", result.ContentBlocks[0].FunctionToolResult.CallID)
		assert.Equal(t, "get_weather", result.ContentBlocks[0].FunctionToolResult.Name)
		assert.Equal(t, `{"temp":72}`, result.ContentBlocks[0].FunctionToolResult.Result)
	})

	t.Run("concat server tool call", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeServerToolCall,
						ServerToolCall: &ServerToolCall{
							CallID: "server_call_1",
							Name:   "server_func",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeServerToolCall,
						ServerToolCall: &ServerToolCall{
							Arguments: map[string]any{"key": "value"},
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "server_call_1", result.ContentBlocks[0].ServerToolCall.CallID)
		assert.Equal(t, "server_func", result.ContentBlocks[0].ServerToolCall.Name)
		assert.NotNil(t, result.ContentBlocks[0].ServerToolCall.Arguments)
	})

	t.Run("concat server tool result", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeServerToolResult,
						ServerToolResult: &ServerToolResult{
							CallID: "server_call_1",
							Name:   "server_func",
							Result: "result1",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type:             ContentBlockTypeServerToolResult,
						ServerToolResult: &ServerToolResult{},
						StreamingMeta:    &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "server_call_1", result.ContentBlocks[0].ServerToolResult.CallID)
		assert.Equal(t, "server_func", result.ContentBlocks[0].ServerToolResult.Name)
		assert.Equal(t, "result1", result.ContentBlocks[0].ServerToolResult.Result)
	})

	t.Run("concat mcp tool call", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolCall,
						MCPToolCall: &MCPToolCall{
							ServerLabel: "mcp-server",
							CallID:      "mcp_call_1",
							Name:        "mcp_func",
							Arguments:   `{"arg`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolCall,
						MCPToolCall: &MCPToolCall{
							Arguments: `":123}`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "mcp-server", result.ContentBlocks[0].MCPToolCall.ServerLabel)
		assert.Equal(t, "mcp_call_1", result.ContentBlocks[0].MCPToolCall.CallID)
		assert.Equal(t, "mcp_func", result.ContentBlocks[0].MCPToolCall.Name)
		assert.Equal(t, `{"arg":123}`, result.ContentBlocks[0].MCPToolCall.Arguments)
	})

	t.Run("concat mcp tool result", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolResult,
						MCPToolResult: &MCPToolResult{
							ServerLabel: "mcp-server",
							CallID:      "mcp_call_1",
							Name:        "mcp_func",
							Result:      `First`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolResult,
						MCPToolResult: &MCPToolResult{
							Result: `Second`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "mcp-server", result.ContentBlocks[0].MCPToolResult.ServerLabel)
		assert.Equal(t, "mcp_call_1", result.ContentBlocks[0].MCPToolResult.CallID)
		assert.Equal(t, "mcp_func", result.ContentBlocks[0].MCPToolResult.Name)
		assert.Equal(t, `Second`, result.ContentBlocks[0].MCPToolResult.Result)
	})

	t.Run("concat mcp list tools", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPListToolsResult,
						MCPListToolsResult: &MCPListToolsResult{
							ServerLabel: "mcp-server",
							Tools: []*MCPListToolsItem{
								{Name: "tool1"},
							},
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPListToolsResult,
						MCPListToolsResult: &MCPListToolsResult{
							Tools: []*MCPListToolsItem{
								{Name: "tool2"},
							},
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "mcp-server", result.ContentBlocks[0].MCPListToolsResult.ServerLabel)
		assert.Len(t, result.ContentBlocks[0].MCPListToolsResult.Tools, 2)
	})

	t.Run("concat mcp tool approval request", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolApprovalRequest,
						MCPToolApprovalRequest: &MCPToolApprovalRequest{
							ID:          "approval_1",
							Name:        "approval_func",
							ServerLabel: "mcp-server",
							Arguments:   `{"request`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolApprovalRequest,
						MCPToolApprovalRequest: &MCPToolApprovalRequest{
							Arguments: `":1}`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "approval_1", result.ContentBlocks[0].MCPToolApprovalRequest.ID)
		assert.Equal(t, "approval_func", result.ContentBlocks[0].MCPToolApprovalRequest.Name)
		assert.Equal(t, "mcp-server", result.ContentBlocks[0].MCPToolApprovalRequest.ServerLabel)
		assert.Equal(t, `{"request":1}`, result.ContentBlocks[0].MCPToolApprovalRequest.Arguments)
	})

	t.Run("concat mcp tool approval response - should error", func(t *testing.T) {
		response1 := &MCPToolApprovalResponse{
			ApprovalRequestID: "approval_1",
			Approve:           false,
		}
		response2 := &MCPToolApprovalResponse{
			ApprovalRequestID: "approval_1",
			Approve:           true,
		}

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type:                    ContentBlockTypeMCPToolApprovalResponse,
						MCPToolApprovalResponse: response1,
						StreamingMeta:           &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type:                    ContentBlockTypeMCPToolApprovalResponse,
						MCPToolApprovalResponse: response2,
						StreamingMeta:           &StreamingMeta{Index: 0},
					},
				},
			},
		}

		_, err := ConcatAgenticMessages(msgs)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "cannot concat multiple mcp tool approval responses")
	})

	t.Run("concat response meta", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ResponseMeta: &AgenticResponseMeta{
					TokenUsage: &TokenUsage{
						PromptTokens:     10,
						CompletionTokens: 5,
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ResponseMeta: &AgenticResponseMeta{
					TokenUsage: &TokenUsage{
						PromptTokens:     10,
						CompletionTokens: 15,
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.NotNil(t, result.ResponseMeta)
		assert.Equal(t, 20, result.ResponseMeta.TokenUsage.CompletionTokens)
		assert.Equal(t, 20, result.ResponseMeta.TokenUsage.PromptTokens)
	})

	t.Run("mixed streaming and non-streaming blocks error", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Hello",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "World",
						},
						// No StreamingMeta - non-streaming
					},
				},
			},
		}

		_, err := ConcatAgenticMessages(msgs)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "found non-streaming block after streaming blocks")
	})

	t.Run("concat MCP tool call", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolCall,
						MCPToolCall: &MCPToolCall{
							ServerLabel: "mcp-server",
							CallID:      "call_456",
							Name:        "list_files",
							Arguments:   `{"path`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolCall,
						MCPToolCall: &MCPToolCall{
							Arguments: `":"/tmp"}`,
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "mcp-server", result.ContentBlocks[0].MCPToolCall.ServerLabel)
		assert.Equal(t, "call_456", result.ContentBlocks[0].MCPToolCall.CallID)
		assert.Equal(t, `{"path":"/tmp"}`, result.ContentBlocks[0].MCPToolCall.Arguments)
	})

	t.Run("concat user input text - should error", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputText,
						UserInputText: &UserInputText{
							Text: "What is ",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputText,
						UserInputText: &UserInputText{
							Text: "the weather?",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
				},
			},
		}

		_, err := ConcatAgenticMessages(msgs)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "cannot concat multiple user input texts")
	})

	t.Run("multiple stream indexes - sparse indexes", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Index0-",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Index2-",
						},
						StreamingMeta: &StreamingMeta{Index: 2},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Part2",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Part2",
						},
						StreamingMeta: &StreamingMeta{Index: 2},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 2)
		assert.Equal(t, "Index0-Part2", result.ContentBlocks[0].AssistantGenText.Text)
		assert.Equal(t, "Index2-Part2", result.ContentBlocks[1].AssistantGenText.Text)
	})

	t.Run("multiple stream indexes - mixed content types", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Text ",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeFunctionToolCall,
						FunctionToolCall: &FunctionToolCall{
							CallID:    "call_1",
							Name:      "func1",
							Arguments: `{"a`,
						},
						StreamingMeta: &StreamingMeta{Index: 1},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Content",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeFunctionToolCall,
						FunctionToolCall: &FunctionToolCall{
							Arguments: `":1}`,
						},
						StreamingMeta: &StreamingMeta{Index: 1},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 2)
		assert.Equal(t, "Text Content", result.ContentBlocks[0].AssistantGenText.Text)
		assert.Equal(t, "call_1", result.ContentBlocks[1].FunctionToolCall.CallID)
		assert.Equal(t, "func1", result.ContentBlocks[1].FunctionToolCall.Name)
		assert.Equal(t, `{"a":1}`, result.ContentBlocks[1].FunctionToolCall.Arguments)
	})

	t.Run("multiple stream indexes - three indexes", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "A",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "B",
						},
						StreamingMeta: &StreamingMeta{Index: 1},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "C",
						},
						StreamingMeta: &StreamingMeta{Index: 2},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "1",
						},
						StreamingMeta: &StreamingMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "2",
						},
						StreamingMeta: &StreamingMeta{Index: 1},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "3",
						},
						StreamingMeta: &StreamingMeta{Index: 2},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 3)
		assert.Equal(t, "A1", result.ContentBlocks[0].AssistantGenText.Text)
		assert.Equal(t, "B2", result.ContentBlocks[1].AssistantGenText.Text)
		assert.Equal(t, "C3", result.ContentBlocks[2].AssistantGenText.Text)
	})
}

func TestAgenticMessageFormat(t *testing.T) {
	m := &AgenticMessage{
		Role: AgenticRoleTypeUser,
		ContentBlocks: []*ContentBlock{
			{
				Type:          ContentBlockTypeUserInputText,
				UserInputText: &UserInputText{Text: "{a}"},
			},
			{
				Type: ContentBlockTypeUserInputImage,
				UserInputImage: &UserInputImage{
					URL:        "{b}",
					Base64Data: "{c}",
				},
			},
			{
				Type: ContentBlockTypeUserInputAudio,
				UserInputAudio: &UserInputAudio{
					URL:        "{d}",
					Base64Data: "{e}",
				},
			},
			{
				Type: ContentBlockTypeUserInputVideo,
				UserInputVideo: &UserInputVideo{
					URL:        "{f}",
					Base64Data: "{g}",
				},
			},
			{
				Type: ContentBlockTypeUserInputFile,
				UserInputFile: &UserInputFile{
					URL:        "{h}",
					Base64Data: "{i}",
				},
			},
		},
	}

	result, err := m.Format(context.Background(), map[string]any{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8", "i": "9",
	}, FString)
	assert.NoError(t, err)
	assert.Equal(t, []*AgenticMessage{{
		Role: AgenticRoleTypeUser,
		ContentBlocks: []*ContentBlock{
			{
				Type:          ContentBlockTypeUserInputText,
				UserInputText: &UserInputText{Text: "1"},
			},
			{
				Type: ContentBlockTypeUserInputImage,
				UserInputImage: &UserInputImage{
					URL:        "2",
					Base64Data: "3",
				},
			},
			{
				Type: ContentBlockTypeUserInputAudio,
				UserInputAudio: &UserInputAudio{
					URL:        "4",
					Base64Data: "5",
				},
			},
			{
				Type: ContentBlockTypeUserInputVideo,
				UserInputVideo: &UserInputVideo{
					URL:        "6",
					Base64Data: "7",
				},
			},
			{
				Type: ContentBlockTypeUserInputFile,
				UserInputFile: &UserInputFile{
					URL:        "8",
					Base64Data: "9",
				},
			},
		},
	}}, result)
}

func TestAgenticPlaceholderFormat(t *testing.T) {
	ctx := context.Background()
	ph := AgenticMessagesPlaceholder("a", false)

	result, err := ph.Format(ctx, map[string]any{
		"a": []*AgenticMessage{{Role: AgenticRoleTypeUser}, {Role: AgenticRoleTypeUser}},
	}, FString)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(result))

	ph = AgenticMessagesPlaceholder("a", true)

	result, err = ph.Format(ctx, map[string]any{}, FString)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(result))
}

func ptrOf[T any](v T) *T {
	return &v
}

func TestAgenticMessageString(t *testing.T) {
	longBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

	msg := &AgenticMessage{
		Role: AgenticRoleTypeAssistant,
		ContentBlocks: []*ContentBlock{
			{
				Type: ContentBlockTypeUserInputText,
				UserInputText: &UserInputText{
					Text: "What's the weather like in New York City today?",
				},
			},
			{
				Type: ContentBlockTypeUserInputImage,
				UserInputImage: &UserInputImage{
					URL:        "https://example.com/weather-map.jpg",
					Base64Data: longBase64,
					MIMEType:   "image/jpeg",
					Detail:     ImageURLDetailHigh,
				},
			},
			{
				Type: ContentBlockTypeUserInputAudio,
				UserInputAudio: &UserInputAudio{
					URL:        "http://audio.com",
					Base64Data: "audio_data",
					MIMEType:   "audio/mp3",
				},
			},
			{
				Type: ContentBlockTypeUserInputVideo,
				UserInputVideo: &UserInputVideo{
					URL:        "http://video.com",
					Base64Data: "video_data",
					MIMEType:   "video/mp4",
				},
			},
			{
				Type: ContentBlockTypeUserInputFile,
				UserInputFile: &UserInputFile{
					URL:        "http://file.com",
					Name:       "file.txt",
					Base64Data: "file_data",
					MIMEType:   "text/plain",
				},
			},
			{
				Type: ContentBlockTypeAssistantGenText,
				AssistantGenText: &AssistantGenText{
					Text: "I'll check the current weather in New York City for you.",
				},
			},
			{
				Type: ContentBlockTypeAssistantGenImage,
				AssistantGenImage: &AssistantGenImage{
					URL:        "http://gen_image.com",
					Base64Data: "gen_image_data",
					MIMEType:   "image/png",
				},
			},
			{
				Type: ContentBlockTypeAssistantGenAudio,
				AssistantGenAudio: &AssistantGenAudio{
					URL:        "http://gen_audio.com",
					Base64Data: "gen_audio_data",
					MIMEType:   "audio/wav",
				},
			},
			{
				Type: ContentBlockTypeAssistantGenVideo,
				AssistantGenVideo: &AssistantGenVideo{
					URL:        "http://gen_video.com",
					Base64Data: "gen_video_data",
					MIMEType:   "video/mp4",
				},
			},
			{
				Type: ContentBlockTypeReasoning,
				Reasoning: &Reasoning{
					Summary: []*ReasoningSummary{
						{Index: 0, Text: "First, I need to identify the location (New York City) from the user's query."},
						{Index: 1, Text: "Then, I should call the weather API to get current conditions."},
						{Index: 2, Text: "Finally, I'll format the response in a user-friendly way with temperature and conditions."},
					},
					EncryptedContent: "encrypted_reasoning_content_that_is_very_long_and_will_be_truncated_for_display",
				},
			},
			{
				Type: ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &FunctionToolCall{
					CallID:    "call_weather_123",
					Name:      "get_current_weather",
					Arguments: `{"location":"New York City","unit":"fahrenheit"}`,
				},
				StreamingMeta: &StreamingMeta{Index: 0},
			},
			{
				Type: ContentBlockTypeFunctionToolResult,
				FunctionToolResult: &FunctionToolResult{
					CallID: "call_weather_123",
					Name:   "get_current_weather",
					Result: `{"temperature":72,"condition":"sunny","humidity":45,"wind_speed":8}`,
				},
			},
			{
				Type: ContentBlockTypeServerToolCall,
				ServerToolCall: &ServerToolCall{
					Name:      "server_tool",
					CallID:    "call_1",
					Arguments: map[string]any{"a": 1},
				},
			},
			{
				Type: ContentBlockTypeServerToolResult,
				ServerToolResult: &ServerToolResult{
					Name:   "server_tool",
					CallID: "call_1",
					Result: map[string]any{"success": true},
				},
			},
			{
				Type: ContentBlockTypeMCPToolApprovalRequest,
				MCPToolApprovalRequest: &MCPToolApprovalRequest{
					ID:          "req_1",
					Name:        "mcp_tool",
					ServerLabel: "mcp_server",
					Arguments:   "{}",
				},
			},
			{
				Type: ContentBlockTypeMCPToolApprovalResponse,
				MCPToolApprovalResponse: &MCPToolApprovalResponse{
					ApprovalRequestID: "req_1",
					Approve:           true,
					Reason:            "looks good",
				},
			},
			{
				Type: ContentBlockTypeMCPToolCall,
				MCPToolCall: &MCPToolCall{
					ServerLabel: "weather-mcp-server",
					CallID:      "mcp_forecast_456",
					Name:        "get_7day_forecast",
					Arguments:   `{"city":"New York","days":7}`,
				},
			},
			{
				Type: ContentBlockTypeMCPToolResult,
				MCPToolResult: &MCPToolResult{
					CallID: "mcp_forecast_456",
					Name:   "get_7day_forecast",
					Result: `{"status":"partial","days_available":3}`,
					Error: &MCPToolCallError{
						Code:    ptrOf[int64](503),
						Message: "Service temporarily unavailable for full 7-day forecast",
					},
				},
			},
			{
				Type: ContentBlockTypeMCPListToolsResult,
				MCPListToolsResult: &MCPListToolsResult{
					ServerLabel: "weather-mcp-server",
					Tools: []*MCPListToolsItem{
						{Name: "get_current_weather", Description: "Get current weather conditions for a location"},
						{Name: "get_7day_forecast", Description: "Get 7-day weather forecast"},
						{Name: "get_weather_alerts", Description: "Get active weather alerts and warnings"},
					},
				},
			},
		},
		ResponseMeta: &AgenticResponseMeta{
			TokenUsage: &TokenUsage{
				PromptTokens:     250,
				CompletionTokens: 180,
				TotalTokens:      430,
			},
		},
	}

	// Print the formatted output
	output := msg.String()

	assert.Equal(t, `role: assistant
content_blocks:
  [0] type: user_input_text
      text: What's the weather like in New York City today?
  [1] type: user_input_image
      url: https://example.com/weather-map.jpg
      base64_data: iVBORw0KGgoAAAANSUhE...... (96 bytes)
      mime_type: image/jpeg
      detail: high
  [2] type: user_input_audio
      url: http://audio.com
      base64_data: audio_data... (10 bytes)
      mime_type: audio/mp3
  [3] type: user_input_video
      url: http://video.com
      base64_data: video_data... (10 bytes)
      mime_type: video/mp4
  [4] type: user_input_file
      name: file.txt
      url: http://file.com
      base64_data: file_data... (9 bytes)
      mime_type: text/plain
  [5] type: assistant_gen_text
      text: I'll check the current weather in New York City for you.
  [6] type: assistant_gen_image
      url: http://gen_image.com
      base64_data: gen_image_data... (14 bytes)
      mime_type: image/png
  [7] type: assistant_gen_audio
      url: http://gen_audio.com
      base64_data: gen_audio_data... (14 bytes)
      mime_type: audio/wav
  [8] type: assistant_gen_video
      url: http://gen_video.com
      base64_data: gen_video_data... (14 bytes)
      mime_type: video/mp4
  [9] type: reasoning
      summary: 3 items
        [0] First, I need to identify the location (New York City) from the user's query.
        [1] Then, I should call the weather API to get current conditions.
        [2] Finally, I'll format the response in a user-friendly way with temperature and conditions.
      encrypted_content: encrypted_reasoning_content_that_is_very_long_and_...
  [10] type: function_tool_call
      call_id: call_weather_123
      name: get_current_weather
      arguments: {"location":"New York City","unit":"fahrenheit"}
      stream_index: 0
  [11] type: function_tool_result
      call_id: call_weather_123
      name: get_current_weather
      result: {"temperature":72,"condition":"sunny","humidity":45,"wind_speed":8}
  [12] type: server_tool_call
      name: server_tool
      call_id: call_1
      arguments: {
  "a": 1
}
  [13] type: server_tool_result
      name: server_tool
      call_id: call_1
      result: {
  "success": true
}
  [14] type: mcp_tool_approval_request
      server_label: mcp_server
      id: req_1
      name: mcp_tool
      arguments: {}
  [15] type: mcp_tool_approval_response
      approval_request_id: req_1
      approve: true
      reason: looks good
  [16] type: mcp_tool_call
      server_label: weather-mcp-server
      call_id: mcp_forecast_456
      name: get_7day_forecast
      arguments: {"city":"New York","days":7}
  [17] type: mcp_tool_result
      call_id: mcp_forecast_456
      name: get_7day_forecast
      result: {"status":"partial","days_available":3}
      error: [503] Service temporarily unavailable for full 7-day forecast
  [18] type: mcp_list_tools_result
      server_label: weather-mcp-server
      tools: 3 items
        - get_current_weather: Get current weather conditions for a location
        - get_7day_forecast: Get 7-day weather forecast
        - get_weather_alerts: Get active weather alerts and warnings
response_meta:
  token_usage: prompt=250, completion=180, total=430
`, output)

	t.Run("nil/empty fields", func(t *testing.T) {
		msg := &AgenticMessage{
			Role: AgenticRoleTypeUser,
			ContentBlocks: []*ContentBlock{
				{Type: ContentBlockTypeUserInputAudio, UserInputAudio: &UserInputAudio{}}, // empty
				{Type: ContentBlockTypeUserInputVideo, UserInputVideo: &UserInputVideo{}},
				{Type: ContentBlockTypeUserInputFile, UserInputFile: &UserInputFile{}},
				{Type: ContentBlockTypeAssistantGenImage, AssistantGenImage: &AssistantGenImage{}},
				{Type: ContentBlockTypeAssistantGenAudio, AssistantGenAudio: &AssistantGenAudio{}},
				{Type: ContentBlockTypeAssistantGenVideo, AssistantGenVideo: &AssistantGenVideo{}},
				{Type: ContentBlockTypeServerToolCall, ServerToolCall: &ServerToolCall{Name: "t"}},                                 // No CallID
				{Type: ContentBlockTypeServerToolResult, ServerToolResult: &ServerToolResult{Name: "t"}},                           // No CallID
				{Type: ContentBlockTypeMCPToolResult, MCPToolResult: &MCPToolResult{Name: "t"}},                                    // No Error
				{Type: ContentBlockTypeMCPListToolsResult, MCPListToolsResult: &MCPListToolsResult{}},                              // No Error
				{Type: ContentBlockTypeMCPToolApprovalResponse, MCPToolApprovalResponse: &MCPToolApprovalResponse{Approve: false}}, // No Reason
				nil, // Nil block in slice
			},
		}

		s := msg.String()
		assert.Contains(t, s, "type: user_input_audio")
		assert.NotContains(t, s, "mime_type:")
		assert.Contains(t, s, "type: server_tool_call")
	})

	t.Run("nil content struct in block", func(t *testing.T) {
		// Test cases where the specific content struct is nil but type is set
		// This shouldn't crash and should just print type
		msg := &AgenticMessage{
			ContentBlocks: []*ContentBlock{
				{Type: ContentBlockTypeReasoning, Reasoning: nil},
				{Type: ContentBlockTypeUserInputText, UserInputText: nil},
				{Type: ContentBlockTypeUserInputImage, UserInputImage: nil},
				{Type: ContentBlockTypeUserInputAudio, UserInputAudio: nil},
				{Type: ContentBlockTypeUserInputVideo, UserInputVideo: nil},
				{Type: ContentBlockTypeUserInputFile, UserInputFile: nil},
				{Type: ContentBlockTypeAssistantGenText, AssistantGenText: nil},
				{Type: ContentBlockTypeAssistantGenImage, AssistantGenImage: nil},
				{Type: ContentBlockTypeAssistantGenAudio, AssistantGenAudio: nil},
				{Type: ContentBlockTypeAssistantGenVideo, AssistantGenVideo: nil},
				{Type: ContentBlockTypeFunctionToolCall, FunctionToolCall: nil},
				{Type: ContentBlockTypeFunctionToolResult, FunctionToolResult: nil},
				{Type: ContentBlockTypeServerToolCall, ServerToolCall: nil},
				{Type: ContentBlockTypeServerToolResult, ServerToolResult: nil},
				{Type: ContentBlockTypeMCPToolCall, MCPToolCall: nil},
				{Type: ContentBlockTypeMCPToolResult, MCPToolResult: nil},
				{Type: ContentBlockTypeMCPListToolsResult, MCPListToolsResult: nil},
				{Type: ContentBlockTypeMCPToolApprovalRequest, MCPToolApprovalRequest: nil},
				{Type: ContentBlockTypeMCPToolApprovalResponse, MCPToolApprovalResponse: nil},
			},
		}
		s := msg.String()
		assert.Contains(t, s, "type: reasoning")
		// ensure no panic and basic output present
	})
}

func TestDeveloperAgenticMessage(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		msg := DeveloperAgenticMessage("developer")
		assert.Equal(t, AgenticRoleTypeDeveloper, msg.Role)
		assert.Len(t, msg.ContentBlocks, 1)
		assert.Equal(t, "developer", msg.ContentBlocks[0].UserInputText.Text)
	})
}

func TestSystemAgenticMessage(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		msg := SystemAgenticMessage("system")
		assert.Equal(t, AgenticRoleTypeSystem, msg.Role)
		assert.Len(t, msg.ContentBlocks, 1)
		assert.Equal(t, "system", msg.ContentBlocks[0].UserInputText.Text)
	})
}

func TestUserAgenticMessage(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		msg := UserAgenticMessage("user")
		assert.Equal(t, AgenticRoleTypeUser, msg.Role)
		assert.Len(t, msg.ContentBlocks, 1)
		assert.Equal(t, "user", msg.ContentBlocks[0].UserInputText.Text)
	})
}

func TestFunctionToolResultAgenticMessage(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		msg := FunctionToolResultAgenticMessage("call_1", "tool_name", "result_str")
		assert.Equal(t, AgenticRoleTypeUser, msg.Role)
		assert.Len(t, msg.ContentBlocks, 1)
		assert.Equal(t, ContentBlockTypeFunctionToolResult, msg.ContentBlocks[0].Type)
		assert.Equal(t, "call_1", msg.ContentBlocks[0].FunctionToolResult.CallID)
		assert.Equal(t, "tool_name", msg.ContentBlocks[0].FunctionToolResult.Name)
		assert.Equal(t, "result_str", msg.ContentBlocks[0].FunctionToolResult.Result)
	})
}

func TestNewContentBlock(t *testing.T) {
	cbType := reflect.TypeOf(ContentBlock{})
	for i := 0; i < cbType.NumField(); i++ {
		field := cbType.Field(i)

		// Skip non-content fields
		if field.Name == "Type" || field.Name == "Extra" || field.Name == "StreamingMeta" {
			continue
		}

		t.Run(field.Name, func(t *testing.T) {
			// Ensure field is a pointer
			assert.Equal(t, reflect.Ptr, field.Type.Kind(), "Field %s should be a pointer", field.Name)

			// Create a new instance of the field's type
			// field.Type is *T, so Elem() is T. reflect.New(T) returns *T.
			elemType := field.Type.Elem()
			inputVal := reflect.New(elemType)
			input := inputVal.Interface()

			// Call NewContentBlock (generic) via type switch
			var block *ContentBlock
			switch v := input.(type) {
			case *Reasoning:
				block = NewContentBlock(v)
			case *UserInputText:
				block = NewContentBlock(v)
			case *UserInputImage:
				block = NewContentBlock(v)
			case *UserInputAudio:
				block = NewContentBlock(v)
			case *UserInputVideo:
				block = NewContentBlock(v)
			case *UserInputFile:
				block = NewContentBlock(v)
			case *AssistantGenText:
				block = NewContentBlock(v)
			case *AssistantGenImage:
				block = NewContentBlock(v)
			case *AssistantGenAudio:
				block = NewContentBlock(v)
			case *AssistantGenVideo:
				block = NewContentBlock(v)
			case *FunctionToolCall:
				block = NewContentBlock(v)
			case *FunctionToolResult:
				block = NewContentBlock(v)
			case *ServerToolCall:
				block = NewContentBlock(v)
			case *ServerToolResult:
				block = NewContentBlock(v)
			case *MCPToolCall:
				block = NewContentBlock(v)
			case *MCPToolResult:
				block = NewContentBlock(v)
			case *MCPListToolsResult:
				block = NewContentBlock(v)
			case *MCPToolApprovalRequest:
				block = NewContentBlock(v)
			case *MCPToolApprovalResponse:
				block = NewContentBlock(v)
			default:
				t.Fatalf("unsupported ContentBlock field type: %T", input)
			}

			// Assertions
			assert.NotNil(t, block, "NewContentBlock should return non-nil for type %T", input)

			// Check if the corresponding field in block is set equals to input
			blockVal := reflect.ValueOf(block).Elem()
			fieldVal := blockVal.FieldByName(field.Name)
			assert.True(t, fieldVal.IsValid(), "Field %s not found in result", field.Name)
			assert.Equal(t, input, fieldVal.Interface(), "Field %s should match input", field.Name)

			// Check Type is set
			typeVal := blockVal.FieldByName("Type")
			assert.NotEmpty(t, typeVal.String(), "Type should be set for %s", field.Name)
		})
	}
}
