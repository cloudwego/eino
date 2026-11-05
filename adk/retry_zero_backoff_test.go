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
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRetryCancellationWithoutBackoff(t *testing.T) {
	for _, delay := range []time.Duration{0, -time.Nanosecond, time.Hour} {
		for _, policy := range []string{"default", "should_retry"} {
			for _, call := range []string{"generate", "stream_start", "stream_read"} {
				t.Run(delay.String()+"/"+policy+"/"+call, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					cm := mockModel.NewMockToolCallingChatModel(gomock.NewController(t))
					calls := 0
					failure := errors.New("transient model failure")
					if call == "generate" {
						cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
							calls++
							return nil, failure
						}).AnyTimes()
					} else {
						cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
							calls++
							if call == "stream_start" {
								return nil, failure
							}
							sr, sw := schema.Pipe[*schema.Message](1)
							sw.Send(nil, failure)
							sw.Close()
							return sr, nil
						}).AnyTimes()
					}
					config := &ModelRetryConfig{MaxRetries: 2, BackoffFunc: func(context.Context, int) time.Duration { cancel(); return delay }}
					if policy == "should_retry" {
						config.ShouldRetry = func(context.Context, *RetryContext) *RetryDecision { return &RetryDecision{Retry: true} }
					}
					wrapper := newTypedRetryModelWrapper[*schema.Message](cm, config)
					var err error
					if call == "generate" {
						_, err = wrapper.Generate(ctx, nil)
					} else {
						_, err = wrapper.Stream(ctx, nil)
					}
					require.Equal(t, 1, calls, "cancellation must prevent further model calls even with no delay")
					require.ErrorIs(t, err, context.Canceled)
				})
			}
		}
	}
}

func TestRetryWithoutBackoffAcceptsActiveContext(t *testing.T) {
	for _, delay := range []time.Duration{0, -time.Nanosecond} {
		for _, policy := range []string{"default", "should_retry"} {
			t.Run(delay.String()+"/"+policy, func(t *testing.T) {
				cm := mockModel.NewMockToolCallingChatModel(gomock.NewController(t))
				output := schema.AssistantMessage("recovered", nil)
				gomock.InOrder(
					cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("transient failure")),
					cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(output, nil),
				)
				config := &ModelRetryConfig{MaxRetries: 1, BackoffFunc: func(context.Context, int) time.Duration { return delay }}
				if policy == "should_retry" {
					config.ShouldRetry = func(_ context.Context, r *RetryContext) *RetryDecision { return &RetryDecision{Retry: r.Err != nil} }
				}
				result, err := newTypedRetryModelWrapper[*schema.Message](cm, config).Generate(context.Background(), nil)
				require.NoError(t, err)
				require.Same(t, output, result)
			})
		}
	}
}
