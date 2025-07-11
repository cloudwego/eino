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
	"context"

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

func NewTransferToAgentAction(destAgentName string) *AgentAction {
	return &AgentAction{TransferToAgent: &TransferToAgentAction{DestAgentName: destAgentName}}
}

func NewExitAction() *AgentAction {
	return &AgentAction{Exit: true}
}

type AgentAction struct {
	TransferToAgent *TransferToAgentAction

	Exit bool

	CustomizedAction any
}

type AgentEvent struct {
	AgentName string

	RunPath []string

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

	Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent]
}

type OnSubAgents interface {
	OnSetSubAgents(ctx context.Context, subAgents []Agent) error
	OnSetAsSubAgent(ctx context.Context, parent Agent) error

	OnDisallowTransferToParent(ctx context.Context) error
}
