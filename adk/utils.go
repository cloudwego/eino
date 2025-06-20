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
	"strings"

	"github.com/cloudwego/eino/internal"
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

func NewModelOutputEvent(agentName string, message Message, messageStream MessageStream) *AgentEvent {
	return &AgentEvent{
		AgentName: agentName,
		Output: &AgentOutput{
			ModelResponse: &ModelOutput{
				Response: &MessageVariant{
					IsStreaming:   message == nil,
					Message:       message,
					MessageStream: messageStream,
				},
			},
		},
	}
}

func NewToolOutputEvent(agentName string, toolName string,
	toolCallID string, message Message, messageStream MessageStream) *AgentEvent {

	return &AgentEvent{
		AgentName: agentName,
		Output: &AgentOutput{
			ToolCallResponse: &ToolCallOutput{
				Name:       toolName,
				ToolCallID: toolCallID,
				Response: &MessageVariant{
					IsStreaming:   message == nil,
					Message:       message,
					MessageStream: messageStream,
				},
			},
		},
	}
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
