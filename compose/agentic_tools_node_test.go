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
				Result: "content1",
			},
		},
		{
			Type: schema.ContentBlockTypeFunctionToolResult,
			FunctionToolResult: &schema.FunctionToolResult{
				CallID: "2",
				Name:   "name2",
				Result: "content2",
			},
		},
		{
			Type: schema.ContentBlockTypeFunctionToolResult,
			FunctionToolResult: &schema.FunctionToolResult{
				CallID: "3",
				Name:   "name3",
				Result: "content3",
			},
		},
	}, ret[0].ContentBlocks)
}

func TestStreamToolMessageToAgenticMessage(t *testing.T) {
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
						Result: "content1-1",
					},
				},
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: "2",
						Name:   "name2",
						Result: "content2-1content2-2",
					},
				},
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: "3",
						Name:   "name3",
						Result: "content3-1content3-2",
					},
				},
			},
		},
	}

	expectedStr, err := sonic.MarshalString(expected)
	assert.NoError(t, err)

	assert.Equal(t, expectedStr, actualStr)
}
