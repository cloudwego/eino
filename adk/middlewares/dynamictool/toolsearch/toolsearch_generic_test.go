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

package toolsearch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Generic table-driven tests covering both *schema.Message and *schema.AgenticMessage
// ---------------------------------------------------------------------------

// --- Generic message construction helpers ---

func makeUserMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(content)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(content)).(M)
	default:
		panic("unreachable")
	}
}

func makeSystemMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{Role: schema.System, Content: content}).(M)
	case *schema.AgenticMessage:
		return any(schema.SystemAgenticMessage(content)).(M)
	default:
		panic("unreachable")
	}
}

type testToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func makeAssistantMsgWithToolCalls[M adk.MessageType](toolCalls []testToolCall) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		tcs := make([]schema.ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			tcs[i] = schema.ToolCall{
				ID:       tc.ID,
				Function: schema.FunctionCall{Name: tc.Name, Arguments: tc.Arguments},
			}
		}
		return any(schema.AssistantMessage("", tcs)).(M)
	case *schema.AgenticMessage:
		blocks := make([]*schema.ContentBlock, len(toolCalls))
		for i, tc := range toolCalls {
			blocks[i] = schema.NewContentBlock(&schema.FunctionToolCall{
				CallID:    tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			})
		}
		return any(&schema.AgenticMessage{
			Role:          schema.AgenticRoleTypeAssistant,
			ContentBlocks: blocks,
		}).(M)
	default:
		panic("unreachable")
	}
}

func makeToolResultMsg[M adk.MessageType](content string, callID string, toolName string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{
			Role:       schema.Tool,
			ToolName:   toolName,
			ToolCallID: callID,
			Content:    content,
		}).(M)
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
	default:
		panic("unreachable")
	}
}

func getMsgRole[M adk.MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return string(v.Role)
	case *schema.AgenticMessage:
		return string(v.Role)
	default:
		panic("unreachable")
	}
}

func getMsgContent[M adk.MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Content
	case *schema.AgenticMessage:
		for _, block := range v.ContentBlocks {
			if block != nil && block.Type == schema.ContentBlockTypeUserInputText && block.UserInputText != nil {
				return block.UserInputText.Text
			}
		}
		return ""
	default:
		panic("unreachable")
	}
}

func getMsgExtra[M adk.MessageType](msg M) map[string]any {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Extra
	case *schema.AgenticMessage:
		return v.Extra
	default:
		panic("unreachable")
	}
}

func setMsgExtra[M adk.MessageType](msg M, key string, val any) {
	switch v := any(msg).(type) {
	case *schema.Message:
		if v.Extra == nil {
			v.Extra = make(map[string]any)
		}
		v.Extra[key] = val
	case *schema.AgenticMessage:
		if v.Extra == nil {
			v.Extra = make(map[string]any)
		}
		v.Extra[key] = val
	default:
		panic("unreachable")
	}
}

func newTestMiddlewareTyped[M adk.MessageType](t *testing.T, tools []tool.BaseTool) *typedMiddleware[M] {
	t.Helper()
	ctx := context.Background()
	mw, err := NewTyped[M](ctx, &Config{
		DynamicTools:       tools,
		UseModelToolSearch: false,
	})
	require.NoError(t, err)
	return mw.(*typedMiddleware[M])
}

func countRemindersGeneric[M adk.MessageType](msgs []M) int {
	count := 0
	for _, msg := range msgs {
		extra := getMsgExtra(msg)
		if extra != nil {
			if v, _ := extra[toolSearchReminderExtraKey].(bool); v {
				count++
			}
		}
	}
	return count
}

// --- Generic test functions ---

func testEnsureReminderGeneric[M adk.MessageType](t *testing.T) {
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	m := newTestMiddlewareTyped[M](t, []tool.BaseTool{dynamicA})

	t.Run("normal: system then user", func(t *testing.T) {
		input := []M{
			makeSystemMsg[M]("sys"),
			makeUserMsg[M]("hi"),
		}
		got := m.ensureReminder(input)
		require.Len(t, got, 3)
		assert.Equal(t, "system", getMsgRole(got[0]))
		// Reminder inserted after system
		extra := getMsgExtra(got[1])
		require.NotNil(t, extra)
		assert.Equal(t, true, extra[toolSearchReminderExtraKey])
		assert.Equal(t, "hi", getMsgContent(got[2]))
	})

	t.Run("all system messages", func(t *testing.T) {
		input := []M{
			makeSystemMsg[M]("sys1"),
			makeSystemMsg[M]("sys2"),
		}
		got := m.ensureReminder(input)
		require.Len(t, got, 3)
		assert.Equal(t, "system", getMsgRole(got[0]))
		assert.Equal(t, "system", getMsgRole(got[1]))
		// Reminder appended at end
		extra := getMsgExtra(got[2])
		require.NotNil(t, extra)
		assert.Equal(t, true, extra[toolSearchReminderExtraKey])
	})

	t.Run("empty input", func(t *testing.T) {
		got := m.ensureReminder(nil)
		require.Len(t, got, 1)
		extra := getMsgExtra(got[0])
		require.NotNil(t, extra)
		assert.Equal(t, true, extra[toolSearchReminderExtraKey])
	})

	t.Run("no system messages", func(t *testing.T) {
		input := []M{
			makeUserMsg[M]("hi"),
		}
		got := m.ensureReminder(input)
		require.Len(t, got, 2)
		// Reminder inserted at position 0
		extra := getMsgExtra(got[0])
		require.NotNil(t, extra)
		assert.Equal(t, true, extra[toolSearchReminderExtraKey])
		assert.Equal(t, "hi", getMsgContent(got[1]))
	})

	t.Run("idempotent: does not insert twice", func(t *testing.T) {
		reminder := makeUserMsg[M]("<reminder>")
		setMsgExtra(reminder, toolSearchReminderExtraKey, true)
		input := []M{
			reminder,
			makeUserMsg[M]("hi"),
		}
		got := m.ensureReminder(input)
		require.Len(t, got, 2)
		assert.Equal(t, "hi", getMsgContent(got[1]))
	})
}

func testMode1InitializationGeneric[M adk.MessageType](t *testing.T) {
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}

	m := newTestMiddlewareTyped[M](t, []tool.BaseTool{dynamicA, dynamicB})

	ctx := context.Background()

	state := &adk.TypedChatModelAgentState[M]{
		Messages: []M{
			makeSystemMsg[M]("sys"),
			makeUserMsg[M]("hello"),
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
			ti("dynamic_tool_a", "Dynamic tool A"),
			ti("dynamic_tool_b", "Dynamic tool B"),
		},
	}

	// Initialization strips dynamic tools, keeps tool_search and static tools.
	_, state, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names := toolNames(state.ToolInfos)
	assert.Equal(t, []string{"static_tool", "tool_search"}, names)
	assert.Nil(t, state.DeferredToolInfos, "Mode 1 should not populate DeferredToolInfos")

	// Verify reminder was inserted.
	assert.Equal(t, 1, countRemindersGeneric(state.Messages), "reminder should be inserted")
}

func testMode1ForwardSelectionGeneric[M adk.MessageType](t *testing.T) {
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}

	m := newTestMiddlewareTyped[M](t, []tool.BaseTool{dynamicA, dynamicB})

	ctx := context.Background()

	// Simulate state AFTER initialization (dynamic tools already stripped).
	// Include a tool_search result message that selected dynamic_tool_a.
	toolSearchResultJSON, _ := json.Marshal(toolSearchResult{Matches: []string{"dynamic_tool_a"}})

	// Build the reminder message with the extra marker
	reminderMsg := makeUserMsg[M]("hello")
	setMsgExtra(reminderMsg, toolSearchReminderExtraKey, true)

	state := &adk.TypedChatModelAgentState[M]{
		Messages: []M{
			makeSystemMsg[M]("sys"),
			reminderMsg,
			makeAssistantMsgWithToolCalls[M]([]testToolCall{
				{ID: "tc1", Name: toolSearchToolName, Arguments: `{"query":"select:dynamic_tool_a"}`},
			}),
			makeToolResultMsg[M](string(toolSearchResultJSON), "tc1", toolSearchToolName),
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
		},
	}

	// Forward selection should add dynamic_tool_a from the tool_search result.
	_, state, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names := toolNames(state.ToolInfos)
	assert.Equal(t, []string{"dynamic_tool_a", "static_tool", "tool_search"}, names)

	// Call again: forward selection should be idempotent (dynamic_tool_a already present).
	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names = toolNames(state.ToolInfos)
	assert.Equal(t, []string{"dynamic_tool_a", "static_tool", "tool_search"}, names)
}

func testMalformedJSONGeneric[M adk.MessageType](t *testing.T) {
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}

	m := newTestMiddlewareTyped[M](t, []tool.BaseTool{dynamicA})

	ctx := context.Background()

	// Build the reminder message with the extra marker
	reminderMsg := makeUserMsg[M]("reminder")
	setMsgExtra(reminderMsg, toolSearchReminderExtraKey, true)

	state := &adk.TypedChatModelAgentState[M]{
		Messages: []M{
			makeSystemMsg[M]("sys"),
			reminderMsg,
			makeAssistantMsgWithToolCalls[M]([]testToolCall{
				{ID: "tc1", Name: toolSearchToolName, Arguments: `{"query":"select:dynamic_tool_a"}`},
			}),
			makeToolResultMsg[M](`{invalid json!!!`, "tc1", toolSearchToolName),
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
		},
	}

	_, state, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err, "malformed JSON in tool_search result should not cause an error")

	names := toolNames(state.ToolInfos)
	assert.NotContains(t, names, "dynamic_tool_a", "malformed JSON result should be skipped")
	assert.Contains(t, names, "static_tool")
	assert.Contains(t, names, "tool_search")
}

// --- Top-level generic test runner ---

func TestToolSearchGeneric(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		t.Run("EnsureReminder", testEnsureReminderGeneric[*schema.Message])
		t.Run("Mode1Init", testMode1InitializationGeneric[*schema.Message])
		t.Run("Mode1ForwardSelection", testMode1ForwardSelectionGeneric[*schema.Message])
		t.Run("MalformedJSON", testMalformedJSONGeneric[*schema.Message])
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		t.Run("EnsureReminder", testEnsureReminderGeneric[*schema.AgenticMessage])
		t.Run("Mode1Init", testMode1InitializationGeneric[*schema.AgenticMessage])
		t.Run("Mode1ForwardSelection", testMode1ForwardSelectionGeneric[*schema.AgenticMessage])
		t.Run("MalformedJSON", testMalformedJSONGeneric[*schema.AgenticMessage])
	})
}
