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

var (
	errRetryAble    = errors.New("retry-able error")
	errNonRetryAble = errors.New("non-retry-able error")
)

// ---------------------------------------------------------------------------
// Test helpers: fake models
// ---------------------------------------------------------------------------

type delegateModel struct {
	generateFn func(input []*schema.Message) (*schema.Message, error)
	streamFn   func(input []*schema.Message) (*schema.StreamReader[*schema.Message], error)
}

func (m *delegateModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if m.generateFn != nil {
		return m.generateFn(input)
	}
	return schema.AssistantMessage("default", nil), nil
}

func (m *delegateModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.streamFn != nil {
		return m.streamFn(input)
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("default", nil),
	}), nil
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

func noBackoff(_ context.Context, _ int) time.Duration { return 0 }

func drainAgentIterator(iter *AsyncIterator[*AgentEvent]) []*AgentEvent {
	var events []*AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}
	return events
}

func drainStreamEvents(events []*AgentEvent) {
	for _, event := range events {
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
			sr := event.Output.MessageOutput.MessageStream
			for {
				_, err := sr.Recv()
				if err != nil {
					break
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestRetryExhaustedError: the error type returned when retries are exhausted
// ---------------------------------------------------------------------------

func TestRetryExhaustedError(t *testing.T) {
	t.Run("wraps ErrExceedMaxRetries for errors.Is", func(t *testing.T) {
		err := &RetryExhaustedError{LastErr: errRetryAble, TotalRetries: 3}
		assert.True(t, errors.Is(err, ErrExceedMaxRetries))
	})

	t.Run("error message includes last error when present", func(t *testing.T) {
		err := &RetryExhaustedError{LastErr: errors.New("connection timeout"), TotalRetries: 3}
		assert.Contains(t, err.Error(), "exceeds max retries")
		assert.Contains(t, err.Error(), "connection timeout")
	})

	t.Run("error message reflects response rejection when LastResp set", func(t *testing.T) {
		err := &RetryExhaustedError{LastResp: schema.AssistantMessage("partial", nil), TotalRetries: 2}
		assert.Equal(t, "exceeds max retries: last response rejected by ShouldRetry", err.Error())
	})

	t.Run("error message is generic when neither error nor response", func(t *testing.T) {
		err := &RetryExhaustedError{TotalRetries: 2}
		assert.Equal(t, "exceeds max retries", err.Error())
	})
}

// ---------------------------------------------------------------------------
// TestWillRetryError: the error type emitted to observers during retry
// ---------------------------------------------------------------------------

func TestWillRetryError(t *testing.T) {
	t.Run("Error returns the original error string", func(t *testing.T) {
		e := &WillRetryError{ErrStr: "transient error", RetryAttempt: 1}
		assert.Equal(t, "transient error", e.Error())
	})

	t.Run("Unwrap returns nil when err is not set (e.g. after checkpoint restore)", func(t *testing.T) {
		e := &WillRetryError{ErrStr: "transient error", RetryAttempt: 1}
		assert.Nil(t, errors.Unwrap(e))
	})
}

// ---------------------------------------------------------------------------
// TestDefaultBackoff: exponential backoff with jitter and cap
// ---------------------------------------------------------------------------

func TestDefaultBackoff(t *testing.T) {
	ctx := context.Background()

	t.Run("exponential growth with jitter", func(t *testing.T) {
		d1 := defaultBackoff(ctx, 1)
		d2 := defaultBackoff(ctx, 2)
		d3 := defaultBackoff(ctx, 3)

		assert.True(t, d1 >= 100*time.Millisecond && d1 < 150*time.Millisecond,
			"attempt 1: ~100ms + jitter, got %v", d1)
		assert.True(t, d2 >= 200*time.Millisecond && d2 < 300*time.Millisecond,
			"attempt 2: ~200ms + jitter, got %v", d2)
		assert.True(t, d3 >= 400*time.Millisecond && d3 < 600*time.Millisecond,
			"attempt 3: ~400ms + jitter, got %v", d3)
	})

	t.Run("capped at 10s for high attempt numbers", func(t *testing.T) {
		for _, attempt := range []int{8, 10, 100} {
			d := defaultBackoff(ctx, attempt)
			assert.True(t, d >= 10*time.Second && d <= 15*time.Second,
				"attempt %d: capped at 10s+jitter, got %v", attempt, d)
		}
	})

	t.Run("non-positive attempt treated as minimum delay", func(t *testing.T) {
		assert.Equal(t, 100*time.Millisecond, defaultBackoff(ctx, 0))
		assert.Equal(t, 100*time.Millisecond, defaultBackoff(ctx, -1))
	})
}

// ---------------------------------------------------------------------------
// TestEffectiveShouldRetryError: backward-compatible dispatch logic
// ---------------------------------------------------------------------------

func TestEffectiveShouldRetryError(t *testing.T) {
	t.Run("prefers ShouldRetry when set", func(t *testing.T) {
		config := &ModelRetryConfig{
			ShouldRetry: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil && err.Error() == "specific"
			},
		}
		fn := effectiveShouldRetryError(config)
		assert.True(t, fn(context.Background(), errors.New("specific")))
		assert.False(t, fn(context.Background(), errors.New("other")))
	})

	t.Run("falls back to IsRetryAble when ShouldRetry is nil", func(t *testing.T) {
		config := &ModelRetryConfig{
			IsRetryAble: func(_ context.Context, err error) bool {
				return errors.Is(err, errRetryAble)
			},
		}
		fn := effectiveShouldRetryError(config)
		assert.True(t, fn(context.Background(), errRetryAble))
		assert.False(t, fn(context.Background(), errors.New("other")))
	})

	t.Run("defaults to retry all errors when nothing configured", func(t *testing.T) {
		fn := effectiveShouldRetryError(&ModelRetryConfig{})
		assert.True(t, fn(context.Background(), errors.New("any")))
		assert.False(t, fn(context.Background(), nil))
	})
}

// ---------------------------------------------------------------------------
// TestLegacyRetry_IsRetryAble: backward-compatible error-only retry using
// the deprecated IsRetryAble field. Ensures existing users are not broken.
// ---------------------------------------------------------------------------

func TestLegacyRetry_IsRetryAble(t *testing.T) {
	isRetryAble := func(_ context.Context, err error) bool { return errors.Is(err, errRetryAble) }

	t.Run("Generate retries transient errors then succeeds", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				if atomic.AddInt32(&callCount, 1) < 3 {
					return nil, errRetryAble
				}
				return schema.AssistantMessage("ok", nil), nil
			}).Times(3)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, IsRetryAble: isRetryAble},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.Equal(t, 1, len(events))
		assert.Nil(t, events[0].Err)
		assert.Equal(t, "ok", events[0].Output.MessageOutput.Message.Content)
		assert.Equal(t, int32(3), atomic.LoadInt32(&callCount))
	})

	t.Run("Generate stops on non-retryable error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errNonRetryAble).Times(1)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, IsRetryAble: isRetryAble},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.True(t, errors.Is(events[0].Err, errNonRetryAble))
	})

	t.Run("Generate returns RetryExhaustedError when all retries fail", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errRetryAble).Times(4)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, IsRetryAble: isRetryAble},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.True(t, errors.Is(events[0].Err, ErrExceedMaxRetries))
		var retryErr *RetryExhaustedError
		assert.True(t, errors.As(events[0].Err, &retryErr))
		assert.True(t, errors.Is(retryErr.LastErr, errRetryAble))
	})

	t.Run("Stream retries direct error then succeeds", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				if atomic.AddInt32(&callCount, 1) < 2 {
					return nil, errRetryAble
				}
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
			}).Times(2)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, IsRetryAble: isRetryAble},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{
			Messages: []Message{schema.UserMessage("hi")}, EnableStreaming: true,
		}))
		assert.Equal(t, 1, len(events))
		assert.Nil(t, events[0].Err)
		assert.True(t, events[0].Output.MessageOutput.IsStreaming)
	})

	t.Run("Stream retries mid-stream error and emits WillRetryError events", func(t *testing.T) {
		m := &streamErrorModel{failAtChunk: 2, maxFailures: 2}

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, IsRetryAble: isRetryAble},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{
			Messages: []Message{schema.UserMessage("hi")}, EnableStreaming: true,
		}))
		assert.Equal(t, 3, len(events))

		var willRetryCount int
		for _, event := range events {
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
				sr := event.Output.MessageOutput.MessageStream
				for {
					_, err := sr.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						var willRetry *WillRetryError
						if errors.As(err, &willRetry) {
							willRetryCount++
						}
						break
					}
				}
			}
		}
		assert.Equal(t, 2, willRetryCount)
		assert.Equal(t, int32(3), atomic.LoadInt32(&m.callCount))
	})

	t.Run("Stream non-retryable mid-stream error passes through immediately", func(t *testing.T) {
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				sr, sw := schema.Pipe[*schema.Message](10)
				go func() {
					defer sw.Close()
					sw.Send(schema.AssistantMessage("chunk1", nil), nil)
					sw.Send(nil, errNonRetryAble)
				}()
				return sr, nil
			},
		}

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, IsRetryAble: isRetryAble},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{
			Messages: []Message{schema.UserMessage("hi")}, EnableStreaming: true,
		}))

		var streamErr error
		for _, event := range events {
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
				sr := event.Output.MessageOutput.MessageStream
				for {
					_, err := sr.Recv()
					if err != nil && err != io.EOF {
						streamErr = err
						break
					}
					if err == io.EOF {
						break
					}
				}
			}
		}
		assert.True(t, errors.Is(streamErr, errNonRetryAble))
	})

	t.Run("WithTools Generate retries then succeeds", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				if atomic.AddInt32(&callCount, 1) < 2 {
					return nil, errRetryAble
				}
				return schema.AssistantMessage("ok", nil), nil
			}).Times(2)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ToolsConfig:      ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{&fakeToolForTest{tarCount: 0}}}},
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, IsRetryAble: isRetryAble},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.Nil(t, events[0].Err)
		assert.Equal(t, "ok", events[0].Output.MessageOutput.Message.Content)
	})

	t.Run("default IsRetryAble retries all errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				if atomic.AddInt32(&callCount, 1) < 2 {
					return nil, errors.New("arbitrary error")
				}
				return schema.AssistantMessage("ok", nil), nil
			}).Times(2)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.Nil(t, events[0].Err)
		assert.Equal(t, "ok", events[0].Output.MessageOutput.Message.Content)
	})

	t.Run("no retry config means errors propagate immediately", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errRetryAble).Times(1)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.True(t, errors.Is(events[0].Err, errRetryAble))
	})

	t.Run("custom BackoffFunc is called with correct attempt numbers", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var backoffCalls []int
		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				if atomic.AddInt32(&callCount, 1) < 3 {
					return nil, errRetryAble
				}
				return schema.AssistantMessage("ok", nil), nil
			}).Times(3)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries:  3,
				IsRetryAble: isRetryAble,
				BackoffFunc: func(_ context.Context, attempt int) time.Duration {
					backoffCalls = append(backoffCalls, attempt)
					return time.Millisecond
				},
			},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.Nil(t, events[0].Err)
		assert.Equal(t, []int{1, 2}, backoffCalls)
	})
}

// ---------------------------------------------------------------------------
// TestShouldRetry_ResponseBased: retry triggered by inspecting a successful
// response (e.g. FinishReason=length, empty content). This is the new
// capability that ShouldRetry enables beyond what IsRetryAble can do.
// ---------------------------------------------------------------------------

func TestShouldRetry_ResponseBased(t *testing.T) {
	shouldRetryOnLength := func(_ context.Context, resp *schema.Message, err error) bool {
		if err != nil {
			return true
		}
		return resp.ResponseMeta != nil && resp.ResponseMeta.FinishReason == "length"
	}

	t.Run("Generate retries when FinishReason is length", func(t *testing.T) {
		var callCount int32
		m := &delegateModel{
			generateFn: func(_ []*schema.Message) (*schema.Message, error) {
				if atomic.AddInt32(&callCount, 1) < 3 {
					msg := schema.AssistantMessage("partial", nil)
					msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
					return msg, nil
				}
				msg := schema.AssistantMessage("full content", nil)
				msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop"}
				return msg, nil
			},
		}

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, ShouldRetry: shouldRetryOnLength},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.Equal(t, int32(3), atomic.LoadInt32(&callCount))

		lastEvent := events[len(events)-1]
		assert.Nil(t, lastEvent.Err)
		assert.Equal(t, "full content", lastEvent.Output.MessageOutput.Message.Content)
	})

	t.Run("Stream retries when FinishReason is length", func(t *testing.T) {
		var callCount int32
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				if atomic.AddInt32(&callCount, 1) < 2 {
					msg := schema.AssistantMessage("partial", nil)
					msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
					return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
				}
				msg := schema.AssistantMessage("complete", nil)
				msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop"}
				return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
			},
		}

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, ShouldRetry: shouldRetryOnLength},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{
			Messages: []Message{schema.UserMessage("hi")}, EnableStreaming: true,
		}))
		drainStreamEvents(events)
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))

		lastEvent := events[len(events)-1]
		assert.Nil(t, lastEvent.Err)
		assert.True(t, lastEvent.Output.MessageOutput.IsStreaming)
	})

	t.Run("Generate retries on empty content", func(t *testing.T) {
		var callCount int32
		m := &delegateModel{
			generateFn: func(_ []*schema.Message) (*schema.Message, error) {
				if atomic.AddInt32(&callCount, 1) < 2 {
					return schema.AssistantMessage("", nil), nil
				}
				return schema.AssistantMessage("real content", nil), nil
			},
		}

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 2,
				ShouldRetry: func(_ context.Context, resp *schema.Message, err error) bool {
					if err != nil {
						return true
					}
					return resp.Content == "" && len(resp.ToolCalls) == 0
				},
			},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		lastEvent := events[len(events)-1]
		assert.Nil(t, lastEvent.Err)
		assert.Equal(t, "real content", lastEvent.Output.MessageOutput.Message.Content)
	})

	t.Run("Generate handles mixed errors and response retries", func(t *testing.T) {
		var callCount int32
		m := &delegateModel{
			generateFn: func(_ []*schema.Message) (*schema.Message, error) {
				switch atomic.AddInt32(&callCount, 1) {
				case 1:
					return nil, errRetryAble
				case 2:
					msg := schema.AssistantMessage("partial", nil)
					msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
					return msg, nil
				default:
					msg := schema.AssistantMessage("done", nil)
					msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop"}
					return msg, nil
				}
			},
		}

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 5,
				ShouldRetry: func(_ context.Context, resp *schema.Message, err error) bool {
					if err != nil {
						return errors.Is(err, errRetryAble)
					}
					return resp.ResponseMeta != nil && resp.ResponseMeta.FinishReason == "length"
				},
			},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.Equal(t, int32(3), atomic.LoadInt32(&callCount))
		lastEvent := events[len(events)-1]
		assert.Nil(t, lastEvent.Err)
		assert.Equal(t, "done", lastEvent.Output.MessageOutput.Message.Content)
	})

	t.Run("Generate RetryExhaustedError carries LastResp when response-triggered", func(t *testing.T) {
		m := &delegateModel{
			generateFn: func(_ []*schema.Message) (*schema.Message, error) {
				msg := schema.AssistantMessage("always partial", nil)
				msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
				return msg, nil
			},
		}

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 2, ShouldRetry: shouldRetryOnLength},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		lastEvent := events[len(events)-1]
		assert.True(t, errors.Is(lastEvent.Err, ErrExceedMaxRetries))

		var retryErr *RetryExhaustedError
		assert.True(t, errors.As(lastEvent.Err, &retryErr))
		assert.Nil(t, retryErr.LastErr)
		assert.NotNil(t, retryErr.LastResp)
		assert.Equal(t, "always partial", retryErr.LastResp.Content)
	})

	t.Run("Stream RetryExhaustedError carries LastResp when response-triggered", func(t *testing.T) {
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				msg := schema.AssistantMessage("always partial", nil)
				msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
				return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
			},
		}

		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries:  1,
			ShouldRetry: shouldRetryOnLength,
			BackoffFunc: noBackoff,
		})

		_, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.Error(t, err)
		var retryErr *RetryExhaustedError
		assert.True(t, errors.As(err, &retryErr))
		assert.Nil(t, retryErr.LastErr)
		assert.NotNil(t, retryErr.LastResp)
		assert.Equal(t, "always partial", retryErr.LastResp.Content)
	})

	t.Run("RetryOnError helper only retries errors, never responses", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				if atomic.AddInt32(&callCount, 1) < 2 {
					return nil, errRetryAble
				}
				return schema.AssistantMessage("ok", nil), nil
			}).Times(2)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: RetryOnError(func(_ context.Context, err error) bool {
					return errors.Is(err, errRetryAble)
				}),
			},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.Nil(t, events[0].Err)
		assert.Equal(t, "ok", events[0].Output.MessageOutput.Message.Content)
	})
}

// ---------------------------------------------------------------------------
// TestModifyInput: transforming input messages between retry attempts.
// Covers the motivating use case: appending a truncated response and
// "please continue" when FinishReason=length.
// ---------------------------------------------------------------------------

func TestModifyInput(t *testing.T) {
	t.Run("Generate appends truncated response on FinishReason=length", func(t *testing.T) {
		var capturedInputs [][]Message
		var callCount int32
		m := &delegateModel{
			generateFn: func(input []*schema.Message) (*schema.Message, error) {
				capturedInputs = append(capturedInputs, input)
				if atomic.AddInt32(&callCount, 1) < 2 {
					msg := schema.AssistantMessage("partial response", nil)
					msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
					return msg, nil
				}
				msg := schema.AssistantMessage("final answer", nil)
				msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop"}
				return msg, nil
			},
		}

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: func(_ context.Context, resp *schema.Message, err error) bool {
					if err != nil {
						return true
					}
					return resp.ResponseMeta != nil && resp.ResponseMeta.FinishReason == "length"
				},
				ModifyInput: func(_ context.Context, input []*schema.Message, resp *schema.Message, _ error) ([]*schema.Message, error) {
					if resp != nil {
						return append(input, resp, schema.UserMessage("Please continue.")), nil
					}
					return input, nil
				},
			},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("Hello")}}))
		lastEvent := events[len(events)-1]
		assert.Nil(t, lastEvent.Err)
		assert.Equal(t, "final answer", lastEvent.Output.MessageOutput.Message.Content)

		assert.Equal(t, 2, len(capturedInputs))
		retryInput := capturedInputs[1]
		assert.True(t, len(retryInput) > len(capturedInputs[0]))

		var hasPartialResp, hasContinue bool
		for _, msg := range retryInput {
			if msg.Content == "partial response" {
				hasPartialResp = true
			}
			if msg.Content == "Please continue." {
				hasContinue = true
			}
		}
		assert.True(t, hasPartialResp, "retry input should contain the truncated response")
		assert.True(t, hasContinue, "retry input should contain 'Please continue.'")
	})

	t.Run("Generate ModifyInput also called on error-triggered retries", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var capturedInputs [][]Message
		var callCount int32
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				capturedInputs = append(capturedInputs, input)
				if atomic.AddInt32(&callCount, 1) < 2 {
					return nil, errRetryAble
				}
				return schema.AssistantMessage("ok", nil), nil
			}).Times(2)

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
				ShouldRetry: RetryOnError(func(_ context.Context, err error) bool {
					return errors.Is(err, errRetryAble)
				}),
				ModifyInput: func(_ context.Context, input []*schema.Message, _ *schema.Message, err error) ([]*schema.Message, error) {
					if err != nil && len(input) > 2 {
						return input[1:], nil
					}
					return input, nil
				},
			},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{Messages: []Message{schema.UserMessage("hi")}}))
		assert.Nil(t, events[0].Err)
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
	})

	t.Run("Generate ModifyInput error aborts retry immediately", func(t *testing.T) {
		modifyErr := errors.New("modify input failed")
		m := &delegateModel{
			generateFn: func(_ []*schema.Message) (*schema.Message, error) {
				msg := schema.AssistantMessage("bad", nil)
				msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
				return msg, nil
			},
		}

		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(_ context.Context, resp *schema.Message, err error) bool {
				if err != nil {
					return true
				}
				return resp.ResponseMeta != nil && resp.ResponseMeta.FinishReason == "length"
			},
			ModifyInput: func(_ context.Context, _ []*schema.Message, _ *schema.Message, _ error) ([]*schema.Message, error) {
				return nil, modifyErr
			},
			BackoffFunc: noBackoff,
		})

		_, err := wrapper.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.ErrorIs(t, err, modifyErr)
	})

	t.Run("Generate ModifyInput error aborts error-triggered retry", func(t *testing.T) {
		modifyErr := errors.New("modify failed on error")
		m := &delegateModel{
			generateFn: func(_ []*schema.Message) (*schema.Message, error) {
				return nil, errRetryAble
			},
		}

		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries:  2,
			ShouldRetry: func(_ context.Context, _ *schema.Message, err error) bool { return err != nil },
			ModifyInput: func(_ context.Context, _ []*schema.Message, _ *schema.Message, _ error) ([]*schema.Message, error) {
				return nil, modifyErr
			},
			BackoffFunc: noBackoff,
		})

		_, err := wrapper.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.ErrorIs(t, err, modifyErr)
	})

	t.Run("Stream ModifyInput on direct error retries with modified input", func(t *testing.T) {
		var callCount int32
		m := &delegateModel{
			streamFn: func(input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				if atomic.AddInt32(&callCount, 1) < 2 {
					return nil, errRetryAble
				}
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
			},
		}

		var modifyCalled bool
		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries:  2,
			ShouldRetry: func(_ context.Context, _ *schema.Message, err error) bool { return err != nil },
			ModifyInput: func(_ context.Context, input []*schema.Message, resp *schema.Message, err error) ([]*schema.Message, error) {
				modifyCalled = true
				assert.Nil(t, resp)
				assert.NotNil(t, err)
				return append(input, schema.UserMessage("retry hint")), nil
			},
			BackoffFunc: noBackoff,
		})

		stream, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.NoError(t, err)
		assert.True(t, modifyCalled)

		msg, concatErr := schema.ConcatMessageStream(stream)
		assert.NoError(t, concatErr)
		assert.Equal(t, "ok", msg.Content)
	})

	t.Run("Stream ModifyInput error on direct error aborts retry", func(t *testing.T) {
		modifyErr := errors.New("modify failed")
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				return nil, errRetryAble
			},
		}

		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries:  2,
			ShouldRetry: func(_ context.Context, _ *schema.Message, err error) bool { return err != nil },
			ModifyInput: func(_ context.Context, _ []*schema.Message, _ *schema.Message, _ error) ([]*schema.Message, error) {
				return nil, modifyErr
			},
			BackoffFunc: noBackoff,
		})

		_, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.ErrorIs(t, err, modifyErr)
	})

	t.Run("Stream ModifyInput on mid-stream concat error retries successfully", func(t *testing.T) {
		streamErr := errors.New("mid-stream error")
		var callCount int32
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				count := atomic.AddInt32(&callCount, 1)
				sr, sw := schema.Pipe[*schema.Message](10)
				go func() {
					defer sw.Close()
					if count < 2 {
						sw.Send(schema.AssistantMessage("partial", nil), nil)
						sw.Send(nil, streamErr)
						return
					}
					msg := schema.AssistantMessage("ok", nil)
					msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop"}
					sw.Send(msg, nil)
				}()
				return sr, nil
			},
		}

		var modifyCalled bool
		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			ModifyInput: func(_ context.Context, input []*schema.Message, resp *schema.Message, err error) ([]*schema.Message, error) {
				modifyCalled = true
				assert.Nil(t, resp)
				assert.NotNil(t, err)
				return input, nil
			},
			BackoffFunc: noBackoff,
		})

		stream, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.NoError(t, err)
		assert.True(t, modifyCalled)

		msg, concatErr := schema.ConcatMessageStream(stream)
		assert.NoError(t, concatErr)
		assert.Equal(t, "ok", msg.Content)
	})

	t.Run("Stream ModifyInput error on mid-stream concat error aborts retry", func(t *testing.T) {
		streamErr := errors.New("mid-stream error")
		modifyErr := errors.New("modify failed on concat error")
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				sr, sw := schema.Pipe[*schema.Message](10)
				go func() {
					defer sw.Close()
					sw.Send(nil, streamErr)
				}()
				return sr, nil
			},
		}

		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries:  2,
			ShouldRetry: func(_ context.Context, _ *schema.Message, err error) bool { return err != nil },
			ModifyInput: func(_ context.Context, _ []*schema.Message, _ *schema.Message, _ error) ([]*schema.Message, error) {
				return nil, modifyErr
			},
			BackoffFunc: noBackoff,
		})

		_, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.ErrorIs(t, err, modifyErr)
	})

	t.Run("Stream ModifyInput on response-triggered retry appends context", func(t *testing.T) {
		var callCount int32
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				if atomic.AddInt32(&callCount, 1) < 2 {
					msg := schema.AssistantMessage("partial", nil)
					msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
					return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
				}
				msg := schema.AssistantMessage("done", nil)
				msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop"}
				return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
			},
		}

		var modifyInputs [][]*schema.Message
		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries: 3,
			ShouldRetry: func(_ context.Context, resp *schema.Message, err error) bool {
				if err != nil {
					return true
				}
				return resp.ResponseMeta != nil && resp.ResponseMeta.FinishReason == "length"
			},
			ModifyInput: func(_ context.Context, input []*schema.Message, resp *schema.Message, err error) ([]*schema.Message, error) {
				modifyInputs = append(modifyInputs, input)
				assert.NotNil(t, resp)
				assert.Nil(t, err)
				return append(input, resp, schema.UserMessage("continue")), nil
			},
			BackoffFunc: noBackoff,
		})

		stream, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.NoError(t, err)
		msg, concatErr := schema.ConcatMessageStream(stream)
		assert.NoError(t, concatErr)
		assert.Equal(t, "done", msg.Content)
		assert.Equal(t, 1, len(modifyInputs))
	})

	t.Run("Stream ModifyInput error on response-triggered retry aborts", func(t *testing.T) {
		modifyErr := errors.New("modify failed on response")
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				msg := schema.AssistantMessage("partial", nil)
				msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
				return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
			},
		}

		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(_ context.Context, resp *schema.Message, err error) bool {
				if err != nil {
					return true
				}
				return resp.ResponseMeta != nil && resp.ResponseMeta.FinishReason == "length"
			},
			ModifyInput: func(_ context.Context, _ []*schema.Message, _ *schema.Message, _ error) ([]*schema.Message, error) {
				return nil, modifyErr
			},
			BackoffFunc: noBackoff,
		})

		_, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.ErrorIs(t, err, modifyErr)
	})

	t.Run("Stream error-only path with ModifyInput (no ShouldRetry)", func(t *testing.T) {
		midStreamErr := errors.New("mid-stream error")
		var callCount int32
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				count := atomic.AddInt32(&callCount, 1)
				sr, sw := schema.Pipe[*schema.Message](10)
				go func() {
					defer sw.Close()
					if count < 2 {
						sw.Send(schema.AssistantMessage("chunk", nil), nil)
						sw.Send(nil, midStreamErr)
						return
					}
					sw.Send(schema.AssistantMessage("success", nil), nil)
				}()
				return sr, nil
			},
		}

		var modifyCalled bool
		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries: 2,
			ModifyInput: func(_ context.Context, input []*schema.Message, resp *schema.Message, err error) ([]*schema.Message, error) {
				modifyCalled = true
				assert.Nil(t, resp)
				assert.NotNil(t, err)
				return input, nil
			},
			BackoffFunc: noBackoff,
		})

		stream, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.NoError(t, err)
		assert.True(t, modifyCalled)

		msg, concatErr := schema.ConcatMessageStream(stream)
		assert.NoError(t, concatErr)
		assert.Equal(t, "success", msg.Content)
	})

	t.Run("Stream error-only path ModifyInput error aborts retry", func(t *testing.T) {
		midStreamErr := errors.New("mid-stream error")
		modifyErr := errors.New("modify failed")
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				sr, sw := schema.Pipe[*schema.Message](10)
				go func() {
					defer sw.Close()
					sw.Send(nil, midStreamErr)
				}()
				return sr, nil
			},
		}

		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries: 2,
			ModifyInput: func(_ context.Context, _ []*schema.Message, _ *schema.Message, _ error) ([]*schema.Message, error) {
				return nil, modifyErr
			},
			BackoffFunc: noBackoff,
		})

		_, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.ErrorIs(t, err, modifyErr)
	})
}

// ---------------------------------------------------------------------------
// TestStream_ShouldRetry_NonRetryableErrors: verifies that non-retryable
// errors in various stream paths pass through without retrying.
// ---------------------------------------------------------------------------

func TestStream_NonRetryableErrors(t *testing.T) {
	t.Run("mid-stream concat error not matched by ShouldRetry passes through", func(t *testing.T) {
		nonRetryableErr := errors.New("non-retryable mid-stream error")
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				sr, sw := schema.Pipe[*schema.Message](10)
				go func() {
					defer sw.Close()
					sw.Send(nil, nonRetryableErr)
				}()
				return sr, nil
			},
		}

		wrapper := newRetryModelWrapper(m, &ModelRetryConfig{
			MaxRetries:  2,
			ShouldRetry: func(_ context.Context, _ *schema.Message, _ error) bool { return false },
			BackoffFunc: noBackoff,
		})

		_, err := wrapper.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
		assert.ErrorIs(t, err, nonRetryableErr)
	})

	t.Run("WithTools Stream non-retryable direct error passes through", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errNonRetryAble).Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "test", Description: "d", Instruction: "i", Model: cm,
			ToolsConfig:      ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{&fakeToolForTest{tarCount: 0}}}},
			ModelRetryConfig: &ModelRetryConfig{MaxRetries: 3, IsRetryAble: func(_ context.Context, err error) bool { return errors.Is(err, errRetryAble) }},
		})
		assert.NoError(t, err)

		events := drainAgentIterator(agent.Run(context.Background(), &AgentInput{
			Messages: []Message{schema.UserMessage("hi")}, EnableStreaming: true,
		}))
		assert.True(t, errors.Is(events[0].Err, errNonRetryAble))
	})
}

// ---------------------------------------------------------------------------
// TestSequentialWorkflow_Retry: integration tests verifying retry behavior
// propagates correctly in multi-agent pipelines.
// ---------------------------------------------------------------------------

func TestSequentialWorkflow_Retry(t *testing.T) {
	t.Run("retry succeeds and successor agent receives final message", func(t *testing.T) {
		retryModel := &streamErrorModel{failAtChunk: 2, maxFailures: 2}

		agentA, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "AgentA", Description: "d", Instruction: "i", Model: retryModel,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries:  3,
				IsRetryAble: func(_ context.Context, err error) bool { return errors.Is(err, errRetryAble) },
			},
		})
		assert.NoError(t, err)

		capturingModel := &inputCapturingModel{}
		agentB, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "AgentB", Description: "d", Instruction: "i", Model: capturingModel,
		})
		assert.NoError(t, err)

		seq, err := NewSequentialAgent(context.Background(), &SequentialAgentConfig{
			Name: "Seq", Description: "d", SubAgents: []Agent{agentA, agentB},
		})
		assert.NoError(t, err)

		input := &AgentInput{Messages: []Message{schema.UserMessage("Hello")}, EnableStreaming: true}
		ctx, _ := initRunCtx(context.Background(), seq.Name(context.Background()), input)
		iter := seq.Run(ctx, input)

		var willRetryCount int
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
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
							willRetryCount++
						}
						break
					}
				}
			}
		}

		assert.Equal(t, 2, willRetryCount, "user should observe 2 WillRetryError events from failed streams")
		assert.Equal(t, 1, len(capturingModel.capturedInputs), "AgentB should run exactly once")

		successorInput := capturingModel.capturedInputs[0]
		var hasSuccess bool
		for _, msg := range successorInput {
			if strings.Contains(msg.Content, "chunkchunkchunkchunkchunk") {
				hasSuccess = true
				break
			}
		}
		assert.True(t, hasSuccess, "AgentB should receive the final successful message")
	})

	t.Run("non-retryable stream error stops the pipeline", func(t *testing.T) {
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				sr, sw := schema.Pipe[*schema.Message](10)
				go func() {
					defer sw.Close()
					sw.Send(schema.AssistantMessage("chunk1", nil), nil)
					sw.Send(nil, errNonRetryAble)
				}()
				return sr, nil
			},
		}

		agentA, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "AgentA", Description: "d", Instruction: "i", Model: m,
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries:  3,
				IsRetryAble: func(_ context.Context, err error) bool { return errors.Is(err, errRetryAble) },
			},
		})
		assert.NoError(t, err)

		capturingModel := &inputCapturingModel{}
		agentB, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "AgentB", Description: "d", Instruction: "i", Model: capturingModel,
		})
		assert.NoError(t, err)

		seq, err := NewSequentialAgent(context.Background(), &SequentialAgentConfig{
			Name: "Seq", Description: "d", SubAgents: []Agent{agentA, agentB},
		})
		assert.NoError(t, err)

		input := &AgentInput{Messages: []Message{schema.UserMessage("Hello")}, EnableStreaming: true}
		ctx, _ := initRunCtx(context.Background(), seq.Name(context.Background()), input)
		iter := seq.Run(ctx, input)

		var finalErr error
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				finalErr = event.Err
			}
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
				sr := event.Output.MessageOutput.MessageStream
				for {
					_, err := sr.Recv()
					if err != nil {
						break
					}
				}
			}
		}

		assert.True(t, errors.Is(finalErr, errNonRetryAble))
		assert.Equal(t, 0, len(capturingModel.capturedInputs), "AgentB should NOT run when AgentA fails")
	})

	t.Run("no retry config means stream error stops the pipeline", func(t *testing.T) {
		var callCount int32
		m := &delegateModel{
			streamFn: func(_ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				atomic.AddInt32(&callCount, 1)
				sr, sw := schema.Pipe[*schema.Message](10)
				go func() {
					defer sw.Close()
					sw.Send(schema.AssistantMessage("chunk", nil), nil)
					sw.Send(nil, errRetryAble)
				}()
				return sr, nil
			},
		}

		agentA, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "AgentA", Description: "d", Instruction: "i", Model: m,
		})
		assert.NoError(t, err)

		capturingModel := &inputCapturingModel{}
		agentB, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
			Name: "AgentB", Description: "d", Instruction: "i", Model: capturingModel,
		})
		assert.NoError(t, err)

		seq, err := NewSequentialAgent(context.Background(), &SequentialAgentConfig{
			Name: "Seq", Description: "d", SubAgents: []Agent{agentA, agentB},
		})
		assert.NoError(t, err)

		input := &AgentInput{Messages: []Message{schema.UserMessage("Hello")}, EnableStreaming: true}
		ctx, _ := initRunCtx(context.Background(), seq.Name(context.Background()), input)
		iter := seq.Run(ctx, input)

		var finalErr error
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				finalErr = event.Err
			}
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
				sr := event.Output.MessageOutput.MessageStream
				for {
					_, err := sr.Recv()
					if err != nil {
						break
					}
				}
			}
		}

		assert.True(t, errors.Is(finalErr, errRetryAble))
		assert.Equal(t, 0, len(capturingModel.capturedInputs), "AgentB should NOT run")
		assert.Equal(t, int32(1), atomic.LoadInt32(&callCount), "model called once (no retry)")
	})
}

// ---------------------------------------------------------------------------
// TestCheckpointSave_WillRetryError: verifies that checkpoint save works
// when the session contains an event with an unconsumed WillRetryError stream.
//
// Scenario:
//  1. ChatModelAgent with retry + interrupt tool
//  2. Stream #1: partial chunk → errRetryAble mid-stream (event sent to user with WillRetryError)
//  3. Stream #2 (retry): tool-call → tool interrupts → checkpoint save
//  4. The unconsumed WillRetryError stream in the session must not break gob encoding
// ---------------------------------------------------------------------------

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
			sw.Send(schema.AssistantMessage("partial", nil), nil)
			sw.Send(nil, errRetryAble)
			return
		}
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

func TestCheckpointSave_WillRetryError_StreamNotConsumed(t *testing.T) {
	ctx := context.Background()

	mdl := &failThenToolCallStreamModel{}
	itool := &interruptToolForRetryTest{}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "TestAgent", Description: "d", Instruction: "i", Model: mdl,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{itool}},
		},
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  1,
			IsRetryAble: func(_ context.Context, err error) bool { return errors.Is(err, errRetryAble) },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	store := newMyStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	iter := runner.Run(ctx, []Message{schema.UserMessage("hello")}, WithCheckPointID("ckpt-1"))
	_ = drainAgentIterator(iter)

	_, exists, _ := store.Get(ctx, "ckpt-1")
	assert.True(t, exists, "checkpoint should be saved even with unconsumed WillRetryError stream in session")
	assert.Equal(t, int32(2), atomic.LoadInt32(&mdl.streamCallCount))
}
