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

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type Message = *schema.Message
type MessageStream = *schema.StreamReader[Message]

type MessageVariant struct {
	IsStreaming bool

	Message       Message
	MessageStream MessageStream
	// message role: Assistant or Tool
	Role schema.RoleType
	// only used when Role is Tool
	ToolName string
}

func EventFromMessage(msg Message, msgStream MessageStream,
	role schema.RoleType, toolName string) *AgentEvent {
	return &AgentEvent{
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				IsStreaming:   msgStream != nil,
				Message:       msg,
				MessageStream: msgStream,
				Role:          role,
				ToolName:      toolName,
			},
		},
	}
}

type messageVariantSerialization struct {
	IsStreaming   bool
	Message       Message
	MessageStream Message
}

func (mv *MessageVariant) GobEncode() ([]byte, error) {
	s := &messageVariantSerialization{
		IsStreaming: mv.IsStreaming,
		Message:     mv.Message,
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

func (mv *MessageVariant) GobDecode(b []byte) error {
	s := &messageVariantSerialization{}
	err := gob.NewDecoder(bytes.NewReader(b)).Decode(s)
	if err != nil {
		return fmt.Errorf("failed to decoding message variant: %w", err)
	}
	mv.IsStreaming = s.IsStreaming
	mv.Message = s.Message
	if s.MessageStream != nil {
		mv.MessageStream = schema.StreamReaderFromArray([]*schema.Message{s.MessageStream})
	}
	return nil
}

func (mv *MessageVariant) GetMessage() (Message, error) {
	var message Message
	if mv.IsStreaming {
		var err error
		message, err = schema.ConcatMessageStream(mv.MessageStream)
		if err != nil {
			return nil, err
		}
	} else {
		message = mv.Message
	}

	return message, nil
}

// TransferToAgentAction represents a transfer action to a single destination agent.
// This action is used when an agent needs to transfer its execution to another
// specific agent in the hierarchy.
type TransferToAgentAction struct {
	DestAgentName string
}

// ConcurrentTransferToAgentAction represents a concurrent transfer action to multiple destination agents.
// This action enables an agent to transfer its execution to multiple sub-agents
// simultaneously using a fork-join execution model. All destination agents will
// execute concurrently, and their results will be aggregated.
//
// Example usage:
//  action := &AgentAction{
//      ConcurrentTransferToAgent: &ConcurrentTransferToAgentAction{
//          DestAgentNames: []string{"AnalyticsAgent", "ValidationAgent", "EnrichmentAgent"},
//      },
//  }
type ConcurrentTransferToAgentAction struct {
	// DestAgentNames contains the names of all destination agents that should
	// execute concurrently. The framework will handle parallel execution and
	// result aggregation using a fork-join pattern.
	DestAgentNames []string
}

type AgentOutput struct {
	MessageOutput *MessageVariant

	CustomizedOutput any
}

func NewTransferToAgentAction(destAgentName string) *AgentAction {
	return &AgentAction{TransferToAgent: &TransferToAgentAction{DestAgentName: destAgentName}}
}

func NewExitAction() *AgentAction {
	return &AgentAction{Exit: true}
}

// AgentAction represents an action that an agent can take during execution.
// Actions determine the flow of execution and can include transfers, interrupts,
// exits, and custom behaviors.
type AgentAction struct {
	// Exit indicates whether the agent should terminate execution.
	Exit bool

	// Interrupted contains interrupt information when the agent is interrupted.
	Interrupted *InterruptInfo

	// TransferToAgent represents a transfer to a single destination agent.
	TransferToAgent *TransferToAgentAction

	// ConcurrentTransferToAgent represents a concurrent transfer to multiple destination agents.
	// When set, the framework will execute all destination agents simultaneously
	// using a fork-join pattern. This enables parallel processing of complex tasks.
	ConcurrentTransferToAgent *ConcurrentTransferToAgentAction

	// BreakLoop represents a loop-breaking action.
	BreakLoop *BreakLoopAction

	// CustomizedAction allows for custom action implementations.
	CustomizedAction any

	// internalInterrupted is used internally for interrupt signal handling.
	internalInterrupted *core.InterruptSignal
}

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

type AgentEvent struct {
	AgentName string

	RunPath []RunStep

	Output *AgentOutput

	Action *AgentAction

	Err error
}

type AgentInput struct {
	Messages        []Message
	EnableStreaming bool
}

//go:generate  mockgen -destination ../internal/mock/adk/Agent_mock.go --package adk -source interface.go
type Agent interface {
	Name(ctx context.Context) string
	Description(ctx context.Context) string

	// Run runs the agent.
	// The returned AgentEvent within the AsyncIterator must be safe to modify.
	// If the returned AgentEvent within the AsyncIterator contains MessageStream,
	// the MessageStream MUST be exclusive and safe to be received directly.
	// NOTE: it's recommended to use SetAutomaticClose() on the MessageStream of AgentEvents emitted by AsyncIterator,
	// so that even the events are not processed, the MessageStream can still be closed.
	Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent]
}

type OnSubAgents interface {
	OnSetSubAgents(ctx context.Context, subAgents []Agent) error
	OnSetAsSubAgent(ctx context.Context, parent Agent) error

	OnDisallowTransferToParent(ctx context.Context) error
}

type ResumableAgent interface {
	Agent

	Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent]
}
