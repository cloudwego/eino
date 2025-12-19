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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultHostNodeKey                 = "host" // the key of the host node in the graph
	defaultHostPrompt                  = "decide which tool is best for the task and call only the best tool."
	specialistsAnswersCollectorNodeKey = "specialist_answers_collect"
	singleIntentAnswerNodeKey          = "single_intent_answer"
	multiIntentSummarizeNodeKey        = "multi_intents_summarize"
	defaultSummarizerPrompt            = "summarize the answers from the specialists into a single answer."
	map2ListConverterNodeKey           = "map_to_list"
)

type state struct {
	msgs              []*schema.Message
	isMultipleIntents bool
}

// NewMultiAgent creates a new host multi-agent system.
//
// IMPORTANT!! For models that don't output tool calls in the first streaming chunk (e.g. Claude)
// the default StreamToolCallChecker may not work properly since it only checks the first chunk for tool calls.
// In such cases, you need to implement a custom StreamToolCallChecker that can properly detect tool calls.
func NewMultiAgent(ctx context.Context, config *MultiAgentConfig) (*MultiAgent, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	hostKeyName := defaultHostNodeKey
	if config.HostNodeName != "" {
		hostKeyName = config.HostNodeName
	}

	var (
		hostPrompt      = config.Host.SystemPrompt
		name            = config.Name
		toolCallChecker = config.StreamToolCallChecker
	)

	if len(hostPrompt) == 0 {
		hostPrompt = defaultHostPrompt
	}

	if len(name) == 0 {
		name = "host multi agent"
	}

	if toolCallChecker == nil {
		toolCallChecker = firstChunkStreamToolCallChecker
	}

	g := compose.NewGraph[[]*schema.Message, *schema.Message](
		compose.WithGenLocalState(func(context.Context) *state { return &state{} }))

	if err := g.AddPassthroughNode(specialistsAnswersCollectorNodeKey); err != nil {
		return nil, err
	}

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

	chatModel, err := agent.ChatModelWithTools(config.Host.ChatModel, config.Host.ToolCallingModel, agentTools)
	if err != nil {
		return nil, err
	}

	if err = addHostAgent(chatModel, hostPrompt, g, hostKeyName); err != nil {
		return nil, err
	}

	const convertorName = "msg2MsgList"
	if err = g.AddLambdaNode(convertorName, compose.ToList[*schema.Message](), compose.WithNodeName("converter")); err != nil {
		return nil, err
	}

	if err = addDirectAnswerBranch(convertorName, g, toolCallChecker); err != nil {
		return nil, err
	}

	if err = addMultiSpecialistsBranch(convertorName, agentMap, g); err != nil {
		return nil, err
	}

	if err = addSingleIntentAnswerNode(g); err != nil {
		return nil, err
	}

	if err = addMultiIntentsSummarizeNode(config.Summarizer, g); err != nil {
		return nil, err
	}

	if err = addAfterSpecialistsBranch(g); err != nil {
		return nil, err
	}

	compileOpts := []compose.GraphCompileOption{compose.WithNodeTriggerMode(compose.AnyPredecessor), compose.WithGraphName(name)}
	r, err := g.Compile(ctx, compileOpts...)
	if err != nil {
		return nil, err
	}

	return &MultiAgent{
		runnable:         r,
		graph:            g,
		graphAddNodeOpts: []compose.GraphAddNodeOpt{compose.WithGraphCompileOptions(compileOpts...)},
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
		if err := g.AddLambdaNode(specialist.Name, lambda, compose.WithStatePreHandler(preHandler),
			compose.WithNodeName(specialist.Name), compose.WithOutputKey(specialist.Name)); err != nil {
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

		if err := g.AddChatModelNode(specialist.Name, specialist.ChatModel, compose.WithStatePreHandler(preHandler), compose.WithNodeName(specialist.Name), compose.WithOutputKey(specialist.Name)); err != nil {
			return err
		}
	}

	return g.AddEdge(specialist.Name, specialistsAnswersCollectorNodeKey)
}

func addHostAgent(model model.BaseChatModel, prompt string, g *compose.Graph[[]*schema.Message, *schema.Message], hostNodeName string) error {
	preHandler := func(_ context.Context, input []*schema.Message, state *state) ([]*schema.Message, error) {
		state.msgs = input
		if len(prompt) == 0 {
			return input, nil
		}
		return append([]*schema.Message{{
			Role:    schema.System,
			Content: prompt,
		}}, input...), nil
	}
	if err := g.AddChatModelNode(defaultHostNodeKey, model, compose.WithStatePreHandler(preHandler), compose.WithNodeName(hostNodeName)); err != nil {
		return err
	}

	return g.AddEdge(compose.START, defaultHostNodeKey)
}

func addDirectAnswerBranch(convertorName string, g *compose.Graph[[]*schema.Message, *schema.Message],
	toolCallChecker func(ctx context.Context, modelOutput *schema.StreamReader[*schema.Message]) (bool, error)) error {
	// handles the case where the host agent returns a direct answer, instead of handling off to any specialist
	branch := compose.NewStreamGraphBranch(func(ctx context.Context, sr *schema.StreamReader[*schema.Message]) (endNode string, err error) {
		isToolCall, err := toolCallChecker(ctx, sr)
		if err != nil {
			return "", err
		}
		if isToolCall {
			return convertorName, nil
		}
		return compose.END, nil
	}, map[string]bool{convertorName: true, compose.END: true})

	return g.AddBranch(defaultHostNodeKey, branch)
}

func addMultiSpecialistsBranch(convertorName string, agentMap map[string]bool, g *compose.Graph[[]*schema.Message, *schema.Message]) error {
	branch := compose.NewGraphMultiBranch(func(ctx context.Context, input []*schema.Message) (map[string]bool, error) {
		if len(input) != 1 {
			return nil, fmt.Errorf("host agent output %d messages, but expected 1", len(input))
		}

		results := map[string]bool{}
		for _, toolCall := range input[0].ToolCalls {
			results[toolCall.Function.Name] = true
		}

		if len(results) > 1 {
			_ = compose.ProcessState(ctx, func(_ context.Context, state *state) error {
				state.isMultipleIntents = true
				return nil
			})
		}

		return results, nil
	}, agentMap)

	return g.AddBranch(convertorName, branch)
}

func addSingleIntentAnswerNode(g *compose.Graph[[]*schema.Message, *schema.Message]) error {
	rc := func(ctx context.Context, input *schema.StreamReader[map[string]any]) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderWithConvert(input, func(msgs map[string]any) (*schema.Message, error) {
			if len(msgs) != 1 {
				return nil, fmt.Errorf("host agent output %d messages, but expected 1", len(msgs))
			}
			for _, msg := range msgs {
				return msg.(*schema.Message), nil
			}
			return nil, schema.ErrNoValue
		}), nil
	}

	_ = g.AddLambdaNode(singleIntentAnswerNodeKey, compose.TransformableLambda(rc))
	return g.AddEdge(singleIntentAnswerNodeKey, compose.END)
}

func addAfterSpecialistsBranch(g *compose.Graph[[]*schema.Message, *schema.Message]) error {
	ab := func(ctx context.Context, _ *schema.StreamReader[map[string]any]) (string, error) {
		var isMultipleIntents bool
		_ = compose.ProcessState(ctx, func(_ context.Context, state *state) error {
			isMultipleIntents = state.isMultipleIntents
			return nil
		})

		if !isMultipleIntents {
			return singleIntentAnswerNodeKey, nil
		}

		return map2ListConverterNodeKey, nil
	}

	b := compose.NewStreamGraphBranch(ab, map[string]bool{
		singleIntentAnswerNodeKey: true,
		map2ListConverterNodeKey:  true,
	})

	return g.AddBranch(specialistsAnswersCollectorNodeKey, b)
}

func addMultiIntentsSummarizeNode(summarizer *Summarizer, g *compose.Graph[[]*schema.Message, *schema.Message]) error {
	map2list := func(ctx context.Context, input map[string]any) ([]*schema.Message, error) {
		var output []*schema.Message
		for k := range input {
			output = append(output, input[k].(*schema.Message))
		}
		return output, nil
	}

	_ = g.AddLambdaNode(map2ListConverterNodeKey, compose.InvokableLambda(map2list))

	if summarizer != nil {
		_ = g.AddChatModelNode(multiIntentSummarizeNodeKey, summarizer.ChatModel,
			compose.WithStatePreHandler(func(ctx context.Context, in []*schema.Message, state *state) ([]*schema.Message, error) {
				var (
					out          []*schema.Message
					systemPrompt = defaultSummarizerPrompt
				)
				if summarizer.SystemPrompt != "" {
					systemPrompt = summarizer.SystemPrompt
				}
				out = append(out, &schema.Message{
					Role:    schema.System,
					Content: systemPrompt,
				})

				out = append(out, state.msgs...)
				out = append(out, in...)
				return out, nil
			}))
		_ = g.AddEdge(map2ListConverterNodeKey, multiIntentSummarizeNodeKey)
		return g.AddEdge(multiIntentSummarizeNodeKey, compose.END)
	}

	s := func(ctx context.Context, input []*schema.Message) (*schema.Message, error) {
		output := &schema.Message{
			Role: schema.Assistant,
		}

		for _, msg := range input {
			output.Content += msg.Content + "\n"
		}

		return output, nil
	}

	_ = g.AddLambdaNode(multiIntentSummarizeNodeKey, compose.InvokableLambda(s))
	_ = g.AddEdge(map2ListConverterNodeKey, multiIntentSummarizeNodeKey)
	return g.AddEdge(multiIntentSummarizeNodeKey, compose.END)
}
