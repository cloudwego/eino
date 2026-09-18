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

// Package adk provides core agent development kit utilities and types.
package adk

import (
	"context"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

var (
	defaultAgentToolParam = schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"request": {
			Desc:     "request to be processed",
			Required: true,
			Type:     schema.String,
		},
	})
)

const (
	agentToolInterruptStateVersionV1 = 1
	agentToolInterruptStateVersionV2 = 2
)

// agentToolInterruptStateV1 CheckpointSchema: persisted as an interrupt state
// via gob. V1 checkpoints store absolute interrupt addresses.
type agentToolInterruptStateV1 struct {
	Version          int
	BridgeCheckpoint []byte
}

// agentToolInterruptStateV2 CheckpointSchema: persisted as an interrupt state
// via gob. V2 checkpoints store child-local interrupt addresses.
type agentToolInterruptStateV2 struct {
	Version          int
	BridgeCheckpoint []byte
}

func init() {
	schema.RegisterName[*agentToolInterruptStateV1]("_eino_adk_agent_tool_interrupt_state_v1")
	schema.RegisterName[*agentToolInterruptStateV2]("_eino_adk_agent_tool_interrupt_state_v2")
}

type AgentToolOptions struct {
	fullChatHistoryAsInput bool
	agentInputSchema       *schema.ParamsOneOf
}

type AgentToolOption func(*AgentToolOptions)

// WithFullChatHistoryAsInput enables using the full chat history as input.
func WithFullChatHistoryAsInput() AgentToolOption {
	return func(options *AgentToolOptions) {
		options.fullChatHistoryAsInput = true
	}
}

// WithAgentInputSchema sets a custom input schema for the agent tool.
func WithAgentInputSchema(schema *schema.ParamsOneOf) AgentToolOption {
	return func(options *AgentToolOptions) {
		options.agentInputSchema = schema
	}
}

func withAgentToolEnableStreaming(enabled bool) tool.Option {
	return tool.WrapImplSpecificOptFn(func(opt *agentToolOptions) {
		opt.enableStreaming = enabled
	})
}

// NewAgentTool creates a tool that wraps an agent for invocation.
//
// The agent must have a non-empty Name and Description, as they are used as
// the tool's name and description respectively. This is validated when Info()
// is called during tool setup.
//
// Event Streaming:
// When EmitInternalEvents is enabled in ToolsConfig, the agent tool will emit AgentEvent
// from the inner agent to the parent agent's AsyncGenerator, allowing real-time streaming
// of the inner agent's output to the end-user via Runner.
//
// Note that these forwarded events are NOT recorded in the parent agent's runSession.
// They are only emitted to the end-user and have no effect on the parent agent's state
// or checkpoint. The only exception is Interrupted action, which is propagated via
// CompositeInterrupt to enable proper interrupt/resume across agent boundaries.
//
// Action Scoping:
// Actions emitted by the inner agent are scoped to the agent tool boundary:
//   - Interrupted: Propagated via CompositeInterrupt to allow proper interrupt/resume across boundaries
//   - Exit, TransferToAgent, BreakLoop: Ignored outside the agent tool; these actions only affect
//     the inner agent's execution and do not propagate to the parent agent
//
// This scoping ensures that nested agents cannot unexpectedly terminate or transfer control
// of their parent agent's execution flow.
func NewAgentTool(_ context.Context, agent Agent, options ...AgentToolOption) tool.BaseTool {
	opts := &AgentToolOptions{}
	for _, opt := range options {
		opt(opts)
	}

	return &agentTool{
		agent:                  agent,
		fullChatHistoryAsInput: opts.fullChatHistoryAsInput,
		inputSchema:            opts.agentInputSchema,
	}
}

// NewTypedAgentTool creates a new agent tool that wraps a TypedAgent as a tool.BaseTool.
func NewTypedAgentTool[M MessageType](_ context.Context, agent TypedAgent[M], options ...AgentToolOption) tool.BaseTool {
	opts := &AgentToolOptions{}
	for _, opt := range options {
		opt(opts)
	}

	return &typedAgentTool[M]{
		agent:                  agent,
		fullChatHistoryAsInput: opts.fullChatHistoryAsInput,
		inputSchema:            opts.agentInputSchema,
	}
}

type typedAgentTool[M MessageType] struct {
	agent TypedAgent[M]

	fullChatHistoryAsInput bool
	inputSchema            *schema.ParamsOneOf
}

type agentTool = typedAgentTool[*schema.Message]

type agentToolRequest struct {
	Request string `json:"request"`
}

func (at *typedAgentTool[M]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	name := at.agent.Name(ctx)
	if name == "" {
		return nil, errors.New("agent tool requires a non-empty Name")
	}
	desc := at.agent.Description(ctx)
	if desc == "" {
		return nil, errors.New("agent tool requires a non-empty Description")
	}
	param := at.inputSchema
	if param == nil {
		param = defaultAgentToolParam
	}

	return &schema.ToolInfo{
		Name:        name,
		Desc:        desc,
		ParamsOneOf: param,
	}, nil
}

func (at *typedAgentTool[M]) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if cancelCtx := getCancelContext(ctx); cancelCtx != nil {
		cancelCtx.markCheckpointAwareDescendant()
	}

	gen, enableStreaming := getEmitGeneratorAndEnableStreaming[M](opts)
	var ms *bridgeStore
	var iter *AsyncIterator[*TypedAgentEvent[M]]
	var err error
	relativeAddress := true

	wasInterrupted, hasState, state := tool.GetInterruptState[any](ctx)
	if !wasInterrupted {
		ms = newBridgeStore()

		var input []M
		if at.fullChatHistoryAsInput {
			var zero M
			if _, ok := any(zero).(*schema.Message); !ok {
				// fullChatHistoryAsInput is only supported for *schema.Message agents and will not
				// be extended to *schema.AgenticMessage. The chat history format and role semantics
				// differ fundamentally between Message and AgenticMessage, and the history rewriting
				// logic (role attribution, system message filtering, transfer messages) is specific
				// to the Message model.
				return "", fmt.Errorf("fullChatHistoryAsInput is only supported for *schema.Message agents")
			}
			msgInput, histErr := getReactChatHistory(ctx, at.agent.Name(ctx))
			if histErr != nil {
				return "", histErr
			}
			input = any(msgInput).([]M)
		} else {
			if at.inputSchema == nil {
				req := &agentToolRequest{}
				err = sonic.UnmarshalString(argumentsInJSON, req)
				if err != nil {
					return "", err
				}
				argumentsInJSON = req.Request
			}
			input = newTypedUserMessages[M](argumentsInJSON)
		}

		runner := newTypedInvokableAgentToolRunner(at.agent, ms, enableStreaming)
		childCtx := core.NewResumeScope(core.ClearCurrentAddress(ctx), nil)
		iter = runner.Run(childCtx, input,
			append(extractAndDeriveAgentToolCancelCtx(ctx, at.agent.Name(ctx), opts), WithCheckPointID(bridgeCheckpointID), withSharedParentSession())...)
	} else {
		if !hasState {
			return "", fmt.Errorf("agent tool '%s' interrupt has happened, but cannot find interrupt state", at.agent.Name(ctx))
		}

		bridgeCheckpoint, usesRelativeAddress, stateErr := decodeAgentToolInterruptState(
			state, at.agent.Name(ctx))
		if stateErr != nil {
			return "", stateErr
		}
		relativeAddress = usesRelativeAddress
		ms = newResumeBridgeStore(bridgeCheckpointID, bridgeCheckpoint)

		agentOpts := extractAndDeriveAgentToolCancelCtx(ctx, at.agent.Name(ctx), opts)
		agentOpts = append(agentOpts, withSharedParentSession())

		runner := newTypedInvokableAgentToolRunner(at.agent, ms, enableStreaming)
		childCtx := ctx
		if relativeAddress {
			childCtx = core.ClearCurrentAddress(ctx)
			iter, err = runner.resumeInNewScope(childCtx, bridgeCheckpointID, agentOpts...)
		} else {
			iter, err = runner.Resume(childCtx, bridgeCheckpointID, agentOpts...)
		}
		if err != nil {
			return "", err
		}
	}

	var lastEvent *TypedAgentEvent[M]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		if lastEvent != nil &&
			lastEvent.Output != nil &&
			lastEvent.Output.MessageOutput != nil &&
			lastEvent.Output.MessageOutput.MessageStream != nil {
			lastEvent.Output.MessageOutput.MessageStream.Close()
		}

		if event.Err != nil {
			return "", event.Err
		}

		if gen != nil {
			if event.Action == nil || event.Action.Interrupted == nil {
				if parentRunCtx := getRunCtx(ctx); parentRunCtx != nil && len(parentRunCtx.RunPath) > 0 {
					rp := make([]RunStep, 0, len(parentRunCtx.RunPath)+len(event.RunPath))
					rp = append(rp, parentRunCtx.RunPath...)
					rp = append(rp, event.RunPath...)
					event.RunPath = rp
				}
				tmp := copyTypedAgentEvent(event)
				gen.Send(event)
				event = tmp
			}
		}

		lastEvent = event
	}

	if lastEvent != nil && lastEvent.Action != nil && lastEvent.Action.Interrupted != nil {
		data, existed, err_ := ms.Get(ctx, bridgeCheckpointID)
		if err_ != nil {
			return "", fmt.Errorf("failed to get interrupt info: %w", err_)
		}
		if !existed {
			return "", fmt.Errorf("interrupt has happened, but cannot find interrupt info")
		}

		var state any = &agentToolInterruptStateV1{
			Version:          agentToolInterruptStateVersionV1,
			BridgeCheckpoint: data,
		}
		if relativeAddress {
			state = &agentToolInterruptStateV2{
				Version:          agentToolInterruptStateVersionV2,
				BridgeCheckpoint: data,
			}
		}
		interruptContexts := lastEvent.Action.Interrupted.InterruptContexts
		if relativeAddress {
			interruptContexts = prependInterruptContextAddresses(
				interruptContexts, core.GetCurrentAddress(ctx))
		}
		subInterrupt := FromInterruptContexts(interruptContexts)
		interruptErr := tool.CompositeInterrupt(ctx, "agent tool interrupt", state, subInterrupt)
		signal := &core.InterruptSignal{}
		if errors.As(interruptErr, &signal) {
			core.MarkInterruptPersistenceBoundary(signal)
		}
		return "", interruptErr
	}

	if lastEvent == nil {
		return "", errors.New("no event returned")
	}

	var ret string
	if lastEvent.Output != nil {
		if output := lastEvent.Output.MessageOutput; output != nil {
			msg, err := output.GetMessage()
			if err != nil {
				return "", err
			}
			ret = extractTextContent(msg)
		}
	}

	return ret, nil
}

func decodeAgentToolInterruptState(state any, agentName string) ([]byte, bool, error) {
	var bridgeCheckpoint []byte
	var relativeAddress bool
	switch state := state.(type) {
	case []byte:
		bridgeCheckpoint = state
	case *agentToolInterruptStateV1:
		if state == nil || state.Version != agentToolInterruptStateVersionV1 {
			return nil, false, fmt.Errorf("agent tool '%s' has unsupported interrupt state version", agentName)
		}
		bridgeCheckpoint = state.BridgeCheckpoint
	case *agentToolInterruptStateV2:
		if state == nil || state.Version != agentToolInterruptStateVersionV2 {
			return nil, false, fmt.Errorf("agent tool '%s' has unsupported interrupt state version", agentName)
		}
		bridgeCheckpoint = state.BridgeCheckpoint
		relativeAddress = true
	default:
		return nil, false, fmt.Errorf("agent tool '%s' has invalid interrupt state type %T", agentName, state)
	}
	if len(bridgeCheckpoint) == 0 {
		return nil, false, fmt.Errorf("agent tool '%s' interrupt state has empty bridge checkpoint", agentName)
	}
	return bridgeCheckpoint, relativeAddress, nil
}

func prependInterruptContextAddresses(contexts []*InterruptCtx, prefix Address) []*InterruptCtx {
	if len(prefix) == 0 {
		return contexts
	}
	cloned := make(map[*InterruptCtx]*InterruptCtx)
	var prepend func(*InterruptCtx) *InterruptCtx
	prepend = func(interruptCtx *InterruptCtx) *InterruptCtx {
		if interruptCtx == nil {
			return nil
		}
		if existing, ok := cloned[interruptCtx]; ok {
			return existing
		}
		copied := *interruptCtx
		copied.Address = make(Address, 0, len(prefix)+len(interruptCtx.Address))
		copied.Address = append(copied.Address, prefix...)
		copied.Address = append(copied.Address, interruptCtx.Address...)
		cloned[interruptCtx] = &copied
		copied.Parent = prepend(interruptCtx.Parent)
		return &copied
	}

	result := make([]*InterruptCtx, len(contexts))
	for i, interruptCtx := range contexts {
		result[i] = prepend(interruptCtx)
	}
	return result
}

// agentToolOptions is a wrapper structure used to convert AgentRunOption slices to tool.Option.
// It stores the agent name and corresponding run options for tool-specific processing.
type agentToolOptions struct {
	agentName       string
	opts            []AgentRunOption
	enableStreaming bool
}

// typedAgentToolEventOptions carries the parent runner's event generator for a
// specific message type. This keeps forwarded internal events type-compatible
// with the parent event stream.
type typedAgentToolEventOptions[M MessageType] struct {
	generator *AsyncGenerator[*TypedAgentEvent[M]]
}

func withAgentToolOptions(agentName string, opts []AgentRunOption) tool.Option {
	return tool.WrapImplSpecificOptFn(func(opt *agentToolOptions) {
		opt.agentName = agentName
		opt.opts = opts
	})
}

func withAgentToolEventGenerator(gen *AsyncGenerator[*AgentEvent]) tool.Option {
	return withTypedAgentToolEventGenerator(gen)
}

func withTypedAgentToolEventGenerator[M MessageType](gen *AsyncGenerator[*TypedAgentEvent[M]]) tool.Option {
	return tool.WrapImplSpecificOptFn(func(o *typedAgentToolEventOptions[M]) {
		o.generator = gen
	})
}

func getOptionsByAgentName(agentName string, opts []tool.Option) []AgentRunOption {
	var ret []AgentRunOption
	for _, opt := range opts {
		o := tool.GetImplSpecificOptions[agentToolOptions](nil, opt)
		if o != nil && o.agentName == agentName {
			ret = append(ret, o.opts...)
		}
	}
	return ret
}

func extractAndDeriveAgentToolCancelCtx(ctx context.Context, agentName string, opts []tool.Option) []AgentRunOption {
	agentOpts := getOptionsByAgentName(agentName, opts)
	childCancelCtx := deriveCheckpointAwareSubAgentCancelContext(ctx, agentOpts)
	return appendCancelContextOption(agentOpts, childCancelCtx)
}

func getEmitGeneratorAndEnableStreaming[M MessageType](opts []tool.Option) (*AsyncGenerator[*TypedAgentEvent[M]], bool) {
	o := tool.GetImplSpecificOptions[agentToolOptions](nil, opts...)
	eventOptions := tool.GetImplSpecificOptions[typedAgentToolEventOptions[M]](nil, opts...)
	if o == nil && eventOptions == nil {
		return nil, false
	}

	var gen *AsyncGenerator[*TypedAgentEvent[M]]
	if eventOptions != nil {
		gen = eventOptions.generator
	}

	var enableStreaming bool
	if o != nil {
		enableStreaming = o.enableStreaming
	}

	return gen, enableStreaming
}

func getReactChatHistory(ctx context.Context, destAgentName string) ([]Message, error) {
	var messages []Message
	err := compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		if len(st.Messages) == 0 {
			return nil
		}
		messages = make([]Message, len(st.Messages)-1)
		copy(messages, st.Messages[:len(st.Messages)-1])
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get chat history from state: %w", err)
	}

	var agentName string
	if runCtx := getRunCtx(ctx); runCtx != nil && len(runCtx.RunPath) > 0 {
		agentName = runCtx.RunPath[len(runCtx.RunPath)-1].agentName
	}

	a, t := GenTransferMessages(ctx, destAgentName)
	messages = append(messages, a, t)
	history := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == schema.System {
			continue
		}

		if msg.Role == schema.Assistant || msg.Role == schema.Tool {
			msg = rewriteMessage(msg, agentName)
		}

		history = append(history, msg)
	}

	return history, nil
}

func newTypedUserMessages[M MessageType](text string) []M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any([]Message{schema.UserMessage(text)}).([]M)
	case *schema.AgenticMessage:
		return any([]*schema.AgenticMessage{schema.UserAgenticMessage(text)}).([]M)
	default:
		return nil
	}
}

func newTypedInvokableAgentToolRunner[M MessageType](agent TypedAgent[M], store compose.CheckPointStore, enableStreaming bool) *TypedRunner[M] {
	return &TypedRunner[M]{
		a:               agent,
		enableStreaming: enableStreaming,
		store:           store,
	}
}
