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
	"encoding/gob"
	"fmt"
	"io"
	"reflect"

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

// ToolCallsContext contains metadata about the tool calls that just completed.
type ToolCallsContext struct {
	// ToolCalls contains the tool call metadata from the model's response.
	ToolCalls []ToolContext
}

// ModelContext contains context information passed to WrapModel.
type ModelContext struct {
	// Tools contains the current tool list configured for the agent.
	// This is populated at request time with the tools that will be sent to the model.
	//
	// Deprecated: Use TypedChatModelAgentState.ToolInfos in BeforeModelRewriteState instead.
	// ModelContext.Tools remains populated for backward compatibility with existing WrapModel handlers,
	// but new code should read and modify state.ToolInfos which is the source of truth for the model call.
	Tools []*schema.ToolInfo

	// ModelRetryConfig contains the retry configuration for the model.
	// This is populated at request time from the agent's ModelRetryConfig.
	// Used by EventSenderModelWrapper to wrap stream errors appropriately.
	ModelRetryConfig *ModelRetryConfig

	// ModelFailoverConfig contains the failover configuration for the model.
	// This is populated at request time from the agent's ModelFailoverConfig.
	// Used by EventSenderModelWrapper to wrap stream errors so that failed failover
	// attempts are skipped (not treated as fatal) by the flow event processor.
	ModelFailoverConfig *ModelFailoverConfig

	cancelContext *cancelContext
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

	// ToolSearchTool is the tool info for the model's native tool search capability.
	// When set by a BeforeAgent handler, the framework passes it to the model via model.WithToolSearchTool.
	ToolSearchTool *schema.ToolInfo
}

// TypedChatModelAgentMiddleware defines the interface for customizing TypedChatModelAgent behavior.
//
// IMPORTANT: This interface is specifically designed for TypedChatModelAgent and agents built
// on top of it (e.g., DeepAgent).
//
// Why TypedChatModelAgentMiddleware instead of AgentMiddleware?
//
// AgentMiddleware is a struct type, which has inherent limitations:
//   - Struct types are closed: users cannot add new methods to extend functionality
//   - The framework only recognizes AgentMiddleware's fixed fields, so even if users
//     embed AgentMiddleware in a custom struct and add methods, the framework cannot
//     call those methods (config.Middlewares is []AgentMiddleware, not a user type)
//   - Callbacks in AgentMiddleware only return error, cannot return modified context
//
// TypedChatModelAgentMiddleware is an interface type, which is open for extension:
//   - Users can implement custom handlers with arbitrary internal state and methods
//   - Hook methods return (context.Context, ..., error) for direct context propagation
//   - Wrapper methods (WrapToolCall, WrapModel) enable context propagation through the
//     wrapped endpoint chain: wrappers can pass modified context to the next wrapper
//   - Configuration is centralized in struct fields rather than scattered in closures
//
// TypedChatModelAgentMiddleware vs AgentMiddleware:
//   - Use AgentMiddleware for simple, static additions (extra instruction/tools)
//   - Use TypedChatModelAgentMiddleware for dynamic behavior, context modification, or call wrapping
//   - AgentMiddleware is kept for backward compatibility with existing users
//   - Both can be used together; see AgentMiddleware documentation for execution order
//
// Use *TypedBaseChatModelAgentMiddleware as an embedded struct to provide default no-op
// implementations for all methods.
type TypedChatModelAgentMiddleware[M messageType] interface {
	// BeforeAgent is called before each agent run, allowing modification of
	// the agent's instruction and tools configuration.
	BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error)

	// BeforeModelRewriteState is called before each model invocation.
	// The returned state is persisted to the agent's internal state and passed to the model.
	// The returned context is propagated to the model call and subsequent handlers.
	//
	// The ChatModelAgentState struct provides access to:
	//   - Messages: the conversation history
	//   - ToolInfos: the tool list that will be sent to the model (modifiable)
	//   - DeferredToolInfos: tools for server-side search (modifiable, nil if unused)
	//
	// This is the recommended place to modify messages and tools before a model call.
	// Changes here are persisted in state and reflected in subsequent iterations.
	BeforeModelRewriteState(ctx context.Context, state *TypedChatModelAgentState[M], mc *ModelContext) (context.Context, *TypedChatModelAgentState[M], error)

	// AfterModelRewriteState is called after each model invocation.
	// The input state includes the model's response as the last message.
	// The returned state is persisted to the agent's internal state.
	//
	// The ChatModelAgentState struct provides access to:
	//   - Messages: the conversation history including the model's response
	//   - ToolInfos: the tool list that was sent to the model
	//   - DeferredToolInfos: tools for server-side search (nil if unused)
	AfterModelRewriteState(ctx context.Context, state *TypedChatModelAgentState[M], mc *ModelContext) (context.Context, *TypedChatModelAgentState[M], error)

	// AfterToolCallsRewriteState is called after all concurrent tool calls in an iteration complete.
	// The input state includes all messages up to and including the tool call results.
	// The returned state is persisted to the agent's internal state.
	//
	// The ToolCallsContext provides metadata about the tool calls that just completed,
	// derived from the assistant message's ToolCalls field.
	AfterToolCallsRewriteState(ctx context.Context, state *TypedChatModelAgentState[M], tc *ToolCallsContext) (context.Context, *TypedChatModelAgentState[M], error)

	// WrapInvokableToolCall wraps a tool's synchronous execution with custom behavior.
	// Return the input endpoint unchanged and nil error if no wrapping is needed.
	//
	// This method is only called for tools that implement InvokableTool.
	// If a tool only implements StreamableTool, this method will not be called for that tool.
	//
	// This method is called at request time when the tool is about to be executed.
	// The tCtx parameter provides metadata about the tool:
	//   - Name: The name of the tool being wrapped
	//   - CallID: The unique identifier for this specific tool call
	WrapInvokableToolCall(ctx context.Context, endpoint InvokableToolCallEndpoint, tCtx *ToolContext) (InvokableToolCallEndpoint, error)

	// WrapStreamableToolCall wraps a tool's streaming execution with custom behavior.
	// Return the input endpoint unchanged and nil error if no wrapping is needed.
	//
	// This method is only called for tools that implement StreamableTool.
	// If a tool only implements InvokableTool, this method will not be called for that tool.
	//
	// This method is called at request time when the tool is about to be executed.
	// The tCtx parameter provides metadata about the tool:
	//   - Name: The name of the tool being wrapped
	//   - CallID: The unique identifier for this specific tool call
	WrapStreamableToolCall(ctx context.Context, endpoint StreamableToolCallEndpoint, tCtx *ToolContext) (StreamableToolCallEndpoint, error)

	// WrapEnhancedInvokableToolCall wraps an enhanced tool's synchronous execution with custom behavior.
	// Return the input endpoint unchanged and nil error if no wrapping is needed.
	//
	// This method is only called for tools that implement EnhancedInvokableTool.
	// If a tool only implements EnhancedStreamableTool, this method will not be called for that tool.
	//
	// This method is called at request time when the tool is about to be executed.
	// The tCtx parameter provides metadata about the tool:
	//   - Name: The name of the tool being wrapped
	//   - CallID: The unique identifier for this specific tool call
	WrapEnhancedInvokableToolCall(ctx context.Context, endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) (EnhancedInvokableToolCallEndpoint, error)

	// WrapEnhancedStreamableToolCall wraps an enhanced tool's streaming execution with custom behavior.
	// Return the input endpoint unchanged and nil error if no wrapping is needed.
	//
	// This method is only called for tools that implement EnhancedStreamableTool.
	// If a tool only implements EnhancedInvokableTool, this method will not be called for that tool.
	//
	// This method is called at request time when the tool is about to be executed.
	// The tCtx parameter provides metadata about the tool:
	//   - Name: The name of the tool being wrapped
	//   - CallID: The unique identifier for this specific tool call
	WrapEnhancedStreamableToolCall(ctx context.Context, endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) (EnhancedStreamableToolCallEndpoint, error)

	// WrapModel wraps a chat model with custom behavior around the actual model call.
	// Return the input model unchanged and nil error if no wrapping is needed.
	//
	// This method is called at request time when the model is about to be invoked.
	// Note: The parameter is model.BaseModel[M] (not ToolCallingChatModel) because wrappers
	// only need to intercept Generate/Stream calls. Tool binding (WithTools) is handled
	// separately by the framework and does not flow through user wrappers.
	//
	// Recommended use cases (behavior around the model call itself):
	//   - Model call retry logic
	//   - Model failover (switching to a backup model)
	//   - Sending events (e.g. streaming progress)
	//   - Processing or transforming the response stream
	//   - Changing call configurations (temperature, top_p, etc.)
	//
	// Discouraged use cases (use BeforeModelRewriteState instead):
	//   - Modifying input messages: changes here are NOT persisted in state, only
	//     affect a single model call, and break prompt cache across iterations.
	//   - Modifying the tool list: use state.ToolInfos / state.DeferredToolInfos in
	//     BeforeModelRewriteState, which is the source of truth for tool configuration.
	//
	// The mc parameter provides read-only context about the current model call:
	//   - Tools: The tool infos that will be sent to the model (Deprecated: read state.ToolInfos instead)
	WrapModel(ctx context.Context, m model.BaseModel[M], mc *ModelContext) (model.BaseModel[M], error)
}

// ChatModelAgentMiddleware is the default middleware type using *schema.Message.
// See TypedChatModelAgentMiddleware for full documentation.
type ChatModelAgentMiddleware = TypedChatModelAgentMiddleware[*schema.Message]

type TypedBaseChatModelAgentMiddleware[M messageType] struct{}

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
//	func (h *MyHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
//		// custom logic
//		return ctx, state, nil
//	}
type BaseChatModelAgentMiddleware = TypedBaseChatModelAgentMiddleware[*schema.Message]

func (b *TypedBaseChatModelAgentMiddleware[M]) WrapInvokableToolCall(_ context.Context, endpoint InvokableToolCallEndpoint, _ *ToolContext) (InvokableToolCallEndpoint, error) {
	return endpoint, nil
}

func (b *TypedBaseChatModelAgentMiddleware[M]) WrapStreamableToolCall(_ context.Context, endpoint StreamableToolCallEndpoint, _ *ToolContext) (StreamableToolCallEndpoint, error) {
	return endpoint, nil
}

func (b *TypedBaseChatModelAgentMiddleware[M]) WrapEnhancedInvokableToolCall(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) (EnhancedInvokableToolCallEndpoint, error) {
	return endpoint, nil
}

func (b *TypedBaseChatModelAgentMiddleware[M]) WrapEnhancedStreamableToolCall(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, _ *ToolContext) (EnhancedStreamableToolCallEndpoint, error) {
	return endpoint, nil
}

func (b *TypedBaseChatModelAgentMiddleware[M]) WrapModel(_ context.Context, m model.BaseModel[M], _ *ModelContext) (model.BaseModel[M], error) {
	return m, nil
}

func (b *TypedBaseChatModelAgentMiddleware[M]) BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
	return ctx, runCtx, nil
}

func (b *TypedBaseChatModelAgentMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *TypedChatModelAgentState[M], mc *ModelContext) (context.Context, *TypedChatModelAgentState[M], error) {
	return ctx, state, nil
}

func (b *TypedBaseChatModelAgentMiddleware[M]) AfterModelRewriteState(ctx context.Context, state *TypedChatModelAgentState[M], mc *ModelContext) (context.Context, *TypedChatModelAgentState[M], error) {
	return ctx, state, nil
}

func (b *TypedBaseChatModelAgentMiddleware[M]) AfterToolCallsRewriteState(ctx context.Context, state *TypedChatModelAgentState[M], tc *ToolCallsContext) (context.Context, *TypedChatModelAgentState[M], error) {
	return ctx, state, nil
}

func processTypedState(ctx context.Context, fn func(extra map[string]any) map[string]any) error {
	runCtx := getRunCtx(ctx)
	if runCtx != nil && runCtx.AgenticRootInput != nil {
		return compose.ProcessState(ctx, func(_ context.Context, st *typedState[*schema.AgenticMessage]) error {
			st.Extra = fn(st.Extra)
			return nil
		})
	}
	return compose.ProcessState(ctx, func(_ context.Context, st *typedState[*schema.Message]) error {
		st.Extra = fn(st.Extra)
		return nil
	})
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
	if err := checkGobEncodability(key, value); err != nil {
		return err
	}

	err := processTypedState(ctx, func(extra map[string]any) map[string]any {
		if extra == nil {
			extra = make(map[string]any)
		}
		extra[key] = value
		return extra
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
	err := processTypedState(ctx, func(extra map[string]any) map[string]any {
		if extra != nil {
			val, found = extra[key]
		}
		return extra
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
	err := processTypedState(ctx, func(extra map[string]any) map[string]any {
		if extra != nil {
			delete(extra, key)
		}
		return extra
	})
	if err != nil {
		return fmt.Errorf("DeleteRunLocalValue failed: must be called within a ChatModelAgent Run() or Resume() execution context: %w", err)
	}
	return nil
}

// TypedSendEvent sends a custom TypedAgentEvent to the event stream during agent execution.
// This allows TypedChatModelAgentMiddleware implementations to emit custom events that will be
// received by the caller iterating over the agent's event stream.
//
// This function can only be called from within a TypedChatModelAgentMiddleware during agent execution.
// Returns an error if called outside of an agent execution context.
func TypedSendEvent[M messageType](ctx context.Context, event *TypedAgentEvent[M]) error {
	execCtx := getTypedChatModelAgentExecCtx[M](ctx)
	if execCtx == nil || execCtx.generator == nil {
		return fmt.Errorf("TypedSendEvent failed: must be called within a ChatModelAgent Run() or Resume() execution context")
	}
	execCtx.send(event)
	return nil
}

// SendEvent sends a custom AgentEvent to the event stream during agent execution.
// This allows ChatModelAgentMiddleware implementations to emit custom events that will be
// received by the caller iterating over the agent's event stream.
//
// This function can only be called from within a ChatModelAgentMiddleware during agent execution.
// Returns an error if called outside of an agent execution context.
func SendEvent(ctx context.Context, event *AgentEvent) error {
	return TypedSendEvent[*schema.Message](ctx, event)
}

// checkGobEncodability probes whether the value can be gob-encoded as part of
// a map[string]any, which is exactly how State.Extra is serialized during
// checkpoint. This catches unregistered types early at Set time, rather than
// letting them fail at checkpoint/resume time with a confusing error.
func checkGobEncodability(key string, value any) error {
	probe := map[string]any{key: value}
	if err := gob.NewEncoder(io.Discard).Encode(probe); err != nil {
		typeName := reflect.TypeOf(value).String()
		return fmt.Errorf("SetRunLocalValue: the value (type %s) for key %q is not gob-serializable, "+
			"which means it will fail when the agent checkpoint is saved or resumed.\n\n"+
			"To fix this, register the type in an init() function in your package:\n\n"+
			"  func init() {\n"+
			"      schema.RegisterName[%s](\"a_unique_name_for_this_type\")\n"+
			"  }\n\n"+
			"This is required because agent state (including values set via SetRunLocalValue) is "+
			"persisted using gob encoding for interrupt/resume support. All concrete types stored "+
			"in interface-typed fields (like map[string]any) must be registered with gob.\n\n"+
			"If this value does not need to survive interrupt/resume, store it on the context instead, "+
			"for example via context.WithValue, so you don't need gob registration.\n\n"+
			"Underlying error: %w", typeName, key, typeName, err)
	}
	return nil
}
