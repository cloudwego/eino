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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ErrExceedMaxIterations indicates the agent reached the maximum iterations limit.
var ErrExceedMaxIterations = errors.New("exceeds max iterations")

// State holds agent runtime state including messages and user-extensible storage.
//
// Deprecated: This type will be unexported in v1.0.0. Use ChatModelAgentState
// in HandlerMiddleware and AgentMiddleware callbacks instead. Direct use of
// compose.ProcessState[*State] is discouraged and will stop working in v1.0.0;
// use the handler APIs instead.
type State struct {
	Messages []Message
	Extra    map[string]any

	// Internal fields below - do not access directly.
	// Kept exported for backward compatibility with existing checkpoints.
	HasReturnDirectly        bool
	ReturnDirectlyToolCallID string
	ToolGenActions           map[string]*AgentAction
	AgentName                string
	RemainingIterations      int
	ReturnDirectlyEvent      *AgentEvent
	RetryAttempt             int
}

const (
	stateGobNameV07 = "_eino_adk_react_state"

	// stateGobNameV080 is a v0.8.0-v0.8.3-only alias used after byte-patching
	// raw checkpoint bytes in preprocessADKCheckpoint.
	// It must stay the same byte length as stateGobNameV07 so the length-prefixed
	// gob string in the stream remains valid.
	stateGobNameV080 = "_eino_adk_state_v080_"
)

func init() {
	// Checkpoint compatibility notes:
	// - ADK/compose checkpoints are gob-encoded and may store state behind `any`, so gob relies on
	//   an on-wire type name to choose a local Go type.
	// - Gob allows only one local Go type per name, and it treats "struct wire" and "GobEncoder wire"
	//   as incompatible even if the name matches.
	//
	// This file maintains 2 epochs of *State decoding:
	// - v0.7.* and current: "_eino_adk_react_state" + struct wire → decode into *State directly.
	//   State's exported fields are a superset of v0.7, so gob handles missing fields gracefully.
	// - v0.8.0-v0.8.3: "_eino_adk_react_state" + GobEncoder wire → byte-patched to stateGobNameV080,
	//   decode into stateV080 and migrate.
	schema.RegisterName[*State](stateGobNameV07)
	schema.RegisterName[*stateV080](stateGobNameV080)

	// the following two lines of registration mainly for backward compatibility
	// when decoding checkpoints created by v0.8.0 - v0.8.3
	gob.Register(&AgentEvent{})
	gob.Register(int(0))
}

func (s *State) getReturnDirectlyEvent() *AgentEvent {
	return s.ReturnDirectlyEvent
}

func (s *State) setReturnDirectlyEvent(event *AgentEvent) {
	s.ReturnDirectlyEvent = event
}

func (s *State) getRetryAttempt() int {
	return s.RetryAttempt
}

func (s *State) setRetryAttempt(attempt int) {
	s.RetryAttempt = attempt
}

func (s *State) getReturnDirectlyToolCallID() string {
	return s.ReturnDirectlyToolCallID
}

func (s *State) setReturnDirectlyToolCallID(id string) {
	s.ReturnDirectlyToolCallID = id
	s.HasReturnDirectly = id != ""
}

func (s *State) getToolGenActions() map[string]*AgentAction {
	return s.ToolGenActions
}

func (s *State) setToolGenAction(key string, action *AgentAction) {
	if s.ToolGenActions == nil {
		s.ToolGenActions = make(map[string]*AgentAction)
	}
	s.ToolGenActions[key] = action
}

func (s *State) popToolGenAction(key string) *AgentAction {
	if s.ToolGenActions == nil {
		return nil
	}
	action := s.ToolGenActions[key]
	delete(s.ToolGenActions, key)
	return action
}

func (s *State) getRemainingIterations() int {
	return s.RemainingIterations
}

func (s *State) setRemainingIterations(iterations int) {
	s.RemainingIterations = iterations
}

func (s *State) decrementRemainingIterations() {
	current := s.getRemainingIterations()
	s.RemainingIterations = current - 1
}

// stateV080 handles the v0.8.0-v0.8.3 checkpoint format.
// In those versions, *State implemented GobEncoder and was registered under
// "_eino_adk_react_state". GobEncode serialized a stateSerialization struct
// into opaque bytes. This type's GobDecode reads that format.
// It is registered under "_eino_adk_state_v080_" — a same-length alias used
// only after byte-patching the checkpoint data in preprocessADKCheckpoint.
type stateV080 struct {
	Messages                 []Message
	HasReturnDirectly        bool
	ReturnDirectlyToolCallID string
	ToolGenActions           map[string]*AgentAction
	AgentName                string
	RemainingIterations      int
	RetryAttempt             int
	ReturnDirectlyEvent      *AgentEvent
	Extra                    map[string]any
	Internals                map[string]any
}

// stateV080Serialization is the on-wire format that v0.8.0-v0.8.3 GobEncode produced.
// It is only used by stateV080.GobDecode to parse those legacy opaque bytes.
type stateV080Serialization stateV080

func (sc *stateV080) GobDecode(b []byte) error {
	ss := &stateV080Serialization{}
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(ss); err != nil {
		return err
	}
	sc.Messages = ss.Messages
	sc.HasReturnDirectly = ss.HasReturnDirectly
	sc.ReturnDirectlyToolCallID = ss.ReturnDirectlyToolCallID
	sc.ToolGenActions = ss.ToolGenActions
	sc.AgentName = ss.AgentName
	sc.RemainingIterations = ss.RemainingIterations
	sc.Extra = ss.Extra
	sc.Internals = ss.Internals
	return nil
}

// stateV080ToState converts a legacy *stateV080 (v0.8.0-v0.8.3) to a current *State.
func stateV080ToState(sc *stateV080) *State {
	s := &State{
		Messages:                 sc.Messages,
		HasReturnDirectly:        sc.HasReturnDirectly,
		ReturnDirectlyToolCallID: sc.ReturnDirectlyToolCallID,
		ToolGenActions:           sc.ToolGenActions,
		AgentName:                sc.AgentName,
		RemainingIterations:      sc.RemainingIterations,
		Extra:                    sc.Extra,
	}
	if sc.ReturnDirectlyToolCallID != "" {
		s.setReturnDirectlyToolCallID(sc.ReturnDirectlyToolCallID)
	}
	if sc.Internals != nil && s.RetryAttempt == 0 {
		if v, ok := sc.Internals["_retryAttempt"].(int); ok {
			s.RetryAttempt = v
		}
	}
	if sc.Internals != nil && s.ReturnDirectlyEvent == nil {
		if v, ok := sc.Internals["_returnDirectlyEvent"].(*AgentEvent); ok {
			s.ReturnDirectlyEvent = v
		}
	}
	return s
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
		st.setToolGenAction(key, action)
		return nil
	})
}

type reactInput struct {
	messages []Message
}

type reactConfig struct {
	// model is the chat model used by the react graph.
	// Tools are configured via model.WithTools call option, not the WithTools method.
	model model.BaseChatModel

	toolsConfig      *compose.ToolsNodeConfig
	modelWrapperConf *modelWrapperConfig

	toolsReturnDirectly map[string]bool

	agentName string

	maxIterations int
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
	handler := func(_ context.Context, st *State) error {
		toolCallID = st.getReturnDirectlyToolCallID()
		return nil
	}

	_ = compose.ProcessState(ctx, handler)

	return toolCallID, toolCallID != ""
}

func genReactState(config *reactConfig) func(ctx context.Context) *State {
	return func(ctx context.Context) *State {
		st := &State{
			AgentName: config.agentName,
		}
		maxIter := 20
		if config.maxIterations > 0 {
			maxIter = config.maxIterations
		}
		st.setRemainingIterations(maxIter)
		return st
	}
}

func newReact(ctx context.Context, config *reactConfig) (reactGraph, error) {
	const (
		initNode_  = "Init"
		chatModel_ = "ChatModel"
		toolNode_  = "ToolNode"
	)

	g := compose.NewGraph[*reactInput, Message](compose.WithGenLocalState(genReactState(config)))

	initLambda := func(ctx context.Context, input *reactInput) ([]Message, error) {
		return input.messages, nil
	}
	_ = g.AddLambdaNode(initNode_, compose.InvokableLambda(initLambda), compose.WithNodeName(initNode_))

	var wrappedModel model.BaseChatModel = config.model
	if config.modelWrapperConf != nil {
		wrappedModel = buildModelWrappers(config.model, config.modelWrapperConf)
	}

	toolsNode, err := compose.NewToolNode(ctx, config.toolsConfig)
	if err != nil {
		return nil, err
	}

	modelPreHandle := func(ctx context.Context, input []Message, st *State) ([]Message, error) {
		if st.getRemainingIterations() <= 0 {
			return nil, ErrExceedMaxIterations
		}
		st.decrementRemainingIterations()
		return input, nil
	}
	_ = g.AddChatModelNode(chatModel_, wrappedModel,
		compose.WithStatePreHandler(modelPreHandle), compose.WithNodeName(chatModel_))

	toolPreHandle := func(ctx context.Context, _ Message, st *State) (Message, error) {
		input := st.Messages[len(st.Messages)-1]

		returnDirectly := config.toolsReturnDirectly
		if execCtx := getChatModelAgentExecCtx(ctx); execCtx != nil && len(execCtx.runtimeReturnDirectly) > 0 {
			returnDirectly = execCtx.runtimeReturnDirectly
		}

		if len(returnDirectly) > 0 {
			for i := range input.ToolCalls {
				toolName := input.ToolCalls[i].Function.Name
				if _, ok := returnDirectly[toolName]; ok {
					st.setReturnDirectlyToolCallID(input.ToolCalls[i].ID)
				}
			}
		}

		return input, nil
	}

	toolPostHandle := func(ctx context.Context, out *schema.StreamReader[[]*schema.Message], st *State) (*schema.StreamReader[[]*schema.Message], error) {
		if event := st.getReturnDirectlyEvent(); event != nil {
			getChatModelAgentExecCtx(ctx).send(event)
			st.setReturnDirectlyEvent(nil)
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
