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
	"reflect"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
)

// AgentContext contains runtime information passed to handlers before each agent run.
// Handlers can modify Instruction, Tools, and ReturnDirectly to customize agent behavior.
type AgentContext struct {
	Instruction    string
	Tools          []tool.BaseTool
	ReturnDirectly map[string]struct{}
}

// HandlerMiddleware defines the interface for customizing agent behavior.
//
// Why HandlerMiddleware instead of AgentMiddleware?
//
// AgentMiddleware is a struct type, which has inherent limitations:
//   - Struct types are closed: users cannot add new methods to extend functionality
//   - The framework only recognizes AgentMiddleware's fixed fields, so even if users
//     embed AgentMiddleware in a custom struct and add methods, the framework cannot
//     call those methods (config.Middlewares is []AgentMiddleware, not a user type)
//   - Callbacks in AgentMiddleware only return error, cannot return modified context
//
// HandlerMiddleware is an interface type, which is open for extension:
//   - Users can implement custom handlers with arbitrary internal state and methods
//   - Hook methods return (context.Context, ..., error) for direct context propagation
//   - Wrapper methods (WrapTool, WrapModel) enable context propagation through the
//     wrapped endpoint chain: wrappers can pass modified context to the next wrapper
//   - Configuration is centralized in struct fields rather than scattered in closures
//
// HandlerMiddleware vs AgentMiddleware:
//   - Use AgentMiddleware for simple, static additions (extra instruction/tools)
//   - Use HandlerMiddleware for dynamic behavior, context modification, or call wrapping
//   - AgentMiddleware is kept for backward compatibility with existing users
//   - Both can be used together; see AgentMiddleware documentation for execution order
//
// Use *BaseHandlerMiddleware as an embedded struct to provide default no-op
// implementations for all methods.
type HandlerMiddleware interface {
	// BeforeAgent is called before each agent run, allowing modification of
	// the agent's instruction and tools configuration.
	BeforeAgent(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error)

	// BeforeModelRewriteHistory is called before each model invocation.
	// The returned messages are persisted to the agent's internal state and passed to the model.
	// The returned context is propagated to the model call and subsequent handlers.
	BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)

	// AfterModelRewriteHistory is called after each model invocation.
	// The input messages include the model's response as the last message.
	// The returned messages are persisted to the agent's internal state.
	AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)

	// WrapTool wraps a tool with custom behavior.
	// Return the input tool unchanged if no wrapping is needed.
	// Called at construction time (or after BeforeAgent if tools are modified dynamically).
	WrapTool(ctx context.Context, t tool.BaseTool) (tool.BaseTool, error)

	// WrapModel wraps a chat model with custom behavior.
	// Return the input model unchanged if no wrapping is needed.
	// Called once when the agent is built, not per-call.
	//
	// Note: The parameter is BaseChatModel (not ToolCallingChatModel) because wrappers
	// only need to intercept Generate/Stream calls. Tool binding (WithTools) is handled
	// separately by the framework and does not flow through user wrappers.
	WrapModel(ctx context.Context, m model.BaseChatModel) (model.BaseChatModel, error)
}

// BaseHandlerMiddleware provides default no-op implementations for HandlerMiddleware.
// Embed *BaseHandlerMiddleware in custom handlers to only override the methods you need.
//
// Example:
//
//	type MyHandler struct {
//		*adk.BaseHandlerMiddleware
//		// custom fields
//	}
//
//	func (h *MyHandler) BeforeModelRewriteHistory(ctx context.Context, messages []adk.Message) (context.Context, []adk.Message, error) {
//		// custom logic
//		return ctx, messages, nil
//	}
type BaseHandlerMiddleware struct{}

func (b *BaseHandlerMiddleware) WrapTool(_ context.Context, t tool.BaseTool) (tool.BaseTool, error) {
	return t, nil
}

func (b *BaseHandlerMiddleware) WrapModel(_ context.Context, m model.BaseChatModel) (model.BaseChatModel, error) {
	return m, nil
}

func (b *BaseHandlerMiddleware) BeforeAgent(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
	return ctx, runCtx, nil
}

func (b *BaseHandlerMiddleware) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

func (b *BaseHandlerMiddleware) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

type handlerInfo struct {
	handler                      HandlerMiddleware
	hasBeforeAgent               bool
	hasBeforeModelRewriteHistory bool
	hasAfterModelRewriteHistory  bool
	hasWrapTool                  bool
	hasWrapModel                 bool
}

var baseHandlerMiddlewareType = reflect.TypeOf(&BaseHandlerMiddleware{})

func isMethodOverridden(handler HandlerMiddleware, methodName string) bool {
	handlerType := reflect.TypeOf(handler)
	handlerMethod, ok1 := handlerType.MethodByName(methodName)
	baseMethod, ok2 := baseHandlerMiddlewareType.MethodByName(methodName)
	if !ok1 || !ok2 {
		return true
	}
	return handlerMethod.Func.Pointer() != baseMethod.Func.Pointer()
}

func newHandlerInfo(h HandlerMiddleware) handlerInfo {
	return handlerInfo{
		handler:                      h,
		hasBeforeAgent:               isMethodOverridden(h, "BeforeAgent"),
		hasBeforeModelRewriteHistory: isMethodOverridden(h, "BeforeModelRewriteHistory"),
		hasAfterModelRewriteHistory:  isMethodOverridden(h, "AfterModelRewriteHistory"),
		hasWrapTool:                  isMethodOverridden(h, "WrapTool"),
		hasWrapModel:                 isMethodOverridden(h, "WrapModel"),
	}
}
