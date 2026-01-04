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

	"github.com/cloudwego/eino/compose"
)

// AgentHandler defines the interface for customizing agent behavior.
// Implementations can modify agent configuration, rewrite message history,
// and wrap tool calls with custom logic.
//
// Use BaseAgentHandler as an embedded struct to provide default no-op
// implementations for all methods.
type AgentHandler interface {
	// BeforeAgent is called before each agent run, allowing modification of
	// the agent's instruction and tools configuration.
	BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error)

	// BeforeModelRewriteHistory is called before each model invocation.
	// The returned messages are persisted to the agent's internal state.
	BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)

	// AfterModelRewriteHistory is called after each model invocation.
	// The returned messages are persisted to the agent's internal state.
	AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)

	// GetToolMiddleware returns a ToolMiddleware for wrapping tool calls.
	// Return an empty ToolMiddleware{} if no tool wrapping is needed.
	//
	// This follows the same pattern as compose.ToolMiddleware, allowing
	// handlers to wrap both invokable and streamable tool calls.
	//
	// The middleware receives a compose.ToolInput which contains:
	//   - Name: tool name (should not be modified)
	//   - CallID: unique call identifier (should not be modified)
	//   - Arguments: tool arguments (may be modified)
	//   - CallOptions: tool options (may be modified)
	GetToolMiddleware() compose.ToolMiddleware
}

// BaseAgentHandler provides default no-op implementations for AgentHandler.
// Embed this struct in custom handlers to only override the methods you need.
type BaseAgentHandler struct{}

func (b BaseAgentHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
	return ctx, nil
}

func (b BaseAgentHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

func (b BaseAgentHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

func (b BaseAgentHandler) GetToolMiddleware() compose.ToolMiddleware {
	return compose.ToolMiddleware{}
}
