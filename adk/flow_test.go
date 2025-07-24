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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
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
						Name: fmt.Sprintf(TransferToAgentToolName, "ChildAgent"),
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

func TestConcurrentFlow(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock models for parent and child agents
	parentModel := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel1 := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel2 := mockModel.NewMockToolCallingChatModel(ctrl)

	// Set up expectations for the parent model
	// First call: parent model generates a message with TransferToAgent tool call
	cnt := 0
	parentModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.Message, error) {
			defer func() {
				cnt++
			}()
			if cnt == 0 {
				return schema.AssistantMessage("I'll transfer this to both the child agent 1 and child agent 2",
					[]schema.ToolCall{
						{
							ID: "tool-call-1",
							Function: schema.FunctionCall{
								Name: fmt.Sprintf(TransferToAgentToolName, "ChildAgent1"),
							},
						},
						{
							ID: "tool-call-2",
							Function: schema.FunctionCall{
								Name: fmt.Sprintf(TransferToAgentToolName, "ChildAgent2"),
							},
						},
					}), nil
			}

			return schema.AssistantMessage("this is the final response", nil), nil
		}).AnyTimes()

	// Set up expectations for the child model
	// Second call: child model generates a response
	childModel1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("Hello from child agent 1",
			[]schema.ToolCall{
				{
					ID: "child-tool-call-1",
					Function: schema.FunctionCall{
						Name: fmt.Sprintf(TransferToAgentToolName, "ParentAgent"),
					},
				},
			}), nil).
		Times(1)
	childModel2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("Hello from child agent 2",
			[]schema.ToolCall{
				{
					ID: "child-tool-call-2",
					Function: schema.FunctionCall{
						Name: fmt.Sprintf(TransferToAgentToolName, "ParentAgent"),
					},
				},
			}), nil).
		Times(1)

	// All models should implement WithTools
	parentModel.EXPECT().WithTools(gomock.Any()).Return(parentModel, nil).AnyTimes()
	childModel1.EXPECT().WithTools(gomock.Any()).Return(childModel1, nil).AnyTimes()
	childModel2.EXPECT().WithTools(gomock.Any()).Return(childModel2, nil).AnyTimes()

	// Create parent agent
	parentAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ParentAgent",
		Description: "Parent agent that will transfer to children",
		Instruction: "You are a parent agent.",
		Model:       parentModel,
	})
	assert.NoError(t, err)
	assert.NotNil(t, parentAgent)

	// Create child agents
	childAgent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent1",
		Description: "Child agent 1 that handles specific tasks",
		Instruction: "You are child agent 1.",
		Model:       childModel1,
	})
	assert.NoError(t, err)
	assert.NotNil(t, childAgent1)

	childAgent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent2",
		Description: "Child agent 2 that handles specific tasks",
		Instruction: "You are child agent 2.",
		Model:       childModel2,
	})
	assert.NoError(t, err)
	assert.NotNil(t, childAgent2)

	// Set up parent-child relationship
	flowAgent, err := SetSubAgents(ctx, parentAgent, []Agent{childAgent1, childAgent2})
	assert.NoError(t, err)
	assert.NotNil(t, flowAgent)

	assert.NotNil(t, parentAgent.subAgents)
	assert.NotNil(t, childAgent1.parentAgent)
	assert.NotNil(t, childAgent2.parentAgent)

	// Run the parent agent
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Please transfer this to the child agents"),
		},
	}
	iterator := flowAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ParentAgent", event.AgentName)
	assert.Equal(t, 1, len(event.RunPath))
	assert.Equal(t, "ParentAgent", event.RunPath[0].AgentName)
	assert.Equal(t, "I'll transfer this to both the child agent 1 and child agent 2", event.Output.MessageOutput.Message.Content)

	// two concurrent transfers can happen at any order
	transferEvent1, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ParentAgent", transferEvent1.AgentName)
	assert.Equal(t, 1, len(transferEvent1.RunPath))
	assert.Equal(t, "ParentAgent", transferEvent1.RunPath[0].AgentName)

	transferEvent2, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ParentAgent", transferEvent2.AgentName)
	assert.Equal(t, 1, len(transferEvent2.RunPath))
	assert.Equal(t, "ParentAgent", transferEvent2.RunPath[0].AgentName)

	assert.Contains(t, []string{
		transferEvent1.Action.TransferToAgent.DestAgentName,
		transferEvent2.Action.TransferToAgent.DestAgentName,
	}, "ChildAgent1")
	assert.Contains(t, []string{
		transferEvent1.Action.TransferToAgent.DestAgentName,
		transferEvent2.Action.TransferToAgent.DestAgentName,
	}, "ChildAgent2")

	// two concurrent contents and two concurrent transfer responses can happen at any order
	e1, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, 2, len(e1.RunPath))
	assert.Equal(t, "ParentAgent", e1.RunPath[0].AgentName)
	e2, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, 2, len(e2.RunPath))
	assert.Equal(t, "ParentAgent", e2.RunPath[0].AgentName)
	e3, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, 2, len(e3.RunPath))
	assert.Equal(t, "ParentAgent", e3.RunPath[0].AgentName)
	e4, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, 2, len(e4.RunPath))
	assert.Equal(t, "ParentAgent", e4.RunPath[0].AgentName)

	var responseEvent1, responseEvent2, transferBack1, transferBack2 *AgentEvent
	responseEvent1 = e1
	if e2.Action == nil {
		responseEvent2 = e2
	} else {
		transferBack1 = e2
	}

	if e3.Action == nil {
		responseEvent2 = e3
	} else {
		if transferBack1 == nil {
			transferBack1 = e3
		} else {
			transferBack2 = e3
		}
	}

	transferBack2 = e4

	assert.NotNil(t, transferBack1)
	assert.NotNil(t, responseEvent2)

	assert.Contains(t, []string{responseEvent1.AgentName, responseEvent2.AgentName}, "ChildAgent1")
	assert.Contains(t, []string{responseEvent1.AgentName, responseEvent2.AgentName}, "ChildAgent2")

	assert.Contains(t, []string{responseEvent1.RunPath[1].AgentName, responseEvent2.RunPath[1].AgentName}, "ChildAgent1")
	assert.Contains(t, []string{responseEvent1.RunPath[1].AgentName, responseEvent2.RunPath[1].AgentName}, "ChildAgent2")

	assert.Contains(t, []string{responseEvent1.Output.MessageOutput.Message.Content,
		responseEvent2.Output.MessageOutput.Message.Content}, "Hello from child agent 1")
	assert.Contains(t, []string{responseEvent1.Output.MessageOutput.Message.Content,
		responseEvent2.Output.MessageOutput.Message.Content}, "Hello from child agent 2")

	assert.Equal(t, "ParentAgent", transferBack1.Action.TransferToAgent.DestAgentName)
	assert.Equal(t, "ParentAgent", transferBack2.Action.TransferToAgent.DestAgentName)

	event, ok = iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ParentAgent", event.AgentName)
	assert.Equal(t, 3, len(event.RunPath))
	assert.Equal(t, "ParentAgent", event.RunPath[0].AgentName)
	assert.Equal(t, getConcurrentAgentsName([]string{"ChildAgent1", "ChildAgent2"}), event.RunPath[1].AgentName)
	assert.Equal(t, "ParentAgent", event.RunPath[2].AgentName)

	event, ok = iterator.Next()
	assert.False(t, ok)
}

func TestConcurrentResume(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock models for parent and child agents
	parentModel := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel1 := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel2 := mockModel.NewMockToolCallingChatModel(ctrl)

	// Set up expectations for the parent model
	// First call: parent model generates a message with TransferToAgent tool call
	cnt := 0
	parentModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.Message, error) {
			defer func() {
				cnt++
			}()
			if cnt == 0 {
				return schema.AssistantMessage("I'll transfer this to both the child agent 1 and child agent 2",
					[]schema.ToolCall{
						{
							ID: "tool-call-1",
							Function: schema.FunctionCall{
								Name: fmt.Sprintf(TransferToAgentToolName, "ChildAgent1"),
							},
						},
						{
							ID: "tool-call-2",
							Function: schema.FunctionCall{
								Name: fmt.Sprintf(TransferToAgentToolName, "ChildAgent2"),
							},
						},
					}), nil
			}

			return schema.AssistantMessage("this is the final response", nil), nil
		}).AnyTimes()

	// Set up expectations for the child model
	// Second call: child model generates an interruption, then generate an exit the next time
	child1Cnt := 0
	childModel1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.Message, error) {
			defer func() {
				child1Cnt++
			}()
			if child1Cnt == 0 {
				return nil, compose.InterruptAndRerun
			}

			return schema.AssistantMessage("", []schema.ToolCall{
				{
					ID: "exit_1",
					Function: schema.FunctionCall{
						Name:      "exit",
						Arguments: `{"final_result":"child 1 resumed and gave final result"}`,
					},
				},
			}), nil
		}).Times(2)
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
		Description: "Parent agent that will transfer to children",
		Instruction: "You are a parent agent.",
		Model:       parentModel,
	})
	assert.NoError(t, err)
	assert.NotNil(t, parentAgent)

	// Create child agents
	childAgent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent1",
		Description: "Child agent 1 that handles specific tasks",
		Instruction: "You are child agent 1.",
		Model:       childModel1,
		Exit:        &ExitTool{},
	})
	assert.NoError(t, err)
	assert.NotNil(t, childAgent1)

	childAgent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent2",
		Description: "Child agent 2 that handles specific tasks",
		Instruction: "You are child agent 2.",
		Model:       childModel2,
	})
	assert.NoError(t, err)
	assert.NotNil(t, childAgent2)

	// Set up parent-child relationship
	flowAgent, err := SetSubAgents(ctx, parentAgent, []Agent{childAgent1, childAgent2})
	assert.NoError(t, err)
	assert.NotNil(t, flowAgent)

	assert.NotNil(t, parentAgent.subAgents)
	assert.NotNil(t, childAgent1.parentAgent)
	assert.NotNil(t, childAgent2.parentAgent)

	// Run the parent agent
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent:           flowAgent,
		CheckPointStore: newMyStore(),
	})

	iterator := runner.Query(ctx, "Please transfer this to the child agents", WithCheckPointID("1"))
	assert.NotNil(t, iterator)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ParentAgent", event.AgentName)
	assert.Equal(t, 1, len(event.RunPath))
	assert.Equal(t, "ParentAgent", event.RunPath[0].AgentName)
	assert.Equal(t, "I'll transfer this to both the child agent 1 and child agent 2", event.Output.MessageOutput.Message.Content)

	// two concurrent transfers can happen at any order
	transferEvent1, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ParentAgent", transferEvent1.AgentName)
	assert.Equal(t, 1, len(transferEvent1.RunPath))
	assert.Equal(t, "ParentAgent", transferEvent1.RunPath[0].AgentName)

	transferEvent2, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ParentAgent", transferEvent2.AgentName)
	assert.Equal(t, 1, len(transferEvent2.RunPath))
	assert.Equal(t, "ParentAgent", transferEvent2.RunPath[0].AgentName)

	assert.Contains(t, []string{
		transferEvent1.Action.TransferToAgent.DestAgentName,
		transferEvent2.Action.TransferToAgent.DestAgentName,
	}, "ChildAgent1")
	assert.Contains(t, []string{
		transferEvent1.Action.TransferToAgent.DestAgentName,
		transferEvent2.Action.TransferToAgent.DestAgentName,
	}, "ChildAgent2")

	// child agent 2 responds normally
	event, ok = iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ChildAgent2", event.AgentName)
	assert.Equal(t, 2, len(event.RunPath))
	assert.Equal(t, "ParentAgent", event.RunPath[0].AgentName)
	assert.Equal(t, "ChildAgent2", event.RunPath[1].AgentName)
	assert.Equal(t, "Hello from child agent 2", event.Output.MessageOutput.Message.Content)

	// concurrent wrapper interrupts
	event, ok = iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "[ChildAgent1,ChildAgent2]", event.AgentName)
	assert.Equal(t, 2, len(event.RunPath))
	assert.Equal(t, "ParentAgent", event.RunPath[0].AgentName)
	assert.Equal(t, "[ChildAgent1,ChildAgent2]", event.RunPath[1].AgentName)
	assert.NotNil(t, event.Action)
	assert.NotNil(t, event.Action.Interrupted)

	event, ok = iterator.Next()
	assert.False(t, ok)

	iterator, err = runner.Resume(context.Background(), "1")
	assert.NoError(t, err)

	event, ok = iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ChildAgent1", event.AgentName)
	assert.Equal(t, 2, len(event.RunPath))
	assert.Equal(t, "ParentAgent", event.RunPath[0].AgentName)
	assert.Equal(t, "ChildAgent1", event.RunPath[1].AgentName)
	assert.Equal(t, 1, len(event.Output.MessageOutput.Message.ToolCalls))

	event, ok = iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "ChildAgent1", event.AgentName)
	assert.Equal(t, 2, len(event.RunPath))
	assert.Equal(t, "ParentAgent", event.RunPath[0].AgentName)
	assert.Equal(t, "ChildAgent1", event.RunPath[1].AgentName)
	assert.True(t, event.Action.Exit)

	event, ok = iterator.Next()
	assert.False(t, ok)
}
