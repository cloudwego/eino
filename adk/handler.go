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

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Handler is the base interface for the new middleware system.
// Implementations can optionally implement capability interfaces
// to provide specific functionality.
//
// Unlike the struct-based AgentMiddleware, Handler allows:
//   - Custom implementations with internal state
//   - Polymorphic composition of different handler types
//   - Easy mocking and testing
//   - Extensibility without modifying existing types
//
// A Handler can implement any combination of capability interfaces:
//   - InstructionModifier: modify the agent's system instruction
//   - ToolsModifier: add, remove, or modify tools
//   - MessageStatePreProcessor: process and persist messages before chat model calls
//   - MessageStatePostProcessor: process and persist messages after chat model calls
//   - InvokableToolCallInterceptor: intercept non-streaming tool calls
//   - StreamableToolCallInterceptor: intercept streaming tool calls
type Handler interface {
	Name() string
}

// InstructionModifier modifies the agent's system instruction.
// Unlike AgentMiddleware.AdditionalInstruction which only appends text,
// this interface allows any transformation: prepend, replace, conditional logic, etc.
type InstructionModifier interface {
	Handler
	ModifyInstruction(ctx context.Context, instruction string) (string, error)
}

// ToolsModifier modifies the agent's tools configuration.
// Unlike AgentMiddleware.AdditionalTools which only adds tools,
// this interface allows adding, removing, modifying tools,
// and controlling which tools return directly.
type ToolsModifier interface {
	Handler
	ModifyTools(ctx context.Context, config *HandlerToolsConfig) error
}

// MessageStatePreProcessor processes and persists message state before each chat model invocation.
// Unlike AgentMiddleware.BeforeChatModel which mutates state via pointer,
// this interface uses a functional style: receive messages, return (possibly modified) messages.
//
// The returned messages are persisted to the agent's internal state and will be visible
// to subsequent iterations of the agent loop.
type MessageStatePreProcessor interface {
	Handler
	PreProcessMessageState(ctx context.Context, messages []Message) ([]Message, error)
}

// MessageStatePostProcessor processes and persists message state after each chat model invocation.
// Unlike AgentMiddleware.AfterChatModel which mutates state via pointer,
// this interface uses a functional style: receive messages, return (possibly modified) messages.
//
// The returned messages are persisted to the agent's internal state and will be visible
// to subsequent iterations of the agent loop.
type MessageStatePostProcessor interface {
	Handler
	PostProcessMessageState(ctx context.Context, messages []Message) ([]Message, error)
}

// InvokableToolCallInterceptor intercepts non-streaming tool calls.
// This provides a simpler API than compose.ToolMiddleware.
//
// Use cases:
//   - Logging tool calls and results
//   - Caching tool results
//   - Transforming arguments before execution
//   - Transforming results after execution
//   - Short-circuiting tool calls with custom results
type InvokableToolCallInterceptor interface {
	Handler
	WrapInvokableToolCall(
		ctx context.Context,
		toolName string,
		arguments string,
		opts []tool.Option,
		next func(ctx context.Context, arguments string, opts []tool.Option) (string, error),
	) (string, error)
}

// StreamableToolCallInterceptor intercepts streaming tool calls.
// This provides a simpler API than compose.ToolMiddleware.
//
// Use cases:
//   - Logging streaming tool calls
//   - Transforming arguments before execution
//   - Wrapping or transforming the result stream
//   - Short-circuiting with custom streams
type StreamableToolCallInterceptor interface {
	Handler
	WrapStreamableToolCall(
		ctx context.Context,
		toolName string,
		arguments string,
		opts []tool.Option,
		next func(ctx context.Context, arguments string, opts []tool.Option) (*schema.StreamReader[string], error),
	) (*schema.StreamReader[string], error)
}
