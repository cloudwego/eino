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
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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

func TestChatModelAgentRetry_NoTools_StreamError(t *testing.T) {
	ctx := context.Background()

	m := &streamErrorModel{
		failAtChunk: 2,
		maxFailures: 2,
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       m,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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

	sr := event.Output.MessageOutput.MessageStream
	chunks := 0
	for {
		m, err := sr.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = m
		chunks++
	}
	assert.Equal(t, 5, chunks)

	_, ok = iterator.Next()
	assert.False(t, ok)
	assert.Equal(t, int32(3), atomic.LoadInt32(&m.callCount))
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
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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

func TestChatModelAgentRetry_WithTools_StreamError(t *testing.T) {
	ctx := context.Background()

	m := &streamErrorModel{
		failAtChunk: 2,
		maxFailures: 2,
		returnTool:  false,
	}

	fakeTool := &fakeToolForTest{tarCount: 0}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       m,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
		},
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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
	assert.True(t, errors.Is(errs[0], errRetryAble))
	assert.True(t, errors.Is(errs[1], errRetryAble))
}

func TestChatModelAgentRetry_NonRetryAbleError(t *testing.T) {
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
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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

func (m *inputCapturingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.capturedInputs = append(m.capturedInputs, input)
	return schema.AssistantMessage("Response from capturing model", nil), nil
}

func (m *inputCapturingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.capturedInputs = append(m.capturedInputs, input)
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("Response from capturing model", nil),
	}), nil
}

func (m *inputCapturingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestChatModelAgentRetry_WithTools_StreamError_SequentialFlow(t *testing.T) {
	ctx := context.Background()

	retryModel := &streamErrorModel{
		failAtChunk: 2,
		maxFailures: 2,
		returnTool:  false,
	}

	fakeTool := &fakeToolForTest{tarCount: 0}

	retryAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryAgent",
		Description: "Agent with retry that emits stream errors",
		Instruction: "You are a helpful assistant.",
		Model:       retryModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
		},
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
		},
	})
	assert.NoError(t, err)

	capturingModel := &inputCapturingModel{}
	successorAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "SuccessorAgent",
		Description: "Agent that captures input from previous agent",
		Instruction: "You are a successor agent.",
		Model:       capturingModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
		},
	})
	assert.NoError(t, err)

	sequentialAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "SequentialAgent",
		Description: "Sequential agent with retry and successor",
		SubAgents:   []Agent{retryAgent, successorAgent},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := sequentialAgent.Run(ctx, input)

	var events []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.GreaterOrEqual(t, len(events), 4)

	assert.Equal(t, 1, len(capturingModel.capturedInputs))

	successorInput := capturingModel.capturedInputs[0]
	var hasSuccessfulRetryAgentMessage bool
	for _, msg := range successorInput {
		if strings.Contains(msg.Content, "chunkchunkchunkchunkchunk") {
			hasSuccessfulRetryAgentMessage = true
			break
		}
	}
	assert.True(t, hasSuccessfulRetryAgentMessage, "Successor agent should only receive the final successful message from retry agent")

	for _, msg := range successorInput {
		assert.NotContains(t, msg.Content, "retry-able error", "Successor agent should not receive failed stream error messages")
	}

	assert.Equal(t, int32(3), atomic.LoadInt32(&retryModel.callCount))
}

func TestChatModelAgentRetry_MaxRetriesExhausted(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errRetryAble).Times(3)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RetryTestAgent",
		Description: "Test agent for retry functionality",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
			BackoffFunc: func(attempt int) time.Duration {
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
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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
			IsRetryAble: func(err error) bool { return errors.Is(err, errRetryAble) },
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
