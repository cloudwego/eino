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
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func agenticMsg(text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: text}),
		},
	}
}

func agenticTextContent(msg *schema.AgenticMessage) string {
	for _, b := range msg.ContentBlocks {
		if b.AssistantGenText != nil {
			return b.AssistantGenText.Text
		}
	}
	return ""
}

func TestAgenticIntegration_ChatModelSingleShot(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("Handled internally with tool result: 42"), nil
		},
	}

	dummyTool := newSlowTool("calculator", 0, "42")

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "ToolCallAgent",
		Description: "Agent with tools for agentic model",
		Instruction: "You are a calculator.",
		Model:       m,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{dummyTool},
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: agent,
	})

	iter := runner.Query(ctx, "What is 6*7?")

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	require.GreaterOrEqual(t, len(events), 1)
	lastEvent := events[len(events)-1]
	assert.Nil(t, lastEvent.Err)
	assert.NotNil(t, lastEvent.Output)
	assert.NotNil(t, lastEvent.Output.MessageOutput)
	assert.Equal(t, "Handled internally with tool result: 42",
		agenticTextContent(lastEvent.Output.MessageOutput.Message))
}

func TestAgenticIntegration_ChatModelToolsPassedViaOptions(t *testing.T) {
	ctx := context.Background()

	var receivedTools []*schema.ToolInfo
	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			o := model.GetCommonOptions(&model.Options{}, opts...)
			receivedTools = o.Tools
			return agenticMsg("done"), nil
		},
	}

	dummyTool := newSlowTool("my_tool", 0, "result")

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "ToolOptAgent",
		Description: "Agent verifying tools are passed via options",
		Model:       m,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{dummyTool},
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: agent,
	})
	iter := runner.Query(ctx, "test tools")
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	require.NotNil(t, receivedTools, "tools should be passed via model.Options")
	assert.Equal(t, 1, len(receivedTools))
	assert.Equal(t, "my_tool", receivedTools[0].Name)
}

func TestAgenticIntegration_StreamingWithRunner(t *testing.T) {
	ctx := context.Background()

	chunk1 := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "Hello "}),
		},
	}
	chunk2 := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "world"}),
		},
	}

	m := &mockAgenticModel{
		streamFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
			r, w := schema.Pipe[*schema.AgenticMessage](2)
			go func() {
				defer w.Close()
				w.Send(chunk1, nil)
				w.Send(chunk2, nil)
			}()
			return r, nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "StreamRunner",
		Description: "Streaming runner agent",
		Model:       m,
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
	})

	iter := runner.Query(ctx, "stream me")

	event, ok := iter.Next()
	require.True(t, ok)
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)

	if event.Output.MessageOutput.IsStreaming {
		require.NotNil(t, event.Output.MessageOutput.MessageStream)
		var chunks []*schema.AgenticMessage
		for {
			chunk, err := event.Output.MessageOutput.MessageStream.Recv()
			if err != nil {
				break
			}
			chunks = append(chunks, chunk)
		}
		assert.GreaterOrEqual(t, len(chunks), 1)
	} else {
		assert.NotNil(t, event.Output.MessageOutput.Message)
	}

	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestAgenticIntegration_CancelDuringExecution(t *testing.T) {
	ctx := context.Background()

	modelStarted := make(chan struct{}, 1)
	modelBlocked := make(chan struct{})

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			select {
			case modelStarted <- struct{}{}:
			default:
			}
			select {
			case <-modelBlocked:
				return agenticMsg("should not reach"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "CancelAgent",
		Description: "cancel test",
		Model:       m,
	})
	require.NoError(t, err)

	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: agent,
	})
	iter := runner.Run(cancelCtx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("Hi"),
	})

	<-modelStarted
	cancel()

	var foundErr bool
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			foundErr = true
		}
	}
	assert.True(t, foundErr, "should propagate cancel error")
}

func TestAgenticIntegration_CancelWithTimeout(t *testing.T) {
	ctx := context.Background()

	sa := &myAgenticAgent{
		name: "slow-agent",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				select {
				case <-time.After(10 * time.Second):
					generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
						Output: &TypedAgentOutput[*schema.AgenticMessage]{
							MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
								Message: agenticMsg("slow response"),
							},
						},
					})
				case <-ctx.Done():
					generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
						Err: ctx.Err(),
					})
				}
			}()
			return iter
		},
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: sa,
	})
	iter := runner.Run(timeoutCtx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("slow request"),
	})

	var gotError bool
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			gotError = true
		}
	}

	assert.True(t, gotError, "should get timeout/cancel error")
}

func TestAgenticIntegration_NestedParallelWorkflow(t *testing.T) {
	ctx := context.Background()

	makeAgent := func(name, text string) TypedAgent[*schema.AgenticMessage] {
		return &mockAgenticAgent{
			name:        name,
			description: name + " agent",
			responses: []*TypedAgentEvent[*schema.AgenticMessage]{
				{
					AgentName: name,
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticMsg(text),
						},
					},
				},
			},
		}
	}

	innerParallel, err := NewTypedParallelAgent[*schema.AgenticMessage](ctx, &TypedParallelAgentConfig[*schema.AgenticMessage]{
		Name:      "inner-parallel",
		SubAgents: []TypedAgent[*schema.AgenticMessage]{makeAgent("inner1", "inner1 out"), makeAgent("inner2", "inner2 out")},
	})
	require.NoError(t, err)

	predecessor := makeAgent("predecessor", "predecessor out")
	successor := makeAgent("successor", "successor out")

	seqAgent, err := NewTypedSequentialAgent[*schema.AgenticMessage](ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
		Name:      "outer-seq",
		SubAgents: []TypedAgent[*schema.AgenticMessage]{predecessor, innerParallel, successor},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: seqAgent,
	})
	iter := runner.Query(ctx, "nested start")

	var outputs []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("unexpected error: %v", event.Err)
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			outputs = append(outputs, agenticTextContent(event.Output.MessageOutput.Message))
		}
	}

	assert.Contains(t, outputs, "predecessor out")
	assert.Contains(t, outputs, "inner1 out")
	assert.Contains(t, outputs, "inner2 out")
	assert.Contains(t, outputs, "successor out")
	assert.GreaterOrEqual(t, len(outputs), 4)
}

func TestAgenticIntegration_AgentTool(t *testing.T) {
	ctx := context.Background()

	innerModel := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("inner tool result"), nil
		},
	}

	innerAgent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "InnerAgent",
		Description: "An agent used as a tool",
		Model:       innerModel,
	})
	require.NoError(t, err)

	agentTool := NewTypedAgentTool(ctx, TypedAgent[*schema.AgenticMessage](innerAgent))
	require.NotNil(t, agentTool)

	info, err := agentTool.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, "InnerAgent", info.Name)
	assert.Equal(t, "An agent used as a tool", info.Desc)

	outerModel := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("outer response after inner tool"), nil
		},
	}

	outerAgent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "OuterAgent",
		Description: "Outer agent with agent tool",
		Model:       outerModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{agentTool},
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: outerAgent,
	})
	iter := runner.Query(ctx, "delegate to inner")

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	require.GreaterOrEqual(t, len(events), 1)
	lastEvent := events[len(events)-1]
	assert.Nil(t, lastEvent.Err)
	assert.NotNil(t, lastEvent.Output)
}

func TestAgenticIntegration_SequentialVisibility(t *testing.T) {
	ctx := context.Background()

	var sa2Input *TypedAgentInput[*schema.AgenticMessage]
	sa1 := &myAgenticAgent{
		name: "vis-sa1",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					AgentName: "vis-sa1",
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticMsg("sa1 visible output"),
						},
					},
				})
			}()
			return iter
		},
	}

	sa2 := &myAgenticAgent{
		name: "vis-sa2",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			sa2Input = input
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					AgentName: "vis-sa2",
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticMsg("sa2 output"),
						},
					},
				})
			}()
			return iter
		},
	}

	seqAgent, err := NewTypedSequentialAgent[*schema.AgenticMessage](ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
		Name:      "vis-seq",
		SubAgents: []TypedAgent[*schema.AgenticMessage]{sa1, sa2},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: seqAgent,
	})
	iter := runner.Query(ctx, "visibility test")

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("unexpected error: %v", event.Err)
		}
	}

	require.NotNil(t, sa2Input, "sa2 should have been called")
}

func TestAgenticIntegration_DeterministicTransfer(t *testing.T) {
	ctx := context.Background()

	innerAgent := &myAgenticAgent{
		name: "inner",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					AgentName: "inner",
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticMsg("inner completed"),
						},
					},
				})
			}()
			return iter
		},
	}

	outerAgent := &myAgenticAgent{
		name: "outer",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					AgentName: "outer",
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticMsg("outer message"),
						},
					},
				})
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					Action: NewTransferToAgentAction("inner"),
				})
			}()
			return iter
		},
	}

	flowAgent, err := TypedSetSubAgents[*schema.AgenticMessage](ctx,
		TypedAgent[*schema.AgenticMessage](outerAgent),
		[]TypedAgent[*schema.AgenticMessage]{innerAgent})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: flowAgent,
	})
	iter := runner.Query(ctx, "start dt")

	var transferFound bool
	var outputs []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("unexpected error: %v", event.Err)
		}
		if event.Action != nil && event.Action.TransferToAgent != nil {
			transferFound = true
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			outputs = append(outputs, agenticTextContent(event.Output.MessageOutput.Message))
		}
	}

	assert.True(t, transferFound, "transfer event should be emitted")
	assert.Contains(t, outputs, "outer message")
	assert.Contains(t, outputs, "inner completed")
}

func TestAgenticIntegration_InterruptEventFormation(t *testing.T) {
	ctx := context.Background()

	t.Run("simple interrupt", func(t *testing.T) {
		agent := &myAgenticAgent{
			name: "int-agent",
			runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
				go func() {
					defer generator.Close()
					intEvent := TypedInterrupt[*schema.AgenticMessage](ctx, "approval needed")
					intEvent.Action.Interrupted.Data = "approval data"
					generator.Send(intEvent)
				}()
				return iter
			},
		}

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
			Agent: agent,
		})
		iter := runner.Query(ctx, "interrupt test")

		var interruptEvent *TypedAgentEvent[*schema.AgenticMessage]
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Action != nil && event.Action.Interrupted != nil {
				interruptEvent = event
			}
		}

		require.NotNil(t, interruptEvent)
		assert.Equal(t, "approval data", interruptEvent.Action.Interrupted.Data)
		require.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts)
		assert.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts[0].ID)
		assert.Equal(t, "approval needed", interruptEvent.Action.Interrupted.InterruptContexts[0].Info)
		assert.True(t, interruptEvent.Action.Interrupted.InterruptContexts[0].IsRootCause)
	})

	t.Run("stateful interrupt", func(t *testing.T) {
		agent := &myAgenticAgent{
			name: "st-agent",
			runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
				go func() {
					defer generator.Close()
					intEvent := TypedStatefulInterrupt[*schema.AgenticMessage](ctx, "state interrupt", "my-state")
					intEvent.Action.Interrupted.Data = "stateful data"
					generator.Send(intEvent)
				}()
				return iter
			},
		}

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
			Agent: agent,
		})
		iter := runner.Query(ctx, "stateful test")

		var interruptEvent *TypedAgentEvent[*schema.AgenticMessage]
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Action != nil && event.Action.Interrupted != nil {
				interruptEvent = event
			}
		}

		require.NotNil(t, interruptEvent)
		assert.Equal(t, "stateful data", interruptEvent.Action.Interrupted.Data)
		require.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts)
		assert.Equal(t, "state interrupt", interruptEvent.Action.Interrupted.InterruptContexts[0].Info)
	})

	t.Run("multi-agent interrupt via transfer", func(t *testing.T) {
		sa1 := &myAgenticAgent{
			name: "ma-sa1",
			runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
				go func() {
					defer generator.Close()
					generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
						Action: NewTransferToAgentAction("ma-sa2"),
					})
				}()
				return iter
			},
		}
		sa2 := &myAgenticAgent{
			name: "ma-sa2",
			runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
				go func() {
					defer generator.Close()
					intEvent := TypedInterrupt[*schema.AgenticMessage](ctx, "transferred interrupt")
					intEvent.Action.Interrupted.Data = "transferred data"
					generator.Send(intEvent)
				}()
				return iter
			},
		}

		flowAgent, err := TypedSetSubAgents[*schema.AgenticMessage](ctx,
			TypedAgent[*schema.AgenticMessage](sa1),
			[]TypedAgent[*schema.AgenticMessage]{sa2})
		require.NoError(t, err)

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
			Agent: flowAgent,
		})
		iter := runner.Query(ctx, "multi-agent interrupt")

		var interruptEvent *TypedAgentEvent[*schema.AgenticMessage]
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Action != nil && event.Action.Interrupted != nil {
				interruptEvent = event
			}
		}

		require.NotNil(t, interruptEvent)
		require.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts)
		assert.Equal(t, "transferred interrupt", interruptEvent.Action.Interrupted.InterruptContexts[0].Info)
		assert.Equal(t, Address{
			{Type: AddressSegmentAgent, ID: "ma-sa1"},
			{Type: AddressSegmentAgent, ID: "ma-sa2"},
		}, interruptEvent.Action.Interrupted.InterruptContexts[0].Address)
	})
}

func TestAgenticIntegration_WorkflowWithoutInterrupt(t *testing.T) {
	ctx := context.Background()

	t.Run("sequential normal", func(t *testing.T) {
		sa1 := &mockAgenticAgent{
			name:        "sa1",
			description: "sa1",
			responses: []*TypedAgentEvent[*schema.AgenticMessage]{
				{AgentName: "sa1", Output: &TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: agenticMsg("sa1 out")}}},
			},
		}
		sa2 := &mockAgenticAgent{
			name:        "sa2",
			description: "sa2",
			responses: []*TypedAgentEvent[*schema.AgenticMessage]{
				{AgentName: "sa2", Output: &TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: agenticMsg("sa2 out")}}},
			},
		}

		seqAgent, err := NewTypedSequentialAgent[*schema.AgenticMessage](ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
			Name:      "seq-normal",
			SubAgents: []TypedAgent[*schema.AgenticMessage]{sa1, sa2},
		})
		require.NoError(t, err)

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: seqAgent})
		iter := runner.Query(ctx, "go sequential")

		var outputs []string
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				t.Fatalf("unexpected error: %v", event.Err)
			}
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
				outputs = append(outputs, agenticTextContent(event.Output.MessageOutput.Message))
			}
		}

		assert.Contains(t, outputs, "sa1 out")
		assert.Contains(t, outputs, "sa2 out")
	})

	t.Run("sequential with exit", func(t *testing.T) {
		sa1 := &myAgenticAgent{
			name: "exit-sa1",
			runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
				go func() {
					defer generator.Close()
					generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
						AgentName: "exit-sa1",
						Output: &TypedAgentOutput[*schema.AgenticMessage]{
							MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: agenticMsg("exit output")},
						},
						Action: &AgentAction{Exit: true},
					})
				}()
				return iter
			},
		}
		sa2 := &mockAgenticAgent{
			name:        "exit-sa2",
			description: "should not run",
			responses: []*TypedAgentEvent[*schema.AgenticMessage]{
				{AgentName: "exit-sa2", Output: &TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: agenticMsg("sa2 out")}}},
			},
		}

		seqAgent, err := NewTypedSequentialAgent[*schema.AgenticMessage](ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
			Name:      "seq-exit",
			SubAgents: []TypedAgent[*schema.AgenticMessage]{sa1, sa2},
		})
		require.NoError(t, err)

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: seqAgent})
		iter := runner.Query(ctx, "exit test")

		var outputs []string
		var exitFound bool
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Action != nil && event.Action.Exit {
				exitFound = true
			}
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
				outputs = append(outputs, agenticTextContent(event.Output.MessageOutput.Message))
			}
		}

		assert.True(t, exitFound)
		assert.Contains(t, outputs, "exit output")
		assert.NotContains(t, outputs, "sa2 out")
	})

	t.Run("parallel normal", func(t *testing.T) {
		sa1 := &mockAgenticAgent{
			name:        "p1",
			description: "p1",
			responses: []*TypedAgentEvent[*schema.AgenticMessage]{
				{AgentName: "p1", Output: &TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: agenticMsg("p1 out")}}},
			},
		}
		sa2 := &mockAgenticAgent{
			name:        "p2",
			description: "p2",
			responses: []*TypedAgentEvent[*schema.AgenticMessage]{
				{AgentName: "p2", Output: &TypedAgentOutput[*schema.AgenticMessage]{MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: agenticMsg("p2 out")}}},
			},
		}

		parallelAgent, err := NewTypedParallelAgent[*schema.AgenticMessage](ctx, &TypedParallelAgentConfig[*schema.AgenticMessage]{
			Name:      "par-normal",
			SubAgents: []TypedAgent[*schema.AgenticMessage]{sa1, sa2},
		})
		require.NoError(t, err)

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: parallelAgent})
		iter := runner.Query(ctx, "go parallel")

		var outputs []string
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				t.Fatalf("unexpected error: %v", event.Err)
			}
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
				outputs = append(outputs, agenticTextContent(event.Output.MessageOutput.Message))
			}
		}

		assert.Contains(t, outputs, "p1 out")
		assert.Contains(t, outputs, "p2 out")
	})

	t.Run("loop normal", func(t *testing.T) {
		iterCount := int32(0)
		sa := &myAgenticAgent{
			name: "loop-sa",
			runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
				cur := atomic.AddInt32(&iterCount, 1)
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
				go func() {
					defer generator.Close()
					generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
						AgentName: "loop-sa",
						Output: &TypedAgentOutput[*schema.AgenticMessage]{
							MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
								Message: agenticMsg(fmt.Sprintf("loop iter %d", cur)),
							},
						},
					})
				}()
				return iter
			},
		}

		loopAgent, err := NewTypedLoopAgent[*schema.AgenticMessage](ctx, &TypedLoopAgentConfig[*schema.AgenticMessage]{
			Name:          "loop-normal",
			SubAgents:     []TypedAgent[*schema.AgenticMessage]{sa},
			MaxIterations: 3,
		})
		require.NoError(t, err)

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: loopAgent})
		iter := runner.Query(ctx, "loop start")

		var outputs []string
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				t.Fatalf("unexpected error: %v", event.Err)
			}
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
				outputs = append(outputs, agenticTextContent(event.Output.MessageOutput.Message))
			}
		}

		assert.Contains(t, outputs, "loop iter 1")
		assert.Contains(t, outputs, "loop iter 2")
		assert.Contains(t, outputs, "loop iter 3")
	})

	t.Run("loop with break", func(t *testing.T) {
		sa := &myAgenticAgent{
			name: "break-sa",
			runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
				go func() {
					defer generator.Close()
					generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
						AgentName: "break-sa",
						Output: &TypedAgentOutput[*schema.AgenticMessage]{
							MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
								Message: agenticMsg("break output"),
							},
						},
						Action: NewBreakLoopAction("loop-break"),
					})
				}()
				return iter
			},
		}

		loopAgent, err := NewTypedLoopAgent[*schema.AgenticMessage](ctx, &TypedLoopAgentConfig[*schema.AgenticMessage]{
			Name:          "loop-break",
			SubAgents:     []TypedAgent[*schema.AgenticMessage]{sa},
			MaxIterations: 5,
		})
		require.NoError(t, err)

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: loopAgent})
		iter := runner.Query(ctx, "break test")

		var outputs []string
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
				outputs = append(outputs, agenticTextContent(event.Output.MessageOutput.Message))
			}
		}

		assert.Equal(t, 1, len(outputs))
		assert.Contains(t, outputs, "break output")
	})
}

func TestAgenticIntegration_CheckpointInterruptResume(t *testing.T) {
	ctx := context.Background()

	var resumeCalled int32
	agent := &myAgenticAgent{
		name: "ckpt-agent",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					AgentName: "ckpt-agent",
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticMsg("before interrupt"),
						},
					},
				})
				intEvent := TypedInterrupt[*schema.AgenticMessage](ctx, "need approval")
				intEvent.Action.Interrupted.Data = "approval data"
				generator.Send(intEvent)
			}()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			atomic.StoreInt32(&resumeCalled, 1)
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					AgentName: "ckpt-agent",
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticMsg("after resume"),
						},
					},
				})
			}()
			return iter
		},
	}

	store := newMyStore()
	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		CheckPointStore: store,
	})

	iter := runner.Query(ctx, "checkpoint test", WithCheckPointID("ckpt-1"))

	var interruptEvent *TypedAgentEvent[*schema.AgenticMessage]
	var preInterruptOutputs []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.Nil(t, event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			interruptEvent = event
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			preInterruptOutputs = append(preInterruptOutputs, agenticTextContent(event.Output.MessageOutput.Message))
		}
	}

	require.NotNil(t, interruptEvent, "should receive interrupt event")
	assert.Contains(t, preInterruptOutputs, "before interrupt")
	require.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts)

	interruptID := interruptEvent.Action.Interrupted.InterruptContexts[0].ID
	require.NotEmpty(t, interruptID)

	resumeIter, err := runner.ResumeWithParams(ctx, "ckpt-1", &ResumeParams{
		Targets: map[string]any{
			interruptID: nil,
		},
	})
	require.NoError(t, err)

	var postResumeOutputs []string
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("unexpected error during resume: %v", event.Err)
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			postResumeOutputs = append(postResumeOutputs, agenticTextContent(event.Output.MessageOutput.Message))
		}
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeCalled), "resume function should have been called")
	assert.Contains(t, postResumeOutputs, "after resume")
}

func TestAgenticIntegration_CheckpointWithMCPListToolsResult(t *testing.T) {
	ctx := context.Background()

	inputSchemaJSON := `{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "search query"},
			"limit": {"type": "integer", "description": "max results"}
		},
		"required": ["query"]
	}`
	var inputSchema jsonschema.Schema
	require.NoError(t, json.Unmarshal([]byte(inputSchemaJSON), &inputSchema))

	mcpMsg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeMCPListToolsResult,
				MCPListToolsResult: &schema.MCPListToolsResult{
					ServerLabel: "test-server",
					Tools: []*schema.MCPListToolsItem{
						{
							Name:        "search",
							Description: "search the web",
							InputSchema: &inputSchema,
						},
					},
				},
			},
			schema.NewContentBlock(&schema.AssistantGenText{Text: "here are tools"}),
		},
	}

	var resumeCalled int32
	agent := &myAgenticAgent{
		name: "mcp-agent",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer gen.Close()
				gen.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					AgentName: "mcp-agent",
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: mcpMsg},
					},
				})
				gen.Send(TypedInterrupt[*schema.AgenticMessage](ctx, "approve tools"))
			}()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			atomic.StoreInt32(&resumeCalled, 1)
			iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer gen.Close()
				gen.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					AgentName: "mcp-agent",
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: agenticMsg("tools approved")},
					},
				})
			}()
			return iter
		},
	}

	store := newMyStore()
	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		CheckPointStore: store,
	})

	iter := runner.Query(ctx, "list tools", WithCheckPointID("mcp-1"))
	var interruptEvent *TypedAgentEvent[*schema.AgenticMessage]
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.Nil(t, ev.Err)
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvent = ev
		}
	}
	require.NotNil(t, interruptEvent)
	interruptID := interruptEvent.Action.Interrupted.InterruptContexts[0].ID

	resumeIter, err := runner.ResumeWithParams(ctx, "mcp-1", &ResumeParams{
		Targets: map[string]any{interruptID: nil},
	})
	require.NoError(t, err)

	var outputs []string
	for {
		ev, ok := resumeIter.Next()
		if !ok {
			break
		}
		require.Nil(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.Message != nil {
			outputs = append(outputs, agenticTextContent(ev.Output.MessageOutput.Message))
		}
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeCalled))
	assert.Contains(t, outputs, "tools approved")
}
