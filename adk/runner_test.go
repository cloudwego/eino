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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

// mockRunnerAgent is a simple implementation of the Agent interface for testing Runner
type mockRunnerAgent struct {
	name        string
	description string
	responses   []*AgentEvent
	// Track calls to verify correct parameters were passed
	callCount       int
	lastInput       *AgentInput
	enableStreaming bool
}

func (a *mockRunnerAgent) Name(_ context.Context) string {
	return a.name
}

func (a *mockRunnerAgent) Description(_ context.Context) string {
	return a.description
}

func (a *mockRunnerAgent) Run(_ context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	// Record the call details for verification
	a.callCount++
	a.lastInput = input
	a.enableStreaming = input.EnableStreaming

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	go func() {
		defer generator.Close()

		for _, event := range a.responses {
			generator.Send(event)

			// If the event has an Exit action, stop sending events
			if event.Action != nil && event.Action.Exit {
				break
			}
		}
	}()

	return iterator
}

func newMockRunnerAgent(name, description string, responses []*AgentEvent) *mockRunnerAgent {
	return &mockRunnerAgent{
		name:        name,
		description: description,
		responses:   responses,
	}
}

func TestNewRunner(t *testing.T) {
	ctx := context.Background()
	config := RunnerConfig{}

	runner := NewRunner(ctx, config)

	// Verify that a non-nil runner is returned
	assert.NotNil(t, runner)
}

func TestRunner_Run(t *testing.T) {
	ctx := context.Background()

	// Create a mock agent with predefined responses
	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent for Runner", []*AgentEvent{
		{
			AgentName: "TestAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response from test agent", nil),
					Role:        schema.Assistant,
				},
			}},
	})

	// Create a runner
	runner := NewRunner(ctx, RunnerConfig{Agent: mockAgent_})

	// Create test messages
	msgs := []Message{
		schema.UserMessage("Hello, agent!"),
	}

	// Test Run method without streaming
	iterator := runner.Run(ctx, msgs)

	// Verify that the agent's Run method was called with the correct parameters
	assert.Equal(t, 1, mockAgent_.callCount)
	assert.Equal(t, msgs, mockAgent_.lastInput.Messages)
	assert.False(t, mockAgent_.enableStreaming)

	// Verify that we can get the expected response from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "TestAgent", event.AgentName)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.NotNil(t, event.Output.MessageOutput.Message)
	assert.Equal(t, "Response from test agent", event.Output.MessageOutput.Message.Content)

	// Verify that the iterator is now closed
	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestRunner_Run_WithStreaming(t *testing.T) {
	ctx := context.Background()

	// Create a mock agent with predefined responses
	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent for Runner", []*AgentEvent{
		{
			AgentName: "TestAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					Message:       nil,
					MessageStream: schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("Streaming response", nil)}),
					Role:          schema.Assistant,
				},
			}},
	})

	// Create a runner
	runner := NewRunner(ctx, RunnerConfig{EnableStreaming: true, Agent: mockAgent_})

	// Create test messages
	msgs := []Message{
		schema.UserMessage("Hello, agent!"),
	}

	// Test Run method with streaming enabled
	iterator := runner.Run(ctx, msgs)

	// Verify that the agent's Run method was called with the correct parameters
	assert.Equal(t, 1, mockAgent_.callCount)
	assert.Equal(t, msgs, mockAgent_.lastInput.Messages)
	assert.True(t, mockAgent_.enableStreaming)

	// Verify that we can get the expected response from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "TestAgent", event.AgentName)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.True(t, event.Output.MessageOutput.IsStreaming)

	// Verify that the iterator is now closed
	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestRunner_Query(t *testing.T) {
	ctx := context.Background()

	// Create a mock agent with predefined responses
	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent for Runner", []*AgentEvent{
		{
			AgentName: "TestAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response to query", nil),
					Role:        schema.Assistant,
				},
			}},
	})

	// Create a runner
	runner := NewRunner(ctx, RunnerConfig{Agent: mockAgent_})

	// Test Query method
	iterator := runner.Query(ctx, "Test query")

	// Verify that the agent's Run method was called with the correct parameters
	assert.Equal(t, 1, mockAgent_.callCount)
	assert.Equal(t, 1, len(mockAgent_.lastInput.Messages))
	assert.Equal(t, "Test query", mockAgent_.lastInput.Messages[0].Content)
	assert.False(t, mockAgent_.enableStreaming)

	// Verify that we can get the expected response from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "TestAgent", event.AgentName)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.NotNil(t, event.Output.MessageOutput.Message)
	assert.Equal(t, "Response to query", event.Output.MessageOutput.Message.Content)

	// Verify that the iterator is now closed
	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestRunner_Query_WithStreaming(t *testing.T) {
	ctx := context.Background()

	// Create a mock agent with predefined responses
	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent for Runner", []*AgentEvent{
		{
			AgentName: "TestAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					Message:       nil,
					MessageStream: schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("Streaming query response", nil)}),
					Role:          schema.Assistant,
				},
			}},
	})

	// Create a runner
	runner := NewRunner(ctx, RunnerConfig{EnableStreaming: true, Agent: mockAgent_})

	// Test Query method with streaming enabled
	iterator := runner.Query(ctx, "Test query")

	// Verify that the agent's Run method was called with the correct parameters
	assert.Equal(t, 1, mockAgent_.callCount)
	assert.Equal(t, 1, len(mockAgent_.lastInput.Messages))
	assert.Equal(t, "Test query", mockAgent_.lastInput.Messages[0].Content)
	assert.True(t, mockAgent_.enableStreaming)

	// Verify that we can get the expected response from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "TestAgent", event.AgentName)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.True(t, event.Output.MessageOutput.IsStreaming)

	// Verify that the iterator is now closed
	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestRunner_Middleware_Run(t *testing.T) {
	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent for Runner", []*AgentEvent{
		{
			AgentName: "TestAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response from test agent", nil),
					Role:        schema.Assistant,
				},
			}},
	})

	var n *AsyncIterator[*AgentEvent] = nil

	mw1 := func(re RunnerEndpoint) RunnerEndpoint {
		return func(ctx context.Context, messages []Message, resp *AsyncIterator[*AgentEvent], opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.Equal(t, n, resp)
			ctx = context.WithValue(ctx, "mw1", "hello")
			messages = []Message{schema.UserMessage("test")}

			nextResp := re(ctx, messages, resp)
			for {
				event, ok := nextResp.Next()
				if !ok {
					break
				}
				assert.NotNil(t, event.Action.CustomizedAction)
			}
			i, g := NewAsyncIteratorPair[*AgentEvent]()
			g.Send(&AgentEvent{
				Action: NewExitAction(),
			})
			g.Close()
			return i
		}
	}

	mw2 := func(re RunnerEndpoint) RunnerEndpoint {
		return func(ctx context.Context, messages []Message, resp *AsyncIterator[*AgentEvent], opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.Equal(t, n, resp)
			assert.Equal(t, []Message{schema.UserMessage("test")}, messages)
			assert.Equal(t, "hello", ctx.Value("mw1"))

			nextResp := re(ctx, messages, resp)
			for {
				event, ok := nextResp.Next()
				if !ok {
					break
				}
				assert.Equal(t, schema.AssistantMessage("Response from test agent", nil), event.Output.MessageOutput.Message)
			}

			i, g := NewAsyncIteratorPair[*AgentEvent]()
			g.Send(&AgentEvent{
				Action: &AgentAction{
					CustomizedAction: "hello",
				},
			})
			g.Close()
			return i
		}
	}

	runner := NewRunner(context.Background(), RunnerConfig{
		Agent:           mockAgent_,
		EnableStreaming: true,
		CheckPointStore: nil,
		Middlewares:     []RunnerMiddleware{mw1, mw2},
	})

	resp := runner.Run(context.Background(), []Message{schema.UserMessage("not_test")})
	for {
		event, ok := resp.Next()
		if !ok {
			break
		}
		assert.Equal(t, true, event.Action.Exit)
	}
}
