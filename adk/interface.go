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

	"github.com/cloudwego/eino/schema"
)

type Message = *schema.Message
type MessageStream = *schema.StreamReader[Message]

type MessageVariant struct {
	IsStreaming bool

	Message       Message
	MessageStream MessageStream
}

type messageVariantSerialization struct {
	IsStreaming   bool
	Message       Message
	MessageStream []Message
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
		s.MessageStream = messages
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
	if len(s.MessageStream) > 0 {
		mv.MessageStream = schema.StreamReaderFromArray(s.MessageStream)
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
	ModelResponse *MessageVariant

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
	Exit bool

	Interrupted *InterruptInfo

	TransferToAgent *TransferToAgentAction

	CustomizedAction any
}

type AgentEvent struct {
	AgentName string

	RunPath []string

	Output *AgentOutput

	Action *AgentAction

	Err error
}

func (event *AgentEvent) GetModelOutput() *MessageVariant {
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
	Messages        []Message
	EnableStreaming bool
}

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

type ResumableAgent interface {
	Agent

	Resume(ctx context.Context, info *InterruptInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent]
}
