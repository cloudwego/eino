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

package react

import (
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
)

type options struct {
	composeOptions []compose.Option
}

// WithChatModelOptions returns an agent option that specifies model.Option for the ChatModel node.
func WithChatModelOptions(opts ...model.Option) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithChatModelOption(opts...))
	})
}

// WithToolOptions returns an agent option that specifies tool.Option for the ToolsNode node.
func WithToolOptions(opts ...tool.Option) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithToolsNodeOption(compose.WithToolOption(opts...)))
	})
}

// WithToolList returns an agent option that specifies tool list for the ToolsNode node, overriding default tool list.
func WithToolList(tool ...tool.BaseTool) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithToolsNodeOption(compose.WithToolList(tool...)))
	})
}

// WithRuntimeMaxSteps returns an agent option that specifies max steps for the agent, overriding default configuration.
func WithRuntimeMaxSteps(maxSteps int) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithRuntimeMaxSteps(maxSteps))
	})
}

// WithModelCallbacks returns an agent option that specifies callback handlers for the ChatModel node.
func WithModelCallbacks(cbs ...callbacks.Handler) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithCallbacks(cbs...).DesignateNode(nodeKeyModel))
	})
}

// WithToolCallbacks returns an agent option that specifies callback handlers for the tools executed by ToolsNode node.
func WithToolCallbacks(cbs ...callbacks.Handler) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithCallbacks(cbs...).DesignateNode(nodeKeyTools))
	})
}

// ConvertOptions converts agent options to compose options, and designate them to the Agent sub graph specified by nodePath.
// Useful when adding Agent's Graph to another Graph.
// The parameter nodePath is the path to the Agent sub graph within the whole Graph.
// If nodePath == nil, then Agent's Graph is treated as a stand-alone, top-level Graph.
func ConvertOptions(nodePath *compose.NodePath, opts ...agent.AgentOption) []compose.Option {
	composeOpts := agent.GetImplSpecificOptions(&options{}, opts...).composeOptions
	if nodePath != nil {
		for i := range composeOpts {
			composeOpts[i] = composeOpts[i].DesignateNodePrependPath(nodePath)
		}
	}

	return composeOpts
}
