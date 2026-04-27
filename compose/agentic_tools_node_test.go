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
		assert.Equal(t, 1, len(ret))
		assert.Equal(t, schema.AgenticRoleTypeUser, ret[0].Role)
		assert.Equal(t, []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeFunctionToolResult,
				FunctionToolResult: &schema.FunctionToolResult{
					CallID: "1",
					Name:   "name1",
					Blocks: []*schema.FunctionToolResultBlock{
						{Text: &schema.UserInputText{Text: "content1"}},
					},
				},
			},
			{
				Type: schema.ContentBlockTypeFunctionToolResult,
				FunctionToolResult: &schema.FunctionToolResult{
					CallID: "2",
					Name:   "name2",
					Blocks: []*schema.FunctionToolResultBlock{
						{Text: &schema.UserInputText{Text: "content2"}},
					},
				},
			},
			{
				Type: schema.ContentBlockTypeFunctionToolResult,
				FunctionToolResult: &schema.FunctionToolResult{
					CallID: "3",
					Name:   "name3",
					Blocks: []*schema.FunctionToolResultBlock{
						{Text: &schema.UserInputText{Text: "content3"}},
					},
				},
			},
		}, ret[0].ContentBlocks)
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
		assert.Equal(t, 1, len(ret))
		assert.Equal(t, schema.AgenticRoleTypeUser, ret[0].Role)

		// first tool result has Blocks (from UserInputMultiContent)
		ftr1 := ret[0].ContentBlocks[0].FunctionToolResult
		assert.Equal(t, "1", ftr1.CallID)
		assert.Equal(t, 5, len(ftr1.Blocks))

		assert.Equal(t, "hello", ftr1.Blocks[0].Text.Text)

		assert.Equal(t, imageURL, ftr1.Blocks[1].Image.URL)
		assert.Equal(t, schema.ImageURLDetailHigh, ftr1.Blocks[1].Image.Detail)

		assert.Equal(t, audioBase64, ftr1.Blocks[2].Audio.Base64Data)

		assert.Equal(t, videoURL, ftr1.Blocks[3].Video.URL)

		assert.Equal(t, fileURL, ftr1.Blocks[4].File.URL)

		// second tool result has text-only Blocks (from Content)
		ftr2 := ret[0].ContentBlocks[1].FunctionToolResult
		assert.Equal(t, "2", ftr2.CallID)
		assert.Equal(t, 1, len(ftr2.Blocks))
		assert.Equal(t, "plain result", ftr2.Blocks[0].Text.Text)
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
		ftr := ret[0].ContentBlocks[0].FunctionToolResult
		assert.Equal(t, 1, len(ftr.Blocks))
		assert.Equal(t, "only text", ftr.Blocks[0].Text.Text)
	})
}

func TestMessageInputPartsToFunctionToolBlocks(t *testing.T) {
	url1 := "https://example.com/img.jpg"
	base64 := "aW1hZ2VkYXRh"

	t.Run("deref nil pointer returns empty string", func(t *testing.T) {
		input := []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: nil, Base64Data: nil},
			}},
		}
		blocks := messageInputPartsToFunctionToolBlocks(input)
		assert.Equal(t, 1, len(blocks))
		assert.Equal(t, "", blocks[0].Image.URL)
		assert.Equal(t, "", blocks[0].Image.Base64Data)
	})

	t.Run("deref non-nil pointer returns value", func(t *testing.T) {
		input := []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: &url1, Base64Data: &base64, MIMEType: "image/jpeg"},
			}},
		}
		blocks := messageInputPartsToFunctionToolBlocks(input)
		assert.Equal(t, 1, len(blocks))
		assert.Equal(t, url1, blocks[0].Image.URL)
		assert.Equal(t, base64, blocks[0].Image.Base64Data)
		assert.Equal(t, "image/jpeg", blocks[0].Image.MIMEType)
	})

	t.Run("empty parts returns empty blocks", func(t *testing.T) {
		blocks := messageInputPartsToFunctionToolBlocks(nil)
		assert.Equal(t, 0, len(blocks))
	})

	t.Run("extra merge - part and media type", func(t *testing.T) {
		imgURL := "https://example.com/img.png"
		input := []schema.MessageInputPart{
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						URL:   &imgURL,
						Extra: map[string]any{"shared": "from_common", "common_only": "c"},
					},
				},
				Extra: map[string]any{"shared": "from_part", "part_only": "p"},
			},
		}
		blocks := messageInputPartsToFunctionToolBlocks(input)
		assert.Equal(t, 1, len(blocks))
		assert.Equal(t, "from_part", blocks[0].Extra["shared"])
		assert.Equal(t, "p", blocks[0].Extra["part_only"])
		assert.Nil(t, blocks[0].Extra["common_only"])
	})

	t.Run("extra - text only has part extra", func(t *testing.T) {
		input := []schema.MessageInputPart{
			{
				Type:  schema.ChatMessagePartTypeText,
				Text:  "hello",
				Extra: map[string]any{"key": "val"},
			},
		}
		blocks := messageInputPartsToFunctionToolBlocks(input)
		assert.Equal(t, map[string]any{"key": "val"}, blocks[0].Extra)
	})

	t.Run("extra - both nil returns nil", func(t *testing.T) {
		imgURL := "https://example.com/img.png"
		input := []schema.MessageInputPart{
			{
				Type:  schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &imgURL}},
			},
		}
		blocks := messageInputPartsToFunctionToolBlocks(input)
		assert.Nil(t, blocks[0].Extra)
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

		assert.Equal(t, 1, len(result))
		assert.Equal(t, 2, len(result[0].ContentBlocks))

		ftr1 := result[0].ContentBlocks[0].FunctionToolResult
		assert.Equal(t, "1", ftr1.CallID)
		assert.Equal(t, 2, len(ftr1.Blocks))
		assert.NotNil(t, ftr1.Blocks[0].Text)
		assert.NotNil(t, ftr1.Blocks[1].Image)
		assert.Equal(t, imageURL, ftr1.Blocks[1].Image.URL)

		ftr2 := result[0].ContentBlocks[1].FunctionToolResult
		assert.Equal(t, "2", ftr2.CallID)
		assert.Equal(t, 1, len(ftr2.Blocks))
		assert.Equal(t, "result2", ftr2.Blocks[0].Text.Text)
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
						Blocks: []*schema.FunctionToolResultBlock{
							{Text: &schema.UserInputText{Text: "content1-1"}},
						},
					},
				},
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: "2",
						Name:   "name2",
						Blocks: []*schema.FunctionToolResultBlock{
							{Text: &schema.UserInputText{Text: "content2-1"}},
							{Text: &schema.UserInputText{Text: "content2-2"}},
						},
					},
				},
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: "3",
						Name:   "name3",
						Blocks: []*schema.FunctionToolResultBlock{
							{Text: &schema.UserInputText{Text: "content3-1"}},
							{Text: &schema.UserInputText{Text: "content3-2"}},
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
