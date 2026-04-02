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

type typedState[M MessageType] struct {
	Messages []M
	Extra    map[string]any

	// Internal fields below - do not access directly.
	// Kept exported for backward compatibility with existing checkpoints.
	HasReturnDirectly        bool
	ReturnDirectlyToolCallID string
	ToolGenActions           map[string]*AgentAction
	AgentName                string
	RemainingIterations      int
	ReturnDirectlyEvent      *TypedAgentEvent[M]
	RetryAttempt             int
}

type State = typedState[*schema.Message]

type agenticState = typedState[*schema.AgenticMessage]

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

	schema.RegisterName[*typedState[*schema.AgenticMessage]]("_eino_adk_agentic_state")
	schema.RegisterName[*TypedAgentEvent[*schema.AgenticMessage]]("_eino_adk_agentic_event")

	gob.Register(&AgentEvent{})
	gob.Register(int(0))
	gob.Register(&AgentInput{})
	gob.Register(&TypedAgentInput[*schema.AgenticMessage]{})
	gob.Register(&typedAgentEventWrapper[*schema.AgenticMessage]{})
	gob.Register(&[]*typedAgentEventWrapper[*schema.AgenticMessage]{})
	gob.Register(&typedLaneEventsOf[*schema.AgenticMessage]{})
	gob.Register(map[int][]*typedAgentEventWrapper[*schema.AgenticMessage]{})
	gob.Register(&schema.AgenticMessage{})
	gob.Register([]*schema.AgenticMessage{})
	schema.RegisterName[*reactInput]("_eino_adk_react_input")
	schema.RegisterName[*agenticReactInput]("_eino_adk_agentic_react_input")
}

func (s *typedState[M]) getReturnDirectlyEvent() *TypedAgentEvent[M] {
	return s.ReturnDirectlyEvent
}

func (s *typedState[M]) setReturnDirectlyEvent(event *TypedAgentEvent[M]) {
	s.ReturnDirectlyEvent = event
}

func (s *typedState[M]) getRetryAttempt() int {
	return s.RetryAttempt
}

func (s *typedState[M]) setRetryAttempt(attempt int) {
	s.RetryAttempt = attempt
}

func (s *typedState[M]) getReturnDirectlyToolCallID() string {
	return s.ReturnDirectlyToolCallID
}

func (s *typedState[M]) setReturnDirectlyToolCallID(id string) {
	s.ReturnDirectlyToolCallID = id
	s.HasReturnDirectly = id != ""
}

func (s *typedState[M]) getToolGenActions() map[string]*AgentAction {
	return s.ToolGenActions
}

func (s *typedState[M]) setToolGenAction(key string, action *AgentAction) {
	if s.ToolGenActions == nil {
		s.ToolGenActions = make(map[string]*AgentAction)
	}
	s.ToolGenActions[key] = action
}

func (s *typedState[M]) popToolGenAction(key string) *AgentAction {
	if s.ToolGenActions == nil {
		return nil
	}
	action := s.ToolGenActions[key]
	delete(s.ToolGenActions, key)
	return action
}

func (s *typedState[M]) getRemainingIterations() int {
	return s.RemainingIterations
}

func (s *typedState[M]) setRemainingIterations(iterations int) {
	s.RemainingIterations = iterations
}

func (s *typedState[M]) decrementRemainingIterations() {
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
	Messages []Message
}

type typedReactConfig[M MessageType] struct {
	model model.BaseModel[M]

	toolsConfig      *compose.ToolsNodeConfig
	modelWrapperConf *typedModelWrapperConfig[M]

	toolsReturnDirectly map[string]bool

	agentName string

	maxIterations int

	cancelCtx *cancelContext
}

type reactConfig = typedReactConfig[*schema.Message]

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
		initNode_                      = "Init"
		chatModel_                     = "ChatModel"
		cancelCheckNode_               = "CancelCheck"
		toolNode_                      = "ToolNode"
		afterToolCallsNode_            = "AfterToolCalls"
		afterToolCallsCancelCheckNode_ = "AfterToolCallsCancelCheck"
	)

	cancelCtx := config.cancelCtx
	g := compose.NewGraph[*reactInput, Message](compose.WithGenLocalState(genReactState(config)))
	_ = g.AddLambdaNode(initNode_, compose.InvokableLambda(func(ctx context.Context, input *reactInput) ([]Message, error) {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.Messages = append(st.Messages, input.Messages...)
			return nil
		})
		return input.Messages, nil
	}), compose.WithNodeName(initNode_))

	var wrappedModel model.BaseChatModel = config.model
	if config.modelWrapperConf != nil {
		wrappedModel = buildModelWrappers(config.model, config.modelWrapperConf)
	}

	toolsConfig := config.toolsConfig

	toolsNode, err := compose.NewToolNode(ctx, toolsConfig)
	if err != nil {
		return nil, err
	}

	_ = g.AddChatModelNode(chatModel_, wrappedModel, compose.WithStatePreHandler(
		func(ctx context.Context, input []Message, st *State) ([]Message, error) {
			if st.getRemainingIterations() <= 0 {
				return nil, ErrExceedMaxIterations
			}
			st.decrementRemainingIterations()
			return input, nil
		}), compose.WithNodeName(chatModel_))

	// CancelAfterChatModel safe-point: on the tool-calls path, after the branch
	// has confirmed that the model response contains tool calls (i.e. not a final
	// answer). Skipped entirely when the model produces a final answer.
	_ = g.AddLambdaNode(cancelCheckNode_, compose.InvokableLambda(func(ctx context.Context, msg Message) (Message, error) {
		if cancelCtx != nil && cancelCtx.shouldCancel() {
			if cancelCtx.getMode()&CancelAfterChatModel != 0 {
				return nil, compose.StatefulInterrupt(ctx, "CancelAfterChatModel", msg)
			}
		}
		wasInterrupted, hasState, state := compose.GetInterruptState[Message](ctx)
		if wasInterrupted && hasState {
			msg = state
		}
		return msg, nil
	}), compose.WithNodeName(cancelCheckNode_))

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

	// AfterToolCalls node: calls AfterToolCallsRewriteState handlers after all tool calls complete.
	// The graph auto-materializes the ToolsNode stream into []Message before this node.
	afterToolCalls := func(ctx context.Context, toolResults []Message) ([]Message, error) {
		var stateMessages []Message
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			stateMessages = st.Messages
			return nil
		})

		state := &ChatModelAgentState{Messages: append(stateMessages, toolResults...)}

		if config.modelWrapperConf != nil {
			assistantMsg := stateMessages[len(stateMessages)-1]
			tc := &ToolCallsContext{}
			for _, toolCall := range assistantMsg.ToolCalls {
				tc.ToolCalls = append(tc.ToolCalls, ToolContext{Name: toolCall.Function.Name, CallID: toolCall.ID})
			}

			for _, handler := range config.modelWrapperConf.handlers {
				var err error
				ctx, state, err = handler.AfterToolCallsRewriteState(ctx, state, tc)
				if err != nil {
					return nil, err
				}
			}
		}

		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.Messages = state.Messages
			return nil
		})

		return toolResults, nil
	}
	_ = g.AddLambdaNode(afterToolCallsNode_, compose.InvokableLambda(afterToolCalls),
		compose.WithNodeName(afterToolCallsNode_))

	// AfterToolCallsCancelCheck: CancelAfterToolCalls safe-point, separated from toolPostHandle.
	afterToolCallsCancelCheck := func(ctx context.Context, toolResults []Message) ([]Message, error) {
		if cancelCtx != nil && cancelCtx.shouldCancel() {
			if cancelCtx.getMode()&CancelAfterToolCalls != 0 {
				return nil, compose.Interrupt(ctx, "CancelAfterToolCalls")
			}
		}
		return toolResults, nil
	}
	_ = g.AddLambdaNode(afterToolCallsCancelCheckNode_, compose.InvokableLambda(afterToolCallsCancelCheck),
		compose.WithNodeName(afterToolCallsCancelCheckNode_))

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
				return cancelCheckNode_, nil
			}
		}
	}
	branch := compose.NewStreamGraphBranch(toolCallCheck, map[string]bool{compose.END: true, cancelCheckNode_: true})
	_ = g.AddBranch(chatModel_, branch)

	_ = g.AddEdge(cancelCheckNode_, toolNode_)
	_ = g.AddEdge(toolNode_, afterToolCallsNode_)
	_ = g.AddEdge(afterToolCallsNode_, afterToolCallsCancelCheckNode_)

	if len(config.toolsReturnDirectly) > 0 {
		const (
			toolNodeToEndConverter = "ToolNodeToEndConverter"
		)

		cvt := func(ctx context.Context, toolResults []Message) (Message, error) {
			id, _ := getReturnDirectlyToolCallID(ctx)

			for _, msg := range toolResults {
				if msg != nil && msg.ToolCallID == id {
					return msg, nil
				}
			}

			return nil, errors.New("return directly tool call result not found")
		}

		_ = g.AddLambdaNode(toolNodeToEndConverter, compose.InvokableLambda(cvt),
			compose.WithNodeName(toolNodeToEndConverter))
		_ = g.AddEdge(toolNodeToEndConverter, compose.END)

		checkReturnDirect := func(ctx context.Context, toolResults []Message) (string, error) {
			_, ok := getReturnDirectlyToolCallID(ctx)

			if ok {
				return toolNodeToEndConverter, nil
			}

			return chatModel_, nil
		}

		returnDirectBranch := compose.NewGraphBranch(checkReturnDirect,
			map[string]bool{toolNodeToEndConverter: true, chatModel_: true})
		_ = g.AddBranch(afterToolCallsCancelCheckNode_, returnDirectBranch)
	} else {
		_ = g.AddEdge(afterToolCallsCancelCheckNode_, chatModel_)
	}

	return g, nil
}

type agenticReactInput struct {
	Messages []*schema.AgenticMessage
}

type agenticReactConfig = typedReactConfig[*schema.AgenticMessage]

type agenticReactGraph = *compose.Graph[*agenticReactInput, *schema.AgenticMessage]

func getAgenticReturnDirectlyToolCallID(ctx context.Context) (string, bool) {
	var toolCallID string
	_ = compose.ProcessState(ctx, func(_ context.Context, st *agenticState) error {
		toolCallID = st.getReturnDirectlyToolCallID()
		return nil
	})
	return toolCallID, toolCallID != ""
}

func genAgenticReactState(config *agenticReactConfig) func(ctx context.Context) *agenticState {
	return func(ctx context.Context) *agenticState {
		st := &agenticState{
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

func agenticMessageHasToolCalls(msg *schema.AgenticMessage) bool {
	if msg == nil {
		return false
	}
	for _, block := range msg.ContentBlocks {
		if block != nil && block.Type == schema.ContentBlockTypeFunctionToolCall && block.FunctionToolCall != nil {
			return true
		}
	}
	return false
}

func newAgenticReact(ctx context.Context, config *agenticReactConfig) (agenticReactGraph, error) {
	const (
		initNode_                      = "Init"
		chatModel_                     = "ChatModel"
		cancelCheckNode_               = "CancelCheck"
		toolNode_                      = "ToolNode"
		afterToolCallsNode_            = "AfterToolCalls"
		afterToolCallsCancelCheckNode_ = "AfterToolCallsCancelCheck"
	)

	cancelCtx := config.cancelCtx
	g := compose.NewGraph[*agenticReactInput, *schema.AgenticMessage](
		compose.WithGenLocalState(genAgenticReactState(config)))
	_ = g.AddLambdaNode(initNode_, compose.InvokableLambda(func(ctx context.Context, input *agenticReactInput) ([]*schema.AgenticMessage, error) {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *agenticState) error {
			st.Messages = append(st.Messages, input.Messages...)
			return nil
		})
		return input.Messages, nil
	}), compose.WithNodeName(initNode_))

	var wrappedModel model.AgenticModel = config.model
	if config.modelWrapperConf != nil {
		wrappedModel = buildModelWrappers(config.model, config.modelWrapperConf)
	}

	toolsNode, err := compose.NewAgenticToolsNode(ctx, config.toolsConfig)
	if err != nil {
		return nil, err
	}

	_ = g.AddAgenticModelNode(chatModel_, wrappedModel, compose.WithStatePreHandler(
		func(ctx context.Context, input []*schema.AgenticMessage, st *agenticState) ([]*schema.AgenticMessage, error) {
			if st.getRemainingIterations() <= 0 {
				return nil, ErrExceedMaxIterations
			}
			st.decrementRemainingIterations()
			return input, nil
		}), compose.WithNodeName(chatModel_))

	_ = g.AddLambdaNode(cancelCheckNode_, compose.InvokableLambda(func(ctx context.Context, msg *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		if cancelCtx != nil && cancelCtx.shouldCancel() {
			if cancelCtx.getMode()&CancelAfterChatModel != 0 {
				return nil, compose.StatefulInterrupt(ctx, "CancelAfterChatModel", msg)
			}
		}
		wasInterrupted, hasState, state := compose.GetInterruptState[*schema.AgenticMessage](ctx)
		if wasInterrupted && hasState {
			msg = state
		}
		return msg, nil
	}), compose.WithNodeName(cancelCheckNode_))

	toolPreHandle := func(ctx context.Context, _ *schema.AgenticMessage, st *agenticState) (*schema.AgenticMessage, error) {
		input := st.Messages[len(st.Messages)-1]
		returnDirectly := config.toolsReturnDirectly
		if execCtx := getTypedChatModelAgentExecCtx[*schema.AgenticMessage](ctx); execCtx != nil && len(execCtx.runtimeReturnDirectly) > 0 {
			returnDirectly = execCtx.runtimeReturnDirectly
		}
		if len(returnDirectly) > 0 {
			for _, block := range input.ContentBlocks {
				if block == nil || block.Type != schema.ContentBlockTypeFunctionToolCall || block.FunctionToolCall == nil {
					continue
				}
				if _, ok := returnDirectly[block.FunctionToolCall.Name]; ok {
					st.setReturnDirectlyToolCallID(block.FunctionToolCall.CallID)
				}
			}
		}
		return input, nil
	}
	toolPostHandle := func(ctx context.Context, out *schema.StreamReader[[]*schema.AgenticMessage], st *agenticState) (*schema.StreamReader[[]*schema.AgenticMessage], error) {
		if event := st.getReturnDirectlyEvent(); event != nil {
			getTypedChatModelAgentExecCtx[*schema.AgenticMessage](ctx).send(event)
			st.setReturnDirectlyEvent(nil)
		}
		return out, nil
	}
	_ = g.AddAgenticToolsNode(toolNode_, toolsNode,
		compose.WithStatePreHandler(toolPreHandle),
		compose.WithStreamStatePostHandler(toolPostHandle),
		compose.WithNodeName(toolNode_))

	afterToolCalls := func(ctx context.Context, toolResults []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *agenticState) error {
			st.Messages = append(st.Messages, toolResults...)
			return nil
		})
		return toolResults, nil
	}
	_ = g.AddLambdaNode(afterToolCallsNode_, compose.InvokableLambda(afterToolCalls),
		compose.WithNodeName(afterToolCallsNode_))

	afterToolCallsCancelCheck := func(ctx context.Context, toolResults []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
		if cancelCtx != nil && cancelCtx.shouldCancel() {
			if cancelCtx.getMode()&CancelAfterToolCalls != 0 {
				return nil, compose.Interrupt(ctx, "CancelAfterToolCalls")
			}
		}
		return toolResults, nil
	}
	_ = g.AddLambdaNode(afterToolCallsCancelCheckNode_, compose.InvokableLambda(afterToolCallsCancelCheck),
		compose.WithNodeName(afterToolCallsCancelCheckNode_))

	_ = g.AddEdge(compose.START, initNode_)
	_ = g.AddEdge(initNode_, chatModel_)

	toolCallCheck := func(ctx context.Context, sMsg *schema.StreamReader[*schema.AgenticMessage]) (string, error) {
		defer sMsg.Close()
		for {
			chunk, err_ := sMsg.Recv()
			if err_ != nil {
				if err_ == io.EOF {
					return compose.END, nil
				}
				return "", err_
			}
			if agenticMessageHasToolCalls(chunk) {
				return cancelCheckNode_, nil
			}
		}
	}
	branch := compose.NewStreamGraphBranch(toolCallCheck, map[string]bool{compose.END: true, cancelCheckNode_: true})
	_ = g.AddBranch(chatModel_, branch)

	_ = g.AddEdge(cancelCheckNode_, toolNode_)
	_ = g.AddEdge(toolNode_, afterToolCallsNode_)
	_ = g.AddEdge(afterToolCallsNode_, afterToolCallsCancelCheckNode_)

	if len(config.toolsReturnDirectly) > 0 {
		const (
			toolNodeToEndConverter = "ToolNodeToEndConverter"
		)

		cvt := func(ctx context.Context, toolResults []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
			id, _ := getAgenticReturnDirectlyToolCallID(ctx)
			for _, msg := range toolResults {
				if msg == nil {
					continue
				}
				for _, block := range msg.ContentBlocks {
					if block != nil && block.Type == schema.ContentBlockTypeFunctionToolResult &&
						block.FunctionToolResult != nil && block.FunctionToolResult.CallID == id {
						return msg, nil
					}
				}
			}
			return nil, errors.New("return directly tool call result not found")
		}

		_ = g.AddLambdaNode(toolNodeToEndConverter, compose.InvokableLambda(cvt),
			compose.WithNodeName(toolNodeToEndConverter))
		_ = g.AddEdge(toolNodeToEndConverter, compose.END)

		checkReturnDirect := func(ctx context.Context, toolResults []*schema.AgenticMessage) (string, error) {
			_, ok := getAgenticReturnDirectlyToolCallID(ctx)
			if ok {
				return toolNodeToEndConverter, nil
			}
			return chatModel_, nil
		}

		returnDirectBranch := compose.NewGraphBranch(checkReturnDirect,
			map[string]bool{toolNodeToEndConverter: true, chatModel_: true})
		_ = g.AddBranch(afterToolCallsCancelCheckNode_, returnDirectBranch)
	} else {
		_ = g.AddEdge(afterToolCallsCancelCheckNode_, chatModel_)
	}

	return g, nil
}
