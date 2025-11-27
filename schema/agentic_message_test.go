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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputText,
						UserInputText: &UserInputText{
							Text: "Hello ",
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputText,
						UserInputText: &UserInputText{
							Text: "World!",
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "Hello World!", result.ContentBlocks[0].UserInputText.Text)
	})

	t.Run("concat user input image", func(t *testing.T) {
		url1 := "https://example.com/image1.jpg"
		url2 := "https://example.com/image2.jpg"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputImage,
						UserInputImage: &UserInputImage{
							URL: &url1,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputImage,
						UserInputImage: &UserInputImage{
							URL: &url2,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		// Should take the last image
		assert.Equal(t, url2, *result.ContentBlocks[0].UserInputImage.URL)
	})

	t.Run("concat user input audio", func(t *testing.T) {
		url1 := "https://example.com/audio1.mp3"
		url2 := "https://example.com/audio2.mp3"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputAudio,
						UserInputAudio: &UserInputAudio{
							URL: &url1,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputAudio,
						UserInputAudio: &UserInputAudio{
							URL: &url2,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		// Should take the last audio
		assert.Equal(t, url2, *result.ContentBlocks[0].UserInputAudio.URL)
	})

	t.Run("concat user input video", func(t *testing.T) {
		url1 := "https://example.com/video1.mp4"
		url2 := "https://example.com/video2.mp4"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputVideo,
						UserInputVideo: &UserInputVideo{
							URL: &url1,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputVideo,
						UserInputVideo: &UserInputVideo{
							URL: &url2,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		// Should take the last video
		assert.Equal(t, url2, *result.ContentBlocks[0].UserInputVideo.URL)
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
		url1 := "https://example.com/gen_image1.jpg"
		url2 := "https://example.com/gen_image2.jpg"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenImage,
						AssistantGenImage: &AssistantGenImage{
							URL: &url1,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenImage,
						AssistantGenImage: &AssistantGenImage{
							URL: &url2,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		// Should take the last image
		assert.Equal(t, url2, *result.ContentBlocks[0].AssistantGenImage.URL)
	})

	t.Run("concat assistant gen audio", func(t *testing.T) {
		url1 := "https://example.com/gen_audio1.mp3"
		url2 := "https://example.com/gen_audio2.mp3"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenAudio,
						AssistantGenAudio: &AssistantGenAudio{
							URL: &url1,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenAudio,
						AssistantGenAudio: &AssistantGenAudio{
							URL: &url2,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		// Should take the last audio
		assert.Equal(t, url2, *result.ContentBlocks[0].AssistantGenAudio.URL)
	})

	t.Run("concat assistant gen video", func(t *testing.T) {
		url1 := "https://example.com/gen_video1.mp4"
		url2 := "https://example.com/gen_video2.mp4"

		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenVideo,
						AssistantGenVideo: &AssistantGenVideo{
							URL: &url1,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeAssistantGenVideo,
						AssistantGenVideo: &AssistantGenVideo{
							URL: &url2,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		// Should take the last video
		assert.Equal(t, url2, *result.ContentBlocks[0].AssistantGenVideo.URL)
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeServerToolResult,
						ServerToolResult: &ServerToolResult{
							Result: "result2",
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "server_call_1", result.ContentBlocks[0].ServerToolResult.CallID)
		assert.Equal(t, "server_func", result.ContentBlocks[0].ServerToolResult.Name)
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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
							CallID: "mcp_call_1",
							Name:   "mcp_func",
							Result: `{"res`,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPToolResult,
						MCPToolResult: &MCPToolResult{
							Result: `ult":true}`,
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "mcp_call_1", result.ContentBlocks[0].MCPToolResult.CallID)
		assert.Equal(t, "mcp_func", result.ContentBlocks[0].MCPToolResult.Name)
		assert.Equal(t, `{"result":true}`, result.ContentBlocks[0].MCPToolResult.Result)
	})

	t.Run("concat mcp list tools", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPListTools,
						MCPListToolsResult: &MCPListToolsResult{
							ServerLabel: "mcp-server",
							Tools: []MCPListToolsItem{
								{Name: "tool1"},
							},
						},
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeMCPListTools,
						MCPListToolsResult: &MCPListToolsResult{
							Tools: []MCPListToolsItem{
								{Name: "tool2"},
							},
						},
						StreamMeta: &StreamMeta{Index: 0},
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
							CallID:      "approval_1",
							Name:        "approval_func",
							ServerLabel: "mcp-server",
							Arguments:   `{"request`,
						},
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "approval_1", result.ContentBlocks[0].MCPToolApprovalRequest.CallID)
		assert.Equal(t, "approval_func", result.ContentBlocks[0].MCPToolApprovalRequest.Name)
		assert.Equal(t, "mcp-server", result.ContentBlocks[0].MCPToolApprovalRequest.ServerLabel)
		assert.Equal(t, `{"request":1}`, result.ContentBlocks[0].MCPToolApprovalRequest.Arguments)
	})

	t.Run("concat mcp tool approval response", func(t *testing.T) {
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
						StreamMeta:              &StreamMeta{Index: 0},
					},
				},
			},
			{
				Role: AgenticRoleTypeAssistant,
				ContentBlocks: []*ContentBlock{
					{
						Type:                    ContentBlockTypeMCPToolApprovalResponse,
						MCPToolApprovalResponse: response2,
						StreamMeta:              &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		// Should take the last response
		assert.Equal(t, response2, result.ContentBlocks[0].MCPToolApprovalResponse)
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
						StreamMeta: &StreamMeta{Index: 0},
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
						// No StreamMeta - non-streaming
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
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
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

	t.Run("concat user input text", func(t *testing.T) {
		msgs := []*AgenticMessage{
			{
				Role: AgenticRoleTypeUser,
				ContentBlocks: []*ContentBlock{
					{
						Type: ContentBlockTypeUserInputText,
						UserInputText: &UserInputText{
							Text: "What is ",
						},
						StreamMeta: &StreamMeta{Index: 0},
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
						StreamMeta: &StreamMeta{Index: 0},
					},
				},
			},
		}

		result, err := ConcatAgenticMessages(msgs)
		assert.NoError(t, err)
		assert.Len(t, result.ContentBlocks, 1)
		assert.Equal(t, "What is the weather?", result.ContentBlocks[0].UserInputText.Text)
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
						StreamMeta: &StreamMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Index2-",
						},
						StreamMeta: &StreamMeta{Index: 2},
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
						StreamMeta: &StreamMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "Part2",
						},
						StreamMeta: &StreamMeta{Index: 2},
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
						StreamMeta: &StreamMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeFunctionToolCall,
						FunctionToolCall: &FunctionToolCall{
							CallID:    "call_1",
							Name:      "func1",
							Arguments: `{"a`,
						},
						StreamMeta: &StreamMeta{Index: 1},
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
						StreamMeta: &StreamMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeFunctionToolCall,
						FunctionToolCall: &FunctionToolCall{
							Arguments: `":1}`,
						},
						StreamMeta: &StreamMeta{Index: 1},
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
						StreamMeta: &StreamMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "B",
						},
						StreamMeta: &StreamMeta{Index: 1},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "C",
						},
						StreamMeta: &StreamMeta{Index: 2},
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
						StreamMeta: &StreamMeta{Index: 0},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "2",
						},
						StreamMeta: &StreamMeta{Index: 1},
					},
					{
						Type: ContentBlockTypeAssistantGenText,
						AssistantGenText: &AssistantGenText{
							Text: "3",
						},
						StreamMeta: &StreamMeta{Index: 2},
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
					URL:        ptrOf("{b}"),
					Base64Data: ptrOf("{c}"),
				},
			},
			{
				Type: ContentBlockTypeUserInputAudio,
				UserInputAudio: &UserInputAudio{
					URL:        ptrOf("{d}"),
					Base64Data: ptrOf("{e}"),
				},
			},
			{
				Type: ContentBlockTypeUserInputVideo,
				UserInputVideo: &UserInputVideo{
					URL:        ptrOf("{f}"),
					Base64Data: ptrOf("{g}"),
				},
			},
			{
				Type: ContentBlockTypeUserInputFile,
				UserInputFile: &UserInputFile{
					URL:        ptrOf("{h}"),
					Base64Data: ptrOf("{i}"),
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
					URL:        ptrOf("2"),
					Base64Data: ptrOf("3"),
				},
			},
			{
				Type: ContentBlockTypeUserInputAudio,
				UserInputAudio: &UserInputAudio{
					URL:        ptrOf("4"),
					Base64Data: ptrOf("5"),
				},
			},
			{
				Type: ContentBlockTypeUserInputVideo,
				UserInputVideo: &UserInputVideo{
					URL:        ptrOf("6"),
					Base64Data: ptrOf("7"),
				},
			},
			{
				Type: ContentBlockTypeUserInputFile,
				UserInputFile: &UserInputFile{
					URL:        ptrOf("8"),
					Base64Data: ptrOf("9"),
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
