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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestNew(t *testing.T) {
	ctx := context.Background()

	t.Run("valid config", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		cfg := &Config{
			Model: cm,
		}

		mw, err := New(ctx, cfg)
		assert.NoError(t, err)
		assert.NotNil(t, mw)
	})

	t.Run("nil config returns error", func(t *testing.T) {
		mw, err := New(ctx, nil)
		assert.Error(t, err)
		assert.Nil(t, mw)
	})

	t.Run("nil model returns error", func(t *testing.T) {
		mw, err := New(ctx, &Config{})
		assert.Error(t, err)
		assert.Nil(t, mw)
	})
}

func TestMiddlewareBeforeModelRewriteState(t *testing.T) {
	ctx := context.Background()
	mtx := &adk.ModelContext{}

	t.Run("no summarization when under threshold", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		mw := &middleware{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 1000},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage("hello"),
				schema.AssistantMessage("hi", nil),
			},
		}

		_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
		assert.Len(t, newState.Messages, 2)
		assert.Equal(t, "hello", newState.Messages[0].Content)
	})

	t.Run("summarization triggered when over threshold", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary content",
			}, nil).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
				schema.AssistantMessage(strings.Repeat("b", 100), nil),
			},
		}

		_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
		assert.Len(t, newState.Messages, 1)
		assert.Equal(t, schema.User, newState.Messages[0].Role)
	})

	t.Run("preserves system messages after summarization", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				for i, msg := range msgs {
					if i == 0 {
						assert.Equal(t, schema.System, msg.Role)
					} else {
						assert.NotEqual(t, schema.System, msg.Role)
					}
				}
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary content",
				}, nil
			}).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.SystemMessage("You are a helpful assistant"),
				schema.UserMessage(strings.Repeat("a", 100)),
				schema.AssistantMessage(strings.Repeat("b", 100), nil),
			},
		}

		_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
		assert.Len(t, newState.Messages, 2)
		assert.Equal(t, schema.System, newState.Messages[0].Role)
		assert.Equal(t, "You are a helpful assistant", newState.Messages[0].Content)
		assert.Equal(t, schema.User, newState.Messages[1].Role)
	})

	t.Run("preserves multiple system messages", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.SystemMessage("System 1"),
				schema.SystemMessage("System 2"),
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
		assert.Len(t, newState.Messages, 3)
		assert.Equal(t, schema.System, newState.Messages[0].Role)
		assert.Equal(t, "System 1", newState.Messages[0].Content)
		assert.Equal(t, schema.System, newState.Messages[1].Role)
		assert.Equal(t, "System 2", newState.Messages[1].Content)
		assert.Equal(t, schema.User, newState.Messages[2].Role)
	})

	t.Run("custom finalize function", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Finalize: func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
					return []adk.Message{
						schema.SystemMessage("system prompt"),
						summary,
					}, nil
				},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
		assert.Len(t, newState.Messages, 2)
		assert.Equal(t, schema.System, newState.Messages[0].Role)
		assert.Equal(t, "system prompt", newState.Messages[0].Content)
	})
}

func TestMiddlewareShouldSummarize(t *testing.T) {
	ctx := context.Background()

	t.Run("returns true when over threshold", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		triggered, err := mw.shouldSummarize(ctx, input)
		assert.NoError(t, err)
		assert.True(t, triggered)
	})

	t.Run("returns false when under threshold", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				Trigger: &TriggerCondition{ContextTokens: 1000},
			},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{
				schema.UserMessage("short message"),
			},
		}

		triggered, err := mw.shouldSummarize(ctx, input)
		assert.NoError(t, err)
		assert.False(t, triggered)
	})

	t.Run("uses default threshold when trigger is nil", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{
				schema.UserMessage("short message"),
			},
		}

		triggered, err := mw.shouldSummarize(ctx, input)
		assert.NoError(t, err)
		assert.False(t, triggered)
	})
}

func TestMiddlewareCountTokens(t *testing.T) {
	ctx := context.Background()

	t.Run("uses custom token counter", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				TokenCounter: func(ctx context.Context, input *TokenCounterInput) (int, error) {
					return 42, nil
				},
			},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{schema.UserMessage("test")},
		}
		tokens, err := mw.countTokens(ctx, input)
		assert.NoError(t, err)
		assert.Equal(t, 42, tokens)
	})

	t.Run("uses default token counter when nil", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{schema.UserMessage("test")},
		}
		tokens, err := mw.countTokens(ctx, input)
		assert.NoError(t, err)
		assert.Equal(t, 1, tokens)
	})

	t.Run("custom token counter error", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				TokenCounter: func(ctx context.Context, input *TokenCounterInput) (int, error) {
					return 0, errors.New("token count error")
				},
			},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{schema.UserMessage("test")},
		}
		_, err := mw.countTokens(ctx, input)
		assert.Error(t, err)
	})
}

func TestExtractTextContent(t *testing.T) {
	t.Run("extracts from Content field", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "hello world",
		}
		assert.Equal(t, "hello world", extractTextContent(msg))
	})

	t.Run("extracts from UserInputMultiContent", func(t *testing.T) {
		msg := &schema.Message{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "part1"},
				{Type: schema.ChatMessagePartTypeText, Text: "part2"},
			},
		}
		assert.Equal(t, "part1\npart2", extractTextContent(msg))
	})

	t.Run("prefers Content over UserInputMultiContent", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "content field",
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "multi content"},
			},
		}
		assert.Equal(t, "content field", extractTextContent(msg))
	})
}

func TestTruncateTextByChars(t *testing.T) {
	t.Run("returns empty for empty string", func(t *testing.T) {
		result := truncateTextByChars("")
		assert.Equal(t, "", result)
	})

	t.Run("returns original if under limit", func(t *testing.T) {
		result := truncateTextByChars("short")
		assert.Equal(t, "short", result)
	})

	t.Run("truncates long text", func(t *testing.T) {
		longText := strings.Repeat("a", 3000)
		result := truncateTextByChars(longText)
		assert.Less(t, len(result), len(longText))
		assert.Contains(t, result, "truncated")
	})

	t.Run("preserves prefix and suffix", func(t *testing.T) {
		longText := strings.Repeat("a", 1000) + strings.Repeat("b", 1000) + strings.Repeat("c", 1000)
		result := truncateTextByChars(longText)
		assert.True(t, strings.HasPrefix(result, strings.Repeat("a", 1000)))
		assert.True(t, strings.HasSuffix(result, strings.Repeat("c", 1000)))
	})
}

func TestAppendSection(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		section  string
		expected string
	}{
		{
			name:     "both empty",
			base:     "",
			section:  "",
			expected: "",
		},
		{
			name:     "base empty",
			base:     "",
			section:  "section",
			expected: "section",
		},
		{
			name:     "section empty",
			base:     "base",
			section:  "",
			expected: "base",
		},
		{
			name:     "both non-empty",
			base:     "base",
			section:  "section",
			expected: "base\n\nsection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appendSection(tt.base, tt.section)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAllUserMessagesTagRegex(t *testing.T) {
	t.Run("matches tag", func(t *testing.T) {
		text := `<all_user_messages>
    - msg1
    - msg2
</all_user_messages>`
		assert.True(t, allUserMessagesTagRegex.MatchString(text))
	})

	t.Run("replaces tag content", func(t *testing.T) {
		text := `before
<all_user_messages>
    - old msg
</all_user_messages>
after`
		replacement := "<all_user_messages>\n    - new msg\n</all_user_messages>"
		result := allUserMessagesTagRegex.ReplaceAllString(text, replacement)
		assert.Contains(t, result, "new msg")
		assert.NotContains(t, result, "old msg")
		assert.Contains(t, result, "before")
		assert.Contains(t, result, "after")
	})
}

func TestConfigCheck(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		var c *Config
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config is required")
	})

	t.Run("nil model", func(t *testing.T) {
		c := &Config{}
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "model is required")
	})

	t.Run("valid config", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		c := &Config{
			Model: cm,
		}
		err := c.check()
		assert.NoError(t, err)
	})

	t.Run("invalid trigger max tokens", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		c := &Config{
			Model:   cm,
			Trigger: &TriggerCondition{ContextTokens: -1},
		}
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must be positive")
	})
}

func TestSetGetContentType(t *testing.T) {
	msg := &schema.Message{
		Role:    schema.User,
		Content: "test",
	}

	setContentType(msg, contentTypeSummary)

	ct, ok := getContentType(msg)
	assert.True(t, ok)
	assert.Equal(t, contentTypeSummary, ct)
}

func TestSetGetExtra(t *testing.T) {
	t.Run("set and get", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "test",
		}

		setExtra(msg, "key", "value")

		v, ok := getExtra[string](msg, "key")
		assert.True(t, ok)
		assert.Equal(t, "value", v)
	})

	t.Run("get from nil message", func(t *testing.T) {
		v, ok := getExtra[string](nil, "key")
		assert.False(t, ok)
		assert.Equal(t, "", v)
	})

	t.Run("get non-existent key", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "test",
		}

		v, ok := getExtra[string](msg, "non-existent")
		assert.False(t, ok)
		assert.Equal(t, "", v)
	})
}

func TestMiddlewareSummarize(t *testing.T) {
	ctx := context.Background()

	t.Run("message structure", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				assert.GreaterOrEqual(t, len(msgs), 3)
				assert.Equal(t, schema.System, msgs[0].Role)
				assert.Equal(t, schema.User, msgs[len(msgs)-1].Role)
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "summary",
				}, nil
			}).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model: cm,
			},
		}

		_, err := mw.summarize(ctx, []adk.Message{schema.UserMessage("test")})
		assert.NoError(t, err)
	})

	t.Run("uses custom instruction", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				lastMsg := msgs[len(msgs)-1]
				assert.Equal(t, schema.User, lastMsg.Role)
				assert.Contains(t, lastMsg.Content, "custom instruction")
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "summary",
				}, nil
			}).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:       cm,
				Instruction: "custom instruction",
			},
		}

		_, err := mw.summarize(ctx, []adk.Message{schema.UserMessage("test")})
		assert.NoError(t, err)
	})

	t.Run("model generate error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, errors.New("generate error")).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model: cm,
			},
		}

		_, err := mw.summarize(ctx, []adk.Message{schema.UserMessage("test")})
		assert.Error(t, err)
	})
}

func TestReplaceUserMessagesInSummary(t *testing.T) {
	ctx := context.Background()

	t.Run("replaces user messages section", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{},
		}

		msgs := []adk.Message{
			schema.UserMessage("msg1"),
			schema.AssistantMessage("response1", nil),
			schema.UserMessage("msg2"),
		}

		summary := `1. Primary Request:
   test

6. All user messages:
<all_user_messages>
    - [old message]
</all_user_messages>

7. Pending Tasks:
   - task1`

		result, err := mw.replaceUserMessagesInSummary(ctx, msgs, summary, 1000)
		assert.NoError(t, err)
		assert.Contains(t, result, "msg1")
		assert.Contains(t, result, "msg2")
		assert.NotContains(t, result, "old message")
		assert.Contains(t, result, "7. Pending Tasks:")
	})

	t.Run("returns original if no matching sections", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{},
		}

		msgs := []adk.Message{
			schema.UserMessage("test"),
		}

		summary := "summary without sections"
		result, err := mw.replaceUserMessagesInSummary(ctx, msgs, summary, 1000)
		assert.NoError(t, err)
		assert.Equal(t, summary, result)
	})

	t.Run("skips summary messages", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{},
		}

		summaryMsg := &schema.Message{
			Role:    schema.User,
			Content: "summary",
		}
		setContentType(summaryMsg, contentTypeSummary)

		msgs := []adk.Message{
			summaryMsg,
			schema.UserMessage("regular message"),
		}

		summary := `6. All user messages:
<all_user_messages>
    - [old]
</all_user_messages>

7. Pending Tasks:
   - task`

		result, err := mw.replaceUserMessagesInSummary(ctx, msgs, summary, 1000)
		assert.NoError(t, err)
		assert.Contains(t, result, "regular message")
		assert.NotContains(t, result, "    - summary")
	})

	t.Run("token counter error", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				TokenCounter: func(ctx context.Context, input *TokenCounterInput) (int, error) {
					return 0, errors.New("count error")
				},
			},
		}

		msgs := []adk.Message{
			schema.UserMessage("test1"),
			schema.UserMessage("test2"),
		}

		_, err := mw.replaceUserMessagesInSummary(ctx, msgs, "summary", 1000)
		assert.Error(t, err)
	})

	t.Run("returns original if empty user messages", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{},
		}

		msgs := []adk.Message{
			schema.AssistantMessage("response", nil),
		}

		summary := `6. All user messages:
    - [old]

7. Pending Tasks:
   - task`

		result, err := mw.replaceUserMessagesInSummary(ctx, msgs, summary, 1000)
		assert.NoError(t, err)
		assert.Equal(t, summary, result)
	})
}

func TestAllUserMessagesTagRegexMatch(t *testing.T) {
	t.Run("matches xml tag", func(t *testing.T) {
		text := "<all_user_messages>\n    - msg\n</all_user_messages>"
		assert.True(t, allUserMessagesTagRegex.MatchString(text))
	})

	t.Run("does not match without tag", func(t *testing.T) {
		text := "6. All user messages:\n    - msg"
		assert.False(t, allUserMessagesTagRegex.MatchString(text))
	})
}

func TestDefaultTrimUserMessage(t *testing.T) {
	t.Run("returns nil for zero remaining tokens", func(t *testing.T) {
		msg := schema.UserMessage("test")
		result := defaultTrimUserMessage(msg, 0)
		assert.Nil(t, result)
	})

	t.Run("returns nil for empty content", func(t *testing.T) {
		msg := schema.UserMessage("")
		result := defaultTrimUserMessage(msg, 100)
		assert.Nil(t, result)
	})

	t.Run("trims long message", func(t *testing.T) {
		longText := strings.Repeat("a", 3000)
		msg := schema.UserMessage(longText)
		result := defaultTrimUserMessage(msg, 100)
		assert.NotNil(t, result)
		assert.Less(t, len(result.Content), len(longText))
	})
}

func TestDefaultTokenCounter(t *testing.T) {
	ctx := context.Background()

	t.Run("counts tool tokens", func(t *testing.T) {
		input := &TokenCounterInput{
			Messages: []adk.Message{},
			Tools: []*schema.ToolInfo{
				{Name: "test_tool", Desc: "description"},
			},
		}
		count, err := defaultTokenCounter(ctx, input)
		assert.NoError(t, err)
		assert.Greater(t, count, 0)
	})
}

func TestPostProcessSummary(t *testing.T) {
	ctx := context.Background()

	t.Run("with transcript path", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				TranscriptFilePath: "/path/to/transcript.txt",
			},
		}

		summary := &schema.Message{
			Role:    schema.User,
			Content: "summary content",
		}

		result, err := mw.postProcessSummary(ctx, []adk.Message{}, summary)
		assert.NoError(t, err)
		assert.Len(t, result.UserInputMultiContent, 2)
		assert.Contains(t, result.UserInputMultiContent[0].Text, "/path/to/transcript.txt")
	})
}
