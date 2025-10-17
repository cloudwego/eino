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
	input := AgentInput{
		Messages: []Message{
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
		Messages: []Message{
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

func (a *panicMockAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	panic("test panic in agent")
}

type panicResumableMockAgent struct {
	mockAgent
}

func (a *panicResumableMockAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	panic("test panic in resume")
}

// Remove the old mockResumableAgent type and replace it with panicResumableMockAgent

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

// TestWorkflowAgentResumePanicRecovery tests panic recovery in Resume method (lines 108-115)
func TestWorkflowAgentResumePanicRecovery(t *testing.T) {
	ctx := context.Background()

	// Create a mock resumable agent that panics on Resume
	panicAgent := &mockResumableAgent{
		mockAgent: mockAgent{
			name:        "PanicResumeAgent",
			description: "Agent that panics on resume",
			responses:   []*AgentEvent{},
		},
	}

	// Create a sequential agent with the panic agent
	config := &SequentialAgentConfig{
		Name:        "ResumeTestAgent",
		Description: "Test agent for resume panic",
		SubAgents:   []Agent{panicAgent},
	}

	sequentialAgent, err := NewSequentialAgent(ctx, config)
	assert.NoError(t, err)

	// Initialize context with run context - this is the key fix
	ctx = ctxWithNewRunCtx(ctx)

	// Create valid resume info
	resumeInfo := &ResumeInfo{
		EnableStreaming: false,
		InterruptInfo: &InterruptInfo{
			Data: &WorkflowInterruptInfo{
				OrigInput: &AgentInput{
					Messages: []Message{schema.UserMessage("test")},
				},
				SequentialInterruptIndex: 0,
				SequentialInterruptInfo: &InterruptInfo{
					Data: "some interrupt data",
				},
				LoopIterations: 0,
			},
		},
	}

	// Call Resume and expect panic recovery
	iterator := sequentialAgent.(ResumableAgent).Resume(ctx, resumeInfo)
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

// mockResumableAgent extends mockAgent to implement ResumableAgent interface
type mockResumableAgent struct {
	mockAgent
}

func (a *mockResumableAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	panic("test panic in resume")
}

// TestWorkflowAgentResumeInvalidDataType tests invalid data type in Resume method
func TestWorkflowAgentResumeInvalidDataType(t *testing.T) {
	ctx := context.Background()

	// Create a workflow agent
	agent := &workflowAgent{
		name:        "InvalidDataTestAgent",
		description: "Agent for invalid data test",
		subAgents:   []*flowAgent{},
		mode:        workflowAgentModeSequential,
	}

	// Create resume info with invalid data type
	resumeInfo := &ResumeInfo{
		EnableStreaming: false,
		InterruptInfo: &InterruptInfo{
			Data: "invalid data type", // Should be *WorkflowInterruptInfo
		},
	}

	// Call Resume and expect type assertion error
	iterator := agent.Resume(ctx, resumeInfo)
	assert.NotNil(t, iterator)

	// Should receive an error event due to type assertion failure
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.NotNil(t, event.Err)
	assert.Contains(t, event.Err.Error(), "type of InterruptInfo.Data is expected to")
	assert.Contains(t, event.Err.Error(), "actual: string")

	// No more events
	_, ok = iterator.Next()
	assert.False(t, ok)
}
