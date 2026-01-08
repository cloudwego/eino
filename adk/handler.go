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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Type aliases for tool call types.
// These provide clearer names while maintaining compatibility with compose package.
type (
	// ToolCall contains information about a tool call.
	// When modifying in wrappers:
	//   - Name: should not be modified
	//   - CallID: should not be modified
	//   - Arguments: may be modified
	//   - CallOptions: may be modified
	ToolCall = compose.ToolInput

	// ToolResult contains the result of a non-streaming tool call.
	ToolResult = compose.ToolOutput

	// StreamToolResult contains the result of a streaming tool call.
	StreamToolResult = compose.StreamToolOutput
)

// AgentRunContext contains runtime information passed to handlers before each agent run.
// Handlers can modify Instruction, Tools, and ReturnDirectly to customize agent behavior.
type AgentRunContext struct {
	Instruction    string
	Tools          []tool.BaseTool
	ReturnDirectly map[string]struct{}
}

// ToolCallWrapper wraps tool call execution.
// Implementations should call next() to execute the actual tool,
// and can modify the call or result as needed.
type ToolCallWrapper interface {
	// WrapInvoke wraps non-streaming tool calls.
	// Call next(ctx, call) to execute the tool.
	WrapInvoke(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*ToolResult, error)) (*ToolResult, error)

	// WrapStream wraps streaming tool calls.
	// Call next(ctx, call) to execute the tool.
	WrapStream(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*StreamToolResult, error)) (*StreamToolResult, error)
}

// BaseToolCallWrapper provides pass-through implementations for ToolCallWrapper.
// Embed this struct in custom wrappers to only override the methods you need.
type BaseToolCallWrapper struct{}

func (h BaseToolCallWrapper) WrapInvoke(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*ToolResult, error)) (*ToolResult, error) {
	return next(ctx, call)
}

func (h BaseToolCallWrapper) WrapStream(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*StreamToolResult, error)) (*StreamToolResult, error) {
	return next(ctx, call)
}

type ModelCall struct {
	Messages []*schema.Message
	Options  []model.Option
}

type ModelResult struct {
	Message *schema.Message
}

type StreamModelResult struct {
	Stream *schema.StreamReader[*schema.Message]
}

type ModelCallWrapper interface {
	WrapGenerate(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*ModelResult, error)) (*ModelResult, error)
	WrapStream(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*StreamModelResult, error)) (*StreamModelResult, error)
}

type BaseModelCallWrapper struct{}

func (h BaseModelCallWrapper) WrapGenerate(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*ModelResult, error)) (*ModelResult, error) {
	return next(ctx, call)
}

func (h BaseModelCallWrapper) WrapStream(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*StreamModelResult, error)) (*StreamModelResult, error) {
	return next(ctx, call)
}

// AgentHandler defines the interface for customizing agent behavior.
// Implementations can modify agent configuration, rewrite message history,
// and wrap tool calls with custom logic.
//
// Use BaseAgentHandler as an embedded struct to provide default no-op
// implementations for all methods.
type AgentHandler interface {
	// BeforeAgent is called before each agent run, allowing modification of
	// the agent's instruction and tools configuration.
	BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error)

	// BeforeModelRewriteHistory is called before each model invocation.
	// The returned messages are persisted to the agent's internal state.
	BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)

	// AfterModelRewriteHistory is called after each model invocation.
	// The returned messages are persisted to the agent's internal state.
	AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)

	// GetToolCallWrapper returns a wrapper for tool calls.
	// Return nil if no tool call wrapping is needed.
	GetToolCallWrapper() ToolCallWrapper

	// GetModelCallWrapper returns a wrapper for model calls.
	// Return nil if no model call wrapping is needed.
	GetModelCallWrapper() ModelCallWrapper
}

// BaseAgentHandler provides default no-op implementations for AgentHandler.
// Embed this struct in custom handlers to only override the methods you need.
type BaseAgentHandler struct{}

func (b BaseAgentHandler) BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
	return ctx, runCtx, nil
}

func (b BaseAgentHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

func (b BaseAgentHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

func (b BaseAgentHandler) GetToolCallWrapper() ToolCallWrapper {
	return nil
}

func (b BaseAgentHandler) GetModelCallWrapper() ModelCallWrapper {
	return nil
}
