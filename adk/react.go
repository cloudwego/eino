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
	"time"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ErrExceedMaxIterations indicates the agent reached the maximum iterations limit.
var ErrExceedMaxIterations = errors.New("exceeds max iterations")

// errReturnDirectlyNotAllowed is returned when a tool asks for a direct return
// but the agent's graph has no direct-return path, so honoring the request is
// impossible. Reporting it beats silently continuing the loop.
var errReturnDirectlyNotAllowed = errors.New("requires the agent to allow returning directly: " +
	"set ToolsConfig.AllowRuntimeReturnDirectly, or list at least one tool in " +
	"ToolsConfig.ReturnDirectly")

type typedState[M MessageType] struct {
	Messages []M
	Extra    map[string]any

	// ToolInfos contains the tool definitions passed to the model via model.WithTools.
	// Managed by the framework and modifiable by BeforeModelRewriteState handlers.
	ToolInfos []*schema.ToolInfo

	// DeferredToolInfos contains tool definitions for server-side deferred retrieval,
	// passed to the model via model.WithDeferredTools. Nil when not in use.
	DeferredToolInfos []*schema.ToolInfo

	// Internal fields below - do not access directly.
	// Kept exported for backward compatibility with existing checkpoints.
	HasReturnDirectly        bool
	ReturnDirectlyToolCallID string
	ToolGenActions           map[string]*AgentAction
	AgentName                string
	RemainingIterations      int
	ReturnDirectlyEvent      *TypedAgentEvent[M]
	RetryAttempt             int
	ToolMsgIDs               map[string]map[string]string // toolName → callID → eino message ID

	// CurrentModelSpanID is the SpanID of the model request span that emitted
	// the most recent assistant message containing tool calls. The tool wrapper
	// snapshots this value into ToolSpansInFlight when emitting a tool_call_start
	// span; the snapshot survives interrupt/resume so the matching tool_call_end
	// span (which may be emitted on a later run) preserves the link.
	CurrentModelSpanID string

	// CurrentAssistantMessageEventID is the SessionEvent EventID of the most
	// recent assistant message that emitted tool calls. Snapshotted into
	// ToolSpansInFlight at start emission time, same lifecycle as CurrentModelSpanID.
	CurrentAssistantMessageEventID string

	// ToolSpansInFlight tracks tool calls whose tool_call_start span has been
	// emitted but whose tool_call_end span has not yet fired (typically because
	// the call is paused on an interrupt awaiting user resume). Keyed by
	// tCtx.CallID. Entries are inserted at start emission, retained across
	// interrupt boundaries, and deleted when the matching end span fires.
	ToolSpansInFlight map[string]*toolSpanInFlight

	// returnDirectlyAllowed records whether this run's graph contains the
	// direct-return path, which is what makes a runtime SetReturnDirectly
	// observable.
	//
	// Deliberately unexported: gob skips unexported fields, so this adds nothing
	// to the checkpoint wire format. It is therefore lost across a resume and is
	// refreshed from live config on every tool iteration.
	returnDirectlyAllowed bool
}

// toolSpanInFlight holds identity for a tool_call_start span that has been
// emitted but whose matching tool_call_end span has not yet fired. The
// typical reason is that the call is paused on a permission interrupt
// awaiting user resume.
//
// The wrapper at typedEventSenderToolWrapper persists one entry per
// tCtx.CallID at start emission. On every subsequent invocation of the
// wrapper for the same CallID (i.e. on resume), the entry is reused so
// that the matching end span carries the same SpanID / StartEventID /
// parent IDs — preserving the temporal semantics that one logical tool
// call corresponds to one logical span pair, even when start and end
// straddle an interrupt boundary.
//
// The entry is deleted when the matching end span is emitted (success,
// hard error, or cancellation). It is NOT deleted on interrupt-shape
// errors; those leave the entry intact for the next resume.
type toolSpanInFlight struct {
	SpanID                  string
	StartEventID            string
	StartedAt               time.Time
	ParentSpanID            string
	AssistantMessageEventID string
}

// State is the internal state of the ChatModelAgent.
//
// Deprecated: State is exported only for checkpoint backward compatibility.
// Do not use it directly.
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

	// backward compatibility when decoding checkpoints created by v0.8.0 - v0.8.3
	gob.Register(&AgentEvent{})
	gob.Register(0)

	schema.RegisterName[*TypedAgentInput[*schema.AgenticMessage]]("_eino_adk_agentic_agent_input")
	schema.RegisterName[*typedAgentEventWrapper[*schema.AgenticMessage]]("_eino_adk_agentic_event_wrapper")
	schema.RegisterName[*[]*typedAgentEventWrapper[*schema.AgenticMessage]]("_eino_adk_agentic_event_wrapper_slice")
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

func (s *typedState[M]) setToolMsgID(toolName, callID, msgID string) {
	if s.ToolMsgIDs == nil {
		s.ToolMsgIDs = make(map[string]map[string]string)
	}
	byCall := s.ToolMsgIDs[toolName]
	if byCall == nil {
		byCall = make(map[string]string)
		s.ToolMsgIDs[toolName] = byCall
	}
	byCall[callID] = msgID
}

func (s *typedState[M]) popToolMsgID(toolName, callID string) string {
	if s.ToolMsgIDs == nil {
		return ""
	}
	byCall := s.ToolMsgIDs[toolName]
	if byCall == nil {
		return ""
	}
	id := byCall[callID]
	delete(byCall, callID)
	if len(byCall) == 0 {
		delete(s.ToolMsgIDs, toolName)
	}
	return id
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

// setReturnDirectlyInState marks callID as the tool call to return directly on
// whichever concrete agent state the run uses.
//
// found reports whether an agent state was reachable at all; allowed reports
// whether that state permits returning directly, in which case the mark was
// written.
//
// The state is generic over the agent's message type and a tool cannot know
// which one it runs under, so both concrete states are attempted.
func setReturnDirectlyInState(ctx context.Context, callID string) (found, allowed bool) {
	mark := func(returnDirectlyAllowed bool, set func()) {
		allowed = returnDirectlyAllowed
		if allowed {
			set()
		}
	}

	if err := compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		mark(st.returnDirectlyAllowed, func() { st.setReturnDirectlyToolCallID(callID) })
		return nil
	}); err == nil {
		return true, allowed
	}

	if err := compose.ProcessState(ctx, func(_ context.Context, st *agenticState) error {
		mark(st.returnDirectlyAllowed, func() { st.setReturnDirectlyToolCallID(callID) })
		return nil
	}); err == nil {
		return true, allowed
	}

	return false, false
}

// SetReturnDirectly signals the ChatModelAgent to stop its ReAct loop once the
// current tool call finishes, and to return that tool call's result as the
// agent's final output.
//
// This is the runtime counterpart of the static ToolsConfig.ReturnDirectly: it
// lets a tool decide from its own execution result whether the loop should stop,
// so the same tool can return directly for some arguments and keep the loop
// running for others.
//
// Where/when to use:
//   - Invoke within a tool's Run (Invokable/Streamable) implementation, after the
//     tool has determined that its own result is the final answer and no further
//     model reasoning is needed.
//   - For a Streamable tool, call it before returning the stream reader.
//
// Prerequisite:
//   - The agent must be able to reach the direct-return path. That holds when at
//     least one tool is listed in ToolsConfig.ReturnDirectly, or when
//     ToolsConfig.AllowRuntimeReturnDirectly is set. Otherwise this returns an
//     error rather than silently doing nothing.
//
// Priority:
//   - Takes priority over ToolsConfig.ReturnDirectly and over
//     ChatModelAgentContext.ReturnDirectly set by a BeforeAgent handler.
//
// Concurrency:
//   - When tool calls run in parallel and more than one requests a direct return
//     in the same iteration, the last request wins.
//
// Limitation:
//   - Only usable within ChatModelAgent runs. It relies on ChatModelAgent's
//     internal state, which is not available in other agent types.
func SetReturnDirectly(ctx context.Context) error {
	callID := compose.GetToolCallID(ctx)
	if callID == "" {
		return errors.New("must be called within a tool call")
	}

	found, allowed := setReturnDirectlyInState(ctx, callID)
	if !found {
		return errors.New("must be called within a ChatModelAgent tool call: " +
			"agent state is unavailable")
	}
	if !allowed {
		return errReturnDirectlyNotAllowed
	}
	return nil
}

type reactInput struct {
	Messages []Message
}

type typedReactConfig[M MessageType] struct {
	model model.BaseModel[M]

	toolsConfig      *compose.ToolsNodeConfig
	modelWrapperConf *typedModelWrapperConfig[M]

	toolsReturnDirectly map[string]bool

	// allowRuntimeReturnDirectly forces the return-direct branch into the graph
	// even when no tool is statically configured in toolsReturnDirectly, so that
	// SetReturnDirectly can take effect at runtime.
	allowRuntimeReturnDirectly bool

	agentName string

	maxIterations int

	cancelCtx *cancelContext

	// afterAgentFunc is called when the agent reaches a successful terminal state.
	// It runs as a graph node, so compose.ProcessState is available.
	afterAgentFunc func(ctx context.Context, msg M) (M, error)
}

// returnDirectlyReachable reports whether the graph should contain the
// direct-return path: either a tool is statically configured to return directly,
// or the agent explicitly allows a tool to decide at runtime.
//
// Agents that do neither keep their original topology, so they pay nothing for
// this feature.
func (c *typedReactConfig[M]) returnDirectlyReachable() bool {
	return len(c.toolsReturnDirectly) > 0 || c.allowRuntimeReturnDirectly
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
			AgentName:             config.agentName,
			returnDirectlyAllowed: config.returnDirectlyReachable(),
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
		afterAgentNode_                = "AfterAgent"
	)

	cancelCtx := config.cancelCtx
	g := compose.NewGraph[*reactInput, Message](compose.WithGenLocalState(genReactState(config)))
	_ = g.AddLambdaNode(initNode_, compose.InvokableLambda(func(ctx context.Context, input *reactInput) ([]Message, error) {
		if input == nil {
			return nil, errors.New("react: init input is nil")
		}
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.Messages = append(st.Messages, input.Messages...)
			return nil
		})
		return input.Messages, nil
	}), compose.WithNodeName(initNode_))

	var wrappedModel = config.model
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
		// Refreshed from live config on every iteration so that a run resumed from
		// a checkpoint written before this field existed still reflects the agent's
		// actual topology.
		st.returnDirectlyAllowed = config.returnDirectlyReachable()
		returnDirectly := config.toolsReturnDirectly
		if execCtx := getTypedChatModelAgentExecCtx[*schema.Message](ctx); execCtx != nil && len(execCtx.runtimeReturnDirectly) > 0 {
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
			getTypedChatModelAgentExecCtx[*schema.Message](ctx).send(ctx, event)
			st.setReturnDirectlyEvent(nil)
		}
		return out, nil
	}
	_ = g.AddToolsNode(toolNode_, toolsNode,
		compose.WithStatePreHandler(toolPreHandle),
		compose.WithStreamStatePostHandler(toolPostHandle),
		compose.WithNodeName(toolNode_))

	// AfterToolCalls node: persists tool results to state and fires the after-tool-calls hook.
	// The graph auto-materializes the ToolsNode stream into []Message before this node.
	afterToolCalls := func(ctx context.Context, toolResults []Message) ([]Message, error) {
		// Propagate tool message IDs from event sender to state messages.
		// The event sender pre-generated IDs and stored them in state.ToolMsgIDs[toolName+callID].
		// Here we pop them and set them on the compose-created tool result messages
		// so that state messages share the same IDs as their corresponding event messages.
		// If no stored ID is found (old checkpoint, custom event sender), generate a fresh one.
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			for _, msg := range toolResults {
				if id := st.popToolMsgID(msg.ToolName, msg.ToolCallID); id != "" {
					msg.Extra = internal.SetMessageID(msg.Extra, id)
				} else {
					msg.Extra = internal.EnsureMessageID(msg.Extra)
				}
				st.Messages = append(st.Messages, msg)
			}
			return nil
		})

		execCtx := getTypedChatModelAgentExecCtx[Message](ctx)
		if execCtx != nil && execCtx.afterToolCallsHook != nil {
			if err := execCtx.afterToolCallsHook(ctx); err != nil {
				return nil, err
			}
		}

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

	// Determine the terminal node: afterAgentNode_ if afterAgentFunc is set, otherwise compose.END.
	terminalNode := compose.END
	if config.afterAgentFunc != nil {
		_ = g.AddLambdaNode(afterAgentNode_, compose.InvokableLambda(config.afterAgentFunc),
			compose.WithNodeName(afterAgentNode_))
		_ = g.AddEdge(afterAgentNode_, compose.END)
		terminalNode = afterAgentNode_
	}

	toolCallCheck := func(ctx context.Context, sMsg MessageStream) (string, error) {
		defer sMsg.Close()
		for {
			chunk, err_ := sMsg.Recv()
			if err_ != nil {
				if err_ == io.EOF {
					return terminalNode, nil
				}

				return "", err_
			}

			if len(chunk.ToolCalls) > 0 {
				return cancelCheckNode_, nil
			}
		}
	}
	branch := compose.NewStreamGraphBranch(toolCallCheck, map[string]bool{terminalNode: true, cancelCheckNode_: true})
	_ = g.AddBranch(chatModel_, branch)

	_ = g.AddEdge(cancelCheckNode_, toolNode_)
	_ = g.AddEdge(toolNode_, afterToolCallsNode_)
	_ = g.AddEdge(afterToolCallsNode_, afterToolCallsCancelCheckNode_)

	if config.returnDirectlyReachable() {
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
		_ = g.AddEdge(toolNodeToEndConverter, terminalNode)

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
			AgentName:             config.agentName,
			returnDirectlyAllowed: config.returnDirectlyReachable(),
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
		afterAgentNode_                = "AfterAgent"
	)

	cancelCtx := config.cancelCtx
	g := compose.NewGraph[*agenticReactInput, *schema.AgenticMessage](
		compose.WithGenLocalState(genAgenticReactState(config)))
	_ = g.AddLambdaNode(initNode_, compose.InvokableLambda(func(ctx context.Context, input *agenticReactInput) ([]*schema.AgenticMessage, error) {
		if input == nil {
			return nil, errors.New("agentic react: init input is nil")
		}
		_ = compose.ProcessState(ctx, func(_ context.Context, st *agenticState) error {
			st.Messages = append(st.Messages, input.Messages...)
			return nil
		})
		return input.Messages, nil
	}), compose.WithNodeName(initNode_))

	var wrappedModel = config.model
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
		// Refreshed from live config on every iteration so that a run resumed from
		// a checkpoint written before this field existed still reflects the agent's
		// actual topology.
		st.returnDirectlyAllowed = config.returnDirectlyReachable()
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
			getTypedChatModelAgentExecCtx[*schema.AgenticMessage](ctx).send(ctx, event)
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
			for _, msg := range toolResults {
				if msg == nil {
					continue
				}
				toolName, callID := extractToolIdentifiers(msg)
				if id := st.popToolMsgID(toolName, callID); id != "" {
					msg.Extra = internal.SetMessageID(msg.Extra, id)
				} else {
					msg.Extra = internal.EnsureMessageID(msg.Extra)
				}
				st.Messages = append(st.Messages, msg)
			}
			return nil
		})

		execCtx := getTypedChatModelAgentExecCtx[*schema.AgenticMessage](ctx)
		if execCtx != nil && execCtx.afterToolCallsHook != nil {
			if err := execCtx.afterToolCallsHook(ctx); err != nil {
				return nil, err
			}
		}

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

	// Determine the terminal node: afterAgentNode_ if afterAgentFunc is set, otherwise compose.END.
	terminalNode := compose.END
	if config.afterAgentFunc != nil {
		_ = g.AddLambdaNode(afterAgentNode_, compose.InvokableLambda(config.afterAgentFunc),
			compose.WithNodeName(afterAgentNode_))
		_ = g.AddEdge(afterAgentNode_, compose.END)
		terminalNode = afterAgentNode_
	}

	toolCallCheck := func(ctx context.Context, sMsg *schema.StreamReader[*schema.AgenticMessage]) (string, error) {
		defer sMsg.Close()
		for {
			chunk, err_ := sMsg.Recv()
			if err_ != nil {
				if err_ == io.EOF {
					return terminalNode, nil
				}
				return "", err_
			}
			if agenticMessageHasToolCalls(chunk) {
				return cancelCheckNode_, nil
			}
		}
	}
	branch := compose.NewStreamGraphBranch(toolCallCheck, map[string]bool{terminalNode: true, cancelCheckNode_: true})
	_ = g.AddBranch(chatModel_, branch)

	_ = g.AddEdge(cancelCheckNode_, toolNode_)
	_ = g.AddEdge(toolNode_, afterToolCallsNode_)
	_ = g.AddEdge(afterToolCallsNode_, afterToolCallsCancelCheckNode_)

	if config.returnDirectlyReachable() {
		const (
			toolNodeToEndConverter = "ToolNodeToEndConverter"
		)

		cvt := func(ctx context.Context, toolResults []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
			id, _ := getAgenticReturnDirectlyToolCallID(ctx)
			for _, msg := range toolResults {
				if msg == nil {
					continue
				}
				_, callID := extractToolIdentifiers(msg)
				if callID == id {
					return msg, nil
				}
			}
			return nil, errors.New("return directly tool call result not found")
		}

		_ = g.AddLambdaNode(toolNodeToEndConverter, compose.InvokableLambda(cvt),
			compose.WithNodeName(toolNodeToEndConverter))
		_ = g.AddEdge(toolNodeToEndConverter, terminalNode)

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

// extractToolIdentifiers extracts the tool name and call ID from an AgenticMessage
// that contains a tool result content block.
// Assumes one tool result per message, which is guaranteed by AgenticToolsNode
// (see compose.toolMessageToAgenticMessage).
func extractToolIdentifiers(msg *schema.AgenticMessage) (toolName, callID string) {
	if msg == nil {
		return "", ""
	}
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		if block.Type == schema.ContentBlockTypeFunctionToolResult && block.FunctionToolResult != nil {
			return block.FunctionToolResult.Name, block.FunctionToolResult.CallID
		}
		if block.Type == schema.ContentBlockTypeToolSearchResult && block.ToolSearchFunctionToolResult != nil {
			return block.ToolSearchFunctionToolResult.Name, block.ToolSearchFunctionToolResult.CallID
		}
	}
	return "", ""
}
