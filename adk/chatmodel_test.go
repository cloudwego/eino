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
			Msgs: []Message{
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
		assert.NotNil(t, event.Output.ModelResponse)

		// Verify the message content
		msg, err := event.Output.ModelResponse.Response.GetMessage()
		assert.NoError(t, err)
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
			Msgs:            []Message{schema.UserMessage("Hello, who are you?")},
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
		assert.NotNil(t, event.Output.ModelResponse)
		assert.True(t, event.Output.ModelResponse.Response.IsStreaming)

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
			Msgs: []Message{schema.UserMessage("Hello, who are you?")},
		}
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		// Get the event from the iterator, should contain an error
		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event)
		assert.NotNil(t, event.Err)
		assert.Equal(t, "model error", event.Err.Error())

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
			Msgs: []Message{schema.UserMessage("Use the test tool")},
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
		assert.NotNil(t, event1.Output.ModelResponse)

		// Second event should be the tool output
		event2, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event2)
		assert.Nil(t, event2.Err)
		assert.NotNil(t, event2.Output)
		assert.NotNil(t, event2.Output.ToolCallResponse)

		// Third event should be the final model output
		event3, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event3)
		assert.Nil(t, event3.Err)
		assert.NotNil(t, event3.Output)
		assert.NotNil(t, event3.Output.ModelResponse)

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
		Msgs: []Message{
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
	assert.NotNil(t, event1.Output.ModelResponse)

	// Second event: tool output (Exit)
	event2, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event2)
	assert.Nil(t, event2.Err)
	assert.NotNil(t, event2.Output)
	assert.NotNil(t, event2.Output.ToolCallResponse)

	// Verify the action is Exit
	assert.NotNil(t, event2.Action)
	assert.True(t, event2.Action.Exit)

	// Verify the final result
	assert.Equal(t, "This is the final result", event2.Output.ToolCallResponse.Response.Msg.Content)

	// No more events
	_, ok = iterator.Next()
	assert.False(t, ok)
}
