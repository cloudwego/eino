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
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func intPtr(v int) *int {
	return &v
}

type fixedSummaryTestModel struct {
	content string
}

func (m *fixedSummaryTestModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(m.content, nil), nil
}

func (m *fixedSummaryTestModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(m.content, nil)}), nil
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

func TestAttack_SummarizationMessagesReplacedUsesReasonOnly(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		Model:   &fixedSummaryTestModel{content: "summary"},
		Trigger: &TriggerCondition{ContextMessages: 1},
	})
	require.NoError(t, err)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "summary-extra-agent",
		Description: "summary extra test",
		Instruction: "test",
		Model:       &fixedSummaryTestModel{content: "ok"},
		Handlers:    []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)

	store := session.NewInMemoryStore[*schema.Message](nil)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        agent,
		SessionID:    "summary-extra-session",
		SessionStore: store,
		SessionConfig: &adk.SessionConfig[*schema.Message]{
			EventExtraProvider: func(_ context.Context, event *adk.SessionEvent[*schema.Message]) (map[string]any, error) {
				if event.Kind == adk.SessionEventMessagesReplaced && event.Extra["_eino_reason"] == "context_summarized" {
					return map[string]any{"reason": "business_summary"}, nil
				}
				return nil, nil
			},
		},
	})

	iter := runner.Run(ctx, []*schema.Message{
		schema.UserMessage("first"),
		schema.UserMessage("second"),
	})
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}

	res, err := store.LoadEvents(ctx, "summary-extra-session", &adk.LoadSessionEventsRequest{
		Kinds: []adk.SessionEventKind{adk.SessionEventMessagesReplaced},
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	extra := res.Events[0].Extra
	assert.NotContains(t, extra, "_eino_source")
	assert.Equal(t, "context_summarized", extra["_eino_reason"])
	assert.Equal(t, "business_summary", extra["reason"])
}

func TestMiddlewareBeforeModelRewriteState(t *testing.T) {
	ctx := context.Background()
	mtx := &adk.ModelContext{}

	t.Run("no summarization when under threshold", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 1000},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

	t.Run("marked runtime system messages stripped from MessagesReplaced event payload", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				assert.Equal(t, schema.System, msgs[0].Role)
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary content",
				}, nil
			}).Times(1)

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
		}

		runtimeSys := schema.SystemMessage("runtime generated system")
		setMsgExtra(runtimeSys, extraKeyRuntimeGeneratedSystemMessage, true)

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				runtimeSys,
				schema.UserMessage(strings.Repeat("a", 100)),
				schema.AssistantMessage(strings.Repeat("b", 100), nil),
			},
		}

		_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
		assert.Len(t, newState.Messages, 2)
		assert.Equal(t, schema.System, newState.Messages[0].Role)
		assert.Equal(t, "runtime generated system", newState.Messages[0].Content)

		eventPayload := stripRuntimeGeneratedLeadingSystemMessages(newState.Messages)
		assert.Len(t, eventPayload, 1)
		assert.Equal(t, schema.User, eventPayload[0].Role)
	})

	t.Run("unmarked caller system messages preserved in both runtime and event payload", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				assert.Equal(t, schema.System, msgs[0].Role)
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary content",
				}, nil
			}).Times(1)

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
		}

		callerSys := schema.SystemMessage("caller supplied system")

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				callerSys,
				schema.UserMessage(strings.Repeat("a", 100)),
				schema.AssistantMessage(strings.Repeat("b", 100), nil),
			},
		}

		_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
		assert.Len(t, newState.Messages, 2)
		assert.Equal(t, schema.System, newState.Messages[0].Role)
		assert.Equal(t, "caller supplied system", newState.Messages[0].Content)

		eventPayload := stripRuntimeGeneratedLeadingSystemMessages(newState.Messages)
		assert.Len(t, eventPayload, 2)
		assert.Equal(t, schema.System, eventPayload[0].Role)
		assert.Equal(t, "caller supplied system", eventPayload[0].Content)
	})

	t.Run("preserves multiple system messages", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
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
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Retry: &RetryConfig{
					MaxRetries:  intPtr(2),
					BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
				},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   cm,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Retry: &RetryConfig{
					BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
				},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
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
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
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
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   primary,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Failover: &FailoverConfig{
					MaxRetries:  intPtr(1),
					BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
					GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
					},
				},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:   primary,
				Trigger: &TriggerCondition{ContextTokens: 10},
				Failover: &FailoverConfig{
					MaxRetries:  intPtr(2),
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
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
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

		mw := &TypedMiddleware[*schema.Message]{
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
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		_, _, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
	})

	t.Run("ReusePromptCaching appends user instruction to original messages without rebuilding", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		sysMsg := schema.SystemMessage("caller supplied system")
		userMsg := schema.UserMessage(strings.Repeat("a", 100))
		assistantMsg := schema.AssistantMessage(strings.Repeat("b", 100), nil)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				require.Len(t, msgs, 4)
				assert.Same(t, sysMsg, msgs[0])
				assert.Same(t, userMsg, msgs[1])
				assert.Same(t, assistantMsg, msgs[2])
				assert.Equal(t, schema.User, msgs[3].Role)
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary content",
				}, nil
			}).Times(1)

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:              cm,
				Trigger:            &TriggerCondition{ContextTokens: 10},
				ReusePromptCaching: true,
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{sysMsg, userMsg, assistantMsg},
		}

		_, newState, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
		assert.Len(t, newState.Messages, 2)
		assert.Equal(t, schema.System, newState.Messages[0].Role)
		assert.Equal(t, "caller supplied system", newState.Messages[0].Content)
		assert.Equal(t, schema.User, newState.Messages[1].Role)
	})

	t.Run("ReusePromptCaching injects state tool infos as model.WithTools", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		stateTools := []*schema.ToolInfo{
			{Name: "state_tool_a"},
			{Name: "state_tool_b"},
		}

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				common := model.GetCommonOptions(&model.Options{}, opts...)
				require.Len(t, common.Tools, 2)
				assert.Equal(t, "state_tool_a", common.Tools[0].Name)
				assert.Equal(t, "state_tool_b", common.Tools[1].Name)
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary content",
				}, nil
			}).Times(1)

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:              cm,
				Trigger:            &TriggerCondition{ContextTokens: 10},
				ReusePromptCaching: true,
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
				schema.AssistantMessage(strings.Repeat("b", 100), nil),
			},
			ToolInfos: stateTools,
		}

		_, _, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
	})

	t.Run("ReusePromptCaching injects state deferred tool infos as model.WithDeferredTools", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)

		stateTools := []*schema.ToolInfo{
			{Name: "state_tool_a"},
		}
		deferredTools := []*schema.ToolInfo{
			{Name: "deferred_tool_a"},
			{Name: "deferred_tool_b"},
		}

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				common := model.GetCommonOptions(&model.Options{}, opts...)
				require.Len(t, common.Tools, 1)
				assert.Equal(t, "state_tool_a", common.Tools[0].Name)
				require.Len(t, common.DeferredTools, 2)
				assert.Equal(t, "deferred_tool_a", common.DeferredTools[0].Name)
				assert.Equal(t, "deferred_tool_b", common.DeferredTools[1].Name)
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary content",
				}, nil
			}).Times(1)

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:              cm,
				Trigger:            &TriggerCondition{ContextTokens: 10},
				ReusePromptCaching: true,
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
				schema.AssistantMessage(strings.Repeat("b", 100), nil),
			},
			ToolInfos:         stateTools,
			DeferredToolInfos: deferredTools,
		}

		_, _, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
	})

	t.Run("ReusePromptCaching with failover injects state tool infos into failover model call", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		failover := mockModel.NewMockBaseChatModel(ctrl)

		stateTools := []*schema.ToolInfo{
			{Name: "state_tool_a"},
		}

		primary.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "partial output",
			}, fmt.Errorf("primary error")).Times(1)
		failover.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				common := model.GetCommonOptions(&model.Options{}, opts...)
				require.Len(t, common.Tools, 1)
				assert.Equal(t, "state_tool_a", common.Tools[0].Name)
				return &schema.Message{
					Role:    schema.Assistant,
					Content: "Summary from failover",
				}, nil
			}).Times(1)

		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{
				Model:              primary,
				Trigger:            &TriggerCondition{ContextTokens: 10},
				ReusePromptCaching: true,
				Failover: &FailoverConfig{
					GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
						return failover, []*schema.Message{schema.UserMessage("failover input")}, nil
					},
				},
			},
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
		}

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage(strings.Repeat("a", 100)),
			},
			ToolInfos: stateTools,
		}

		_, _, err := mw.BeforeModelRewriteState(ctx, state, mtx)
		assert.NoError(t, err)
	})

}

func TestMiddlewareShouldSummarize(t *testing.T) {
	ctx := context.Background()

	t.Run("returns true when over messages threshold", func(t *testing.T) {
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
			cfg: &Config{},
		}

		input := &TokenCounterInput{
			Messages: []adk.Message{schema.UserMessage("test")},
		}
		tokens, err := mw.countTokens(ctx, input)
		assert.NoError(t, err)
		assert.Greater(t, tokens, 0)
	})

	t.Run("custom token counter error", func(t *testing.T) {
		mw := &TypedMiddleware[*schema.Message]{
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

func TestGetUserMsgTextContent(t *testing.T) {
	t.Run("Message extracts from Content field", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "hello world",
		}
		assert.Equal(t, "hello world", getUserMsgTextContent(msg))
	})

	t.Run("Message extracts from UserInputMultiContent", func(t *testing.T) {
		msg := &schema.Message{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "part1"},
				{Type: schema.ChatMessagePartTypeText, Text: "part2"},
			},
		}
		assert.Equal(t, "part1\npart2", getUserMsgTextContent(msg))
	})

	t.Run("Message prefers UserInputMultiContent over Content", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "content field",
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "multi content"},
			},
		}
		assert.Equal(t, "multi content", getUserMsgTextContent(msg))
	})

	t.Run("Message nil returns empty", func(t *testing.T) {
		assert.Equal(t, "", getUserMsgTextContent[*schema.Message](nil))
	})

	t.Run("AgenticMessage extracts UserInputText", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{UserInputText: &schema.UserInputText{Text: "user input"}},
			},
		}
		assert.Equal(t, "user input", getUserMsgTextContent(msg))
	})

	t.Run("AgenticMessage nil returns empty", func(t *testing.T) {
		assert.Equal(t, "", getUserMsgTextContent[*schema.AgenticMessage](nil))
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

	setMsgExtra(msg, extraKeyContentType, string(contentTypeSummary))

	ct := typedGetContentType(msg)
	assert.Equal(t, contentTypeSummary, ct)
}

func TestSetGetExtra(t *testing.T) {
	t.Run("set and get", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "test",
		}

		setMsgExtra(msg, "key", "value")

		extra := getMsgExtra(msg)
		v, ok := extra["key"].(string)
		assert.True(t, ok)
		assert.Equal(t, "value", v)
	})

	t.Run("get non-existent key", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.User,
			Content: "test",
		}

		extra := getMsgExtra(msg)
		assert.Nil(t, extra)
	})
}

func TestMiddlewareBuildSummarizationModelInput(t *testing.T) {
	ctx := context.Background()

	t.Run("message structure", func(t *testing.T) {
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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

		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		summary := newTypedSummaryMessage[*schema.Message](resp.Content)
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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

func TestPopulateUserMessagesInternal(t *testing.T) {
	ctx := context.Background()

	t.Run("replaces user messages section", func(t *testing.T) {
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

		result, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[*schema.Message]{
			contextMsgs: msgs,
			summaryText: summary,
		})
		assert.NoError(t, err)
		assert.Contains(t, result, "msg1")
		assert.Contains(t, result, "msg2")
		assert.NotContains(t, result, "old message")
		assert.Contains(t, result, "7. Pending Tasks:")
	})

	t.Run("returns original if no matching sections", func(t *testing.T) {
		msgs := []adk.Message{
			schema.UserMessage("test"),
		}

		summary := "summary without sections"
		result, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[*schema.Message]{
			contextMsgs: msgs,
			summaryText: summary,
		})
		assert.NoError(t, err)
		assert.Equal(t, summary, result)
	})

	t.Run("skips summary messages", func(t *testing.T) {
		summaryMsg := &schema.Message{
			Role:    schema.User,
			Content: "summary",
		}
		setMsgExtra(summaryMsg, extraKeyContentType, string(contentTypeSummary))

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

		result, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[*schema.Message]{
			contextMsgs: msgs,
			summaryText: summary,
		})
		assert.NoError(t, err)
		assert.Contains(t, result, "regular message")
		assert.NotContains(t, result, "    - summary")
	})

	t.Run("returns original if empty user messages", func(t *testing.T) {
		msgs := []adk.Message{
			schema.AssistantMessage("response", nil),
		}

		summary := `6. All user messages:
    - [old]

7. Pending Tasks:
   - task`

		result, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[*schema.Message]{
			contextMsgs: msgs,
			summaryText: summary,
		})
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
		result := defaultTypedTrimUserMessage(msg, 0)
		assert.Nil(t, result)
	})

	t.Run("returns nil for empty content", func(t *testing.T) {
		msg := schema.UserMessage("")
		result := defaultTypedTrimUserMessage(msg, 100)
		assert.Nil(t, result)
	})

	t.Run("trims long message", func(t *testing.T) {
		longText := strings.Repeat("a", 3000)
		msg := schema.UserMessage(longText)
		result := defaultTypedTrimUserMessage(msg, 100)
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
		count, err := defaultTypedTokenCounter(ctx, input)
		assert.NoError(t, err)
		assert.Greater(t, count, 0)
	})

	t.Run("reuses latest assistant total tokens as baseline", func(t *testing.T) {
		input := &TokenCounterInput{
			Messages: []adk.Message{
				schema.UserMessage("earlier context"),
				{
					Role:    schema.Assistant,
					Content: "baseline",
					ResponseMeta: &schema.ResponseMeta{
						Usage: &schema.TokenUsage{TotalTokens: 100},
					},
				},
				schema.UserMessage("later context"),
			},
		}

		count, err := defaultTypedTokenCounter(ctx, input)
		require.NoError(t, err)
		assert.Equal(t, 100+estimateMessageTokens(schema.UserMessage("later context")), count)
	})
}

func TestGetAssistantTotalTokens(t *testing.T) {
	t.Run("returns zero for nil message", func(t *testing.T) {
		assert.Zero(t, getAssistantTotalTokens[*schema.Message](nil))
		assert.Zero(t, getAssistantTotalTokens[*schema.AgenticMessage](nil))
	})

	t.Run("reads total tokens from assistant messages only", func(t *testing.T) {
		msg := &schema.Message{
			Role: schema.Assistant,
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{TotalTokens: 42},
			},
		}
		assert.Equal(t, 42, getAssistantTotalTokens(msg))
		assert.Zero(t, getAssistantTotalTokens(schema.UserMessage("ignored")))
	})

	t.Run("reads total tokens from agentic assistant messages only", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ResponseMeta: &schema.AgenticResponseMeta{
				TokenUsage: &schema.TokenUsage{TotalTokens: 64},
			},
		}
		assert.Equal(t, 64, getAssistantTotalTokens(msg))
		assert.Zero(t, getAssistantTotalTokens(schema.UserAgenticMessage("ignored")))
	})
}

func TestEstimateMessageTokens(t *testing.T) {
	t.Run("returns zero for nil message", func(t *testing.T) {
		assert.Zero(t, estimateMessageTokens(nil))
	})

	t.Run("counts assistant text reasoning and tool calls", func(t *testing.T) {
		msg := &schema.Message{
			Role:             schema.Assistant,
			ReasoningContent: "reason",
			ToolCalls: []schema.ToolCall{
				{
					Function: schema.FunctionCall{
						Name:      "tool",
						Arguments: `{"k":"v"}`,
					},
				},
			},
			AssistantGenMultiContent: []schema.MessageOutputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "answer"},
			},
		}

		expectedLen := len("answer") + len("reason") + len("tool") + len(`{"k":"v"}`)
		assert.Equal(t, estimateTokenCount(expectedLen), estimateMessageTokens(msg))
	})

	t.Run("adds multimodal estimate for user content", func(t *testing.T) {
		msg := &schema.Message{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "hello"},
				{Type: schema.ChatMessagePartTypeImageURL},
			},
		}

		assert.Equal(t, estimateTokenCount(len("hello"))+multimodalTokenEstimate, estimateMessageTokens(msg))
	})
}

func TestEstimateAgenticMessageTokens(t *testing.T) {
	t.Run("returns zero for nil message", func(t *testing.T) {
		assert.Zero(t, estimateAgenticMessageTokens(nil))
	})

	t.Run("counts assistant blocks and multimodal outputs", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "answer"}),
				schema.NewContentBlock(&schema.Reasoning{Text: "reason"}),
				schema.NewContentBlock(&schema.FunctionToolCall{Name: "tool", Arguments: `{"k":"v"}`}),
				schema.NewContentBlock(&schema.AssistantGenImage{}),
			},
		}

		expectedLen := len("answer") + len("reason") + len("tool") + len(`{"k":"v"}`)
		assert.Equal(t, estimateTokenCount(expectedLen)+multimodalTokenEstimate, estimateAgenticMessageTokens(msg))
	})
}

func TestPostProcessSummary(t *testing.T) {
	ctx := context.Background()

	t.Run("with transcript path", func(t *testing.T) {
		result, err := postProcessSummary(ctx, &postProcessSummaryParams[*schema.Message]{
			contextMsgs:    []adk.Message{},
			summaryContent: "summary content",
			transcriptPath: "/path/to/transcript.txt",
		})
		assert.NoError(t, err)
		assert.Contains(t, result.Content, "/path/to/transcript.txt")
		assert.Contains(t, result.Content, getContinueInstruction())
	})
}

func TestEventHelpers(t *testing.T) {
	ctx := context.Background()

	t.Run("emitEvent is no-op outside execution context", func(t *testing.T) {
		mw := &TypedMiddleware[*schema.Message]{cfg: &Config{}}
		err := mw.emitEvent(ctx, &CustomizedAction{Type: ActionTypeBeforeSummarize})
		assert.NoError(t, err)
	})

	t.Run("emitGenerateSummaryEvent is skipped when internal events are disabled", func(t *testing.T) {
		mw := &TypedMiddleware[*schema.Message]{cfg: &Config{EmitInternalEvents: false}}
		err := mw.emitGenerateSummaryEvent(ctx, 1, GenerateSummaryPhasePrimary, schema.AssistantMessage("ok", nil), nil)
		assert.NoError(t, err)
	})

	t.Run("emitGenerateSummaryEvent is no-op when enabled outside execution context", func(t *testing.T) {
		mw := &TypedMiddleware[*schema.Message]{cfg: &Config{EmitInternalEvents: true}}
		err := mw.emitGenerateSummaryEvent(ctx, 1, GenerateSummaryPhasePrimary, schema.AssistantMessage("ok", nil), nil)
		assert.NoError(t, err)
	})
}

func TestGetFailoverModel(t *testing.T) {
	ctx := context.Background()
	defaultInput := []adk.Message{schema.UserMessage("default")}
	fctx := &FailoverContext{Attempt: 1}

	t.Run("requires failover config", func(t *testing.T) {
		mw := &TypedMiddleware[*schema.Message]{cfg: &Config{}}
		mdl, input, err := mw.getFailoverModel(ctx, fctx, defaultInput)
		assert.Nil(t, mdl)
		assert.Nil(t, input)
		assert.ErrorContains(t, err, "failover config is required")
	})

	t.Run("uses primary model and default input when callback is not provided", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
		mw := &TypedMiddleware[*schema.Message]{
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
	t.Run("should failover branches", func(t *testing.T) {
		assert.False(t, typedShouldFailover(context.Background(), (*FailoverConfig)(nil), nil, errors.New("x")))
		assert.False(t, typedShouldFailover(context.Background(), &FailoverConfig{}, nil, nil))
		assert.True(t, typedShouldFailover(context.Background(), &FailoverConfig{}, nil, errors.New("x")))

		cfg := &FailoverConfig{
			ShouldFailover: func(ctx context.Context, resp adk.Message, err error) bool {
				return resp != nil && err == nil
			},
		}
		assert.True(t, typedShouldFailover(context.Background(), cfg, schema.AssistantMessage("ok", nil), nil))
	})

	t.Run("config check branches", func(t *testing.T) {
		assert.ErrorContains(t, (&RetryConfig{MaxRetries: intPtr(-1)}).check(), "retry.MaxRetries must be non-negative")
		assert.ErrorContains(t, (&FailoverConfig{MaxRetries: intPtr(-1)}).check(), "failover.MaxRetries must be non-negative")
		assert.ErrorContains(t, (&TriggerCondition{}).check(), "at least one of contextTokens or contextMessages")
		assert.ErrorContains(t, (&TriggerCondition{ContextTokens: -1}).check(), "contextTokens must be non-negative")
		assert.ErrorContains(t, (&TriggerCondition{ContextMessages: -1, ContextTokens: 1}).check(), "contextMessages must be non-negative")
	})

	t.Run("default backoff branches", func(t *testing.T) {
		assert.Equal(t, time.Second, defaultBackoffDuration(0))

		delay := defaultBackoffDuration(8)
		assert.GreaterOrEqual(t, delay, 10*time.Second)
		assert.Less(t, delay, 15*time.Second)
	})

	t.Run("user messages replaced note is present", func(t *testing.T) {
		note := getUserMessagesReplacedNote()
		assert.NotEmpty(t, note)
		assert.Contains(t, []string{userMessagesReplacedNote, userMessagesReplacedNoteZh}, note)
	})
}

func TestSummarize(t *testing.T) {
	ctx := context.Background()

	newMW := func(cfg *Config) *TypedMiddleware[*schema.Message] {
		return &TypedMiddleware[*schema.Message]{
			cfg:                               cfg,
			TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
		}
	}

	t.Run("basic summarization", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary content",
			}, nil).Times(1)

		mw := newMW(&Config{Model: cm})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.SystemMessage("You are a helpful assistant"),
				schema.UserMessage(strings.Repeat("a", 100)),
				schema.AssistantMessage(strings.Repeat("b", 100), nil),
			},
		}

		result, err := mw.Summarize(ctx, state)
		assert.NoError(t, err)
		assert.NotEmpty(t, result)
		assert.Equal(t, schema.System, result[0].Role)
	})

	t.Run("model error propagates", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("model error")).Times(1)

		mw := newMW(&Config{Model: cm})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{schema.UserMessage("hello")},
		}

		result, err := mw.Summarize(ctx, state)
		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("retry works", func(t *testing.T) {
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

		mw := newMW(&Config{
			Model: cm,
			Retry: &RetryConfig{
				MaxRetries:  intPtr(2),
				BackoffFunc: func(_ context.Context, _ int, _ adk.Message, _ error) time.Duration { return 0 },
			},
		})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{schema.UserMessage("hello")},
		}

		result, err := mw.Summarize(ctx, state)
		assert.NoError(t, err)
		assert.NotEmpty(t, result)
		assert.Equal(t, 2, callCount)
	})

	t.Run("failover works", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		primary := mockModel.NewMockBaseChatModel(ctrl)
		failoverModel := mockModel.NewMockBaseChatModel(ctrl)

		primary.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("primary error")).Times(1)
		failoverModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary from failover",
			}, nil).Times(1)

		mw := newMW(&Config{
			Model: primary,
			Failover: &FailoverConfig{
				GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext) (model.BaseChatModel, []*schema.Message, error) {
					return failoverModel, []*schema.Message{schema.UserMessage("failover input")}, nil
				},
			},
		})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{schema.UserMessage("hello")},
		}

		result, err := mw.Summarize(ctx, state)
		assert.NoError(t, err)
		assert.NotEmpty(t, result)
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
		mw := newMW(&Config{
			Model: cm,
			Callback: func(ctx context.Context, before, after adk.ChatModelAgentState) error {
				callbackCalled = true
				assert.Len(t, before.Messages, 1)
				assert.NotEmpty(t, after.Messages)
				return nil
			},
		})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{schema.UserMessage("hello")},
		}

		result, err := mw.Summarize(ctx, state)
		assert.NoError(t, err)
		assert.NotNil(t, result)
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

		mw := newMW(&Config{
			Model: cm,
			Finalize: func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
				return []adk.Message{
					schema.SystemMessage("custom system"),
					summary,
				}, nil
			},
		})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{schema.UserMessage("hello")},
		}

		result, err := mw.Summarize(ctx, state)
		assert.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, schema.System, result[0].Role)
		assert.Equal(t, "custom system", result[0].Content)
	})

	t.Run("callback error propagates", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockBaseChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&schema.Message{
				Role:    schema.Assistant,
				Content: "Summary",
			}, nil).Times(1)

		mw := newMW(&Config{
			Model: cm,
			Callback: func(ctx context.Context, before, after adk.ChatModelAgentState) error {
				return fmt.Errorf("callback error")
			},
		})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{schema.UserMessage("hello")},
		}

		result, err := mw.Summarize(ctx, state)
		assert.Error(t, err)
		assert.Nil(t, result)
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

		mw := newMW(&Config{
			Model: cm,
			Finalize: func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
				return nil, fmt.Errorf("finalize error")
			},
		})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{schema.UserMessage("hello")},
		}

		result, err := mw.Summarize(ctx, state)
		assert.Error(t, err)
		assert.Nil(t, result)
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

		mw := newMW(&Config{Model: cm})

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.SystemMessage("System 1"),
				schema.SystemMessage("System 2"),
				schema.UserMessage(strings.Repeat("a", 100)),
			},
		}

		result, err := mw.Summarize(ctx, state)
		assert.NoError(t, err)
		assert.Len(t, result, 3)
		assert.Equal(t, schema.System, result[0].Role)
		assert.Equal(t, "System 1", result[0].Content)
		assert.Equal(t, schema.System, result[1].Role)
		assert.Equal(t, "System 2", result[1].Content)
	})
}

func TestNewTypedAgenticMessage(t *testing.T) {
	ctx := context.Background()

	// TypedConfig requires a Model, so passing an empty config will return an error.
	// This test verifies that NewTyped[*schema.AgenticMessage] compiles correctly.
	mw, err := NewTyped(ctx, &TypedConfig[*schema.AgenticMessage]{})
	assert.Error(t, err)
	assert.Nil(t, mw)

	// Verify the return type is correct at compile time.
	var _ adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] = mw
}

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
// Generic tests
// ============================================================================

func TestSummarizationGeneric(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		t.Run("Helpers", testSummarizationHelpers[*schema.Message])
		t.Run("Flow", testSummarizationFlow[*schema.Message])
		t.Run("TokenCounterUsesStateToolInfos", testTokenCounterReceivesStateToolInfos[*schema.Message])
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		t.Run("Helpers", testSummarizationHelpers[*schema.AgenticMessage])
		t.Run("Flow", testSummarizationFlow[*schema.AgenticMessage])
		t.Run("TokenCounterUsesStateToolInfos", testTokenCounterReceivesStateToolInfos[*schema.AgenticMessage])
	})
}

func TestEmitInternalEvents_AgenticMessage_NoopOutsideExecContext(t *testing.T) {
	ctx := context.Background()

	longContent := strings.Repeat("x", 800000)
	msgs := []*schema.AgenticMessage{
		{
			Role: schema.AgenticRoleTypeSystem,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "system"}),
			},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: longContent}),
			},
		},
	}

	mockResp := smakeAssistantMsg[*schema.AgenticMessage]("This is the summary.")
	mw, err := NewTyped(ctx, &TypedConfig[*schema.AgenticMessage]{
		Model:              &genericMockModel[*schema.AgenticMessage]{response: mockResp},
		EmitInternalEvents: true,
		Trigger: &TriggerCondition{
			ContextTokens: 1,
		},
	})
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: msgs}
	_, gotState, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.NotNil(t, gotState)
	require.Len(t, gotState.Messages, 2)
	assert.Equal(t, schema.AgenticRoleTypeSystem, gotState.Messages[0].Role)
	assert.Equal(t, schema.AgenticRoleTypeUser, gotState.Messages[1].Role)
}

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

	t.Run("getUserMsgTextContent", func(t *testing.T) {
		usr := smakeUserMsg[M]("hello world")
		assert.Equal(t, "hello world", getUserMsgTextContent(usr))
	})

	t.Run("getMsgExtra_setMsgExtra", func(t *testing.T) {
		msg := smakeUserMsg[M]("test")
		extra := getMsgExtra(msg)
		assert.Nil(t, extra)

		setMsgExtra(msg, "key1", "value1")
		extra = getMsgExtra(msg)
		assert.Equal(t, "value1", extra["key1"])
	})

	t.Run("makeSystemMsg", func(t *testing.T) {
		msg := makeSystemMsg[M]("system prompt")
		assert.True(t, isSystemRole(msg))
		switch m := any(msg).(type) {
		case *schema.Message:
			assert.Equal(t, "system prompt", m.Content)
		case *schema.AgenticMessage:
			require.Len(t, m.ContentBlocks, 1)
			assert.Equal(t, "system prompt", m.ContentBlocks[0].UserInputText.Text)
		}
	})

	t.Run("makeUserMsg", func(t *testing.T) {
		msg := makeUserMsg[M]("user input")
		assert.True(t, isUserRole(msg))
		assert.Equal(t, "user input", getUserMsgTextContent(msg))
	})

	t.Run("newTypedSummaryMessage", func(t *testing.T) {
		msg := newTypedSummaryMessage[M]("summary content")

		assert.True(t, isUserRole(msg))

		switch m := any(msg).(type) {
		case *schema.Message:
			assert.Equal(t, schema.User, m.Role)
			assert.Equal(t, "summary content", m.Content)
		case *schema.AgenticMessage:
			assert.Equal(t, schema.AgenticRoleTypeUser, m.Role)
			require.Len(t, m.ContentBlocks, 1)
			assert.Equal(t, "summary content", m.ContentBlocks[0].UserInputText.Text)
		}
	})

	t.Run("isInnerMessage", func(t *testing.T) {
		summaryMsg := newTypedSummaryMessage[M]("summary content")
		assert.True(t, isInternalUserMessage(summaryMsg))
		assert.False(t, isPreservedMessage(summaryMsg))

		skillsMsg := makeUserMsg[M]("skills content")
		setMsgExtra(skillsMsg, extraKeyContentType, string(contentTypeSkills))
		assert.True(t, isInternalUserMessage(skillsMsg))
		assert.True(t, isPreservedMessage(skillsMsg))

		normalMsg := makeUserMsg[M]("normal content")
		assert.False(t, isInternalUserMessage(normalMsg))
		assert.False(t, isPreservedMessage(normalMsg))
	})
}

func testSummarizationFlow[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	summaryText := "This is a summary of the conversation."
	mockModel := &genericMockModel[M]{
		response: smakeAssistantMsg[M](summaryText),
	}

	tokenCounter := func(_ context.Context, input *TypedTokenCounterInput[M]) (int, error) {
		total := 0
		for _, msg := range input.Messages {
			total += len(getUserMsgTextContent(msg))
		}
		return total, nil
	}

	cfg := &TypedConfig[M]{
		Model:        mockModel,
		TokenCounter: tokenCounter,
		Trigger: &TriggerCondition{
			ContextTokens: 20,
		},
	}

	mw, err := NewTyped(ctx, cfg)
	require.NoError(t, err)

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

	require.GreaterOrEqual(t, len(newState.Messages), 2,
		"should have at least system + summary messages")

	assert.True(t, isSystemRole(newState.Messages[0]),
		"first message should be system")

	foundSummary := false
	for _, msg := range newState.Messages {
		extra := getMsgExtra(msg)
		if extra != nil {
			if ct, ok := extra[extraKeyContentType]; ok && ct == string(contentTypeSummary) {
				foundSummary = true
				break
			}
		}
		if strings.Contains(getUserMsgTextContent(msg), summaryText) {
			foundSummary = true
			break
		}
	}
	assert.True(t, foundSummary, "should have a summary message")
}

func testTokenCounterReceivesStateToolInfos[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	stateTools := []*schema.ToolInfo{
		{Name: "state_tool_a"},
		{Name: "state_tool_b"},
	}
	mcTools := []*schema.ToolInfo{
		{Name: "mc_tool_should_not_appear"},
	}

	var receivedTools []*schema.ToolInfo
	tokenCounter := func(_ context.Context, input *TypedTokenCounterInput[M]) (int, error) {
		receivedTools = input.Tools
		return 0, nil
	}

	cfg := &TypedConfig[M]{
		Model: &genericMockModel[M]{
			response: smakeAssistantMsg[M]("unused"),
		},
		TokenCounter: tokenCounter,
		Trigger: &TriggerCondition{
			ContextTokens: 9999,
		},
	}

	mw, err := NewTyped(ctx, cfg)
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[M]{
		Messages:  []M{smakeUserMsg[M]("hello")},
		ToolInfos: stateTools,
	}
	mc := &adk.TypedModelContext[M]{Tools: mcTools}

	_, _, err = mw.BeforeModelRewriteState(ctx, state, mc)
	require.NoError(t, err)

	require.NotNil(t, receivedTools, "token counter should have been called")
	require.Len(t, receivedTools, 2)
	assert.Equal(t, "state_tool_a", receivedTools[0].Name)
	assert.Equal(t, "state_tool_b", receivedTools[1].Name)
}

func TestGetAssistantTextContent(t *testing.T) {
	t.Run("schema.Message with MultiContent", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.Assistant,
			Content: "fallback content",
			AssistantGenMultiContent: []schema.MessageOutputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "hello"},
				{Type: schema.ChatMessagePartTypeText, Text: "world"},
			},
		}
		got := getAssistantTextContent(msg)
		assert.Equal(t, "hello\nworld", got)
	})

	t.Run("schema.Message with MultiContent skips non-text parts", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.Assistant,
			Content: "fallback",
			AssistantGenMultiContent: []schema.MessageOutputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "text part"},
				{Type: schema.ChatMessagePartTypeImageURL},
				{Type: schema.ChatMessagePartTypeText, Text: ""},
			},
		}
		got := getAssistantTextContent(msg)
		assert.Equal(t, "text part", got)
	})

	t.Run("schema.Message falls back to Content when MultiContent is empty", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.Assistant,
			Content: "plain content",
		}
		got := getAssistantTextContent(msg)
		assert.Equal(t, "plain content", got)
	})

	t.Run("schema.Message falls back to Content when MultiContent has no text", func(t *testing.T) {
		msg := &schema.Message{
			Role:    schema.Assistant,
			Content: "fallback",
			AssistantGenMultiContent: []schema.MessageOutputPart{
				{Type: schema.ChatMessagePartTypeImageURL},
			},
		}
		got := getAssistantTextContent(msg)
		assert.Equal(t, "fallback", got)
	})

	t.Run("schema.AgenticMessage with multiple text blocks", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "first"}),
				schema.NewContentBlock(&schema.AssistantGenText{Text: "second"}),
			},
		}
		got := getAssistantTextContent(msg)
		assert.Equal(t, "first\nsecond", got)
	})

	t.Run("schema.AgenticMessage with nil blocks", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				nil,
				schema.NewContentBlock(&schema.AssistantGenText{Text: "only"}),
				nil,
			},
		}
		got := getAssistantTextContent(msg)
		assert.Equal(t, "only", got)
	})

	t.Run("schema.AgenticMessage with no text blocks", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role:          schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{},
		}
		got := getAssistantTextContent(msg)
		assert.Equal(t, "", got)
	})
}

func TestStripRuntimeGeneratedLeadingSystemMessages(t *testing.T) {
	t.Run("no system messages", func(t *testing.T) {
		msgs := []*schema.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("hi", nil),
		}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 2)
		assert.Equal(t, "hello", result[0].Content)
	})

	t.Run("unmarked system message preserved", func(t *testing.T) {
		sys := schema.SystemMessage("durable system")
		msgs := []*schema.Message{
			sys,
			schema.UserMessage("hello"),
		}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 2)
		assert.Equal(t, schema.System, result[0].Role)
		assert.Equal(t, "durable system", result[0].Content)
	})

	t.Run("single marked system message stripped", func(t *testing.T) {
		sys := schema.SystemMessage("runtime system")
		setMsgExtra(sys, extraKeyRuntimeGeneratedSystemMessage, true)
		msgs := []*schema.Message{
			sys,
			schema.UserMessage("hello"),
		}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 1)
		assert.Equal(t, schema.User, result[0].Role)
		assert.Equal(t, "hello", result[0].Content)
	})

	t.Run("multiple marked system messages stripped", func(t *testing.T) {
		sys1 := schema.SystemMessage("runtime 1")
		setMsgExtra(sys1, extraKeyRuntimeGeneratedSystemMessage, true)
		sys2 := schema.SystemMessage("runtime 2")
		setMsgExtra(sys2, extraKeyRuntimeGeneratedSystemMessage, true)
		msgs := []*schema.Message{
			sys1,
			sys2,
			schema.UserMessage("hello"),
		}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 1)
		assert.Equal(t, schema.User, result[0].Role)
	})

	t.Run("mixed marked and unmarked - only prefix stripped", func(t *testing.T) {
		marked := schema.SystemMessage("runtime")
		setMsgExtra(marked, extraKeyRuntimeGeneratedSystemMessage, true)
		unmarked := schema.SystemMessage("durable")
		msgs := []*schema.Message{
			marked,
			unmarked,
			schema.UserMessage("hello"),
		}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 2)
		assert.Equal(t, schema.System, result[0].Role)
		assert.Equal(t, "durable", result[0].Content)
	})

	t.Run("non-leading system message preserved", func(t *testing.T) {
		sys := schema.SystemMessage("mid-system")
		setMsgExtra(sys, extraKeyRuntimeGeneratedSystemMessage, true)
		msgs := []*schema.Message{
			schema.UserMessage("first"),
			sys,
			schema.AssistantMessage("resp", nil),
		}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 3)
		assert.Equal(t, schema.User, result[0].Role)
		assert.Equal(t, schema.System, result[1].Role)
	})

	t.Run("only marked system messages - empty result", func(t *testing.T) {
		sys1 := schema.SystemMessage("runtime 1")
		setMsgExtra(sys1, extraKeyRuntimeGeneratedSystemMessage, true)
		sys2 := schema.SystemMessage("runtime 2")
		setMsgExtra(sys2, extraKeyRuntimeGeneratedSystemMessage, true)
		msgs := []*schema.Message{sys1, sys2}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 0)
	})

	t.Run("agentic messages - marked stripped", func(t *testing.T) {
		sys := schema.SystemAgenticMessage("runtime system")
		setMsgExtra(sys, extraKeyRuntimeGeneratedSystemMessage, true)
		msgs := []*schema.AgenticMessage{
			sys,
			schema.UserAgenticMessage("hello"),
		}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 1)
		assert.Equal(t, schema.AgenticRoleTypeUser, result[0].Role)
	})

	t.Run("agentic messages - unmarked preserved", func(t *testing.T) {
		sys := schema.SystemAgenticMessage("durable system")
		msgs := []*schema.AgenticMessage{
			sys,
			schema.UserAgenticMessage("hello"),
		}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 2)
		assert.Equal(t, schema.AgenticRoleTypeSystem, result[0].Role)
	})

	t.Run("returns a new slice, not the original slice", func(t *testing.T) {
		sys := schema.SystemMessage("runtime")
		setMsgExtra(sys, extraKeyRuntimeGeneratedSystemMessage, true)
		user := schema.UserMessage("hello")
		assistant := schema.AssistantMessage("resp", nil)
		msgs := []*schema.Message{sys, user, assistant}
		result := stripRuntimeGeneratedLeadingSystemMessages(msgs)
		assert.Len(t, result, 2)
		originalLen := len(result)
		result = append(result, schema.UserMessage("extra"))
		assert.Len(t, msgs, 3, "appending to result must not affect original slice")
		assert.Len(t, result, originalLen+1)
	})
}

func TestIsMarkedRuntimeGeneratedSystemMessage(t *testing.T) {
	t.Run("marked system message", func(t *testing.T) {
		msg := schema.SystemMessage("test")
		setMsgExtra(msg, extraKeyRuntimeGeneratedSystemMessage, true)
		assert.True(t, isMarkedRuntimeGeneratedSystemMessage(msg))
	})

	t.Run("unmarked system message", func(t *testing.T) {
		msg := schema.SystemMessage("test")
		assert.False(t, isMarkedRuntimeGeneratedSystemMessage(msg))
	})

	t.Run("non-system message with marker", func(t *testing.T) {
		msg := schema.UserMessage("test")
		setMsgExtra(msg, extraKeyRuntimeGeneratedSystemMessage, true)
		assert.False(t, isMarkedRuntimeGeneratedSystemMessage(msg))
	})

	t.Run("nil extra", func(t *testing.T) {
		msg := schema.SystemMessage("test")
		msg.Extra = nil
		assert.False(t, isMarkedRuntimeGeneratedSystemMessage(msg))
	})

	t.Run("marker with wrong type", func(t *testing.T) {
		msg := schema.SystemMessage("test")
		setMsgExtra(msg, extraKeyRuntimeGeneratedSystemMessage, "yes")
		assert.False(t, isMarkedRuntimeGeneratedSystemMessage(msg))
	})
}
