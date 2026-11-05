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

package adk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// Zero backoff must still observe caller cancellation. contextAwareSleep
// used to return nil for delay <= 0 without checking ctx.Done(), so a
// cancelled run kept dispatching model calls until MaxRetries was
// exhausted (issue #1448).
func TestRetryZeroBackoffRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			callCount++
			// Cancel the caller before the next retry.
			cancel()
			return nil, errRetryAble
		}).AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ZeroBackoffCancelAgent",
		Description: "Test zero-backoff cancellation",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  5,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
			BackoffFunc: func(_ context.Context, _ int) time.Duration {
				return 0 // zero backoff: must still observe cancellation
			},
		},
	})
	require.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	require.True(t, ok)
	require.NotNil(t, event)
	require.NotNil(t, event.Err)

	// The error must be the cancellation, not a retry-exhausted wrapper.
	assert.ErrorIs(t, event.Err, context.Canceled)

	// Only one model call should have been dispatched.
	assert.Equal(t, 1, callCount, "zero-backoff retry must not dispatch after cancellation")

	_, ok = iterator.Next()
	assert.False(t, ok)
}
