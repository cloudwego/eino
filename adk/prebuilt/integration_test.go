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

package prebuilt

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/planexecute"
	"github.com/cloudwego/eino/adk/prebuilt/supervisor"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

type approvalInfo struct {
	ToolName        string
	ArgumentsInJSON string
	ToolCallID      string
}

func (ai *approvalInfo) String() string {
	return fmt.Sprintf("tool '%s' interrupted with arguments '%s', waiting for approval",
		ai.ToolName, ai.ArgumentsInJSON)
}

type approvalResult struct {
	Approved         bool
	DisapproveReason *string
}

func init() {
	schema.Register[*approvalInfo]()
	schema.Register[*approvalResult]()
}

type approvableTool struct {
	name string
	t    *testing.T
}

func (m *approvableTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: m.name,
		Desc: "A tool that requires approval before execution",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"action": {Type: schema.String, Desc: "The action to perform"},
		}),
	}, nil
}

func (m *approvableTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	wasInterrupted, _, storedArguments := compose.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return "", compose.StatefulInterrupt(ctx, &approvalInfo{
			ToolName:        m.name,
			ArgumentsInJSON: argumentsInJSON,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, argumentsInJSON)
	}

	isResumeTarget, hasData, data := compose.GetResumeContext[*approvalResult](ctx)
	if !isResumeTarget {
		return "", compose.StatefulInterrupt(ctx, &approvalInfo{
			ToolName:        m.name,
			ArgumentsInJSON: storedArguments,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, storedArguments)
	}

	if !hasData {
		return "", fmt.Errorf("tool '%s' resumed with no data", m.name)
	}

	if data.Approved {
		return fmt.Sprintf("Tool '%s' executed successfully with args: %s", m.name, storedArguments), nil
	}

	if data.DisapproveReason != nil {
		return fmt.Sprintf("Tool '%s' disapproved, reason: %s", m.name, *data.DisapproveReason), nil
	}

	return fmt.Sprintf("Tool '%s' disapproved", m.name), nil
}

type integrationCheckpointStore struct {
	data map[string][]byte
}

func newIntegrationCheckpointStore() *integrationCheckpointStore {
	return &integrationCheckpointStore{data: make(map[string][]byte)}
}

func (s *integrationCheckpointStore) Set(_ context.Context, key string, value []byte) error {
	s.data[key] = value
	return nil
}

func (s *integrationCheckpointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := s.data[key]
	return v, ok, nil
}

type defaultPlan struct {
	Steps []string `json:"steps"`
}

func (p *defaultPlan) FirstStep() string {
	if len(p.Steps) == 0 {
		return ""
	}
	return p.Steps[0]
}

func (p *defaultPlan) MarshalJSON() ([]byte, error) {
	type planTyp defaultPlan
	return sonic.Marshal((*planTyp)(p))
}

func (p *defaultPlan) UnmarshalJSON(bytes []byte) error {
	type planTyp defaultPlan
	return sonic.Unmarshal(bytes, (*planTyp)(p))
}

type namedAgent struct {
	adk.ResumableAgent
	name        string
	description string
}

func (n *namedAgent) Name(_ context.Context) string {
	return n.name
}

func (n *namedAgent) Description(_ context.Context) string {
	return n.description
}

func formatRunPath(runPath []adk.RunStep) string {
	if len(runPath) == 0 {
		return "[]"
	}
	var parts []string
	for _, step := range runPath {
		parts = append(parts, step.String())
	}
	return "[" + strings.Join(parts, " -> ") + "]"
}

func formatAgentEventIntegration(event *adk.AgentEvent) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("{AgentName: %q, RunPath: %s", event.AgentName, formatRunPath(event.RunPath)))
	if event.Output != nil {
		if event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			msg := event.Output.MessageOutput.Message
			sb.WriteString(fmt.Sprintf(", Output.Message: {Role: %q, Content: %q}", msg.Role, msg.Content))
		}
	}
	if event.Action != nil {
		if event.Action.Interrupted != nil {
			sb.WriteString(fmt.Sprintf(", Action.Interrupted: {%d contexts}", len(event.Action.Interrupted.InterruptContexts)))
		}
		if event.Action.BreakLoop != nil {
			sb.WriteString(fmt.Sprintf(", Action.BreakLoop: {From: %q, Done: %v}", event.Action.BreakLoop.From, event.Action.BreakLoop.Done))
		}
		if event.Action.TransferToAgent != nil {
			sb.WriteString(fmt.Sprintf(", Action.TransferToAgent: {Dest: %q}", event.Action.TransferToAgent.DestAgentName))
		}
	}
	if event.Err != nil {
		sb.WriteString(fmt.Sprintf(", Err: %v", event.Err))
	}
	sb.WriteString("}")
	return sb.String()
}

func TestSupervisorWithPlanExecuteInterruptResume(t *testing.T) {
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSupervisorModel := mockModel.NewMockToolCallingChatModel(ctrl)
	mockPlannerModel := mockModel.NewMockToolCallingChatModel(ctrl)
	mockExecutorModel := mockModel.NewMockToolCallingChatModel(ctrl)
	mockReplannerModel := mockModel.NewMockToolCallingChatModel(ctrl)

	budgetTool := &approvableTool{name: "allocate_budget", t: t}

	plan := &defaultPlan{Steps: []string{"Allocate budget for the project", "Complete task"}}
	userInput := []adk.Message{schema.UserMessage("Set up a new project with budget allocation")}

	plannerModelWithTools := mockModel.NewMockToolCallingChatModel(ctrl)
	mockPlannerModel.EXPECT().WithTools(gomock.Any()).Return(plannerModelWithTools, nil).AnyTimes()

	planJSON, _ := sonic.MarshalString(plan)
	plannerResponse := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "plan_call_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "plan",
				Arguments: planJSON,
			},
		},
	})
	plannerModelWithTools.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input []*schema.Message, opts ...interface{}) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			go func() {
				defer sw.Close()
				sw.Send(plannerResponse, nil)
			}()
			return sr, nil
		},
	).Times(1)

	mockExecutorModel.EXPECT().WithTools(gomock.Any()).Return(mockExecutorModel, nil).AnyTimes()

	toolCallMsg := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "call_budget_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "allocate_budget",
				Arguments: `{"action": "allocate $50000 for project"}`,
			},
		},
	})
	mockExecutorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(toolCallMsg, nil).Times(1)

	completionMsg := schema.AssistantMessage("Budget allocated successfully", nil)
	mockExecutorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(completionMsg, nil).AnyTimes()

	replannerModelWithTools := mockModel.NewMockToolCallingChatModel(ctrl)
	mockReplannerModel.EXPECT().WithTools(gomock.Any()).Return(replannerModelWithTools, nil).AnyTimes()

	respondResponse := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "respond_call_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "respond",
				Arguments: `{"response":"Project setup completed with budget allocation"}`,
			},
		},
	})
	replannerModelWithTools.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input []*schema.Message, opts ...interface{}) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			go func() {
				defer sw.Close()
				sw.Send(respondResponse, nil)
			}()
			return sr, nil
		},
	).AnyTimes()

	plannerAgent, err := planexecute.NewPlanner(ctx, &planexecute.PlannerConfig{
		ToolCallingChatModel: mockPlannerModel,
	})
	assert.NoError(t, err)

	executorAgent, err := planexecute.NewExecutor(ctx, &planexecute.ExecutorConfig{
		Model: mockExecutorModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{budgetTool},
			},
		},
	})
	assert.NoError(t, err)

	replannerAgent, err := planexecute.NewReplanner(ctx, &planexecute.ReplannerConfig{
		ChatModel: mockReplannerModel,
	})
	assert.NoError(t, err)

	planExecuteAgent, err := planexecute.New(ctx, &planexecute.Config{
		Planner:       plannerAgent,
		Executor:      executorAgent,
		Replanner:     replannerAgent,
		MaxIterations: 10,
	})
	assert.NoError(t, err)

	projectAgent := &namedAgent{
		ResumableAgent: planExecuteAgent,
		name:           "project_execution_agent",
		description:    "the agent responsible for complex project execution tasks",
	}

	var pa adk.Agent
	pa = projectAgent

	_, ok := pa.(adk.ResumableAgent)
	assert.True(t, ok)

	mockSupervisorModel.EXPECT().WithTools(gomock.Any()).Return(mockSupervisorModel, nil).AnyTimes()

	transferToProjectMsg := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "transfer_call_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "transfer_to_agent",
				Arguments: `{"agent_name":"project_execution_agent"}`,
			},
		},
	})
	mockSupervisorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(transferToProjectMsg, nil).Times(1)

	finalSupervisorMsg := schema.AssistantMessage("Project setup completed successfully with budget allocation approved.", nil)
	mockSupervisorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(finalSupervisorMsg, nil).AnyTimes()

	supervisorChatAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "project_manager",
		Description: "the supervisor agent responsible for coordinating project management tasks",
		Instruction: "You are a project manager supervisor. Delegate complex project tasks to project_execution_agent.",
		Model:       mockSupervisorModel,
		Exit:        &adk.ExitTool{},
	})
	assert.NoError(t, err)

	supervisorAgent, err := supervisor.New(ctx, &supervisor.Config{
		Supervisor: supervisorChatAgent,
		SubAgents:  []adk.Agent{projectAgent},
	})
	assert.NoError(t, err)

	store := newIntegrationCheckpointStore()
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           supervisorAgent,
		CheckPointStore: store,
	})

	t.Log("========================================")
	t.Log("Starting Supervisor + PlanExecute Integration Test")
	t.Log("========================================")

	checkpointID := "test-supervisor-plan_execute-1"
	iter := runner.Run(ctx, userInput, adk.WithCheckPointID(checkpointID))

	var interruptEvent *adk.AgentEvent
	eventCount := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		eventCount++
		t.Logf("Event %d: %s", eventCount, formatAgentEventIntegration(event))

		if event.Err != nil {
			t.Logf("Event has error: %v", event.Err)
		}

		if event.Action != nil && event.Action.Interrupted != nil {
			interruptEvent = event
			t.Log("========================================")
			t.Log("INTERRUPT DETECTED - Deep interrupt from tool within executor")
			t.Log("========================================")
			break
		}
	}

	if interruptEvent == nil {
		t.Fatal("Expected an interrupt event from the approvable tool, but none was received")
	}

	assert.NotNil(t, interruptEvent.Action.Interrupted, "Should have interrupt info")
	assert.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts, "Should have interrupt contexts")

	t.Logf("Interrupt event received with %d contexts", len(interruptEvent.Action.Interrupted.InterruptContexts))
	for i, ctx := range interruptEvent.Action.Interrupted.InterruptContexts {
		t.Logf("Interrupt context %d: ID=%s, Info=%v, IsRootCause=%v", i, ctx.ID, ctx.Info, ctx.IsRootCause)
	}

	var toolInterruptID string
	for _, intCtx := range interruptEvent.Action.Interrupted.InterruptContexts {
		if intCtx.IsRootCause {
			toolInterruptID = intCtx.ID
			break
		}
	}
	assert.NotEmpty(t, toolInterruptID, "Should have a root cause interrupt ID")

	t.Log("========================================")
	t.Logf("Resuming with approval for interrupt ID: %s", toolInterruptID)
	t.Log("========================================")

	resumeIter, err := runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{
		Targets: map[string]any{
			toolInterruptID: &approvalResult{Approved: true},
		},
	})
	assert.NoError(t, err, "Resume should not error")
	assert.NotNil(t, resumeIter, "Resume iterator should not be nil")

	var resumeEvents []*adk.AgentEvent
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		resumeEvents = append(resumeEvents, event)
	}

	assert.NotEmpty(t, resumeEvents, "Should have resume events after approval")

	for _, event := range resumeEvents {
		assert.NoError(t, event.Err, "Resume event should not have error")
	}

	var hasToolResponse, hasBreakLoop bool
	for _, event := range resumeEvents {
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg := event.Output.MessageOutput.Message
			if msg != nil && msg.Role == "tool" && strings.Contains(msg.Content, "executed successfully") {
				hasToolResponse = true
			}
		}
		if event.Action != nil && event.Action.BreakLoop != nil && event.Action.BreakLoop.Done {
			hasBreakLoop = true
		}
	}

	assert.True(t, hasToolResponse, "Should have tool response indicating successful execution")
	assert.True(t, hasBreakLoop, "Should have break loop action indicating task completion")
}
