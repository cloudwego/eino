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

// Package host implements the host pattern for multi-agent system.
package host

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

// MultiAgent is a host multi-agent system.
// A host agent is responsible for deciding which specialist to 'hand off' the task to.
// One or more specialist agents are responsible for completing the task.
type MultiAgent struct {
	runnable         compose.Runnable[[]*schema.Message, *schema.Message]
	graph            *compose.Graph[[]*schema.Message, *schema.Message]
	graphAddNodeOpts []compose.GraphAddNodeOpt
}

func (ma *MultiAgent) Generate(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (*schema.Message, error) {
	composeOptions := agent.GetComposeOptions(opts...)

	handler := convertCallbacks(opts...)
	if handler != nil {
		composeOptions = append(composeOptions, compose.WithCallbacks(handler).DesignateNode(ma.HostNodeKey()))
	}

	return ma.runnable.Invoke(ctx, input, composeOptions...)
}

func (ma *MultiAgent) Stream(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
	composeOptions := agent.GetComposeOptions(opts...)

	handler := convertCallbacks(opts...)
	if handler != nil {
		composeOptions = append(composeOptions, compose.WithCallbacks(handler).DesignateNode(ma.HostNodeKey()))
	}

	return ma.runnable.Stream(ctx, input, composeOptions...)
}

// ExportGraph exports the underlying graph from MultiAgent, along with the []compose.GraphAddNodeOpt to be used when adding this graph to another graph.
func (ma *MultiAgent) ExportGraph() (compose.AnyGraph, []compose.GraphAddNodeOpt) {
	return ma.graph, ma.graphAddNodeOpts
}

func (ma *MultiAgent) HostNodeKey() string {
	return defaultHostNodeKey
}

// MultiAgentConfig is the config for host multi-agent system.
type MultiAgentConfig struct {
	Host        Host
	Specialists []*Specialist

	Name         string // the name of the host multi-agent
	HostNodeName string // the name of the host node in the graph, default is "host"
	// StreamToolCallChecker is a function to determine whether the model's streaming output contains tool calls.
	// Different models have different ways of outputting tool calls in streaming mode:
	// - Some models (like OpenAI) output tool calls directly
	// - Others (like Claude) output text first, then tool calls
	// This handler allows custom logic to check for tool calls in the stream.
	// It should return:
	// - true if the output contains tool calls and agent should continue processing
	// - false if no tool calls and agent should stop
	// Note: This field only needs to be configured when using streaming mode
	// Note: The handler MUST close the modelOutput stream before returning
	// Optional. By default, it checks if the first chunk contains tool calls.
	// Note: The default implementation does not work well with Claude, which typically outputs tool calls after text content.
	// Note: If your ChatModel doesn't output tool calls first, you can try adding prompts to constrain the model from generating extra text during the tool call.
	StreamToolCallChecker func(ctx context.Context, modelOutput *schema.StreamReader[*schema.Message]) (bool, error)

	// Summarizer is the summarizer agent that will summarize the outputs of all the chosen specialist agents.
	// Only when the Host agent picks multiple Specialist will this be called.
	// If you do not provide a summarizer, a default summarizer that simply concatenates all the output messages into one message will be used.
	// Note: the default summarizer do not support streaming.
	Summarizer *Summarizer
}

func (conf *MultiAgentConfig) validate() error {
	if conf == nil {
		return errors.New("host multi agent config is nil")
	}

	if conf.Host.ChatModel == nil && conf.Host.ToolCallingModel == nil {
		return errors.New("host multi agent host ChatModel is nil")
	}

	if len(conf.Specialists) == 0 {
		return errors.New("host multi agent specialists are empty")
	}

	for _, s := range conf.Specialists {
		if s.ChatModel == nil && s.Invokable == nil && s.Streamable == nil {
			return fmt.Errorf("specialist %s has no chat model or Invokable or Streamable", s.Name)
		}

		if err := s.AgentMeta.validate(); err != nil {
			return err
		}
	}

	return nil
}

// AgentMeta is the meta information of an agent within a multi-agent system.
type AgentMeta struct {
	Name        string // the name of the agent, should be unique within multi-agent system
	IntendedUse string // the intended use-case of the agent, used as the reason for the multi-agent system to hand over control to this agent
}

func (am AgentMeta) validate() error {
	if len(am.Name) == 0 {
		return errors.New("agent meta name is empty")
	}

	if len(am.IntendedUse) == 0 {
		return errors.New("agent meta intended use is empty")
	}

	return nil
}

// Host is the host agent within a multi-agent system.
// Currently, it can only be a model.ChatModel.
type Host struct {
	ToolCallingModel model.ToolCallingChatModel
	// Deprecated: ChatModel is deprecated, please use ToolCallingModel instead.
	// This field will be removed in a future release.
	ChatModel    model.ChatModel
	SystemPrompt string
}

// Specialist is a specialist agent within a host multi-agent system.
// It can be a model.ChatModel or any Invokable and/or Streamable, such as react.Agent.
// ChatModel and (Invokable / Streamable) are mutually exclusive, only one should be provided.
// notice: SystemPrompt only effects when ChatModel has been set.
// If Invokable is provided but not Streamable, then the Specialist will be 'compose.InvokableLambda'.
// If Streamable is provided but not Invokable, then the Specialist will be 'compose.StreamableLambda'.
// if Both Invokable and Streamable is provided, then the Specialist will be 'compose.AnyLambda'.
type Specialist struct {
	AgentMeta

	ChatModel    model.BaseChatModel
	SystemPrompt string

	Invokable  compose.Invoke[[]*schema.Message, *schema.Message, agent.AgentOption]
	Streamable compose.Stream[[]*schema.Message, *schema.Message, agent.AgentOption]
}

type Summarizer struct {
	ChatModel    model.BaseChatModel
	SystemPrompt string
}

func firstChunkStreamToolCallChecker(_ context.Context, sr *schema.StreamReader[*schema.Message]) (bool, error) {
	defer sr.Close()

	for {
		msg, err := sr.Recv()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}

		if len(msg.ToolCalls) > 0 {
			return true, nil
		}

		if len(msg.Content) == 0 { // skip empty chunks at the front
			continue
		}

		return false, nil
	}
}
