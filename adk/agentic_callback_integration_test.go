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
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type agenticCallbackRecorder struct {
	mu             sync.Mutex
	onStartCalled  bool
	onEndCalled    bool
	runInfo        *callbacks.RunInfo
	inputReceived  *AgenticCallbackInput
	eventsReceived []*TypedAgentEvent[*schema.AgenticMessage]
	eventsDone     chan struct{}
	closeOnce      sync.Once
}

func (r *agenticCallbackRecorder) getOnStartCalled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onStartCalled
}

func (r *agenticCallbackRecorder) getOnEndCalled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onEndCalled
}

func (r *agenticCallbackRecorder) getEventsReceived() []*TypedAgentEvent[*schema.AgenticMessage] {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*TypedAgentEvent[*schema.AgenticMessage], len(r.eventsReceived))
	copy(result, r.eventsReceived)
	return result
}

func newAgenticRecordingHandler(recorder *agenticCallbackRecorder) callbacks.Handler {
	recorder.eventsDone = make(chan struct{})
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component != ComponentOfAgenticAgent {
				return ctx
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			recorder.onStartCalled = true
			recorder.runInfo = info
			if agentInput := ConvAgenticCallbackInput(input); agentInput != nil {
				recorder.inputReceived = agentInput
			}
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info.Component != ComponentOfAgenticAgent {
				return ctx
			}
			recorder.mu.Lock()
			recorder.onEndCalled = true
			recorder.runInfo = info
			recorder.mu.Unlock()

			if agentOutput := ConvAgenticCallbackOutput(output); agentOutput != nil {
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

func TestAgenticCallbackOnStartInvocation(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("test response"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TestAgent",
		Description: "Test agent for callback",
		Instruction: "You are a test agent",
		Model:       m,
	})
	assert.NoError(t, err)

	recorder := &agenticCallbackRecorder{}
	handler := newAgenticRecordingHandler(recorder)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
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

func TestAgenticCallbackOnEndInvocation(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("test response"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TestAgent",
		Description: "Test agent for callback",
		Instruction: "You are a test agent",
		Model:       m,
	})
	assert.NoError(t, err)

	recorder := &agenticCallbackRecorder{}
	handler := newAgenticRecordingHandler(recorder)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
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

func TestAgenticCallbackRunInfo(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("test response"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TestChatAgent",
		Description: "Test chat agent",
		Instruction: "You are a test agent",
		Model:       m,
	})
	assert.NoError(t, err)

	recorder := &agenticCallbackRecorder{}
	handler := newAgenticRecordingHandler(recorder)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
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
	assert.Equal(t, ComponentOfAgenticAgent, recorder.runInfo.Component)
}

func TestAgenticCallbackMultipleHandlers(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("test response"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TestAgent",
		Description: "Test agent",
		Instruction: "You are a test agent",
		Model:       m,
	})
	assert.NoError(t, err)

	recorder1 := &agenticCallbackRecorder{}
	recorder2 := &agenticCallbackRecorder{}
	handler1 := newAgenticRecordingHandler(recorder1)
	handler2 := newAgenticRecordingHandler(recorder2)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
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

func TestAgenticCallbackEventsMatchAgentOutput(t *testing.T) {
	ctx := context.Background()

	expectedContent := "This is the test response content"
	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg(expectedContent), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TestAgent",
		Description: "Test agent",
		Instruction: "You are a test agent",
		Model:       m,
	})
	assert.NoError(t, err)

	recorder := &agenticCallbackRecorder{}
	handler := newAgenticRecordingHandler(recorder)

	var agentEvents []*TypedAgentEvent[*schema.AgenticMessage]
	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
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
			if msg != nil && agenticTextContent(msg) == expectedContent {
				foundExpectedContent = true
				break
			}
		}
	}
	assert.True(t, foundExpectedContent, "Callback events should contain the expected content")
}

func TestAgenticCallbackWithSequentialWorkflow(t *testing.T) {
	ctx := context.Background()

	m1 := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("response 1"), nil
		},
	}

	m2 := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("response 2"), nil
		},
	}

	agent1, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "Agent1",
		Description: "First agent",
		Instruction: "You are agent 1",
		Model:       m1,
	})
	assert.NoError(t, err)

	agent2, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "Agent2",
		Description: "Second agent",
		Instruction: "You are agent 2",
		Model:       m2,
	})
	assert.NoError(t, err)

	seqAgent, err := NewTypedSequentialAgent[*schema.AgenticMessage](ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
		Name:        "SequentialAgent",
		Description: "Sequential workflow",
		SubAgents:   []TypedAgent[*schema.AgenticMessage]{agent1, agent2},
	})
	assert.NoError(t, err)

	var callbackInfos []*callbacks.RunInfo
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component == ComponentOfAgenticAgent {
				callbackInfos = append(callbackInfos, info)
			}
			return ctx
		}).
		Build()

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: seqAgent})
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
		if info.Name == "Agent1" {
			foundAgent1 = true
		}
		if info.Name == "Agent2" {
			foundAgent2 = true
		}
	}
	assert.True(t, foundAgent1, "Agent1 callback should be invoked")
	assert.True(t, foundAgent2, "Agent2 callback should be invoked")
}

func TestCoverage_FlowAgent_RunWithCallbacksAndSubAgents(t *testing.T) {
	ctx := context.Background()

	child := &mockAgenticAgent{
		name:        "child",
		description: "child agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "child",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticMsg("child response"),
					},
				},
			},
		},
	}

	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("parent response"), nil
		},
	}

	parent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "parent",
		Description: "parent agent",
		Instruction: "You are helpful.",
		Model:       m,
	})
	require.NoError(t, err)

	ra, err := TypedSetSubAgents[*schema.AgenticMessage](ctx, parent, []TypedAgent[*schema.AgenticMessage]{child})
	require.NoError(t, err)
	require.NotNil(t, ra)

	var onStartCalled, onEndCalled bool
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component != ComponentOfAgenticAgent {
				return ctx
			}
			onStartCalled = true
			if agentInput := ConvAgenticCallbackInput(input); agentInput != nil {
				assert.NotNil(t, agentInput.Input)
			}
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info.Component != ComponentOfAgenticAgent {
				return ctx
			}
			onEndCalled = true
			return ctx
		}).
		Build()

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: ra,
	})

	iter := runner.Run(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("Hi"),
	}, WithCallbacks(handler))

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	require.NotEmpty(t, events)
	assert.True(t, onStartCalled, "agentic OnStart callback should fire")
	assert.True(t, onEndCalled, "agentic OnEnd callback should fire")
}

func TestCoverage_WrapAgenticIterWithOnEnd(t *testing.T) {
	ctx := context.Background()

	var onEndCalled bool
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info.Component == ComponentOfAgenticAgent {
				onEndCalled = true
			}
			return ctx
		}).
		Build()

	ctx = initAgenticCallbacks(ctx, "test-agent", "ChatModel",
		WithCallbacks(handler))

	cbInput := &AgenticCallbackInput{
		Input: &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("Hi")},
		},
	}
	ctx = callbacks.OnStart(ctx, cbInput)

	origIter, origGen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
	go func() {
		defer origGen.Close()
		origGen.Send(&TypedAgentEvent[*schema.AgenticMessage]{
			Output: &TypedAgentOutput[*schema.AgenticMessage]{
				MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
					Message: agenticMsg("done"),
				},
			},
		})
	}()

	wrappedIter := wrapAgenticIterWithOnEnd(ctx, origIter)

	for {
		_, ok := wrappedIter.Next()
		if !ok {
			break
		}
	}

	assert.True(t, onEndCalled, "OnEnd callback should have been called")
}
