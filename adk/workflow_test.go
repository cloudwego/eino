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
	// A custom run function for more complex test cases
	runFunc func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent]
}

func (a *mockAgent) Name(_ context.Context) string {
	return a.name
}

func (a *mockAgent) Description(_ context.Context) string {
	return a.description
}

func (a *mockAgent) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	if a.runFunc != nil {
		return a.runFunc(ctx, input, options...)
	}

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
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response from Agent1", nil),
					Role:        schema.Assistant,
				},
			},
		},
	})

	agent2 := newMockAgent("Agent2", "Second agent", []*AgentEvent{
		{
			AgentName: "Agent2",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response from Agent2", nil),
					Role:        schema.Assistant,
				},
			}},
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

	assert.Equal(t, "Test sequential agent", sequentialAgent.Description(ctx))

	// Run the sequential agent
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Test input"),
		},
	}

	// Initialize the run context
	ctx, _ = initRunCtx(ctx, sequentialAgent.Name(ctx), input)

	iterator := sequentialAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// First event should be from agent1
	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event1)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.MessageOutput)

	// Get the message content from agent1
	msg1 := event1.Output.MessageOutput.Message
	assert.NotNil(t, msg1)
	assert.Equal(t, "Response from Agent1", msg1.Content)

	// Second event should be from agent2
	event2, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event2)
	assert.Nil(t, event2.Err)
	assert.NotNil(t, event2.Output)
	assert.NotNil(t, event2.Output.MessageOutput)

	// Get the message content from agent2
	msg2 := event2.Output.MessageOutput.Message
	assert.NotNil(t, msg2)
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
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response from Agent1", nil),
					Role:        schema.Assistant,
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
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response from Agent2", nil),
					Role:        schema.Assistant,
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
		Messages: []Message{
			schema.UserMessage("Test input"),
		},
	}

	ctx, _ = initRunCtx(ctx, sequentialAgent.Name(ctx), input)

	iterator := sequentialAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// First event should be from agent1 with exit action
	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event1)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.MessageOutput)
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
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response from Agent1", nil),
					Role:        schema.Assistant,
				},
			},
		},
	})

	agent2 := newMockAgent("Agent2", "Second agent", []*AgentEvent{
		{
			AgentName: "Agent2",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Response from Agent2", nil),
					Role:        schema.Assistant,
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
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Test input"),
		},
	}

	ctx, _ = initRunCtx(ctx, parallelAgent.Name(ctx), input)

	iterator := parallelAgent.Run(ctx, input)
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
		assert.NotNil(t, event.Output.MessageOutput)

		msg := event.Output.MessageOutput.Message
		assert.NotNil(t, msg)
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
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Loop iteration", nil),
					Role:        schema.Assistant,
				},
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
		Messages: []Message{
			schema.UserMessage("Test input"),
		},
	}

	ctx, _ = initRunCtx(ctx, loopAgent.Name(ctx), input)

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
		assert.NotNil(t, event.Output.MessageOutput)

		msg := event.Output.MessageOutput.Message
		assert.NotNil(t, msg)
		assert.Equal(t, "Loop iteration", msg.Content)
	}
}

// TestLoopAgentWithBreakLoop tests the loop workflow agent with an break loop action
func TestLoopAgentWithBreakLoop(t *testing.T) {
	ctx := context.Background()

	// Create a mock agent that will break the loop after the first iteration
	agent := newMockAgent("LoopAgent", "Loop agent", []*AgentEvent{
		{
			AgentName: "LoopAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming: false,
					Message:     schema.AssistantMessage("Loop iteration with break loop", nil),
					Role:        schema.Assistant,
				},
			},
			Action: NewBreakLoopAction("LoopAgent"),
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
		Messages: []Message{
			schema.UserMessage("Test input"),
		},
	}
	ctx, _ = initRunCtx(ctx, loopAgent.Name(ctx), input)

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

	// Should have only 1 event due to break loop action
	assert.Equal(t, 1, len(events))

	// Verify the event
	event := events[0]
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.NotNil(t, event.Action)
	assert.NotNil(t, event.Action.BreakLoop)
	assert.True(t, event.Action.BreakLoop.Done)
	assert.Equal(t, "LoopAgent", event.Action.BreakLoop.From)
	assert.Equal(t, 0, event.Action.BreakLoop.CurrentIterations)

	msg := event.Output.MessageOutput.Message
	assert.NotNil(t, msg)
	assert.Equal(t, "Loop iteration with break loop", msg.Content)
}

// Add these test functions to the existing workflow_test.go file

// Replace the existing TestWorkflowAgentPanicRecovery function
func TestWorkflowAgentPanicRecovery(t *testing.T) {
	ctx := context.Background()

	// Create a panic agent that panics in Run method
	panicAgent := &panicMockAgent{
		mockAgent: mockAgent{
			name:        "PanicAgent",
			description: "Agent that panics",
			responses:   []*AgentEvent{},
		},
	}

	// Create a sequential agent with the panic agent
	config := &SequentialAgentConfig{
		Name:        "PanicTestAgent",
		Description: "Test agent with panic",
		SubAgents:   []Agent{panicAgent},
	}

	sequentialAgent, err := NewSequentialAgent(ctx, config)
	assert.NoError(t, err)

	// Run the agent and expect panic recovery
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Test input"),
		},
	}

	ctx, _ = initRunCtx(ctx, sequentialAgent.Name(ctx), input)
	iterator := sequentialAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// Should receive an error event due to panic recovery
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.NotNil(t, event.Err)
	assert.Contains(t, event.Err.Error(), "panic")

	// No more events
	_, ok = iterator.Next()
	assert.False(t, ok)
}

// Add these new mock agent types that properly panic
type panicMockAgent struct {
	mockAgent
}

func (a *panicMockAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	panic("test panic in agent")
}

// TestWorkflowAgentUnsupportedMode tests unsupported workflow mode error (lines 65-71)
func TestWorkflowAgentUnsupportedMode(t *testing.T) {
	ctx := context.Background()

	// Create a workflow agent with unsupported mode
	agent := &workflowAgent{
		name:        "UnsupportedModeAgent",
		description: "Agent with unsupported mode",
		subAgents:   []*flowAgent{},
		mode:        workflowAgentMode(999), // Invalid mode
	}

	// Run the agent and expect error
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Test input"),
		},
	}

	ctx, _ = initRunCtx(ctx, agent.Name(ctx), input)
	iterator := agent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// Should receive an error event due to unsupported mode
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.NotNil(t, event.Err)
	assert.Contains(t, event.Err.Error(), "unsupported workflow agent mode")

	// No more events
	_, ok = iterator.Next()
	assert.False(t, ok)
}

// TestVisibility is a comprehensive test for the new visibility model.
func TestVisibility(t *testing.T) {
	ctx := context.Background()

	// Define Agents
	startAgent := newMockAgent("StartAgent", "", []*AgentEvent{{
		AgentName: "StartAgent",
		Output:    &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Event from StartAgent", nil)}},
	}})

	// Lane A: Nested Parallel Agent
	nestedB := &mockAgent{
		name: "NestedB",
		runFunc: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer generator.Close()
				t.Run("Nested Predecessor Visibility", func(t *testing.T) {
					history := ""
					for _, msg := range input.Messages {
						history += msg.Content
					}
					assert.Contains(t, history, "StartAgent")
				})
				generator.Send(&AgentEvent{
					AgentName: "NestedB",
					Output:    &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Output from NestedB", nil)}},
				})
			}()
			return iterator
		},
	}
	nestedC := newMockAgent("NestedC", "", []*AgentEvent{{
		AgentName: "NestedC",
		Output:    &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Output from NestedC", nil)}},
	}})
	laneA, _ := NewParallelAgent(ctx, &ParallelAgentConfig{
		Name:      "LaneA",
		SubAgents: []Agent{nestedB, nestedC},
	})

	// Lane B: Simple Agent that checks visibility
	laneB := &mockAgent{
		name: "LaneB",
		runFunc: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer generator.Close()
				t.Run("Isolation and Predecessor Visibility", func(t *testing.T) {
					// This agent will check what it can see.
					// It should see its predecessor (StartAgent) but NOT see anything from LaneA.
					history := ""
					for _, msg := range input.Messages {
						history += msg.Content
					}
					assert.Contains(t, history, "Test input")
					assert.Contains(t, history, "StartAgent")
					assert.NotContains(t, history, "NestedB")
					assert.NotContains(t, history, "NestedC")
				})

				generator.Send(&AgentEvent{
					AgentName: "LaneB",
					Output:    &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Output from LaneB", nil)}},
				})
			}()
			return iterator
		},
	}

	// Successor Agent: Runs after the parallel block
	successor := &mockAgent{
		name: "Successor",
		runFunc: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer generator.Close()
				t.Run("Successor Visibility", func(t *testing.T) {
					// This agent should see everything from the parallel block and the start agent.
					history := ""
					for _, msg := range input.Messages {
						history += msg.Content
					}
					assert.Contains(t, history, "Test input")
					assert.Contains(t, history, "StartAgent")
					assert.Contains(t, history, "NestedB")
					assert.Contains(t, history, "NestedC")
					assert.Contains(t, history, "LaneB")
				})

				generator.Send(&AgentEvent{AgentName: "Successor"})
			}()
			return iterator
		},
	}

	// Main Test Agent: Seq(StartAgent, Par(LaneA, LaneB), Successor)
	mainParallel, _ := NewParallelAgent(ctx, &ParallelAgentConfig{
		Name:      "MainParallel",
		SubAgents: []Agent{laneA, laneB},
	})

	mainSequential, _ := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:      "MainSequential",
		SubAgents: []Agent{startAgent, mainParallel, successor},
	})

	// Run the test
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Test input"),
		},
	}
	iterator := mainSequential.Run(ctx, input)

	// Collect events and check RunPaths
	var events []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	t.Run("Inheritance and Path", func(t *testing.T) {
		// Verify RunPath and Lanes
		var start, nestedB, nestedC, laneB, successor bool
		for _, event := range events {
			switch event.AgentName {
			case "StartAgent":
				start = true
				assert.Equal(t, []RunStep{
					{agentName: "MainSequential"},
					{agentName: "StartAgent"},
				}, event.RunPath)
			case "NestedB":
				nestedB = true
				assert.Equal(t, []RunStep{
					{agentName: "MainSequential"},
					{agentName: "StartAgent"},
					{agentName: "MainParallel"},
					{agentName: "LaneA", lanes: []string{"LaneA"}},
					{agentName: "NestedB", lanes: []string{"LaneA", "NestedB"}},
				}, event.RunPath)
			case "NestedC":
				nestedC = true
				assert.Equal(t, []RunStep{
					{agentName: "MainSequential"},
					{agentName: "StartAgent"},
					{agentName: "MainParallel"},
					{agentName: "LaneA", lanes: []string{"LaneA"}},
					{agentName: "NestedC", lanes: []string{"LaneA", "NestedC"}},
				}, event.RunPath)
			case "LaneB":
				laneB = true
				assert.Equal(t, []RunStep{
					{agentName: "MainSequential"},
					{agentName: "StartAgent"},
					{agentName: "MainParallel"},
					{agentName: "LaneB", lanes: []string{"LaneB"}},
				}, event.RunPath)
			case "Successor":
				successor = true
				assert.Equal(t, []RunStep{
					{agentName: "MainSequential"},
					{agentName: "StartAgent"},
					{agentName: "MainParallel"},
					{agentName: "Successor"},
				}, event.RunPath)
			}
		}
		// Ensure all key agents ran
		assert.True(t, start)
		assert.True(t, nestedB)
		assert.True(t, nestedC)
		assert.True(t, laneB)
		assert.True(t, successor)
	})
}
