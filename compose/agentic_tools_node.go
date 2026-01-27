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
	"context"

	"github.com/cloudwego/eino/schema"
)

// NewAgenticToolsNode creates a new AgenticToolsNode.
// e.g.
//
//	conf := &ToolsNodeConfig{
//		Tools: []tool.BaseTool{invokableTool1, streamableTool2},
//	}
//	toolsNode, err := NewAgenticToolsNode(ctx, conf)
func NewAgenticToolsNode(ctx context.Context, conf *ToolsNodeConfig) (*AgenticToolsNode, error) {
	tn, err := NewToolNode(ctx, conf)
	if err != nil {
		return nil, err
	}
	return &AgenticToolsNode{inner: tn}, nil
}

type AgenticToolsNode struct {
	inner *ToolsNode
}

func (a *AgenticToolsNode) Invoke(ctx context.Context, input *schema.AgenticMessage, opts ...ToolsNodeOption) ([]*schema.AgenticMessage, error) {
	result, err := a.inner.Invoke(ctx, agenticMessageToToolCallMessage(input), opts...)
	if err != nil {
		return nil, err
	}
	return toolMessageToAgenticMessage(result), nil
}

func (a *AgenticToolsNode) Stream(ctx context.Context, input *schema.AgenticMessage,
	opts ...ToolsNodeOption) (*schema.StreamReader[[]*schema.AgenticMessage], error) {
	result, err := a.inner.Stream(ctx, agenticMessageToToolCallMessage(input), opts...)
	if err != nil {
		return nil, err
	}
	return streamToolMessageToAgenticMessage(result), nil
}

func agenticMessageToToolCallMessage(input *schema.AgenticMessage) *schema.Message {
	var tc []schema.ToolCall
	for _, block := range input.ContentBlocks {
		if block.Type != schema.ContentBlockTypeFunctionToolCall || block.FunctionToolCall == nil {
			continue
		}
		tc = append(tc, schema.ToolCall{
			ID: block.FunctionToolCall.CallID,
			Function: schema.FunctionCall{
				Name:      block.FunctionToolCall.Name,
				Arguments: block.FunctionToolCall.Arguments,
			},
			Extra: block.Extra,
		})
	}
	return &schema.Message{
		Role:      schema.Assistant,
		ToolCalls: tc,
	}
}

func toolMessageToAgenticMessage(input []*schema.Message) []*schema.AgenticMessage {
	var results []*schema.ContentBlock
	for _, m := range input {
		results = append(results, &schema.ContentBlock{
			Type: schema.ContentBlockTypeFunctionToolResult,
			FunctionToolResult: &schema.FunctionToolResult{
				CallID: m.ToolCallID,
				Name:   m.ToolName,
				Result: m.Content,
			},
			Extra: m.Extra,
		})
	}
	return []*schema.AgenticMessage{{
		Role:          schema.AgenticRoleTypeUser,
		ContentBlocks: results,
	}}
}

func streamToolMessageToAgenticMessage(input *schema.StreamReader[[]*schema.Message]) *schema.StreamReader[[]*schema.AgenticMessage] {
	return schema.StreamReaderWithConvert(input, func(t []*schema.Message) ([]*schema.AgenticMessage, error) {
		var results []*schema.ContentBlock
		for i, m := range t {
			if m == nil {
				continue
			}
			results = append(results, &schema.ContentBlock{
				Type: schema.ContentBlockTypeFunctionToolResult,
				FunctionToolResult: &schema.FunctionToolResult{
					CallID: m.ToolCallID,
					Name:   m.ToolName,
					Result: m.Content,
				},
				StreamingMeta: &schema.StreamingMeta{Index: i},
				Extra:         m.Extra,
			})
		}
		return []*schema.AgenticMessage{{
			Role:          schema.AgenticRoleTypeUser,
			ContentBlocks: results,
		}}, nil
	})
}

func (a *AgenticToolsNode) GetType() string { return "" }
