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
		{
			name: "custom tool result generator",
			config: &Config{
				PatchedToolResultGenerator: func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*schema.ToolResult, error) {
					argText := ""
					if toolArgument != nil {
						argText = toolArgument.Text
					}
					return &schema.ToolResult{
						Parts: []schema.ToolOutputPart{{
							Type: schema.ToolPartTypeText,
							Text: fmt.Sprintf("result %s %s %s", toolName, toolCallID, argText),
						}},
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
				PatchedToolResultGenerator: func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*schema.ToolResult, error) {
					return &schema.ToolResult{
						Parts: []schema.ToolOutputPart{{
							Type: schema.ToolPartTypeText,
							Text: "enhanced",
						}},
					}, nil
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

func TestPatchedToolResultGenerator_SetsContentAndMultiContent(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		PatchedToolResultGenerator: func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*schema.ToolResult, error) {
			require.Equal(t, "tool_a", toolName)
			require.Equal(t, "call_1", toolCallID)
			require.NotNil(t, toolArgument)
			require.Equal(t, `{"q":"hi"}`, toolArgument.Text)
			return &schema.ToolResult{
				Parts: []schema.ToolOutputPart{{
					Type: schema.ToolPartTypeText,
					Text: "patched text",
				}},
			}, nil
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
	require.Len(t, patched.UserInputMultiContent, 1)
	assert.Equal(t, schema.ChatMessagePartTypeText, patched.UserInputMultiContent[0].Type)
	assert.Equal(t, "patched text", patched.UserInputMultiContent[0].Text)
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
			PatchedToolResultGenerator: func(context.Context, string, string, *schema.ToolArgument) (*schema.ToolResult, error) {
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
			PatchedToolResultGenerator: func(context.Context, string, string, *schema.ToolArgument) (*schema.ToolResult, error) {
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
	msg := toolResultToAgenticMessage("tool_search", "call_1", &schema.ToolResult{
		Parts: []schema.ToolOutputPart{{
			Type:             schema.ToolPartTypeToolSearchResult,
			ToolSearchResult: search,
		}},
	})
	require.Len(t, msg.ContentBlocks, 1)
	assert.Equal(t, schema.ContentBlockTypeToolSearchResult, msg.ContentBlocks[0].Type)
	require.NotNil(t, msg.ContentBlocks[0].ToolSearchFunctionToolResult)
	assert.Equal(t, search, msg.ContentBlocks[0].ToolSearchFunctionToolResult.Result)

	empty := toolResultToAgenticMessage("tool_a", "call_2", nil)
	require.Len(t, empty.ContentBlocks, 1)
	require.NotNil(t, empty.ContentBlocks[0].FunctionToolResult)
	require.Len(t, empty.ContentBlocks[0].FunctionToolResult.Content, 1)
	assert.Equal(t, "", empty.ContentBlocks[0].FunctionToolResult.Content[0].Text.Text)
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
			{Type: schema.ToolPartTypeImage}, // nil Image skipped
			{
				Type: schema.ToolPartTypeAudio,
				Audio: &schema.ToolOutputAudio{
					MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "audio/mpeg"},
				},
			},
			{Type: schema.ToolPartTypeAudio},
			{
				Type: schema.ToolPartTypeVideo,
				Video: &schema.ToolOutputVideo{
					MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "video/mp4"},
				},
			},
			{Type: schema.ToolPartTypeVideo},
			{
				Type: schema.ToolPartTypeFile,
				File: &schema.ToolOutputFile{
					MessagePartCommon: schema.MessagePartCommon{URL: nil, Base64Data: &b64, MIMEType: "application/pdf"},
				},
			},
			{Type: schema.ToolPartTypeFile},
			{Type: schema.ToolPartTypeToolSearchResult}, // ignored by function-block conversion
		},
	}

	blocks := toolResultToFunctionBlocks(result)
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

	assert.Nil(t, toolResultToFunctionBlocks(nil))
	assert.Nil(t, toolResultToFunctionBlocks(&schema.ToolResult{}))
}

func TestPatchedToolResultGenerator_AgenticMultimodal(t *testing.T) {
	ctx := context.Background()
	url := "https://example.com/img.png"
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &Config{
		PatchedToolResultGenerator: func(ctx context.Context, toolName, toolCallID string, toolArgument *schema.ToolArgument) (*schema.ToolResult, error) {
			require.Equal(t, `{"q":1}`, toolArgument.Text)
			return &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: "see image"},
					{
						Type: schema.ToolPartTypeImage,
						Image: &schema.ToolOutputImage{
							MessagePartCommon: schema.MessagePartCommon{URL: &url},
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
