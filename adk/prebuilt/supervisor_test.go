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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	mockAdk "github.com/cloudwego/eino/internal/mock/adk"
	"github.com/cloudwego/eino/schema"
)

// TestNewSupervisor tests the NewSupervisor function
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

	// Create the SupervisorConfig
	conf := &SupervisorConfig{
		Supervisor: supervisorAgent,
		SubAgents:  []adk.Agent{subAgent1, subAgent2},
	}

	multiAgent, err := NewSupervisor(ctx, conf)
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

	// agent2's output
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

func TestSupervisorInterruptResume(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock agents
	supervisorAgent := mockAdk.NewMockResumableAgent(ctrl)
	subAgent1 := mockAdk.NewMockResumableAgent(ctrl)

	supervisorAgent.EXPECT().Name(gomock.Any()).Return("SupervisorAgent").AnyTimes()
	subAgent1.EXPECT().Name(gomock.Any()).Return("SubAgent1").AnyTimes()

	aMsg, tMsg := adk.GenTransferMessages(ctx, "SubAgent1")
	i, g := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	g.Send(adk.EventFromMessage(aMsg, nil, schema.Assistant, ""))
	event := adk.EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
	event.Action = &adk.AgentAction{TransferToAgent: &adk.TransferToAgentAction{DestAgentName: "SubAgent1"}}
	g.Send(event)
	g.Close()
	supervisorAgent.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i).Times(1)

	i1, g1 := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	g1.Send(&adk.AgentEvent{Action: &adk.AgentAction{Interrupted: &adk.InterruptInfo{Data: "interrupt info for sub agent 1"}}})
	g1.Close()
	subAgent1.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i1).Times(1)

	// Create the SupervisorConfig
	conf := &SupervisorConfig{
		Supervisor: supervisorAgent,
		SubAgents:  []adk.Agent{subAgent1},
	}

	multiAgent, err := NewSupervisor(ctx, conf)
	assert.NoError(t, err)
	assert.NotNil(t, multiAgent)
	assert.Equal(t, "SupervisorAgent", multiAgent.Name(ctx))

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           multiAgent,
		CheckPointStore: newMyStore(),
	})
	aIter := runner.Run(ctx, []adk.Message{schema.UserMessage("test")}, adk.WithCheckPointID("test_id"))

	event, ok := aIter.Next()
	assert.True(t, ok)
	event, ok = aIter.Next()
	assert.True(t, ok)
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "interrupt info for sub agent 1", event.Action.Interrupted.Data)

	i2, g2 := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	g2.Send(adk.EventFromMessage(schema.AssistantMessage("from_sub_agent1", nil), nil, schema.Assistant, ""))
	g2.Close()
	subAgent1.EXPECT().Resume(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, info *adk.ResumeInfo, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
			o := adk.GetCommonOptions(nil, opts...)
			checkpointID, ok := o.CheckPointID()
			assert.True(t, ok)
			assert.Equal(t, "test_id_SupervisorAgent->SubAgent1", checkpointID)
			return i2
		}).Times(1)

	i3, g3 := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	g3.Send(adk.EventFromMessage(schema.AssistantMessage("from_supervisor", nil), nil, schema.Assistant, ""))
	g3.Close()
	supervisorAgent.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i3).Times(1)

	opt := adk.WithHistoryModifier(func(ctx context.Context, messages []adk.Message) []adk.Message {
		t.Log("modified message")
		return messages
	}).DesignateAgent("SubAgent1")
	aIter1, err := runner.Resume(ctx, "test_id", opt)
	assert.NoError(t, err)

	event, ok = aIter1.Next()
	assert.True(t, ok)
	assert.Equal(t, "from_sub_agent1", event.Output.MessageOutput.Message.Content)
	event, ok = aIter1.Next()
	assert.True(t, ok)
	assert.Equal(t, fmt.Sprintf(adk.TransferToAgentToolName, "SupervisorAgent"), event.Output.MessageOutput.Message.ToolCalls[0].Function.Name)
	event, ok = aIter1.Next()
	assert.True(t, ok)
	event, ok = aIter1.Next()
	assert.True(t, ok)
	assert.Equal(t, "from_supervisor", event.Output.MessageOutput.Message.Content)
	event, ok = aIter1.Next()
	assert.False(t, ok)
}

func TestSupervisorConcurrentTransfer(t *testing.T) {
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

	i, g := adk.NewAsyncIteratorPair[*adk.AgentEvent]()

	aMsg, tMsg := adk.GenTransferMessages(ctx, "SubAgent1")
	g.Send(adk.EventFromMessage(aMsg, nil, schema.Assistant, ""))
	event := adk.EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
	event.Action = &adk.AgentAction{TransferToAgent: &adk.TransferToAgentAction{DestAgentName: "SubAgent1"}}
	g.Send(event)

	aMsg2, tMsg2 := adk.GenTransferMessages(ctx, "SubAgent2")
	g.Send(adk.EventFromMessage(aMsg2, nil, schema.Assistant, ""))
	event2 := adk.EventFromMessage(tMsg2, nil, schema.Tool, tMsg2.ToolName)
	event2.Action = &adk.AgentAction{TransferToAgent: &adk.TransferToAgentAction{DestAgentName: "SubAgent2"}}
	g.Send(event2)

	g.Close()
	supervisorAgent.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i).Times(1)

	i, g = adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	subAgent1Msg := schema.AssistantMessage("SubAgent1", nil)
	g.Send(adk.EventFromMessage(subAgent1Msg, nil, schema.Assistant, ""))
	g.Close()
	subAgent1.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(i).Times(1)

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

	// Create the SupervisorConfig
	conf := &SupervisorConfig{
		Supervisor: supervisorAgent,
		SubAgents:  []adk.Agent{subAgent1, subAgent2},
	}

	multiAgent, err := NewSupervisor(ctx, conf)
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

	// agent1, agent 2 output, followed by transfer back to supervisor respectively.
	// Order not deterministic since they are concurrent.
	for i := 0; i < 6; i++ {
		event, ok = aIter.Next()
		assert.True(t, ok)
	}

	// finish
	event, ok = aIter.Next()
	assert.True(t, ok)
	assert.Equal(t, "SupervisorAgent", event.AgentName)
	assert.Equal(t, schema.Assistant, event.Output.MessageOutput.Role)
	assert.Equal(t, finishMsg.Content, event.Output.MessageOutput.Message.Content)
}

func newMyStore() *myStore {
	return &myStore{
		m: map[string][]byte{},
	}
}

type myStore struct {
	m map[string][]byte
}

func (m *myStore) Set(ctx context.Context, key string, value []byte) error {
	m.m[key] = value
	return nil
}

func (m *myStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, ok := m.m[key]
	return v, ok, nil
}
