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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func intPtr(v int) *int {
	return &v
}

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

	t.Run("retry succeeds after transient error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		callCount := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				callCount++
				if callCount == 1 {
					return nil, fmt.Errorf("transient error")
				}
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary after retry",
				}, nil
			}).Times(2)

		mw := &middleware{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Retry: &RetryConfig{
					MaxRetries:  intPtr(2),
					BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
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
		assert.Len(t, newState.Messages, 1)
		assert.Equal(t, 2, callCount)
	})

	t.Run("retry uses default max retries when MaxRetries is nil", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		callCount := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				callCount++
				return nil, fmt.Errorf("transient error")
			}).Times(4)

		mw := &middleware{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Retry: &RetryConfig{
					BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
				},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		_, _, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to generate summary")
		assert.Equal(t, 4, callCount)
	})

	t.Run("failover succeeds after primary failure", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		failover := mockModel.NewMockBaseChatModel(ctrl)

		primary.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("primary error")).Times(1)
		failover.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				assert.Len(t, msgs, 1)
				assert.Equal(t, "failover input", msgs[0].Content)
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary from failover",
				}, nil
			}).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   primary,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Failover: &FailoverConfig{
					GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						assert.Equal(t, 1, failoverCtx.Attempt)
						assert.Equal(t, schema.System, failoverCtx.SystemInstruction.Role)
						assert.Equal(t, schema.User, failoverCtx.UserInstruction.Role)
						assert.Len(t, failoverCtx.OriginalMessages, 1)
						assert.Nil(t, failoverCtx.LastModelResponse)
						assert.EqualError(t, failoverCtx.LastErr, "primary error")
						return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
					},
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
		assert.Len(t, newState.Messages, 1)
		assert.Equal(t, schema.User, newState.Messages[0].Role)
	})

	t.Run("failover context last err is retry exhausted error when retries exhausted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		failover := mockModel.NewMockBaseChatModel(ctrl)

		primary.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("primary error")).Times(2)
		failover.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary from failover",
			}, nil).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   primary,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Retry: &RetryConfig{
					MaxRetries:  intPtr(1),
					BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
				},
				Failover: &FailoverConfig{
					GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						assert.ErrorContains(t, failoverCtx.LastErr, "exceeds max retries")
						return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
					},
				},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		_, _, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
	})

	t.Run("returns failover exhausted error when failover model fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		failover := mockModel.NewMockBaseChatModel(ctrl)

		primary.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("primary error")).Times(1)
		failover.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("failover error")).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   primary,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Failover: &FailoverConfig{
					MaxRetries:  intPtr(0),
					BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
					GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
					},
				},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		_, _, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "exceeds max failover attempts")
	})

	t.Run("failover retries with max retries and succeeds on second attempt", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		failover1 := mockModel.NewMockBaseChatModel(ctrl)
		failover2 := mockModel.NewMockBaseChatModel(ctrl)

		primary.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("primary error")).Times(1)
		failover1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("failover error 1")).Times(1)
		failover2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary from second failover",
			}, nil).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   primary,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Failover: &FailoverConfig{
					MaxRetries:  intPtr(1),
					BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
					GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						if failoverCtx.Attempt == 1 {
							assert.EqualError(t, failoverCtx.LastErr, "primary error")
							return failover1, []*schema.Message{schema.UserMessage("failover input 1")}, nil
						}
						assert.Equal(t, 2, failoverCtx.Attempt)
						assert.EqualError(t, failoverCtx.LastErr, "failover error 1")
						return failover2, []*schema.Message{schema.UserMessage("failover input 2")}, nil
					},
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
		assert.Len(t, newState.Messages, 1)
	})

	t.Run("failover context carries generate resp as last output message", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		failover := mockModel.NewMockBaseChatModel(ctrl)

		primary.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "partial output",
			}, fmt.Errorf("primary error")).Times(1)
		failover.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary from failover",
			}, nil).Times(1)

		mw := &middleware{
			cfg: &Config{
				Model:   primary,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Failover: &FailoverConfig{
					GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						if assert.NotNil(t, failoverCtx.LastModelResponse) {
							assert.Equal(t, "partial output", failoverCtx.LastModelResponse.Content)
						}
						return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
					},
				},
			},
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		_, _, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
	})

}

func TestMiddlewareShouldSummarize(t *testing.T) {
	ctx := context.Background()

	t.Run("returns true when over messages threshold", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				Trigger: &TriggerCondition{ContextMessages: 1},
			},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{
				schema.UserMessage("msg1"),
				schema.UserMessage("msg2"),
			},
		}

		triggered, err := mw.shouldSummarize(ctx, input)
		assert.NoError(t, err)
		assert.True(t, triggered)
	})

	t.Run("returns false when under messages threshold", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				Trigger: &TriggerCondition{
					ContextMessages: 3,
					ContextTokens:   1000,
				},
			},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{
				schema.UserMessage("msg1"),
				schema.UserMessage("msg2"),
			},
		}

		triggered, err := mw.shouldSummarize(ctx, input)
		assert.NoError(t, err)
		assert.False(t, triggered)
	})

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

	t.Run("prefers UserInputMultiContent over Content", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "content field",
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "multi content"},
			},
		}
		assert.Equal(t, "multi content", extractTextContent(msg))
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
		assert.Contains(t, err.Error(), "must be non-negative")
	})

	t.Run("invalid trigger max messages", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		c := &Config{
			Model:   cm,
			Trigger: &TriggerCondition{ContextMessages: -1},
		}
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must be non-negative")
	})

	t.Run("both trigger conditions are zero", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		c := &Config{
			Model:   cm,
			Trigger: &TriggerCondition{ContextTokens: 0, ContextMessages: 0},
		}
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must be non-negative")
	})

	t.Run("negative retry max retries", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		c := &Config{
			Model: cm,
			Retry: &RetryConfig{MaxRetries: intPtr(-1)},
		}
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "retry.MaxRetries must be non-negative")
	})

	t.Run("failover getFailoverModel is optional", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		c := &Config{
			Model:    cm,
			Failover: &FailoverConfig{},
		}
		err := c.check()
		assert.NoError(t, err)
	})

	t.Run("failover max retries accepts int value", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		failover := mockModel.NewMockBaseChatModel(ctrl)

		c := &Config{
			Model: cm,
			Failover: &FailoverConfig{
				MaxRetries: intPtr(1),
				GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
					return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
				},
			},
		}
		err := c.check()
		assert.NoError(t, err)
	})

	t.Run("failover max retries must be non-negative", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		failover := mockModel.NewMockBaseChatModel(ctrl)

		c := &Config{
			Model: cm,
			Failover: &FailoverConfig{
				MaxRetries: intPtr(-1),
				GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
					return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
				},
			},
		}
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failover.MaxRetries must be non-negative")
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

func TestMiddlewareBuildSummarizationModelInput(t *testing.T) {
	ctx := context.Background()

	t.Run("message structure", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{},
		}

		testMsg := []adk.Message{schema.UserMessage("test")}
		input, err := mw.buildSummarizationModelInput(ctx, testMsg, testMsg)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(input), 3)
		assert.Equal(t, schema.System, input[0].Role)
		assert.Equal(t, schema.User, input[len(input)-1].Role)
	})

	t.Run("uses context messages", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{},
		}

		contextMsgs := []adk.Message{
			schema.UserMessage("context message"),
		}
		input, err := mw.buildSummarizationModelInput(ctx, contextMsgs, contextMsgs)
		assert.NoError(t, err)

		found := false
		for _, msg := range input {
			if msg.Content == "context message" {
				found = true
				break
			}
		}
		assert.True(t, found, "should contain context message")
	})

	t.Run("uses GenModelInput", func(t *testing.T) {
		expectedInput := []adk.Message{
			schema.UserMessage("custom input"),
		}

		mw := &middleware{
			cfg: &Config{
				GenModelInput: func(ctx context.Context, defaultSystemInstruction, userInstruction adk.Message, originalMsgs []adk.Message) ([]adk.Message, error) {
					return expectedInput, nil
				},
			},
		}

		testMsg := []adk.Message{schema.UserMessage("test")}
		input, err := mw.buildSummarizationModelInput(ctx, testMsg, testMsg)
		assert.NoError(t, err)
		assert.Len(t, input, 1)
		assert.Equal(t, "custom input", input[0].Content)
	})

	t.Run("GenModelInput error", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				GenModelInput: func(ctx context.Context, defaultSystemInstruction, userInstruction adk.Message, originalMsgs []adk.Message) ([]adk.Message, error) {
					return nil, errors.New("gen input error")
				},
			},
		}

		testMsg := []adk.Message{schema.UserMessage("test")}
		_, err := mw.buildSummarizationModelInput(ctx, testMsg, testMsg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "gen input error")
	})

	t.Run("uses custom instruction", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				UserInstruction: "custom instruction",
			},
		}

		testMsg := []adk.Message{schema.UserMessage("test")}
		input, err := mw.buildSummarizationModelInput(ctx, testMsg, testMsg)
		assert.NoError(t, err)

		lastMsg := input[len(input)-1]
		assert.Equal(t, schema.User, lastMsg.Role)
		assert.Contains(t, lastMsg.Content, "custom instruction")
	})
}

func TestMiddlewareSummarize(t *testing.T) {
	ctx := context.Background()

	t.Run("generates summary", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "summary",
			}, nil).Times(1)

		input := []adk.Message{schema.UserMessage("test")}
		resp, err := cm.Generate(ctx, input)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		summary := newSummaryMessage(resp.Content)
		assert.NotNil(t, summary)
		assert.Equal(t, "summary", summary.Content)
	})

	t.Run("model generate error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, errors.New("generate error")).Times(1)

		input := []adk.Message{schema.UserMessage("test")}
		_, err := cm.Generate(ctx, input)
		assert.Error(t, err)
	})
}

func TestMiddlewareGenerateWithRetry(t *testing.T) {
	ctx := context.Background()

	t.Run("retries until success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		mw := &middleware{
			cfg: &Config{},
		}

		callCount := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(context.Context, []*schema.Message, ...any) (*schema.Message, error) {
				callCount++
				if callCount == 1 {
					return schema.AssistantMessage("partial output", nil), errors.New("transient error")
				}
				return schema.AssistantMessage("final summary", nil), nil
			}).Times(2)

		resp, err := mw.generateWithRetry(ctx, cm, []adk.Message{schema.UserMessage("test")}, nil, &RetryConfig{})

		assert.NoError(t, err)
		if assert.NotNil(t, resp) {
			assert.Equal(t, "final summary", resp.Content)
		}
	})

	t.Run("delegates to generateAndEmit without retry config", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		mw := &middleware{
			cfg: &Config{},
		}
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("partial output", nil), errors.New("generate error")).Times(1)

		resp, err := mw.generateWithRetry(ctx, cm, []adk.Message{schema.UserMessage("test")}, nil, nil)

		assert.EqualError(t, err, "generate error")
		if assert.NotNil(t, resp) {
			assert.Equal(t, "partial output", resp.Content)
		}
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

	t.Run("filters user messages", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				PreserveUserMessages: &PreserveUserMessages{
					Enabled: true,
					Filter: func(ctx context.Context, msg adk.Message) (bool, error) {
						return msg.Content == "keep_me", nil
					},
				},
			},
		}

		msgs := []adk.Message{
			schema.UserMessage("drop_me_1"),
			schema.AssistantMessage("response1", nil),
			schema.UserMessage("keep_me"),
			schema.UserMessage("drop_me_2"),
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
		assert.Contains(t, result, "keep_me")
		assert.NotContains(t, result, "drop_me_1")
		assert.NotContains(t, result, "drop_me_2")
		assert.NotContains(t, result, "old message")
	})

	t.Run("filter error", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				PreserveUserMessages: &PreserveUserMessages{
					Enabled: true,
					Filter: func(ctx context.Context, msg adk.Message) (bool, error) {
						return false, errors.New("filter error")
					},
				},
			},
		}

		msgs := []adk.Message{
			schema.UserMessage("msg"),
		}

		_, err := mw.replaceUserMessagesInSummary(ctx, msgs, "summary", 1000)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "filter error")
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

func TestReplaceUserMessagesInSummary_FilterRemovesAll(t *testing.T) {
	ctx := context.Background()
	mw := &middleware{
		cfg: &Config{
			PreserveUserMessages: &PreserveUserMessages{
				Enabled: true,
				Filter: func(ctx context.Context, msg adk.Message) (bool, error) {
					return false, nil
				},
			},
		},
	}

	msgs := []adk.Message{
		schema.UserMessage("drop_me"),
		schema.AssistantMessage("response", nil),
	}

	summary := `6. All user messages:
<all_user_messages>
    - [old message]
</all_user_messages>

7. Pending Tasks:
   - task`

	result, err := mw.replaceUserMessagesInSummary(ctx, msgs, summary, 1000)
	assert.NoError(t, err)
	assert.NotContains(t, result, "old message")
	assert.Contains(t, result, "<all_user_messages>\n\n</all_user_messages>")
}

func TestEventHelpers(t *testing.T) {
	ctx := context.Background()

	t.Run("emitEvent returns wrapped error outside execution context", func(t *testing.T) {
		mw := &middleware{cfg: &Config{}}
		err := mw.emitEvent(ctx, &CustomizedAction{Type: ActionTypeBeforeSummarize})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to send internal event")
	})

	t.Run("emitGenerateSummaryEvent is skipped when internal events are disabled", func(t *testing.T) {
		mw := &middleware{cfg: &Config{EmitInternalEvents: false}}
		err := mw.emitGenerateSummaryEvent(ctx, 1, GenerateSummaryPhasePrimary, schema.AssistantMessage("ok", nil), nil)
		assert.NoError(t, err)
	})

	t.Run("emitGenerateSummaryEvent returns wrapped error when enabled outside execution context", func(t *testing.T) {
		mw := &middleware{cfg: &Config{EmitInternalEvents: true}}
		err := mw.emitGenerateSummaryEvent(ctx, 1, GenerateSummaryPhasePrimary, schema.AssistantMessage("ok", nil), nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to send internal event")
	})
}

func TestGetFailoverModel(t *testing.T) {
	ctx := context.Background()
	defaultInput := []adk.Message{schema.UserMessage("default")}
	fctx := &FailoverContext{Attempt: 1}

	t.Run("requires failover config", func(t *testing.T) {
		mw := &middleware{cfg: &Config{}}
		mdl, input, err := mw.getFailoverModel(ctx, fctx, defaultInput)
		assert.Nil(t, mdl)
		assert.Nil(t, input)
		assert.ErrorContains(t, err, "failover config is required")
	})

	t.Run("uses primary model and default input when callback is not provided", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		mw := &middleware{
			cfg: &Config{
				Model:    primary,
				Failover: &FailoverConfig{},
			},
		}

		mdl, input, err := mw.getFailoverModel(ctx, fctx, defaultInput)
		assert.NoError(t, err)
		assert.Same(t, primary, mdl)
		assert.Equal(t, defaultInput, input)
	})

	t.Run("wraps callback error", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				Failover: &FailoverConfig{
					GetFailoverModel: func(context.Context, *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						return nil, nil, errors.New("boom")
					},
				},
			},
		}

		mdl, input, err := mw.getFailoverModel(ctx, fctx, defaultInput)
		assert.Nil(t, mdl)
		assert.Nil(t, input)
		assert.ErrorContains(t, err, "failed to get failover model")
	})

	t.Run("requires non nil failover model", func(t *testing.T) {
		mw := &middleware{
			cfg: &Config{
				Failover: &FailoverConfig{
					GetFailoverModel: func(context.Context, *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						return nil, []*schema.Message{schema.UserMessage("input")}, nil
					},
				},
			},
		}

		mdl, input, err := mw.getFailoverModel(ctx, fctx, defaultInput)
		assert.Nil(t, mdl)
		assert.Nil(t, input)
		assert.ErrorContains(t, err, "failover model is required")
	})

	t.Run("requires non empty failover input", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		failoverModel := mockModel.NewMockBaseChatModel(ctrl)
		mw := &middleware{
			cfg: &Config{
				Failover: &FailoverConfig{
					GetFailoverModel: func(context.Context, *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						return failoverModel, nil, nil
					},
				},
			},
		}

		mdl, input, err := mw.getFailoverModel(ctx, fctx, defaultInput)
		assert.Nil(t, mdl)
		assert.Nil(t, input)
		assert.ErrorContains(t, err, "failover model input messages are required")
	})

	t.Run("returns custom failover model and input", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		failoverModel := mockModel.NewMockBaseChatModel(ctrl)
		customInput := []*schema.Message{schema.UserMessage("custom")}
		mw := &middleware{
			cfg: &Config{
				Failover: &FailoverConfig{
					GetFailoverModel: func(context.Context, *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						return failoverModel, customInput, nil
					},
				},
			},
		}

		mdl, input, err := mw.getFailoverModel(ctx, fctx, defaultInput)
		assert.NoError(t, err)
		assert.Same(t, failoverModel, mdl)
		if assert.Len(t, input, 1) {
			assert.Equal(t, "custom", input[0].Content)
		}
	})
}

func TestHelperBranches(t *testing.T) {
	t.Run("get user message context tokens", func(t *testing.T) {
		mw := &middleware{cfg: &Config{Trigger: &TriggerCondition{ContextTokens: 90}}}
		assert.Equal(t, 30, mw.getUserMessageContextTokens())

		mw.cfg.PreserveUserMessages = &PreserveUserMessages{MaxTokens: 12}
		assert.Equal(t, 12, mw.getUserMessageContextTokens())
	})

	t.Run("should failover branches", func(t *testing.T) {
		assert.False(t, shouldFailover(context.Background(), nil, nil, errors.New("x")))
		assert.False(t, shouldFailover(context.Background(), &FailoverConfig{}, nil, nil))
		assert.True(t, shouldFailover(context.Background(), &FailoverConfig{}, nil, errors.New("x")))

		cfg := &FailoverConfig{
			ShouldFailover: func(ctx context.Context, resp adk.Message, err error) bool {
				return resp != nil && err == nil
			},
		}
		assert.True(t, shouldFailover(context.Background(), cfg, schema.AssistantMessage("ok", nil), nil))
	})

	t.Run("config check branches", func(t *testing.T) {
		assert.ErrorContains(t, (&RetryConfig{MaxRetries: intPtr(-1)}).check(), "retry.MaxRetries must be non-negative")
		assert.ErrorContains(t, (&FailoverConfig{MaxRetries: intPtr(-1)}).check(), "failover.MaxRetries must be non-negative")
		assert.ErrorContains(t, (&TriggerCondition{}).check(), "at least one of contextTokens or contextMessages")
		assert.ErrorContains(t, (&TriggerCondition{ContextTokens: -1}).check(), "contextTokens must be non-negative")
		assert.ErrorContains(t, (&TriggerCondition{ContextMessages: -1, ContextTokens: 1}).check(), "contextMessages must be non-negative")
	})

	t.Run("default backoff branches", func(t *testing.T) {
		assert.Equal(t, time.Second, defaultBackoffFunc(context.Background(), 0, nil, nil))

		delay := defaultBackoffFunc(context.Background(), 8, nil, nil)
		assert.GreaterOrEqual(t, delay, 10*time.Second)
		assert.Less(t, delay, 15*time.Second)
	})

	t.Run("user messages replaced note is present", func(t *testing.T) {
		note := getUserMessagesReplacedNote()
		assert.NotEmpty(t, note)
		assert.Contains(t, []string{userMessagesReplacedNote, userMessagesReplacedNoteZh}, note)
	})
}

func TestSummarizeMessages(t *testing.T) {
	ctx := context.Background()

	t.Run("basic summarization", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary content",
			}, nil).Times(1)

		cfg := &Config{
			Model: cm,
		}

		messages := []adk.Message{
			schema.SystemMessage("You are a helpful assistant"),
			schema.UserMessage(strings.Repeat("a", 100)),
			schema.AssistantMessage(strings.Repeat("b", 100), nil),
		}

		output, err := SummarizeMessages(ctx, cfg, messages)
		assert.NoError(t, err)
		assert.NotNil(t, output)
		assert.NotNil(t, output.ModelResponse)
		assert.Equal(t, "Summary content", output.ModelResponse.Content)
		assert.NotEmpty(t, output.FinalizedMessages)
		assert.Equal(t, schema.System, output.FinalizedMessages[0].Role)
	})

	t.Run("model error propagates", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("model error")).Times(1)

		cfg := &Config{
			Model: cm,
		}

		messages := []adk.Message{
			schema.UserMessage("hello"),
		}

		output, err := SummarizeMessages(ctx, cfg, messages)
		assert.Error(t, err)
		assert.Nil(t, output)
	})

	t.Run("retry works in sync call", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		callCount := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...any) (*schema.Message, error) {
				callCount++
				if callCount == 1 {
					return nil, fmt.Errorf("transient error")
				}
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary after retry",
				}, nil
			}).Times(2)

		cfg := &Config{
			Model: cm,
			Retry: &RetryConfig{
				MaxRetries:  intPtr(2),
				BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
			},
		}

		messages := []adk.Message{
			schema.UserMessage("hello"),
		}

		output, err := SummarizeMessages(ctx, cfg, messages)
		assert.NoError(t, err)
		assert.NotNil(t, output)
		assert.Equal(t, "Summary after retry", output.ModelResponse.Content)
		assert.Equal(t, 2, callCount)
	})

	t.Run("failover works in sync call", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		failover := mockModel.NewMockBaseChatModel(ctrl)

		primary.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("primary error")).Times(1)
		failover.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary from failover",
			}, nil).Times(1)

		cfg := &Config{
			Model: primary,
			Failover: &FailoverConfig{
				GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
					return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
				},
			},
		}

		messages := []adk.Message{
			schema.UserMessage("hello"),
		}

		output, err := SummarizeMessages(ctx, cfg, messages)
		assert.NoError(t, err)
		assert.NotNil(t, output)
		assert.Equal(t, "Summary from failover", output.ModelResponse.Content)
	})

	t.Run("callback is invoked", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		callbackCalled := false
		cfg := &Config{
			Model: cm,
			Callback: func(ctx context.Context, before, after adk.ChatModelAgentState) error {
				callbackCalled = true
				assert.Len(t, before.Messages, 1)
				assert.NotEmpty(t, after.Messages)
				return nil
			},
		}

		messages := []adk.Message{
			schema.UserMessage("hello"),
		}

		output, err := SummarizeMessages(ctx, cfg, messages)
		assert.NoError(t, err)
		assert.NotNil(t, output)
		assert.True(t, callbackCalled)
	})

	t.Run("custom finalize is used", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		cfg := &Config{
			Model: cm,
			Finalize: func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
				return []adk.Message{
					schema.SystemMessage("custom system"),
					summary,
				}, nil
			},
		}

		messages := []adk.Message{
			schema.UserMessage("hello"),
		}

		output, err := SummarizeMessages(ctx, cfg, messages)
		assert.NoError(t, err)
		assert.NotNil(t, output)
		assert.Len(t, output.FinalizedMessages, 2)
		assert.Equal(t, schema.System, output.FinalizedMessages[0].Role)
		assert.Equal(t, "custom system", output.FinalizedMessages[0].Content)
	})

	t.Run("errors when EmitInternalEvents is true", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		cfg := &Config{
			Model:              cm,
			EmitInternalEvents: true,
		}

		output, err := SummarizeMessages(ctx, cfg, []adk.Message{schema.UserMessage("hello")})
		assert.Error(t, err)
		assert.Nil(t, output)
		assert.Contains(t, err.Error(), "emitInternalEvents")
	})

	t.Run("errors when Trigger is set", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		cfg := &Config{
			Model:   cm,
			Trigger: &TriggerCondition{ContextTokens: 1000},
		}

		output, err := SummarizeMessages(ctx, cfg, []adk.Message{schema.UserMessage("hello")})
		assert.Error(t, err)
		assert.Nil(t, output)
		assert.Contains(t, err.Error(), "trigger")
	})

	t.Run("nil model returns config check error", func(t *testing.T) {
		cfg := &Config{}

		output, err := SummarizeMessages(ctx, cfg, []adk.Message{schema.UserMessage("hello")})
		assert.Error(t, err)
		assert.Nil(t, output)
		assert.Contains(t, err.Error(), "model is required")
	})

	t.Run("callback error propagates", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		cfg := &Config{
			Model: cm,
			Callback: func(ctx context.Context, before, after adk.ChatModelAgentState) error {
				return fmt.Errorf("callback error")
			},
		}

		output, err := SummarizeMessages(ctx, cfg, []adk.Message{schema.UserMessage("hello")})
		assert.Error(t, err)
		assert.Nil(t, output)
		assert.Contains(t, err.Error(), "callback error")
	})

	t.Run("finalize error propagates", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		cfg := &Config{
			Model: cm,
			Finalize: func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
				return nil, fmt.Errorf("finalize error")
			},
		}

		output, err := SummarizeMessages(ctx, cfg, []adk.Message{schema.UserMessage("hello")})
		assert.Error(t, err)
		assert.Nil(t, output)
		assert.Contains(t, err.Error(), "finalize error")
	})

	t.Run("preserves system messages", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		cfg := &Config{
			Model: cm,
		}

		messages := []adk.Message{
			schema.SystemMessage("System 1"),
			schema.SystemMessage("System 2"),
			schema.UserMessage(strings.Repeat("a", 100)),
		}

		output, err := SummarizeMessages(ctx, cfg, messages)
		assert.NoError(t, err)
		assert.NotNil(t, output)
		assert.Len(t, output.FinalizedMessages, 3)
		assert.Equal(t, schema.System, output.FinalizedMessages[0].Role)
		assert.Equal(t, "System 1", output.FinalizedMessages[0].Content)
		assert.Equal(t, schema.System, output.FinalizedMessages[1].Role)
		assert.Equal(t, "System 2", output.FinalizedMessages[1].Content)
	})

	t.Run("custom token counter is used", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary\n<all_user_messages>\n    - old\n</all_user_messages>",
			}, nil).Times(1)

		tokenCounterCalled := false
		cfg := &Config{
			Model: cm,
			TokenCounter: func(ctx context.Context, input *TokenCounterInput) (int, error) {
				tokenCounterCalled = true
				return 42, nil
			},
		}

		messages := []adk.Message{
			schema.UserMessage("msg1"),
			schema.AssistantMessage("resp", nil),
			schema.UserMessage("msg2"),
		}

		output, err := SummarizeMessages(ctx, cfg, messages)
		assert.NoError(t, err)
		assert.NotNil(t, output)
		assert.True(t, tokenCounterCalled)
	})
}
