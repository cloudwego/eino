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
