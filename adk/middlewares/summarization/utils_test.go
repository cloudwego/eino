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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestPopulateUserMessagesConfigCheck(t *testing.T) {
	ctx := context.Background()

	t.Run("negative max tokens", func(t *testing.T) {
		_, err := PopulateUserMessages(ctx, nil, "", PopulateUserMessagesConfig[*schema.Message]{MaxTokens: intPtr(-1)})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "MaxTokens must be non-negative")
	})
}

func TestPopulateUserMessages(t *testing.T) {
	ctx := context.Background()

	messages := []adk.Message{
		schema.UserMessage("msg1"),
		schema.AssistantMessage("response1", nil),
		schema.UserMessage("msg2"),
	}

	summaryText := "1. Primary Request:\n   test\n\n6. All user messages:\n<all_user_messages>\n    - [old message]\n</all_user_messages>\n\n7. Pending Tasks:\n   - task1"

	resultText, err := PopulateUserMessages(ctx, messages, summaryText, PopulateUserMessagesConfig[*schema.Message]{
		MaxTokens: intPtr(10000),
	})
	assert.NoError(t, err)

	assert.Contains(t, resultText, "msg1")
	assert.Contains(t, resultText, "msg2")
	assert.NotContains(t, resultText, "old message")
	assert.Contains(t, resultText, "7. Pending Tasks:")
}

func TestPopulateUserMessagesWithFilter(t *testing.T) {
	ctx := context.Background()

	messages := []adk.Message{
		schema.UserMessage("drop_me_1"),
		schema.AssistantMessage("response1", nil),
		schema.UserMessage("keep_me"),
		schema.UserMessage("drop_me_2"),
	}

	summaryText := "6. All user messages:\n<all_user_messages>\n    - [old message]\n</all_user_messages>"

	resultText, err := PopulateUserMessages(ctx, messages, summaryText, PopulateUserMessagesConfig[*schema.Message]{
		MaxTokens: intPtr(10000),
		Filter: func(ctx context.Context, msg adk.Message) (bool, error) {
			return msg.Content == "keep_me", nil
		},
	})
	assert.NoError(t, err)

	assert.Contains(t, resultText, "keep_me")
	assert.NotContains(t, resultText, "drop_me_1")
	assert.NotContains(t, resultText, "drop_me_2")
}

func TestPopulateUserMessagesWithTokenCounter(t *testing.T) {
	ctx := context.Background()

	messages := []adk.Message{
		schema.UserMessage("msg1"),
		schema.UserMessage("msg2"),
		schema.UserMessage("msg3"),
	}

	summaryText := "6. All user messages:\n<all_user_messages>\n    - [old]\n</all_user_messages>"

	resultText, err := PopulateUserMessages(ctx, messages, summaryText, PopulateUserMessagesConfig[*schema.Message]{
		MaxTokens: intPtr(5),
		TokenCounter: func(ctx context.Context, input *TokenCounterInput) (int, error) {
			return 3, nil
		},
	})
	assert.NoError(t, err)

	assert.Contains(t, resultText, "msg3")
	assert.NotContains(t, resultText, "msg1")
}

func TestPopulateUserMessagesFallbackToOriginalMessages(t *testing.T) {
	ctx := context.Background()

	messages := []adk.Message{
		schema.SystemMessage("system prompt"),
		schema.UserMessage("user msg"),
		schema.AssistantMessage("response", nil),
	}

	summaryText := "6. All user messages:\n<all_user_messages>\n    - [old]\n</all_user_messages>"

	resultText, err := PopulateUserMessages(ctx, messages, summaryText, PopulateUserMessagesConfig[*schema.Message]{
		MaxTokens: intPtr(10000),
	})
	assert.NoError(t, err)

	assert.Contains(t, resultText, "user msg")
	assert.NotContains(t, resultText, "system prompt")
}

func TestPopulateUserMessagesWithPreserveSkills(t *testing.T) {
	ptr := func(i int) *int { return &i }
	ctx := context.Background()

	finalizer, err := NewFinalizer().
		PreserveSkills(&PreserveSkillsConfig{
			MaxSkills:     ptr(2),
			SkillToolName: "load_skill",
		}).
		Custom(func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
			summaryText := getUserMsgTextContent(summary)
			rewritten, err := PopulateUserMessages(ctx, originalMessages, summaryText, PopulateUserMessagesConfig[*schema.Message]{
				MaxTokens: intPtr(10000),
			})
			if err != nil {
				return nil, err
			}
			summary.UserInputMultiContent[len(summary.UserInputMultiContent)-1] = schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: rewritten,
			}
			return []adk.Message{summary}, nil
		}).
		Build()
	assert.NoError(t, err)

	skillMessages := []adk.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: "call_1",
					Function: schema.FunctionCall{
						Name:      "load_skill",
						Arguments: `{"skill": "test-skill"}`,
					},
				},
			},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "call_1",
			Content:    "skill content 1",
		},
	}

	contextMsgs := []adk.Message{
		schema.UserMessage("user msg"),
		schema.AssistantMessage("response", nil),
	}

	ctx = context.WithValue(ctx, ctxKeyModelInput{}, skillMessages)

	summaryText := "6. All user messages:\n<all_user_messages>\n    - [old]\n</all_user_messages>"
	summary := &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: summaryText},
		},
	}

	result, err := finalizer(ctx, append(skillMessages, contextMsgs...), summary)
	assert.NoError(t, err)
	assert.Len(t, result, 1)

	assert.True(t, len(result[0].UserInputMultiContent) >= 2)
	assert.Contains(t, result[0].UserInputMultiContent[0].Text, "test-skill")

	lastPartText := result[0].UserInputMultiContent[len(result[0].UserInputMultiContent)-1].Text
	assert.Contains(t, lastPartText, "user msg")
	assert.NotContains(t, lastPartText, "old")
}
