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

// messageType is the sealed type constraint for message types used in ADK.
// Only *schema.Message and *schema.AgenticMessage satisfy this constraint.
// External packages cannot add new types to this union; all generic functions
// in ADK use exhaustive type switches on these two types.
type messageType interface {
	*schema.Message | *schema.AgenticMessage
}

type Message = *schema.Message
type MessageStream = *schema.StreamReader[Message]

type AgenticMessage = *schema.AgenticMessage
type AgenticMessageStream = *schema.StreamReader[AgenticMessage]

// isNilMessage checks whether a generic message value is nil.
// Direct `msg == nil` does not compile for generic pointer types in Go;
// the canonical workaround is to compare through the `any` interface.
func isNilMessage[M messageType](msg M) bool {
	var zero M
	return any(msg) == any(zero)
}

// TypedMessageVariant represents a message output from an agent event.
// It carries either a complete message or a streaming reader, along with
// metadata describing the event's origin.
//
// Role and ToolName are only meaningful for *schema.Message events. For
// *schema.AgenticMessage events (created via EventFromAgenticMessage), these
// fields are always zero-valued because AgenticMessage carries tool results as
// ContentBlocks within the message itself and does not support agent transfer.
//
// For *schema.Message events, Role and ToolName exist independently of the inner
// Message because in streaming mode (IsStreaming=true, Message=nil), the message
// has not materialized yet and the consumer needs metadata without consuming the stream.
type TypedMessageVariant[M messageType] struct {
	IsStreaming bool

	Message       M
	MessageStream *schema.StreamReader[M]

	// Role indicates the origin of this event within the agent's ReAct loop.
	// Only meaningful for *schema.Message events:
	//   - schema.Assistant: the event carries model output (generation or stream).
	//   - schema.Tool: the event carries a tool execution result.
	// Always zero-valued for *schema.AgenticMessage events; use AgenticRole instead.
	Role schema.RoleType

	// AgenticRole indicates the role of the agentic message (assistant, user, system).
	// Only meaningful for *schema.AgenticMessage events.
	// In streaming mode, this is available before consuming the stream.
	// Always zero-valued for *schema.Message events; use Role instead.
	AgenticRole schema.AgenticRoleType

	// ToolName is the name of the tool that produced this event.
	// Only meaningful for *schema.Message events: non-empty when Role == schema.Tool.
	// In streaming mode, this is the only way to identify the source tool before
	// the stream is consumed.
	// Always empty for *schema.AgenticMessage events.
	ToolName string
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
	AgenticRole   schema.AgenticRoleType
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
		AgenticRole: mv.AgenticRole,
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
	mv.AgenticRole = s.AgenticRole
	mv.ToolName = s.ToolName
	if s.MessageStream != nil {
		mv.MessageStream = schema.StreamReaderFromArray([]*schema.AgenticMessage{s.MessageStream})
	}
	return nil
}

// typedEventFromMessage creates a TypedAgentEvent containing the given message and optional stream.
func typedEventFromMessage[M messageType](msg M, msgStream *schema.StreamReader[M],
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

// typedModelOutputEvent creates a model-output event for the generic path.
// For *schema.Message, Role is set to schema.Assistant.
// For *schema.AgenticMessage, AgenticRole is set to schema.AgenticRoleTypeAssistant.
func typedModelOutputEvent[M messageType](msg M, msgStream *schema.StreamReader[M]) *TypedAgentEvent[M] {
	var role schema.RoleType
	var agenticRole schema.AgenticRoleType
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		role = schema.Assistant
	} else {
		agenticRole = schema.AgenticRoleTypeAssistant
	}
	event := typedEventFromMessage(msg, msgStream, role, "")
	event.Output.MessageOutput.AgenticRole = agenticRole
	return event
}

// EventFromMessage creates an AgentEvent containing the given message and optional stream.
//
// role identifies the origin of this event:
//   - schema.Assistant: model output (generation or stream).
//   - schema.Tool: tool execution result; toolName must be non-empty.
//
// For *schema.AgenticMessage events, use EventFromAgenticMessage instead.
func EventFromMessage(msg Message, msgStream *schema.StreamReader[Message],
	role schema.RoleType, toolName string) *AgentEvent {
	return typedEventFromMessage(msg, msgStream, role, toolName)
}

// EventFromAgenticMessage creates a TypedAgentEvent for the AgenticMessage path.
// Unlike EventFromMessage, it does not require role or toolName parameters because
// AgenticMessage carries tool results as ContentBlocks within the message itself,
// and does not support agent transfer.
//
// agenticRole identifies the role of the message (e.g. schema.AgenticRoleTypeAssistant).
// In streaming mode, the role is available on the event before consuming the stream.
func EventFromAgenticMessage(msg AgenticMessage, msgStream AgenticMessageStream, agenticRole schema.AgenticRoleType) *TypedAgentEvent[AgenticMessage] {
	return &TypedAgentEvent[AgenticMessage]{
		Output: &TypedAgentOutput[AgenticMessage]{
			MessageOutput: &TypedMessageVariant[AgenticMessage]{
				IsStreaming:   msgStream != nil,
				Message:       msg,
				MessageStream: msgStream,
				AgenticRole:   agenticRole,
			},
		},
	}
}

// TransferToAgentAction represents a transfer-to-agent action.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
type TransferToAgentAction struct {
	DestAgentName string
}

type TypedAgentOutput[M messageType] struct {
	MessageOutput *TypedMessageVariant[M]

	CustomizedOutput any
}

type AgentOutput = TypedAgentOutput[*schema.Message]

// NewTransferToAgentAction creates an action to transfer to the specified agent.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
func NewTransferToAgentAction(destAgentName string) *AgentAction {
	return &AgentAction{TransferToAgent: &TransferToAgentAction{DestAgentName: destAgentName}}
}

// NewExitAction creates an action that signals the agent to exit.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
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

// RunStep represents a step in the agent execution path.
// CheckpointSchema: persisted via serialization.RunCtx (gob).
//
// NOT RECOMMENDED: RunStep is mainly relevant for agent transfer and workflow agents,
// which have not proven to be more effective empirically. Consider using ChatModelAgent
// with AgentTool or DeepAgent instead for most multi-agent scenarios.
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
type TypedAgentEvent[M messageType] struct {
	AgentName string

	// RunPath represents the execution path from root agent to the current event source.
	// This field is managed entirely by the framework and cannot be set by end-users.
	//
	// NOT RECOMMENDED: RunPath is mainly relevant for agent transfer and workflow agents,
	// which have not proven to be more effective empirically. For ChatModelAgent with
	// AgentTool or DeepAgent, RunPath is trivial. Consider those patterns instead.
	RunPath []RunStep

	Output *TypedAgentOutput[M]

	Action *AgentAction

	Err error
}

// AgentEvent is the default event type using *schema.Message.
type AgentEvent = TypedAgentEvent[*schema.Message]

type TypedAgentInput[M messageType] struct {
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
type TypedAgent[M messageType] interface {
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

// OnSubAgents is the interface for agents that support sub-agent registration and transfer.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
type OnSubAgents interface {
	OnSetSubAgents(ctx context.Context, subAgents []Agent) error
	OnSetAsSubAgent(ctx context.Context, parent Agent) error

	OnDisallowTransferToParent(ctx context.Context) error
}

type TypedResumableAgent[M messageType] interface {
	TypedAgent[M]

	Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]]
}

type ResumableAgent = TypedResumableAgent[*schema.Message]

func concatMessageStream[M messageType](stream *schema.StreamReader[M]) (M, error) {
	var zero M
	switch s := any(stream).(type) {
	case *schema.StreamReader[*schema.Message]:
		result, err := schema.ConcatMessageStream(s)
		if err != nil {
			return zero, err
		}
		return any(result).(M), nil
	case *schema.StreamReader[*schema.AgenticMessage]:
		defer s.Close()
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
		panic("unreachable: unknown messageType")
	}
}
