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

package summarization

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// =============================================================================
// Attack tests for getAssistantTextContent
// =============================================================================

func TestAttack_GetAssistantTextContent_BothContentAndMultiContent(t *testing.T) {
	// When both Content and AssistantGenMultiContent are populated,
	// the function should prefer AssistantGenMultiContent.
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: "plain content fallback",
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "multi part 1"},
			{Type: schema.ChatMessagePartTypeText, Text: "multi part 2"},
		},
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "multi part 1\nmulti part 2", result)
	assert.NotContains(t, result, "plain content fallback",
		"should prefer AssistantGenMultiContent over Content field")
}

func TestAttack_GetAssistantTextContent_FallbackToContent(t *testing.T) {
	// When AssistantGenMultiContent is empty, should fall back to Content.
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: "fallback content",
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "fallback content", result)
}

func TestAttack_GetAssistantTextContent_EmptyMultiContentParts(t *testing.T) {
	// When AssistantGenMultiContent has parts but all have empty Text,
	// the function should fall back to Content.
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: "should use this",
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeText, Text: ""},
			{Type: schema.ChatMessagePartTypeImageURL}, // non-text type
		},
	}

	result := getAssistantTextContent(msg)
	// Empty text parts are filtered, so no parts collected → falls back to Content
	assert.Equal(t, "should use this", result)
}

func TestAttack_GetAssistantTextContent_MultiContentWithNonTextTypes(t *testing.T) {
	// Non-text parts in AssistantGenMultiContent should be ignored.
	msg := &schema.Message{
		Role: schema.Assistant,
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeImageURL},
			{Type: schema.ChatMessagePartTypeText, Text: "actual text"},
			{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "reasoning"}},
		},
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "actual text", result, "should only extract text parts")
}

func TestAttack_GetAssistantTextContent_AgenticMessage_NilBlocks(t *testing.T) {
	// AgenticMessage with nil blocks in ContentBlocks should not panic.
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			nil,
			schema.NewContentBlock(&schema.AssistantGenText{Text: "hello"}),
			nil,
			schema.NewContentBlock(&schema.AssistantGenText{Text: "world"}),
		},
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "hello\nworld", result)
}

func TestAttack_GetAssistantTextContent_AgenticMessage_NonTextBlocks(t *testing.T) {
	// AgenticMessage with non-text blocks (tool calls, images, etc.) should only get text.
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolCall{Name: "tool1", Arguments: "{}"}),
			schema.NewContentBlock(&schema.AssistantGenText{Text: "response text"}),
			schema.NewContentBlock(&schema.Reasoning{Text: "reasoning text"}),
		},
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "response text", result, "should only extract AssistantGenText blocks")
}

func TestAttack_GetAssistantTextContent_AgenticMessage_EmptyBlocks(t *testing.T) {
	// AgenticMessage with empty ContentBlocks should return empty string.
	msg := &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{},
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "", result)
}

func TestAttack_GetAssistantTextContent_AgenticMessage_NilAssistantGenText(t *testing.T) {
	// Block with Type == AssistantGenText but nil AssistantGenText field.
	// The code checks `block.AssistantGenText != nil` so this should be safe.
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{Type: schema.ContentBlockTypeAssistantGenText, AssistantGenText: nil},
			schema.NewContentBlock(&schema.AssistantGenText{Text: "valid"}),
		},
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "valid", result)
}

// =============================================================================
// Attack tests for postProcessSummary with edge-case contextMsgs
// =============================================================================

func TestAttack_PostProcessSummary_EmptyContextMsgs(t *testing.T) {
	// When contextMsgs is empty (len==0), replaceUserMessagesInSummary is skipped.
	ctx := context.Background()

	summaryContent := "Summary with <all_user_messages>old content</all_user_messages> tag"
	result, err := postProcessSummary(ctx, &postProcessSummaryParams[*schema.Message]{
		contextMsgs:    nil,
		summaryContent: summaryContent,
	})
	require.NoError(t, err)

	// The <all_user_messages> tag should NOT be replaced because contextMsgs is empty
	text := getUserMsgTextContent(result)
	assert.Contains(t, text, "<all_user_messages>old content</all_user_messages>")
}

func TestAttack_PostProcessSummary_AllContextMsgsAreSummaries(t *testing.T) {
	// contextMsgs is non-empty but all messages have contentTypeSummary.
	// replaceUserMessagesInSummary WILL be called (len > 0), but inside it
	// all messages are filtered out because they have summary content type.
	// The function should gracefully return the original summary text unchanged.
	ctx := context.Background()

	summaryMsg := &schema.Message{
		Role:    schema.User,
		Content: "previous summary content",
		Extra:   map[string]any{extraKeyContentType: string(contentTypeSummary)},
	}

	summaryContent := "New summary with <all_user_messages>placeholder</all_user_messages>"
	result, err := postProcessSummary(ctx, &postProcessSummaryParams[*schema.Message]{
		contextMsgs:    []*schema.Message{summaryMsg},
		summaryContent: summaryContent,
	})
	require.NoError(t, err)

	// Since all msgs are summaries, hasUserMsgs is false, so original text is preserved.
	text := getUserMsgTextContent(result)
	assert.Contains(t, text, "<all_user_messages>placeholder</all_user_messages>",
		"tag should not be replaced when all context msgs are summaries")
}

func TestAttack_PostProcessSummary_ContextMsgsNoUserMessages(t *testing.T) {
	// contextMsgs has messages but none are user role.
	ctx := context.Background()

	assistantMsg := &schema.Message{
		Role:    schema.Assistant,
		Content: "assistant response",
	}

	summaryContent := "Summary <all_user_messages>content</all_user_messages>"
	result, err := postProcessSummary(ctx, &postProcessSummaryParams[*schema.Message]{
		contextMsgs:    []*schema.Message{assistantMsg},
		summaryContent: summaryContent,
	})
	require.NoError(t, err)

	text := getUserMsgTextContent(result)
	// No user messages found, so original tag preserved
	assert.Contains(t, text, "<all_user_messages>content</all_user_messages>")
}

// =============================================================================
// Attack tests for buildInternalFinalizer + DefaultFinalize parity
// =============================================================================

func TestAttack_BuildInternalFinalizer_DefaultFinalize_Parity(t *testing.T) {
	// When TranscriptFilePath is empty, buildInternalFinalizer and DefaultFinalize
	// should produce identical results.
	ctx := context.Background()

	systemMsg := schema.SystemMessage("You are a helpful assistant.")
	userMsg := &schema.Message{Role: schema.User, Content: "Hello, please help me."}
	assistantReply := &schema.Message{
		Role:    schema.Assistant,
		Content: "summary of conversation",
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "summary of conversation"},
		},
	}

	originalMsgs := []*schema.Message{systemMsg, userMsg}

	cfg := &TypedConfig[*schema.Message]{
		TranscriptFilePath: "",
	}

	internalFinalizer := buildInternalFinalizer(cfg)

	result1, err := internalFinalizer(ctx, originalMsgs, assistantReply)
	require.NoError(t, err)

	result2, err := DefaultFinalize(ctx, originalMsgs, assistantReply)
	require.NoError(t, err)

	require.Equal(t, len(result1), len(result2), "should produce same number of messages")
	for i := range result1 {
		text1 := getUserMsgTextContent(result1[i])
		text2 := getUserMsgTextContent(result2[i])
		assert.Equal(t, text1, text2, "message %d content should be identical", i)
	}
}

func TestAttack_BuildInternalFinalizer_WithTranscriptPath(t *testing.T) {
	// With TranscriptFilePath set, buildInternalFinalizer should include transcript path
	// instruction, while DefaultFinalize should NOT include it.
	ctx := context.Background()

	userMsg := &schema.Message{Role: schema.User, Content: "hello"}
	assistantReply := &schema.Message{
		Role:    schema.Assistant,
		Content: "summary text",
	}
	originalMsgs := []*schema.Message{userMsg}

	cfg := &TypedConfig[*schema.Message]{
		TranscriptFilePath: "/path/to/transcript.md",
	}

	internalFinalizer := buildInternalFinalizer(cfg)
	result1, err := internalFinalizer(ctx, originalMsgs, assistantReply)
	require.NoError(t, err)

	result2, err := DefaultFinalize(ctx, originalMsgs, assistantReply)
	require.NoError(t, err)

	text1 := getUserMsgTextContent(result1[0])
	text2 := getUserMsgTextContent(result2[0])

	assert.Contains(t, text1, "/path/to/transcript.md",
		"internal finalizer should include transcript path")
	assert.NotContains(t, text2, "/path/to/transcript.md",
		"DefaultFinalize should NOT include transcript path")
}

// =============================================================================
// Attack tests for token budget overflow
// =============================================================================

func TestAttack_TokenBudgetOverflow_SingleLargeMessage(t *testing.T) {
	// A single user message with >30000 tokens (>120000 chars at 4 chars/token).
	// The trimming logic should handle this via defaultTypedTrimUserMessage.
	ctx := context.Background()

	// Create a message much larger than 30000 tokens (> 120000 chars)
	largeContent := strings.Repeat("x", 150000) // ~37500 tokens

	userMsg := &schema.Message{Role: schema.User, Content: largeContent}
	summaryText := "Summary <all_user_messages>placeholder</all_user_messages>"

	result, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[*schema.Message]{
		contextMsgs: []*schema.Message{userMsg},
		summaryText: summaryText,
	})
	require.NoError(t, err)

	// Since there's only 1 user message, selected = userMsgs (no trimming in that branch)
	// The code takes len(userMsgs)==1 as a special case: selected = userMsgs directly.
	assert.Contains(t, result, "<all_user_messages>")
	assert.Contains(t, result, "</all_user_messages>")
}

func TestAttack_TokenBudgetOverflow_MultipleMessagesExceedBudget(t *testing.T) {
	// Multiple user messages where each exceeds 30000 tokens.
	// The trimming should kick in for the second message that crosses the budget.
	ctx := context.Background()

	// Each message ~10000 tokens (40000 chars); 4 of them = 40000 tokens > 30000 budget
	msgContent := strings.Repeat("a", 40000)
	msgs := make([]*schema.Message, 4)
	for i := range msgs {
		msgs[i] = &schema.Message{Role: schema.User, Content: msgContent}
	}

	summaryText := "Summary <all_user_messages>old</all_user_messages>"

	result, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[*schema.Message]{
		contextMsgs: msgs,
		summaryText: summaryText,
	})
	require.NoError(t, err)

	// The result should contain the replacement and a note about cleared messages
	assert.Contains(t, result, "<all_user_messages>")
	assert.Contains(t, result, "</all_user_messages>")
}

func TestAttack_TokenBudgetOverflow_TrimUserMessage(t *testing.T) {
	// Verify defaultTypedTrimUserMessage with remaining budget > 0 produces truncated content.
	largeContent := strings.Repeat("y", 200000) // ~50000 tokens
	msg := &schema.Message{Role: schema.User, Content: largeContent}

	trimmed := defaultTypedTrimUserMessage(msg, 100) // very small remaining budget
	text := getUserMsgTextContent(trimmed)
	assert.NotEmpty(t, text, "trimmed message should not be empty")
	assert.Less(t, len(text), len(largeContent), "trimmed should be shorter")
}

func TestAttack_TokenBudgetOverflow_TrimUserMessageZeroBudget(t *testing.T) {
	// With 0 remaining tokens, defaultTypedTrimUserMessage should return zero.
	msg := &schema.Message{Role: schema.User, Content: "hello world"}

	trimmed := defaultTypedTrimUserMessage[*schema.Message](msg, 0)
	assert.Nil(t, trimmed, "zero budget should return nil message")
}

// =============================================================================
// Attack tests for newTypedSummaryMessage metadata
// =============================================================================

func TestAttack_NewTypedSummaryMessage_ExtraMetadata(t *testing.T) {
	// Verify the summary message has the correct extraKeyContentType set
	// so recursive summarization doesn't re-process it.
	msg := newTypedSummaryMessage[*schema.Message]("test summary content")

	assert.NotNil(t, msg.Extra)
	ct, ok := msg.Extra[extraKeyContentType].(string)
	require.True(t, ok, "extra should contain content type key")
	assert.Equal(t, string(contentTypeSummary), ct)
}

func TestAttack_NewTypedSummaryMessage_AgenticExtraMetadata(t *testing.T) {
	// Verify AgenticMessage variant also gets proper metadata.
	msg := newTypedSummaryMessage[*schema.AgenticMessage]("test agentic summary")

	assert.NotNil(t, msg.Extra)
	ct, ok := msg.Extra[extraKeyContentType].(string)
	require.True(t, ok, "extra should contain content type key")
	assert.Equal(t, string(contentTypeSummary), ct)
}

func TestAttack_NewTypedSummaryMessage_IsFilteredBySummarizationCheck(t *testing.T) {
	// Verify that typedGetContentType correctly identifies summary messages,
	// ensuring they are skipped in replaceUserMessagesInSummary.
	msg := newTypedSummaryMessage[*schema.Message]("summary content")

	ct := typedGetContentType(msg)
	assert.Equal(t, contentTypeSummary, ct)
}

// =============================================================================
// Attack tests for appendSection concatenation correctness
// =============================================================================

func TestAttack_AppendSection_BothNonEmpty(t *testing.T) {
	result := appendSection("first part", "second part")
	assert.Equal(t, "first part\n\nsecond part", result)
}

func TestAttack_AppendSection_BaseEmpty(t *testing.T) {
	result := appendSection("", "only section")
	assert.Equal(t, "only section", result)
}

func TestAttack_AppendSection_SectionEmpty(t *testing.T) {
	result := appendSection("only base", "")
	assert.Equal(t, "only base", result)
}

func TestAttack_AppendSection_BothEmpty(t *testing.T) {
	result := appendSection("", "")
	assert.Equal(t, "", result)
}

func TestAttack_AppendSection_FinalMessageWellFormed(t *testing.T) {
	// Simulate the actual postProcessSummary concatenation flow:
	// preamble + content + continueInstruction
	preamble := getSummaryPreamble()
	content := "Summary body text"
	continueInstr := getContinueInstruction()

	step1 := appendSection(preamble, content)
	final := appendSection(step1, continueInstr)

	// Verify structure: preamble, double newline, content, double newline, continue
	parts := strings.Split(final, "\n\n")
	assert.GreaterOrEqual(t, len(parts), 3,
		"final message should have at least 3 sections separated by double newlines")
	assert.Equal(t, preamble, parts[0])
}

// =============================================================================
// Attack tests for AgenticMessage path in getAssistantTextContent
// =============================================================================

func TestAttack_GetAssistantTextContent_AgenticMessage_AllNilBlocks(t *testing.T) {
	// All blocks are nil — should not panic and return empty string.
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			nil, nil, nil,
		},
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "", result)
}

func TestAttack_GetAssistantTextContent_AgenticMessage_MixedBlocksWithEmptyText(t *testing.T) {
	// Mix of valid and empty-text AssistantGenText blocks.
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: ""}),
			schema.NewContentBlock(&schema.AssistantGenText{Text: "non-empty"}),
			schema.NewContentBlock(&schema.AssistantGenText{Text: ""}),
			schema.NewContentBlock(&schema.AssistantGenText{Text: "also valid"}),
		},
	}

	result := getAssistantTextContent(msg)
	// The code does NOT filter empty text for AgenticMessage — it joins all AssistantGenText.Text
	// including empty ones with "\n"
	assert.Contains(t, result, "non-empty")
	assert.Contains(t, result, "also valid")
}

func TestAttack_GetAssistantTextContent_AgenticMessage_OnlyToolCalls(t *testing.T) {
	// Only tool call blocks, no text at all.
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolCall{Name: "read", Arguments: `{"path":"test"}`}),
			schema.NewContentBlock(&schema.FunctionToolCall{Name: "write", Arguments: `{"content":"x"}`}),
		},
	}

	result := getAssistantTextContent(msg)
	assert.Equal(t, "", result, "should return empty when only tool calls present")
}

// =============================================================================
// Attack tests for DefaultFinalize end-to-end behavior
// =============================================================================

func TestAttack_DefaultFinalize_PreservesSystemMessages(t *testing.T) {
	ctx := context.Background()

	sys1 := schema.SystemMessage("system prompt 1")
	sys2 := schema.SystemMessage("system prompt 2")
	userMsg := &schema.Message{Role: schema.User, Content: "user question"}
	originalMsgs := []*schema.Message{sys1, sys2, userMsg}

	summary := &schema.Message{
		Role:    schema.Assistant,
		Content: "conversation summary",
	}

	result, err := DefaultFinalize(ctx, originalMsgs, summary)
	require.NoError(t, err)

	// First two should be system messages
	require.GreaterOrEqual(t, len(result), 3)
	assert.Equal(t, schema.System, result[0].Role)
	assert.Equal(t, schema.System, result[1].Role)
	// Last one should be the processed summary (user role with summary content type)
	lastMsg := result[len(result)-1]
	assert.Equal(t, schema.User, lastMsg.Role)
	ct := typedGetContentType(lastMsg)
	assert.Equal(t, contentTypeSummary, ct, "final message should be marked as summary")
}

func TestAttack_DefaultFinalize_EmptySummaryContent(t *testing.T) {
	// What happens if the model returned an empty summary?
	ctx := context.Background()

	userMsg := &schema.Message{Role: schema.User, Content: "test"}
	originalMsgs := []*schema.Message{userMsg}

	summary := &schema.Message{
		Role:    schema.Assistant,
		Content: "",
	}

	_, err := DefaultFinalize(ctx, originalMsgs, summary)
	require.Error(t, err, "empty summary content should return an error")
	assert.Contains(t, err.Error(), "summary content is empty")
}

// =============================================================================
// Attack test for replaceUserMessagesInSummary with no <all_user_messages> tag
// =============================================================================

func TestAttack_ReplaceUserMessages_NoTag(t *testing.T) {
	// If the summary doesn't contain the <all_user_messages> tag,
	// the function should return the original text unchanged.
	ctx := context.Background()

	userMsg := &schema.Message{Role: schema.User, Content: "hello"}
	summaryText := "This is a summary without any tag markers."

	result, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[*schema.Message]{
		contextMsgs: []*schema.Message{userMsg},
		summaryText: summaryText,
	})
	require.NoError(t, err)
	assert.Equal(t, summaryText, result)
}

func TestAttack_ReplaceUserMessages_MultipleTagInstances(t *testing.T) {
	// If there are multiple <all_user_messages> tags, only the LAST one should be replaced.
	ctx := context.Background()

	userMsg := &schema.Message{Role: schema.User, Content: "my message"}
	summaryText := "<all_user_messages>first</all_user_messages> middle <all_user_messages>second</all_user_messages>"

	result, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[*schema.Message]{
		contextMsgs: []*schema.Message{userMsg},
		summaryText: summaryText,
	})
	require.NoError(t, err)

	// First tag should be preserved, last one replaced
	assert.Contains(t, result, "<all_user_messages>first</all_user_messages>",
		"first tag should remain unchanged")
	assert.Contains(t, result, "my message", "user message should appear in replacement")
}
