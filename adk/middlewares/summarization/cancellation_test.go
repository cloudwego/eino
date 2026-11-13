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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

type cancellationTestModel[M adk.MessageType] struct {
	generate func(context.Context) (M, error)
	calls    int
}

func (m *cancellationTestModel[M]) Generate(ctx context.Context, _ []M, _ ...model.Option) (M, error) {
	m.calls++
	return m.generate(ctx)
}

func (m *cancellationTestModel[M]) Stream(context.Context, []M, ...model.Option) (*schema.StreamReader[M], error) {
	panic("unexpected streaming call")
}

func TestSummarizeCancellation(t *testing.T) {
	t.Run("Message", func(t *testing.T) { testSummarizeCancellation[*schema.Message](t) })
	t.Run("AgenticMessage", func(t *testing.T) { testSummarizeCancellation[*schema.AgenticMessage](t) })
}

func testSummarizeCancellation[M adk.MessageType](t *testing.T) {
	for _, scenario := range []string{"before call", "expired deadline", "primary error", "primary success", "retry policy", "failover selection", "failover error", "failover success", "failover policy"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantErr := error(context.Canceled)
			if scenario == "before call" {
				cancel()
			}
			if scenario == "expired deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer stop()
				wantErr = context.DeadlineExceeded
			}
			var zero M
			transient := errors.New("temporary provider failure")
			primary := &cancellationTestModel[M]{generate: func(context.Context) (M, error) {
				if scenario == "primary error" || scenario == "primary success" {
					cancel()
				}
				if scenario == "primary success" {
					return zero, nil
				}
				return zero, transient
			}}
			fallback := &cancellationTestModel[M]{generate: func(context.Context) (M, error) {
				if scenario != "failover policy" {
					cancel()
				}
				if scenario == "failover success" || scenario == "failover policy" {
					return zero, nil
				}
				return zero, transient
			}}
			selections := 0
			finalized := false
			cfg := &TypedConfig[M]{
				Model:    primary,
				Trigger:  &TriggerCondition{ContextMessages: 1},
				Finalize: func(context.Context, []M, M) ([]M, error) { finalized = true; return nil, nil },
				Failover: &TypedFailoverConfig[M]{
					MaxRetries: intPtr(2),
					ShouldFailover: func(_ context.Context, _ M, err error) bool {
						if scenario == "failover policy" && fallback.calls > 0 {
							cancel()
							return false
						}
						return err != nil
					},
					BackoffFunc: func(context.Context, int, M, error) time.Duration { return 0 },
					GetFailoverModel: func(context.Context, *TypedFailoverContext[M]) (model.BaseModel[M], []M, error) {
						selections++
						if scenario == "failover selection" {
							cancel()
						}
						return fallback, []M{makeUserMsg[M]("summarize")}, nil
					},
				},
			}
			if scenario == "retry policy" {
				cfg.Retry = &TypedRetryConfig[M]{
					MaxRetries:  intPtr(2),
					ShouldRetry: func(context.Context, M, error) bool { cancel(); return true },
					BackoffFunc: func(context.Context, int, M, error) time.Duration { return 0 },
				}
			}
			mw, err := NewTyped(ctx, cfg)
			require.NoError(t, err)
			_, _, err = mw.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[M]{
				Messages: []M{makeUserMsg[M]("original conversation"), makeUserMsg[M]("next turn")},
			}, nil)
			require.ErrorIs(t, err, wantErr)
			require.False(t, finalized, "cancelled summaries must not replace conversation state")
			wantPrimary, wantSelections, wantFallback := 1, 0, 0
			switch scenario {
			case "before call", "expired deadline":
				wantPrimary = 0
			case "failover selection":
				wantSelections = 1
			case "failover error", "failover success", "failover policy":
				wantSelections, wantFallback = 1, 1
			}
			require.Equal(t, wantPrimary, primary.calls)
			require.Equal(t, wantSelections, selections)
			require.Equal(t, wantFallback, fallback.calls)
		})
	}
}

func TestSummarizeProviderTimeoutStillRetries(t *testing.T) {
	ctrl := gomock.NewController(t)
	cm := mockModel.NewMockBaseChatModel(ctrl)
	gomock.InOrder(
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, context.DeadlineExceeded),
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(schema.AssistantMessage("summary", nil), nil),
	)
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		Model:   cm,
		Trigger: &TriggerCondition{ContextMessages: 1},
		Retry: &RetryConfig{
			MaxRetries:  intPtr(1),
			BackoffFunc: func(context.Context, int, *schema.Message, error) time.Duration { return 0 },
		},
	})
	require.NoError(t, err)
	_, state, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.UserMessage("first"), schema.UserMessage("second")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NoError(t, ctx.Err())
}
