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

// mockAgent is a simple implementation of the Agent interface for testing
type mockAgent struct {
	name        string
	description string
	responses   []*AgentEvent
}

func (a *mockAgent) Name(_ context.Context) string {
	return a.name
}

func (a *mockAgent) Description(_ context.Context) string {
	return a.description
}

func (a *mockAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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

// newMockAgent creates a new mock agent with the given name, description, and responses
func newMockAgent(name, description string, responses []*AgentEvent) *mockAgent {
	return &mockAgent{
		name:        name,
		description: description,
		responses:   responses,
	}
}

// TestSequentialAgent tests the sequential workflow agent
func TestSequentialAgent(t *testing.T) {
	ctx := context.Background()

	// Create mock agents with predefined responses
	agent1 := newMockAgent("Agent1", "First agent", []*AgentEvent{
		{
			AgentName: "Agent1",
			Output: &AgentOutput{
				ModelResponse: &ModelOutput{
					Response: &MessageVariant{
						IsStreaming: false,
						Msg:         schema.AssistantMessage("Response from Agent1", nil),
					},
				},
			},
		},
	})

	agent2 := newMockAgent("Agent2", "Second agent", []*AgentEvent{
		{
			AgentName: "Agent2",
			Output: &AgentOutput{
				ModelResponse: &ModelOutput{
					Response: &MessageVariant{
						IsStreaming: false,
						Msg:         schema.AssistantMessage("Response from Agent2", nil),
					},
				},
			},
		},
	})

	// Create a sequential agent with the mock agents
	config := &SequentialAgentConfig{
		Name:        "SequentialTestAgent",
		Description: "Test sequential agent",
		SubAgents:   []Agent{agent1, agent2},
	}

	sequentialAgent, err := NewSequentialAgent(ctx, config)
	assert.NoError(t, err)
	assert.NotNil(t, sequentialAgent)

	// Run the sequential agent
	input := &AgentInput{
		Msgs: []Message{
			schema.UserMessage("Test input"),
		},
	}

	iterator := sequentialAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// First event should be from agent1
	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event1)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.ModelResponse)

	// Get the message content from agent1
	msg1, err := event1.Output.ModelResponse.Response.GetMessage()
	assert.NoError(t, err)
	assert.Equal(t, "Response from Agent1", msg1.Content)

	// Second event should be from agent2
	event2, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event2)
	assert.Nil(t, event2.Err)
	assert.NotNil(t, event2.Output)
	assert.NotNil(t, event2.Output.ModelResponse)

	// Get the message content from agent2
	msg2, err := event2.Output.ModelResponse.Response.GetMessage()
	assert.NoError(t, err)
	assert.Equal(t, "Response from Agent2", msg2.Content)

	// No more events
	_, ok = iterator.Next()
	assert.False(t, ok)
}

// TestSequentialAgentWithExit tests the sequential workflow agent with an exit action
func TestSequentialAgentWithExit(t *testing.T) {
	ctx := context.Background()

	// Create mock agents with predefined responses
	agent1 := newMockAgent("Agent1", "First agent", []*AgentEvent{
		{
			AgentName: "Agent1",
			Output: &AgentOutput{
				ModelResponse: &ModelOutput{
					Response: &MessageVariant{
						IsStreaming: false,
						Msg:         schema.AssistantMessage("Response from Agent1", nil),
					},
				},
			},
			Action: &AgentAction{
				Exit: true,
			},
		},
	})

	agent2 := newMockAgent("Agent2", "Second agent", []*AgentEvent{
		{
			AgentName: "Agent2",
			Output: &AgentOutput{
				ModelResponse: &ModelOutput{
					Response: &MessageVariant{
						IsStreaming: false,
						Msg:         schema.AssistantMessage("Response from Agent2", nil),
					},
				},
			},
		},
	})

	// Create a sequential agent with the mock agents
	config := &SequentialAgentConfig{
		Name:        "SequentialTestAgent",
		Description: "Test sequential agent",
		SubAgents:   []Agent{agent1, agent2},
	}

	sequentialAgent, err := NewSequentialAgent(ctx, config)
	assert.NoError(t, err)
	assert.NotNil(t, sequentialAgent)

	// Run the sequential agent
	input := &AgentInput{
		Msgs: []Message{
			schema.UserMessage("Test input"),
		},
	}

	iterator := sequentialAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// First event should be from agent1 with exit action
	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event1)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.ModelResponse)
	assert.NotNil(t, event1.Action)
	assert.True(t, event1.Action.Exit)

	// No more events due to exit action
	_, ok = iterator.Next()
	assert.False(t, ok)
}

// TestParallelAgent tests the parallel workflow agent
func TestParallelAgent(t *testing.T) {
	ctx := context.Background()

	// Create mock agents with predefined responses
	agent1 := newMockAgent("Agent1", "First agent", []*AgentEvent{
		{
			AgentName: "Agent1",
			Output: &AgentOutput{
				ModelResponse: &ModelOutput{
					Response: &MessageVariant{
						IsStreaming: false,
						Msg:         schema.AssistantMessage("Response from Agent1", nil),
					},
				},
			},
		},
	})

	agent2 := newMockAgent("Agent2", "Second agent", []*AgentEvent{
		{
			AgentName: "Agent2",
			Output: &AgentOutput{
				ModelResponse: &ModelOutput{
					Response: &MessageVariant{
						IsStreaming: false,
						Msg:         schema.AssistantMessage("Response from Agent2", nil),
					},
				},
			},
		},
	})

	// Create a parallel agent with the mock agents
	config := &ParallelAgentConfig{

		Name:        "ParallelTestAgent",
		Description: "Test parallel agent",
		SubAgents:   []Agent{agent1, agent2},
	}

	parallelAgent, err := NewParallelAgent(ctx, config)
	assert.NoError(t, err)
	assert.NotNil(t, parallelAgent)

	// Run the parallel agent
	input := AgentInput{
		Msgs: []Message{
			schema.UserMessage("Test input"),
		},
	}

	iterator := parallelAgent.Run(ctx, &input)
	assert.NotNil(t, iterator)

	// Collect all events
	var events []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	// Should have two events, one from each agent
	assert.Equal(t, 2, len(events))

	// Verify the events
	for _, event := range events {
		assert.Nil(t, event.Err)
		assert.NotNil(t, event.Output)
		assert.NotNil(t, event.Output.ModelResponse)

		msg, err := event.Output.ModelResponse.Response.GetMessage()
		assert.NoError(t, err)

		// Check the source agent name and message content
		if event.AgentName == "Agent1" {
			assert.Equal(t, "Response from Agent1", msg.Content)
		} else if event.AgentName == "Agent2" {
			assert.Equal(t, "Response from Agent2", msg.Content)
		} else {
			t.Fatalf("Unexpected source agent name: %s", event.AgentName)
		}
	}
}

// TestLoopAgent tests the loop workflow agent
func TestLoopAgent(t *testing.T) {
	ctx := context.Background()

	// Create a mock agent that will be called multiple times
	agent := newMockAgent("LoopAgent", "Loop agent", []*AgentEvent{
		{
			AgentName: "LoopAgent",
			Output: &AgentOutput{
				ModelResponse: &ModelOutput{
					Response: &MessageVariant{
						IsStreaming: false,
						Msg:         schema.AssistantMessage("Loop iteration", nil),
					},
				},
			},
		},
	})

	// Create a loop agent with the mock agent and max iterations set to 3
	config := &LoopAgentConfig{
		Name:        "LoopTestAgent",
		Description: "Test loop agent",
		SubAgents:   []Agent{agent},

		MaxIterations: 3,
	}

	loopAgent, err := NewLoopAgent(ctx, config)
	assert.NoError(t, err)
	assert.NotNil(t, loopAgent)

	// Run the loop agent
	input := &AgentInput{
		Msgs: []Message{
			schema.UserMessage("Test input"),
		},
	}

	iterator := loopAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// Collect all events
	var events []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	// Should have 3 events (one for each iteration)
	assert.Equal(t, 3, len(events))

	// Verify all events
	for _, event := range events {
		assert.Nil(t, event.Err)
		assert.NotNil(t, event.Output)
		assert.NotNil(t, event.Output.ModelResponse)

		msg, err := event.Output.ModelResponse.Response.GetMessage()
		assert.NoError(t, err)
		assert.Equal(t, "Loop iteration", msg.Content)
	}
}

// TestLoopAgentWithExit tests the loop workflow agent with an exit action
func TestLoopAgentWithExit(t *testing.T) {
	ctx := context.Background()

	// Create a mock agent that will exit after the first iteration
	agent := newMockAgent("LoopAgent", "Loop agent", []*AgentEvent{
		{
			AgentName: "LoopAgent",
			Output: &AgentOutput{
				ModelResponse: &ModelOutput{
					Response: &MessageVariant{
						IsStreaming: false,
						Msg:         schema.AssistantMessage("Loop iteration with exit", nil),
					},
				},
			},
			Action: &AgentAction{
				Exit: true,
			},
		},
	})

	// Create a loop agent with the mock agent and max iterations set to 3
	config := &LoopAgentConfig{
		Name:          "LoopTestAgent",
		Description:   "Test loop agent",
		SubAgents:     []Agent{agent},
		MaxIterations: 3,
	}

	loopAgent, err := NewLoopAgent(ctx, config)
	assert.NoError(t, err)
	assert.NotNil(t, loopAgent)

	// Run the loop agent
	input := &AgentInput{
		Msgs: []Message{
			schema.UserMessage("Test input"),
		},
	}

	iterator := loopAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// Collect all events
	var events []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	// Should have only 1 event due to exit action
	assert.Equal(t, 1, len(events))

	// Verify the event
	event := events[0]
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.ModelResponse)
	assert.NotNil(t, event.Action)
	assert.True(t, event.Action.Exit)

	msg, err := event.Output.ModelResponse.Response.GetMessage()
	assert.NoError(t, err)
	assert.Equal(t, "Loop iteration with exit", msg.Content)
}
