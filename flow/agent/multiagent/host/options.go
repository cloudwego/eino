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

package host

import (
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	agent2 "github.com/cloudwego/eino/internal/flow/agent"
)

type options struct {
	agentCallbacks []MultiAgentCallback
	composeOptions []compose.Option
}

func WithAgentCallbacks(agentCallbacks ...MultiAgentCallback) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(opts *options) {
		opts.agentCallbacks = append(opts.agentCallbacks, agentCallbacks...)
	})
}

// WithAgentModelOptions returns an agent option that specifies model.Option for the given agent.
// The given agentName should be the name of the agent in the graph.
// e.g.
//
//	if specifying model.Option for the host agent, use MultiAgent.HostNodeKey() as the agentName.
//	if specifying model.Option for the specialist agent, use the specialist agent's AgentMeta.Name as the agentName.
func WithAgentModelOptions(agentName string, opts ...model.Option) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithChatModelOption(opts...).DesignateNode(agentName))
	})
}

// WithAgentModelCallbacks returns an agent option that specifies callbacks.Handler for the given agent's ChatModel.
// The given agentName should be the name of the agent in the graph.
// e.g.
//
//	if specifying model.Option for the host agent, use MultiAgent.HostNodeKey() as the agentName.
//	if specifying model.Option for the specialist agent, use the specialist agent's AgentMeta.Name as the agentName.
func WithAgentModelCallbacks(agentName string, cbs ...callbacks.Handler) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithCallbacks(cbs...).DesignateNode(agentName))
	})
}

// WithSpecialistLambdaOptions returns an agent option that specifies agent.AgentOption for the given specialist's Lambda.
// The given specialistName should be the name of the specialist in the graph.
func WithSpecialistLambdaOptions(specialistName string, opts ...agent.AgentOption) agent.AgentOption {
	anyOpts := make([]any, len(opts))
	for i, opt := range opts {
		anyOpts[i] = opt
	}
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithLambdaOption(anyOpts...).DesignateNode(specialistName))
	})
}

// WithSpecialistLambdaCallbacks returns an agent option that specifies callbacks.Handler for the given specialist's Lambda.
// The given specialistName should be the name of the specialist in the graph.
func WithSpecialistLambdaCallbacks(specialistName string, cbs ...callbacks.Handler) agent.AgentOption {
	return agent.WrapImplSpecificOptFn(func(o *options) {
		o.composeOptions = append(o.composeOptions, compose.WithCallbacks(cbs...).DesignateNode(specialistName))
	})
}

// ConvertOptions converts agent options to compose options, and designate them to the Agent sub graph specified by nodePath.
// Useful when adding MultiAgent's Graph to another Graph.
// The parameter nodePath is the path to the MultiAgent sub graph within the whole Graph.
// If nodePath == nil, then MultiAgent's Graph is treated as a stand-alone, top-level Graph.
func ConvertOptions(nodePath *compose.NodePath, opts ...agent.AgentOption) []compose.Option {
	composeOpts := agent.GetImplSpecificOptions(&options{}, opts...).composeOptions
	if nodePath != nil {
		for i := range composeOpts {
			composeOpts[i] = agent2.DesignateNodePrependPath(composeOpts[i], nodePath)
		}
	}

	convertedCallbackHandler := convertCallbacks(opts...)
	if convertedCallbackHandler != nil {
		callbackOpt := compose.WithCallbacks(convertedCallbackHandler).DesignateNode(defaultHostNodeKey)
		if nodePath != nil {
			return append(composeOpts, agent2.DesignateNodePrependPath(callbackOpt, nodePath))
		}
		return append(composeOpts, callbackOpt)
	}

	return composeOpts
}
