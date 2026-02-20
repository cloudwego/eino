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
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
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
	extra    map[string]any

	// Internal fields below - do not access directly.
	// Kept exported for backward compatibility with existing checkpoints.
	ReturnDirectlyToolCallID string
	ToolGenActions           map[string]*AgentAction
	AgentName                string
	RemainingIterations      int

	internals map[string]any
}

const (
	stateKeyReturnDirectlyEvent = "_returnDirectlyEvent"
	stateKeyRetryAttempt        = "_retryAttempt"
)

func init() {
	gob.Register(&AgentEvent{})
	gob.Register(int(0))
}

func (s *State) getReturnDirectlyEvent() *AgentEvent {
	if s.internals == nil {
		return nil
	}
	if v, ok := s.internals[stateKeyReturnDirectlyEvent]; ok {
		return v.(*AgentEvent)
	}
	return nil
}

func (s *State) setReturnDirectlyEvent(event *AgentEvent) {
	if s.internals == nil {
		s.internals = make(map[string]any)
	}
	if event == nil {
		delete(s.internals, stateKeyReturnDirectlyEvent)
	} else {
		s.internals[stateKeyReturnDirectlyEvent] = event
	}
}

func (s *State) getRetryAttempt() int {
	if s.internals == nil {
		return 0
	}
	if v, ok := s.internals[stateKeyRetryAttempt]; ok {
		return v.(int)
	}
	return 0
}

func (s *State) setRetryAttempt(attempt int) {
	if s.internals == nil {
		s.internals = make(map[string]any)
	}
	s.internals[stateKeyRetryAttempt] = attempt
}

const (
	stateKeyReturnDirectlyToolCallID = "_returnDirectlyToolCallID"
	stateKeyToolGenActions           = "_toolGenActions"
	stateKeyRemainingIterations      = "_remainingIterations"
)

func (s *State) getReturnDirectlyToolCallID() string {
	if s.internals == nil {
		return ""
	}
	if v, ok := s.internals[stateKeyReturnDirectlyToolCallID].(string); ok {
		return v
	}
	return ""
}

func (s *State) setReturnDirectlyToolCallID(id string) {
	if s.internals == nil {
		s.internals = make(map[string]any)
	}
	s.internals[stateKeyReturnDirectlyToolCallID] = id
}

func (s *State) getToolGenActions() map[string]*AgentAction {
	if s.internals == nil {
		return nil
	}
	if v, ok := s.internals[stateKeyToolGenActions].(map[string]*AgentAction); ok {
		return v
	}
	return nil
}

func (s *State) setToolGenAction(key string, action *AgentAction) {
	if s.internals == nil {
		s.internals = make(map[string]any)
	}
	actions, ok := s.internals[stateKeyToolGenActions].(map[string]*AgentAction)
	if !ok || actions == nil {
		actions = make(map[string]*AgentAction)
		s.internals[stateKeyToolGenActions] = actions
	}
	actions[key] = action
}

func (s *State) popToolGenAction(key string) *AgentAction {
	if s.internals == nil {
		return nil
	}
	actions, ok := s.internals[stateKeyToolGenActions].(map[string]*AgentAction)
	if !ok || actions == nil {
		return nil
	}
	action := actions[key]
	delete(actions, key)
	return action
}

func (s *State) getRemainingIterations() int {
	if s.internals == nil {
		return 0
	}
	if v, ok := s.internals[stateKeyRemainingIterations].(int); ok {
		return v
	}
	return 0
}

func (s *State) setRemainingIterations(iterations int) {
	if s.internals == nil {
		s.internals = make(map[string]any)
	}
	s.internals[stateKeyRemainingIterations] = iterations
}

func (s *State) decrementRemainingIterations() {
	if s.internals == nil {
		s.internals = make(map[string]any)
	}
	current := s.getRemainingIterations()
	s.internals[stateKeyRemainingIterations] = current - 1
}

type stateSerialization struct {
	Messages                 []Message
	ReturnDirectlyToolCallID string
	ToolGenActions           map[string]*AgentAction
	AgentName                string
	RemainingIterations      int
	Extra                    map[string]any
	Internals                map[string]any
}

func (s *State) GobEncode() ([]byte, error) {
	internals := s.internals
	if internals == nil {
		internals = make(map[string]any)
	}
	ss := &stateSerialization{
		Messages:                 s.Messages,
		ReturnDirectlyToolCallID: s.getReturnDirectlyToolCallID(),
		ToolGenActions:           s.getToolGenActions(),
		AgentName:                s.AgentName,
		RemainingIterations:      s.getRemainingIterations(),
		Extra:                    s.extra,
		Internals:                internals,
	}
	buf := &bytes.Buffer{}
	if err := gob.NewEncoder(buf).Encode(ss); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *State) GobDecode(b []byte) error {
	ss := &stateSerialization{}
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(ss); err != nil {
		return err
	}
	s.Messages = ss.Messages
	s.extra = ss.Extra
	s.internals = ss.Internals
	if s.internals == nil {
		s.internals = make(map[string]any)
	}

	s.AgentName = ss.AgentName

	if ss.ReturnDirectlyToolCallID != "" {
		s.setReturnDirectlyToolCallID(ss.ReturnDirectlyToolCallID)
	}
	if ss.ToolGenActions != nil {
		s.internals[stateKeyToolGenActions] = ss.ToolGenActions
	}
	if ss.RemainingIterations != 0 {
		s.setRemainingIterations(ss.RemainingIterations)
	}
	return nil
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

func newReact(ctx context.Context, config *reactConfig, cs *cancelSig) (reactGraph, error) {
	const (
		initNode_       = "Init"
		chatModel_      = "ChatModel"
		beforeToolNode_ = "BeforeToolNode"
		toolNode_       = "ToolNode"
		afterToolNode_  = "AfterToolNode"
	)

	checkCancel := cs != nil

	nodeNameAfterModel := func() string {
		if checkCancel {
			return beforeToolNode_
		}
		return toolNode_
	}

	nodeNameAfterTool := func() string {
		if checkCancel {
			return afterToolNode_
		}
		return chatModel_
	}

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
				return nodeNameAfterModel(), nil
			}
		}
	}
	branch := compose.NewStreamGraphBranch(toolCallCheck, map[string]bool{compose.END: true, nodeNameAfterModel(): true})
	_ = g.AddBranch(chatModel_, branch)

	if checkCancel {
		beforeToolNode := func(ctx context.Context, input Message) (output Message, err error) {
			if sig := checkCancelSig(cs); sig != nil && sig.Mode != CancelAfterToolCall {
				return nil, compose.Interrupt(ctx, "cancelled externally")
			}

			return input, nil
		}
		_ = g.AddLambdaNode(beforeToolNode_, compose.InvokableLambda(beforeToolNode), compose.WithNodeName(beforeToolNode_))
		g.AddEdge(beforeToolNode_, toolNode_)

		afterToolNode := func(ctx context.Context, input []Message) (output []Message, err error) {
			if sig := checkCancelSig(cs); sig != nil && sig.Mode != CancelAfterChatModel {
				return nil, compose.Interrupt(ctx, "cancelled externally")
			}

			return input, nil
		}
		_ = g.AddLambdaNode(afterToolNode_, compose.InvokableLambda(afterToolNode), compose.WithNodeName(afterToolNode_))
		g.AddEdge(afterToolNode_, chatModel_)
	}

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

			return nodeNameAfterTool(), nil
		}

		branch = compose.NewStreamGraphBranch(checkReturnDirect,
			map[string]bool{toolNodeToEndConverter: true, nodeNameAfterTool(): true})
		_ = g.AddBranch(toolNode_, branch)
	} else {
		_ = g.AddEdge(toolNode_, nodeNameAfterTool())
	}

	return g, nil
}

type cancelSig struct {
	done   chan struct{}
	config atomic.Value
}

func newCancelSig() *cancelSig {
	return &cancelSig{
		done: make(chan struct{}),
	}
}

func (cs *cancelSig) cancel(cfg *cancelConfig) {
	cs.config.Store(cfg)
	close(cs.done)
}

func (cs *cancelSig) isCancelled() bool {
	select {
	case <-cs.done:
		return true
	default:
		return false
	}
}

func checkCancelSig(cs *cancelSig) *cancelConfig {
	if cs == nil {
		return nil
	}
	select {
	case <-cs.done:
		return cs.config.Load().(*cancelConfig)
	default:
		return nil
	}
}

func wrapModelForCancelable(m model.BaseChatModel, cs *cancelSig) model.BaseChatModel {
	return &cancelableChatModel{
		inner: m,
		cs:    cs,
	}
}

type cancelableChatModel struct {
	inner model.BaseChatModel
	cs    *cancelSig
}

func (c *cancelableChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if cfg := checkCancelSig(c.cs); cfg != nil && cfg.Mode == CancelImmediate {
		return nil, compose.Interrupt(ctx, "cancelled externally")
	}
	type resultPair struct {
		res *schema.Message
		err error
	}
	resultCh := make(chan *resultPair, 1)
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				resultCh <- &resultPair{nil, e}
			}
		}()

		res, err := c.inner.Generate(ctx, input, opts...)
		resultCh <- &resultPair{res: res, err: err}
	}()

	var timeCh <-chan time.Time
	select {
	case <-c.cs.done:
		cfg := c.cs.config.Load().(*cancelConfig)
		if cfg.Mode == CancelImmediate {
			if cfg.Timeout == nil {
				return nil, compose.Interrupt(ctx, "cancelled externally")
			}
			timeCh = time.After(*cfg.Timeout)
		}
	case res := <-resultCh:
		return res.res, res.err
	}
	select {
	case <-timeCh:
		return nil, compose.Interrupt(ctx, "cancelled externally")
	case res := <-resultCh:
		return res.res, res.err
	}
}

func (c *cancelableChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("implement me")
}
