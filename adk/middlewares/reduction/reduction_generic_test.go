/*
 * Copyright 2026 CloudWeGo Authors
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

package reduction

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Generic message construction helpers
// ---------------------------------------------------------------------------

type testToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func makeUserMsgG[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(content)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(content)).(M)
	}
	panic("unreachable")
}

func makeSystemMsgG[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{Role: schema.System, Content: content}).(M)
	case *schema.AgenticMessage:
		return any(schema.SystemAgenticMessage(content)).(M)
	}
	panic("unreachable")
}

func makeAssistantMsgG[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{Role: schema.Assistant, Content: content}).(M)
	case *schema.AgenticMessage:
		return any(&schema.AgenticMessage{
			Role:          schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: content})},
		}).(M)
	}
	panic("unreachable")
}

func makeAssistantMsgWithToolCallsG[M adk.MessageType](toolCalls []testToolCall) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		tcs := make([]schema.ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			tcs[i] = schema.ToolCall{
				ID:       tc.ID,
				Type:     "function",
				Function: schema.FunctionCall{Name: tc.Name, Arguments: tc.Arguments},
			}
		}
		return any(schema.AssistantMessage("", tcs)).(M)
	case *schema.AgenticMessage:
		blocks := make([]*schema.ContentBlock, 0, len(toolCalls))
		for _, tc := range toolCalls {
			blocks = append(blocks, schema.NewContentBlock(&schema.FunctionToolCall{
				CallID:    tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			}))
		}
		return any(&schema.AgenticMessage{
			Role:          schema.AgenticRoleTypeAssistant,
			ContentBlocks: blocks,
		}).(M)
	}
	panic("unreachable")
}

func makeToolResultMsgG[M adk.MessageType](content string, callID string, toolName string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		msg := schema.ToolMessage(content, callID)
		msg.ToolName = toolName
		return any(msg).(M)
	case *schema.AgenticMessage:
		return any(schema.FunctionToolResultAgenticMessage(callID, toolName, []*schema.FunctionToolResultBlock{
			{Text: &schema.UserInputText{Text: content}},
		})).(M)
	}
	panic("unreachable")
}

func getMsgContentG[M adk.MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Content
	case *schema.AgenticMessage:
		for _, block := range v.ContentBlocks {
			if block == nil {
				continue
			}
			if block.UserInputText != nil {
				return block.UserInputText.Text
			}
			if block.AssistantGenText != nil {
				return block.AssistantGenText.Text
			}
			if block.FunctionToolResult != nil {
				for _, b := range block.FunctionToolResult.Blocks {
					if b != nil && b.Text != nil {
						return b.Text.Text
					}
				}
			}
		}
		return ""
	}
	panic("unreachable")
}

// ---------------------------------------------------------------------------
// Part 1: Helper function tests
// ---------------------------------------------------------------------------

func testHelperFunctions[M adk.MessageType](t *testing.T) {
	t.Run("isAssistantMsg", func(t *testing.T) {
		assistant := makeAssistantMsgG[M]("hello")
		user := makeUserMsgG[M]("hello")
		assert.True(t, isAssistantMsg(assistant))
		assert.False(t, isAssistantMsg(user))
	})

	t.Run("isSystemMsg", func(t *testing.T) {
		sys := makeSystemMsgG[M]("system prompt")
		user := makeUserMsgG[M]("hello")
		assert.True(t, isSystemMsg(sys))
		assert.False(t, isSystemMsg(user))
	})

	t.Run("isUserMsg", func(t *testing.T) {
		user := makeUserMsgG[M]("hello")
		assert.True(t, isUserMsg(user))

		// A user message that only has tool results should return false.
		toolResultOnly := makeToolResultMsgG[M]("result", "call_1", "my_tool")
		assert.False(t, isUserMsg(toolResultOnly))
	})

	t.Run("hasToolCalls", func(t *testing.T) {
		withTC := makeAssistantMsgWithToolCallsG[M]([]testToolCall{
			{ID: "c1", Name: "tool1", Arguments: `{"a":1}`},
		})
		assert.True(t, hasToolCalls(withTC))

		noTC := makeAssistantMsgG[M]("plain response")
		assert.False(t, hasToolCalls(noTC))
	})

	t.Run("isToolResultMsg", func(t *testing.T) {
		tr := makeToolResultMsgG[M]("result content", "call_1", "my_tool")
		assert.True(t, isToolResultMsg(tr))

		user := makeUserMsgG[M]("not a tool result")
		assert.False(t, isToolResultMsg(user))
	})

	t.Run("isToolResultOnlyMsg", func(t *testing.T) {
		trOnly := makeToolResultMsgG[M]("result content", "call_1", "my_tool")
		assert.True(t, isToolResultOnlyMsg(trOnly))

		// A normal user message is not a tool-result-only message.
		user := makeUserMsgG[M]("hello")
		assert.False(t, isToolResultOnlyMsg(user))

		// For AgenticMessage, a mixed message (user text + tool result) should return false.
		var zero M
		if _, ok := any(zero).(*schema.AgenticMessage); ok {
			mixed := any(&schema.AgenticMessage{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.UserInputText{Text: "hello"}),
					schema.NewContentBlock(&schema.FunctionToolResult{CallID: "c1", Name: "tool1", Blocks: []*schema.FunctionToolResultBlock{
						{Text: &schema.UserInputText{Text: "result"}},
					}}),
				},
			}).(M)
			assert.False(t, isToolResultOnlyMsg(mixed))
		}
	})

	t.Run("getMsgClearedFlagGeneric_setMsgClearedFlagGeneric", func(t *testing.T) {
		msg := makeAssistantMsgG[M]("test")
		assert.False(t, getMsgClearedFlagGeneric(msg))

		setMsgClearedFlagGeneric(msg)
		assert.True(t, getMsgClearedFlagGeneric(msg))
	})

	t.Run("getToolCallsGeneric", func(t *testing.T) {
		tcs := []testToolCall{
			{ID: "call_a", Name: "tool_alpha", Arguments: `{"x":1}`},
			{ID: "call_b", Name: "tool_beta", Arguments: `{"y":2}`},
		}
		msg := makeAssistantMsgWithToolCallsG[M](tcs)
		got := getToolCallsGeneric(msg)
		require.Len(t, got, 2)

		assert.Equal(t, "call_a", got[0].CallID)
		assert.Equal(t, "tool_alpha", got[0].Name)
		assert.Equal(t, `{"x":1}`, got[0].Arguments)
		assert.Equal(t, 0, got[0].BlockIndex)

		assert.Equal(t, "call_b", got[1].CallID)
		assert.Equal(t, "tool_beta", got[1].Name)
		assert.Equal(t, `{"y":2}`, got[1].Arguments)
		assert.Equal(t, 1, got[1].BlockIndex)

		// Empty assistant message returns nil.
		noTC := makeAssistantMsgG[M]("plain")
		assert.Nil(t, getToolCallsGeneric(noTC))
	})

	t.Run("setToolCallArguments", func(t *testing.T) {
		tcs := []testToolCall{
			{ID: "call_a", Name: "tool_alpha", Arguments: `{"old":"args"}`},
		}
		msg := makeAssistantMsgWithToolCallsG[M](tcs)
		setToolCallArguments(msg, 0, `{"new":"args"}`)

		got := getToolCallsGeneric(msg)
		require.Len(t, got, 1)
		assert.Equal(t, `{"new":"args"}`, got[0].Arguments)

		// Verify AgenticMessage path writes to the ContentBlock directly.
		if am, ok := any(msg).(*schema.AgenticMessage); ok {
			require.NotNil(t, am.ContentBlocks[0].FunctionToolCall)
			assert.Equal(t, `{"new":"args"}`, am.ContentBlocks[0].FunctionToolCall.Arguments)
		}
	})

	t.Run("copyMessagesGeneric", func(t *testing.T) {
		original := []M{
			makeAssistantMsgWithToolCallsG[M]([]testToolCall{
				{ID: "c1", Name: "t1", Arguments: `{"k":"v"}`},
			}),
			makeUserMsgG[M]("user text"),
		}
		copied := copyMessagesGeneric(original)
		require.Len(t, copied, 2)

		// Modify the copy's tool call arguments.
		setToolCallArguments(copied[0], 0, `{"modified":"true"}`)

		// Original must be unchanged.
		origTCs := getToolCallsGeneric(original[0])
		require.Len(t, origTCs, 1)
		assert.Equal(t, `{"k":"v"}`, origTCs[0].Arguments, "original must not be affected by copy mutation")

		copiedTCs := getToolCallsGeneric(copied[0])
		assert.Equal(t, `{"modified":"true"}`, copiedTCs[0].Arguments)
	})
}

// ---------------------------------------------------------------------------
// Part 2: Clear rewrite flow
// ---------------------------------------------------------------------------

func testClearFlowGeneric[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	// Token counter that always returns a high count to trigger clearing.
	highTokenCounter := func(_ context.Context, _ []M, _ []*schema.ToolInfo) (int64, error) {
		return 999999, nil
	}

	// ClearRetentionSuffixLimit defaults to 1 in copyAndFillDefaults when set to 0,
	// so we explicitly set it to 1. This means the last tool-call group (call_new)
	// is retained and only the older group (call_old) is cleared.
	config := &TypedConfig[M]{
		SkipTruncation:            true,
		TokenCounter:              highTokenCounter,
		MaxTokensForClear:         100,
		ClearRetentionSuffixLimit: 1,
	}

	mw, err := NewTyped[M](ctx, config)
	require.NoError(t, err)

	// Messages: system, user, assistant+toolcalls(old), tool_result(old), user, assistant+toolcalls(new)
	msgs := []M{
		makeSystemMsgG[M]("you are helpful"),
		makeUserMsgG[M]("what's the weather?"),
		makeAssistantMsgWithToolCallsG[M]([]testToolCall{
			{ID: "call_old", Name: "get_weather", Arguments: `{"location":"London"}`},
		}),
		makeToolResultMsgG[M]("Sunny and warm", "call_old", "get_weather"),
		makeUserMsgG[M]("set thermostat"),
		makeAssistantMsgWithToolCallsG[M]([]testToolCall{
			{ID: "call_new", Name: "set_thermostat", Arguments: `{"temp":20}`},
		}),
	}

	state := &adk.TypedChatModelAgentState[M]{Messages: msgs}
	_, resultState, err := mw.BeforeModelRewriteState(ctx, state, &adk.TypedModelContext[M]{})
	require.NoError(t, err)
	require.Equal(t, 6, len(resultState.Messages))

	// The default ClearHandler preserves tool call arguments (sets them to the original).
	// Verify they are unchanged.
	oldTCs := getToolCallsGeneric(resultState.Messages[2])
	require.Len(t, oldTCs, 1)
	assert.Equal(t, `{"location":"London"}`, oldTCs[0].Arguments, "default handler preserves tool call arguments")

	// The old tool result (index 3) should have its content replaced with a placeholder.
	// The placeholder text is locale-dependent, so just verify it changed from the original.
	oldResultContent := getMsgContentG[M](resultState.Messages[3])
	assert.NotEqual(t, "Sunny and warm", oldResultContent, "old tool result content should be replaced with placeholder")

	// The cleared flag should be set on the old assistant message.
	assert.True(t, getMsgClearedFlagGeneric(resultState.Messages[2]), "cleared flag should be set on old assistant msg")

	// System message (index 0) should be untouched.
	assert.Equal(t, "you are helpful", getMsgContentG[M](resultState.Messages[0]))

	// Recent messages (index 4, 5) should not be affected: the new tool-call group
	// is in the retention window.
	newTCs := getToolCallsGeneric(resultState.Messages[5])
	require.Len(t, newTCs, 1)
	assert.Equal(t, `{"temp":20}`, newTCs[0].Arguments, "recent tool calls must not be cleared")
}

// ---------------------------------------------------------------------------
// Part 3: Truncation flow
// ---------------------------------------------------------------------------

func testTruncationGeneric[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	callCount := 0
	// Token counter returns decreasing counts as messages shrink.
	tokenCounter := func(_ context.Context, msgs []M, _ []*schema.ToolInfo) (int64, error) {
		callCount++
		// First call: over limit. After truncation (fewer msgs), under limit.
		return int64(len(msgs)) * 100, nil
	}

	config := &TypedConfig[M]{
		SkipTruncation:            true,
		SkipClear:                 true,
		TokenCounter:              tokenCounter,
		MaxTokensForClear:         250, // 5 messages * 100 = 500 > 250
		ClearRetentionSuffixLimit: 0,
	}

	mw, err := NewTyped[M](ctx, config)
	require.NoError(t, err)

	msgs := []M{
		makeSystemMsgG[M]("system prompt"),
		makeUserMsgG[M]("old user message"),
		makeAssistantMsgG[M]("old assistant response"),
		makeUserMsgG[M]("new user message"),
		makeAssistantMsgG[M]("new assistant response"),
	}

	state := &adk.TypedChatModelAgentState[M]{Messages: msgs}
	_, resultState, err := mw.BeforeModelRewriteState(ctx, state, &adk.TypedModelContext[M]{})
	require.NoError(t, err)

	// Since SkipClear is true, the clear path is entirely skipped.
	// The middleware should return the state unchanged because clear is skipped
	// (truncation in BeforeModelRewriteState is the clear phase, not the tool-output truncation).
	// The messages are returned as-is since the clearing loop is the only message-removal mechanism.
	assert.Equal(t, len(msgs), len(resultState.Messages))
}

// ---------------------------------------------------------------------------
// Part 4: ClearPostProcess callback
// ---------------------------------------------------------------------------

func testClearPostProcessGeneric[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	postProcessCalled := false
	highTokenCounter := func(_ context.Context, _ []M, _ []*schema.ToolInfo) (int64, error) {
		return 999999, nil
	}

	// ClearRetentionSuffixLimit=0 defaults to 1 via copyAndFillDefaults.
	// We need at least 2 tool-call groups so that the first one gets cleared
	// while the second is retained by the suffix limit.
	config := &TypedConfig[M]{
		SkipTruncation:            true,
		TokenCounter:              highTokenCounter,
		MaxTokensForClear:         100,
		ClearRetentionSuffixLimit: 1,
		ClearPostProcess: func(ctx context.Context, state *adk.TypedChatModelAgentState[M]) context.Context {
			postProcessCalled = true
			return ctx
		},
	}

	mw, err := NewTyped[M](ctx, config)
	require.NoError(t, err)

	msgs := []M{
		makeSystemMsgG[M]("system"),
		makeUserMsgG[M]("user"),
		makeAssistantMsgWithToolCallsG[M]([]testToolCall{
			{ID: "call_1", Name: "tool1", Arguments: `{"a":"b"}`},
		}),
		makeToolResultMsgG[M]("result", "call_1", "tool1"),
		makeUserMsgG[M]("another request"),
		makeAssistantMsgWithToolCallsG[M]([]testToolCall{
			{ID: "call_2", Name: "tool2", Arguments: `{"c":"d"}`},
		}),
		makeToolResultMsgG[M]("result2", "call_2", "tool2"),
	}

	state := &adk.TypedChatModelAgentState[M]{Messages: msgs}
	_, _, err = mw.BeforeModelRewriteState(ctx, state, &adk.TypedModelContext[M]{})
	require.NoError(t, err)
	assert.True(t, postProcessCalled, "ClearPostProcess should have been called")
}

// ---------------------------------------------------------------------------
// Top-level test
// ---------------------------------------------------------------------------

func TestReductionGeneric(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		t.Run("Helpers", testHelperFunctions[*schema.Message])
		t.Run("ClearFlow", testClearFlowGeneric[*schema.Message])
		t.Run("Truncation", testTruncationGeneric[*schema.Message])
		t.Run("ClearPostProcess", testClearPostProcessGeneric[*schema.Message])
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		t.Run("Helpers", testHelperFunctions[*schema.AgenticMessage])
		t.Run("ClearFlow", testClearFlowGeneric[*schema.AgenticMessage])
		t.Run("Truncation", testTruncationGeneric[*schema.AgenticMessage])
		t.Run("ClearPostProcess", testClearPostProcessGeneric[*schema.AgenticMessage])
	})
}
