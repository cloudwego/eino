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

// ErrExceedMaxIterations indicates the agent reached the maximum iterations limit.
var ErrExceedMaxIterations = errors.New("exceeds max iterations")

// State holds agent runtime state including messages, tool actions,
// and remaining iterations.
type State struct {
	Messages []Message

	HasReturnDirectly        bool
	ReturnDirectlyToolCallID string

	ToolGenActions map[string]*AgentAction

	AgentName string

	RemainingIterations int

	RuntimeReturnDirectly map[string]struct{}

	ReturnDirectlyEvent *AgentEvent

	generator    *AsyncGenerator[*AgentEvent]
	retryAttempt int
}

// SendToolGenAction attaches an AgentAction to the next tool event emitted for the
// current tool execution.
//
// Where/when to use:
//   - Invoke within a tool's Run (Invokable/Streamable) implementation to include
//     an action alongside that tool's output event.
//   - The action is scoped by the current tool call context: if a ToolCallID is
//     available, it is used as the key to support concurrent calls of the same
//     tool with different parameters; otherwise, the provided toolName is used.
//   - The stored action is ephemeral and will be popped and attached to the tool
//     event when the tool finishes (including streaming completion).
//
// Limitation:
//   - This function is intended for use within ChatModelAgent runs only. It relies
//     on ChatModelAgent's internal State to store and pop actions, which is not
//     available in other agent types.
func SendToolGenAction(ctx context.Context, toolName string, action *AgentAction) error {
	key := toolName
	toolCallID := compose.GetToolCallID(ctx)
	if len(toolCallID) > 0 {
		key = toolCallID
	}

	return compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		st.ToolGenActions[key] = action

		return nil
	})
}

func popToolGenAction(ctx context.Context, toolName string) *AgentAction {
	toolCallID := compose.GetToolCallID(ctx)

	var action *AgentAction
	err := compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		if len(toolCallID) > 0 {
			if a := st.ToolGenActions[toolCallID]; a != nil {
				action = a
				delete(st.ToolGenActions, toolCallID)
				return nil
			}
		}

		if a := st.ToolGenActions[toolName]; a != nil {
			action = a
			delete(st.ToolGenActions, toolName)
		}

		return nil
	})

	if err != nil {
		panic("impossible")
	}

	return action
}

type toolResultEventSenderWrapper struct{}

func (w *toolResultEventSenderWrapper) WrapInvoke(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*ToolResult, error)) (*ToolResult, error) {
	result, err := next(ctx, call)
	if err != nil {
		return nil, err
	}

	prePopAction := popToolGenAction(ctx, call.Name)
	msg := schema.ToolMessage(result.Result, call.CallID, schema.WithToolName(call.Name))
	event := EventFromMessage(msg, nil, schema.Tool, call.Name)
	if prePopAction != nil {
		event.Action = prePopAction
	}

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.generator == nil {
			return nil
		}
		if st.HasReturnDirectly && st.ReturnDirectlyToolCallID == call.CallID {
			st.ReturnDirectlyEvent = event
		} else {
			st.generator.Send(event)
		}
		return nil
	})

	return result, nil
}

func (w *toolResultEventSenderWrapper) WrapStream(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*StreamToolResult, error)) (*StreamToolResult, error) {
	result, err := next(ctx, call)
	if err != nil {
		return nil, err
	}

	prePopAction := popToolGenAction(ctx, call.Name)
	streams := result.Result.Copy(2)

	cvt := func(in string) (Message, error) {
		return schema.ToolMessage(in, call.CallID, schema.WithToolName(call.Name)), nil
	}
	msgStream := schema.StreamReaderWithConvert(streams[0], cvt)
	event := EventFromMessage(nil, msgStream, schema.Tool, call.Name)
	event.Action = prePopAction

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.generator == nil {
			return nil
		}
		if st.HasReturnDirectly && st.ReturnDirectlyToolCallID == call.CallID {
			st.ReturnDirectlyEvent = event
		} else {
			st.generator.Send(event)
		}
		return nil
	})

	result.Result = streams[1]
	return result, nil
}

type reactInput struct {
	messages              []Message
	runtimeReturnDirectly map[string]struct{}
	generator             *AsyncGenerator[*AgentEvent]
}

type reactConfig struct {
	model model.ToolCallingChatModel

	toolsConfig *compose.ToolsNodeConfig

	toolsReturnDirectly map[string]struct{}

	agentName string

	maxIterations int

	handlers    []AgentHandler
	middlewares []AgentMiddleware
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

type reactGraph = *compose.Graph[*reactInput, Message]
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
			ToolGenActions: map[string]*AgentAction{},
			AgentName:      config.agentName,
			RemainingIterations: func() int {
				if config.maxIterations <= 0 {
					return 20
				}
				return config.maxIterations
			}(),
		}
	}

	const (
		initNode_  = "Init"
		chatModel_ = "ChatModel"
		toolNode_  = "ToolNode"
	)

	g := compose.NewGraph[*reactInput, Message](compose.WithGenLocalState(genState))

	initLambda := func(ctx context.Context, input *reactInput) ([]Message, error) {
		_ = compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
			st.RuntimeReturnDirectly = input.runtimeReturnDirectly
			st.generator = input.generator
			return nil
		})
		return input.messages, nil
	}
	_ = g.AddLambdaNode(initNode_, compose.InvokableLambda(initLambda), compose.WithNodeName(initNode_))

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

		messages := append(st.Messages, input...)

		for _, m := range config.middlewares {
			if m.BeforeChatModel != nil {
				state := &ChatModelAgentState{Messages: messages}
				if err := m.BeforeChatModel(ctx, state); err != nil {
					return nil, err
				}
				messages = state.Messages
			}
		}

		for _, h := range config.handlers {
			var err error
			ctx, messages, err = h.BeforeModelRewriteHistory(ctx, messages)
			if err != nil {
				return nil, err
			}
		}

		st.Messages = messages
		return st.Messages, nil
	}
	modelPostHandle := func(ctx context.Context, input Message, st *State) (Message, error) {
		messages := append(st.Messages, input)

		for _, m := range config.middlewares {
			if m.AfterChatModel != nil {
				state := &ChatModelAgentState{Messages: messages}
				if err := m.AfterChatModel(ctx, state); err != nil {
					return nil, err
				}
				messages = state.Messages
			}
		}

		for _, h := range config.handlers {
			var err error
			ctx, messages, err = h.AfterModelRewriteHistory(ctx, messages)
			if err != nil {
				return nil, err
			}
		}

		st.Messages = messages
		return input, nil
	}
	_ = g.AddChatModelNode(chatModel_, chatModel,
		compose.WithStatePreHandler(modelPreHandle), compose.WithStatePostHandler(modelPostHandle), compose.WithNodeName(chatModel_))

	toolPreHandle := func(ctx context.Context, input Message, st *State) (Message, error) {
		input = st.Messages[len(st.Messages)-1]

		returnDirectly := config.toolsReturnDirectly
		if len(st.RuntimeReturnDirectly) > 0 {
			returnDirectly = st.RuntimeReturnDirectly
		}

		if len(returnDirectly) > 0 {
			for i := range input.ToolCalls {
				toolName := input.ToolCalls[i].Function.Name
				if _, ok := returnDirectly[toolName]; ok {
					st.ReturnDirectlyToolCallID = input.ToolCalls[i].ID
					st.HasReturnDirectly = true
				}
			}
		}

		return input, nil
	}

	toolPostHandle := func(_ context.Context, out *schema.StreamReader[[]*schema.Message], st *State) (*schema.StreamReader[[]*schema.Message], error) {
		if st.ReturnDirectlyEvent != nil && st.generator != nil {
			st.generator.Send(st.ReturnDirectlyEvent)
			st.ReturnDirectlyEvent = nil
		}
		return out, nil
	}

	_ = g.AddToolsNode(toolNode_, toolsNode,
		compose.WithStatePreHandler(toolPreHandle),
		compose.WithStreamStatePostHandler(toolPostHandle),
		compose.WithNodeName(toolNode_))

	_ = g.AddEdge(compose.START, initNode_)
	_ = g.AddEdge(initNode_, chatModel_)

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

	if len(config.toolsReturnDirectly) > 0 {
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
	} else {
		_ = g.AddEdge(toolNode_, chatModel_)
	}

	return g, nil
}
