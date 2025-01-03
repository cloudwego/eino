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

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose/internal"
	"github.com/cloudwego/eino/schema"
)

// ToolsNode a node that can run tools in a graph. the interface in Graph Node as below:
//
//	Invoke(ctx context.Context, input *schema.Message, opts ...ToolsNodeOption) ([]*schema.Message, error)
//	Stream(ctx context.Context, input *schema.Message, opts ...ToolsNodeOption) (*schema.StreamReader[[]*schema.Message], error)
type ToolsNode struct {
	*internal.ToolsNode
}

// ToolsNodeConfig is the config for ToolsNode. It requires a list of tools.
// Tools are BaseTool but must implement InvokableTool or StreamableTool.
type ToolsNodeConfig struct {
	Tools []tool.BaseTool
}

// ToolsNodeOption is the option func type for ToolsNode.
type ToolsNodeOption = internal.ToolsNodeOption

// NewToolNode creates a new ToolsNode.
// e.g.
//
//	conf := &ToolsNodeConfig{
//		Tools: []tool.BaseTool{invokableTool1, streamableTool2},
//	}
//	toolsNode, err := NewToolNode(ctx, conf)
func NewToolNode(ctx context.Context, config *ToolsNodeConfig) (*ToolsNode, error) {
	tn, err := internal.NewToolNode(ctx, config.Tools)
	if err != nil {
		return nil, err
	}

	return &ToolsNode{tn}, nil
}

// WithToolOption adds tool options to the ToolsNode.
func WithToolOption(opts ...tool.Option) ToolsNodeOption {
	return func(o *internal.ToolsNodeOptions) {
		o.ToolOptions = append(o.ToolOptions, opts...)
	}
}

// Invoke calls the tools and collects the results of invokable tools.
// it's parallel if there are multiple tool calls in the input message.
func (tn *ToolsNode) Invoke(ctx context.Context, input *schema.Message,
	opts ...ToolsNodeOption) ([]*schema.Message, error) {
	return tn.ToolsNode.Invoke(ctx, input, opts...)
}

// Stream calls the tools and collects the results of stream readers.
// it's parallel if there are multiple tool calls in the input message.
func (tn *ToolsNode) Stream(ctx context.Context, input *schema.Message,
	opts ...ToolsNodeOption) (*schema.StreamReader[[]*schema.Message], error) {
	return tn.ToolsNode.Stream(ctx, input, opts...)
}
