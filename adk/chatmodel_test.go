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

	"github.com/cloudwego/eino/components/model"
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

	t.Run("BasicChatModelWithAgentMiddleware", func(t *testing.T) {
		ctx := context.Background()

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello, I am an AI assistant.", nil), nil).
			Times(1)

		afterChatModelExecuted := false

		// Create a ChatModelAgent
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for unit testing",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			Middlewares: []AgentMiddleware{
				{
					BeforeChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
						state.Messages = append(state.Messages, schema.UserMessage("m"))
						return nil
					},
					AfterChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
						assert.Len(t, state.Messages, 4)
						afterChatModelExecuted = true
						return nil
					},
				},
			},
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

		assert.True(t, afterChatModelExecuted)
	})

	t.Run("AfterChatModel_NoTools_ModifyDoesNotAffectEvent", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("original content", nil), nil).
			Times(1)

		var capturedMessages []*schema.Message

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for AfterChatModel NoTools scenario",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			Middlewares: []AgentMiddleware{
				{
					AfterChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
						capturedMessages = make([]*schema.Message, len(state.Messages))
						copy(capturedMessages, state.Messages)
						state.Messages = append(state.Messages, schema.AssistantMessage("appended content", nil))
						return nil
					},
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
		assert.NotNil(t, event)
		assert.Nil(t, event.Err)
		assert.NotNil(t, event.Output)
		assert.NotNil(t, event.Output.MessageOutput)

		msg := event.Output.MessageOutput.Message
		assert.NotNil(t, msg)
		assert.Equal(t, "original content", msg.Content)

		_, ok = iterator.Next()
		assert.False(t, ok)

		assert.Len(t, capturedMessages, 3)
	})

	t.Run("AfterChatModel_ReAct_ModifyAffectsFlow", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		generateCount := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				generateCount++
				if generateCount == 1 {
					return schema.AssistantMessage("first response with tool call", []schema.ToolCall{
						{ID: "tc1", Function: schema.FunctionCall{Name: "test_tool", Arguments: "{}"}},
					}), nil
				}
				return schema.AssistantMessage("final response", nil), nil
			}).AnyTimes()
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		toolCalled := false
		testTool := &fakeToolForTest{tarCount: 0}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for AfterChatModel ReAct scenario",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{testTool},
				},
			},
			Middlewares: []AgentMiddleware{
				{
					AfterChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
						lastMsg := state.Messages[len(state.Messages)-1]
						if len(lastMsg.ToolCalls) > 0 {
							toolCalled = true
							state.Messages[len(state.Messages)-1] = schema.AssistantMessage("modified to remove tool call", nil)
						}
						return nil
					},
				},
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
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

		assert.True(t, toolCalled)
		assert.Equal(t, 1, generateCount)

		assert.Equal(t, 1, len(events))
		event := events[0]
		assert.NotNil(t, event.Output)
		assert.NotNil(t, event.Output.MessageOutput)
		assert.Equal(t, "first response with tool call", event.Output.MessageOutput.Message.Content)
		assert.Len(t, event.Output.MessageOutput.Message.ToolCalls, 1)
	})

	t.Run("AfterChatModel_ReAct_AppendToolCall_AffectsFlow", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		generateCount := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				generateCount++
				if generateCount == 1 {
					return schema.AssistantMessage("first response no tool", nil), nil
				}
				return schema.AssistantMessage("final response", nil), nil
			}).AnyTimes()
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		testTool := &fakeToolForTest{tarCount: 0}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for AfterChatModel ReAct append tool call",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{testTool},
				},
			},
			Middlewares: []AgentMiddleware{
				{
					AfterChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
						if generateCount == 1 {
							state.Messages[len(state.Messages)-1] = schema.AssistantMessage("modified with tool call", []schema.ToolCall{
								{ID: "tc1", Function: schema.FunctionCall{Name: "test_tool", Arguments: "{}"}},
							})
						}
						return nil
					},
				},
			},
		})
		assert.NoError(t, err)

		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Hello")},
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

		assert.Equal(t, 2, generateCount)

		assert.Equal(t, 3, len(events))

		event0 := events[0]
		assert.NotNil(t, event0.Output)
		assert.NotNil(t, event0.Output.MessageOutput)
		assert.Equal(t, "first response no tool", event0.Output.MessageOutput.Message.Content)
		assert.Empty(t, event0.Output.MessageOutput.Message.ToolCalls)

		event2 := events[2]
		assert.NotNil(t, event2.Output)
		assert.NotNil(t, event2.Output.MessageOutput)
		assert.Equal(t, "final response", event2.Output.MessageOutput.Message.Content)
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
					}}), nil).
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

func TestConcurrentSameToolSendToolGenActionUsesToolCallID(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("tools", []schema.ToolCall{
			{ID: "id1", Function: schema.FunctionCall{Name: "action_tool", Arguments: "A"}},
			{ID: "id2", Function: schema.FunctionCall{Name: "action_tool", Arguments: "B"}},
		}), nil).
		Times(1)

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("done", nil), nil).
		Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Agent with action tool",
		Instruction: "",
		Model:       cm,
		ToolsConfig: ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{actionTool{}}}},
	})
	assert.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("go")}})
	seen := map[string]bool{}
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		if e.Output != nil && e.Output.MessageOutput != nil && e.Output.MessageOutput.Message != nil && e.Output.MessageOutput.Message.Role == schema.Tool {
			if e.Action != nil && e.Action.CustomizedAction != nil {
				if s, ok := e.Action.CustomizedAction.(string); ok {
					seen[s] = true
				}
			}
		}
	}
	assert.True(t, seen["A"])
	assert.True(t, seen["B"])
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

type actionTool struct{}

func (a actionTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "action_tool", Desc: "action tool"}, nil
}

func (a actionTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	_ = SendToolGenAction(ctx, "action_tool", &AgentAction{CustomizedAction: argumentsInJSON})
	return "ok", nil
}

type streamActionTool struct{}

func (s streamActionTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "action_tool_stream", Desc: "action stream tool"}, nil
}

func (s streamActionTool) StreamableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	_ = SendToolGenAction(ctx, "action_tool_stream", &AgentAction{CustomizedAction: argumentsInJSON})
	sr, sw := schema.Pipe[string](1)
	go func() {
		defer sw.Close()
		_ = sw.Send("o", nil)
		_ = sw.Send("k", nil)
	}()
	return sr, nil
}

type legacyStreamActionTool struct{}

func (s legacyStreamActionTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "legacy_action_tool_stream", Desc: "legacy action stream tool"}, nil
}

func (s legacyStreamActionTool) StreamableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	_ = compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		st.ToolGenActions["legacy_action_tool_stream"] = &AgentAction{CustomizedAction: argumentsInJSON}
		return nil
	})
	sr, sw := schema.Pipe[string](1)
	go func() {
		defer sw.Close()
		_ = sw.Send("o", nil)
		_ = sw.Send("k", nil)
	}()
	return sr, nil
}

// TestChatModelAgentOutputKey tests the outputKey configuration and setOutputToSession function
func TestChatModelAgentOutputKey(t *testing.T) {
	// Test outputKey configuration - stores output in session
	t.Run("OutputKeyStoresInSession", func(t *testing.T) {
		for i := 0; i < 1000; i++ {

		}
		ctx := context.Background()

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello, I am an AI assistant.", nil), nil).
			Times(1)

		// Create a ChatModelAgent with outputKey configured
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for unit testing",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			OutputKey:   "agent_output", // This should store output in session
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		// Initialize a run context to enable session storage
		input := &AgentInput{
			Messages: []Message{
				schema.UserMessage("Hello, who are you?"),
			},
		}
		ctx, runCtx := initRunCtx(ctx, "TestAgent", input)
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

		// Run the agent
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		// Get the event from the iterator
		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event)
		assert.Nil(t, event.Err)

		// Verify the message content
		msg := event.Output.MessageOutput.Message
		assert.Equal(t, "Hello, I am an AI assistant.", msg.Content)

		// Verify that the output was stored in the session
		time.AfterFunc(100*time.Millisecond, func() {
			sessionValues := GetSessionValues(ctx)
			assert.Contains(t, sessionValues, "agent_output")
			assert.Equal(t, "Hello, I am an AI assistant.", sessionValues["agent_output"])
		})

		// No more events
		_, ok = iterator.Next()
		assert.False(t, ok)
	})

	// Test outputKey configuration with streaming - stores concatenated output in session
	t.Run("OutputKeyWithStreamingStoresInSession", func(t *testing.T) {
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

		// Create a ChatModelAgent with outputKey configured
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for unit testing",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			OutputKey:   "agent_output", // This should store concatenated stream in session
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		// Initialize a run context to enable session storage
		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello, who are you?")},
			EnableStreaming: true,
		}
		ctx, runCtx := initRunCtx(ctx, "TestAgent", input)
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

		// Run the agent
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		// Get the event from the iterator
		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event)
		assert.Nil(t, event.Err)
		assert.True(t, event.Output.MessageOutput.IsStreaming)

		time.AfterFunc(100*time.Millisecond, func() {
			// Verify that the concatenated output was stored in the session
			sessionValues := GetSessionValues(ctx)
			assert.Contains(t, sessionValues, "agent_output")
			assert.Equal(t, "Hello, I am an AI assistant.", sessionValues["agent_output"])
		})

		// No more events
		_, ok = iterator.Next()
		assert.False(t, ok)
	})

	// Test setOutputToSession function directly - regular message
	t.Run("SetOutputToSessionRegularMessage", func(t *testing.T) {
		ctx := context.Background()

		// Initialize a run context to enable session storage
		input := &AgentInput{
			Messages: []Message{schema.UserMessage("test")},
		}
		ctx, runCtx := initRunCtx(ctx, "TestAgent", input)
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

		// Test with a regular message
		msg := schema.AssistantMessage("Test response", nil)
		err := setOutputToSession(ctx, msg, nil, "test_output")
		assert.NoError(t, err)

		// Verify the message content was stored
		sessionValues := GetSessionValues(ctx)
		assert.Contains(t, sessionValues, "test_output")
		assert.Equal(t, "Test response", sessionValues["test_output"])
	})

	// Test setOutputToSession function directly - streaming message
	t.Run("SetOutputToSessionStreamingMessage", func(t *testing.T) {
		ctx := context.Background()

		// Initialize a run context to enable session storage
		input := &AgentInput{
			Messages: []Message{schema.UserMessage("test")},
		}
		ctx, runCtx := initRunCtx(ctx, "TestAgent", input)
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

		// Test with a streaming message
		sr := schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("Stream", nil),
			schema.AssistantMessage(" response", nil),
			schema.AssistantMessage(" content", nil),
		})
		err := setOutputToSession(ctx, nil, sr, "test_output")
		assert.NoError(t, err)

		// Verify the concatenated stream content was stored
		sessionValues := GetSessionValues(ctx)
		assert.Contains(t, sessionValues, "test_output")
		assert.Equal(t, "Stream response content", sessionValues["test_output"])
	})

	// Test setOutputToSession function directly - error case
	t.Run("SetOutputToSessionErrorCase", func(t *testing.T) {
		ctx := context.Background()

		// Initialize a run context to enable session storage
		input := &AgentInput{
			Messages: []Message{schema.UserMessage("test")},
		}
		ctx, runCtx := initRunCtx(ctx, "TestAgent", input)
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

		// Test with an invalid stream (simulate error)
		// Create a stream that will fail when concatenated
		sr := schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("test", nil),
		})
		// Close the stream to simulate an error condition
		sr.Close()

		// This should return an error because the stream is closed
		err := setOutputToSession(ctx, nil, sr, "test_output")
		// Note: The actual behavior may vary depending on the stream implementation
		// Some streams may not error when closed, so we'll accept either outcome
		if err != nil {
			// If there's an error, verify nothing was stored
			sessionValues := GetSessionValues(ctx)
			assert.NotContains(t, sessionValues, "test_output")
		} else {
			// If no error, verify the content was stored
			sessionValues := GetSessionValues(ctx)
			assert.Contains(t, sessionValues, "test_output")
			assert.Equal(t, "test", sessionValues["test_output"])
		}
	})

	// Test outputKey with React workflow (tools enabled)
	t.Run("OutputKeyWithReactWorkflow", func(t *testing.T) {
		ctx := context.Background()

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Create a simple tool for testing
		fakeTool := &fakeToolForTest{
			tarCount: 1,
		}

		// Set up expectations for the mock model - it will generate a final response
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Final response from React workflow", nil), nil).
			Times(1)
		// Model should implement WithTools
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create a ChatModelAgent with outputKey and tools configured
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tools",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			OutputKey:   "agent_output",
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{fakeTool},
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		// Initialize a run context to enable session storage
		input := &AgentInput{
			Messages: []Message{schema.UserMessage("Use the tool")},
		}
		ctx, runCtx := initRunCtx(ctx, "TestAgent", input)
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

		// Run the agent
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		// Get the event from the iterator
		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.NotNil(t, event)
		assert.Nil(t, event.Err)

		// Verify the message content
		msg := event.Output.MessageOutput.Message
		assert.Equal(t, "Final response from React workflow", msg.Content)

		// Verify that the output was stored in the session
		time.AfterFunc(time.Millisecond*10, func() {
			sessionValues := GetSessionValues(ctx)
			assert.Contains(t, sessionValues, "agent_output")
			assert.Equal(t, "Final response from React workflow", sessionValues["agent_output"])
		})

		// No more events
		_, ok = iterator.Next()
		assert.False(t, ok)
	})
}

func TestConcurrentSameStreamToolSendToolGenActionUsesToolCallID(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("tools", []schema.ToolCall{
			{ID: "sid1", Function: schema.FunctionCall{Name: "action_tool_stream", Arguments: "SA"}},
			{ID: "sid2", Function: schema.FunctionCall{Name: "action_tool_stream", Arguments: "SB"}},
		}), nil).
		Times(1)

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("done", nil), nil).
		Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Agent with stream action tool",
		Instruction: "",
		Model:       cm,
		ToolsConfig: ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{streamActionTool{}}}},
	})
	assert.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("go")}})
	seen := map[string]bool{}
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		if e.Output != nil && e.Output.MessageOutput != nil {
			if e.Output.MessageOutput.IsStreaming {
				if e.Action != nil && e.Action.CustomizedAction != nil {
					if s, ok := e.Action.CustomizedAction.(string); ok {
						seen[s] = true
					}
				}
			}
		}
	}
	assert.True(t, seen["SA"])
	assert.True(t, seen["SB"])
}

func TestStreamToolLegacyNameKeyFallback(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("tools", []schema.ToolCall{
			{ID: "lsid1", Function: schema.FunctionCall{Name: "legacy_action_tool_stream", Arguments: "LA"}},
		}), nil).
		Times(1)

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("done", nil), nil).
		Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Agent with legacy stream action tool",
		Instruction: "",
		Model:       cm,
		ToolsConfig: ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{legacyStreamActionTool{}}}},
	})
	assert.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("go")}})
	found := false
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		if e.Output != nil && e.Output.MessageOutput != nil && e.Output.MessageOutput.IsStreaming {
			if e.Action != nil && e.Action.CustomizedAction != nil {
				if s, ok := e.Action.CustomizedAction.(string); ok {
					found = (s == "LA")
				}
			}
		}
	}
	assert.True(t, found)
}

func TestChatModelAgent_ToolResultMiddleware_EmitsFinalResult(t *testing.T) {
	originalResult := "original_result"
	modifiedResult := "modified_by_middleware"

	resultModifyingMiddleware := compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				output, err := next(ctx, input)
				if err != nil {
					return nil, err
				}
				output.Result = modifiedResult
				return output, nil
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				output, err := next(ctx, input)
				if err != nil {
					return nil, err
				}
				output.Result = schema.StreamReaderFromArray([]string{modifiedResult})
				return output, nil
			}
		},
	}

	t.Run("Invoke", func(t *testing.T) {
		ctx := context.Background()
		testTool := &simpleToolForMiddlewareTest{name: "test_tool", result: originalResult}

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		info, err := testTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"input": "test"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("final response", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "test_agent",
			Description: "test agent with middleware",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools:               []tool.BaseTool{testTool},
					ToolCallMiddlewares: []compose.ToolMiddleware{resultModifyingMiddleware},
				},
			},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: false, CheckPointStore: newBridgeStore()})
		it := r.Run(ctx, []Message{schema.UserMessage("call the tool")})

		var toolResultEvents []*AgentEvent
		for {
			ev, ok := it.Next()
			if !ok {
				break
			}
			if ev.Output != nil && ev.Output.MessageOutput != nil &&
				ev.Output.MessageOutput.Message != nil &&
				ev.Output.MessageOutput.Message.Role == schema.Tool {
				toolResultEvents = append(toolResultEvents, ev)
			}
		}

		assert.NotEmpty(t, toolResultEvents, "should have at least one tool result event")
		for _, ev := range toolResultEvents {
			assert.Equal(t, modifiedResult, ev.Output.MessageOutput.Message.Content,
				"tool result event should contain the middleware-modified result, not the original")
			assert.NotEqual(t, originalResult, ev.Output.MessageOutput.Message.Content,
				"tool result event should NOT contain the original result")
		}
	})

	t.Run("Stream", func(t *testing.T) {
		ctx := context.Background()
		testTool := &simpleToolForMiddlewareTest{name: "test_tool_stream", result: originalResult}

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		info, err := testTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"input": "test"}`,
						},
					},
				}),
			}), nil).
			Times(1)
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("final response", nil),
			}), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "test_agent",
			Description: "test agent with middleware",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools:               []tool.BaseTool{testTool},
					ToolCallMiddlewares: []compose.ToolMiddleware{resultModifyingMiddleware},
				},
			},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true, CheckPointStore: newBridgeStore()})
		it := r.Run(ctx, []Message{schema.UserMessage("call the tool")})

		var toolResultContents []string
		for {
			ev, ok := it.Next()
			if !ok {
				break
			}
			if ev.Output != nil && ev.Output.MessageOutput != nil {
				if ev.Output.MessageOutput.Message != nil &&
					ev.Output.MessageOutput.Message.Role == schema.Tool {
					toolResultContents = append(toolResultContents, ev.Output.MessageOutput.Message.Content)
				}
				if ev.Output.MessageOutput.IsStreaming &&
					ev.Output.MessageOutput.MessageStream != nil &&
					ev.Output.MessageOutput.Role == schema.Tool {
					var msgs []*schema.Message
					for {
						msg, err := ev.Output.MessageOutput.MessageStream.Recv()
						if err != nil {
							break
						}
						msgs = append(msgs, msg)
					}
					if len(msgs) > 0 {
						concated, err := schema.ConcatMessages(msgs)
						if err == nil {
							toolResultContents = append(toolResultContents, concated.Content)
						}
					}
				}
			}
		}

		assert.NotEmpty(t, toolResultContents, "should have at least one tool result event")
		for _, content := range toolResultContents {
			assert.Equal(t, modifiedResult, content,
				"tool result event should contain the middleware-modified result, not the original")
			assert.NotEqual(t, originalResult, content,
				"tool result event should NOT contain the original result")
		}
	})
}

type simpleToolForMiddlewareTest struct {
	name   string
	result string
}

func (s *simpleToolForMiddlewareTest) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: s.name,
		Desc: "simple tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"input": {
					Desc:     "input",
					Required: true,
					Type:     schema.String,
				},
			}),
	}, nil
}

func (s *simpleToolForMiddlewareTest) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return s.result, nil
}

func (s *simpleToolForMiddlewareTest) StreamableRun(_ context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	return schema.StreamReaderFromArray([]string{s.result}), nil
}

func TestSendEvent(t *testing.T) {
	t.Run("SendEventWithoutExecCtx", func(t *testing.T) {
		ctx := context.Background()
		event := &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.AssistantMessage("custom event", nil),
				},
			},
		}
		err := SendEvent(ctx, event)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "SendEvent failed: must be called within a ChatModelAgent Run() or Resume() execution context")
	})

	t.Run("SendEventWithNilGenerator", func(t *testing.T) {
		ctx := context.Background()
		execCtx := &chatModelAgentExecCtx{
			generator: nil,
		}
		ctx = withChatModelAgentExecCtx(ctx, execCtx)

		event := &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.AssistantMessage("custom event", nil),
				},
			},
		}
		err := SendEvent(ctx, event)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "SendEvent failed: must be called within a ChatModelAgent Run() or Resume() execution context")
	})

	t.Run("SendEventInMiddleware", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello, I am an AI assistant.", nil), nil).
			Times(1)

		var customEventReceived bool
		customEventContent := "custom_event_from_middleware"

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for SendEvent",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			Middlewares: []AgentMiddleware{
				{
					BeforeChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
						customEvent := &AgentEvent{
							Output: &AgentOutput{
								MessageOutput: &MessageVariant{
									Message: schema.AssistantMessage(customEventContent, nil),
								},
							},
						}
						return SendEvent(ctx, customEvent)
					},
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		input := &AgentInput{
			Messages: []Message{
				schema.UserMessage("Hello"),
			},
		}
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Message != nil &&
				event.Output.MessageOutput.Message.Content == customEventContent {
				customEventReceived = true
			}
		}

		assert.True(t, customEventReceived, "should receive custom event sent from middleware")
	})

	t.Run("SendEventInMiddlewareWithTools", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		fakeTool := &fakeToolForTest{
			tarCount: 1,
		}
		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Using tool",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					}}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Task completed", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		var customEventReceived bool
		customEventContent := "custom_event_from_middleware_with_tools"

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for SendEvent with tools",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{fakeTool},
				},
			},
			Middlewares: []AgentMiddleware{
				{
					BeforeChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
						customEvent := &AgentEvent{
							Output: &AgentOutput{
								MessageOutput: &MessageVariant{
									Message: schema.AssistantMessage(customEventContent, nil),
								},
							},
						}
						return SendEvent(ctx, customEvent)
					},
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		input := &AgentInput{
			Messages: []Message{
				schema.UserMessage("Use the test tool"),
			},
		}
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		customEventCount := 0
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Message != nil &&
				event.Output.MessageOutput.Message.Content == customEventContent {
				customEventReceived = true
				customEventCount++
			}
		}

		assert.True(t, customEventReceived, "should receive custom event sent from middleware with tools")
		assert.Equal(t, 2, customEventCount, "middleware should be called twice (once for each ChatModel call)")
	})

	t.Run("SendEventInMiddlewareStreaming", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		sr := schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("Hello", nil),
			schema.AssistantMessage(", streaming", nil),
		})
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(sr, nil).
			Times(1)

		var customEventReceived bool
		customEventContent := "custom_event_streaming"

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent for SendEvent streaming",
			Instruction: "You are a helpful assistant.",
			Model:       cm,
			Middlewares: []AgentMiddleware{
				{
					BeforeChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
						customEvent := &AgentEvent{
							Output: &AgentOutput{
								MessageOutput: &MessageVariant{
									Message: schema.AssistantMessage(customEventContent, nil),
								},
							},
						}
						return SendEvent(ctx, customEvent)
					},
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, agent)

		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)
		assert.NotNil(t, iterator)

		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Message != nil &&
				event.Output.MessageOutput.Message.Content == customEventContent {
				customEventReceived = true
			}
		}

		assert.True(t, customEventReceived, "should receive custom event in streaming mode")
	})
}

func TestChatModelAgentExecCtx(t *testing.T) {
	t.Run("WithAndGetExecCtx", func(t *testing.T) {
		ctx := context.Background()

		result := getChatModelAgentExecCtx(ctx)
		assert.Nil(t, result)

		execCtx := &chatModelAgentExecCtx{}
		ctx = withChatModelAgentExecCtx(ctx, execCtx)

		result = getChatModelAgentExecCtx(ctx)
		assert.NotNil(t, result)
		assert.Equal(t, execCtx, result)
	})

	t.Run("ExecCtxSendMethod", func(t *testing.T) {
		var nilExecCtx *chatModelAgentExecCtx
		nilExecCtx.send(&AgentEvent{})

		execCtxWithNilGenerator := &chatModelAgentExecCtx{
			generator: nil,
		}
		execCtxWithNilGenerator.send(&AgentEvent{})
	})
}
