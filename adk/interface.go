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

	Msg       Message
	MsgStream MessageStream
}

func (mv *MessageVariant) GetMessage() (Message, error) {
	var msg Message
	if mv.IsStreaming {
		var err error
		msg, err = schema.ConcatMessageStream(mv.MsgStream)
		if err != nil {
			return nil, err
		}
	} else {
		msg = mv.Msg
	}

	return msg, nil
}

type ToolCallOutput struct {
	Name       string
	ToolCallID string

	Response *MessageVariant
}

type ModelOutput struct {
	Response *MessageVariant
}

type TransferToAgentAction struct {
	DestAgentName string
}

type AgentOutput struct {
	ModelResponse *ModelOutput

	ToolCallResponse *ToolCallOutput

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

func (event *AgentEvent) GetModelOutput() *ModelOutput {
	if event.Output == nil {
		return nil
	}

	return event.Output.ModelResponse
}

func (event *AgentEvent) GetToolCallOutput() *ToolCallOutput {
	if event.Output == nil {
		return nil
	}

	return event.Output.ToolCallResponse
}

type AgentInput struct {
	Msgs            []Message
	EnableStreaming bool
}

type AgentRunOption struct{}

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
