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
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/schema"
)

type AsyncIterator[T any] struct {
	ch *internal.UnboundedChan[T]
}

func (ai *AsyncIterator[T]) Next() (T, bool) {
	return ai.ch.Receive()
}

type AsyncGenerator[T any] struct {
	ch *internal.UnboundedChan[T]
}

func (ag *AsyncGenerator[T]) Send(v T) {
	ag.ch.Send(v)
}

func (ag *AsyncGenerator[T]) Close() {
	ag.ch.Close()
}

func NewAsyncIteratorPair[T any]() (*AsyncIterator[T], *AsyncGenerator[T]) {
	ch := internal.NewUnboundedChan[T]()
	return &AsyncIterator[T]{ch}, &AsyncGenerator[T]{ch}
}

func copyMap[K comparable, V any](m map[K]V) map[K]V {
	res := make(map[K]V, len(m))
	for k, v := range m {
		res[k] = v
	}
	return res
}

func concatInstructions(instructions ...string) string {
	var sb strings.Builder
	sb.WriteString(instructions[0])
	for i := 1; i < len(instructions); i++ {
		sb.WriteString("\n\n")
		sb.WriteString(instructions[i])
	}

	return sb.String()
}

func GenTransferMessages(_ context.Context, destAgentName string) (Message, Message) {
	toolCallID := uuid.NewString()
	tooCall := schema.ToolCall{ID: toolCallID, Function: schema.FunctionCall{Name: TransferToAgentToolName, Arguments: destAgentName}}
	assistantMessage := schema.AssistantMessage("", []schema.ToolCall{tooCall})
	toolMessage := schema.ToolMessage(transferToAgentToolOutput(destAgentName), toolCallID, schema.WithToolName(TransferToAgentToolName))
	return assistantMessage, toolMessage
}

// set automatic close for event's message stream
func setAutomaticClose(e *AgentEvent) {
	if e.Output == nil || e.Output.MessageOutput == nil || !e.Output.MessageOutput.IsStreaming {
		return
	}

	e.Output.MessageOutput.MessageStream.SetAutomaticClose()
}

// getMessageFromWrappedEvent extracts the message from an AgentEvent.
// If the stream contains an error chunk, this function returns (nil, err) and
// sets StreamErr to prevent re-consumption. The nil message ensures that
// failed stream responses are not included in subsequent agents' context windows.
func getMessageFromWrappedEvent(e *agentEventWrapper) (Message, error) {
	if e.AgentEvent.Output == nil || e.AgentEvent.Output.MessageOutput == nil {
		return nil, nil
	}

	if !e.AgentEvent.Output.MessageOutput.IsStreaming {
		return e.AgentEvent.Output.MessageOutput.Message, nil
	}

	if e.concatenatedMessage != nil {
		return e.concatenatedMessage, nil
	}

	if e.StreamErr != nil {
		return nil, e.StreamErr
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.concatenatedMessage != nil {
		return e.concatenatedMessage, nil
	}

	var (
		msgs []Message
		s    = e.AgentEvent.Output.MessageOutput.MessageStream
	)

	defer s.Close()
	for {
		msg, err := s.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			e.StreamErr = err
			// Replace the stream with successfully received messages only (no error at the end).
			// The error is preserved in StreamErr for users to check.
			// We intentionally exclude the error from the new stream to ensure gob encoding
			// compatibility, as the stream may be consumed during serialization.
			e.AgentEvent.Output.MessageOutput.MessageStream = schema.StreamReaderFromArray(msgs)
			return nil, err
		}

		msgs = append(msgs, msg)
	}

	if len(msgs) == 0 {
		return nil, errors.New("no messages in MessageVariant.MessageStream")
	}

	if len(msgs) == 1 {
		e.concatenatedMessage = msgs[0]
	} else {
		var err error
		e.concatenatedMessage, err = schema.ConcatMessages(msgs)
		if err != nil {
			e.StreamErr = err
			e.AgentEvent.Output.MessageOutput.MessageStream = schema.StreamReaderFromArray(msgs)
			return nil, err
		}
	}

	return e.concatenatedMessage, nil
}

// copyAgentEvent copies an AgentEvent.
// If the MessageVariant is streaming, the MessageStream will be copied.
// RunPath will be deep copied.
// The result of Copy will be a new AgentEvent that is:
// - safe to set fields of AgentEvent
// - safe to extend RunPath
// - safe to receive from MessageStream
// NOTE: even if the AgentEvent is copied, it's still not recommended to modify
// the Message itself or Chunks of the MessageStream, as they are not copied.
// NOTE: if you have CustomizedOutput or CustomizedAction, they are NOT copied.
func copyAgentEvent(ae *AgentEvent) *AgentEvent {
	rp := make([]RunStep, len(ae.RunPath))
	copy(rp, ae.RunPath)

	copied := &AgentEvent{
		AgentName: ae.AgentName,
		RunPath:   rp,
		Action:    ae.Action,
		Err:       ae.Err,
	}

	if ae.Output == nil {
		return copied
	}

	copied.Output = &AgentOutput{
		CustomizedOutput: ae.Output.CustomizedOutput,
	}

	mv := ae.Output.MessageOutput
	if mv == nil {
		return copied
	}

	copied.Output.MessageOutput = &MessageVariant{
		IsStreaming: mv.IsStreaming,
		Role:        mv.Role,
		ToolName:    mv.ToolName,
	}
	if mv.IsStreaming {
		sts := ae.Output.MessageOutput.MessageStream.Copy(2)
		mv.MessageStream = sts[0]
		copied.Output.MessageOutput.MessageStream = sts[1]
	} else {
		copied.Output.MessageOutput.Message = mv.Message
	}

	return copied
}

func GetMessage(e *AgentEvent) (Message, *AgentEvent, error) {
	if e.Output == nil || e.Output.MessageOutput == nil {
		return nil, e, nil
	}

	msgOutput := e.Output.MessageOutput
	if msgOutput.IsStreaming {
		ss := msgOutput.MessageStream.Copy(2)
		e.Output.MessageOutput.MessageStream = ss[0]

		msg, err := schema.ConcatMessageStream(ss[1])

		return msg, e, err
	}

	return msgOutput.Message, e, nil
}

func genErrorIter(err error) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	generator.Send(&AgentEvent{Err: err})
	generator.Close()
	return iterator
}
