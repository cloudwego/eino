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
	"context"
	"fmt"
	"io"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	hostName          = "host"
	defaultHostPrompt = "decide which tool is best for the task and call only the best tool."
)

type state struct {
	msgs []*schema.Message
}

// NewMultiAgent creates a new host multi-agent system.
func NewMultiAgent(ctx context.Context, config *MultiAgentConfig) (*MultiAgent, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	g := compose.NewGraph[[]*schema.Message, *schema.Message](
		compose.WithGenLocalState(func(context.Context) *state { return &state{} }))

	agentTools := make([]*schema.ToolInfo, 0, len(config.Specialists))
	agentMap := make(map[string]bool, len(config.Specialists)+1)
	for i := range config.Specialists {
		specialist := config.Specialists[i]

		agentTools = append(agentTools, &schema.ToolInfo{
			Name: specialist.Name,
			Desc: specialist.IntendedUse,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"reason": {
					Type: schema.String,
					Desc: "the reason to call this tool",
				},
			}),
		})

		if err := addSpecialistAgent(specialist, g); err != nil {
			return nil, err
		}

		agentMap[specialist.Name] = true
	}

	if err := addHostAgent(config, agentTools, g); err != nil {
		return nil, err
	}

	const convertorName = "msg2MsgList"
	if err := g.AddLambdaNode(convertorName, compose.ToList[*schema.Message](), compose.WithNodeName("converter")); err != nil {
		return nil, err
	}

	if err := addDirectAnswerBranch(convertorName, g); err != nil {
		return nil, err
	}

	if err := addSpecialistsBranch(convertorName, agentMap, g); err != nil {
		return nil, err
	}

	r, err := g.Compile(ctx, compose.WithNodeTriggerMode(compose.AnyPredecessor), compose.WithGraphName(config.Name))
	if err != nil {
		return nil, err
	}

	return &MultiAgent{
		runnable: r,
	}, nil
}

func addSpecialistAgent(specialist *Specialist, g *compose.Graph[[]*schema.Message, *schema.Message]) error {
	if specialist.Invokable != nil || specialist.Streamable != nil {
		lambda, err := compose.AnyLambda(specialist.Invokable, specialist.Streamable, nil, nil, compose.WithLambdaType("Specialist"))
		if err != nil {
			return err
		}
		preHandler := func(_ context.Context, input []*schema.Message, state *state) ([]*schema.Message, error) {
			return state.msgs, nil // replace the tool call message with input msgs stored in state
		}
		if err := g.AddLambdaNode(specialist.Name, lambda, compose.WithStatePreHandler(preHandler), compose.WithNodeName(specialist.Name)); err != nil {
			return err
		}
	} else if specialist.ChatModel != nil {
		preHandler := func(_ context.Context, input []*schema.Message, state *state) ([]*schema.Message, error) {
			if len(specialist.SystemPrompt) > 0 {
				return append([]*schema.Message{{
					Role:    schema.System,
					Content: specialist.SystemPrompt,
				}}, state.msgs...), nil
			}

			return state.msgs, nil // replace the tool call message with input msgs stored in state
		}
		if err := g.AddChatModelNode(specialist.Name, specialist.ChatModel, compose.WithStatePreHandler(preHandler), compose.WithNodeName(specialist.Name)); err != nil {
			return err
		}
	}

	return g.AddEdge(specialist.Name, compose.END)
}

func addHostAgent(config *MultiAgentConfig, agentTools []*schema.ToolInfo, g *compose.Graph[[]*schema.Message, *schema.Message]) error {
	if err := config.Host.ChatModel.BindTools(agentTools); err != nil {
		return err
	}

	preHandler := func(_ context.Context, input []*schema.Message, state *state) ([]*schema.Message, error) {
		state.msgs = input
		if len(config.Host.SystemPrompt) == 0 {
			return input, nil
		}
		return append([]*schema.Message{{
			Role:    schema.System,
			Content: config.Host.SystemPrompt,
		}}, input...), nil
	}
	if err := g.AddChatModelNode(hostName, config.Host.ChatModel, compose.WithStatePreHandler(preHandler), compose.WithNodeName(hostName)); err != nil {
		return err
	}

	return g.AddEdge(compose.START, hostName)
}

func addDirectAnswerBranch(convertorName string, g *compose.Graph[[]*schema.Message, *schema.Message]) error {
	// handles the case where the host agent returns a direct answer, instead of handling off to any specialist
	branch := compose.NewStreamGraphBranch(func(ctx context.Context, sr *schema.StreamReader[*schema.Message]) (endNode string, err error) {
		defer sr.Close()

		for {
			msg, e := sr.Recv()
			if e == io.EOF {
				break
			}

			if e != nil {
				return "", e
			}

			if msg.Role != schema.Assistant {
				return "", fmt.Errorf("host agent should output assistant message, actual type= %s", msg.Role)
			}

			if len(msg.ToolCalls) == 0 {
				continue
			}

			if len(msg.ToolCalls) > 1 {
				handOffs := make([]string, 0, len(msg.ToolCalls))
				for _, t := range msg.ToolCalls {
					handOffs = append(handOffs, t.Function.Name)
				}
				return "", fmt.Errorf("host agent returns multiple handoff candidates: %v", handOffs)
			}

			function := msg.ToolCalls[0].Function
			if len(function.Name) == 0 {
				continue
			}

			return convertorName, nil
		}

		return compose.END, nil
	}, map[string]bool{convertorName: true, compose.END: true})

	return g.AddBranch(hostName, branch)
}

func addSpecialistsBranch(convertorName string, agentMap map[string]bool, g *compose.Graph[[]*schema.Message, *schema.Message]) error {
	branch := compose.NewGraphBranch(func(ctx context.Context, input []*schema.Message) (string, error) {
		if len(input) != 1 {
			return "", fmt.Errorf("host agent output %d messages, but expected 1", len(input))
		}

		if len(input[0].ToolCalls) != 1 {
			return "", fmt.Errorf("host agent output %d tool calls, but expected 1", len(input[0].ToolCalls))
		}

		return input[0].ToolCalls[0].Function.Name, nil
	}, agentMap)

	return g.AddBranch(convertorName, branch)
}