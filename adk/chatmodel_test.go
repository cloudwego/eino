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
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestChatModelAgentRun tests the Run method of ChatModelAgent
func TestChatModelAgentRun(t *testing.T) {
	// Basic test for Run method
	t.Run("BasicFunctionality", func(t *testing.T) {
		ctx := context.Background()

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello, I am an AI assistant.", nil), nil).
			Times(1)

		// Create a ChatModelAgent
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for unit testing",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		// Run the agent
		input := &AgentInput{
			Messages: []Message{
				schema.UserMessage("Hello, who are you?"),
			},
		}
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		// Get the event from the iterator
		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event)
		assert.Nil(t, event.Err)
		assert.NotNil(t, event.Output)
		assert.NotNil(t, event.Output.MessageOutput)

		// Verify the message content
		msg := event.Output.MessageOutput.Message
		assert.NotNil(t, msg)
		assert.Equal(t, "Hello, I am an AI assistant.", msg.Content)

		// No more events
		_, ok = iterator.Next()
		assert.False(t, ok)
	})

	// Test with streaming output
	t.Run("StreamOutput", func(t *testing.T) {
		ctx := context.Background()

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Create a stream reader for the mock response
		sr := schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("Hello", nil),
			schema.AssistantMessage(", I am", nil),
			schema.AssistantMessage(" an AI assistant.", nil),
		})

		// Set up expectations for the mock model
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(sr, nil).
			Times(1)

		// Create a ChatModelAgent
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for unit testing",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		// Run the agent with streaming enabled
		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello, who are you?")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		// Get the event from the iterator
		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event)
		assert.Nil(t, event.Err)
		assert.NotNil(t, event.Output)
		assert.NotNil(t, event.Output.MessageOutput)
		assert.True(t, event.Output.MessageOutput.IsStreaming)

		// No more events
		_, ok = iterator.Next()
		assert.False(t, ok)
	})

	// Test error handling
	t.Run("ErrorHandling", func(t *testing.T) {
		ctx := context.Background()

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model to return an error
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, errors.New("model error")).
			Times(1)

		// Create a ChatModelAgent
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for unit testing",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		// Run the agent
		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello, who are you?")},
		}
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		// Get the event from the iterator, should contain an error
		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event)
		assert.NotNil(t, event.Err)
		assert.Contains(t, event.Err.Error(), "model error")

		// No more events
		_, ok = iterator.Next()
		assert.False(t, ok)
	})

	// Test with tools
	t.Run("WithTools", func(t *testing.T) {
		ctx := context.Background()

		// Create a fake tool for testing
		fakeTool := &fakeToolForTest{
			tarCount: 1,
		}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Using tool",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Task completed", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create a ChatModelAgent with tools
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for unit testing",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{fakeTool},
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		// Run the agent
		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Use the test tool")},
		}
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		// Get events from the iterator
		// First event should be the model output with tool call
		event1, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event1)
		assert.Nil(t, event1.Err)
		assert.NotNil(t, event1.Output)
		assert.NotNil(t, event1.Output.MessageOutput)
		assert.Equal(t, schema.Assistant, event1.Output.MessageOutput.Role)

		// Second event should be the tool output
		event2, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event2)
		assert.Nil(t, event2.Err)
		assert.NotNil(t, event2.Output)
		assert.NotNil(t, event2.Output.MessageOutput)
		assert.Equal(t, schema.Tool, event2.Output.MessageOutput.Role)

		// Third event should be the final model output
		event3, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event3)
		assert.Nil(t, event3.Err)
		assert.NotNil(t, event3.Output)
		assert.NotNil(t, event3.Output.MessageOutput)
		assert.Equal(t, schema.Assistant, event3.Output.MessageOutput.Role)

		// No more events
		_, ok = iterator.Next()
		assert.False(t, ok)
	})
}

// TestExitTool tests the Exit tool functionality
func TestExitTool(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock chat model
	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	// Set up expectations for the mock model
	// First call: model generates a message with Exit tool call
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("I'll exit with a final result",
			[]schema.ToolCall{
				{
					ID: "tool-call-1",
					Function: schema.FunctionCall{
						Name:      "exit",
						Arguments: `{"final_result": "This is the final result"}`},
				},
			}), nil).
		Times(1)

	// Model should implement WithTools
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	// Create an agent with the Exit tool
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent with Exit tool",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		Exit:        &ExitTool{},
	})
	assert.NoError(t, err)
	assert.NotNil(t, agent)

	// Run the agent
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Please exit with a final result"),
		},
	}
	iterator := agent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// First event: model output with tool call
	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event1)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.MessageOutput)
	assert.Equal(t, schema.Assistant, event1.Output.MessageOutput.Role)

	// Second event: tool output (Exit)
	event2, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event2)
	assert.Nil(t, event2.Err)
	assert.NotNil(t, event2.Output)
	assert.NotNil(t, event2.Output.MessageOutput)
	assert.Equal(t, schema.Tool, event2.Output.MessageOutput.Role)

	// Verify the action is Exit
	assert.NotNil(t, event2.Action)
	assert.True(t, event2.Action.Exit)

	// Verify the final result
	assert.Equal(t, "This is the final result", event2.Output.MessageOutput.Message.Content)

	// No more events
	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestParallelReturnDirectlyToolCall(t *testing.T) {
	ctx := context.Background()
	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock chat model
	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	// Set up expectations for the mock model
	// First call: model generates a message with Exit tool call
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("I'll exit with a final result",
			[]schema.ToolCall{
				{
					ID:       "tool-call-1",
					Function: schema.FunctionCall{Name: "tool1"},
				},
				{
					ID:       "tool-call-2",
					Function: schema.FunctionCall{Name: "tool2"},
				},
				{
					ID:       "tool-call-3",
					Function: schema.FunctionCall{Name: "tool3"},
				},
			}), nil).
		Times(1)

	// Model should implement WithTools
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	// Create an agent with the Exit tool
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent with Exit tool",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{
					&myTool{name: "tool1", desc: "tool1", waitTime: time.Millisecond},
					&myTool{name: "tool2", desc: "tool2", waitTime: 10 * time.Millisecond},
					&myTool{name: "tool3", desc: "tool3", waitTime: 100 * time.Millisecond},
				},
			},
			ReturnDirectly: map[string]bool{
				"tool1": true,
			},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, agent)

	r := NewRunner(ctx, RunnerConfig{
		Agent: agent,
	})
	iter := r.Query(ctx, "")
	times := 0
	for {
		e, ok := iter.Next()
		if !ok {
			assert.Equal(t, 4, times)
			break
		}
		if times == 3 {
			assert.Equal(t, "tool1", e.Output.MessageOutput.Message.ToolName)
		}
		times++
	}
}

type myTool struct {
	name     string
	desc     string
	waitTime time.Duration
}

func (m *myTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: m.name,
		Desc: m.desc,
	}, nil
}

func (m *myTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	time.Sleep(m.waitTime)
	return "success", nil
}
