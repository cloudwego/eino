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
	"encoding/json"
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

// EventFromMessage wraps a message or stream into an AgentEvent with role metadata.
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

type TransferToAgentAction struct {
	DestAgentName string
}

type AgentOutput struct {
	MessageOutput *MessageVariant

	CustomizedOutput any
}

// NewTransferToAgentAction creates an action to transfer to the specified agent.
func NewTransferToAgentAction(destAgentName string) *AgentAction {
	return &AgentAction{TransferToAgent: &TransferToAgentAction{DestAgentName: destAgentName}}
}

// NewExitAction creates an action that signals the agent to exit.
func NewExitAction() *AgentAction {
	return &AgentAction{Exit: true}
}

type AgentAction struct {
	// Exit signals that the agent wants to terminate execution.
	//
	// Exit Scoping Rules:
	// - An Exit action terminates execution up to the nearest Runner boundary.
	// - When a sub-agent emits Exit, the parent flowAgent will see the exit event,
	//   but the exit only takes effect if it originated from the same Runner scope.
	// - Exit from a nested Runner (e.g., agent tools with their own Runner) does NOT
	//   terminate the parent Runner's execution.
	//
	// This scoping is determined by comparing the exit event's RunPath (specifically
	// the runnerName in the path) with the current execution context's runnerName.
	// See RunStep documentation for more details on runner boundary marking.
	Exit bool

	Interrupted *InterruptInfo

	TransferToAgent *TransferToAgentAction

	BreakLoop *BreakLoopAction

	CustomizedAction any

	internalInterrupted *core.InterruptSignal
}

// RunStep represents a step in the execution path (RunPath).
// It can represent either an agent step (agentName set) or a runner boundary step (runnerName set).
//
// Runner Boundary Marking:
// The runnerName field marks Runner boundaries in the RunPath. This serves two critical purposes:
//
//  1. Exit Scoping: When an agent emits an Exit action, the framework needs to determine if the exit
//     should propagate up or be contained within the current Runner scope. By comparing the exit event's
//     runnerName with the current context's runnerName, we can distinguish:
//     - Exit from current Runner scope → should terminate the current execution
//     - Exit from nested Runner scope → should NOT terminate the parent execution
//
//  2. RunPath Prepending: When events flow up from nested Runners, the parent's RunPath should be
//     prepended exactly once. The runnerName check (in flowAgent.run) ensures prepending only occurs
//     when crossing a Runner boundary, preventing infinite prepending at each agent level.
//
// CheckpointSchema: persisted via serialization.RunCtx (gob).
type RunStep struct {
	agentName  string
	runnerName string // marks Runner boundary; empty for agent steps
}

func init() {
	schema.RegisterName[[]RunStep]("eino_run_step_list")
}

func (r *RunStep) String() string {
	if r.runnerName != "" {
		return r.runnerName + "(Runner)"
	}
	return r.agentName
}

func (r *RunStep) Equals(r1 RunStep) bool {
	return r.agentName == r1.agentName && r.runnerName == r1.runnerName
}

func (r *RunStep) GobEncode() ([]byte, error) {
	s := &runStepSerialization{AgentName: r.agentName, RunnerName: r.runnerName}
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
	r.runnerName = s.RunnerName
	return nil
}

func (r *RunStep) MarshalJSON() ([]byte, error) {
	if r.runnerName != "" {
		return json.Marshal(r.runnerName + "(Runner)")
	}
	return json.Marshal(r.agentName)
}

type runStepSerialization struct {
	AgentName  string
	RunnerName string
}

// AgentEvent CheckpointSchema: persisted via serialization.RunCtx (gob).
type AgentEvent struct {
	AgentName string

	// RunPath semantics:
	// - The eino framework prepends parent context exactly once: runCtx.RunPath + event.RunPath.
	//   The runCtx.RunPath includes both the Runner step and all ancestor agent steps set by flowAgent,
	//   e.g., [{runnerName: "root"}, {agentName: "supervisor"}, {agentName: "worker"}].
	// - Custom agents should NOT include parent segments; any provided RunPath is treated as relative
	//   child provenance that will be appended to the existing context.
	// - Exact RunPath match against the framework's runCtx.RunPath governs recording to runSession.
	//
	// STRONG RECOMMENDATION: Custom agents should NOT set RunPath themselves unless they fully understand
	// the merge and recording rules. Setting parent or absolute paths can lead to duplicated segments
	// after merge and unexpected non-recording. Prefer leaving RunPath empty and let the framework set
	// context, or append only relative child segments when implementing advanced orchestration.
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
