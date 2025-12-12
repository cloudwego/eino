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

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// mockAgent implements the Agent interface for testing
type mockAgentForTool struct {
	name        string
	description string
	responses   []*AgentEvent
}

func (a *mockAgentForTool) Name(_ context.Context) string {
	return a.name
}

func (a *mockAgentForTool) Description(_ context.Context) string {
	return a.description
}

func (a *mockAgentForTool) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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

func newMockAgentForTool(name, description string, responses []*AgentEvent) *mockAgentForTool {
	return &mockAgentForTool{
		name:        name,
		description: description,
		responses:   responses,
	}
}

func TestAgentTool_Info(t *testing.T) {
	// Create a mock agent
	mockAgent_ := newMockAgentForTool("TestAgent", "Test agent description", nil)

	// Create an agentTool with the mock agent
	agentTool_ := NewAgentTool(context.Background(), mockAgent_)

	// Test the Info method
	ctx := context.Background()
	info, err := agentTool_.Info(ctx)

	// Verify results
	assert.NoError(t, err)
	assert.NotNil(t, info)
	assert.Equal(t, "TestAgent", info.Name)
	assert.Equal(t, "Test agent description", info.Desc)
	assert.NotNil(t, info.ParamsOneOf)
}

func TestAgentTool_InvokableRun(t *testing.T) {
	// Create a context
	ctx := context.Background()

	// Test cases
	tests := []struct {
		name           string
		agentResponses []*AgentEvent
		request        string
		expectedOutput string
		expectError    bool
	}{
		{
			name: "successful model response",
			agentResponses: []*AgentEvent{
				{
					AgentName: "TestAgent",
					Output: &AgentOutput{
						MessageOutput: &MessageVariant{
							IsStreaming: false,
							Message:     schema.AssistantMessage("Test response", nil),
							Role:        schema.Assistant,
						},
					},
				},
			},
			request:        `{"request":"Test request"}`,
			expectedOutput: "Test response",
			expectError:    false,
		},
		{
			name: "successful tool call response",
			agentResponses: []*AgentEvent{
				{
					AgentName: "TestAgent",
					Output: &AgentOutput{
						MessageOutput: &MessageVariant{
							IsStreaming: false,
							Message:     schema.ToolMessage("Tool response", "test-id"),
							Role:        schema.Tool,
						},
					},
				},
			},
			request:        `{"request":"Test tool request"}`,
			expectedOutput: "Tool response",
			expectError:    false,
		},
		{
			name:           "invalid request JSON",
			agentResponses: nil,
			request:        `invalid json`,
			expectedOutput: "",
			expectError:    true,
		},
		{
			name:           "no events returned",
			agentResponses: []*AgentEvent{},
			request:        `{"request":"Test request"}`,
			expectedOutput: "",
			expectError:    true,
		},
		{
			name: "error in event",
			agentResponses: []*AgentEvent{
				{
					AgentName: "TestAgent",
					Err:       assert.AnError,
				},
			},
			request:        `{"request":"Test request"}`,
			expectedOutput: "",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock agent with the test responses
			mockAgent_ := newMockAgentForTool("TestAgent", "Test agent description", tt.agentResponses)

			// Create an agentTool with the mock agent
			agentTool_ := NewAgentTool(ctx, mockAgent_)

			// Call InvokableRun
			output, err := agentTool_.(tool.InvokableTool).InvokableRun(ctx, tt.request)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedOutput, output)
			}
		})
	}
}

func TestGetReactHistory(t *testing.T) {
	g := compose.NewGraph[string, []Message](compose.WithGenLocalState(func(ctx context.Context) (state *State) {
		return &State{
			Messages: []Message{
				schema.UserMessage("user query"),
				schema.AssistantMessage("", []schema.ToolCall{{ID: "tool call id 1", Function: schema.FunctionCall{Name: "tool1", Arguments: "arguments1"}}}),
				schema.ToolMessage("tool result 1", "tool call id 1", schema.WithToolName("tool1")),
				schema.AssistantMessage("", []schema.ToolCall{{ID: "tool call id 2", Function: schema.FunctionCall{Name: "tool2", Arguments: "arguments2"}}}),
			},
			AgentName: "MyAgent",
		}
	}))
	assert.NoError(t, g.AddLambdaNode("1", compose.InvokableLambda(func(ctx context.Context, input string) (output []Message, err error) {
		return getReactChatHistory(ctx, "DestAgentName")
	})))
	assert.NoError(t, g.AddEdge(compose.START, "1"))
	assert.NoError(t, g.AddEdge("1", compose.END))

	ctx := context.Background()
	runner, err := g.Compile(ctx)
	assert.NoError(t, err)
	result, err := runner.Invoke(ctx, "")
	assert.NoError(t, err)
	assert.Equal(t, []Message{
		schema.UserMessage("user query"),
		schema.UserMessage("For context: [MyAgent] called tool: `tool1` with arguments: arguments1."),
		schema.UserMessage("For context: [MyAgent] `tool1` tool returned result: tool result 1."),
		schema.UserMessage("For context: [MyAgent] called tool: `transfer_to_agent` with arguments: DestAgentName."),
		schema.UserMessage("For context: [MyAgent] `transfer_to_agent` tool returned result: successfully transferred to agent [DestAgentName]."),
	}, result)
}

// mockAgentWithInputCapture implements the Agent interface for testing and captures the input it receives
type mockAgentWithInputCapture struct {
	name          string
	description   string
	capturedInput []Message
	responses     []*AgentEvent
}

func (a *mockAgentWithInputCapture) Name(_ context.Context) string {
	return a.name
}

func (a *mockAgentWithInputCapture) Description(_ context.Context) string {
	return a.description
}

func (a *mockAgentWithInputCapture) Run(_ context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	a.capturedInput = input.Messages

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

func newMockAgentWithInputCapture(name, description string, responses []*AgentEvent) *mockAgentWithInputCapture {
	return &mockAgentWithInputCapture{
		name:        name,
		description: description,
		responses:   responses,
	}
}

func TestAgentToolWithOptions(t *testing.T) {
	// Test Case 1: WithFullChatHistoryAsInput
	t.Run("WithFullChatHistoryAsInput", func(t *testing.T) {
		ctx := context.Background()

		// 1. Set up a mock agent that will capture the input it receives
		mockAgent := newMockAgentWithInputCapture("test-agent", "a test agent", []*AgentEvent{
			{
				AgentName: "test-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("done", nil),
						Role:        schema.Assistant,
					},
				},
			},
		})

		// 2. Create an agentTool with the option
		agentTool := NewAgentTool(ctx, mockAgent, WithFullChatHistoryAsInput())

		// 3. Set up a context with a chat history using a graph
		history := []Message{
			schema.UserMessage("first user message"),
			schema.AssistantMessage("first assistant response", nil),
		}

		g := compose.NewGraph[string, string](compose.WithGenLocalState(func(ctx context.Context) (state *State) {
			return &State{
				AgentName: "react-agent",
				Messages:  append(history, schema.AssistantMessage("tool call msg", nil)),
			}
		}))

		assert.NoError(t, g.AddLambdaNode("1", compose.InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			// Run the tool within the graph context that has the state
			_, err = agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"request":"some ignored input"}`)
			return "done", err
		})))
		assert.NoError(t, g.AddEdge(compose.START, "1"))
		assert.NoError(t, g.AddEdge("1", compose.END))

		runner, err := g.Compile(ctx)
		assert.NoError(t, err)

		// 4. Run the graph which will execute the tool with the state
		_, err = runner.Invoke(ctx, "")
		assert.NoError(t, err)

		// 5. Assert that the agent received the full history
		// The agent should receive: history (minus last assistant message) + transfer messages
		assert.Len(t, mockAgent.capturedInput, 4) // 2 from history + 2 transfer messages
		assert.Equal(t, "first user message", mockAgent.capturedInput[0].Content)
		assert.Equal(t, "For context: [react-agent] said: first assistant response.", mockAgent.capturedInput[1].Content)
		assert.Equal(t, "For context: [react-agent] called tool: `transfer_to_agent` with arguments: test-agent.", mockAgent.capturedInput[2].Content)
		assert.Equal(t, "For context: [react-agent] `transfer_to_agent` tool returned result: successfully transferred to agent [test-agent].", mockAgent.capturedInput[3].Content)
	})

	// Test Case 2: WithAgentInputSchema
	t.Run("WithAgentInputSchema", func(t *testing.T) {
		ctx := context.Background()

		// 1. Define a custom schema
		customSchema := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"custom_arg": {
				Desc:     "a custom argument",
				Required: true,
				Type:     schema.String,
			},
		})

		// 2. Set up a mock agent to capture input
		mockAgent := newMockAgentWithInputCapture("schema-agent", "agent with custom schema", []*AgentEvent{
			{
				AgentName: "schema-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("schema processed", nil),
						Role:        schema.Assistant,
					},
				},
			},
		})

		// 3. Create agentTool with the custom schema option
		agentTool := NewAgentTool(ctx, mockAgent, WithAgentInputSchema(customSchema))

		// 4. Verify the Info() method returns the custom schema
		info, err := agentTool.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, customSchema, info.ParamsOneOf)

		// 5. Run the tool with arguments matching the custom schema
		_, err = agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"custom_arg":"hello world"}`)
		assert.NoError(t, err)

		// 6. Assert that the agent received the correctly parsed argument
		// With custom schema, the agent should receive the raw JSON as input
		assert.Len(t, mockAgent.capturedInput, 1)
		assert.Equal(t, `{"custom_arg":"hello world"}`, mockAgent.capturedInput[0].Content)
	})

	// Test Case 3: WithAgentInputSchema with complex schema
	t.Run("WithAgentInputSchema_ComplexSchema", func(t *testing.T) {
		ctx := context.Background()

		// 1. Define a complex custom schema with multiple parameters
		complexSchema := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {
				Desc:     "user name",
				Required: true,
				Type:     schema.String,
			},
			"age": {
				Desc:     "user age",
				Required: false,
				Type:     schema.Integer,
			},
			"active": {
				Desc:     "user status",
				Required: false,
				Type:     schema.Boolean,
			},
		})

		// 2. Set up a mock agent
		mockAgent := newMockAgentWithInputCapture("complex-agent", "agent with complex schema", []*AgentEvent{
			{
				AgentName: "complex-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("complex processed", nil),
						Role:        schema.Assistant,
					},
				},
			},
		})

		// 3. Create agentTool with the complex schema option
		agentTool := NewAgentTool(ctx, mockAgent, WithAgentInputSchema(complexSchema))

		// 4. Verify the Info() method returns the complex schema
		info, err := agentTool.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, complexSchema, info.ParamsOneOf)

		// 5. Run the tool with complex arguments
		_, err = agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"name":"John","age":30,"active":true}`)
		assert.NoError(t, err)

		// 6. Assert that the agent received the complex JSON
		assert.Len(t, mockAgent.capturedInput, 1)
		assert.Equal(t, `{"name":"John","age":30,"active":true}`, mockAgent.capturedInput[0].Content)
	})

	// Test Case 4: Both options together
	t.Run("BothOptionsTogether", func(t *testing.T) {
		ctx := context.Background()

		// 1. Define a custom schema
		customSchema := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Desc:     "search query",
				Required: true,
				Type:     schema.String,
			},
		})

		// 2. Set up a mock agent
		mockAgent := newMockAgentWithInputCapture("combined-agent", "agent with both options", []*AgentEvent{
			{
				AgentName: "combined-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("combined processed", nil),
						Role:        schema.Assistant,
					},
				},
			},
		})

		// 3. Create agentTool with both options
		agentTool := NewAgentTool(ctx, mockAgent, WithAgentInputSchema(customSchema), WithFullChatHistoryAsInput())

		// 4. Set up a context with chat history using a graph
		history := []Message{
			schema.UserMessage("previous conversation"),
			schema.AssistantMessage("previous response", nil),
		}

		g := compose.NewGraph[string, string](compose.WithGenLocalState(func(ctx context.Context) (state *State) {
			return &State{
				AgentName: "react-agent",
				Messages:  append(history, schema.AssistantMessage("tool call", nil)),
			}
		}))

		assert.NoError(t, g.AddLambdaNode("1", compose.InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			// Run the tool within the graph context that has the state
			_, err = agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"query":"current query"}`)
			return "done", err
		})))
		assert.NoError(t, g.AddEdge(compose.START, "1"))
		assert.NoError(t, g.AddEdge("1", compose.END))

		runner, err := g.Compile(ctx)
		assert.NoError(t, err)

		// 5. Run the graph which will execute the tool with the state
		_, err = runner.Invoke(ctx, "")
		assert.NoError(t, err)

		// 6. Verify both options work together
		info, err := agentTool.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, customSchema, info.ParamsOneOf)

		// The agent should receive full history + the custom query
		assert.Len(t, mockAgent.capturedInput, 4) // 2 history + 2 transfer messages
		assert.Equal(t, "previous conversation", mockAgent.capturedInput[0].Content)
		assert.Equal(t, "For context: [react-agent] said: previous response.", mockAgent.capturedInput[1].Content)
		assert.Equal(t, "For context: [react-agent] called tool: `transfer_to_agent` with arguments: combined-agent.", mockAgent.capturedInput[2].Content)
		assert.Equal(t, "For context: [react-agent] `transfer_to_agent` tool returned result: successfully transferred to agent [combined-agent].", mockAgent.capturedInput[3].Content)
	})
}
