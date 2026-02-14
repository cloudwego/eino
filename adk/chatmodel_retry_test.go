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
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

var errRetryAble = errors.New("retry-able error")
var errNonRetryAble = errors.New("non-retry-able error")

func TestChatModelAgentRetry_NoTools_DirectError_Generate(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 3 {
				return nil, errRetryAble
			}
			return schema.AssistantMessage("Success after retry", nil), nil
		}).Times(3)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.Equal(t, "Success after retry", event.Output.MessageOutput.Message.Content)

	_, ok = iterator.Next()
	assert.False(t, ok)
	assert.Equal(t, int32(3), atomic.LoadInt32(&callCount))
}

func TestChatModelAgentRetry_NoTools_DirectError_Stream(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 2 {
				return nil, errRetryAble
			}
			return schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("Success", nil),
			}), nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.True(t, event.Output.MessageOutput.IsStreaming)

	_, ok = iterator.Next()
	assert.False(t, ok)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
}

type streamErrorModel struct {
	callCount   int32
	failAtChunk int
	maxFailures int
	tools       []*schema.ToolInfo
	returnTool  bool
}

func (m *streamErrorModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("Generated", nil), nil
}

func (m *streamErrorModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	count := atomic.AddInt32(&m.callCount, 1)

	sr, sw := schema.Pipe[*schema.Message](10)
	go func() {
		defer sw.Close()
		for i := 0; i < 5; i++ {
			if i == m.failAtChunk && int(count) <= m.maxFailures {
				sw.Send(nil, errRetryAble)
				return
			}
			if m.returnTool && i == 0 {
				sw.Send(schema.AssistantMessage("", []schema.ToolCall{{
					ID:       "call-1",
					Function: schema.FunctionCall{Name: "test_tool", Arguments: "{}"},
				}}), nil)
			} else {
				sw.Send(schema.AssistantMessage("chunk", nil), nil)
			}
		}
	}()
	return sr, nil
}

func (m *streamErrorModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.tools = tools
	return m, nil
}

func TestChatModelAgentRetry_StreamError(t *testing.T) {
	t.Run("WithTools", func(t *testing.T) {
		ctx := context.Background()

		m := &streamErrorModel{
			failAtChunk: 2,
			maxFailures: 2,
			returnTool:  false,
		}

		config := &ChatModelAgentConfig{
			Name:        "RetryTestAgent",
			Description: "Test agent for retry functionality",
			Instruction: "You are a helpful assistant.",
			Model:       m,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries:  3,
				IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
			},
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{&fakeToolForTest{tarCount: 0}},
				},
			},
		}

		agent, err := NewChatModelAgent(ctx, config)
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		var events []*AgentEvent
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}

		assert.Equal(t, 3, len(events))

		var streamErrEventCount int
		var errs []error
		for i, event := range events {
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
				sr := event.Output.MessageOutput.MessageStream
				for {
					msg, err := sr.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						streamErrEventCount++
						errs = append(errs, err)
						t.Logf("event %d: err: %v", i, err)
						break
					}
					t.Logf("event %d: %v", i, msg.Content)
				}
			}
		}

		assert.Equal(t, 2, streamErrEventCount)
		assert.Equal(t, 2, len(errs))
		var willRetryErr *WillRetryError
		assert.True(t, errors.As(errs[0], &willRetryErr))
		assert.True(t, errors.As(errs[1], &willRetryErr))
		assert.Equal(t, int32(3), atomic.LoadInt32(&m.callCount))
	})

	t.Run("NoTools", func(t *testing.T) {
		ctx := context.Background()

		m := &streamErrorModel{
			failAtChunk: 2,
			maxFailures: 2,
			returnTool:  false,
		}

		config := &ChatModelAgentConfig{
			Name:        "RetryTestAgent",
			Description: "Test agent for retry functionality",
			Instruction: "You are a helpful assistant.",
			Model:       m,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries:  3,
				IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
			},
		}

		agent, err := NewChatModelAgent(ctx, config)
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		var events []*AgentEvent
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}

		assert.Equal(t, 3, len(events))

		var streamErrEventCount int
		var errs []error
		for i, event := range events {
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
				sr := event.Output.MessageOutput.MessageStream
				for {
					msg, err := sr.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						streamErrEventCount++
						errs = append(errs, err)
						t.Logf("event %d: err: %v", i, err)
						break
					}
					t.Logf("event %d: %v", i, msg.Content)
				}
			}
		}

		assert.Equal(t, 2, streamErrEventCount)
		assert.Equal(t, 2, len(errs))
		var willRetryErr *WillRetryError
		assert.True(t, errors.As(errs[0], &willRetryErr))
		assert.True(t, errors.As(errs[1], &willRetryErr))
		assert.Equal(t, int32(3), atomic.LoadInt32(&m.callCount))
	})
}

func TestChatModelAgentRetry_WithTools_DirectError_Generate(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 2 {
				return nil, errRetryAble
			}
			return schema.AssistantMessage("Success after retry", nil), nil
		}).Times(2)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	fakeTool := &fakeToolForTest{tarCount: 0}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
		},
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.Equal(t, "Success after retry", event.Output.MessageOutput.Message.Content)

	_, ok = iterator.Next()
	assert.False(t, ok)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
}

func TestChatModelAgentRetry_NonRetryableError(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errNonRetryAble).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.NotNil(t, event.Err)
	assert.True(t, errors.Is(event.Err, errNonRetryAble))

	_, ok = iterator.Next()
	assert.False(t, ok)
}

type inputCapturingModel struct {
	capturedInputs [][]Message
}

func (m *inputCapturingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.capturedInputs = append(m.capturedInputs, input)
	return schema.AssistantMessage("Response from capturing model", nil), nil
}

func (m *inputCapturingModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.capturedInputs = append(m.capturedInputs, input)
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("Response from capturing model", nil),
	}), nil
}

func (m *inputCapturingModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestChatModelAgentRetry_MaxRetriesExhausted(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errRetryAble).Times(4)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.NotNil(t, event.Err)
	assert.True(t, errors.Is(event.Err, ErrExceedMaxRetries))
	var retryErr *RetryExhaustedError
	assert.True(t, errors.As(event.Err, &retryErr))
	assert.True(t, errors.Is(retryErr.LastErr, errRetryAble))

	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestChatModelAgentRetry_BackoffFunction(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var backoffCalls []int
	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 3 {
				return nil, errRetryAble
			}
			return schema.AssistantMessage("Success", nil), nil
		}).Times(3)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
			BackoffFunc: func(ctx context.Context, attempt int) time.Duration {
				backoffCalls = append(backoffCalls, attempt)
				return time.Millisecond
			},
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)

	_, ok = iterator.Next()
	assert.False(t, ok)

	assert.Equal(t, []int{1, 2}, backoffCalls)
}

func TestChatModelAgentRetry_NoRetryConfig(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errRetryAble).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent without retry config",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.NotNil(t, event.Err)
	assert.True(t, errors.Is(event.Err, errRetryAble))

	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestChatModelAgentRetry_WithTools_NonRetryAbleStreamError(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errNonRetryAble).Times(1)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	fakeTool := &fakeToolForTest{tarCount: 0}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
		},
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.NotNil(t, event.Err)
	assert.True(t, errors.Is(event.Err, errNonRetryAble))

	_, ok = iterator.Next()
	assert.False(t, ok)
}

type nonRetryAbleStreamErrorModel struct {
	tools []*schema.ToolInfo
}

func (m *nonRetryAbleStreamErrorModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("Generated", nil), nil
}

func (m *nonRetryAbleStreamErrorModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](10)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("chunk1", nil), nil)
		sw.Send(nil, errNonRetryAble)
	}()
	return sr, nil
}

func (m *nonRetryAbleStreamErrorModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.tools = tools
	return m, nil
}

func TestChatModelAgentRetry_NoTools_NonRetryAbleStreamError(t *testing.T) {
	ctx := context.Background()

	m := &nonRetryAbleStreamErrorModel{}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       m,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	var events []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.Equal(t, 2, len(events))

	event0 := events[0]
	assert.NotNil(t, event0.Output)
	assert.NotNil(t, event0.Output.MessageOutput)
	assert.True(t, event0.Output.MessageOutput.IsStreaming)
	sr := event0.Output.MessageOutput.MessageStream
	var streamErr error
	for {
		_, err := sr.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			streamErr = err
			break
		}
	}
	assert.NotNil(t, streamErr)
	assert.True(t, errors.Is(streamErr, errNonRetryAble), "Stream error should be the original error")

	event1 := events[1]
	assert.NotNil(t, event1.Err)
	assert.True(t, errors.Is(event1.Err, errNonRetryAble))
}

func TestDefaultBackoff(t *testing.T) {
	ctx := context.Background()

	d1 := defaultBackoff(ctx, 1)
	d2 := defaultBackoff(ctx, 2)
	d3 := defaultBackoff(ctx, 3)

	t.Logf("Backoff delays: d1=%v, d2=%v, d3=%v", d1, d2, d3)

	assert.True(t, d1 >= 100*time.Millisecond && d1 < 150*time.Millisecond,
		"First retry should be ~100ms + jitter (0-50ms), got %v", d1)
	assert.True(t, d2 >= 200*time.Millisecond && d2 < 300*time.Millisecond,
		"Second retry should be ~200ms + jitter (0-100ms), got %v", d2)
	assert.True(t, d3 >= 400*time.Millisecond && d3 < 600*time.Millisecond,
		"Third retry should be ~400ms + jitter (0-200ms), got %v", d3)

	d10 := defaultBackoff(ctx, 10)
	t.Logf("Backoff delay for attempt 10: %v", d10)
	assert.True(t, d10 >= 10*time.Second && d10 <= 15*time.Second,
		"Delay should be capped at 10s + jitter (0-5s), got %v", d10)

	d100 := defaultBackoff(ctx, 100)
	t.Logf("Backoff delay for attempt 100: %v", d100)
	assert.True(t, d100 >= 10*time.Second && d100 <= 15*time.Second,
		"Delay should still be capped at 10s + jitter for very high attempts, got %v", d100)
}

func TestRetryExhaustedError_ErrorString(t *testing.T) {
	errWithLast := &RetryExhaustedError{
		LastErr:      errors.New("connection timeout"),
		TotalRetries: 3,
	}
	assert.Contains(t, errWithLast.Error(), "exceeds max retries")
	assert.Contains(t, errWithLast.Error(), "connection timeout")

	errWithoutLast := &RetryExhaustedError{
		LastErr:      nil,
		TotalRetries: 3,
	}
	assert.Equal(t, "exceeds max retries", errWithoutLast.Error())
}

func TestWillRetryError_ErrorString(t *testing.T) {
	willRetry := &WillRetryError{ErrStr: "transient error", RetryAttempt: 1}
	assert.Equal(t, "transient error", willRetry.Error())
}

type customError struct {
	code int
	msg  string
}

func (e *customError) Error() string {
	return e.msg
}

func TestWillRetryError_Unwrap(t *testing.T) {
	originalErr := &customError{code: 500, msg: "internal error"}
	willRetry := &WillRetryError{ErrStr: originalErr.Error(), RetryAttempt: 1, err: originalErr}

	assert.True(t, errors.Is(willRetry, originalErr))

	var targetErr *customError
	assert.True(t, errors.As(willRetry, &targetErr))
	assert.Equal(t, 500, targetErr.code)
	assert.Equal(t, "internal error", targetErr.msg)
}

func TestChatModelAgentRetry_DefaultIsRetryAble(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 2 {
				return nil, errors.New("any error should be retried")
			}
			return schema.AssistantMessage("Success", nil), nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent with default IsRetryAble",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 3,
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.Nil(t, event.Err)
	assert.Equal(t, "Success", event.Output.MessageOutput.Message.Content)

	_, ok = iterator.Next()
	assert.False(t, ok)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
}

func TestSequentialWorkflow_RetryAbleStreamError_SuccessfulRetry(t *testing.T) {
	ctx := context.Background()

	retryModel := &streamErrorModel{
		failAtChunk: 2,
		maxFailures: 2,
	}

	agentA, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "AgentA",
		Description: "Agent A with retry that emits stream errors then succeeds",
		Instruction: "You are agent A.",
		Model:       retryModel,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	capturingModel := &inputCapturingModel{}
	agentB, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "AgentB",
		Description: "Agent B that captures input",
		Instruction: "You are agent B.",
		Model:       capturingModel,
	})
	assert.NoError(t, err)

	sequentialAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "SequentialAgent",
		Description: "Sequential agent A->B",
		SubAgents:   []Agent{agentA, agentB},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	ctx, _ = initRunCtx(ctx, sequentialAgent.Name(ctx), input)
	iterator := sequentialAgent.Run(ctx, input)

	var events []*AgentEvent
	var willRetryErrCount int
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
			sr := event.Output.MessageOutput.MessageStream
			for {
				_, err := sr.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					var retryErr *WillRetryError
					if errors.As(err, &retryErr) {
						willRetryErrCount++
					}
					break
				}
			}
		}
	}

	assert.Equal(t, 2, willRetryErrCount, "End-user should receive 2 WillRetryError events")
	assert.Equal(t, 1, len(capturingModel.capturedInputs), "Agent B should be called exactly once")

	successorInput := capturingModel.capturedInputs[0]
	var hasSuccessfulMessage bool
	for _, msg := range successorInput {
		if strings.Contains(msg.Content, "chunkchunkchunkchunkchunk") {
			hasSuccessfulMessage = true
			break
		}
	}
	assert.True(t, hasSuccessfulMessage, "Agent B should receive the successful message from Agent A")

	for _, msg := range successorInput {
		assert.NotContains(t, msg.Content, "retry-able error", "Agent B should not receive failed stream messages")
	}
}

type streamErrorModelNoRetry struct {
	callCount int32
}

func (m *streamErrorModelNoRetry) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("Generated", nil), nil
}

func (m *streamErrorModelNoRetry) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	atomic.AddInt32(&m.callCount, 1)
	sr, sw := schema.Pipe[*schema.Message](10)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("chunk1", nil), nil)
		sw.Send(schema.AssistantMessage("chunk2", nil), nil)
		sw.Send(nil, errRetryAble)
	}()
	return sr, nil
}

func (m *streamErrorModelNoRetry) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestSequentialWorkflow_NonRetryAbleStreamError_StopsFlow(t *testing.T) {
	ctx := context.Background()

	nonRetryModel := &nonRetryAbleStreamErrorModel{}

	agentA, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "AgentA",
		Description: "Agent A that emits non-retryable stream error",
		Instruction: "You are agent A.",
		Model:       nonRetryModel,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	capturingModel := &inputCapturingModel{}
	agentB, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "AgentB",
		Description: "Agent B that captures input",
		Instruction: "You are agent B.",
		Model:       capturingModel,
	})
	assert.NoError(t, err)

	sequentialAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "SequentialAgent",
		Description: "Sequential agent A->B",
		SubAgents:   []Agent{agentA, agentB},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	ctx, _ = initRunCtx(ctx, sequentialAgent.Name(ctx), input)
	iterator := sequentialAgent.Run(ctx, input)

	var events []*AgentEvent
	var streamErrFound bool
	var finalErrEvent *AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
		if event.Err != nil {
			finalErrEvent = event
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
			sr := event.Output.MessageOutput.MessageStream
			for {
				_, err := sr.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					streamErrFound = true
					assert.True(t, errors.Is(err, errNonRetryAble), "Stream error should be the original error")
					break
				}
			}
		}
	}

	assert.True(t, streamErrFound, "End-user should receive stream error")
	assert.NotNil(t, finalErrEvent, "Should receive a final error event")
	assert.True(t, errors.Is(finalErrEvent.Err, errNonRetryAble), "Final error should be the non-retryable error")
	assert.Equal(t, 0, len(capturingModel.capturedInputs), "Agent B should NOT be called due to error")
}

func TestSequentialWorkflow_NoRetryConfig_StreamError_StopsFlow(t *testing.T) {
	ctx := context.Background()

	noRetryModel := &streamErrorModelNoRetry{}

	agentA, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "AgentA",
		Description: "Agent A without retry config that emits stream error",
		Instruction: "You are agent A.",
		Model:       noRetryModel,
	})
	assert.NoError(t, err)

	capturingModel := &inputCapturingModel{}
	agentB, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "AgentB",
		Description: "Agent B that captures input",
		Instruction: "You are agent B.",
		Model:       capturingModel,
	})
	assert.NoError(t, err)

	sequentialAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "SequentialAgent",
		Description: "Sequential agent A->B",
		SubAgents:   []Agent{agentA, agentB},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	ctx, _ = initRunCtx(ctx, sequentialAgent.Name(ctx), input)
	iterator := sequentialAgent.Run(ctx, input)

	var events []*AgentEvent
	var streamErrFound bool
	var finalErrEvent *AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
		if event.Err != nil {
			finalErrEvent = event
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
			sr := event.Output.MessageOutput.MessageStream
			for {
				_, err := sr.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					streamErrFound = true
					assert.True(t, errors.Is(err, errRetryAble), "Stream error should be the original error")
					break
				}
			}
		}
	}

	assert.True(t, streamErrFound, "End-user should receive stream error")
	assert.NotNil(t, finalErrEvent, "Should receive a final error event")
	assert.True(t, errors.Is(finalErrEvent.Err, errRetryAble), "Final error should be the original error")
	assert.Equal(t, 0, len(capturingModel.capturedInputs), "Agent B should NOT be called due to error")
	assert.Equal(t, int32(1), atomic.LoadInt32(&noRetryModel.callCount), "Model should only be called once (no retry)")
}
