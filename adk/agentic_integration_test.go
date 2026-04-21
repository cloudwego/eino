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

	require.Len(t, events, 1)
	assertAgenticEventRoleFields(t, events)
	lastEvent := events[len(events)-1]
	require.Nil(t, lastEvent.Err)
	require.NotNil(t, lastEvent.Output)
	require.NotNil(t, lastEvent.Output.MessageOutput)
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
	require.Len(t, receivedTools, 1)
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
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)

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
		assert.Equal(t, 2, len(chunks))
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

	var capturedErr error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			capturedErr = event.Err
		}
	}
	require.Error(t, capturedErr, "should propagate cancel error")
	assert.ErrorIs(t, capturedErr, context.Canceled)
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

	var capturedErr error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			capturedErr = event.Err
		}
	}

	require.Error(t, capturedErr, "should get timeout/cancel error")
	assert.ErrorIs(t, capturedErr, context.DeadlineExceeded)
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

	require.NotEmpty(t, events)
	assertAgenticEventRoleFields(t, events)
	lastEvent := events[len(events)-1]
	assert.Nil(t, lastEvent.Err)
	assert.NotNil(t, lastEvent.Output)
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
