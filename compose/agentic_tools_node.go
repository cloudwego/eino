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
		if msg, ok := toolSearchResultMessageToAgenticMessage(m, nil); ok {
			results[i] = msg
			continue
		}

		ftr := &schema.FunctionToolResult{
			CallID: m.ToolCallID,
			Name:   m.ToolName,
		}
		if len(m.UserInputMultiContent) > 0 {
			ftr.Content = messageInputPartsToFunctionToolBlocks(m.UserInputMultiContent)
		} else if m.Content != "" {
			ftr.Content = []*schema.FunctionToolResultContentBlock{
				newFuncToolResultContentBlock(&schema.UserInputText{Text: m.Content}),
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
			if msg, ok := toolSearchResultMessageToAgenticMessage(m, &schema.StreamingMeta{Index: i}); ok {
				results[i] = msg
				continue
			}

			ftr := &schema.FunctionToolResult{
				CallID: m.ToolCallID,
				Name:   m.ToolName,
			}
			if len(m.UserInputMultiContent) > 0 {
				ftr.Content = messageInputPartsToFunctionToolBlocks(m.UserInputMultiContent)
			} else if m.Content != "" {
				ftr.Content = []*schema.FunctionToolResultContentBlock{
					newFuncToolResultContentBlock(&schema.UserInputText{Text: m.Content}),
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

func toolSearchResultMessageToAgenticMessage(m *schema.Message, meta *schema.StreamingMeta) (*schema.AgenticMessage, bool) {
	if m == nil || len(m.UserInputMultiContent) != 1 {
		return nil, false
	}

	part := m.UserInputMultiContent[0]
	if part.Type != schema.ChatMessagePartTypeToolSearchResult || part.ToolSearchResult == nil {
		return nil, false
	}

	block := schema.NewContentBlock(&schema.ToolSearchFunctionToolResult{
		CallID: m.ToolCallID,
		Name:   m.ToolName,
		Result: part.ToolSearchResult,
	})
	block.StreamingMeta = meta
	block.Extra = m.Extra

	return &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{block},
		Extra:         m.Extra,
	}, true
}

func messageInputPartsToFunctionToolBlocks(parts []schema.MessageInputPart) []*schema.FunctionToolResultContentBlock {
	blocks := make([]*schema.FunctionToolResultContentBlock, 0, len(parts))
	for _, p := range parts {
		var block *schema.FunctionToolResultContentBlock
		switch p.Type {
		case schema.ChatMessagePartTypeText:
			block = newFuncToolResultContentBlock(&schema.UserInputText{Text: p.Text})
			block.Extra = p.Extra
		case schema.ChatMessagePartTypeImageURL:
			if p.Image != nil {
				block = newFuncToolResultContentBlock(&schema.UserInputImage{
					URL:        derefString(p.Image.URL),
					Base64Data: derefString(p.Image.Base64Data),
					MIMEType:   p.Image.MIMEType,
					Detail:     p.Image.Detail,
				})
				block.Extra = p.Extra
			}
		case schema.ChatMessagePartTypeAudioURL:
			if p.Audio != nil {
				block = newFuncToolResultContentBlock(&schema.UserInputAudio{
					URL:        derefString(p.Audio.URL),
					Base64Data: derefString(p.Audio.Base64Data),
					MIMEType:   p.Audio.MIMEType,
				})
				block.Extra = p.Extra
			}
		case schema.ChatMessagePartTypeVideoURL:
			if p.Video != nil {
				block = newFuncToolResultContentBlock(&schema.UserInputVideo{
					URL:        derefString(p.Video.URL),
					Base64Data: derefString(p.Video.Base64Data),
					MIMEType:   p.Video.MIMEType,
				})
				block.Extra = p.Extra
			}
		case schema.ChatMessagePartTypeFileURL:
			if p.File != nil {
				block = newFuncToolResultContentBlock(&schema.UserInputFile{
					URL:        derefString(p.File.URL),
					Base64Data: derefString(p.File.Base64Data),
					Name:       p.File.Name,
					MIMEType:   p.File.MIMEType,
				})
				block.Extra = p.Extra
			}
		}
		if block != nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

type userInputVariant interface {
	schema.UserInputText | schema.UserInputImage | schema.UserInputAudio | schema.UserInputVideo | schema.UserInputFile
}

// newFuncToolResultContentBlock creates a FunctionToolResultContentBlock from a typed content pointer.
func newFuncToolResultContentBlock[T userInputVariant](content *T) *schema.FunctionToolResultContentBlock {
	switch c := any(content).(type) {
	case *schema.UserInputText:
		return &schema.FunctionToolResultContentBlock{Type: schema.FunctionToolResultContentBlockTypeText, Text: c}
	case *schema.UserInputImage:
		return &schema.FunctionToolResultContentBlock{Type: schema.FunctionToolResultContentBlockTypeImage, Image: c}
	case *schema.UserInputAudio:
		return &schema.FunctionToolResultContentBlock{Type: schema.FunctionToolResultContentBlockTypeAudio, Audio: c}
	case *schema.UserInputVideo:
		return &schema.FunctionToolResultContentBlock{Type: schema.FunctionToolResultContentBlockTypeVideo, Video: c}
	case *schema.UserInputFile:
		return &schema.FunctionToolResultContentBlock{Type: schema.FunctionToolResultContentBlockTypeFile, File: c}
	default:
		return nil
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *AgenticToolsNode) GetType() string { return "" }
