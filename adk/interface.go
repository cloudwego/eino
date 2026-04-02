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
	"fmt"
	"io"

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

// ComponentOfAgent is the component type identifier for ADK agents in callbacks.
// Use this to filter callback events to only agent-related events.
const ComponentOfAgent components.Component = "Agent"

// ComponentOfAgenticAgent is the component type identifier for ADK agents
// that use *schema.AgenticMessage in callbacks.
const ComponentOfAgenticAgent components.Component = "AgenticAgent"

// MessageType is the type constraint for message types used in ADK.
type MessageType interface {
	*schema.Message | *schema.AgenticMessage
}

type Message = *schema.Message
type MessageStream = *schema.StreamReader[Message]

type TypedMessageVariant[M MessageType] struct {
	IsStreaming bool

	Message       M
	MessageStream *schema.StreamReader[M]
	Role          schema.RoleType
	ToolName      string
}

func (mv *TypedMessageVariant[M]) GetMessage() (M, error) {
	if mv.IsStreaming {
		return concatMessageStream(mv.MessageStream)
	}
	return mv.Message, nil
}

type MessageVariant = TypedMessageVariant[*schema.Message]

type messageVariantSerialization struct {
	IsStreaming   bool
	Message       Message
	MessageStream Message
	Role          schema.RoleType
	ToolName      string
}

type agenticMessageVariantSerialization struct {
	IsStreaming   bool
	Message       *schema.AgenticMessage
	MessageStream *schema.AgenticMessage
	Role          schema.RoleType
	ToolName      string
}

func (mv *TypedMessageVariant[M]) GobEncode() ([]byte, error) {
	if mvMsg, ok := any(mv).(*TypedMessageVariant[*schema.Message]); ok {
		return gobEncodeMessageVariant(mvMsg)
	}
	if mvAgentic, ok := any(mv).(*TypedMessageVariant[*schema.AgenticMessage]); ok {
		return gobEncodeAgenticMessageVariant(mvAgentic)
	}
	return nil, fmt.Errorf("gob encoding not supported for this message type")
}

func (mv *TypedMessageVariant[M]) GobDecode(b []byte) error {
	if mvMsg, ok := any(mv).(*TypedMessageVariant[*schema.Message]); ok {
		return gobDecodeMessageVariant(mvMsg, b)
	}
	if mvAgentic, ok := any(mv).(*TypedMessageVariant[*schema.AgenticMessage]); ok {
		return gobDecodeAgenticMessageVariant(mvAgentic, b)
	}
	return fmt.Errorf("gob decoding not supported for this message type")
}

func gobEncodeMessageVariant(mv *TypedMessageVariant[*schema.Message]) ([]byte, error) {
	s := &messageVariantSerialization{
		IsStreaming: mv.IsStreaming,
		Message:     mv.Message,
		Role:        mv.Role,
		ToolName:    mv.ToolName,
	}
	if mv.IsStreaming {
		var messages []Message
		for {
			frame, err := mv.MessageStream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("error receiving message stream: %w", err)
			}
			messages = append(messages, frame)
		}
		m, err := schema.ConcatMessages(messages)
		if err != nil {
			return nil, fmt.Errorf("failed to encode message: cannot concat message stream: %w", err)
		}
		s.MessageStream = m
	}
	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode(s)
	if err != nil {
		return nil, fmt.Errorf("failed to gob encode message variant: %w", err)
	}
	return buf.Bytes(), nil
}

func gobDecodeMessageVariant(mv *TypedMessageVariant[*schema.Message], b []byte) error {
	s := &messageVariantSerialization{}
	err := gob.NewDecoder(bytes.NewReader(b)).Decode(s)
	if err != nil {
		return fmt.Errorf("failed to decoding message variant: %w", err)
	}
	mv.IsStreaming = s.IsStreaming
	mv.Message = s.Message
	mv.Role = s.Role
	mv.ToolName = s.ToolName
	if s.MessageStream != nil {
		mv.MessageStream = schema.StreamReaderFromArray([]*schema.Message{s.MessageStream})
	}
	return nil
}

func gobEncodeAgenticMessageVariant(mv *TypedMessageVariant[*schema.AgenticMessage]) ([]byte, error) {
	s := &agenticMessageVariantSerialization{
		IsStreaming: mv.IsStreaming,
		Message:     mv.Message,
		Role:        mv.Role,
		ToolName:    mv.ToolName,
	}
	if mv.IsStreaming {
		var messages []*schema.AgenticMessage
		for {
			frame, err := mv.MessageStream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("error receiving agentic message stream: %w", err)
			}
			messages = append(messages, frame)
		}
		m, err := schema.ConcatAgenticMessages(messages)
		if err != nil {
			return nil, fmt.Errorf("failed to encode agentic message: cannot concat message stream: %w", err)
		}
		s.MessageStream = m
	}
	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode(s)
	if err != nil {
		return nil, fmt.Errorf("failed to gob encode agentic message variant: %w", err)
	}
	return buf.Bytes(), nil
}

func gobDecodeAgenticMessageVariant(mv *TypedMessageVariant[*schema.AgenticMessage], b []byte) error {
	s := &agenticMessageVariantSerialization{}
	err := gob.NewDecoder(bytes.NewReader(b)).Decode(s)
	if err != nil {
		return fmt.Errorf("failed to decode agentic message variant: %w", err)
	}
	mv.IsStreaming = s.IsStreaming
	mv.Message = s.Message
	mv.Role = s.Role
	mv.ToolName = s.ToolName
	if s.MessageStream != nil {
		mv.MessageStream = schema.StreamReaderFromArray([]*schema.AgenticMessage{s.MessageStream})
	}
	return nil
}

// TypedEventFromMessage creates a TypedAgentEvent containing the given message and optional stream.
func TypedEventFromMessage[M MessageType](msg M, msgStream *schema.StreamReader[M],
	role schema.RoleType, toolName string) *TypedAgentEvent[M] {
	return &TypedAgentEvent[M]{
		Output: &TypedAgentOutput[M]{
			MessageOutput: &TypedMessageVariant[M]{
				IsStreaming:   msgStream != nil,
				Message:       msg,
				MessageStream: msgStream,
				Role:          role,
				ToolName:      toolName,
			},
		},
	}
}

// EventFromMessage creates an AgentEvent containing the given message and optional stream.
func EventFromMessage(msg Message, msgStream *schema.StreamReader[Message],
	role schema.RoleType, toolName string) *AgentEvent {
	return TypedEventFromMessage(msg, msgStream, role, toolName)
}

type TransferToAgentAction struct {
	DestAgentName string
}

type TypedAgentOutput[M MessageType] struct {
	MessageOutput *TypedMessageVariant[M]

	CustomizedOutput any
}

type AgentOutput = TypedAgentOutput[*schema.Message]

// NewTransferToAgentAction creates an action to transfer to the specified agent.
func NewTransferToAgentAction(destAgentName string) *AgentAction {
	return &AgentAction{TransferToAgent: &TransferToAgentAction{DestAgentName: destAgentName}}
}

// NewExitAction creates an action that signals the agent to exit.
func NewExitAction() *AgentAction {
	return &AgentAction{Exit: true}
}

// AgentAction represents actions that an agent can emit during execution.
//
// Action Scoping in Agent Tools:
// When an agent is wrapped as an agent tool (via NewAgentTool), actions emitted by the inner agent
// are scoped to the tool boundary:
//   - Interrupted: Propagated via CompositeInterrupt to allow proper interrupt/resume across boundaries
//   - Exit, TransferToAgent, BreakLoop: Ignored outside the agent tool; these actions only affect
//     the inner agent's execution and do not propagate to the parent agent
//
// This scoping ensures that nested agents cannot unexpectedly terminate or transfer control
// of their parent agent's execution flow.
type AgentAction struct {
	Exit bool

	Interrupted *InterruptInfo

	TransferToAgent *TransferToAgentAction

	BreakLoop *BreakLoopAction

	CustomizedAction any

	internalInterrupted *core.InterruptSignal
}

// RunStep CheckpointSchema: persisted via serialization.RunCtx (gob).
type RunStep struct {
	agentName string
}

func init() {
	schema.RegisterName[[]RunStep]("eino_run_step_list")
}

func (r *RunStep) String() string {
	return r.agentName
}

func (r *RunStep) Equals(r1 RunStep) bool {
	return r.agentName == r1.agentName
}

func (r *RunStep) GobEncode() ([]byte, error) {
	s := &runStepSerialization{AgentName: r.agentName}
	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode(s)
	if err != nil {
		return nil, fmt.Errorf("failed to gob encode RunStep: %w", err)
	}
	return buf.Bytes(), nil
}

func (r *RunStep) GobDecode(b []byte) error {
	s := &runStepSerialization{}
	err := gob.NewDecoder(bytes.NewReader(b)).Decode(s)
	if err != nil {
		return fmt.Errorf("failed to gob decode RunStep: %w", err)
	}
	r.agentName = s.AgentName
	return nil
}

type runStepSerialization struct {
	AgentName string
}

// TypedAgentEvent represents a single event emitted during agent execution.
// CheckpointSchema: persisted via serialization.RunCtx (gob).
type TypedAgentEvent[M MessageType] struct {
	AgentName string

	// RunPath represents the execution path from root agent to the current event source.
	// This field is managed entirely by the eino framework and cannot be set by end-users
	// because RunStep's fields are unexported. The framework sets RunPath exactly once:
	// - flowAgent sets it when the event has no RunPath (len == 0)
	// - agentTool prepends parent RunPath when forwarding events from nested agents
	RunPath []RunStep

	Output *TypedAgentOutput[M]

	Action *AgentAction

	Err error
}

// AgentEvent is the default event type using *schema.Message.
type AgentEvent = TypedAgentEvent[*schema.Message]

type TypedAgentInput[M MessageType] struct {
	Messages        []M
	EnableStreaming bool
}

type AgentInput = TypedAgentInput[*schema.Message]

// TypedAgent is the base agent interface parameterized by message type.
//
// For M = *schema.Message, the full ADK feature set is supported (multi-agent
// orchestration, cancel monitoring, retry, flowAgent).
// For M = *schema.AgenticMessage, single-agent execution works but cancel
// monitoring on the model stream and retry are not yet wired.
type TypedAgent[M MessageType] interface {
	Name(ctx context.Context) string
	Description(ctx context.Context) string

	// Run runs the agent.
	// The returned AgentEvent within the AsyncIterator must be safe to modify.
	// If the returned AgentEvent within the AsyncIterator contains MessageStream,
	// the MessageStream MUST be exclusive and safe to be received directly.
	// NOTE: it's recommended to use SetAutomaticClose() on the MessageStream of AgentEvents emitted by AsyncIterator,
	// so that even the events are not processed, the MessageStream can still be closed.
	Run(ctx context.Context, input *TypedAgentInput[M], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]]
}

//go:generate  mockgen -destination ../internal/mock/adk/Agent_mock.go --package adk github.com/cloudwego/eino/adk Agent,ResumableAgent
type Agent = TypedAgent[*schema.Message]

type TypedOnSubAgents[M MessageType] interface {
	OnSetSubAgents(ctx context.Context, subAgents []TypedAgent[M]) error
	OnSetAsSubAgent(ctx context.Context, parent TypedAgent[M]) error

	OnDisallowTransferToParent(ctx context.Context) error
}

// OnSubAgents is the concrete *schema.Message variant.
type OnSubAgents = TypedOnSubAgents[*schema.Message]

type TypedResumableAgent[M MessageType] interface {
	TypedAgent[M]

	Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]]
}

type ResumableAgent = TypedResumableAgent[*schema.Message]

func concatMessageStream[M MessageType](stream *schema.StreamReader[M]) (M, error) {
	var zero M
	switch s := any(stream).(type) {
	case *schema.StreamReader[*schema.Message]:
		result, err := schema.ConcatMessageStream(s)
		if err != nil {
			return zero, err
		}
		return any(result).(M), nil
	case *schema.StreamReader[*schema.AgenticMessage]:
		var msgs []*schema.AgenticMessage
		for {
			frame, err := s.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return zero, err
			}
			msgs = append(msgs, frame)
		}
		result, err := schema.ConcatAgenticMessages(msgs)
		if err != nil {
			return zero, err
		}
		return any(result).(M), nil
	default:
		return zero, fmt.Errorf("unsupported message type for stream concatenation")
	}
}
