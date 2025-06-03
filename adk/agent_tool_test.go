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
						ModelResponse: &ModelOutput{
							Response: &MessageVariant{
								IsStreaming: false,
								Msg:         schema.AssistantMessage("Test response", nil),
							},
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
						ToolCallResponse: &ToolCallOutput{
							Name:       "TestTool",
							ToolCallID: "test-id",
							Response: &MessageVariant{
								IsStreaming: false,
								Msg:         schema.ToolMessage("Tool response", "test-id"),
							},
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
