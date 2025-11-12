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

package adk

import (
	"context"
	"errors"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

var ErrExceedMaxIterations = errors.New("exceeds max iterations")

type State struct {
	Messages []Message

	HasReturnDirectly        bool
	ReturnDirectlyToolCallID string

	ToolGenActions map[string]*AgentAction

	AgentName string

	AgentToolInterruptData map[string] /*tool call id*/ *agentToolInterruptInfo

	RemainingIterations int
}

type agentToolInterruptInfo struct {
	LastEvent *AgentEvent
	Data      []byte
}

func SendToolGenAction(ctx context.Context, toolName string, action *AgentAction) error {
	return compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		st.ToolGenActions[toolName] = action

		return nil
	})
}

func popToolGenAction(ctx context.Context, toolName string) *AgentAction {
	var action *AgentAction
	err := compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		action = st.ToolGenActions[toolName]
		if action != nil {
			delete(st.ToolGenActions, toolName)
		}

		return nil
	})

	if err != nil {
		panic("impossible")
	}

	return action
}

type reactConfig struct {
	model model.ToolCallingChatModel

	toolsConfig *compose.ToolsNodeConfig

	toolsReturnDirectly map[string]bool

	agentName string

	maxIterations int

	beforeChatModel, afterChatModel []func(context.Context, *ChatModelAgentState) error
}

func genToolInfos(ctx context.Context, config *compose.ToolsNodeConfig) ([]*schema.ToolInfo, error) {
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

type reactGraph = *compose.Graph[[]Message, Message]
type sToolNodeOutput = *schema.StreamReader[[]Message]
type sGraphOutput = MessageStream

func getReturnDirectlyToolCallID(ctx context.Context) (string, bool) {
	var toolCallID string
	var hasReturnDirectly bool
	handler := func(_ context.Context, st *State) error {
		toolCallID = st.ReturnDirectlyToolCallID
		hasReturnDirectly = st.HasReturnDirectly
		return nil
	}

	_ = compose.ProcessState(ctx, handler)

	return toolCallID, hasReturnDirectly
}

func newReact(ctx context.Context, config *reactConfig) (reactGraph, error) {
	genState := func(ctx context.Context) *State {
		return &State{
			ToolGenActions:         map[string]*AgentAction{},
			AgentName:              config.agentName,
			AgentToolInterruptData: make(map[string]*agentToolInterruptInfo),
			RemainingIterations: func() int {
				if config.maxIterations <= 0 {
					return 20
				}
				return config.maxIterations
			}(),
		}
	}

	const (
		chatModel_ = "ChatModel"
		toolNode_  = "ToolNode"
	)

	g := compose.NewGraph[[]Message, Message](compose.WithGenLocalState(genState))

	toolsInfo, err := genToolInfos(ctx, config.toolsConfig)
	if err != nil {
		return nil, err
	}

	chatModel, err := config.model.WithTools(toolsInfo)
	if err != nil {
		return nil, err
	}

	toolsNode, err := compose.NewToolNode(ctx, config.toolsConfig)
	if err != nil {
		return nil, err
	}

	modelPreHandle := func(ctx context.Context, input []Message, st *State) ([]Message, error) {
		if st.RemainingIterations <= 0 {
			return nil, ErrExceedMaxIterations
		}
		st.RemainingIterations--

		s := &ChatModelAgentState{Messages: append(st.Messages, input...)}
		for _, b := range config.beforeChatModel {
			err = b(ctx, s)
			if err != nil {
				return nil, err
			}
		}
		st.Messages = s.Messages

		return st.Messages, nil
	}
	modelPostHandle := func(ctx context.Context, input Message, st *State) (Message, error) {
		s := &ChatModelAgentState{Messages: append(st.Messages, input)}
		for _, a := range config.afterChatModel {
			err = a(ctx, s)
			if err != nil {
				return nil, err
			}
		}
		st.Messages = s.Messages
		return input, nil
	}
	_ = g.AddChatModelNode(chatModel_, chatModel,
		compose.WithStatePreHandler(modelPreHandle), compose.WithStatePostHandler(modelPostHandle), compose.WithNodeName(chatModel_))

	toolPreHandle := func(ctx context.Context, input Message, st *State) (Message, error) {
		input = st.Messages[len(st.Messages)-1]
		if len(config.toolsReturnDirectly) > 0 {
			for i := range input.ToolCalls {
				toolName := input.ToolCalls[i].Function.Name
				if config.toolsReturnDirectly[toolName] {
					st.ReturnDirectlyToolCallID = input.ToolCalls[i].ID
					st.HasReturnDirectly = true
				}
			}
		}

		return input, nil
	}

	_ = g.AddToolsNode(toolNode_, toolsNode,
		compose.WithStatePreHandler(toolPreHandle), compose.WithNodeName(toolNode_))

	_ = g.AddEdge(compose.START, chatModel_)

	toolCallCheck := func(ctx context.Context, sMsg MessageStream) (string, error) {
		defer sMsg.Close()
		for {
			chunk, err_ := sMsg.Recv()
			if err_ != nil {
				if err_ == io.EOF {
					return compose.END, nil
				}

				return "", err_
			}

			if len(chunk.ToolCalls) > 0 {
				return toolNode_, nil
			}
		}
	}
	branch := compose.NewStreamGraphBranch(toolCallCheck, map[string]bool{compose.END: true, toolNode_: true})
	_ = g.AddBranch(chatModel_, branch)

	if len(config.toolsReturnDirectly) == 0 {
		_ = g.AddEdge(toolNode_, chatModel_)
	} else {
		const (
			toolNodeToEndConverter = "ToolNodeToEndConverter"
		)

		cvt := func(ctx context.Context, sToolCallMessages sToolNodeOutput) (sGraphOutput, error) {
			id, _ := getReturnDirectlyToolCallID(ctx)

			return schema.StreamReaderWithConvert(sToolCallMessages,
				func(in []Message) (Message, error) {

					for _, chunk := range in {
						if chunk != nil && chunk.ToolCallID == id {
							return chunk, nil
						}
					}

					return nil, schema.ErrNoValue
				}), nil
		}

		_ = g.AddLambdaNode(toolNodeToEndConverter, compose.TransformableLambda(cvt),
			compose.WithNodeName(toolNodeToEndConverter))
		_ = g.AddEdge(toolNodeToEndConverter, compose.END)

		checkReturnDirect := func(ctx context.Context,
			sToolCallMessages sToolNodeOutput) (string, error) {

			_, ok := getReturnDirectlyToolCallID(ctx)

			if ok {
				return toolNodeToEndConverter, nil
			}

			return chatModel_, nil
		}

		branch = compose.NewStreamGraphBranch(checkReturnDirect,
			map[string]bool{toolNodeToEndConverter: true, chatModel_: true})
		_ = g.AddBranch(toolNode_, branch)
	}

	return g, nil
}
