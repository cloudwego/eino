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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

const (
	generalAgentName = "general-purpose"
	taskToolName     = "task"
)

const (
	SessionKeyTodos = "deep_agent_session_key_todos"
)

func typedBuildAppendPromptTool[M adk.MessageType](prompt string, t tool.BaseTool) adk.TypedChatModelAgentMiddleware[M] {
	return &typedAppendPromptTool[M]{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
		t:                                 t,
		prompt:                            prompt,
	}
}

type typedAppendPromptTool[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	t      tool.BaseTool
	prompt string
}

func (w *typedAppendPromptTool[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
	nRunCtx := *runCtx
	nRunCtx.Instruction += w.prompt
	if w.t != nil {
		nRunCtx.Tools = append(nRunCtx.Tools, w.t)
	}
	return ctx, &nRunCtx, nil
}
