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

package supervisor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockAdk "github.com/cloudwego/eino/internal/mock/adk"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestNewSupervisor tests the New function
func TestNewSupervisor(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock agents
	supervisorAgent := mockAdk.NewMockAgent(ctrl)
	subAgent1 := mockAdk.NewMockAgent(ctrl)
	subAgent2 := mockAdk.NewMockAgent(ctrl)

	supervisorAgent.EXPECT().Name(gomock.Any()).Return("SupervisorAgent").AnyTimes()
	subAgent1.EXPECT().Name(gomock.Any()).Return("SubAgent1").AnyTimes()
	subAgent2.EXPECT().Name(gomock.Any()).Return("SubAgent2").AnyTimes()

	aMsg, tMsg := adk.GenTransferMessages(ctx, "SubAgent1")
	i, g := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	g.Send(adk.EventFromMessage(aMsg, nil, schema.Assistant, ""))
	event := adk.EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
	event.Action = &adk.AgentAction{TransferToAgent: &adk.TransferToAgentAction{DestAgentName: "SubAgent1"}}
	g.Send(event)
	g.Close()
	supervisorAgent.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i).Times(1)

	i, g = adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	subAgent1Msg := schema.AssistantMessage("SubAgent1", nil)
	g.Send(adk.EventFromMessage(subAgent1Msg, nil, schema.Assistant, ""))
	g.Close()
	subAgent1.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i).Times(1)

	aMsg, tMsg = adk.GenTransferMessages(ctx, "SubAgent2 message")
	i, g = adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	g.Send(adk.EventFromMessage(aMsg, nil, schema.Assistant, ""))
	event = adk.EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
	event.Action = &adk.AgentAction{TransferToAgent: &adk.TransferToAgentAction{DestAgentName: "SubAgent2"}}
	g.Send(event)
	g.Close()
	supervisorAgent.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i).Times(1)

	i, g = adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	subAgent2Msg := schema.AssistantMessage("SubAgent2 message", nil)
	g.Send(adk.EventFromMessage(subAgent2Msg, nil, schema.Assistant, ""))
	g.Close()
	subAgent2.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i).Times(1)

	i, g = adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	finishMsg := schema.AssistantMessage("finish", nil)
	g.Send(adk.EventFromMessage(finishMsg, nil, schema.Assistant, ""))
	g.Close()
	supervisorAgent.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i).Times(1)

	conf := &Config{
		Supervisor: supervisorAgent,
		SubAgents:  []adk.Agent{subAgent1, subAgent2},
	}

	multiAgent, err := New(ctx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, multiAgent)
	assert.Equal(t, "SupervisorAgent", multiAgent.Name(ctx))

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: multiAgent})
	aIter := runner.Run(ctx, []adk.Message{schema.UserMessage("test")})

	// transfer to agent1
	event, ok := aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.NotEqual(t, 0, len(event.Output.MessageOutput.Message.ToolCalls))

	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Tool, event.Output.MessageOutput.Role)
	assert.Equal(t, "SubAgent1", event.Action.TransferToAgent.DestAgentName)

	// agent1's output
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SubAgent1", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.Equal(t, subAgent1Msg.Content, event.Output.MessageOutput.Message.Content)

	// transfer back to supervisor
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SubAgent1", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.NotEqual(t, 0, len(event.Output.MessageOutput.Message.ToolCalls))

	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SubAgent1", event.AgentName)
	assert.Equal(t, schema.Tool, event.Output.MessageOutput.Role)
	assert.Equal(t, "SupervisorAgent", event.Action.TransferToAgent.DestAgentName)

	// transfer to agent2
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.NotEqual(t, 0, len(event.Output.MessageOutput.Message.ToolCalls))

	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Tool, event.Output.MessageOutput.Role)
	assert.Equal(t, "SubAgent2", event.Action.TransferToAgent.DestAgentName)

	// agent1's output
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SubAgent2", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.Equal(t, subAgent2Msg.Content, event.Output.MessageOutput.Message.Content)

	// transfer back to supervisor
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SubAgent2", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.NotEqual(t, 0, len(event.Output.MessageOutput.Message.ToolCalls))

	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SubAgent2", event.AgentName)
	assert.Equal(t, schema.Tool, event.Output.MessageOutput.Role)
	assert.Equal(t, "SupervisorAgent", event.Action.TransferToAgent.DestAgentName)

	// finish
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.Equal(t, finishMsg.Content, event.Output.MessageOutput.Message.Content)
}

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

type checkpointStore struct {
	data map[string][]byte
}

func newCheckpointStore() *checkpointStore {
	return &checkpointStore{data: make(map[string][]byte)}
}

func (s *checkpointStore) Set(_ context.Context, key string, value []byte) error {
	s.data[key] = value
	return nil
}

func (s *checkpointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := s.data[key]
	return v, ok, nil
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

func TestNestedSupervisorInterruptResume(t *testing.T) {
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockOuterSupervisorModel := mockModel.NewMockToolCallingChatModel(ctrl)
	mockInnerSupervisorModel := mockModel.NewMockToolCallingChatModel(ctrl)
	mockWorkerModel := mockModel.NewMockToolCallingChatModel(ctrl)

	paymentTool := &approvableTool{name: "process_payment", t: t}

	userInput := []adk.Message{schema.UserMessage("Process a payment of $1000")}

	mockWorkerModel.EXPECT().WithTools(gomock.Any()).Return(mockWorkerModel, nil).AnyTimes()

	workerToolCallMsg := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "call_payment_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "process_payment",
				Arguments: `{"action": "process $1000 payment"}`,
			},
		},
	})
	mockWorkerModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(workerToolCallMsg, nil).Times(1)

	workerCompletionMsg := schema.AssistantMessage("Payment processed successfully", nil)
	mockWorkerModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(workerCompletionMsg, nil).AnyTimes()

	workerAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "payment_worker",
		Description: "the agent responsible for processing payments",
		Instruction: "You are a payment processing worker. Use the process_payment tool to handle payments.",
		Model:       mockWorkerModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{paymentTool},
			},
		},
	})
	assert.NoError(t, err)

	mockInnerSupervisorModel.EXPECT().WithTools(gomock.Any()).Return(mockInnerSupervisorModel, nil).AnyTimes()

	innerTransferMsg := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "inner_transfer_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "transfer_to_agent",
				Arguments: `{"agent_name":"payment_worker"}`,
			},
		},
	})
	mockInnerSupervisorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(innerTransferMsg, nil).Times(1)

	innerFinalMsg := schema.AssistantMessage("Payment has been processed and approved.", nil)
	mockInnerSupervisorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(innerFinalMsg, nil).AnyTimes()

	innerSupervisorChatAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "payment_supervisor",
		Description: "the supervisor agent responsible for payment operations",
		Instruction: "You are a payment supervisor. Delegate payment tasks to payment_worker.",
		Model:       mockInnerSupervisorModel,
		Exit:        &adk.ExitTool{},
	})
	assert.NoError(t, err)

	innerSupervisorAgent, err := New(ctx, &Config{
		Supervisor: innerSupervisorChatAgent,
		SubAgents:  []adk.Agent{workerAgent},
	})
	assert.NoError(t, err)

	innerSupervisorWrapped := &namedAgent{
		ResumableAgent: innerSupervisorAgent,
		name:           "payment_department",
		description:    "the department responsible for all payment-related operations",
	}

	mockOuterSupervisorModel.EXPECT().WithTools(gomock.Any()).Return(mockOuterSupervisorModel, nil).AnyTimes()

	outerTransferMsg := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "outer_transfer_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "transfer_to_agent",
				Arguments: `{"agent_name":"payment_department"}`,
			},
		},
	})
	mockOuterSupervisorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(outerTransferMsg, nil).Times(1)

	outerFinalMsg := schema.AssistantMessage("The payment request has been fully processed by the payment department.", nil)
	mockOuterSupervisorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(outerFinalMsg, nil).AnyTimes()

	outerSupervisorChatAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "company_coordinator",
		Description: "the top-level coordinator for company operations",
		Instruction: "You are the company coordinator. Route payment requests to payment_department.",
		Model:       mockOuterSupervisorModel,
		Exit:        &adk.ExitTool{},
	})
	assert.NoError(t, err)

	outerSupervisorAgent, err := New(ctx, &Config{
		Supervisor: outerSupervisorChatAgent,
		SubAgents:  []adk.Agent{innerSupervisorWrapped},
	})
	assert.NoError(t, err)

	outerSupervisorWrapped := &namedAgent{
		ResumableAgent: outerSupervisorAgent,
		name:           "headquarters",
		description:    "the company headquarters that coordinates all departments",
	}

	store := newCheckpointStore()
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           outerSupervisorWrapped,
		CheckPointStore: store,
	})

	t.Log("========================================")
	t.Log("Starting Nested Supervisor Integration Test (with namedAgent wrappers)")
	t.Log("Hierarchy: headquarters(wrapper) -> company_coordinator -> payment_department(wrapper) -> payment_supervisor -> payment_worker -> process_payment tool")
	t.Log("========================================")

	checkpointID := "test-nested-supervisor-1"
	iter := runner.Run(ctx, userInput, adk.WithCheckPointID(checkpointID))

	var interruptEvent *adk.AgentEvent
	eventCount := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		eventCount++

		if event.Action != nil && event.Action.Interrupted != nil {
			interruptEvent = event
			t.Log("INTERRUPT DETECTED - Deep interrupt from tool within nested supervisor")
			break
		}
	}

	if interruptEvent == nil {
		t.Fatal("Expected an interrupt event from the process_payment tool, but none was received")
	}

	assert.NotNil(t, interruptEvent.Action.Interrupted, "Should have interrupt info")
	assert.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts, "Should have interrupt contexts")

	var toolInterruptID string
	for _, intCtx := range interruptEvent.Action.Interrupted.InterruptContexts {
		if intCtx.IsRootCause {
			toolInterruptID = intCtx.ID
			break
		}
	}
	assert.NotEmpty(t, toolInterruptID, "Should have a root cause interrupt ID")

	t.Logf("Resuming with approval for interrupt ID: %s", toolInterruptID)

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

	var hasToolResponse, hasTransferBack bool
	for _, event := range resumeEvents {
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg := event.Output.MessageOutput.Message
			if msg != nil && msg.Role == "tool" && strings.Contains(msg.Content, "executed successfully") {
				hasToolResponse = true
			}
		}
		if event.Action != nil && event.Action.TransferToAgent != nil {
			if event.Action.TransferToAgent.DestAgentName == "company_coordinator" {
				hasTransferBack = true
			}
		}
	}

	assert.True(t, hasToolResponse, "Should have tool response indicating successful payment processing")
	assert.True(t, hasTransferBack, "Should have transfer back to outer supervisor indicating completion")
}
