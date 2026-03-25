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
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	generalAgentName = "general-purpose"
	exploreAgentName = "explore"
	planAgentName    = "plan"
	taskToolName     = "task"
)

var defaultReadOnlyDisabledTools = []string{filesystem.ToolNameWriteFile, filesystem.ToolNameEditFile, taskToolName}

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

type readOnlyReminderMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	reminder string
}

func (m *readOnlyReminderMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	lastToolIdx := -1
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if state.Messages[i].Role == schema.Tool {
			lastToolIdx = i
			break
		}
	}

	if lastToolIdx < 0 {
		return ctx, state, nil
	}

	nState := *state
	nState.Messages = make([]adk.Message, len(state.Messages))
	copy(nState.Messages, state.Messages)

	orig := nState.Messages[lastToolIdx]
	patched := *orig
	patched.Content = orig.Content + "\n\n" + m.reminder
	nState.Messages[lastToolIdx] = &patched

	return ctx, &nState, nil
}

type disableToolsMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	disabledTools map[string]bool
}

func (m *disableToolsMiddleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	nRunCtx := *runCtx
	filtered := make([]tool.BaseTool, 0, len(nRunCtx.Tools))
	for _, t := range nRunCtx.Tools {
		info, err := t.Info(ctx)
		if err != nil {
			return ctx, runCtx, err
		}
		if !m.disabledTools[info.Name] {
			filtered = append(filtered, t)
		}
	}
	nRunCtx.Tools = filtered
	return ctx, &nRunCtx, nil
}
