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

package deep

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	generalAgentName = "general-purpose"
	taskToolName     = "task"
)

// messageType is the sealed type constraint for message types in the deep package.
// This mirrors the unexported constraint in the adk package.
type messageType interface {
	*schema.Message | *schema.AgenticMessage
}

const (
	SessionKeyTodos = "deep_agent_session_key_todos"
)

func assertAgentTool(t tool.BaseTool) (tool.InvokableTool, error) {
	it, ok := t.(tool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("failed to assert agent tool type: %T", t)
	}
	return it, nil
}

func typedBuildAppendPromptTool[M messageType](prompt string, t tool.BaseTool) adk.TypedChatModelAgentMiddleware[M] {
	return &typedAppendPromptTool[M]{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
		t:                                 t,
		prompt:                            prompt,
	}
}

func buildAppendPromptTool(prompt string, t tool.BaseTool) adk.ChatModelAgentMiddleware {
	return typedBuildAppendPromptTool[*schema.Message](prompt, t)
}

type typedAppendPromptTool[M messageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	t      tool.BaseTool
	prompt string
}

type appendPromptTool = typedAppendPromptTool[*schema.Message]

func (w *typedAppendPromptTool[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	nRunCtx := *runCtx
	nRunCtx.Instruction += w.prompt
	if w.t != nil {
		nRunCtx.Tools = append(nRunCtx.Tools, w.t)
	}
	return ctx, &nRunCtx, nil
}

type middlewareAdapter[M messageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	inner adk.ChatModelAgentMiddleware
}

func (a *middlewareAdapter[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	return a.inner.BeforeAgent(ctx, runCtx)
}

func (a *middlewareAdapter[M]) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return a.inner.WrapInvokableToolCall(ctx, endpoint, tCtx)
}

func (a *middlewareAdapter[M]) WrapStreamableToolCall(ctx context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	return a.inner.WrapStreamableToolCall(ctx, endpoint, tCtx)
}

func (a *middlewareAdapter[M]) WrapEnhancedInvokableToolCall(ctx context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.EnhancedInvokableToolCallEndpoint, error) {
	return a.inner.WrapEnhancedInvokableToolCall(ctx, endpoint, tCtx)
}

func (a *middlewareAdapter[M]) WrapEnhancedStreamableToolCall(ctx context.Context, endpoint adk.EnhancedStreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.EnhancedStreamableToolCallEndpoint, error) {
	return a.inner.WrapEnhancedStreamableToolCall(ctx, endpoint, tCtx)
}

func adaptMiddleware[M messageType](m adk.ChatModelAgentMiddleware) adk.TypedChatModelAgentMiddleware[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(m).(adk.TypedChatModelAgentMiddleware[M])
	default:
		return &middlewareAdapter[M]{
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
			inner:                             m,
		}
	}
}
