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
)

const (
	generalAgentName = "general-purpose"
	taskToolName     = "task"
)

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

func buildAppendPromptTool(prompt string, t tool.BaseTool) adk.ChatModelAgentMiddleware {
	return &appendPromptTool{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		t:                            t,
		prompt:                       prompt,
	}
}

type appendPromptTool struct {
	*adk.BaseChatModelAgentMiddleware
	t      tool.BaseTool
	prompt string
}

func (w *appendPromptTool) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	nRunCtx := *runCtx
	nRunCtx.Instruction += w.prompt
	if w.t != nil {
		nRunCtx.Tools = append(nRunCtx.Tools, w.t)
	}
	return ctx, &nRunCtx, nil
}
