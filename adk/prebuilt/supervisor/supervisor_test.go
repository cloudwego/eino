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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	mockAdk "github.com/cloudwego/eino/internal/mock/adk"
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
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.NotEqual(t, 0, len(event.Output.MessageOutput.Message.ToolCalls))

	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
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

	// agent2's output
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SubAgent2", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.Equal(t, subAgent2Msg.Content, event.Output.MessageOutput.Message.Content)

	// transfer back to supervisor
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.NotEqual(t, 0, len(event.Output.MessageOutput.Message.ToolCalls))

	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Tool, event.Output.MessageOutput.Role)
	assert.Equal(t, "SupervisorAgent", event.Action.TransferToAgent.DestAgentName)

	// finish
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.Equal(t, finishMsg.Content, event.Output.MessageOutput.Message.Content)
}

// mockSupervisor is a simple supervisor that performs concurrent transfers
type mockSupervisor struct {
	name    string
	targets []string
	times   int
}

func (a *mockSupervisor) Name(_ context.Context) string {
	return a.name
}

func (a *mockSupervisor) Description(_ context.Context) string {
	return "mock supervisor agent"
}

func (a *mockSupervisor) Run(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	if a.times > 0 {
		gen.Send(adk.EventFromMessage(schema.AssistantMessage("job done", nil), nil, schema.Assistant, ""))
		gen.Close()
		return iter
	}

	a.times++

	// Create assistant message with tool call for concurrent transfer
	toolCall := schema.ToolCall{
		ID:   "transfer-tool-call",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      adk.TransferToAgentToolName,
			Arguments: `{"agent_names":["` + a.targets[0] + `","` + a.targets[1] + `"]}`,
		},
	}
	assistantMsg := schema.AssistantMessage("", []schema.ToolCall{toolCall})
	gen.Send(adk.EventFromMessage(assistantMsg, nil, schema.Assistant, ""))

	// Create tool message for the transfer
	toolMsg := schema.ToolMessage(fmt.Sprintf("Successfully transfered to agents %v", a.targets), toolCall.ID,
		schema.WithToolName(adk.TransferToAgentToolName))
	transferEvent := adk.EventFromMessage(toolMsg, nil, schema.Tool, toolMsg.ToolName)
	transferEvent.Action = &adk.AgentAction{
		ConcurrentTransferToAgent: &adk.ConcurrentTransferToAgentAction{
			DestAgentNames: a.targets,
		},
	}
	gen.Send(transferEvent)
	gen.Close()

	return iter
}

// mockSimpleAgent is a basic agent that returns a simple message
type mockSimpleAgent struct {
	name string
	msg  string
}

func (a *mockSimpleAgent) Name(_ context.Context) string {
	return a.name
}

func (a *mockSimpleAgent) Description(_ context.Context) string {
	return "mock simple agent"
}

func (a *mockSimpleAgent) Run(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage(a.msg, nil), nil, schema.Assistant, ""))
	gen.Close()
	return iter
}

// mockInterruptibleResumableAgent interrupts on first run and resumes on second
type mockInterruptibleResumableAgent struct {
	name string
	t    *testing.T
}

func (a *mockInterruptibleResumableAgent) Name(_ context.Context) string {
	return a.name
}

func (a *mockInterruptibleResumableAgent) Description(_ context.Context) string {
	return "mock interruptible/resumable agent"
}

func (a *mockInterruptibleResumableAgent) Run(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("I will interrupt", nil), nil, schema.Assistant, ""))
	gen.Send(adk.Interrupt(ctx, "interrupt data"))
	gen.Close()
	return iter
}

func (a *mockInterruptibleResumableAgent) Resume(ctx context.Context, info *adk.ResumeInfo, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	assert.True(a.t, info.WasInterrupted)

	// Check if this agent is the target of the resume
	isResumeTarget, hasData, data := compose.GetResumeContext[string](ctx)
	if isResumeTarget && hasData {
		assert.Equal(a.t, "resume data", data)
	}

	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("I have resumed", nil), nil, schema.Assistant, ""))
	gen.Close()
	return iter
}

// TestNestedSupervisor_ConcurrentTransfer_WithInterruptAndResume tests a complex scenario:
// - Nested supervisor hierarchy
// - Concurrent transfers at two levels
// - Interrupt at grandchild level
// - Resume with targeted data
func TestNestedSupervisor_ConcurrentTransfer_WithInterruptAndResume(t *testing.T) {
	ctx := context.Background()

	// 1. Define the agent hierarchy
	grandChild1 := &mockSimpleAgent{name: "GrandChild1", msg: "GrandChild1 reporting"}
	grandChild2 := &mockInterruptibleResumableAgent{name: "GrandChild2", t: t}
	subSupervisor := &mockSupervisor{name: "SubSupervisor", targets: []string{"GrandChild1", "GrandChild2"}}

	subAgent1 := &mockSimpleAgent{name: "SubAgent1", msg: "SubAgent1 reporting"}
	superSupervisor := &mockSupervisor{name: "SuperSupervisor", targets: []string{"SubAgent1", "SubSupervisor"}}

	// 2. Build the nested supervisor hierarchy
	nestedSupervisor, err := New(ctx, &Config{
		Supervisor: subSupervisor,
		SubAgents:  []adk.Agent{grandChild1, grandChild2},
	})
	assert.NoError(t, err)

	topSupervisor, err := New(ctx, &Config{
		Supervisor: superSupervisor,
		SubAgents:  []adk.Agent{subAgent1, nestedSupervisor},
	})
	assert.NoError(t, err)

	// 3. Run the top-level supervisor and expect interrupt
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: topSupervisor, CheckPointStore: newMyStore()})
	aIter := runner.Run(ctx, []adk.Message{schema.UserMessage("start")},
		adk.WithCheckPointID("test-checkpoint"))

	var finalEvent *adk.AgentEvent
	var events []*adk.AgentEvent
	for event, ok := aIter.Next(); ok; event, ok = aIter.Next() {
		var role, content string
		var toolCalls []schema.ToolCall
		if event.Output != nil && event.Output.MessageOutput != nil {
			role = string(event.Output.MessageOutput.Role)
			content = event.Output.MessageOutput.Message.Content
			toolCalls = event.Output.MessageOutput.Message.ToolCalls
		}
		t.Logf("Event: Agent=%s, Role=%s, Content=%s, ToolCalls= %v, Interrupted=%v, transfer=%v, concurrentTransfer=%v",
			event.AgentName, role, content, toolCalls,
			event.Action != nil && event.Action.Interrupted != nil,
			event.Action != nil && event.Action.TransferToAgent != nil,
			event.Action != nil && event.Action.ConcurrentTransferToAgent != nil)

		events = append(events, event)
		if event.Action != nil && event.Action.Interrupted != nil {
			finalEvent = event
		}
	}

	if finalEvent == nil {
		t.Fatal("Should have received an interrupt event")
	}
	assert.Equal(t, "SuperSupervisor", finalEvent.AgentName, "Interrupt should propagate to top supervisor")

	// 4. Verify the execution sequence - handle complex concurrent execution
	assert.Equal(t, 8, len(events), "Should have 8 events in initial execution")

	// Group events by type and agent for flexible assertions
	transferEvents := make(map[string]bool)
	outputEvents := make(map[string]string)
	interruptEvents := make([]string, 0)

	for _, event := range events {
		if event.Action != nil && event.Action.ConcurrentTransferToAgent != nil {
			// Track concurrent transfer events
			transferEvents[event.AgentName] = true
		} else if event.Action != nil && event.Action.Interrupted != nil {
			// Track interrupt events
			interruptEvents = append(interruptEvents, event.AgentName)
		} else if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			// Track output events
			outputEvents[event.AgentName] = event.Output.MessageOutput.Message.Content
		}
	}

	// Verify we have the expected concurrent transfers
	assert.True(t, transferEvents["SuperSupervisor"], "Should have SuperSupervisor concurrent transfer")
	assert.True(t, transferEvents["SubSupervisor"], "Should have SubSupervisor concurrent transfer")

	// Verify we have the expected outputs from all agents
	assert.Equal(t, "SubAgent1 reporting", outputEvents["SubAgent1"])
	assert.Equal(t, "GrandChild1 reporting", outputEvents["GrandChild1"])
	assert.Equal(t, "I will interrupt", outputEvents["GrandChild2"])

	// Verify interrupt propagation
	assert.Equal(t, 1, len(interruptEvents), "Should have exactly one interrupt event")
	assert.Equal(t, "SuperSupervisor", interruptEvents[0], "Interrupt should propagate to top supervisor")

	// 5. Resume the execution with targeted resume data
	resumeIter, err := runner.TargetedResume(ctx, "test-checkpoint", map[string]any{
		"GrandChild2": "resume data",
	})
	assert.NoError(t, err)

	// 6. Verify the resume flow completes successfully
	var resumeEvents []*adk.AgentEvent
	for event, ok := resumeIter.Next(); ok; event, ok = resumeIter.Next() {
		var role, content string
		var toolCalls []schema.ToolCall
		if event.Output != nil && event.Output.MessageOutput != nil {
			role = string(event.Output.MessageOutput.Role)
			content = event.Output.MessageOutput.Message.Content
			toolCalls = event.Output.MessageOutput.Message.ToolCalls
		}
		t.Logf("Resume Event: Agent=%s, Role=%s, Content=%s, ToolCalls= %v, Interrupted=%v, transfer=%v, concurrentTransfer=%v",
			event.AgentName, role, content, toolCalls,
			event.Action != nil && event.Action.Interrupted != nil,
			event.Action != nil && event.Action.TransferToAgent != nil,
			event.Action != nil && event.Action.ConcurrentTransferToAgent != nil)
		resumeEvents = append(resumeEvents, event)
	}

	assert.Equal(t, 7, len(resumeEvents), "Should have 7 events in resume execution")

	// Group resume events by type and agent for flexible assertions
	resumeOutputs := make(map[string]string)
	resumeTransfers := make(map[string]string)
	resumeRoles := make(map[string]string)

	for _, event := range resumeEvents {
		if event.Action != nil && event.Action.TransferToAgent != nil {
			// Track transfer actions
			resumeTransfers[event.AgentName] = event.Action.TransferToAgent.DestAgentName
		} else if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			// Track output events
			resumeOutputs[event.AgentName] = event.Output.MessageOutput.Message.Content
			resumeRoles[event.AgentName] = string(event.Output.MessageOutput.Role)
		}
	}

	// Verify GrandChild2 resume
	assert.Equal(t, "I have resumed", resumeOutputs["GrandChild2"])

	// Verify SubSupervisor completion flow
	assert.Equal(t, "job done", resumeOutputs["SubSupervisor"])
	assert.Equal(t, "assistant", resumeRoles["SubSupervisor"])
	assert.Equal(t, "SubSupervisor", resumeTransfers["SubSupervisor"])

	// Verify SuperSupervisor completion flow
	assert.Equal(t, "job done", resumeOutputs["SuperSupervisor"])
	assert.Equal(t, "assistant", resumeRoles["SuperSupervisor"])
	assert.Equal(t, "SuperSupervisor", resumeTransfers["SuperSupervisor"])
}

func newMyStore() *myStore {
	return &myStore{
		m: map[string][]byte{},
	}
}

type myStore struct {
	m map[string][]byte
}

func (m *myStore) Set(_ context.Context, key string, value []byte) error {
	m.m[key] = value
	return nil
}

func (m *myStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := m.m[key]
	return v, ok, nil
}
