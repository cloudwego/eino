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
	inputReceived  *TypedAgentCallbackInput[*schema.AgenticMessage]
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
			if agentInput := ConvTypedCallbackInput[*schema.AgenticMessage](input); agentInput != nil {
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

			if agentOutput := ConvTypedCallbackOutput[*schema.AgenticMessage](output); agentOutput != nil {
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

func TestAgenticCallback(t *testing.T) {
	ctx := context.Background()

	expectedContent := "This is the test response content"
	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg(expectedContent), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "TestChatAgent",
		Description: "Test chat agent",
		Instruction: "You are a test agent",
		Model:       m,
	})
	require.NoError(t, err)

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
	assertAgenticEventRoleFields(t, agentEvents)

	t.Run("OnStart_Invocation", func(t *testing.T) {
		assert.True(t, recorder.getOnStartCalled(), "OnStart should be called")
		require.NotNil(t, recorder.inputReceived, "Input should be received")
		require.NotNil(t, recorder.inputReceived.Input, "AgentInput should be set")
		assert.Len(t, recorder.inputReceived.Input.Messages, 1)
	})

	t.Run("OnEnd_Invocation", func(t *testing.T) {
		assert.True(t, recorder.getOnEndCalled(), "OnEnd should be called")
		assert.Len(t, recorder.getEventsReceived(), 1)
	})

	t.Run("RunInfo_Fields", func(t *testing.T) {
		require.NotNil(t, recorder.runInfo)
		assert.Equal(t, "TestChatAgent", recorder.runInfo.Name)
		assert.Equal(t, ComponentOfAgenticAgent, recorder.runInfo.Component)
	})

	t.Run("Events_MatchAgentOutput", func(t *testing.T) {
		require.NotEmpty(t, agentEvents, "Agent should emit events")
		received := recorder.getEventsReceived()
		require.NotEmpty(t, received, "Callback should receive events")

		require.Len(t, received, 1, "Callback should receive exactly 1 event")
		require.NotNil(t, received[0].Output)
		require.NotNil(t, received[0].Output.MessageOutput)
		require.NotNil(t, received[0].Output.MessageOutput.Message)
		assert.Equal(t, expectedContent, agenticTextContent(received[0].Output.MessageOutput.Message))
	})
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
	require.NoError(t, err)

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

	assert.True(t, recorder1.getOnStartCalled(), "Handler1 OnStart should be called")
	assert.True(t, recorder2.getOnStartCalled(), "Handler2 OnStart should be called")
	assert.True(t, recorder1.getOnEndCalled(), "Handler1 OnEnd should be called")
	assert.True(t, recorder2.getOnEndCalled(), "Handler2 OnEnd should be called")

	assert.NotEmpty(t, recorder1.getEventsReceived(), "Handler1 should receive events")
	assert.NotEmpty(t, recorder2.getEventsReceived(), "Handler2 should receive events")
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

	cbInput := &TypedAgentCallbackInput[*schema.AgenticMessage]{
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
