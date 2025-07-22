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
