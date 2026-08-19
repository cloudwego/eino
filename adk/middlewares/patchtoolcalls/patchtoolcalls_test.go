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

func collectToolResultIDs[M adk.MessageType](messages []M) []string {
	var ids []string
	for _, msg := range messages {
		switch m := any(msg).(type) {
		case *schema.Message:
			if m.Role == schema.Tool {
				ids = append(ids, m.ToolCallID)
			}
		case *schema.AgenticMessage:
			for _, block := range m.ContentBlocks {
				if callID, ok := agenticResultCallID(block); ok {
					ids = append(ids, callID)
				}
			}
		}
	}
	return ids
}

func assertSyntheticMarker(t *testing.T, msg *schema.AgenticMessage, expected bool) {
	t.Helper()
	v, ok := msg.Extra[syntheticAgenticToolResultMarker]
	assert.Equal(t, expected, ok && v == true)
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
		{
			name: "custom tool result generator",
			config: &Config{
				PatchedToolResultGenerator: func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*PatchedToolResult, error) {
					argText := ""
					if toolArgument != nil {
						argText = toolArgument.Text
					}
					return &PatchedToolResult{
						Content: fmt.Sprintf("result %s %s %s", toolName, toolCallID, argText),
					}, nil
				},
			},
			messages: []M{
				makeUserMsg[M]("hello"),
				makeAssistantMsgWithToolCalls[M]("", []testToolCall{
					{ID: "call_1", Name: "tool_a", Arguments: "{}"},
					{ID: "call_2", Name: "tool_b", Arguments: `{"x":1}`},
				}),
				makeToolResultMsg[M]("result_a", "call_1", "tool_a"),
			},
			wantLen:        4,
			checkPatchedAt: 2,
			wantCallID:     "call_2",
			wantToolName:   "tool_b",
			wantContent:    `result tool_b call_2 {"x":1}`,
		},
		{
			name: "tool result generator takes precedence over content generator",
			config: &Config{
				PatchedContentGenerator: func(ctx context.Context, toolName, toolCallID string) (string, error) {
					return "legacy", nil
				},
				PatchedToolResultGenerator: func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*PatchedToolResult, error) {
					return &PatchedToolResult{Content: "enhanced"}, nil
				},
			},
			messages: []M{
				makeUserMsg[M]("hello"),
				makeAssistantMsgWithToolCalls[M]("", []testToolCall{
					{ID: "call_1", Name: "tool_a", Arguments: "{}"},
				}),
			},
			wantLen:        3,
			checkPatchedAt: 2,
			wantCallID:     "call_1",
			wantToolName:   "tool_a",
			wantContent:    "enhanced",
		},
		{
			name:   "two consecutive assistant messages with tool calls",
			config: nil,
			messages: []M{
				makeUserMsg[M]("hello"),
				makeAssistantMsgWithToolCalls[M]("", []testToolCall{
					{ID: "call_1", Name: "tool_a", Arguments: "{}"},
				}),
				makeAssistantMsgWithToolCalls[M]("continued...", nil),
			},
			wantLen:        4,
			checkPatchedAt: 2,
			wantCallID:     "call_1",
			wantToolName:   "tool_a",
			wantContent:    fmt.Sprintf(defaultPatchedToolMessageTemplate, "tool_a", "call_1"),
		},
		{
			name:   "assistant message followed by user message without tool result",
			config: nil,
			messages: []M{
				makeUserMsg[M]("hello"),
				makeAssistantMsgWithToolCalls[M]("", []testToolCall{
					{ID: "call_1", Name: "tool_a", Arguments: "{}"},
				}),
				makeUserMsg[M]("continued..."),
			},
			wantLen:        4,
			checkPatchedAt: 2,
			wantCallID:     "call_1",
			wantToolName:   "tool_a",
			wantContent:    fmt.Sprintf(defaultPatchedToolMessageTemplate, "tool_a", "call_1"),
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

func TestPatchedToolResultGenerator_SetsContentOnlyForPlainText(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		PatchedToolResultGenerator: func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*PatchedToolResult, error) {
			require.Equal(t, "tool_a", toolName)
			require.Equal(t, "call_1", toolCallID)
			require.NotNil(t, toolArgument)
			require.Equal(t, `{"q":"hi"}`, toolArgument.Text)
			return &PatchedToolResult{Content: "patched text"}, nil
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "call_1", Function: schema.FunctionCall{Name: "tool_a", Arguments: `{"q":"hi"}`}},
			}),
		},
	}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, newState.Messages, 3)

	patched := newState.Messages[2]
	assert.Equal(t, schema.Tool, patched.Role)
	assert.Equal(t, "call_1", patched.ToolCallID)
	assert.Equal(t, "tool_a", patched.ToolName)
	assert.Equal(t, "patched text", patched.Content)
	assert.Empty(t, patched.UserInputMultiContent,
		"plain-text tool results must not set UserInputMultiContent (OpenAI Content/MultiContent exclusivity)")
}

func TestPatchedToolResultGenerator_ToolResultPath(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		PatchedToolResultGenerator: func(context.Context, string, string, *schema.ToolArgument) (*PatchedToolResult, error) {
			return &PatchedToolResult{
				ToolResult: &schema.ToolResult{
					Parts: []schema.ToolOutputPart{{
						Type: schema.ToolPartTypeText,
						Text: "via tool result",
					}},
				},
			}, nil
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "call_1", Function: schema.FunctionCall{Name: "tool_a", Arguments: "{}"}},
			}),
		},
	}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, newState.Messages, 2)
	assert.Equal(t, "via tool result", newState.Messages[1].Content)
	assert.Empty(t, newState.Messages[1].UserInputMultiContent)
}

func TestPatchedToolResultGenerator_ContentAndToolResultMutuallyExclusive(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		PatchedToolResultGenerator: func(context.Context, string, string, *schema.ToolArgument) (*PatchedToolResult, error) {
			return &PatchedToolResult{
				Content: "plain",
				ToolResult: &schema.ToolResult{
					Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "also"}},
				},
			}, nil
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "call_1", Function: schema.FunctionCall{Name: "tool_a", Arguments: "{}"}},
			}),
		},
	}
	_, _, err = mw.BeforeModelRewriteState(ctx, state, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func testPatchToolCallsRemoveOrphanResults[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[M](ctx, &Config{RemoveOrphanResults: true})
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[M]{Messages: []M{
		makeToolResultMsg[M]("orphan", "call_orphan", "tool_orphan"),
		makeAssistantMsgWithToolCalls[M]("", []testToolCall{{ID: "call_1", Name: "tool_a", Arguments: "{}"}}),
		makeToolResultMsg[M]("result", "call_1", "tool_a"),
	}}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"call_1"}, collectToolResultIDs(newState.Messages))
}

func TestPatchToolCallsRemoveOrphanResults(t *testing.T) {
	t.Run("Message", testPatchToolCallsRemoveOrphanResults[*schema.Message])
	t.Run("AgenticMessage", testPatchToolCallsRemoveOrphanResults[*schema.AgenticMessage])
}

func testPatchToolCallsRemoveDuplicateResults[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[M](ctx, &Config{RemoveDuplicateResults: true})
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[M]{Messages: []M{
		makeAssistantMsgWithToolCalls[M]("", []testToolCall{{ID: "call_1", Name: "tool_a", Arguments: "{}"}}),
		makeToolResultMsg[M]("result", "call_1", "tool_a"),
		makeToolResultMsg[M]("duplicate", "call_1", "tool_a"),
	}}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"call_1"}, collectToolResultIDs(newState.Messages))
}

func TestPatchToolCallsRemoveDuplicateResults(t *testing.T) {
	t.Run("Message", testPatchToolCallsRemoveDuplicateResults[*schema.Message])
	t.Run("AgenticMessage", testPatchToolCallsRemoveDuplicateResults[*schema.AgenticMessage])
}

func testAttackPatchToolCallsAllowsRepeatedCallIDAcrossWindows[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[M](ctx, &Config{Strict: true, RemoveDuplicateResults: true})
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[M]{Messages: []M{
		makeAssistantMsgWithToolCalls[M]("", []testToolCall{{ID: "call_1", Name: "tool_a", Arguments: "{}"}}),
		makeToolResultMsg[M]("result_a", "call_1", "tool_a"),
		makeAssistantMsgWithToolCalls[M]("", []testToolCall{{ID: "call_1", Name: "tool_b", Arguments: "{}"}}),
		makeToolResultMsg[M]("result_b", "call_1", "tool_b"),
	}}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, newState.Messages, 4)
	assert.Equal(t, []string{"call_1", "call_1"}, collectToolResultIDs(newState.Messages))
	assertMsgContent(t, newState.Messages[1], "result_a")
	assertMsgContent(t, newState.Messages[3], "result_b")
}

func TestAttack_PatchToolCallsAllowsRepeatedCallIDAcrossWindows(t *testing.T) {
	t.Log("reused provider call IDs must be scoped to each assistant tool-call message's contiguous result window")
	t.Run("Message", testAttackPatchToolCallsAllowsRepeatedCallIDAcrossWindows[*schema.Message])
	t.Run("AgenticMessage", testAttackPatchToolCallsAllowsRepeatedCallIDAcrossWindows[*schema.AgenticMessage])
}

func testPatchToolCallsSkipsEmptyIDInNonStrictMode[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[M](ctx, nil)
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[M]{Messages: []M{
		makeAssistantMsgWithToolCalls[M]("", []testToolCall{{ID: "", Name: "tool_a", Arguments: "{}"}}),
	}}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	assert.Len(t, newState.Messages, 1)
	assert.Empty(t, collectToolResultIDs(newState.Messages))
}

func TestPatchToolCallsSkipsEmptyIDInNonStrictMode(t *testing.T) {
	t.Run("Message", testPatchToolCallsSkipsEmptyIDInNonStrictMode[*schema.Message])
	t.Run("AgenticMessage", testPatchToolCallsSkipsEmptyIDInNonStrictMode[*schema.AgenticMessage])
}

func testPatchToolCallsReportsEmptyIDInStrictMode[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[M](ctx, &Config{Strict: true})
	require.NoError(t, err)

	messages := []M{
		makeAssistantMsgWithToolCalls[M]("", []testToolCall{{ID: "", Name: "tool_a", Arguments: "{}"}}),
	}
	state := &adk.TypedChatModelAgentState[M]{Messages: messages}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.Error(t, err)
	assert.Nil(t, newState)
	assert.Same(t, any(messages[0]), any(state.Messages[0]))
	assert.Contains(t, err.Error(), "empty_tool_call_id=1")
}

func TestPatchToolCallsReportsEmptyIDInStrictMode(t *testing.T) {
	t.Run("Message", testPatchToolCallsReportsEmptyIDInStrictMode[*schema.Message])
	t.Run("AgenticMessage", testPatchToolCallsReportsEmptyIDInStrictMode[*schema.AgenticMessage])
}

func TestPatchToolCallsStrictCountsAllMismatchCategories(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.Message](ctx, &Config{Strict: true})
	require.NoError(t, err)

	messages := []*schema.Message{
		makeToolResultMsg[*schema.Message]("orphan", "call_orphan", "tool_orphan"),
		makeAssistantMsgWithToolCalls[*schema.Message]("", []testToolCall{
			{ID: "call_missing", Name: "tool_missing", Arguments: "{}"},
			{ID: "", Name: "tool_empty", Arguments: "{}"},
			{ID: "call_dup", Name: "tool_dup", Arguments: "{}"},
		}),
		makeToolResultMsg[*schema.Message]("result", "call_dup", "tool_dup"),
		makeToolResultMsg[*schema.Message]("duplicate", "call_dup", "tool_dup"),
	}
	state := &adk.TypedChatModelAgentState[*schema.Message]{Messages: messages}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.Error(t, err)
	assert.Nil(t, newState)
	assert.Equal(t, messages, state.Messages)
	assert.Contains(t, err.Error(), "missing=1")
	assert.Contains(t, err.Error(), "orphan=1")
	assert.Contains(t, err.Error(), "duplicate=1")
	assert.Contains(t, err.Error(), "empty_tool_call_id=1")
}

func TestPatchToolCallsMarksSyntheticAgenticResult(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &Config{MarkSynthetic: true})
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{
		makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{{ID: "call_1", Name: "tool_a", Arguments: "{}"}}),
	}}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, newState.Messages, 2)
	assertSyntheticMarker(t, newState.Messages[1], true)
}

func TestPatchToolCallsMixedAgenticBlockRemovalUpdatesMessage(t *testing.T) {
	ctx := context.Background()
	assistant := makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{{ID: "call_1", Name: "tool_a", Arguments: "{}"}})
	mixed := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputText{Text: "keep"}),
			schema.NewContentBlock(&schema.FunctionToolResult{CallID: "call_orphan", Name: "tool_orphan"}),
			schema.NewContentBlock(&schema.FunctionToolResult{CallID: "call_1", Name: "tool_a"}),
		},
	}
	adk.EnsureMessageID(mixed)
	originalID := adk.GetMessageID(mixed)

	plan, err := buildAgenticNormalizationPlan(ctx, Config{RemoveOrphanResults: true}, []*schema.AgenticMessage{assistant, mixed})
	require.NoError(t, err)
	require.Len(t, plan.messages, 2)
	require.Len(t, plan.messages[1].ContentBlocks, 2)
	assert.Equal(t, schema.ContentBlockTypeUserInputText, plan.messages[1].ContentBlocks[0].Type)
	assert.Equal(t, "call_1", plan.messages[1].ContentBlocks[1].FunctionToolResult.CallID)
	require.Len(t, plan.events, 1)
	assert.Equal(t, adk.SessionEventMessageUpdated, plan.events[0].Kind)
	assert.Equal(t, originalID, plan.events[0].MessageUpdated.MessageID)
	assert.Equal(t, originalID, adk.GetMessageID(plan.events[0].MessageUpdated.Message))
}

func TestAttack_PatchToolCallsAgenticRechecksWindowAfterBlockRemoval(t *testing.T) {
	t.Log("missing detection must use the rewritten result window after orphan blocks are removed")
	ctx := context.Background()
	assistant := makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{{ID: "call_1", Name: "tool_a", Arguments: "{}"}})
	mixed := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputText{Text: "keep"}),
			schema.NewContentBlock(&schema.FunctionToolResult{CallID: "call_orphan", Name: "tool_orphan"}),
		},
	}
	delayed := makeToolResultMsg[*schema.AgenticMessage]("delayed", "call_1", "tool_a")

	plan, err := buildAgenticNormalizationPlan(ctx, Config{RemoveOrphanResults: true}, []*schema.AgenticMessage{
		assistant,
		mixed,
		delayed,
	})
	require.NoError(t, err)
	require.Len(t, plan.messages, 3)
	assert.Equal(t, []string{"call_1"}, collectToolResultIDs(plan.messages))
	assertMsgContent(t, plan.messages[1], fmt.Sprintf(defaultPatchedToolMessageTemplate, "tool_a", "call_1"))
	require.Len(t, plan.messages[2].ContentBlocks, 1)
	assert.Equal(t, schema.ContentBlockTypeUserInputText, plan.messages[2].ContentBlocks[0].Type)
	assert.Len(t, plan.events, 3)
	assert.Equal(t, adk.SessionEventMessageInserted, plan.events[0].Kind)
	assert.Equal(t, adk.SessionEventMessageUpdated, plan.events[1].Kind)
	assert.Equal(t, adk.SessionEventMessagesDeleted, plan.events[2].Kind)
}

func TestPatchToolCallsInsertionEventAnchorsReplayOrder(t *testing.T) {
	ctx := context.Background()
	assistant := makeAssistantMsgWithToolCalls[*schema.Message]("", []testToolCall{
		{ID: "call_1", Name: "tool_a", Arguments: "{}"},
		{ID: "call_2", Name: "tool_b", Arguments: "{}"},
	})
	result := makeToolResultMsg[*schema.Message]("result", "call_1", "tool_a")
	messages := []*schema.Message{assistant, result}

	plan, err := buildMessageNormalizationPlan(ctx, Config{}, messages)
	require.NoError(t, err)
	require.Len(t, plan.messages, 3)
	require.Len(t, plan.events, 1)
	event := plan.events[0]
	require.Equal(t, adk.SessionEventMessageInserted, event.Kind)
	assert.Equal(t, adk.GetMessageID(result), event.MessageInserted.BeforeMessageID)

	replayed := append([]*schema.Message{}, messages...)
	for i, msg := range replayed {
		if adk.GetMessageID(msg) == event.MessageInserted.BeforeMessageID {
			replayed = append(replayed, nil)
			copy(replayed[i+1:], replayed[i:])
			replayed[i] = event.MessageInserted.Message
			break
		}
	}
	assert.Equal(t, []string{"call_2", "call_1"}, collectToolResultIDs(replayed))
	assert.Equal(t, []string{"call_2", "call_1"}, collectToolResultIDs(plan.messages))
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

// TestPatchToolCalls_AgenticMessage_NilBlockInUserMessage verifies the middleware handles
// a User Agentic Message with nil ContentBlock without panicking.
func TestPatchToolCalls_AgenticMessage_NilBlockInUserMessage(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.AgenticMessage](ctx, nil)
	require.NoError(t, err)

	msgs := []*schema.AgenticMessage{
		schema.UserAgenticMessage("hello"),
		makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{
			{ID: "call_1", Name: "tool_a", Arguments: "{}"},
		}),
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				nil, // nil block to test robustness
			},
		},
	}

	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: msgs}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	assert.NoError(t, err, "should not panic when encountering nil block in user message")
	assert.Len(t, newState.Messages, 4, "should patch call_1 and insert tool response")

	// Verify the patched message is inserted at index 2
	patchMsg := newState.Messages[2]
	assert.Equal(t, schema.AgenticRoleTypeUser, patchMsg.Role)

	foundResult := false
	for _, block := range patchMsg.ContentBlocks {
		if block != nil && block.Type == schema.ContentBlockTypeFunctionToolResult &&
			block.FunctionToolResult != nil && block.FunctionToolResult.CallID == "call_1" {
			foundResult = true
			break
		}
	}
	assert.True(t, foundResult, "patched message should contain tool result for call_1")
}

func TestPatchedToolResultGenerator_ErrorPropagation(t *testing.T) {
	ctx := context.Background()
	wantErr := fmt.Errorf("boom")

	t.Run("Message", func(t *testing.T) {
		mw, err := New(ctx, &Config{
			PatchedToolResultGenerator: func(context.Context, string, string, *schema.ToolArgument) (*PatchedToolResult, error) {
				return nil, wantErr
			},
		})
		require.NoError(t, err)
		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.AssistantMessage("", []schema.ToolCall{
					{ID: "call_1", Function: schema.FunctionCall{Name: "tool_a", Arguments: "{}"}},
				}),
			},
		}
		_, _, err = mw.BeforeModelRewriteState(ctx, state, nil)
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("AgenticMessage", func(t *testing.T) {
		mw, err := NewTyped[*schema.AgenticMessage](ctx, &Config{
			PatchedToolResultGenerator: func(context.Context, string, string, *schema.ToolArgument) (*PatchedToolResult, error) {
				return nil, wantErr
			},
		})
		require.NoError(t, err)
		state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{
					{ID: "call_1", Name: "tool_a", Arguments: `{"a":1}`},
				}),
			},
		}
		_, _, err = mw.BeforeModelRewriteState(ctx, state, nil)
		require.ErrorIs(t, err, wantErr)
	})
}

func TestPatchedContentGenerator_ErrorPropagation(t *testing.T) {
	ctx := context.Background()
	wantErr := fmt.Errorf("legacy boom")

	t.Run("Message", func(t *testing.T) {
		mw, err := New(ctx, &Config{
			PatchedContentGenerator: func(context.Context, string, string) (string, error) {
				return "", wantErr
			},
		})
		require.NoError(t, err)
		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.AssistantMessage("", []schema.ToolCall{
					{ID: "call_1", Function: schema.FunctionCall{Name: "tool_a"}},
				}),
			},
		}
		_, _, err = mw.BeforeModelRewriteState(ctx, state, nil)
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("AgenticMessage", func(t *testing.T) {
		mw, err := NewTyped[*schema.AgenticMessage](ctx, &Config{
			PatchedContentGenerator: func(context.Context, string, string) (string, error) {
				return "", wantErr
			},
		})
		require.NoError(t, err)
		state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{
					{ID: "call_1", Name: "tool_a", Arguments: "{}"},
				}),
			},
		}
		_, _, err = mw.BeforeModelRewriteState(ctx, state, nil)
		require.ErrorIs(t, err, wantErr)
	})
}

func TestToolResultToMessage_NilAndEmpty(t *testing.T) {
	msg, err := toolResultToMessage("tool_a", "call_1", nil)
	require.NoError(t, err)
	assert.Equal(t, "call_1", msg.ToolCallID)
	assert.Equal(t, "tool_a", msg.ToolName)
	assert.Empty(t, msg.Content)
	assert.Empty(t, msg.UserInputMultiContent)

	msg, err = toolResultToMessage("tool_a", "call_1", &schema.ToolResult{})
	require.NoError(t, err)
	assert.Empty(t, msg.Content)
	assert.Empty(t, msg.UserInputMultiContent)
}

func TestToolResultToMessage_PlainTextSetsContentOnly(t *testing.T) {
	msg, err := toolResultToMessage("tool_a", "call_1", &schema.ToolResult{
		Parts: []schema.ToolOutputPart{{
			Type: schema.ToolPartTypeText,
			Text: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", msg.Content)
	assert.Empty(t, msg.UserInputMultiContent)
}

func TestToolResultToMessage_MultimodalDoesNotSetContent(t *testing.T) {
	url := "https://example.com/a.png"
	result := &schema.ToolResult{
		Parts: []schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: "caption"},
			{
				Type: schema.ToolPartTypeImage,
				Image: &schema.ToolOutputImage{
					MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "image/png"},
				},
			},
		},
	}
	msg, err := toolResultToMessage("vision", "call_1", result)
	require.NoError(t, err)
	assert.Empty(t, msg.Content, "multi-part results should not set Content")
	require.Len(t, msg.UserInputMultiContent, 2)
}

func TestToolResultToAgenticMessage_ToolSearchAndEmpty(t *testing.T) {
	search := &schema.ToolSearchResult{Tools: []*schema.ToolInfo{{Name: "dyn"}}}
	msg, err := toolResultToAgenticMessage("tool_search", "call_1", &schema.ToolResult{
		Parts: []schema.ToolOutputPart{{
			Type:             schema.ToolPartTypeToolSearchResult,
			ToolSearchResult: search,
		}},
	})
	require.NoError(t, err)
	require.Len(t, msg.ContentBlocks, 1)
	assert.Equal(t, schema.ContentBlockTypeToolSearchResult, msg.ContentBlocks[0].Type)
	require.NotNil(t, msg.ContentBlocks[0].ToolSearchFunctionToolResult)
	assert.Equal(t, search, msg.ContentBlocks[0].ToolSearchFunctionToolResult.Result)

	empty, err := toolResultToAgenticMessage("tool_a", "call_2", nil)
	require.NoError(t, err)
	require.Len(t, empty.ContentBlocks, 1)
	require.NotNil(t, empty.ContentBlocks[0].FunctionToolResult)
	require.Len(t, empty.ContentBlocks[0].FunctionToolResult.Content, 1)
	assert.Equal(t, "", empty.ContentBlocks[0].FunctionToolResult.Content[0].Text.Text)
}

func TestToolResultToAgenticMessage_MalformedPartsError(t *testing.T) {
	_, err := toolResultToAgenticMessage("vision", "call_1", &schema.ToolResult{
		Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeImage}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image content is nil")

	_, err = toolResultToAgenticMessage("tool_search", "call_2", &schema.ToolResult{
		Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeToolSearchResult}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool search result is nil")
}

func TestToolResultToFunctionBlocks_AllModalities(t *testing.T) {
	url := "https://example.com/x"
	b64 := "YmFzZTY0"
	result := &schema.ToolResult{
		Parts: []schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: "hello", Extra: map[string]any{"k": "v"}},
			{
				Type: schema.ToolPartTypeImage,
				Image: &schema.ToolOutputImage{
					MessagePartCommon: schema.MessagePartCommon{URL: &url, Base64Data: &b64, MIMEType: "image/png"},
				},
			},
			{
				Type: schema.ToolPartTypeAudio,
				Audio: &schema.ToolOutputAudio{
					MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "audio/mpeg"},
				},
			},
			{
				Type: schema.ToolPartTypeVideo,
				Video: &schema.ToolOutputVideo{
					MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "video/mp4"},
				},
			},
			{
				Type: schema.ToolPartTypeFile,
				File: &schema.ToolOutputFile{
					MessagePartCommon: schema.MessagePartCommon{URL: nil, Base64Data: &b64, MIMEType: "application/pdf"},
				},
			},
		},
	}

	blocks, err := toolResultToFunctionBlocks(result)
	require.NoError(t, err)
	require.Len(t, blocks, 5)
	assert.Equal(t, schema.FunctionToolResultContentBlockTypeText, blocks[0].Type)
	assert.Equal(t, "hello", blocks[0].Text.Text)
	assert.Equal(t, schema.FunctionToolResultContentBlockTypeImage, blocks[1].Type)
	assert.Equal(t, url, blocks[1].Image.URL)
	assert.Equal(t, b64, blocks[1].Image.Base64Data)
	assert.Equal(t, schema.FunctionToolResultContentBlockTypeAudio, blocks[2].Type)
	assert.Equal(t, schema.FunctionToolResultContentBlockTypeVideo, blocks[3].Type)
	assert.Equal(t, schema.FunctionToolResultContentBlockTypeFile, blocks[4].Type)
	assert.Equal(t, "", blocks[4].File.URL)
	assert.Equal(t, b64, blocks[4].File.Base64Data)

	blocks, err = toolResultToFunctionBlocks(nil)
	require.NoError(t, err)
	assert.Nil(t, blocks)
	blocks, err = toolResultToFunctionBlocks(&schema.ToolResult{})
	require.NoError(t, err)
	assert.Nil(t, blocks)
}

func TestToolResultToFunctionBlocks_MalformedPartsError(t *testing.T) {
	tests := []struct {
		name    string
		part    schema.ToolOutputPart
		wantSub string
	}{
		{name: "nil image", part: schema.ToolOutputPart{Type: schema.ToolPartTypeImage}, wantSub: "image content is nil"},
		{name: "nil audio", part: schema.ToolOutputPart{Type: schema.ToolPartTypeAudio}, wantSub: "audio content is nil"},
		{name: "nil video", part: schema.ToolOutputPart{Type: schema.ToolPartTypeVideo}, wantSub: "video content is nil"},
		{name: "nil file", part: schema.ToolOutputPart{Type: schema.ToolPartTypeFile}, wantSub: "file content is nil"},
		{name: "tool search mixed", part: schema.ToolOutputPart{Type: schema.ToolPartTypeToolSearchResult, ToolSearchResult: &schema.ToolSearchResult{}}, wantSub: "tool search result must be the sole part"},
		{name: "unknown type", part: schema.ToolOutputPart{Type: schema.ToolPartType("nope")}, wantSub: "unknown tool part type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := toolResultToFunctionBlocks(&schema.ToolResult{Parts: []schema.ToolOutputPart{tt.part}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

func TestPatchedToolResultGenerator_AgenticNilImagePropagatesError(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &Config{
		PatchedToolResultGenerator: func(context.Context, string, string, *schema.ToolArgument) (*PatchedToolResult, error) {
			return &PatchedToolResult{
				ToolResult: &schema.ToolResult{
					Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeImage}},
				},
			}, nil
		},
	})
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{
				{ID: "call_1", Name: "vision", Arguments: "{}"},
			}),
		},
	}
	_, _, err = mw.BeforeModelRewriteState(ctx, state, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image content is nil")
}

func TestPatchedToolResultGenerator_AgenticMultimodal(t *testing.T) {
	ctx := context.Background()
	url := "https://example.com/img.png"
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &Config{
		PatchedToolResultGenerator: func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*PatchedToolResult, error) {
			require.Equal(t, `{"q":1}`, toolArgument.Text)
			return &PatchedToolResult{
				ToolResult: &schema.ToolResult{
					Parts: []schema.ToolOutputPart{
						{Type: schema.ToolPartTypeText, Text: "see image"},
						{
							Type: schema.ToolPartTypeImage,
							Image: &schema.ToolOutputImage{
								MessagePartCommon: schema.MessagePartCommon{URL: &url},
							},
						},
					},
				},
			}, nil
		},
	})
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			makeAssistantMsgWithToolCalls[*schema.AgenticMessage]("", []testToolCall{
				{ID: "call_1", Name: "vision", Arguments: `{"q":1}`},
			}),
		},
	}
	_, newState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, newState.Messages, 2)
	patched := newState.Messages[1]
	require.Len(t, patched.ContentBlocks, 1)
	fr := patched.ContentBlocks[0].FunctionToolResult
	require.NotNil(t, fr)
	require.Len(t, fr.Content, 2)
	assert.Equal(t, "see image", fr.Content[0].Text.Text)
	assert.Equal(t, url, fr.Content[1].Image.URL)
}

func TestDerefString(t *testing.T) {
	assert.Equal(t, "", derefString(nil))
	s := "x"
	assert.Equal(t, "x", derefString(&s))
}
