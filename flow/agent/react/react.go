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

package react

import (
	"context"
	"fmt"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

type nodeState struct {
	Messages []*schema.Message
}

const (
	nodeKeyTools     = "tools"
	nodeKeyChatModel = "chat"
)

// MessageModifier modify the input messages before the model is called.
type MessageModifier func(ctx context.Context, input []*schema.Message) []*schema.Message

// AgentConfig is the config for ReAct agent.
type AgentConfig struct {
	// Model is the chat model to be used for handling user messages.
	Model model.ChatModel
	// ToolsConfig is the config for tools node.
	ToolsConfig compose.ToolsNodeConfig

	// MessageModifier.
	// modify the input messages before the model is called, it's useful when you want to add some system prompt or other messages.
	MessageModifier MessageModifier

	// MaxStep.
	// default 12 of steps in pregel (node num + 10).
	MaxStep int `json:"max_step"`

	// Tools that will make agent return directly when the tool is called.
	ToolReturnDirectly map[string]struct{}
}

// NewPersonaModifier add the system prompt as persona before the model is called.
// example:
//
//	persona := "You are an expert in golang."
//	config := AgentConfig{
//		Model: model,
//		MessageModifier: NewPersonaModifier(persona),
//	}
//	agent, err := NewAgent(ctx, config)
//	if err != nil {return}
//	msg, err := agent.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "how to build agent with eino"}})
//	if err != nil {return}
//	println(msg.Content)
func NewPersonaModifier(persona string) MessageModifier {
	return func(ctx context.Context, input []*schema.Message) []*schema.Message {
		res := make([]*schema.Message, 0, len(input)+1)

		res = append(res, schema.SystemMessage(persona))
		res = append(res, input...)
		return res
	}
}

// NewAgent creates a ReAct agent that feeds tool response into next round of Chat Model generation.
func NewAgent(ctx context.Context, config *AgentConfig) (*Agent, error) {
	if config.MessageModifier == nil {
		config.MessageModifier = func(ctx context.Context, input []*schema.Message) []*schema.Message {
			return input
		}
	}

	a := &Agent{}

	runnable, err := a.build(ctx, config)
	if err != nil {
		return nil, err
	}

	a.runnable = runnable

	return a, nil
}

// Agent is the ReAct agent.
// ReAct agent is a simple agent that handles user messages with a chat model and tools.
// ReAct will call the chat model, if the message contains tool calls, it will call the tools.
// if the tool is configured to return directly, ReAct will return directly.
// otherwise, ReAct will continue to call the chat model until the message contains no tool calls.
// e.g.
//
//	agent, err := ReAct.NewAgent(ctx, &react.AgentConfig{})
//	if err != nil {...}
//	msg, err := agent.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "how to build agent with eino"}})
//	if err != nil {...}
//	println(msg.Content)
type Agent struct {
	runnable compose.Runnable[[]*schema.Message, *schema.Message]
}

func (r *Agent) build(ctx context.Context, config *AgentConfig) (compose.Runnable[[]*schema.Message, *schema.Message], error) {
	toolInfos := make([]*schema.ToolInfo, 0, len(config.ToolsConfig.Tools))
	for _, t := range config.ToolsConfig.Tools {
		tl, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}

		toolInfos = append(toolInfos, tl)
	}

	err := config.Model.BindTools(toolInfos)
	if err != nil {
		return nil, err
	}

	// graph
	graph := compose.NewGraph[[]*schema.Message, *schema.Message](
		compose.WithGenLocalState(
			func(ctx context.Context) *nodeState {
				return &nodeState{
					Messages: make([]*schema.Message, 0, 3),
				}
			}))

	err = graph.AddChatModelNode(nodeKeyChatModel, config.Model,
		compose.WithStatePreHandler(func(ctx context.Context, input []*schema.Message, state *nodeState) ([]*schema.Message, error) {
			state.Messages = append(state.Messages, input...)

			modifiedInput := make([]*schema.Message, 0, len(input))
			modifiedInput = append(modifiedInput, state.Messages...)
			modifiedInput = config.MessageModifier(ctx, modifiedInput)

			return modifiedInput, nil
		}),
	)
	if err != nil {
		return nil, err
	}

	toolsNode, err := compose.NewToolNode(ctx, &config.ToolsConfig)
	if err != nil {
		return nil, err
	}

	err = graph.AddToolsNode(nodeKeyTools, toolsNode, compose.WithStatePreHandler(func(ctx context.Context, input *schema.Message, state *nodeState) (*schema.Message, error) {
		state.Messages = append(state.Messages, input)

		if err := checkReturnDirectlyBeforeToolsNode(input, config); err != nil {
			return nil, err
		}

		return input, nil
	}))
	if err != nil {
		return nil, err
	}

	if err = graph.AddEdge(compose.START, nodeKeyChatModel); err != nil {
		return nil, err
	}

	err = graph.AddBranch(nodeKeyChatModel, compose.NewStreamGraphBranch(func(ctx context.Context, sr *schema.StreamReader[*schema.Message]) (endNode string, err error) {
		defer sr.Close()

		msg, err := sr.Recv()
		if err != nil {
			return "", err
		}

		if len(msg.ToolCalls) == 0 {
			return compose.END, nil
		}

		return nodeKeyTools, nil
	}, map[string]bool{nodeKeyTools: true, compose.END: true}))
	if err != nil {
		return nil, err
	}

	if len(config.ToolReturnDirectly) > 0 {
		if err = r.buildReturnDirectly(graph, config); err != nil {
			return nil, err
		}
	} else {
		if err = graph.AddEdge(nodeKeyTools, nodeKeyChatModel); err != nil {
			return nil, err
		}
	}

	var opts []compose.GraphCompileOption
	if config.MaxStep > 0 {
		opts = append(opts, compose.WithMaxRunSteps(config.MaxStep))
	}

	return graph.Compile(ctx, opts...)
}

func (r *Agent) buildReturnDirectly(graph *compose.Graph[[]*schema.Message, *schema.Message], config *AgentConfig) (err error) {
	takeFirst := func(ctx context.Context, msgs *schema.StreamReader[[]*schema.Message]) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderWithConvert(msgs, func(msgs []*schema.Message) (*schema.Message, error) {
			if len(msgs) != 1 {
				return nil, fmt.Errorf("return directly tools node output expected to have only one msg, but got %d", len(msgs))
			}
			return msgs[0], nil
		}), nil
	}

	nodeKeyTakeFirst := "convertor" // convert output of tools node ([]*schema.Message) to a single *schema.Message, so that it could be returned directly
	if err = graph.AddLambdaNode(nodeKeyTakeFirst, compose.TransformableLambda(takeFirst)); err != nil {
		return err
	}

	// this branch checks if the tool called should return directly. It either leads to END or back to ChatModel
	err = graph.AddBranch(nodeKeyTools, compose.NewStreamGraphBranch(func(ctx context.Context, msgsStream *schema.StreamReader[[]*schema.Message]) (endNode string, err error) {
		state, err := compose.GetState[*nodeState](ctx) // last msg stored in state should contain the tool call information
		if err != nil {
			return "", fmt.Errorf("get nodeState in branch failed: %w", err)
		}

		defer msgsStream.Close()

		for {
			msgs, err := msgsStream.Recv()
			if err != nil {
				if err == io.EOF {
					return nodeKeyChatModel, nil
				}
				return "", fmt.Errorf("receive first packet from tools node result returns err: %w", err)
			}

			if len(msgs) == 0 {
				continue
			}

			toolCallID := msgs[0].ToolCallID
			if len(toolCallID) == 0 {
				continue
			}

			for _, toolCall := range state.Messages[len(state.Messages)-1].ToolCalls {
				if toolCall.ID == toolCallID {
					if _, ok := config.ToolReturnDirectly[toolCall.Function.Name]; ok {
						return nodeKeyTakeFirst, nil
					}
				}
			}

			return nodeKeyChatModel, nil
		}
	}, map[string]bool{nodeKeyChatModel: true, nodeKeyTakeFirst: true}))
	if err != nil {
		return err
	}

	return graph.AddEdge(nodeKeyTakeFirst, compose.END)
}

func checkReturnDirectlyBeforeToolsNode(input *schema.Message, config *AgentConfig) error {
	if len(config.ToolReturnDirectly) == 0 {
		return nil
	}

	if len(input.ToolCalls) > 1 { // check if a return directly tool call belongs to a batch of parallel tool calls, which is not supported for now
		var returnDirectly bool
		toolCalls := input.ToolCalls
		toolNames := make([]string, 0, len(toolCalls))
		for i := range toolCalls {
			toolNames = append(toolNames, toolCalls[i].Function.Name)

			if _, ok := config.ToolReturnDirectly[toolCalls[i].Function.Name]; ok {
				returnDirectly = true
			}
		}

		if returnDirectly {
			return fmt.Errorf("return directly tool call is not allowed when there are parallel tool calls: %v", toolNames)
		}
	}

	return nil
}

// Generate generates a response from the agent.
func (r *Agent) Generate(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (output *schema.Message, err error) {
	output, err = r.runnable.Invoke(ctx, input, agent.GetComposeOptions(opts...)...)
	if err != nil {
		return nil, err
	}

	return output, nil
}

// Stream calls the agent and returns a stream response.
func (r *Agent) Stream(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (
	output *schema.StreamReader[*schema.Message], err error) {
	res, err := r.runnable.Stream(ctx, input, agent.GetComposeOptions(opts...)...)
	if err != nil {
		return nil, err
	}

	return res, nil
}
