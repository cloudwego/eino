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
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

var errRetryAble = errors.New("retry-able error")
var errNonRetryAble = errors.New("non-retry-able error")

var instantBackoff = func(_ context.Context, _ int) time.Duration { return time.Millisecond }

type agentEvent struct {
	Err           error
	Output        *AgentOutput
	StreamContent string
}

func drainAgentEvents(t *testing.T, iterator *AsyncIterator[*AgentEvent]) []agentEvent {
	t.Helper()
	var events []agentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, agentEvent{Err: event.Err, Output: event.Output})
	}
	return events
}

func drainStreamingAgentEvents(t *testing.T, iterator *AsyncIterator[*AgentEvent]) (events []agentEvent, streamTermErrs []error) {
	t.Helper()
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		ae := agentEvent{Err: event.Err, Output: event.Output}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				var chunks []string
				for {
					msg, recvErr := mo.MessageStream.Recv()
					if recvErr != nil {
						streamTermErrs = append(streamTermErrs, recvErr)
						break
					}
					if msg != nil {
						chunks = append(chunks, msg.Content)
					}
				}
				ae.StreamContent = strings.Join(chunks, "")
			}
		}
		events = append(events, ae)
	}
	return events, streamTermErrs
}

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

// failThenToolCallStreamModel is a ChatModel that:
//   - First Stream() call: yields a partial chunk then fails with a retryable error mid-stream.
//   - Second Stream() call (retry): yields a tool-call message (success).
//   - Third Generate() call (after tool result): yields a final assistant message.
//
// This exercises the path where the eventSenderModel copies the first stream,
// wraps its error as WillRetryError, and sends it as an event to the session.
// The retryModelWrapper then retries, gets a clean stream with a tool call,
// the tool interrupts, and checkpoint save needs to gob-encode the session
// (which still contains the unconsumed WillRetryError event stream).
type failThenToolCallStreamModel struct {
	streamCallCount int32
	genCallCount    int32
}

func (m *failThenToolCallStreamModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	atomic.AddInt32(&m.genCallCount, 1)
	return schema.AssistantMessage("final answer", nil), nil
}

func (m *failThenToolCallStreamModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	count := atomic.AddInt32(&m.streamCallCount, 1)

	sr, sw := schema.Pipe[*schema.Message](10)
	go func() {
		defer sw.Close()
		if count == 1 {
			// First call: yield a partial chunk then fail.
			sw.Send(schema.AssistantMessage("partial", nil), nil)
			sw.Send(nil, errRetryAble)
			return
		}
		// Second call (retry): yield a tool-call message.
		sw.Send(schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-1",
			Function: schema.FunctionCall{
				Name:      "interrupt_tool",
				Arguments: `{}`,
			},
		}}), nil)
	}()
	return sr, nil
}

func (m *failThenToolCallStreamModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

// interruptToolForRetryTest is a tool that always interrupts.
type interruptToolForRetryTest struct{}

func (t *interruptToolForRetryTest) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "interrupt_tool",
		Desc: "tool that interrupts",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: "string"},
		}),
	}, nil
}

func (t *interruptToolForRetryTest) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	return "", tool.Interrupt(ctx, "interrupted by tool")
}

// TestCheckpointSave_WillRetryError_StreamNotConsumed verifies that checkpoint
// saving succeeds when the session contains an event with an unconsumed stream
// that ends with WillRetryError.
//
// Scenario:
//  1. ChatModelAgent with retry (MaxRetries=1) and a tool that always interrupts
//  2. Model.Stream() #1 yields "partial" then errRetryAble mid-stream
//     → eventSenderModel copies the stream, wraps the error as WillRetryError,
//     sends the event to the session (stream NOT consumed by anyone yet)
//     → retryModelWrapper detects error on its copy, retries
//  3. Model.Stream() #2 succeeds with a tool-call message
//  4. Tool executes → interrupts
//  5. Runner.handleIter sees the interrupt → saveCheckPoint → gob encodes runSession
//  6. The session has the WillRetryError event with an unconsumed stream
//     → agentEventWrapper.GobEncode proactively consumes the stream via
//     getMessageFromWrappedEvent, so MessageVariant.GobEncode sees an error-free
//     array and succeeds
func TestCheckpointSave_WillRetryError_StreamNotConsumed(t *testing.T) {
	ctx := context.Background()

	mdl := &failThenToolCallStreamModel{}
	itool := &interruptToolForRetryTest{}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Agent for checkpoint stream error test",
		Instruction: "You are a test agent.",
		Model:       mdl,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{itool},
			},
		},
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			IsRetryAble: func(_ context.Context, err error) bool {
				return errors.Is(err, errRetryAble)
			},
			BackoffFunc: instantBackoff,
		},
	})
	assert.NoError(t, err)

	store := newMyStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	iter := runner.Run(ctx,
		[]Message{schema.UserMessage("hello")},
		WithCheckPointID("ckpt-1"),
	)

	var events []*AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)

		if event.Err != nil {
			t.Logf("event error: %v", event.Err)
		}
	}

	// Verify the checkpoint was saved successfully.
	_, exists, _ := store.Get(ctx, "ckpt-1")
	assert.True(t, exists, "checkpoint should be saved successfully; "+
		"if this fails, the WillRetryError stream in the session caused gob encoding to fail")

	// Sanity: the model should have been called twice for Stream (fail + retry).
	assert.Equal(t, int32(2), atomic.LoadInt32(&mdl.streamCallCount),
		"model should be called twice: first fail, then retry success")
}

func TestChatModelAgentRetry_ShouldRetry_RejectMessage_Stream(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			count := atomic.AddInt32(&callCount, 1)
			r, w := schema.Pipe[*schema.Message](1)
			go func() {
				if count < 2 {
					_ = w.Send(schema.AssistantMessage("bad stream content", nil), nil)
				} else {
					_ = w.Send(schema.AssistantMessage("good stream content", nil), nil)
				}
				w.Close()
			}()
			return r, nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ShouldRetryStreamTestAgent",
		Description: "Test ShouldRetry message rejection in stream mode",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 3,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.Err != nil {
					return &RetryDecision{Retry: true}
				}
				if retryCtx.OutputMessage != nil && strings.Contains(retryCtx.OutputMessage.Content, "bad") {
					return &RetryDecision{Retry: true}
				}
				return &RetryDecision{Retry: false}
			},
			BackoffFunc: instantBackoff,
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	events, _ := drainStreamingAgentEvents(t, iterator)
	var foundGoodContent bool
	for _, e := range events {
		if e.StreamContent == "good stream content" {
			foundGoodContent = true
		}
	}
	require.True(t, foundGoodContent, "should have received good stream content")
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
}

func TestShouldRetry_Generate(t *testing.T) {
	t.Run("RetryContext_Fields", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				count := atomic.AddInt32(&callCount, 1)
				if count < 2 {
					return schema.AssistantMessage("bad", nil), nil
				}
				return schema.AssistantMessage("good", nil), nil
			}).Times(2)

		var capturedContexts []*RetryContext
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "RetryContextFieldsAgent",
			Description: "Test that RetryContext fields are correctly populated",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					capturedContexts = append(capturedContexts, retryCtx)
					if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad" {
						return &RetryDecision{Retry: true}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		}
		iterator := agent.Run(ctx, input)

		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			_ = event
		}

		assert.Len(t, capturedContexts, 2, "ShouldRetry should be called twice")

		assert.Equal(t, 1, capturedContexts[0].RetryAttempt)
		assert.Len(t, capturedContexts[0].InputMessages, 2)
		assert.True(t, len(capturedContexts[0].Options) > 0, "should have default options")
		assert.Equal(t, "bad", capturedContexts[0].OutputMessage.Content)
		assert.Nil(t, capturedContexts[0].Err)

		assert.Equal(t, 2, capturedContexts[1].RetryAttempt)
		assert.Equal(t, "good", capturedContexts[1].OutputMessage.Content)
		assert.Nil(t, capturedContexts[1].Err)
	})

	t.Run("RewriteError_OnMessage", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("unrecoverable bad message", nil), nil).Times(1)

		fatalErr := errors.New("fatal: unrecoverable model output")

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "RewriteErrorTestAgent",
			Description: "Test ShouldRetry RewriteError on message",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.OutputMessage != nil && strings.Contains(retryCtx.OutputMessage.Content, "unrecoverable") {
						return &RetryDecision{
							Retry:  false,
							RewriteError: fatalErr,
						}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		}
		iterator := agent.Run(ctx, input)

		events := drainAgentEvents(t, iterator)
		require.NotEmpty(t, events)
		foundErr := false
		for _, e := range events {
			if e.Err != nil && errors.Is(e.Err, fatalErr) {
				foundErr = true
			}
		}
		require.True(t, foundErr, "should have received the fatal rewrite error")
	})

	t.Run("RewriteError_OnError", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		origErr := errors.New("original transient error")
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, origErr).Times(1)

		wrappedErr := errors.New("wrapped: original transient error with more context")

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "RewriteErrorOnErrorTestAgent",
			Description: "Test ShouldRetry RewriteError replacing original error",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.Err != nil {
						return &RetryDecision{
							Retry:  false,
							RewriteError: wrappedErr,
						}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		}
		iterator := agent.Run(ctx, input)

		events := drainAgentEvents(t, iterator)
		require.NotEmpty(t, events)
		foundErr := false
		for _, e := range events {
			if e.Err != nil && errors.Is(e.Err, wrappedErr) {
				foundErr = true
			}
		}
		require.True(t, foundErr, "should have received the wrapped rewrite error")
	})

	t.Run("ModifiedOptions", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		var capturedOpts [][]model.Option
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				count := atomic.AddInt32(&callCount, 1)
				capturedOpts = append(capturedOpts, opts)
				if count < 2 {
					return nil, errRetryAble
				}
				return schema.AssistantMessage("success", nil), nil
			}).Times(2)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "ModifiedOptionsTestAgent",
			Description: "Test ShouldRetry ModifiedOptions",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.Err != nil {
						return &RetryDecision{
							Retry:     true,
							ModifiedOptions: []model.Option{model.WithMaxTokens(8192)},
						}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
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
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
		assert.Equal(t, 2, len(capturedOpts))
		assert.Equal(t, len(capturedOpts[0])+1, len(capturedOpts[1]))
	})

	t.Run("ModifiedInputMessages_NoPersist", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		var capturedInputs [][]*schema.Message
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				count := atomic.AddInt32(&callCount, 1)
				inputCopy := make([]*schema.Message, len(input))
				copy(inputCopy, input)
				capturedInputs = append(capturedInputs, inputCopy)
				if count < 2 {
					return nil, errRetryAble
				}
				return schema.AssistantMessage("success", nil), nil
			}).Times(2)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "ModifiedInputNoPersistAgent",
			Description: "Test ShouldRetry ModifiedInputMessages without persistence",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.Err != nil {
						return &RetryDecision{
							Retry: true,
							ModifiedInputMessages: []*schema.Message{
								schema.SystemMessage("compressed instruction"),
								schema.UserMessage("Hello"),
							},
							PersistModifiedInputMessages: false,
						}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
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
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
		assert.Equal(t, 2, len(capturedInputs))
		assert.Equal(t, "compressed instruction", capturedInputs[1][0].Content, "second call should use modified input")
	})

	t.Run("Backoff", func(t *testing.T) {
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
				return schema.AssistantMessage("success", nil), nil
			}).Times(2)

		customBackoff := 50 * time.Millisecond

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "BackoffTestAgent",
			Description: "Test ShouldRetry custom Backoff in decision",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.Err != nil {
						return &RetryDecision{
							Retry: true,
							Backoff:     customBackoff,
						}
					}
					return &RetryDecision{Retry: false}
				},
			},
		})
		assert.NoError(t, err)

		start := time.Now()
		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		}
		iterator := agent.Run(ctx, input)

		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event)
		assert.Nil(t, event.Err)
		elapsed := time.Since(start)
		assert.True(t, elapsed >= customBackoff && elapsed < customBackoff+200*time.Millisecond, "expected backoff ~%v, got %v", customBackoff, elapsed)
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
	})

	t.Run("SuppressFlag_Rejected_NoEvent", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				count := atomic.AddInt32(&callCount, 1)
				if count == 1 {
					return schema.AssistantMessage("bad", nil), nil
				}
				return schema.AssistantMessage("good", nil), nil
			}).Times(2)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "SuppressRejected",
			Description: "Test suppress flag rejects first then accepts",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 1,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad" {
						return &RetryDecision{Retry: true}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		}
		iterator := agent.Run(ctx, input)

		var msgEvents []*AgentEvent
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				msgEvents = append(msgEvents, event)
			}
		}
		assert.Equal(t, 1, len(msgEvents), "should have exactly 1 message event (suppressed rejected)")
		assert.Equal(t, "good", msgEvents[0].Output.MessageOutput.Message.Content)
	})

	t.Run("SuppressFlag_AllRejected_NoEvents", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("always bad", nil), nil).Times(3)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "SuppressAllRejected",
			Description: "Test suppress flag all rejected no events",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					return &RetryDecision{Retry: true}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		}
		iterator := agent.Run(ctx, input)

		events := drainAgentEvents(t, iterator)
		var msgEventCount int
		var foundExhaustedErr bool
		for _, e := range events {
			if e.Output != nil && e.Output.MessageOutput != nil {
				msgEventCount++
			}
			if e.Err != nil && errors.Is(e.Err, ErrExceedMaxRetries) {
				foundExhaustedErr = true
			}
		}
		assert.Equal(t, 0, msgEventCount, "no message events should be emitted when all are rejected")
		require.True(t, foundExhaustedErr, "final event should have RetryExhaustedError")
	})

	t.Run("SuppressFlag_Accepted_FirstAttempt", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("perfect", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "SuppressAcceptedFirst",
			Description: "Test suppress flag accepted first attempt",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		}
		iterator := agent.Run(ctx, input)

		var msgEvents []*AgentEvent
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				msgEvents = append(msgEvents, event)
			}
		}
		assert.Equal(t, 1, len(msgEvents), "should have exactly 1 event")
		assert.Equal(t, "perfect", msgEvents[0].Output.MessageOutput.Message.Content)
	})

	t.Run("ContextCanceled_DuringSleep", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				atomic.AddInt32(&callCount, 1)
				return nil, errors.New("transient")
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "ContextCancelDuringSleep",
			Description: "Test context cancellation during backoff sleep",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 5,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					return &RetryDecision{Retry: true}
				},
				BackoffFunc: func(_ context.Context, _ int) time.Duration { return 10 * time.Second },
			},
		})
		require.NoError(t, err)

		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		iterator := agent.Run(ctx, &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		})
		events := drainAgentEvents(t, iterator)
		elapsed := time.Since(start)

		require.True(t, elapsed < 2*time.Second, "should not block for full backoff; elapsed: %v", elapsed)
		assert.Equal(t, int32(1), atomic.LoadInt32(&callCount))

		var foundCtxErr bool
		for _, e := range events {
			if e.Err != nil && errors.Is(e.Err, context.Canceled) {
				foundCtxErr = true
			}
		}
		require.True(t, foundCtxErr, "should have received context.Canceled error")
	})
}

func TestShouldRetry_Stream(t *testing.T) {
	t.Run("ErrorRetry", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		streamErr := errors.New("stream unavailable")
		var callCount int32
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				count := atomic.AddInt32(&callCount, 1)
				if count < 2 {
					return nil, streamErr
				}
				r, w := schema.Pipe[*schema.Message](1)
				go func() {
					_ = w.Send(schema.AssistantMessage("recovered stream", nil), nil)
					w.Close()
				}()
				return r, nil
			}).Times(2)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "StreamErrorRetryAgent",
			Description: "Test ShouldRetry when Stream returns error (nil stream)",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.Err != nil {
						return &RetryDecision{Retry: true}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		events, _ := drainStreamingAgentEvents(t, iterator)
		var foundContent bool
		for _, e := range events {
			if e.StreamContent == "recovered stream" {
				foundContent = true
			}
		}
		require.True(t, foundContent, "should have received recovered stream content after error retry")
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
	})

	t.Run("ErrorRewrite", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		streamErr := errors.New("model overloaded")
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, streamErr).Times(1)

		fatalErr := errors.New("fatal: model overloaded, aborting")

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "StreamErrorRewriteAgent",
			Description: "Test ShouldRetry RewriteError when Stream returns error",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.Err != nil && strings.Contains(retryCtx.Err.Error(), "overloaded") {
						return &RetryDecision{
							Retry:  false,
							RewriteError: fatalErr,
						}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		events := drainAgentEvents(t, iterator)
		require.NotEmpty(t, events)
		foundErr := false
		for _, e := range events {
			if e.Err != nil && errors.Is(e.Err, fatalErr) {
				foundErr = true
			}
		}
		require.True(t, foundErr, "should have received the fatal rewrite error from stream")
	})

	t.Run("RewriteError_OnMessage", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				r, w := schema.Pipe[*schema.Message](1)
				go func() {
					_ = w.Send(schema.AssistantMessage("hallucinated garbage output", nil), nil)
					w.Close()
				}()
				return r, nil
			}).Times(1)

		fatalErr := errors.New("fatal: hallucinated output detected")

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "StreamRewriteOnMessageAgent",
			Description: "Test ShouldRetry RewriteError on successful stream with bad content",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.OutputMessage != nil && strings.Contains(retryCtx.OutputMessage.Content, "hallucinated") {
						return &RetryDecision{
							Retry:  false,
							RewriteError: fatalErr,
						}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		events := drainAgentEvents(t, iterator)
		require.NotEmpty(t, events)
		foundErr := false
		for _, e := range events {
			if e.Err != nil && errors.Is(e.Err, fatalErr) {
				foundErr = true
			}
		}
		require.True(t, foundErr, "should have received fatal rewrite error from stream message inspection")
	})

	t.Run("PartialStreamError", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		partialErr := errors.New("connection reset mid-stream")
		var callCount int32
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				count := atomic.AddInt32(&callCount, 1)
				r, w := schema.Pipe[*schema.Message](1)
				go func() {
					_ = w.Send(schema.AssistantMessage("partial chunk", nil), nil)
					if count < 2 {
						w.Send(nil, partialErr)
					} else {
						_ = w.Send(schema.AssistantMessage(" complete", nil), nil)
						w.Close()
					}
				}()
				return r, nil
			}).Times(2)

		var capturedContexts []*RetryContext
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "StreamPartialErrorAgent",
			Description: "Test ShouldRetry when stream has partial content then error",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					capturedContexts = append(capturedContexts, retryCtx)
					if retryCtx.Err != nil {
						return &RetryDecision{Retry: true}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				mo := event.Output.MessageOutput
				if mo.IsStreaming && mo.MessageStream != nil {
					for {
						_, err := mo.MessageStream.Recv()
						if err != nil {
							break
						}
					}
				}
			}
		}

		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
		assert.Equal(t, 2, len(capturedContexts))
		assert.NotNil(t, capturedContexts[0].Err, "first attempt should have stream error")
		assert.NotNil(t, capturedContexts[0].OutputMessage, "first attempt should have partial message despite error")
		assert.Equal(t, "partial chunk", capturedContexts[0].OutputMessage.Content)
	})

	t.Run("ModifiedInputsAndOptions_WithPersist", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		var capturedInputs [][]*schema.Message
		var capturedOptLens []int
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				count := atomic.AddInt32(&callCount, 1)
				inputCopy := make([]*schema.Message, len(input))
				copy(inputCopy, input)
				capturedInputs = append(capturedInputs, inputCopy)
				capturedOptLens = append(capturedOptLens, len(opts))

				r, w := schema.Pipe[*schema.Message](1)
				go func() {
					if count < 2 {
						_ = w.Send(schema.AssistantMessage("too long response exceeds limit", nil), nil)
					} else {
						_ = w.Send(schema.AssistantMessage("good response", nil), nil)
					}
					w.Close()
				}()
				return r, nil
			}).Times(2)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "StreamModifiedInputsPersistAgent",
			Description: "Test ShouldRetry with ModifiedInputMessages (persist) and ModifiedOptions in stream mode",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.OutputMessage != nil && strings.Contains(retryCtx.OutputMessage.Content, "too long") {
						return &RetryDecision{
							Retry: true,
							ModifiedInputMessages: []*schema.Message{
								schema.SystemMessage("compressed instruction"),
								schema.UserMessage("summarized history"),
							},
							PersistModifiedInputMessages: true,
							ModifiedOptions:              []model.Option{model.WithMaxTokens(16384)},
						}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		events, _ := drainStreamingAgentEvents(t, iterator)
		var foundGood bool
		for _, e := range events {
			if e.StreamContent == "good response" {
				foundGood = true
			}
		}

		require.True(t, foundGood, "should have received good response after retry with modified inputs")
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
		assert.Equal(t, 2, len(capturedInputs))
		assert.Equal(t, "compressed instruction", capturedInputs[1][0].Content, "second call should use modified input")
		assert.Equal(t, "summarized history", capturedInputs[1][1].Content)
		assert.Equal(t, capturedOptLens[0]+1, capturedOptLens[1])
	})

	t.Run("VerdictSignal_CleanStream_Rejected", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				count := atomic.AddInt32(&callCount, 1)
				if count == 1 {
					return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("bad", nil)}), nil
				}
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("good", nil)}), nil
			}).Times(2)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "VerdictCleanRejected",
			Description: "Test verdict signal on clean stream rejected",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 1,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad" {
						return &RetryDecision{Retry: true}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		var streamEvents []int
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				mo := event.Output.MessageOutput
				if mo.IsStreaming && mo.MessageStream != nil {
					idx := len(streamEvents)
					streamEvents = append(streamEvents, idx)
					var lastErr error
					for {
						_, recvErr := mo.MessageStream.Recv()
						if recvErr != nil {
							lastErr = recvErr
							break
						}
					}
					if idx == 0 {
						var willRetryErr *WillRetryError
						assert.True(t, errors.As(lastErr, &willRetryErr), "first stream should end with WillRetryError")
					} else {
						assert.ErrorIs(t, lastErr, io.EOF, "second stream should end with io.EOF")
					}
				}
			}
		}
		assert.Equal(t, 2, len(streamEvents), "should have exactly 2 stream events")
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
	})

	t.Run("VerdictSignal_StreamError_Rejected", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		streamErr := errors.New("mid-stream error")
		var callCount int32
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				count := atomic.AddInt32(&callCount, 1)
				if count == 1 {
					r, w := schema.Pipe[*schema.Message](1)
					go func() {
						_ = w.Send(schema.AssistantMessage("partial", nil), nil)
						w.Send(nil, streamErr)
					}()
					return r, nil
				}
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("good", nil)}), nil
			}).Times(2)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "VerdictStreamErrorRejected",
			Description: "Test verdict signal on stream error rejected",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 1,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					if retryCtx.Err != nil {
						return &RetryDecision{Retry: true}
					}
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		var firstEventHasWillRetry bool
		var eventCount int
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				mo := event.Output.MessageOutput
				if mo.IsStreaming && mo.MessageStream != nil {
					eventCount++
					for {
						_, recvErr := mo.MessageStream.Recv()
						if recvErr != nil {
							if eventCount == 1 {
								var willRetryErr *WillRetryError
								if errors.As(recvErr, &willRetryErr) {
									firstEventHasWillRetry = true
								}
							}
							break
						}
					}
				}
			}
		}
		assert.True(t, firstEventHasWillRetry, "first event stream should end with WillRetryError via errWrapper path")
		assert.Equal(t, 2, eventCount, "should have 2 stream events")
	})

	t.Run("VerdictSignal_Accepted_FirstAttempt", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("perfect", nil)}), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "VerdictAcceptedFirst",
			Description: "Test verdict signal accepted first attempt",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					return &RetryDecision{Retry: false}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		var eventCount int
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				mo := event.Output.MessageOutput
				if mo.IsStreaming && mo.MessageStream != nil {
					eventCount++
					var lastErr error
					for {
						_, recvErr := mo.MessageStream.Recv()
						if recvErr != nil {
							lastErr = recvErr
							break
						}
					}
					assert.ErrorIs(t, lastErr, io.EOF, "accepted stream should end with io.EOF")
				}
			}
		}
		assert.Equal(t, 1, eventCount, "should have exactly 1 event")
	})

	t.Run("VerdictSignal_AllRejected_Exhausted", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("always bad", nil)}), nil
			}).Times(3)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "VerdictAllRejected",
			Description: "Test verdict signal all rejected exhausted",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					return &RetryDecision{Retry: true}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		events, streamTermErrs := drainStreamingAgentEvents(t, iterator)
		var willRetryCount int
		var foundExhaustedErr bool
		for _, e := range events {
			if e.Err != nil && errors.Is(e.Err, ErrExceedMaxRetries) {
				foundExhaustedErr = true
			}
		}
		for _, termErr := range streamTermErrs {
			var willRetryErr *WillRetryError
			if errors.As(termErr, &willRetryErr) {
				willRetryCount++
			}
		}
		assert.Equal(t, 3, willRetryCount, "all 3 stream events should end with WillRetryError")
		require.True(t, foundExhaustedErr, "final error should be RetryExhaustedError")
	})

	t.Run("ShouldRetry_Panics_VerdictStillSent", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("trigger panic", nil)}), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "ShouldRetryPanicsAgent",
			Description: "Test that ShouldRetry panic sends verdict signal and does not deadlock",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 1,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					panic("deliberate panic in ShouldRetry")
				},
				BackoffFunc: instantBackoff,
			},
		})
		require.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}

		done := make(chan struct{})
		var events []agentEvent
		go func() {
			defer close(done)
			iterator := agent.Run(ctx, input)
			events = drainAgentEvents(t, iterator)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("test deadlocked — verdict signal was not sent after ShouldRetry panic")
		}
		require.NotEmpty(t, events)
		var foundPanicErr bool
		for _, e := range events {
			if e.Err != nil && strings.Contains(e.Err.Error(), "panic") {
				foundPanicErr = true
			}
		}
		assert.True(t, foundPanicErr, "should have received a panic error event")
	})
}

func TestErrStreamCanceled(t *testing.T) {
	t.Run("Stream_ShouldRetry_NeverRetried", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				r, w := schema.Pipe[*schema.Message](1)
				go func() {
					_ = w.Send(schema.AssistantMessage("partial", nil), nil)
					w.Send(nil, ErrStreamCanceled)
				}()
				return r, nil
			}).Times(1)

		var shouldRetryCalled int32
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "StreamCanceledShouldRetry",
			Description: "Test ErrStreamCanceled never retried with ShouldRetry",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					atomic.AddInt32(&shouldRetryCalled, 1)
					return &RetryDecision{Retry: true}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				mo := event.Output.MessageOutput
				if mo.IsStreaming && mo.MessageStream != nil {
					for {
						_, recvErr := mo.MessageStream.Recv()
						if recvErr != nil {
							break
						}
					}
				}
			}
		}
		assert.Equal(t, int32(0), atomic.LoadInt32(&shouldRetryCalled), "ShouldRetry should never be called for ErrStreamCanceled")
	})

	t.Run("Stream_LegacyIsRetryAble_NeverRetried", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				r, w := schema.Pipe[*schema.Message](1)
				go func() {
					_ = w.Send(schema.AssistantMessage("partial", nil), nil)
					w.Send(nil, ErrStreamCanceled)
				}()
				return r, nil
			}).Times(1)

		var isRetryAbleCalled int32
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "StreamCanceledLegacy",
			Description: "Test ErrStreamCanceled never retried with legacy IsRetryAble",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				IsRetryAble: func(_ context.Context, err error) bool {
					atomic.AddInt32(&isRetryAbleCalled, 1)
					return true
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)

		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				mo := event.Output.MessageOutput
				if mo.IsStreaming && mo.MessageStream != nil {
					for {
						_, recvErr := mo.MessageStream.Recv()
						if recvErr != nil {
							break
						}
					}
				}
			}
		}
		assert.Equal(t, int32(0), atomic.LoadInt32(&isRetryAbleCalled), "IsRetryAble should never be called for ErrStreamCanceled")
	})

	t.Run("Generate_ShouldRetry_NeverRetried", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, ErrStreamCanceled).Times(1)

		var shouldRetryCalled int32
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "GenCanceledShouldRetry",
			Description: "Test ErrStreamCanceled in Generate never retried",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
					atomic.AddInt32(&shouldRetryCalled, 1)
					return &RetryDecision{Retry: true}
				},
				BackoffFunc: instantBackoff,
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
		}
		iterator := agent.Run(ctx, input)

		for {
			_, ok := iterator.Next()
			if !ok {
				break
			}
		}
		assert.Equal(t, int32(0), atomic.LoadInt32(&shouldRetryCalled), "ShouldRetry should never be called for ErrStreamCanceled")
	})
}
