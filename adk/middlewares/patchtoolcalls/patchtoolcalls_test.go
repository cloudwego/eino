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
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestNewTypedAgenticMessage(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.AgenticMessage](ctx, nil)
	assert.NoError(t, err)
	assert.NotNil(t, mw)

	var _ adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] = mw
}

type testToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func makeUserMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(content)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(content)).(M)
	}
	panic("unreachable")
}

func makeAssistantMsgWithToolCalls[M adk.MessageType](content string, toolCalls []testToolCall) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		tcs := make([]schema.ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			tcs[i] = schema.ToolCall{ID: tc.ID, Function: schema.FunctionCall{Name: tc.Name, Arguments: tc.Arguments}}
		}
		return any(schema.AssistantMessage(content, tcs)).(M)
	case *schema.AgenticMessage:
		blocks := make([]*schema.ContentBlock, 0, len(toolCalls)+1)
		if content != "" {
			blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: content}))
		}
		for _, tc := range toolCalls {
			blocks = append(blocks, schema.NewContentBlock(&schema.FunctionToolCall{CallID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}))
		}
		return any(&schema.AgenticMessage{
			Role:          schema.AgenticRoleTypeAssistant,
			ContentBlocks: blocks,
		}).(M)
	}
	panic("unreachable")
}

func makeToolResultMsg[M adk.MessageType](content string, callID string, toolName string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.ToolMessage(content, callID, schema.WithToolName(toolName))).(M)
	case *schema.AgenticMessage:
		return any(&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.FunctionToolResult{
					CallID: callID,
					Name:   toolName,
					Content: []*schema.FunctionToolResultContentBlock{
						{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: content}},
					},
				}),
			},
		}).(M)
	}
	panic("unreachable")
}

func assertMsgContent[M adk.MessageType](t *testing.T, msg M, expectedContent string) {
	t.Helper()
	switch m := any(msg).(type) {
	case *schema.Message:
		assert.Equal(t, expectedContent, m.Content)
	case *schema.AgenticMessage:
		for _, block := range m.ContentBlocks {
			if block.Type == schema.ContentBlockTypeFunctionToolResult && block.FunctionToolResult != nil {
				for _, b := range block.FunctionToolResult.Content {
					if b.Text != nil {
						assert.Equal(t, expectedContent, b.Text.Text)
						return
					}
				}
			}
		}
		t.Errorf("no text content found in agentic message, expected %q", expectedContent)
	}
}

func assertToolResultID[M adk.MessageType](t *testing.T, msg M, expectedID string) {
	t.Helper()
	switch m := any(msg).(type) {
	case *schema.Message:
		assert.Equal(t, expectedID, m.ToolCallID)
	case *schema.AgenticMessage:
		for _, block := range m.ContentBlocks {
			if block.Type == schema.ContentBlockTypeFunctionToolResult && block.FunctionToolResult != nil {
				assert.Equal(t, expectedID, block.FunctionToolResult.CallID)
				return
			}
		}
		t.Errorf("no tool result found in agentic message, expected call ID %q", expectedID)
	}
}

func assertToolResultName[M adk.MessageType](t *testing.T, msg M, expectedName string) {
	t.Helper()
	switch m := any(msg).(type) {
	case *schema.Message:
		assert.Equal(t, expectedName, m.ToolName)
	case *schema.AgenticMessage:
		for _, block := range m.ContentBlocks {
			if block.Type == schema.ContentBlockTypeFunctionToolResult && block.FunctionToolResult != nil {
				assert.Equal(t, expectedName, block.FunctionToolResult.Name)
				return
			}
		}
		t.Errorf("no tool result found in agentic message, expected tool name %q", expectedName)
	}
}

func testPatchToolCallsGeneric[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		config         *Config
		messages       []M
		wantLen        int
		checkPatchedAt int // index of the patched message to check (-1 if no check needed)
		wantCallID     string
		wantToolName   string
		wantContent    string
	}{
		{
			name:           "empty messages",
			config:         nil,
			messages:       nil,
			wantLen:        0,
			checkPatchedAt: -1,
		},
		{
			name:   "no tool calls to patch",
			config: nil,
			messages: []M{
				makeUserMsg[M]("hello"),
				makeAssistantMsgWithToolCalls[M]("hi there", nil),
			},
			wantLen:        2,
			checkPatchedAt: -1,
		},
		{
			name:   "missing tool result",
			config: nil,
			messages: []M{
				makeUserMsg[M]("hello"),
				makeAssistantMsgWithToolCalls[M]("", []testToolCall{
					{ID: "call_1", Name: "tool_a", Arguments: "{}"},
					{ID: "call_2", Name: "tool_b", Arguments: "{}"},
				}),
				makeToolResultMsg[M]("result_a", "call_1", "tool_a"),
			},
			wantLen:        4,
			checkPatchedAt: 2,
			wantCallID:     "call_2",
			wantToolName:   "tool_b",
			wantContent:    fmt.Sprintf(defaultPatchedToolMessageTemplate, "tool_b", "call_2"),
		},
		{
			name: "custom content generator",
			config: &Config{
				PatchedContentGenerator: func(ctx context.Context, toolName, toolCallID string) (string, error) {
					return fmt.Sprintf("123 %s %s", toolName, toolCallID), nil
				},
			},
			messages: []M{
				makeUserMsg[M]("hello"),
				makeAssistantMsgWithToolCalls[M]("", []testToolCall{
					{ID: "call_1", Name: "tool_a", Arguments: "{}"},
					{ID: "call_2", Name: "tool_b", Arguments: "{}"},
				}),
				makeToolResultMsg[M]("result_a", "call_1", "tool_a"),
			},
			wantLen:        4,
			checkPatchedAt: 2,
			wantCallID:     "call_2",
			wantToolName:   "tool_b",
			wantContent:    "123 tool_b call_2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw, err := NewTyped[M](ctx, tt.config)
			assert.NoError(t, err)

			state := &adk.TypedChatModelAgentState[M]{
				Messages: tt.messages,
			}
			_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
			assert.NoError(t, err)
			assert.Len(t, newState.Messages, tt.wantLen)

			if tt.checkPatchedAt >= 0 && tt.checkPatchedAt < len(newState.Messages) {
				patched := newState.Messages[tt.checkPatchedAt]
				assertToolResultID(t, patched, tt.wantCallID)
				assertToolResultName(t, patched, tt.wantToolName)
				assertMsgContent(t, patched, tt.wantContent)
			}
		})
	}
}

func TestPatchToolCallsGeneric(t *testing.T) {
	t.Run("Message", testPatchToolCallsGeneric[*schema.Message])
	t.Run("AgenticMessage", testPatchToolCallsGeneric[*schema.AgenticMessage])
}

func TestPatchToolCallsAgenticToolSearchResult(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.AgenticMessage](ctx, nil)
	require.NoError(t, err)

	messages := []*schema.AgenticMessage{
		makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{
			{ID: "call_1", Name: "tool_search", Arguments: `{"query":"dynamic"}`},
		}),
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.ToolSearchFunctionToolResult{
					CallID: "call_1",
					Name:   "tool_search",
					Result: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{
						{Name: "dynamic_tool", Desc: "dynamic tool"},
					}},
				}),
			},
		},
	}

	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: messages}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	assert.Len(t, newState.Messages, 2)
	assert.Equal(t, schema.ContentBlockTypeToolSearchResult, newState.Messages[1].ContentBlocks[0].Type)
}

// TestPatchToolCalls_NilFunctionToolCallInBlock verifies the middleware handles
// a ContentBlock with Type=FunctionToolCall but FunctionToolCall=nil without panicking.
func TestPatchToolCalls_NilFunctionToolCallInBlock(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.AgenticMessage](ctx, nil)
	require.NoError(t, err)

	msgs := []*schema.AgenticMessage{
		schema.UserAgenticMessage("hello"),
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type:             schema.ContentBlockTypeFunctionToolCall,
					FunctionToolCall: nil, // nil despite type indicating tool call
				},
				schema.NewContentBlock(&schema.FunctionToolCall{
					CallID: "call_1",
					Name:   "real_tool",
				}),
			},
		},
	}

	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: msgs}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	assert.NoError(t, err)
	assert.Len(t, newState.Messages, 3, "should patch call_1 but skip nil FunctionToolCall block")

	patchMsg := newState.Messages[2]
	assert.Equal(t, schema.AgenticRoleTypeUser, patchMsg.Role)
	foundResult := false
	for _, block := range patchMsg.ContentBlocks {
		if block != nil && block.Type == schema.ContentBlockTypeFunctionToolResult &&
			block.FunctionToolResult != nil && block.FunctionToolResult.CallID == "call_1" {
			foundResult = true
		}
	}
	assert.True(t, foundResult, "patched message should contain tool result for call_1")
}
