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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

type state struct {
	Messages                 []*schema.Message
	ReturnDirectlyToolCallID string
}

const (
	nodeKeyTools = "tools"
	nodeKeyModel = "chat"
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
	// When multiple tools are called and more than one tool is in the return directly list, only the first one will be returned.
	ToolReturnDirectly map[string]struct{}

	// StreamOutputHandler is a function to determine whether the model's streaming output contains tool calls.
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
	StreamToolCallChecker func(ctx context.Context, modelOutput *schema.StreamReader[*schema.Message]) (bool, error)
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

func firstChunkStreamToolCallChecker(_ context.Context, sr *schema.StreamReader[*schema.Message]) (bool, error) {
	defer sr.Close()

	msg, err := sr.Recv()
	if err != nil {
		return false, err
	}

	if len(msg.ToolCalls) == 0 {
		return false, nil
	}

	return true, nil
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

// NewAgent creates a ReAct agent that feeds tool response into next round of Chat Model generation.
//
// IMPORTANT!! For models that don't output tool calls in the first streaming chunk (e.g. Claude)
// the default StreamToolCallChecker may not work properly since it only checks the first chunk for tool calls.
// In such cases, you need to implement a custom StreamToolCallChecker that can properly detect tool calls.
func NewAgent(ctx context.Context, config *AgentConfig) (_ *Agent, err error) {
	var (
		chatModel       = config.Model
		toolsNode       *compose.ToolsNode
		toolInfos       []*schema.ToolInfo
		toolCallChecker = config.StreamToolCallChecker
		messageModifier = config.MessageModifier
	)

	if messageModifier == nil {
		messageModifier = func(ctx context.Context, input []*schema.Message) []*schema.Message {
			return input
		}
	}
	if toolCallChecker == nil {
		toolCallChecker = firstChunkStreamToolCallChecker
	}

	if toolInfos, err = genToolInfos(ctx, config.ToolsConfig); err != nil {
		return nil, err
	}

	if err = chatModel.BindTools(toolInfos); err != nil {
		return nil, err
	}

	if toolsNode, err = compose.NewToolNode(ctx, &config.ToolsConfig); err != nil {
		return nil, err
	}

	graph := compose.NewGraph[[]*schema.Message, *schema.Message](compose.WithGenLocalState(func(ctx context.Context) *state {
		return &state{Messages: make([]*schema.Message, 0, config.MaxStep+1)}
	}))

	modelPreHandle := func(ctx context.Context, input []*schema.Message, state *state) ([]*schema.Message, error) {
		state.Messages = append(state.Messages, input...)

		modifiedInput := make([]*schema.Message, 0, len(state.Messages))
		copy(modifiedInput, state.Messages)
		return messageModifier(ctx, modifiedInput), nil
	}
	if err = graph.AddChatModelNode(nodeKeyModel, chatModel, compose.WithStatePreHandler(modelPreHandle)); err != nil {
		return nil, err
	}

	if err = graph.AddEdge(compose.START, nodeKeyModel); err != nil {
		return nil, err
	}

	toolsNodePreHandle := func(ctx context.Context, input *schema.Message, state *state) (*schema.Message, error) {
		state.Messages = append(state.Messages, input)
		state.ReturnDirectlyToolCallID = getReturnDirectlyToolCallID(input, config.ToolReturnDirectly)
		return input, nil
	}
	if err = graph.AddToolsNode(nodeKeyTools, toolsNode, compose.WithStatePreHandler(toolsNodePreHandle)); err != nil {
		return nil, err
	}

	modelPostBranchCondition := func(_ context.Context, sr *schema.StreamReader[*schema.Message]) (endNode string, err error) {
		if isToolCall, err := toolCallChecker(ctx, sr); err != nil {
			return "", err
		} else if isToolCall {
			return nodeKeyTools, nil
		}
		return compose.END, nil
	}

	if err = graph.AddBranch(nodeKeyModel, compose.NewStreamGraphBranch(modelPostBranchCondition, map[string]bool{nodeKeyTools: true, compose.END: true})); err != nil {
		return nil, err
	}

	if len(config.ToolReturnDirectly) > 0 {
		if err = buildReturnDirectly(graph); err != nil {
			return nil, err
		}
	} else if err = graph.AddEdge(nodeKeyTools, nodeKeyModel); err != nil {
		return nil, err
	}

	runnable, err := graph.Compile(ctx, compose.WithMaxRunSteps(config.MaxStep))
	if err != nil {
		return nil, err
	}

	return &Agent{runnable: runnable}, nil
}

func buildReturnDirectly(graph *compose.Graph[[]*schema.Message, *schema.Message]) (err error) {
	directReturn := func(ctx context.Context, msgs *schema.StreamReader[[]*schema.Message]) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderWithConvert(msgs, func(msgs []*schema.Message) (*schema.Message, error) {
			state, err := compose.GetState[*state](ctx)
			if err != nil {
				return nil, fmt.Errorf("get state failed: %w", err)
			}
			for i := range msgs {
				msg := msgs[i]
				if msg != nil && msg.ToolCallID == state.ReturnDirectlyToolCallID {
					return msg, nil
				}
			}
			return nil, schema.ErrNoValue
		}), nil
	}

	nodeKeyDirectReturn := "direct_return"
	if err = graph.AddLambdaNode(nodeKeyDirectReturn, compose.TransformableLambda(directReturn)); err != nil {
		return err
	}

	// this branch checks if the tool called should return directly. It either leads to END or back to ChatModel
	err = graph.AddBranch(nodeKeyTools, compose.NewStreamGraphBranch(func(ctx context.Context, msgsStream *schema.StreamReader[[]*schema.Message]) (endNode string, err error) {
		msgsStream.Close()

		s, err := compose.GetState[*state](ctx) // last msg stored in state should contain the tool call information
		if err != nil {
			return "", fmt.Errorf("get state in branch failed: %w", err)
		}

		if len(s.ReturnDirectlyToolCallID) > 0 {
			return nodeKeyDirectReturn, nil
		}

		return nodeKeyModel, nil
	}, map[string]bool{nodeKeyModel: true, nodeKeyDirectReturn: true}))
	if err != nil {
		return err
	}

	return graph.AddEdge(nodeKeyDirectReturn, compose.END)
}

func genToolInfos(ctx context.Context, config compose.ToolsNodeConfig) ([]*schema.ToolInfo, error) {
	toolInfos := make([]*schema.ToolInfo, 0, len(config.Tools))
	for _, t := range config.Tools {
		tl, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}

		toolInfos = append(toolInfos, tl)
	}

	return toolInfos, nil
}

func getReturnDirectlyToolCallID(input *schema.Message, toolReturnDirectly map[string]struct{}) string {
	if len(toolReturnDirectly) == 0 {
		return ""
	}

	for _, toolCall := range input.ToolCalls {
		if _, ok := toolReturnDirectly[toolCall.Function.Name]; ok {
			return toolCall.ID
		}
	}

	return ""
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
