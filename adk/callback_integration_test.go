/*
 * Copyright 2026 CloudWeGo Authors
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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/callbacks"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

type callbackRecorder struct {
	mu             sync.Mutex
	onStartCalled  bool
	onEndCalled    bool
	runInfo        *callbacks.RunInfo
	inputReceived  *AgentCallbackInput
	eventsReceived []*AgentEvent
	eventsDone     chan struct{}
	closeOnce      sync.Once
}

func newRecordingHandler(recorder *callbackRecorder) callbacks.Handler {
	recorder.eventsDone = make(chan struct{})
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component != ComponentOfAgent {
				return ctx
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			recorder.onStartCalled = true
			recorder.runInfo = info
			if agentInput := ConvAgentCallbackInput(input); agentInput != nil {
				recorder.inputReceived = agentInput
			}
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info.Component != ComponentOfAgent {
				return ctx
			}
			recorder.mu.Lock()
			recorder.onEndCalled = true
			recorder.runInfo = info
			recorder.mu.Unlock()

			if agentOutput := ConvAgentCallbackOutput(output); agentOutput != nil {
				if agentOutput.Events != nil {
					go func() {
						defer recorder.closeOnce.Do(func() { close(recorder.eventsDone) })
						for {
							event, ok := agentOutput.Events.Next()
							if !ok {
								break
							}
							recorder.mu.Lock()
							recorder.eventsReceived = append(recorder.eventsReceived, event)
							recorder.mu.Unlock()
						}
					}()
					return ctx
				}
			}
			recorder.closeOnce.Do(func() { close(recorder.eventsDone) })
			return ctx
		}).
		Build()
}

func TestCallbackOnStartInvocation(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("test response", nil), nil).
		Times(1)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent for callback",
		Instruction: "You are a test agent",
		Model:       cm,
	})
	assert.NoError(t, err)

	recorder := &callbackRecorder{}
	handler := newRecordingHandler(recorder)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent})
	iter := runner.Query(ctx, "hello", WithCallbacks(handler))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	<-recorder.eventsDone

	assert.True(t, recorder.onStartCalled, "OnStart should be called")
	assert.NotNil(t, recorder.inputReceived, "Input should be received")
	assert.NotNil(t, recorder.inputReceived.Input, "AgentInput should be set")
	assert.Len(t, recorder.inputReceived.Input.Messages, 1)
}

func TestCallbackOnEndInvocation(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("test response", nil), nil).
		Times(1)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent for callback",
		Instruction: "You are a test agent",
		Model:       cm,
	})
	assert.NoError(t, err)

	recorder := &callbackRecorder{}
	handler := newRecordingHandler(recorder)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent})
	iter := runner.Query(ctx, "hello", WithCallbacks(handler))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	<-recorder.eventsDone

	assert.True(t, recorder.onEndCalled, "OnEnd should be called")
	assert.NotEmpty(t, recorder.eventsReceived, "Events should be received")
}

func TestCallbackRunInfoForChatModelAgent(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("test response", nil), nil).
		Times(1)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestChatAgent",
		Description: "Test chat agent",
		Instruction: "You are a test agent",
		Model:       cm,
	})
	assert.NoError(t, err)

	recorder := &callbackRecorder{}
	handler := newRecordingHandler(recorder)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent})
	iter := runner.Query(ctx, "hello", WithCallbacks(handler))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	<-recorder.eventsDone

	assert.NotNil(t, recorder.runInfo)
	assert.Equal(t, "TestChatAgent", recorder.runInfo.Name)
	assert.Equal(t, "ChatModel", recorder.runInfo.Type)
	assert.Equal(t, ComponentOfAgent, recorder.runInfo.Component)
}

func TestMultipleCallbackHandlers(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("test response", nil), nil).
		Times(1)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent",
		Instruction: "You are a test agent",
		Model:       cm,
	})
	assert.NoError(t, err)

	recorder1 := &callbackRecorder{}
	recorder2 := &callbackRecorder{}
	handler1 := newRecordingHandler(recorder1)
	handler2 := newRecordingHandler(recorder2)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent})
	iter := runner.Query(ctx, "hello", WithCallbacks(handler1, handler2))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	<-recorder1.eventsDone
	<-recorder2.eventsDone

	assert.True(t, recorder1.onStartCalled, "Handler1 OnStart should be called")
	assert.True(t, recorder2.onStartCalled, "Handler2 OnStart should be called")
	assert.True(t, recorder1.onEndCalled, "Handler1 OnEnd should be called")
	assert.True(t, recorder2.onEndCalled, "Handler2 OnEnd should be called")

	assert.NotEmpty(t, recorder1.eventsReceived, "Handler1 should receive events")
	assert.NotEmpty(t, recorder2.eventsReceived, "Handler2 should receive events")
}

func TestCallbackWithWorkflowAgent(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm1 := mockModel.NewMockToolCallingChatModel(ctrl)
	cm1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("response 1", nil), nil).
		Times(1)
	cm1.EXPECT().WithTools(gomock.Any()).Return(cm1, nil).AnyTimes()

	cm2 := mockModel.NewMockToolCallingChatModel(ctrl)
	cm2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("response 2", nil), nil).
		Times(1)
	cm2.EXPECT().WithTools(gomock.Any()).Return(cm2, nil).AnyTimes()

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "Agent1",
		Description: "First agent",
		Instruction: "You are agent 1",
		Model:       cm1,
	})
	assert.NoError(t, err)

	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "Agent2",
		Description: "Second agent",
		Instruction: "You are agent 2",
		Model:       cm2,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "SequentialAgent",
		Description: "Sequential workflow",
		SubAgents:   []Agent{agent1, agent2},
	})
	assert.NoError(t, err)

	var callbackInfos []*callbacks.RunInfo
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component == ComponentOfAgent {
				callbackInfos = append(callbackInfos, info)
			}
			return ctx
		}).
		Build()

	runner := NewRunner(ctx, RunnerConfig{Agent: seqAgent})
	iter := runner.Query(ctx, "hello", WithCallbacks(handler))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	assert.NotEmpty(t, callbackInfos, "OnStart should be called for agents")
	foundAgent1 := false
	foundAgent2 := false
	for _, info := range callbackInfos {
		if info.Name == "Agent1" && info.Type == "ChatModel" {
			foundAgent1 = true
		}
		if info.Name == "Agent2" && info.Type == "ChatModel" {
			foundAgent2 = true
		}
	}
	assert.True(t, foundAgent1, "Agent1 callback should be invoked")
	assert.True(t, foundAgent2, "Agent2 callback should be invoked")
}

func TestCallbackEventsMatchAgentOutput(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	expectedContent := "This is the test response content"
	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage(expectedContent, nil), nil).
		Times(1)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "Test agent",
		Instruction: "You are a test agent",
		Model:       cm,
	})
	assert.NoError(t, err)

	recorder := &callbackRecorder{}
	handler := newRecordingHandler(recorder)

	var agentEvents []*AgentEvent
	runner := NewRunner(ctx, RunnerConfig{Agent: agent})
	iter := runner.Query(ctx, "hello", WithCallbacks(handler))
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		agentEvents = append(agentEvents, event)
	}

	<-recorder.eventsDone

	assert.NotEmpty(t, agentEvents, "Agent should emit events")
	assert.NotEmpty(t, recorder.eventsReceived, "Callback should receive events")

	foundExpectedContent := false
	for _, event := range recorder.eventsReceived {
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg := event.Output.MessageOutput.Message
			if msg != nil && msg.Content == expectedContent {
				foundExpectedContent = true
				break
			}
		}
	}
	assert.True(t, foundExpectedContent, "Callback events should contain the expected content")
}

func TestCallbackOnEndForWorkflowAgent(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm1 := mockModel.NewMockToolCallingChatModel(ctrl)
	cm1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("response 1", nil), nil).
		Times(1)
	cm1.EXPECT().WithTools(gomock.Any()).Return(cm1, nil).AnyTimes()

	cm2 := mockModel.NewMockToolCallingChatModel(ctrl)
	cm2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("response 2", nil), nil).
		Times(1)
	cm2.EXPECT().WithTools(gomock.Any()).Return(cm2, nil).AnyTimes()

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "Agent1",
		Description: "First agent",
		Instruction: "You are agent 1",
		Model:       cm1,
	})
	assert.NoError(t, err)

	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "Agent2",
		Description: "Second agent",
		Instruction: "You are agent 2",
		Model:       cm2,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "SequentialAgent",
		Description: "Sequential workflow",
		SubAgents:   []Agent{agent1, agent2},
	})
	assert.NoError(t, err)

	recorder := &callbackRecorder{}
	handler := newRecordingHandler(recorder)

	runner := NewRunner(ctx, RunnerConfig{Agent: seqAgent})
	iter := runner.Query(ctx, "hello", WithCallbacks(handler))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	<-recorder.eventsDone

	assert.True(t, recorder.getOnStartCalled(), "OnStart should be called for workflow agent")
	assert.True(t, recorder.getOnEndCalled(), "OnEnd should be called for workflow agent")
	assert.NotEmpty(t, recorder.getEventsReceived(), "Events should be received for workflow agent")
}
