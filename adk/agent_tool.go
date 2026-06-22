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
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
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

// agentToolInterruptState is the JSON-encoded state captured when an AgentTool
// invocation is interrupted. It wraps the bridge checkpoint bytes alongside
// the synthetic child session ID so resume preserves SessionID-based event
// filtering across interrupt/resume.
type agentToolInterruptState struct {
	ChildSessionID   string `json:"child_session_id"`
	BridgeCheckpoint []byte `json:"bridge_checkpoint"`
}

func (at *typedAgentTool[M]) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if cancelCtx := getCancelContext(ctx); cancelCtx != nil {
		cancelCtx.markAgentToolDescendant()
	}

	gen, enableStreaming := getEmitGeneratorAndEnableStreaming[M](opts)
	var ms *bridgeStore
	var iter *AsyncIterator[*TypedAgentEvent[M]]
	var err error

	wasInterrupted, hasState, rawState := tool.GetInterruptState[[]byte](ctx)

	var childSessionID string
	var bridgeCheckpoint []byte

	if !wasInterrupted {
		// First invocation — generate a globally-unique child session ID.
		// Synthetic UUID avoids collisions with model-assigned tool call IDs
		// (which may be reused across turns) and with user-assigned session IDs.
		childSessionID = "agent_tool:" + uuid.NewString()
	} else if !hasState {
		return "", fmt.Errorf("agent tool '%s' interrupt has happened, but cannot find interrupt state", at.agent.Name(ctx))
	} else {
		// Resume — try the JSON envelope (introduced when SessionID-based event
		// filtering landed). If the envelope does not parse or carries no bridge
		// checkpoint, the rawState is from a pre-envelope version: treat the
		// raw bytes as the bridge checkpoint and synthesize a fresh
		// childSessionID. Pre-envelope checkpoints predate session persistence,
		// so the synthesized ID has no parent-session filter to coordinate with.
		var wrapped agentToolInterruptState
		if json.Unmarshal(rawState, &wrapped) == nil && len(wrapped.BridgeCheckpoint) > 0 {
			childSessionID = wrapped.ChildSessionID
			bridgeCheckpoint = wrapped.BridgeCheckpoint
		} else {
			childSessionID = "agent_tool:" + uuid.NewString()
			bridgeCheckpoint = rawState
		}
	}

	if !wasInterrupted {
		ms = newBridgeStore()

		var input []M
		if at.fullChatHistoryAsInput {
			var zero M
			if _, ok := any(zero).(*schema.Message); !ok {
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
		iter = runner.Run(ctx, input,
			append(extractAndDeriveAgentToolCancelCtx(ctx, at.agent.Name(ctx), opts), WithCheckPointID(bridgeCheckpointID), withSharedParentSession())...)
	} else {
		ms = newResumeBridgeStore(bridgeCheckpointID, bridgeCheckpoint)

		agentOpts := extractAndDeriveAgentToolCancelCtx(ctx, at.agent.Name(ctx), opts)
		agentOpts = append(agentOpts, withSharedParentSession())

		runner := newTypedInvokableAgentToolRunner(at.agent, ms, enableStreaming)
		iter, err = runner.Resume(ctx, bridgeCheckpointID, agentOpts...)
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
				// Tag forwarded events with the child session ID so live consumers
				// can distinguish child timeline events and the parent's persistence
				// loop can skip them.
				stampAgentToolSessionEvent(event, childSessionID)
				tmp := CopyTypedAgentEvent(event)
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

		// Wrap bridge checkpoint with childSessionID so resume can recover it.
		wrapped := agentToolInterruptState{
			ChildSessionID:   childSessionID,
			BridgeCheckpoint: data,
		}
		wrappedBytes, mErr := json.Marshal(wrapped)
		if mErr != nil {
			return "", fmt.Errorf("agent_tool: failed to encode interrupt state: %w", mErr)
		}

		return "", tool.CompositeInterrupt(ctx, "agent tool interrupt", wrappedBytes,
			lastEvent.Action.internalInterrupted)
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
	baseOpts := getCommonOptions(nil, agentOpts...)
	parentCtx := baseOpts.cancelCtx
	if parentCtx == nil {
		parentCtx = getCancelContext(ctx)
	}
	if parentCtx != nil {
		parentCtx.markAgentToolDescendant()
		childCtx := parentCtx.deriveAgentToolCancelContext(ctx)
		agentOpts = append(agentOpts, WrapImplSpecificOptFn(func(o *options) {
			o.cancelCtx = childCtx
		}))
	}
	return agentOpts
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

func stampAgentToolSessionEvent[M MessageType](event *TypedAgentEvent[M], childSessionID string) {
	if event == nil || childSessionID == "" {
		return
	}
	if event.EventID == "" {
		event.EventID = uuid.NewString()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = newEventTimestamp()
	}
	if event.SessionEvent == nil {
		event.SessionEvent = &SessionEvent[M]{
			SessionID: childSessionID,
			EventID:   event.EventID,
			Timestamp: event.Timestamp,
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			event.SessionEvent.Kind = SessionEventMessage
		}
		return
	}
	event.SessionEvent.SessionID = childSessionID
	if event.SessionEvent.EventID == "" {
		event.SessionEvent.EventID = event.EventID
	}
	if event.SessionEvent.Timestamp.IsZero() {
		event.SessionEvent.Timestamp = event.Timestamp
	}
}

// newTypedInvokableAgentToolRunner creates a runner for the inner agent without
// SessionEventStore. The child's events are forwarded to the parent's live stream
// (tagged with childSessionID on SessionEvent) and filtered out of the parent's persistence.
// The child's durability relies solely on the bridge checkpoint stored inside
// agentToolInterruptState — there is no independent child session log.
// This may change in the future if AgentTool needs cross-turn context
// continuation or audit-level event logging for the child session.
func newTypedInvokableAgentToolRunner[M MessageType](agent TypedAgent[M], store compose.CheckPointStore, enableStreaming bool) *TypedRunner[M] {
	return &TypedRunner[M]{
		a:               agent,
		enableStreaming: enableStreaming,
		store:           store,
	}
}
