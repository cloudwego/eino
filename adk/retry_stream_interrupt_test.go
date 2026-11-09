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

package adk

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestRetryModelStreamPreservesInterrupt(t *testing.T) {
	for _, policy := range []string{"default", "is_retry_able", "should_retry"} {
		for _, delivery := range []string{"direct", "stream"} {
			t.Run(policy+"/"+delivery, func(t *testing.T) {
				ctx := context.Background()
				graph := compose.NewGraph[string, string]()
				require.NoError(t, graph.AddLambdaNode("pause", compose.InvokableLambda(
					func(ctx context.Context, _ string) (string, error) {
						if interrupted, _, _ := compose.GetInterruptState[string](ctx); interrupted {
							return "resumed", nil
						}
						return "", compose.Interrupt(ctx, "waiting for input")
					})))
				require.NoError(t, graph.AddEdge(compose.START, "pause"))
				require.NoError(t, graph.AddEdge("pause", compose.END))
				store := newMyStore()
				runnable, err := graph.Compile(ctx, compose.WithCheckPointStore(store))
				require.NoError(t, err)
				_, interruptErr := runnable.Invoke(ctx, "input", compose.WithCheckPointID("source-pause"))
				_, interrupted := compose.ExtractInterruptInfo(interruptErr)
				require.True(t, interrupted)

				ctrl := gomock.NewController(t)
				cm := mockModel.NewMockToolCallingChatModel(ctrl)
				calls := 0
				cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
						calls++
						if calls > 1 {
							return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("incorrect retry", nil)}), nil
						}
						if delivery == "direct" {
							return nil, interruptErr
						}
						sr, sw := schema.Pipe[*schema.Message](2)
						sw.Send(schema.AssistantMessage("partial", nil), nil)
						sw.Send(nil, interruptErr)
						sw.Close()
						return sr, nil
					}).AnyTimes()
				decisions := 0
				config := &ModelRetryConfig{
					MaxRetries:  1,
					BackoffFunc: func(context.Context, int) time.Duration { return 0 },
				}
				switch policy {
				case "is_retry_able":
					config.IsRetryAble = func(context.Context, error) bool {
						decisions++
						return true
					}
				case "should_retry":
					config.ShouldRetry = func(_ context.Context, retry *RetryContext) *RetryDecision {
						decisions++
						return &RetryDecision{Retry: retry.Err != nil}
					}
				}
				iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
				ctx = withTypedChatModelAgentExecCtx(ctx, &chatModelAgentExecCtx{generator: generator})
				sender := &typedEventSenderModel[*schema.Message]{inner: cm, modelRetryConfig: config}
				wrapper := newTypedRetryModelWrapper[*schema.Message](sender, config)
				output, err := wrapper.Stream(ctx, []*schema.Message{schema.UserMessage("hello")})
				if output != nil {
					output.Close()
				}
				generator.Close()
				require.Equal(t, 1, calls, "an interruption must not start another model call")
				require.ErrorIs(t, err, interruptErr)
				event, ok := iterator.Next()
				if delivery == "direct" {
					require.False(t, ok)
				} else {
					require.True(t, ok)
					stream := event.Output.MessageOutput.MessageStream
					defer stream.Close()
					chunk, recvErr := stream.Recv()
					require.NoError(t, recvErr)
					require.Equal(t, "partial", chunk.Content)
					_, recvErr = stream.Recv()
					require.ErrorIs(t, recvErr, interruptErr)
					var retryErr *WillRetryError
					require.False(t, errors.As(recvErr, &retryErr), "the event must not announce a retry that will not happen")
					_, recvErr = stream.Recv()
					require.ErrorIs(t, recvErr, io.EOF)
				}
				require.Zero(t, decisions, "interrupts bypass both runtime and event retry policies")

				// The error still refers to the source graph's checkpoint. The adapter,
				// rather than the model retry layer, owns that graph's explicit resume.
				resumed, err := runnable.Invoke(context.Background(), "", compose.WithCheckPointID("source-pause"))
				require.NoError(t, err)
				require.Equal(t, "resumed", resumed)
			})
		}
	}
}
