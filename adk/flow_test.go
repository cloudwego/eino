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
	"go.uber.org/mock/gomock"

	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestTransferToAgent tests the TransferToAgent functionality
func TestTransferToAgent(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock models for parent and child agents
	parentModel := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel := mockModel.NewMockToolCallingChatModel(ctrl)

	// Set up expectations for the parent model
	// First call: parent model generates a message with TransferToAgent tool call
	parentModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("I'll transfer this to the child agent",
			[]schema.ToolCall{
				{
					ID: "tool-call-1",
					Function: schema.FunctionCall{
						Name:      TransferToAgentToolName,
						Arguments: `{"agent_name": "ChildAgent"}`,
					},
				},
			}), nil).
		Times(1)

	// Set up expectations for the child model
	// Second call: child model generates a response
	childModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("Hello from child agent", nil), nil).
		Times(1)

	// Both models should implement WithTools
	parentModel.EXPECT().WithTools(gomock.Any()).Return(parentModel, nil).AnyTimes()
	childModel.EXPECT().WithTools(gomock.Any()).Return(childModel, nil).AnyTimes()

	// Create parent agent
	parentAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ParentAgent",
		Description: "Parent agent that will transfer to child",
		Instruction: "You are a parent agent.",
		Model:       parentModel,
	})
	assert.NoError(t, err)
	assert.NotNil(t, parentAgent)

	// Create child agent
	childAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent",
		Description: "Child agent that handles specific tasks",
		Instruction: "You are a child agent.",
		Model:       childModel,
	})
	assert.NoError(t, err)
	assert.NotNil(t, childAgent)

	// Set up parent-child relationship
	flowAgent, err := SetSubAgents(ctx, parentAgent, []Agent{childAgent})
	assert.NoError(t, err)
	assert.NotNil(t, flowAgent)

	assert.NotNil(t, parentAgent.subAgents)
	assert.NotNil(t, childAgent.parentAgent)

	// Run the parent agent
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Please transfer this to the child agent"),
		},
	}
	ctx, _ = initRunCtx(ctx, flowAgent.Name(ctx), input)
	iterator := flowAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// First event: parent model output with tool call
	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event1)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.MessageOutput)
	assert.Equal(t, schema.Assistant, event1.Output.MessageOutput.Role)

	// Second event: tool output (TransferToAgent)
	event2, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event2)
	assert.Nil(t, event2.Err)
	assert.NotNil(t, event2.Output)
	assert.NotNil(t, event2.Output.MessageOutput)
	assert.Equal(t, schema.Tool, event2.Output.MessageOutput.Role)

	// Verify the action is TransferToAgent
	assert.NotNil(t, event2.Action)
	assert.NotNil(t, event2.Action.TransferToAgent)
	assert.Equal(t, "ChildAgent", event2.Action.TransferToAgent.DestAgentName)

	// Third event: child model output
	event3, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event3)
	assert.Nil(t, event3.Err)
	assert.NotNil(t, event3.Output)
	assert.NotNil(t, event3.Output.MessageOutput)
	assert.Equal(t, schema.Assistant, event3.Output.MessageOutput.Role)

	// Verify the message content from child agent
	msg := event3.Output.MessageOutput.Message
	assert.NotNil(t, msg)
	assert.Equal(t, "Hello from child agent", msg.Content)

	// No more events
	_, ok = iterator.Next()
	assert.False(t, ok)
}

// TestRunConcurrentLanes tests the basic concurrent execution functionality
func TestRunConcurrentLanes(t *testing.T) {
	ctx := context.Background()

	// Create mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock models for parent and two child agents
	parentModel := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel1 := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel2 := mockModel.NewMockToolCallingChatModel(ctrl)

	// Set up expectations for the parent model
	// Parent generates two transfer actions
	parentModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("I'll transfer to both child agents",
			[]schema.ToolCall{
				{
					ID: "tool-call-1",
					Function: schema.FunctionCall{
						Name:      TransferToAgentToolName,
						Arguments: `{"agent_name": "ChildAgent1"}`,
					},
				},
				{
					ID: "tool-call-2",
					Function: schema.FunctionCall{
						Name:      TransferToAgentToolName,
						Arguments: `{"agent_name": "ChildAgent2"}`,
					},
				},
			}), nil).
		Times(1)

	// Set up expectations for child models
	childModel1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("Hello from child agent 1", nil), nil).
		Times(1)

	childModel2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("Hello from child agent 2", nil), nil).
		Times(1)

	// All models should implement WithTools
	parentModel.EXPECT().WithTools(gomock.Any()).Return(parentModel, nil).AnyTimes()
	childModel1.EXPECT().WithTools(gomock.Any()).Return(childModel1, nil).AnyTimes()
	childModel2.EXPECT().WithTools(gomock.Any()).Return(childModel2, nil).AnyTimes()

	// Create parent agent
	parentAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ParentAgent",
		Description: "Parent agent that transfers to multiple children",
		Instruction: "You are a parent agent.",
		Model:       parentModel,
	})
	assert.NoError(t, err)
	assert.NotNil(t, parentAgent)

	// Create child agents
	childAgent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent1",
		Description: "First child agent",
		Instruction: "You are the first child agent.",
		Model:       childModel1,
	})
	assert.NoError(t, err)
	assert.NotNil(t, childAgent1)

	childAgent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent2",
		Description: "Second child agent",
		Instruction: "You are the second child agent.",
		Model:       childModel2,
	})
	assert.NoError(t, err)
	assert.NotNil(t, childAgent2)

	// Set up parent-child relationships
	flowAgent, err := SetSubAgents(ctx, parentAgent, []Agent{childAgent1, childAgent2})
	assert.NoError(t, err)
	assert.NotNil(t, flowAgent)

	// Run the parent agent
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Please transfer to both child agents"),
		},
	}
	ctx, _ = initRunCtx(ctx, flowAgent.Name(ctx), input)
	iterator := flowAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// Collect all events to verify execution order
	var events []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	// Verify exact event sequence and content
	assert.Equal(t, 5, len(events), "Should have exactly 5 events")

	// Event 1: Parent agent output with transfer tool calls
	event1 := events[0]
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.MessageOutput)
	assert.Equal(t, "I'll transfer to both child agents", event1.Output.MessageOutput.Message.Content)
	assert.Equal(t, 2, len(event1.Output.MessageOutput.Message.ToolCalls))

	// Event 2: First transfer action
	event2 := events[1]
	assert.NotNil(t, event2.Action)
	assert.NotNil(t, event2.Action.TransferToAgent)
	assert.Equal(t, "ChildAgent1", event2.Action.TransferToAgent.DestAgentName)

	// Event 3: Second transfer action
	event3 := events[2]
	assert.NotNil(t, event3.Action)
	assert.NotNil(t, event3.Action.TransferToAgent)
	assert.Equal(t, "ChildAgent2", event3.Action.TransferToAgent.DestAgentName)

	// Events 4 & 5: Child agent outputs (order may vary due to concurrency)
	// But both should be present with correct content
	child1Event := events[3]
	child2Event := events[4]

	// Verify both child outputs are present
	assert.NotNil(t, child1Event.Output)
	assert.NotNil(t, child1Event.Output.MessageOutput)
	assert.NotNil(t, child2Event.Output)
	assert.NotNil(t, child2Event.Output.MessageOutput)

	// Check which event belongs to which child agent
	var child1Content, child2Content string
	if child1Event.Output.MessageOutput.Message.Content == "Hello from child agent 1" {
		child1Content = child1Event.Output.MessageOutput.Message.Content
		child2Content = child2Event.Output.MessageOutput.Message.Content
	} else {
		child1Content = child2Event.Output.MessageOutput.Message.Content
		child2Content = child1Event.Output.MessageOutput.Message.Content
	}

	assert.Equal(t, "Hello from child agent 1", child1Content)
	assert.Equal(t, "Hello from child agent 2", child2Content)
}
