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
	results := make([]*schema.AgenticMessage, len(input))
	for i, m := range input {
		ftr := &schema.FunctionToolResult{
			CallID: m.ToolCallID,
			Name:   m.ToolName,
		}
		if len(m.UserInputMultiContent) > 0 {
			ftr.Blocks = messageInputPartsToFunctionToolBlocks(m.UserInputMultiContent)
		} else if m.Content != "" {
			ftr.Blocks = []*schema.FunctionToolResultBlock{
				{Text: &schema.UserInputText{Text: m.Content}},
			}
		}
		results[i] = &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{{
				Type:               schema.ContentBlockTypeFunctionToolResult,
				FunctionToolResult: ftr,
				Extra:              m.Extra,
			}},
			Extra: m.Extra,
		}
	}
	return results
}

func streamToolMessageToAgenticMessage(input *schema.StreamReader[[]*schema.Message]) *schema.StreamReader[[]*schema.AgenticMessage] {
	return schema.StreamReaderWithConvert(input, func(t []*schema.Message) ([]*schema.AgenticMessage, error) {
		results := make([]*schema.AgenticMessage, len(t))
		for i, m := range t {
			if m == nil {
				continue
			}
			ftr := &schema.FunctionToolResult{
				CallID: m.ToolCallID,
				Name:   m.ToolName,
			}
			if len(m.UserInputMultiContent) > 0 {
				ftr.Blocks = messageInputPartsToFunctionToolBlocks(m.UserInputMultiContent)
			} else if m.Content != "" {
				ftr.Blocks = []*schema.FunctionToolResultBlock{
					{Text: &schema.UserInputText{Text: m.Content}},
				}
			}
			results[i] = &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{{
					Type:               schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: ftr,
					StreamingMeta:      &schema.StreamingMeta{Index: i},
					Extra:              m.Extra,
				}},
				Extra: m.Extra,
			}
		}
		return results, nil
	})
}

func messageInputPartsToFunctionToolBlocks(parts []schema.MessageInputPart) []*schema.FunctionToolResultBlock {
	blocks := make([]*schema.FunctionToolResultBlock, 0, len(parts))
	for _, p := range parts {
		var block *schema.FunctionToolResultBlock
		switch p.Type {
		case schema.ChatMessagePartTypeText:
			block = &schema.FunctionToolResultBlock{
				Text:  &schema.UserInputText{Text: p.Text},
				Extra: p.Extra,
			}
		case schema.ChatMessagePartTypeImageURL:
			if p.Image != nil {
				block = &schema.FunctionToolResultBlock{
					Image: &schema.UserInputImage{
						URL:        derefString(p.Image.URL),
						Base64Data: derefString(p.Image.Base64Data),
						MIMEType:   p.Image.MIMEType,
						Detail:     p.Image.Detail,
					},
					Extra: p.Extra,
				}
			}
		case schema.ChatMessagePartTypeAudioURL:
			if p.Audio != nil {
				block = &schema.FunctionToolResultBlock{
					Audio: &schema.UserInputAudio{
						URL:        derefString(p.Audio.URL),
						Base64Data: derefString(p.Audio.Base64Data),
						MIMEType:   p.Audio.MIMEType,
					},
					Extra: p.Extra,
				}
			}
		case schema.ChatMessagePartTypeVideoURL:
			if p.Video != nil {
				block = &schema.FunctionToolResultBlock{
					Video: &schema.UserInputVideo{
						URL:        derefString(p.Video.URL),
						Base64Data: derefString(p.Video.Base64Data),
						MIMEType:   p.Video.MIMEType,
					},
					Extra: p.Extra,
				}
			}
		case schema.ChatMessagePartTypeFileURL:
			if p.File != nil {
				block = &schema.FunctionToolResultBlock{
					File: &schema.UserInputFile{
						URL:        derefString(p.File.URL),
						Base64Data: derefString(p.File.Base64Data),
						Name:       p.File.Name,
						MIMEType:   p.File.MIMEType,
					},
					Extra: p.Extra,
				}
			}
		}
		if block != nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *AgenticToolsNode) GetType() string { return "" }
