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
	"fmt"
	"reflect"
	"runtime"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// InvokableToolCallEndpoint is the function signature for invoking a tool synchronously.
// Middleware authors implement wrappers around this endpoint to add custom behavior.
type InvokableToolCallEndpoint func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error)

// StreamableToolCallEndpoint is the function signature for invoking a tool with streaming output.
// Middleware authors implement wrappers around this endpoint to add custom behavior.
type StreamableToolCallEndpoint func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error)

type EnhancedInvokableToolCallEndpoint func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error)

type EnhancedStreamableToolCallEndpoint func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error)

// ToolContext provides metadata about the tool being wrapped.
type ToolContext struct {
	Name   string
	CallID string
}

// ModelContext contains context information passed to WrapModel.
type ModelContext struct {
	// Tools contains the current tool list configured for the agent.
	// This is populated at request time with the tools that will be sent to the model.
	Tools []*schema.ToolInfo

	// ModelRetryConfig contains the retry configuration for the model.
	// This is populated at request time from the agent's ModelRetryConfig.
	// Used by EventSenderModelWrapper to wrap stream errors appropriately.
	ModelRetryConfig *ModelRetryConfig
}

// ChatModelAgentContext contains runtime information passed to handlers before each ChatModelAgent run.
// Handlers can modify Instruction, Tools, and ReturnDirectly to customize agent behavior.
//
// This type is specific to ChatModelAgent. Other agent types may define their own context types.
type ChatModelAgentContext struct {
	// Instruction is the current instruction for the Agent execution.
	// It includes the instruction configured for the agent, additional instructions appended by framework
	// and AgentMiddleware, and modifications applied by previous BeforeAgent handlers.
	// The finalized instruction after all BeforeAgent handlers are then passed to GenModelInput,
	// to be (optionally) formatted with SessionValues and converted to system message.
	Instruction string

	// Tools are the raw tools (without any wrapper or tool middleware) currently configured for the Agent execution.
	// They includes tools passed in AgentConfig, implicit tools added by framework such as transfer / exit tools,
	// and other tools already added by middlewares.
	Tools []tool.BaseTool

	// ReturnDirectly is the set of tool names currently configured to cause the Agent to return directly.
	// This is based on the return directly map configured for the agent, plus any modifications
	// by previous BeforeAgent handlers.
	ReturnDirectly map[string]bool
}

// ChatModelAgentMiddleware defines the interface for customizing ChatModelAgent behavior.
//
// IMPORTANT: This interface is specifically designed for ChatModelAgent and agents built
// on top of it (e.g., DeepAgent).
//
// Why ChatModelAgentMiddleware instead of AgentMiddleware?
//
// AgentMiddleware is a struct type, which has inherent limitations:
//   - Struct types are closed: users cannot add new methods to extend functionality
//   - The framework only recognizes AgentMiddleware's fixed fields, so even if users
//     embed AgentMiddleware in a custom struct and add methods, the framework cannot
//     call those methods (config.Middlewares is []AgentMiddleware, not a user type)
//   - Callbacks in AgentMiddleware only return error, cannot return modified context
//
// ChatModelAgentMiddleware is an interface type, which is open for extension:
//   - Users can implement custom handlers with arbitrary internal state and methods
//   - Hook methods return (context.Context, ..., error) for direct context propagation
//   - Wrapper methods (WrapToolCall, WrapModel) enable context propagation through the
//     wrapped endpoint chain: wrappers can pass modified context to the next wrapper
//   - Configuration is centralized in struct fields rather than scattered in closures
//
// ChatModelAgentMiddleware vs AgentMiddleware:
//   - Use AgentMiddleware for simple, static additions (extra instruction/tools)
//   - Use ChatModelAgentMiddleware for dynamic behavior, context modification, or call wrapping
//   - AgentMiddleware is kept for backward compatibility with existing users
//   - Both can be used together; see AgentMiddleware documentation for execution order
//
// Use *BaseChatModelAgentMiddleware as an embedded struct to provide default no-op
// implementations for all methods.
type ChatModelAgentMiddleware interface {
	// BeforeAgent is called before each agent run, allowing modification of
	// the agent's instruction and tools configuration.
	BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error)

	// BeforeModelRewriteState is called before each model invocation.
	// The returned state is persisted to the agent's internal state and passed to the model.
	// The returned context is propagated to the model call and subsequent handlers.
	//
	// The ChatModelAgentState struct provides access to:
	//   - Messages: the conversation history
	BeforeModelRewriteState(ctx context.Context, state *ChatModelAgentState) (context.Context, *ChatModelAgentState, error)

	// AfterModelRewriteState is called after each model invocation.
	// The input state includes the model's response as the last message.
	// The returned state is persisted to the agent's internal state.
	//
	// The ChatModelAgentState struct provides access to:
	//   - Messages: the conversation history including the model's response
	AfterModelRewriteState(ctx context.Context, state *ChatModelAgentState) (context.Context, *ChatModelAgentState, error)

	// WrapInvokableToolCall wraps a tool's synchronous execution with custom behavior.
	// Return the input endpoint unchanged if no wrapping is needed.
	//
	// This method is only called for tools that implement InvokableTool.
	// If a tool only implements StreamableTool, this method will not be called for that tool.
	//
	// This method is called at request time when the tool is about to be executed.
	// The tCtx parameter provides metadata about the tool:
	//   - Name: The name of the tool being wrapped
	//   - CallID: The unique identifier for this specific tool call
	WrapInvokableToolCall(ctx context.Context, endpoint InvokableToolCallEndpoint, tCtx *ToolContext) InvokableToolCallEndpoint

	// WrapStreamableToolCall wraps a tool's streaming execution with custom behavior.
	// Return the input endpoint unchanged if no wrapping is needed.
	//
	// This method is only called for tools that implement StreamableTool.
	// If a tool only implements InvokableTool, this method will not be called for that tool.
	//
	// This method is called at request time when the tool is about to be executed.
	// The tCtx parameter provides metadata about the tool:
	//   - Name: The name of the tool being wrapped
	//   - CallID: The unique identifier for this specific tool call
	WrapStreamableToolCall(ctx context.Context, endpoint StreamableToolCallEndpoint, tCtx *ToolContext) StreamableToolCallEndpoint

	WrapEnhancedInvokableToolCall(ctx context.Context, endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint

	WrapEnhancedStreamableToolCall(ctx context.Context, endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint

	// WrapModel wraps a chat model with custom behavior.
	// Return the input model unchanged if no wrapping is needed.
	//
	// This method is called at request time when the model is about to be invoked.
	// Note: The parameter is BaseChatModel (not ToolCallingChatModel) because wrappers
	// only need to intercept Generate/Stream calls. Tool binding (WithTools) is handled
	// separately by the framework and does not flow through user wrappers.
	//
	// The mc parameter contains the current tool configuration:
	//   - Tools: The tool infos that will be sent to the model
	WrapModel(ctx context.Context, m model.BaseChatModel, mc *ModelContext) model.BaseChatModel
}

// BaseChatModelAgentMiddleware provides default no-op implementations for ChatModelAgentMiddleware.
// Embed *BaseChatModelAgentMiddleware in custom handlers to only override the methods you need.
//
// Example:
//
//	type MyHandler struct {
//		*adk.BaseChatModelAgentMiddleware
//		// custom fields
//	}
//
//	func (h *MyHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, *adk.ChatModelAgentState, error) {
//		// custom logic
//		return ctx, state, nil
//	}
type BaseChatModelAgentMiddleware struct{}

func (b *BaseChatModelAgentMiddleware) WrapInvokableToolCall(_ context.Context, endpoint InvokableToolCallEndpoint, _ *ToolContext) InvokableToolCallEndpoint {
	return endpoint
}

func (b *BaseChatModelAgentMiddleware) WrapStreamableToolCall(_ context.Context, endpoint StreamableToolCallEndpoint, _ *ToolContext) StreamableToolCallEndpoint {
	return endpoint
}

func (b *BaseChatModelAgentMiddleware) WrapEnhancedInvokableToolCall(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
	return endpoint
}

func (b *BaseChatModelAgentMiddleware) WrapEnhancedStreamableToolCall(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, _ *ToolContext) EnhancedStreamableToolCallEndpoint {
	return endpoint
}

func (b *BaseChatModelAgentMiddleware) WrapModel(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
	return m
}

func (b *BaseChatModelAgentMiddleware) BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
	return ctx, runCtx, nil
}

func (b *BaseChatModelAgentMiddleware) BeforeModelRewriteState(ctx context.Context, state *ChatModelAgentState) (context.Context, *ChatModelAgentState, error) {
	return ctx, state, nil
}

func (b *BaseChatModelAgentMiddleware) AfterModelRewriteState(ctx context.Context, state *ChatModelAgentState) (context.Context, *ChatModelAgentState, error) {
	return ctx, state, nil
}

type handlerInfo struct {
	handler                           ChatModelAgentMiddleware
	hasBeforeAgent                    bool
	hasBeforeModelRewriteState        bool
	hasAfterModelRewriteState         bool
	hasWrapInvokableToolCall          bool
	hasWrapStreamableToolCall         bool
	hasWrapEnhancedInvokableToolCall  bool
	hasWrapEnhancedStreamableToolCall bool
	hasWrapModel                      bool
}

// isMethodOverridden checks if a method is actually defined on the handler type
// (not just promoted from the embedded BaseChatModelAgentMiddleware).
//
// Implementation: Go compiler generates wrapper functions for promoted methods and marks
// their source file as "<autogenerated>". By checking the method's source file location,
// we can distinguish between actually overridden methods and promoted methods.
//
// Why function pointer comparison doesn't work:
// Go compiler creates unique wrapper functions for each promoted method, so function
// pointers are always different even for non-overridden methods.
//
// Stability notes:
// - This behavior is defined in Go compiler source (cmd/compile/internal/gc/main.go)
// - It has been stable since Go 1.0 and is relied upon by Go toolchain (pprof, go vet)
// - While not officially documented, the risk of change is low
func isMethodOverridden(handler ChatModelAgentMiddleware, methodName string) bool {
	handlerType := reflect.TypeOf(handler)
	handlerMethod, ok := handlerType.MethodByName(methodName)
	if !ok {
		return false
	}

	funcInfo := runtime.FuncForPC(handlerMethod.Func.Pointer())
	if funcInfo == nil {
		return true
	}

	file, _ := funcInfo.FileLine(handlerMethod.Func.Pointer())
	return file != "<autogenerated>"
}

func newHandlerInfo(h ChatModelAgentMiddleware) handlerInfo {
	return handlerInfo{
		handler:                           h,
		hasBeforeAgent:                    isMethodOverridden(h, "BeforeAgent"),
		hasBeforeModelRewriteState:        isMethodOverridden(h, "BeforeModelRewriteState"),
		hasAfterModelRewriteState:         isMethodOverridden(h, "AfterModelRewriteState"),
		hasWrapInvokableToolCall:          isMethodOverridden(h, "WrapInvokableToolCall"),
		hasWrapStreamableToolCall:         isMethodOverridden(h, "WrapStreamableToolCall"),
		hasWrapEnhancedInvokableToolCall:  isMethodOverridden(h, "WrapEnhancedInvokableToolCall"),
		hasWrapEnhancedStreamableToolCall: isMethodOverridden(h, "WrapEnhancedStreamableToolCall"),
		hasWrapModel:                      isMethodOverridden(h, "WrapModel"),
	}
}

// SetRunLocalValue sets a key-value pair that persists for the duration of the current agent Run() invocation.
// The value is scoped to this specific execution and is not shared across different Run() calls or agent instances.
//
// Values stored here are compatible with interrupt/resume cycles - they will be serialized and restored
// when the agent is resumed. For custom types, you must register them using schema.RegisterName[T]()
// in an init() function to ensure proper serialization.
//
// This function can only be called from within a ChatModelAgentMiddleware during agent execution.
// Returns an error if called outside of an agent execution context.
func SetRunLocalValue(ctx context.Context, key string, value any) error {
	err := compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.extra == nil {
			st.extra = make(map[string]any)
		}
		st.extra[key] = value
		return nil
	})
	if err != nil {
		return fmt.Errorf("SetRunLocalValue failed: must be called within a ChatModelAgent Run() or Resume() execution context: %w", err)
	}
	return nil
}

// GetRunLocalValue retrieves a value that was set during the current agent Run() invocation.
// The value is scoped to this specific execution and is not shared across different Run() calls or agent instances.
//
// Values stored via SetRunLocalValue are compatible with interrupt/resume cycles - they will be serialized
// and restored when the agent is resumed. For custom types, you must register them using schema.RegisterName[T]()
// in an init() function to ensure proper serialization.
//
// This function can only be called from within a ChatModelAgentMiddleware during agent execution.
// Returns the value and true if found, or nil and false if not found or if called outside of an agent execution context.
func GetRunLocalValue(ctx context.Context, key string) (any, bool, error) {
	var val any
	var found bool
	err := compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.extra != nil {
			val, found = st.extra[key]
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("GetRunLocalValue failed: must be called within a ChatModelAgent Run() or Resume() execution context: %w", err)
	}
	return val, found, nil
}

// DeleteRunLocalValue removes a value that was set during the current agent Run() invocation.
//
// This function can only be called from within a ChatModelAgentMiddleware during agent execution.
// Returns an error if called outside of an agent execution context.
func DeleteRunLocalValue(ctx context.Context, key string) error {
	err := compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.extra != nil {
			delete(st.extra, key)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("DeleteRunLocalValue failed: must be called within a ChatModelAgent Run() or Resume() execution context: %w", err)
	}
	return nil
}
