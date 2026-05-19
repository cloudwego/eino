/*
 * Copyright 2024 CloudWeGo Authors
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

package compose

import (
	"io"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestAgenticMessageToToolCallMessage(t *testing.T) {
	input := &schema.AgenticMessage{
		ContentBlocks: []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					CallID:    "1",
					Name:      "name1",
					Arguments: "arg1",
				},
			},
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					CallID:    "2",
					Name:      "name2",
					Arguments: "arg2",
				},
			},
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					CallID:    "3",
					Name:      "name3",
					Arguments: "arg3",
				},
			},
		},
	}
	ret := agenticMessageToToolCallMessage(input)
	assert.Equal(t, schema.Assistant, ret.Role)
	assert.Equal(t, []schema.ToolCall{
		{
			ID: "1",
			Function: schema.FunctionCall{
				Name:      "name1",
				Arguments: "arg1",
			},
		},
		{
			ID: "2",
			Function: schema.FunctionCall{
				Name:      "name2",
				Arguments: "arg2",
			},
		},
		{
			ID: "3",
			Function: schema.FunctionCall{
				Name:      "name3",
				Arguments: "arg3",
			},
		},
	}, ret.ToolCalls)
}

func TestToolMessageToAgenticMessage(t *testing.T) {
	t.Run("text only", func(t *testing.T) {
		input := []*schema.Message{
			{
				Role:       schema.Tool,
				Content:    "content1",
				ToolCallID: "1",
				ToolName:   "name1",
			},
			{
				Role:       schema.Tool,
				Content:    "content2",
				ToolCallID: "2",
				ToolName:   "name2",
			},
			{
				Role:       schema.Tool,
				Content:    "content3",
				ToolCallID: "3",
				ToolName:   "name3",
			},
		}
		ret := toolMessageToAgenticMessage(input)
		assert.Equal(t, 3, len(ret))
		for i, msg := range ret {
			assert.Equal(t, schema.AgenticRoleTypeUser, msg.Role)
			assert.Equal(t, 1, len(msg.ContentBlocks))
			assert.Equal(t, schema.ContentBlockTypeFunctionToolResult, msg.ContentBlocks[0].Type)
			ftr := msg.ContentBlocks[0].FunctionToolResult
			assert.Equal(t, input[i].ToolCallID, ftr.CallID)
			assert.Equal(t, input[i].ToolName, ftr.Name)
			assert.Equal(t, 1, len(ftr.Content))
			assert.Equal(t, input[i].Content, ftr.Content[0].Text.Text)
		}
	})

	t.Run("with multimodal content", func(t *testing.T) {
		imageURL := "https://example.com/image.png"
		audioBase64 := "YXVkaW9kYXRh"
		videoURL := "https://example.com/video.mp4"
		fileURL := "https://example.com/file.pdf"

		input := []*schema.Message{
			{
				Role:       schema.Tool,
				Content:    "text result",
				ToolCallID: "1",
				ToolName:   "tool1",
				UserInputMultiContent: []schema.MessageInputPart{
					{Type: schema.ChatMessagePartTypeText, Text: "hello"},
					{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
						MessagePartCommon: schema.MessagePartCommon{URL: &imageURL, MIMEType: "image/png"},
						Detail:            schema.ImageURLDetailHigh,
					}},
					{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{
						MessagePartCommon: schema.MessagePartCommon{Base64Data: &audioBase64, MIMEType: "audio/wav"},
					}},
					{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{
						MessagePartCommon: schema.MessagePartCommon{URL: &videoURL, MIMEType: "video/mp4"},
					}},
					{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{
						MessagePartCommon: schema.MessagePartCommon{URL: &fileURL, MIMEType: "application/pdf"},
					}},
				},
			},
			{
				Role:       schema.Tool,
				Content:    "plain result",
				ToolCallID: "2",
				ToolName:   "tool2",
			},
		}

		ret := toolMessageToAgenticMessage(input)
		assert.Equal(t, 2, len(ret))

		// first message: multimodal tool result
		assert.Equal(t, schema.AgenticRoleTypeUser, ret[0].Role)
		assert.Equal(t, 1, len(ret[0].ContentBlocks))
		ftr1 := ret[0].ContentBlocks[0].FunctionToolResult
		assert.Equal(t, "1", ftr1.CallID)
		assert.Equal(t, 5, len(ftr1.Content))

		assert.Equal(t, "hello", ftr1.Content[0].Text.Text)

		assert.Equal(t, imageURL, ftr1.Content[1].Image.URL)
		assert.Equal(t, schema.ImageURLDetailHigh, ftr1.Content[1].Image.Detail)

		assert.Equal(t, audioBase64, ftr1.Content[2].Audio.Base64Data)

		assert.Equal(t, videoURL, ftr1.Content[3].Video.URL)

		assert.Equal(t, fileURL, ftr1.Content[4].File.URL)

		// second message: text-only tool result
		assert.Equal(t, schema.AgenticRoleTypeUser, ret[1].Role)
		assert.Equal(t, 1, len(ret[1].ContentBlocks))
		ftr2 := ret[1].ContentBlocks[0].FunctionToolResult
		assert.Equal(t, "2", ftr2.CallID)
		assert.Equal(t, 1, len(ftr2.Content))
		assert.Equal(t, "plain result", ftr2.Content[0].Text.Text)
	})

	t.Run("nil media fields are skipped", func(t *testing.T) {
		input := []*schema.Message{
			{
				Role:       schema.Tool,
				Content:    "result",
				ToolCallID: "1",
				ToolName:   "tool1",
				UserInputMultiContent: []schema.MessageInputPart{
					{Type: schema.ChatMessagePartTypeImageURL, Image: nil},
					{Type: schema.ChatMessagePartTypeAudioURL, Audio: nil},
					{Type: schema.ChatMessagePartTypeVideoURL, Video: nil},
					{Type: schema.ChatMessagePartTypeFileURL, File: nil},
					{Type: schema.ChatMessagePartTypeText, Text: "only text"},
				},
			},
		}
		ret := toolMessageToAgenticMessage(input)
		assert.Equal(t, 1, len(ret))
		ftr := ret[0].ContentBlocks[0].FunctionToolResult
		assert.Equal(t, 1, len(ftr.Content))
		assert.Equal(t, "only text", ftr.Content[0].Text.Text)
	})

	t.Run("tool search result", func(t *testing.T) {
		input := []*schema.Message{
			{
				Role:       schema.Tool,
				ToolCallID: "call_1",
				ToolName:   "tool_search",
				UserInputMultiContent: []schema.MessageInputPart{
					{
						Type: schema.ChatMessagePartTypeToolSearchResult,
						ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{
							{Name: "dynamic_tool", Desc: "dynamic tool"},
						}},
					},
				},
			},
		}

		ret := toolMessageToAgenticMessage(input)
		require.Len(t, ret, 1)
		require.Len(t, ret[0].ContentBlocks, 1)
		block := ret[0].ContentBlocks[0]
		assert.Equal(t, schema.ContentBlockTypeToolSearchResult, block.Type)
		require.NotNil(t, block.ToolSearchFunctionToolResult)
		assert.Equal(t, "call_1", block.ToolSearchFunctionToolResult.CallID)
		assert.Equal(t, "tool_search", block.ToolSearchFunctionToolResult.Name)
		require.NotNil(t, block.ToolSearchFunctionToolResult.Result)
		require.Len(t, block.ToolSearchFunctionToolResult.Result.Tools, 1)
		assert.Equal(t, "dynamic_tool", block.ToolSearchFunctionToolResult.Result.Tools[0].Name)
	})
}

func TestStreamToolMessageToAgenticMessage(t *testing.T) {
	t.Run("text only", func(t *testing.T) {
		testStreamToolMessageTextOnly(t)
	})

	t.Run("with multimodal content", func(t *testing.T) {
		imageURL := "https://example.com/image.png"
		input := schema.StreamReaderFromArray([][]*schema.Message{
			{
				{
					Role:       schema.Tool,
					Content:    "result1",
					ToolName:   "tool1",
					ToolCallID: "1",
					UserInputMultiContent: []schema.MessageInputPart{
						{Type: schema.ChatMessagePartTypeText, Text: "text part"},
						{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
							MessagePartCommon: schema.MessagePartCommon{URL: &imageURL},
						}},
					},
				},
				nil,
			},
			{
				nil,
				{
					Role:       schema.Tool,
					Content:    "result2",
					ToolName:   "tool2",
					ToolCallID: "2",
				},
			},
		})
		ret := streamToolMessageToAgenticMessage(input)
		var chunks [][]*schema.AgenticMessage
		for {
			chunk, err := ret.Recv()
			if err == io.EOF {
				break
			}
			assert.NoError(t, err)
			chunks = append(chunks, chunk)
		}
		result, err := schema.ConcatAgenticMessagesArray(chunks)
		assert.NoError(t, err)

		assert.Equal(t, 2, len(result))

		// first message: multimodal tool result (single chunk → StreamingMeta preserved)
		assert.Equal(t, schema.AgenticRoleTypeUser, result[0].Role)
		assert.Equal(t, 1, len(result[0].ContentBlocks))
		ftr1 := result[0].ContentBlocks[0].FunctionToolResult
		assert.Equal(t, "1", ftr1.CallID)
		assert.Equal(t, 2, len(ftr1.Content))
		assert.NotNil(t, ftr1.Content[0].Text)
		assert.NotNil(t, ftr1.Content[1].Image)
		assert.Equal(t, imageURL, ftr1.Content[1].Image.URL)

		// second message: text-only tool result (single chunk → StreamingMeta preserved)
		assert.Equal(t, schema.AgenticRoleTypeUser, result[1].Role)
		assert.Equal(t, 1, len(result[1].ContentBlocks))
		ftr2 := result[1].ContentBlocks[0].FunctionToolResult
		assert.Equal(t, "2", ftr2.CallID)
		assert.Equal(t, 1, len(ftr2.Content))
		assert.Equal(t, "result2", ftr2.Content[0].Text.Text)
	})

	t.Run("tool search result", func(t *testing.T) {
		input := schema.StreamReaderFromArray([][]*schema.Message{
			{
				{
					Role:       schema.Tool,
					ToolName:   "tool_search",
					ToolCallID: "call_1",
					UserInputMultiContent: []schema.MessageInputPart{
						{
							Type: schema.ChatMessagePartTypeToolSearchResult,
							ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{
								{Name: "dynamic_tool", Desc: "dynamic tool"},
							}},
						},
					},
				},
			},
		})

		ret := streamToolMessageToAgenticMessage(input)
		chunk, err := ret.Recv()
		require.NoError(t, err)
		require.Len(t, chunk, 1)
		require.Len(t, chunk[0].ContentBlocks, 1)
		block := chunk[0].ContentBlocks[0]
		assert.Equal(t, schema.ContentBlockTypeToolSearchResult, block.Type)
		assert.Equal(t, &schema.StreamingMeta{Index: 0}, block.StreamingMeta)
		require.NotNil(t, block.ToolSearchFunctionToolResult)
		assert.Equal(t, "call_1", block.ToolSearchFunctionToolResult.CallID)
		assert.Equal(t, "tool_search", block.ToolSearchFunctionToolResult.Name)
		require.NotNil(t, block.ToolSearchFunctionToolResult.Result)
		require.Len(t, block.ToolSearchFunctionToolResult.Result.Tools, 1)
		assert.Equal(t, "dynamic_tool", block.ToolSearchFunctionToolResult.Result.Tools[0].Name)
	})
}

func testStreamToolMessageTextOnly(t *testing.T) {
	input := schema.StreamReaderFromArray([][]*schema.Message{
		{
			{
				Role:       schema.Tool,
				Content:    "content1-1",
				ToolName:   "name1",
				ToolCallID: "1",
			},
			nil, nil,
		},
		{
			nil,
			{
				Role:       schema.Tool,
				Content:    "content2-1",
				ToolName:   "name2",
				ToolCallID: "2",
			},
			nil,
		},
		{
			nil,
			{
				Role:       schema.Tool,
				Content:    "content2-2",
				ToolName:   "name2",
				ToolCallID: "2",
			},
			nil,
		},
		{
			nil, nil,
			{
				Role:       schema.Tool,
				Content:    "content3-1",
				ToolName:   "name3",
				ToolCallID: "3",
			},
		},
		{
			nil, nil,
			{
				Role:       schema.Tool,
				Content:    "content3-2",
				ToolName:   "name3",
				ToolCallID: "3",
			},
		},
	})
	ret := streamToolMessageToAgenticMessage(input)
	var chunks [][]*schema.AgenticMessage
	for {
		chunk, err := ret.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		chunks = append(chunks, chunk)
	}
	result, err := schema.ConcatAgenticMessagesArray(chunks)
	assert.NoError(t, err)

	actualStr, err := sonic.MarshalString(result)
	assert.NoError(t, err)

	expected := []*schema.AgenticMessage{
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: "1",
						Name:   "name1",
						Content: []*schema.FunctionToolResultContentBlock{
							{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "content1-1"}},
						},
					},
					StreamingMeta: &schema.StreamingMeta{Index: 0},
				},
			},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: "2",
						Name:   "name2",
						Content: []*schema.FunctionToolResultContentBlock{
							{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "content2-1"}},
							{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "content2-2"}},
						},
					},
				},
			},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: "3",
						Name:   "name3",
						Content: []*schema.FunctionToolResultContentBlock{
							{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "content3-1"}},
							{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "content3-2"}},
						},
					},
				},
			},
		},
	}

	expectedStr, err := sonic.MarshalString(expected)
	assert.NoError(t, err)

	assert.Equal(t, expectedStr, actualStr)
}
