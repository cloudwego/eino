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

package planexecute

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	mockAdk "github.com/cloudwego/eino/internal/mock/adk"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestNewPlanner tests the NewPlanner function with ChatModelWithFormattedOutput
func TestNewPlannerWithFormattedOutput(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock chat model
	mockChatModel := mockModel.NewMockBaseChatModel(ctrl)

	// Create the PlannerConfig
	conf := &PlannerConfig{
		ChatModelWithFormattedOutput: mockChatModel,
	}

	// Create the planner
	p, err := NewPlanner(ctx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, p)

	// Verify the planner's name and description
	assert.Equal(t, "Planner", p.Name(ctx))
	assert.Equal(t, "a planner agent", p.Description(ctx))
}

// TestNewPlannerWithToolCalling tests the NewPlanner function with ToolCallingChatModel
func TestNewPlannerWithToolCalling(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock tool calling chat model
	mockToolCallingModel := mockModel.NewMockToolCallingChatModel(ctrl)
	mockToolCallingModel.EXPECT().WithTools(gomock.Any()).Return(mockToolCallingModel, nil).Times(1)

	// Create the PlannerConfig
	conf := &PlannerConfig{
		ToolCallingChatModel: mockToolCallingModel,
		// Use default instruction and tool info
	}

	// Create the planner
	p, err := NewPlanner(ctx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, p)

	// Verify the planner's name and description
	assert.Equal(t, "Planner", p.Name(ctx))
	assert.Equal(t, "a planner agent", p.Description(ctx))
}

// TestPlannerRunWithFormattedOutput tests the Run method of a planner created with ChatModelWithFormattedOutput
func TestPlannerRunWithFormattedOutput(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock chat model
	mockChatModel := mockModel.NewMockBaseChatModel(ctrl)

	// Create a plan response
	planJSON := `{"steps":["Step 1", "Step 2", "Step 3"]}`
	planMsg := schema.AssistantMessage(planJSON, nil)
	sr, sw := schema.Pipe[*schema.Message](1)
	sw.Send(planMsg, nil)
	sw.Close()

	// Mock the Generate method
	mockChatModel.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).Return(sr, nil).Times(1)

	// Create the PlannerConfig
	conf := &PlannerConfig{
		ChatModelWithFormattedOutput: mockChatModel,
	}

	// Create the planner
	p, err := NewPlanner(ctx, conf)
	assert.NoError(t, err)

	// Run the planner
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: p})
	iterator := runner.Run(ctx, []adk.Message{schema.UserMessage("Plan this task")})

	// Get the event from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)
	msg, _, err := adk.GetMessage(event)
	assert.NoError(t, err)
	assert.Equal(t, planMsg.Content, msg.Content)

	event, ok = iterator.Next()
	assert.False(t, ok)

	plan := defaultNewPlan(ctx)
	err = plan.UnmarshalJSON([]byte(msg.Content))
	assert.NoError(t, err)
	plan_ := plan.(*defaultPlan)
	assert.Equal(t, 3, len(plan_.Steps))
	assert.Equal(t, "Step 1", plan_.Steps[0])
	assert.Equal(t, "Step 2", plan_.Steps[1])
	assert.Equal(t, "Step 3", plan_.Steps[2])
}

// TestPlannerRunWithToolCalling tests the Run method of a planner created with ToolCallingChatModel
func TestPlannerRunWithToolCalling(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock tool calling chat model
	mockToolCallingModel := mockModel.NewMockToolCallingChatModel(ctrl)

	// Create a tool call response with a plan
	planArgs := `{"steps":["Step 1", "Step 2", "Step 3"]}`
	toolCall := schema.ToolCall{
		ID:   "tool_call_id",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "Plan", // This should match PlanToolInfo.Name
			Arguments: planArgs,
		},
	}

	toolCallMsg := schema.AssistantMessage("", nil)
	toolCallMsg.ToolCalls = []schema.ToolCall{toolCall}
	sr, sw := schema.Pipe[*schema.Message](1)
	sw.Send(toolCallMsg, nil)
	sw.Close()

	// Mock the WithTools method to return a model that will be used for Generate
	mockToolCallingModel.EXPECT().WithTools(gomock.Any()).Return(mockToolCallingModel, nil).Times(1)

	// Mock the Generate method to return the tool call message
	mockToolCallingModel.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).Return(sr, nil).Times(1)

	// Create the PlannerConfig with ToolCallingChatModel
	conf := &PlannerConfig{
		ToolCallingChatModel: mockToolCallingModel,
		// Use default instruction and tool info
	}

	// Create the planner
	p, err := NewPlanner(ctx, conf)
	assert.NoError(t, err)

	// Run the planner
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: p})
	iterator := runner.Run(ctx, []adk.Message{schema.UserMessage("no input")})

	// Get the event from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)

	msg, _, err := adk.GetMessage(event)
	assert.NoError(t, err)
	assert.Equal(t, planArgs, msg.Content)

	_, ok = iterator.Next()
	assert.False(t, ok)

	plan := defaultNewPlan(ctx)
	err = plan.UnmarshalJSON([]byte(msg.Content))
	assert.NoError(t, err)
	plan_ := plan.(*defaultPlan)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(plan_.Steps))
	assert.Equal(t, "Step 1", plan_.Steps[0])
	assert.Equal(t, "Step 2", plan_.Steps[1])
	assert.Equal(t, "Step 3", plan_.Steps[2])
}

// TestNewExecutor tests the NewExecutor function
func TestNewExecutor(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock tool calling chat model
	mockToolCallingModel := mockModel.NewMockToolCallingChatModel(ctrl)

	// Create the ExecutorConfig
	conf := &ExecutorConfig{
		Model:         mockToolCallingModel,
		MaxIterations: 3,
	}

	// Create the executor
	executor, err := NewExecutor(ctx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, executor)

	// Verify the executor's name and description
	assert.Equal(t, "Executor", executor.Name(ctx))
	assert.Equal(t, "an executor agent", executor.Description(ctx))
}

// TestExecutorRun tests the Run method of the executor
func TestExecutorRun(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock tool calling chat model
	mockToolCallingModel := mockModel.NewMockToolCallingChatModel(ctrl)

	// Store a plan in the session
	plan := &defaultPlan{Steps: []string{"Step 1", "Step 2", "Step 3"}}
	adk.AddSessionValue(ctx, PlanSessionKey, plan)

	// Set up expectations for the mock model
	// The model should return the last user message as its response
	mockToolCallingModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			// Find the last user message
			var lastUserMessage string
			for _, msg := range messages {
				if msg.Role == schema.User {
					lastUserMessage = msg.Content
				}
			}
			// Return the last user message as the model's response
			return schema.AssistantMessage(lastUserMessage, nil), nil
		}).Times(1)

	// Create the ExecutorConfig
	conf := &ExecutorConfig{
		Model:         mockToolCallingModel,
		MaxIterations: 3,
	}

	// Create the executor
	executor, err := NewExecutor(ctx, conf)
	assert.NoError(t, err)

	// Run the executor
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: executor})
	iterator := runner.Run(ctx, []adk.Message{schema.UserMessage("no input")},
		adk.WithSessionValues(map[string]any{
			PlanSessionKey:      plan,
			UserInputSessionKey: []adk.Message{schema.UserMessage("no input")},
		}),
	)

	// Get the event from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	msg, _, err := adk.GetMessage(event)
	assert.NoError(t, err)
	t.Logf("executor model input msg:\n %s\n", msg.Content)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

// TestNewReplanner tests the NewReplanner function
func TestNewReplanner(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock tool calling chat model
	mockToolCallingModel := mockModel.NewMockToolCallingChatModel(ctrl)
	// Mock the WithTools method
	mockToolCallingModel.EXPECT().WithTools(gomock.Any()).Return(mockToolCallingModel, nil).Times(1)

	// Create plan and respond tools
	planTool := &schema.ToolInfo{
		Name: "Plan",
		Desc: "Plan tool",
	}

	respondTool := &schema.ToolInfo{
		Name: "Respond",
		Desc: "Respond tool",
	}

	// Create the ReplannerConfig
	conf := &ReplannerConfig{
		ChatModel:   mockToolCallingModel,
		PlanTool:    planTool,
		RespondTool: respondTool,
	}

	// Create the replanner
	rp, err := NewReplanner(ctx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, rp)

	// Verify the replanner's name and description
	assert.Equal(t, "Replanner", rp.Name(ctx))
	assert.Equal(t, "a replanner agent", rp.Description(ctx))
}

// TestReplannerRunWithPlan tests the Replanner's ability to use the plan_tool
func TestReplannerRunWithPlan(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock tool calling chat model
	mockToolCallingModel := mockModel.NewMockToolCallingChatModel(ctrl)

	// Create plan and respond tools
	planTool := &schema.ToolInfo{
		Name: "Plan",
		Desc: "Plan tool",
	}

	respondTool := &schema.ToolInfo{
		Name: "Respond",
		Desc: "Respond tool",
	}

	// Create a tool call response for the Plan tool
	planArgs := `{"steps":["Updated Step 1", "Updated Step 2"]}`
	toolCall := schema.ToolCall{
		ID:   "tool_call_id",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      planTool.Name,
			Arguments: planArgs,
		},
	}

	toolCallMsg := schema.AssistantMessage("", nil)
	toolCallMsg.ToolCalls = []schema.ToolCall{toolCall}
	sr, sw := schema.Pipe[*schema.Message](1)
	sw.Send(toolCallMsg, nil)
	sw.Close()

	// Mock the Generate method
	mockToolCallingModel.EXPECT().WithTools(gomock.Any()).Return(mockToolCallingModel, nil).Times(1)
	mockToolCallingModel.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).Return(sr, nil).Times(1)

	// Create the ReplannerConfig
	conf := &ReplannerConfig{
		ChatModel:   mockToolCallingModel,
		PlanTool:    planTool,
		RespondTool: respondTool,
	}

	// Create the replanner
	rp, err := NewReplanner(ctx, conf)
	assert.NoError(t, err)

	// Store necessary values in the session
	plan := &defaultPlan{Steps: []string{"Step 1", "Step 2", "Step 3"}}

	rp, err = agentOutputSessionKVs(ctx, rp)
	assert.NoError(t, err)

	// Run the replanner
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: rp})
	iterator := runner.Run(ctx, []adk.Message{schema.UserMessage("no input")},
		adk.WithSessionValues(map[string]any{
			PlanSessionKey:         plan,
			ExecutedStepSessionKey: "Execution result",
			UserInputSessionKey:    []adk.Message{schema.UserMessage("User input")},
		}),
	)

	// Get the event from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)

	event, ok = iterator.Next()
	assert.True(t, ok)
	kvs := event.Output.CustomizedOutput.(map[string]any)
	assert.Greater(t, len(kvs), 0)

	// Verify the updated plan was stored in the session
	planValue, ok := kvs[PlanSessionKey]
	assert.True(t, ok)
	updatedPlan, ok := planValue.(*defaultPlan)
	assert.True(t, ok)
	assert.Equal(t, 2, len(updatedPlan.Steps))
	assert.Equal(t, "Updated Step 1", updatedPlan.Steps[0])
	assert.Equal(t, "Updated Step 2", updatedPlan.Steps[1])

	// Verify the execute results were updated
	executeResultsValue, ok := kvs[ExecutedStepsSessionKey]
	assert.True(t, ok)
	executeResults, ok := executeResultsValue.([]ExecutedStep)
	assert.True(t, ok)
	assert.Equal(t, 1, len(executeResults))
	assert.Equal(t, "Step 1", executeResults[0].Step)
	assert.Equal(t, "Execution result", executeResults[0].Result)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

// TestReplannerRunWithRespond tests the Replanner's ability to use the respond_tool
func TestReplannerRunWithRespond(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock tool calling chat model
	mockToolCallingModel := mockModel.NewMockToolCallingChatModel(ctrl)

	// Create plan and respond tools
	planTool := &schema.ToolInfo{
		Name: "Plan",
		Desc: "Plan tool",
	}

	respondTool := &schema.ToolInfo{
		Name: "Respond",
		Desc: "Respond tool",
	}

	// Create a tool call response for the Respond tool
	responseArgs := `{"response":"This is the final response to the user"}`
	toolCall := schema.ToolCall{
		ID:   "tool_call_id",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      respondTool.Name,
			Arguments: responseArgs,
		},
	}

	toolCallMsg := schema.AssistantMessage("", nil)
	toolCallMsg.ToolCalls = []schema.ToolCall{toolCall}
	sr, sw := schema.Pipe[*schema.Message](1)
	sw.Send(toolCallMsg, nil)
	sw.Close()

	// Mock the Generate method
	mockToolCallingModel.EXPECT().WithTools(gomock.Any()).Return(mockToolCallingModel, nil).Times(1)
	mockToolCallingModel.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).Return(sr, nil).Times(1)

	// Create the ReplannerConfig
	conf := &ReplannerConfig{
		ChatModel:   mockToolCallingModel,
		PlanTool:    planTool,
		RespondTool: respondTool,
	}

	// Create the replanner
	rp, err := NewReplanner(ctx, conf)
	assert.NoError(t, err)

	// Store necessary values in the session
	plan := &defaultPlan{Steps: []string{"Step 1", "Step 2", "Step 3"}}

	// Run the replanner
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: rp})
	iterator := runner.Run(ctx, []adk.Message{schema.UserMessage("no input")},
		adk.WithSessionValues(map[string]any{
			PlanSessionKey:         plan,
			ExecutedStepSessionKey: "Execution result",
			UserInputSessionKey:    []adk.Message{schema.UserMessage("User input")},
		}),
	)

	// Get the event from the iterator
	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)
	msg, _, err := adk.GetMessage(event)
	assert.NoError(t, err)
	assert.Equal(t, responseArgs, msg.Content)

	// Verify that an exit action was generated
	event, ok = iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event.Action)
	assert.NotNil(t, event.Action.BreakLoop)
	assert.False(t, event.Action.BreakLoop.Done)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

// TestNewPlanExecuteAgent tests the New function
func TestNewPlanExecuteAgent(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock agents
	mockPlanner := mockAdk.NewMockAgent(ctrl)
	mockExecutor := mockAdk.NewMockAgent(ctrl)
	mockReplanner := mockAdk.NewMockAgent(ctrl)

	// Set up expectations for the mock agents
	mockPlanner.EXPECT().Name(gomock.Any()).Return("Planner").AnyTimes()
	mockPlanner.EXPECT().Description(gomock.Any()).Return("a planner agent").AnyTimes()

	mockExecutor.EXPECT().Name(gomock.Any()).Return("Executor").AnyTimes()
	mockExecutor.EXPECT().Description(gomock.Any()).Return("an executor agent").AnyTimes()

	mockReplanner.EXPECT().Name(gomock.Any()).Return("Replanner").AnyTimes()
	mockReplanner.EXPECT().Description(gomock.Any()).Return("a replanner agent").AnyTimes()

	conf := &Config{
		Planner:   mockPlanner,
		Executor:  mockExecutor,
		Replanner: mockReplanner,
	}

	// Create the plan execute agent
	agent, err := New(ctx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
}

func TestPlanExecuteAgentWithReplan(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock agents
	mockPlanner := mockAdk.NewMockAgent(ctrl)
	mockExecutor := mockAdk.NewMockAgent(ctrl)
	mockReplanner := mockAdk.NewMockAgent(ctrl)

	// Set up expectations for the mock agents
	mockPlanner.EXPECT().Name(gomock.Any()).Return("Planner").AnyTimes()
	mockPlanner.EXPECT().Description(gomock.Any()).Return("a planner agent").AnyTimes()

	mockExecutor.EXPECT().Name(gomock.Any()).Return("Executor").AnyTimes()
	mockExecutor.EXPECT().Description(gomock.Any()).Return("an executor agent").AnyTimes()

	mockReplanner.EXPECT().Name(gomock.Any()).Return("Replanner").AnyTimes()
	mockReplanner.EXPECT().Description(gomock.Any()).Return("a replanner agent").AnyTimes()

	// Create a plan
	originalPlan := &defaultPlan{Steps: []string{"Step 1", "Step 2", "Step 3"}}
	// Create an updated plan with fewer steps (after replanning)
	updatedPlan := &defaultPlan{Steps: []string{"Updated Step 2", "Updated Step 3"}}
	// Create execute result
	originalExecuteResult := "Execution result for Step 1"
	updatedExecuteResult := "Execution result for Updated Step 2"

	// Create user input
	userInput := []adk.Message{schema.UserMessage("User task input")}

	finalResponse := &Response{Response: "Final response to user after executing all steps"}

	// Mock the planner Run method to set the original plan
	mockPlanner.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
			iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()

			// Set the plan in the session
			adk.AddSessionValue(ctx, PlanSessionKey, originalPlan)
			adk.AddSessionValue(ctx, UserInputSessionKey, userInput)

			// Send a message event
			planJSON, _ := sonic.MarshalString(originalPlan)
			msg := schema.AssistantMessage(planJSON, nil)
			event := adk.EventFromMessage(msg, nil, schema.Assistant, "")
			generator.Send(event)
			generator.Close()

			return iterator
		},
	).Times(1)

	// Mock the executor Run method to set the execute result
	mockExecutor.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
			iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()

			plan, _ := adk.GetSessionValue(ctx, PlanSessionKey)
			currentPlan := plan.(*defaultPlan)
			var msg adk.Message
			// Check if this is the first replanning (original plan has 3 steps)
			if len(currentPlan.Steps) == 3 {
				msg = schema.AssistantMessage(originalExecuteResult, nil)
				adk.AddSessionValue(ctx, ExecutedStepSessionKey, originalExecuteResult)
			} else {
				msg = schema.AssistantMessage(updatedExecuteResult, nil)
				adk.AddSessionValue(ctx, ExecutedStepSessionKey, updatedExecuteResult)
			}
			event := adk.EventFromMessage(msg, nil, schema.Assistant, "")
			generator.Send(event)
			generator.Close()

			return iterator
		},
	).Times(2)

	// Mock the replanner Run method to first update the plan, then respond to user
	mockReplanner.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
			iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()

			// First call: Update the plan
			// Get the current plan from the session
			plan, _ := adk.GetSessionValue(ctx, PlanSessionKey)
			currentPlan := plan.(*defaultPlan)

			// Check if this is the first replanning (original plan has 3 steps)
			if len(currentPlan.Steps) == 3 {
				// Send a message event with the updated plan
				planJSON, _ := sonic.MarshalString(updatedPlan)
				msg := schema.AssistantMessage(planJSON, nil)
				event := adk.EventFromMessage(msg, nil, schema.Assistant, "")
				generator.Send(event)

				// Set the updated plan & execute result in the session
				adk.AddSessionValue(ctx, PlanSessionKey, updatedPlan)
				adk.AddSessionValue(ctx, ExecutedStepsSessionKey, []ExecutedStep{{
					Step:   currentPlan.Steps[0],
					Result: originalExecuteResult,
				}})
			} else {
				// Second call: Respond to user
				responseJSON, err := sonic.MarshalString(finalResponse)
				assert.NoError(t, err)
				msg := schema.AssistantMessage(responseJSON, nil)
				event := adk.EventFromMessage(msg, nil, schema.Assistant, "")
				generator.Send(event)

				// Send exit action
				action := adk.NewExitAction()
				generator.Send(&adk.AgentEvent{Action: action})
			}

			generator.Close()
			return iterator
		},
	).Times(2)

	conf := &Config{
		Planner:   mockPlanner,
		Executor:  mockExecutor,
		Replanner: mockReplanner,
	}

	// Create the plan execute agent
	agent, err := New(ctx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, agent)

	// Run the agent
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	iterator := runner.Run(ctx, userInput)

	// Collect all events
	var events []*adk.AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	// Verify the events
	assert.Greater(t, len(events), 0)

	for i, event := range events {
		eventJSON, e := sonic.MarshalString(event)
		assert.NoError(t, e)
		t.Logf("event %d:\n%s", i, eventJSON)
	}
}
