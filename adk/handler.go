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
)

// AgentRunContext contains runtime information passed to handlers before each agent run.
// Handlers can modify Instruction, Tools, and ReturnDirectly to customize agent behavior.
type AgentRunContext struct {
	Instruction    string
	Tools          []tool.BaseTool
	ReturnDirectly map[string]struct{}
}

// AgentHandler defines the interface for customizing agent behavior.
//
// Why AgentHandler instead of AgentMiddleware?
//
// AgentMiddleware is a struct type, which has inherent limitations:
//   - Struct types are closed: users cannot add new methods to extend functionality
//   - The framework only recognizes AgentMiddleware's fixed fields, so even if users
//     embed AgentMiddleware in a custom struct and add methods, the framework cannot
//     call those methods (config.Middlewares is []AgentMiddleware, not a user type)
//   - Callbacks in AgentMiddleware only return error, cannot return modified context
//
// AgentHandler is an interface type, which is open for extension:
//   - Users can implement custom handlers with arbitrary internal state and methods
//   - All methods return (context.Context, ..., error), allowing context propagation
//   - Configuration is centralized in struct fields rather than scattered in closures
//
// AgentHandler vs AgentMiddleware:
//   - Use AgentMiddleware for simple, static additions (extra instruction/tools)
//   - Use AgentHandler for dynamic behavior, context modification, or call wrapping
//   - AgentMiddleware is kept for backward compatibility with existing users
//   - Both can be used together; middlewares are applied first, then handlers
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

	// WrapTool wraps a tool with custom behavior.
	// Return the input tool unchanged if no wrapping is needed.
	// This is converted to compose.ToolMiddleware internally.
	WrapTool(ctx context.Context, t tool.BaseTool) (tool.BaseTool, error)

	// WrapModel wraps a chat model with custom behavior.
	// Return the input model unchanged if no wrapping is needed.
	// This is called once when the agent is built, not per-call.
	WrapModel(ctx context.Context, m model.BaseChatModel) (model.BaseChatModel, error)
}

// BaseAgentHandler provides default no-op implementations for AgentHandler.
// Embed *BaseAgentHandler in custom handlers to only override the methods you need.
type BaseAgentHandler struct{}

func (b *BaseAgentHandler) WrapTool(_ context.Context, t tool.BaseTool) (tool.BaseTool, error) {
	return t, nil
}

func (b *BaseAgentHandler) WrapModel(_ context.Context, m model.BaseChatModel) (model.BaseChatModel, error) {
	return m, nil
}

func (b *BaseAgentHandler) BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
	return ctx, runCtx, nil
}

func (b *BaseAgentHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

func (b *BaseAgentHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}
