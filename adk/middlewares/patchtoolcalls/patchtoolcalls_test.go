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

package patchtoolcalls

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestPatchToolCalls(t *testing.T) {
	ctx := context.Background()
	m, err := NewMiddleware(ctx, nil)
	assert.NoError(t, err)

	// empty messages
	state := &adk.ChatModelAgentState{
		Messages: nil,
	}
	_, newState, err := m.BeforeModelRewriteState(ctx, state, nil)
	assert.NoError(t, err)
	assert.Len(t, newState.Messages, 0)

	state = &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("hi there", nil),
		},
	}
	_, newState, err = m.BeforeModelRewriteState(ctx, state, nil)
	assert.NoError(t, err)
	assert.Len(t, newState.Messages, 2)

	state = &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "call_1", Function: schema.FunctionCall{Name: "tool_a"}},
				{ID: "call_2", Function: schema.FunctionCall{Name: "tool_b"}},
			}),
			schema.ToolMessage("result_a", "call_1", schema.WithToolName("tool_a")),
		},
	}
	_, newState, err = m.BeforeModelRewriteState(ctx, state, nil)
	assert.NoError(t, err)
	patchedMsg := newState.Messages[2]
	assert.Equal(t, schema.Tool, patchedMsg.Role)
	assert.Equal(t, "call_2", patchedMsg.ToolCallID)
	assert.Equal(t, "tool_b", patchedMsg.ToolName)
	assert.Equal(t, fmt.Sprintf(defaultPatchedToolMessageTemplate, "tool_b", "call_2"), patchedMsg.Content)

	m, err = NewMiddleware(ctx, &Config{
		PatchedContentGenerator: func(ctx context.Context, toolName, toolCallID string) (string, error) {
			return fmt.Sprintf("123 %s %s", toolName, toolCallID), nil
		},
	})
	assert.NoError(t, err)
	state = &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "call_1", Function: schema.FunctionCall{Name: "tool_a"}},
				{ID: "call_2", Function: schema.FunctionCall{Name: "tool_b"}},
			}),
			schema.ToolMessage("result_a", "call_1", schema.WithToolName("tool_a")),
		},
	}
	_, newState, err = m.BeforeModelRewriteState(ctx, state, nil)
	assert.NoError(t, err)
	patchedMsg = newState.Messages[2]
	assert.Equal(t, schema.Tool, patchedMsg.Role)
	assert.Equal(t, "call_2", patchedMsg.ToolCallID)
	assert.Equal(t, "tool_b", patchedMsg.ToolName)
	assert.Equal(t, "123 tool_b call_2", patchedMsg.Content)
}
