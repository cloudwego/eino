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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ============================================================================
// Generic message helpers (prefixed with 's' to avoid conflicts)
// ============================================================================

func smakeUserMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(content)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(content)).(M)
	}
	panic("unreachable")
}

func smakeSystemMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.SystemMessage(content)).(M)
	case *schema.AgenticMessage:
		return any(schema.SystemAgenticMessage(content)).(M)
	}
	panic("unreachable")
}

func smakeAssistantMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.AssistantMessage(content, nil)).(M)
	case *schema.AgenticMessage:
		am := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: content}),
			},
		}
		return any(am).(M)
	}
	panic("unreachable")
}

// ============================================================================
// Generic mock model
// ============================================================================

type genericMockModel[M adk.MessageType] struct {
	response M
	err      error
}

func (m *genericMockModel[M]) Generate(_ context.Context, _ []M, _ ...model.Option) (M, error) {
	return m.response, m.err
}

func (m *genericMockModel[M]) Stream(_ context.Context, _ []M, _ ...model.Option) (*schema.StreamReader[M], error) {
	return nil, fmt.Errorf("not implemented")
}

// ============================================================================
// typedMsgToLegacy / typedMsgsToLegacy tests
// ============================================================================

func TestTypedMsgToLegacy_AgenticMessage(t *testing.T) {
	t.Run("AgenticMessage returns nil", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: "hello"}),
			},
		}
		result := typedMsgToLegacy[*schema.AgenticMessage](msg)
		assert.Nil(t, result, "typedMsgToLegacy should return nil for AgenticMessage")
	})

	t.Run("nil AgenticMessage returns nil", func(t *testing.T) {
		var msg *schema.AgenticMessage
		result := typedMsgToLegacy[*schema.AgenticMessage](msg)
		assert.Nil(t, result)
	})

	t.Run("Message returns the message itself", func(t *testing.T) {
		msg := schema.UserMessage("hello")
		result := typedMsgToLegacy[*schema.Message](msg)
		assert.Equal(t, msg, result)
	})
}

func TestTypedMsgsToLegacy_AgenticMessage(t *testing.T) {
	t.Run("AgenticMessage slice returns nil", func(t *testing.T) {
		msgs := []*schema.AgenticMessage{
			{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.UserInputText{Text: "hello"}),
				},
			},
			{
				Role: schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.AssistantGenText{Text: "hi"}),
				},
			},
		}
		result := typedMsgsToLegacy[*schema.AgenticMessage](msgs)
		assert.Nil(t, result, "typedMsgsToLegacy should return nil for AgenticMessage slice")
	})

	t.Run("Message slice returns converted slice", func(t *testing.T) {
		msg1 := schema.UserMessage("hello")
		msg2 := schema.AssistantMessage("hi", nil)
		msgs := []*schema.Message{msg1, msg2}
		result := typedMsgsToLegacy[*schema.Message](msgs)
		require.Len(t, result, 2)
		assert.Equal(t, msg1, result[0])
		assert.Equal(t, msg2, result[1])
	})
}

// ============================================================================
// Tests
// ============================================================================

func TestSummarizationGeneric(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		t.Run("Helpers", testSummarizationHelpers[*schema.Message])
		t.Run("Flow", testSummarizationFlow[*schema.Message])
		t.Run("SummarizeMessages", testTypedSummarizeMessages[*schema.Message])
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		t.Run("Helpers", testSummarizationHelpers[*schema.AgenticMessage])
		t.Run("Flow", testSummarizationFlow[*schema.AgenticMessage])
		t.Run("SummarizeMessages", testTypedSummarizeMessages[*schema.AgenticMessage])
	})
}

// testSummarizationHelpers tests the generic helper functions.
func testSummarizationHelpers[M adk.MessageType](t *testing.T) {
	t.Run("isSystemRole", func(t *testing.T) {
		sys := smakeSystemMsg[M]("hello")
		usr := smakeUserMsg[M]("hello")
		assert.True(t, isSystemRole(sys))
		assert.False(t, isSystemRole(usr))
	})

	t.Run("isUserRole", func(t *testing.T) {
		usr := smakeUserMsg[M]("hello")
		sys := smakeSystemMsg[M]("hello")
		assert.True(t, isUserRole(usr))
		assert.False(t, isUserRole(sys))
	})

	t.Run("getTextContent", func(t *testing.T) {
		usr := smakeUserMsg[M]("hello world")
		assert.Equal(t, "hello world", getTextContent(usr))

		ast := smakeAssistantMsg[M]("reply")
		assert.Equal(t, "reply", getTextContent(ast))
	})

	t.Run("getMsgExtra_setMsgExtra", func(t *testing.T) {
		msg := smakeUserMsg[M]("test")
		// Extra should be nil initially
		extra := getMsgExtra(msg)
		assert.Nil(t, extra)

		// Set and read back
		setMsgExtra(msg, "key1", "value1")
		extra = getMsgExtra(msg)
		assert.Equal(t, "value1", extra["key1"])
	})

	t.Run("makeSystemMsg", func(t *testing.T) {
		msg := makeSystemMsg[M]("system prompt")
		assert.True(t, isSystemRole(msg))
		assert.Equal(t, "system prompt", getTextContent(msg))
	})

	t.Run("makeUserMsg", func(t *testing.T) {
		msg := makeUserMsg[M]("user input")
		assert.True(t, isUserRole(msg))
		assert.Equal(t, "user input", getTextContent(msg))
	})

	t.Run("setMsgTextContent", func(t *testing.T) {
		msg := smakeUserMsg[M]("original")
		msg = setMsgTextContent(msg, "replaced")
		assert.Equal(t, "replaced", getTextContent(msg))
	})

	t.Run("setMsgMultipartContent", func(t *testing.T) {
		msg := smakeUserMsg[M]("original")
		msg = setMsgMultipartContent(msg, "summary part", "continue part")

		// For Message: check UserInputMultiContent
		// For AgenticMessage: check ContentBlocks
		switch m := any(msg).(type) {
		case *schema.Message:
			require.Len(t, m.UserInputMultiContent, 2)
			assert.Equal(t, "summary part", m.UserInputMultiContent[0].Text)
			assert.Equal(t, "continue part", m.UserInputMultiContent[1].Text)
			assert.Empty(t, m.Content) // Content cleared
		case *schema.AgenticMessage:
			require.Len(t, m.ContentBlocks, 2)
			assert.Equal(t, "summary part", m.ContentBlocks[0].UserInputText.Text)
			assert.Equal(t, "continue part", m.ContentBlocks[1].UserInputText.Text)
		}
	})
}

// testSummarizationFlow tests BeforeModelRewriteState end-to-end.
func testSummarizationFlow[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	summaryText := "This is a summary of the conversation."
	mockModel := &genericMockModel[M]{
		response: smakeAssistantMsg[M](summaryText),
	}

	// Token counter that counts characters
	tokenCounter := func(_ context.Context, input *TypedTokenCounterInput[M]) (int, error) {
		total := 0
		for _, msg := range input.Messages {
			total += len(getTextContent(msg))
		}
		return total, nil
	}

	cfg := &TypedConfig[M]{
		Model:        mockModel,
		TokenCounter: tokenCounter,
		Trigger: &TriggerCondition{
			ContextTokens: 20, // low threshold to trigger summarization
		},
	}

	mw, err := NewTyped[M](ctx, cfg)
	require.NoError(t, err)

	// Build messages that exceed the threshold (>20 chars total)
	msgs := []M{
		smakeSystemMsg[M]("You are a helpful assistant."),
		smakeUserMsg[M]("Tell me a very long story about dragons and castles"),
		smakeAssistantMsg[M]("Once upon a time there was a magnificent dragon"),
		smakeUserMsg[M]("What happened next?"),
	}

	state := &adk.TypedChatModelAgentState[M]{Messages: msgs}
	mtx := &adk.TypedModelContext[M]{}

	_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
	require.NoError(t, err)

	// Summarization was triggered — verify messages were replaced.
	// The new state should have at least a system message and a summary user message.
	require.GreaterOrEqual(t, len(newState.Messages), 2,
		"should have at least system + summary messages")

	// The first message should still be system
	assert.True(t, isSystemRole(newState.Messages[0]),
		"first message should be system")

	// Verify that a summary message exists by checking content type in Extra,
	// or that the summary text appears in one of the messages.
	foundSummary := false
	for _, msg := range newState.Messages {
		extra := getMsgExtra(msg)
		if extra != nil {
			if ct, ok := extra[extraKeyContentType]; ok && ct == string(contentTypeSummary) {
				foundSummary = true
				break
			}
		}
		// Also check if the summary text appears in message content
		if strings.Contains(getTextContent(msg), summaryText) {
			foundSummary = true
			break
		}
	}
	assert.True(t, foundSummary, "should have a summary message")
}

// testTypedSummarizeMessages tests the synchronous TypedSummarizeMessages API.
func testTypedSummarizeMessages[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	summaryText := "Summary of conversation."
	mockModel := &genericMockModel[M]{
		response: smakeAssistantMsg[M](summaryText),
	}

	tokenCounter := func(_ context.Context, input *TypedTokenCounterInput[M]) (int, error) {
		total := 0
		for _, msg := range input.Messages {
			total += len(getTextContent(msg))
		}
		return total, nil
	}

	cfg := &TypedConfig[M]{
		Model:        mockModel,
		TokenCounter: tokenCounter,
	}

	msgs := []M{
		smakeSystemMsg[M]("System prompt"),
		smakeUserMsg[M]("Hello, can you help me with something?"),
		smakeAssistantMsg[M]("Of course! I would be happy to help you with anything."),
		smakeUserMsg[M]("Tell me about Go generics"),
	}

	output, err := TypedSummarizeMessages[M](ctx, cfg, msgs)
	require.NoError(t, err)
	require.NotNil(t, output)

	// FinalizedMessages should contain the summarized conversation
	assert.Greater(t, len(output.FinalizedMessages), 0,
		"should have finalized messages")

	// ModelResponse should be the raw summary
	assert.Equal(t, summaryText, getTextContent(output.ModelResponse))
}
