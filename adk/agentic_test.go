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

package adk

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type mockAgenticModel struct {
	generateFn func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error)
	streamFn   func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error)
}

func (m *mockAgenticModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	return m.generateFn(ctx, input, opts...)
}

func (m *mockAgenticModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	if m.streamFn != nil {
		return m.streamFn(ctx, input, opts...)
	}
	result, err := m.generateFn(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	r, w := schema.Pipe[*schema.AgenticMessage](1)
	go func() { defer w.Close(); w.Send(result, nil) }()
	return r, nil
}

type testAgenticMiddleware struct {
	*TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]
	beforeFn func(context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], *TypedModelContext[*schema.AgenticMessage]) (context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], error)
	afterFn  func(context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], *TypedModelContext[*schema.AgenticMessage]) (context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], error)
}

func (m *testAgenticMiddleware) BeforeModelRewriteState(ctx context.Context, state *TypedChatModelAgentState[*schema.AgenticMessage], mc *TypedModelContext[*schema.AgenticMessage]) (context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], error) {
	if m.beforeFn != nil {
		return m.beforeFn(ctx, state, mc)
	}
	return ctx, state, nil
}

func (m *testAgenticMiddleware) AfterModelRewriteState(ctx context.Context, state *TypedChatModelAgentState[*schema.AgenticMessage], mc *TypedModelContext[*schema.AgenticMessage]) (context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], error) {
	if m.afterFn != nil {
		return m.afterFn(ctx, state, mc)
	}
	return ctx, state, nil
}

func TestAgenticChatModelAgentRun_NoTools(t *testing.T) {
	ctx := context.Background()

	agenticResponse := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "Hello from agentic model"}),
		},
	}

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticResponse, nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "AgenticTestAgent",
		Description: "Agentic test agent",
		Instruction: "You are helpful.",
		Model:       m,
	})
	assert.NoError(t, err)
	assert.NotNil(t, agent)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Hi"),
		},
	}
	iter := agent.Run(ctx, input)
	require.NotNil(t, iter)

	event, ok := iter.Next()
	assert.True(t, ok)
	require.NotNil(t, event)
	assert.Nil(t, event.Err)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)

	msg := event.Output.MessageOutput.Message
	require.NotNil(t, msg)
	assert.Equal(t, schema.AgenticRoleTypeAssistant, msg.Role)
	assert.Len(t, msg.ContentBlocks, 1)
	assert.Equal(t, "Hello from agentic model", msg.ContentBlocks[0].AssistantGenText.Text)

	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestAgenticChatModelAgentRun_WithTools(t *testing.T) {
	ctx := context.Background()

	agenticResponse := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "Used tool and got result"}),
		},
	}

	var receivedToolInfos []*schema.ToolInfo
	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			o := model.GetCommonOptions(&model.Options{}, opts...)
			receivedToolInfos = o.Tools
			return agenticResponse, nil
		},
	}

	dummyTool := newSlowTool("dummy_tool", 0, "ok")

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "AgenticToolAgent",
		Description: "Agentic agent with tools",
		Instruction: "You are helpful.",
		Model:       m,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{dummyTool},
			},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, agent)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Call a tool"),
		},
	}
	iter := agent.Run(ctx, input)

	event, ok := iter.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)

	_, ok = iter.Next()
	assert.False(t, ok)

	require.Len(t, receivedToolInfos, 1)
	assert.Equal(t, "dummy_tool", receivedToolInfos[0].Name)
}

func TestAgenticChatModelAgentRun_Streaming(t *testing.T) {
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
		Name:        "AgenticStreamAgent",
		Description: "Agentic streaming agent",
		Instruction: "You are helpful.",
		Model:       m,
	})
	assert.NoError(t, err)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Hi"),
		},
		EnableStreaming: true,
	}
	iter := agent.Run(ctx, input)

	event, ok := iter.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)
	require.NotNil(t, event.Output.MessageOutput.MessageStream)
	event.Output.MessageOutput.MessageStream.Close()

	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestDefaultAgenticGenModelInput(t *testing.T) {
	ctx := context.Background()

	t.Run("WithInstruction", func(t *testing.T) {
		input := &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				schema.UserAgenticMessage("Hello"),
			},
		}
		msgs, err := newDefaultGenModelInput[*schema.AgenticMessage]()(ctx, "Be helpful", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2)
		assert.Equal(t, schema.AgenticRoleTypeSystem, msgs[0].Role)
		assert.Equal(t, schema.AgenticRoleTypeUser, msgs[1].Role)
	})

	t.Run("WithoutInstruction", func(t *testing.T) {
		input := &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				schema.UserAgenticMessage("Hello"),
			},
		}
		msgs, err := newDefaultGenModelInput[*schema.AgenticMessage]()(ctx, "", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 1)
		assert.Equal(t, schema.AgenticRoleTypeUser, msgs[0].Role)
	})
}

func TestAgenticRunnerQuery(t *testing.T) {
	ctx := context.Background()

	agenticResponse := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "query response"}),
		},
	}

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticResponse, nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "QueryAgent",
		Description: "Query test agent",
		Instruction: "Be helpful.",
		Model:       m,
	})
	assert.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: agent,
	})

	iter := runner.Query(ctx, "What's up?")

	event, ok := iter.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)

	_, ok = iter.Next()
	assert.False(t, ok)
}

func agenticAssistantMessage(text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: text}),
		},
	}
}

type mockAgenticRunnerAgent struct {
	name            string
	description     string
	responses       []*TypedAgentEvent[*schema.AgenticMessage]
	callCount       int
	lastInput       *TypedAgentInput[*schema.AgenticMessage]
	enableStreaming bool
}

func (a *mockAgenticRunnerAgent) Name(_ context.Context) string        { return a.name }
func (a *mockAgenticRunnerAgent) Description(_ context.Context) string { return a.description }
func (a *mockAgenticRunnerAgent) Run(_ context.Context, input *TypedAgentInput[*schema.AgenticMessage], _ ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
	a.callCount++
	a.lastInput = input
	a.enableStreaming = input.EnableStreaming

	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
	go func() {
		defer generator.Close()
		for _, event := range a.responses {
			generator.Send(event)
			if event.Action != nil && event.Action.Exit {
				break
			}
		}
	}()
	return iterator
}

type mockAgenticAgent struct {
	name        string
	description string
	responses   []*TypedAgentEvent[*schema.AgenticMessage]
}

func (a *mockAgenticAgent) Name(_ context.Context) string        { return a.name }
func (a *mockAgenticAgent) Description(_ context.Context) string { return a.description }
func (a *mockAgenticAgent) Run(_ context.Context, _ *TypedAgentInput[*schema.AgenticMessage], _ ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
	go func() {
		defer generator.Close()
		for _, event := range a.responses {
			generator.Send(event)
			if event.Action != nil && event.Action.Exit {
				break
			}
		}
	}()
	return iterator
}

type myAgenticAgent struct {
	name     string
	runFn    func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]]
	resumeFn func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]]
}

func (m *myAgenticAgent) Name(_ context.Context) string {
	if len(m.name) > 0 {
		return m.name
	}
	return "myAgenticAgent"
}
func (m *myAgenticAgent) Description(_ context.Context) string { return "my agentic agent description" }
func (m *myAgenticAgent) Run(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
	return m.runFn(ctx, input, options...)
}
func (m *myAgenticAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
	return m.resumeFn(ctx, info, opts...)
}

func TestAgenticChatModelAgentRun_WithMiddleware(t *testing.T) {
	ctx := context.Background()

	agenticResponse := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "Hello from agentic agent"}),
		},
	}

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticResponse, nil
		},
	}

	afterModelExecuted := false

	mw := &testAgenticMiddleware{
		beforeFn: func(ctx context.Context, state *TypedChatModelAgentState[*schema.AgenticMessage], mc *TypedModelContext[*schema.AgenticMessage]) (context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], error) {
			state.Messages = append(state.Messages, schema.UserAgenticMessage("extra"))
			return ctx, state, nil
		},
		afterFn: func(ctx context.Context, state *TypedChatModelAgentState[*schema.AgenticMessage], mc *TypedModelContext[*schema.AgenticMessage]) (context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], error) {
			assert.Len(t, state.Messages, 4)
			afterModelExecuted = true
			return ctx, state, nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "AgenticMiddlewareAgent",
		Description: "Agentic agent with middleware",
		Instruction: "You are helpful.",
		Model:       m,
		Handlers:    []TypedChatModelAgentMiddleware[*schema.AgenticMessage]{mw},
	})
	assert.NoError(t, err)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Hi"),
		},
	}
	iter := agent.Run(ctx, input)
	event, ok := iter.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)
	require.NotNil(t, event.Output.MessageOutput.Message)
	assert.Equal(t, schema.AgenticRoleTypeAssistant, event.Output.MessageOutput.Message.Role)
	_, ok = iter.Next()
	assert.False(t, ok)
	assert.True(t, afterModelExecuted)
}

func TestAgenticAfterModel_NoTools_ModifyDoesNotAffectEvent(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticAssistantMessage("original content"), nil
		},
	}

	var capturedMessages []*schema.AgenticMessage

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "AgenticAfterModelAgent",
		Description: "Test AfterModelRewriteState",
		Instruction: "You are helpful.",
		Model:       m,
		Handlers: []TypedChatModelAgentMiddleware[*schema.AgenticMessage]{
			&testAgenticMiddleware{
				afterFn: func(ctx context.Context, state *TypedChatModelAgentState[*schema.AgenticMessage], mc *TypedModelContext[*schema.AgenticMessage]) (context.Context, *TypedChatModelAgentState[*schema.AgenticMessage], error) {
					capturedMessages = make([]*schema.AgenticMessage, len(state.Messages))
					copy(capturedMessages, state.Messages)
					state.Messages = append(state.Messages, agenticAssistantMessage("appended content"))
					return ctx, state, nil
				},
			},
		},
	})
	assert.NoError(t, err)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Hello"),
		},
	}
	iterator := agent.Run(ctx, input)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)

	msg := event.Output.MessageOutput.Message
	require.NotNil(t, msg)
	assert.Equal(t, "original content", msg.ContentBlocks[0].AssistantGenText.Text)

	_, ok = iterator.Next()
	assert.False(t, ok)

	assert.Len(t, capturedMessages, 3)
}

func TestAgenticGetComposeOptions_WithChatModelOptions(t *testing.T) {
	ctx := context.Background()

	var capturedTemperature float32
	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			options := model.GetCommonOptions(&model.Options{}, opts...)
			if options.Temperature != nil {
				capturedTemperature = *options.Temperature
			}
			return agenticAssistantMessage("response"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "AgenticOptionsAgent",
		Description: "Test agent",
		Model:       m,
	})
	assert.NoError(t, err)

	temp := float32(0.7)
	iter := agent.Run(ctx, &TypedAgentInput[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("test")}},
		WithChatModelOptions([]model.Option{model.WithTemperature(temp)}))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	assert.Equal(t, temp, capturedTemperature)
}

func TestAgenticChatModelAgent_PrepareExecContextError(t *testing.T) {
	ctx := context.Background()

	expectedErr := errors.New("tool info error")
	errTool := &errorTool{infoErr: expectedErr}

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticAssistantMessage("response"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "AgenticErrToolAgent",
		Description: "Test agent",
		Model:       m,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{errTool},
			},
		},
	})
	assert.NoError(t, err)

	iter := agent.Run(ctx, &TypedAgentInput[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("test")}})

	event, ok := iter.Next()
	assert.True(t, ok)
	assert.NotNil(t, event.Err)
	assert.Contains(t, event.Err.Error(), "tool info error")

	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestAgenticChatModelAgentOutputKey(t *testing.T) {
	t.Run("OutputKeyStoresInSession", func(t *testing.T) {
		ctx := context.Background()

		m := &mockAgenticModel{
			generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
				return agenticAssistantMessage("Hello from agentic assistant."), nil
			},
		}

		agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "AgenticOutputKeyAgent",
			Description: "Test agent for output key",
			Instruction: "You are helpful.",
			Model:       m,
			OutputKey:   "agent_output",
		})
		assert.NoError(t, err)

		input := &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				schema.UserAgenticMessage("Hello"),
			},
		}
		ctx, runCtx := initTypedRunCtx[*schema.AgenticMessage](ctx, "AgenticOutputKeyAgent", input)
		require.NotNil(t, runCtx)
		require.NotNil(t, runCtx.Session)

		iterator := agent.Run(ctx, input)

		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.Nil(t, event.Err)

		msg := event.Output.MessageOutput.Message
		assert.Equal(t, "Hello from agentic assistant.", msg.ContentBlocks[0].AssistantGenText.Text)

		_, ok = iterator.Next()
		assert.False(t, ok)

		sessionValues := GetSessionValues(ctx)
		assert.Contains(t, sessionValues, "agent_output")
		assert.Equal(t, "Hello from agentic assistant.", sessionValues["agent_output"])
	})

	t.Run("OutputKeyWithStreamingStoresInSession", func(t *testing.T) {
		ctx := context.Background()

		chunk1 := agenticAssistantMessage("Hello")
		chunk2 := agenticAssistantMessage(", world.")

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
			Name:        "AgenticStreamOutputKeyAgent",
			Description: "Test agent for streaming output key",
			Instruction: "You are helpful.",
			Model:       m,
			OutputKey:   "agent_output",
		})
		assert.NoError(t, err)

		input := &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				schema.UserAgenticMessage("Hello"),
			},
			EnableStreaming: true,
		}
		ctx, runCtx := initTypedRunCtx[*schema.AgenticMessage](ctx, "AgenticStreamOutputKeyAgent", input)
		require.NotNil(t, runCtx)
		require.NotNil(t, runCtx.Session)

		iterator := agent.Run(ctx, input)

		event, ok := iterator.Next()
		assert.True(t, ok)
		assert.Nil(t, event.Err)
		assert.True(t, event.Output.MessageOutput.IsStreaming)

		_, ok = iterator.Next()
		assert.False(t, ok)
	})

	t.Run("SetOutputToSessionAgenticMessage", func(t *testing.T) {
		ctx := context.Background()

		input := &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("test")},
		}
		ctx, runCtx := initTypedRunCtx[*schema.AgenticMessage](ctx, "TestAgent", input)
		require.NotNil(t, runCtx)
		require.NotNil(t, runCtx.Session)

		msg := agenticAssistantMessage("Test response")
		err := setOutputToSession(ctx, msg, nil, "test_output")
		assert.NoError(t, err)

		sessionValues := GetSessionValues(ctx)
		assert.Contains(t, sessionValues, "test_output")
		assert.Equal(t, "Test response", sessionValues["test_output"])
	})
}

func TestAgenticRunner_Run_WithStreaming(t *testing.T) {
	ctx := context.Background()

	mockAgent_ := &mockAgenticRunnerAgent{
		name:        "AgenticStreamRunnerAgent",
		description: "Test agent for agentic runner streaming",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "AgenticStreamRunnerAgent",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						IsStreaming: true,
						MessageStream: schema.StreamReaderFromArray([]*schema.AgenticMessage{
							agenticAssistantMessage("Streaming response"),
						}),
					},
				},
			},
		},
	}

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{EnableStreaming: true, Agent: mockAgent_})

	msgs := []*schema.AgenticMessage{
		schema.UserAgenticMessage("Hello, agent!"),
	}

	iterator := runner.Run(ctx, msgs)

	assert.Equal(t, 1, mockAgent_.callCount)
	assert.Equal(t, msgs, mockAgent_.lastInput.Messages)
	assert.True(t, mockAgent_.enableStreaming)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "AgenticStreamRunnerAgent", event.AgentName)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)
	assert.True(t, event.Output.MessageOutput.IsStreaming)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestAgenticRunner_Query_WithStreaming(t *testing.T) {
	ctx := context.Background()

	mockAgent_ := &mockAgenticRunnerAgent{
		name:        "AgenticStreamQueryAgent",
		description: "Test agent for agentic runner query streaming",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "AgenticStreamQueryAgent",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						IsStreaming: true,
						MessageStream: schema.StreamReaderFromArray([]*schema.AgenticMessage{
							agenticAssistantMessage("Streaming query response"),
						}),
					},
				},
			},
		},
	}

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{EnableStreaming: true, Agent: mockAgent_})

	iterator := runner.Query(ctx, "Test query")

	assert.Equal(t, 1, mockAgent_.callCount)
	assert.Len(t, mockAgent_.lastInput.Messages, 1)
	assert.True(t, mockAgent_.enableStreaming)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "AgenticStreamQueryAgent", event.AgentName)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)
	assert.True(t, event.Output.MessageOutput.IsStreaming)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestAgenticSimpleInterrupt(t *testing.T) {
	data := "hello world"
	agent := &myAgenticAgent{
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						IsStreaming: true,
						MessageStream: schema.StreamReaderFromArray([]*schema.AgenticMessage{
							schema.UserAgenticMessage("hello "),
							schema.UserAgenticMessage("world"),
						}),
					},
				},
			})
			intEvent := TypedInterrupt[*schema.AgenticMessage](ctx, data)
			intEvent.Action.Interrupted.Data = data
			generator.Send(intEvent)
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			assert.True(t, info.WasInterrupted)
			assert.Nil(t, info.InterruptState)
			assert.True(t, info.EnableStreaming)
			assert.Equal(t, data, info.Data)

			assert.True(t, info.IsResumeTarget)
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			generator.Close()
			return iter
		},
	}
	store := newMyStore()
	ctx := context.Background()
	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: store,
	})
	iter := runner.Query(ctx, "hello world", WithCheckPointID("1"))

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
	assert.Equal(t, data, interruptEvent.Action.Interrupted.Data)
	assert.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts[0].ID)
	assert.True(t, interruptEvent.Action.Interrupted.InterruptContexts[0].IsRootCause)
	assert.Equal(t, data, interruptEvent.Action.Interrupted.InterruptContexts[0].Info)
	assert.Equal(t, Address{{Type: AddressSegmentAgent, ID: "myAgenticAgent"}},
		interruptEvent.Action.Interrupted.InterruptContexts[0].Address)
}

func TestCascadingFrom_NewChatModelAgentFrom(t *testing.T) {
	ctx := context.Background()

	agenticResponse := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "from response"}),
		},
	}

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticResponse, nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "FromAgent",
		Description: "Test cascading constructor",
		Instruction: "Be helpful.",
		Model:       m,
	})
	assert.NoError(t, err)
	assert.Equal(t, "FromAgent", agent.Name(ctx))

	runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})

	iter := runner.Run(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("Hello"),
	})

	event, ok := iter.Next()
	assert.True(t, ok)
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)

	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestCascadingTyped_TypedStatefulInterrupt(t *testing.T) {
	ctx := context.Background()
	ctx = AppendAddressSegment(ctx, AddressSegmentAgent, "test-agent")

	type myState struct {
		Count int
	}

	event := TypedStatefulInterrupt[*schema.AgenticMessage](ctx, "please confirm", &myState{Count: 42})
	require.NotNil(t, event)
	require.NotNil(t, event.Action)
	require.NotNil(t, event.Action.Interrupted)
}

func TestCascadingTyped_EventFromAgenticMessage(t *testing.T) {
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "hello"}),
		},
	}

	event := EventFromAgenticMessage(msg, nil, schema.AgenticRoleTypeAssistant)
	require.NotNil(t, event)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)
	assert.Equal(t, msg, event.Output.MessageOutput.Message)
	assert.False(t, event.Output.MessageOutput.IsStreaming)
	assert.Equal(t, schema.RoleType(""), event.Output.MessageOutput.Role)
	assert.Equal(t, schema.AgenticRoleTypeAssistant, event.Output.MessageOutput.AgenticRole)
	assert.Empty(t, event.Output.MessageOutput.ToolName)
}

// assertAgenticEventRoleFields asserts that all AgenticMessage events in the
// list have zero-valued Role and ToolName fields (which are *schema.Message-only),
// and that AgenticRole is populated with a non-zero value.
func assertAgenticEventRoleFields(t *testing.T, events []*TypedAgentEvent[*schema.AgenticMessage]) {
	t.Helper()
	for i, event := range events {
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		mo := event.Output.MessageOutput
		assert.Equal(t, schema.RoleType(""), mo.Role, "event[%d]: AgenticMessage must have zero Role", i)
		assert.Empty(t, mo.ToolName, "event[%d]: AgenticMessage must have empty ToolName", i)
		assert.NotEmpty(t, mo.AgenticRole, "event[%d]: AgenticMessage must have non-zero AgenticRole", i)
	}
}

func TestCoverage_FlowAgent_ResumeNotResumable(t *testing.T) {
	ctx := context.Background()

	agent := &mockAgenticAgent{
		name:        "non-resumable",
		description: "cannot resume",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{Output: &TypedAgentOutput[*schema.AgenticMessage]{
				MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
					Message: agenticMsg("done"),
				},
			}},
		},
	}

	fa := toTypedFlowAgent[*schema.AgenticMessage](agent)

	info := &ResumeInfo{WasInterrupted: true}
	iter := fa.Resume(ctx, info)

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
	require.Error(t, capturedErr, "should get error for non-resumable agent")
}

func TestCoverage_GenAgenticErrorIter(t *testing.T) {
	testErr := errors.New("test agentic error")
	iter := genAgenticErrorIter(testErr)

	event, ok := iter.Next()
	require.True(t, ok)
	assert.Equal(t, testErr, event.Err)

	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestCoverage_ChatModelAgent_OnSetSubAgents_FrozenError(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("done"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "freeze-test",
		Description: "frozen test agent",
		Model:       m,
	})
	require.NoError(t, err)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("Hi")},
	}
	iter := agent.Run(ctx, input)
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	err = agent.OnSetSubAgents(ctx, []TypedAgent[*schema.AgenticMessage]{
		&mockAgenticAgent{name: "late-child"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "frozen")
}

func TestCoverage_ChatModelAgent_OnSetAsSubAgent_FrozenError(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("done"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "freeze-child",
		Description: "frozen child agent",
		Model:       m,
	})
	require.NoError(t, err)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("Hi")},
	}
	iter := agent.Run(ctx, input)
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	err = agent.OnSetAsSubAgent(ctx, &mockAgenticAgent{name: "parent"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "frozen")
}

func TestCoverage_ChatModelAgent_OnSetAsSubAgent_DuplicateError(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("done"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "dup-child",
		Description: "duplicate child agent",
		Model:       m,
	})
	require.NoError(t, err)

	err = agent.OnSetAsSubAgent(ctx, &mockAgenticAgent{name: "parent1"})
	assert.NoError(t, err)

	err = agent.OnSetAsSubAgent(ctx, &mockAgenticAgent{name: "parent2"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already been set as a sub-agent")
}

func TestCoverage_ChatModelAgent_OnDisallowTransferToParent_FrozenError(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("done"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "disallow-test",
		Description: "disallow transfer test",
		Model:       m,
	})
	require.NoError(t, err)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("Hi")},
	}
	iter := agent.Run(ctx, input)
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	err = agent.OnDisallowTransferToParent(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "frozen")
}

func TestCoverage_TypedGetMessage_AgenticNonStreaming(t *testing.T) {
	msg := agenticMsg("hello")
	event := &TypedAgentEvent[*schema.AgenticMessage]{
		Output: &TypedAgentOutput[*schema.AgenticMessage]{
			MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
				Message: msg,
			},
		},
	}

	result, retEvent, err := TypedGetMessage(event)
	assert.NoError(t, err)
	assert.Equal(t, msg, result)
	assert.Equal(t, event, retEvent)
}

func TestCoverage_TypedGetMessage_AgenticStreaming(t *testing.T) {
	r, w := schema.Pipe[*schema.AgenticMessage](2)
	go func() {
		defer w.Close()
		w.Send(&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "Hello "}),
			},
		}, nil)
		w.Send(&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "world"}),
			},
		}, nil)
	}()

	event := &TypedAgentEvent[*schema.AgenticMessage]{
		Output: &TypedAgentOutput[*schema.AgenticMessage]{
			MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
				IsStreaming:   true,
				MessageStream: r,
			},
		},
	}

	result, retEvent, err := TypedGetMessage(event)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	require.NotNil(t, retEvent)
	assert.NotNil(t, retEvent.Output.MessageOutput.MessageStream)
}

func TestCoverage_TypedGetMessage_NilOutput(t *testing.T) {
	event := &TypedAgentEvent[*schema.AgenticMessage]{}

	result, retEvent, err := TypedGetMessage(event)
	assert.NoError(t, err)
	assert.Nil(t, result)
	assert.Equal(t, event, retEvent)
}

func TestCoverage_GetMessage_NonStreaming(t *testing.T) {
	msg := schema.AssistantMessage("hello", nil)
	event := &AgentEvent{
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				Message: msg,
			},
		},
	}

	result, retEvent, err := GetMessage(event)
	assert.NoError(t, err)
	assert.Equal(t, msg, result)
	assert.Equal(t, event, retEvent)
}

func TestCoverage_GetMessage_Streaming(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](2)
	go func() {
		defer w.Close()
		w.Send(schema.AssistantMessage("Hello ", nil), nil)
		w.Send(schema.AssistantMessage("world", nil), nil)
	}()

	event := &AgentEvent{
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				IsStreaming:   true,
				MessageStream: r,
			},
		},
	}

	result, retEvent, err := GetMessage(event)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, retEvent)
}

func TestCoverage_NewTypedAgentTool_Agentic(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("tool response"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "tool-agent",
		Description: "agent wrapped as tool",
		Model:       m,
	})
	require.NoError(t, err)

	agentTool := NewTypedAgentTool[*schema.AgenticMessage](ctx, agent)

	info, err := agentTool.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, "tool-agent", info.Name)

	result, err := agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"request":"test"}`)
	require.NoError(t, err)
	assert.Contains(t, result, "tool response")
}
func TestCoverage_CopyAgenticEvent(t *testing.T) {
	original := &TypedAgentEvent[*schema.AgenticMessage]{
		AgentName: "agent1",
		RunPath:   []RunStep{{agentName: "root"}, {agentName: "agent1"}},
		Output: &TypedAgentOutput[*schema.AgenticMessage]{
			MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
				Message: agenticMsg("hello"),
			},
		},
		Action: &AgentAction{
			TransferToAgent: &TransferToAgentAction{DestAgentName: "agent2"},
		},
	}

	copied := copyTypedAgentEvent(original)
	assert.Equal(t, original.AgentName, copied.AgentName)
	assert.Equal(t, len(original.RunPath), len(copied.RunPath))
	assert.Equal(t, original.Action, copied.Action)

	copied.RunPath[0].agentName = "mutated"
	assert.NotEqual(t, original.RunPath[0].agentName, copied.RunPath[0].agentName)
}

func TestCoverage_ChatModelAgent_ModelGenerateError(t *testing.T) {
	ctx := context.Background()

	testErr := errors.New("model generate failed")
	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return nil, testErr
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "error-model-agent",
		Description: "tests model generate error",
		Model:       m,
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: agent,
	})

	iter := runner.Query(ctx, "trigger error")

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
	require.Error(t, capturedErr, "should propagate model error")
}

func TestCoverage_NewTypedUserMessages(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		msgs := newTypedUserMessages[*schema.Message]("hello")
		require.Len(t, msgs, 1)
		assert.Equal(t, schema.User, msgs[0].Role)
		assert.Equal(t, "hello", msgs[0].Content)
	})

	t.Run("AgenticMessage", func(t *testing.T) {
		msgs := newTypedUserMessages[*schema.AgenticMessage]("hello")
		require.Len(t, msgs, 1)
		assert.Equal(t, schema.AgenticRoleTypeUser, msgs[0].Role)
	})
}

func TestCoverage_TypedEndpointModel_NilEndpoints(t *testing.T) {
	ctx := context.Background()

	m := &typedEndpointModel[*schema.AgenticMessage]{}

	_, err := m.Generate(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "generate endpoint not set")

	_, err = m.Stream(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stream endpoint not set")
}

func TestCoverage_TypedEndpointModel_WithEndpoints(t *testing.T) {
	ctx := context.Background()

	expected := agenticMsg("generated")
	m := &typedEndpointModel[*schema.AgenticMessage]{
		generate: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return expected, nil
		},
		stream: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
			r, w := schema.Pipe[*schema.AgenticMessage](1)
			go func() {
				defer w.Close()
				w.Send(expected, nil)
			}()
			return r, nil
		},
	}

	result, err := m.Generate(ctx, nil)
	assert.NoError(t, err)
	assert.Equal(t, expected, result)

	stream, err := m.Stream(ctx, nil)
	assert.NoError(t, err)
	require.NotNil(t, stream)
	msg, err := stream.Recv()
	assert.NoError(t, err)
	assert.Equal(t, expected, msg)
	_, err = stream.Recv()
	assert.Equal(t, io.EOF, err)
}

func TestCoverage_SetAutomaticClose(t *testing.T) {
	r, w := schema.Pipe[*schema.AgenticMessage](1)
	go func() {
		defer w.Close()
		w.Send(agenticMsg("data"), nil)
	}()

	event := &TypedAgentEvent[*schema.AgenticMessage]{
		Output: &TypedAgentOutput[*schema.AgenticMessage]{
			MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
				IsStreaming:   true,
				MessageStream: r,
			},
		},
	}

	typedSetAutomaticClose(event)
}

func TestConcatMessageStream_AgenticClosesStream(t *testing.T) {
	r, w := schema.Pipe[*schema.AgenticMessage](2)
	go func() {
		defer w.Close()
		w.Send(agenticMsg("a"), nil)
		w.Send(agenticMsg("b"), nil)
	}()

	result, err := concatMessageStream(r)
	require.NoError(t, err)
	require.NotNil(t, result)

	_, recvErr := r.Recv()
	assert.Error(t, recvErr,
		"stream should be closed after concatMessageStream returns")
}

// --- Agentic retry/failover stream test helpers ---

func agenticStreamWithMidError(chunks []*schema.AgenticMessage, err error) *schema.StreamReader[*schema.AgenticMessage] {
	sr, sw := schema.Pipe[*schema.AgenticMessage](len(chunks) + 1)
	go func() {
		defer sw.Close()
		for _, c := range chunks {
			sw.Send(c, nil)
		}
		sw.Send(nil, err)
	}()
	return sr
}

func agenticStreamOK(chunks []*schema.AgenticMessage) *schema.StreamReader[*schema.AgenticMessage] {
	sr, sw := schema.Pipe[*schema.AgenticMessage](len(chunks))
	go func() {
		defer sw.Close()
		for _, c := range chunks {
			sw.Send(c, nil)
		}
	}()
	return sr
}

func drainTypedAgenticEvents(t *testing.T, iter *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]]) *schema.AgenticMessage {
	t.Helper()
	var lastMsg *schema.AgenticMessage
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			var willRetry *WillRetryError
			if errors.As(ev.Err, &willRetry) {
				continue
			}
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			if ev.Output.MessageOutput.IsStreaming && ev.Output.MessageOutput.MessageStream != nil {
				sr := ev.Output.MessageOutput.MessageStream
				for {
					chunk, err := sr.Recv()
					if err != nil {
						break
					}
					lastMsg = chunk
				}
			} else if ev.Output.MessageOutput.Message != nil {
				lastMsg = ev.Output.MessageOutput.Message
			}
		}
	}
	return lastMsg
}

func TestAgenticRetryWithShouldRetry_Generate(t *testing.T) {
	ctx := context.Background()

	var callCount int32
	var shouldRetryCalls int32
	genErr := errors.New("transient generate error")

	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			n := atomic.AddInt32(&callCount, 1)
			if n == 1 {
				return nil, genErr
			}
			return agenticMsg("retry ok"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "retry-gen-agent",
		Description: "test retry generate",
		Model:       m,
		ModelRetryConfig: &TypedModelRetryConfig[*schema.AgenticMessage]{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *TypedRetryContext[*schema.AgenticMessage]) *TypedRetryDecision[*schema.AgenticMessage] {
				n := atomic.AddInt32(&shouldRetryCalls, 1)
				if n == 1 {
					assert.Nil(t, retryCtx.OutputMessage, "OutputMessage should be nil when Generate returns error")
					assert.ErrorIs(t, retryCtx.Err, genErr, "Err should be the generate error")
					assert.Equal(t, 1, retryCtx.RetryAttempt)
					return &TypedRetryDecision[*schema.AgenticMessage]{Retry: true}
				}
				return &TypedRetryDecision[*schema.AgenticMessage]{Retry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
	iter := runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})

	msg := drainTypedAgenticEvents(t, iter)
	require.NotNil(t, msg, "should have received a final message")
	assert.Equal(t, "retry ok", agenticTextContent(msg))
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount), "model should be called twice")
	assert.Equal(t, int32(2), atomic.LoadInt32(&shouldRetryCalls), "ShouldRetry should be called for both attempts")
}

func TestAgenticRetryWithShouldRetry_Stream(t *testing.T) {
	ctx := context.Background()

	var streamCallCount int32
	var shouldRetryCalls int32
	streamErr := errors.New("mid-stream error")

	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return nil, errors.New("generate should not be called")
		},
		streamFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
			n := atomic.AddInt32(&streamCallCount, 1)
			if n == 1 {
				return agenticStreamWithMidError(
					[]*schema.AgenticMessage{agenticMsg("partial")},
					streamErr,
				), nil
			}
			return agenticStreamOK([]*schema.AgenticMessage{agenticMsg("stream ok")}), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "retry-stream-agent",
		Description: "test retry stream",
		Model:       m,
		ModelRetryConfig: &TypedModelRetryConfig[*schema.AgenticMessage]{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *TypedRetryContext[*schema.AgenticMessage]) *TypedRetryDecision[*schema.AgenticMessage] {
				n := atomic.AddInt32(&shouldRetryCalls, 1)
				if n == 1 {
					assert.NotNil(t, retryCtx.OutputMessage, "OutputMessage should be non-nil from partial stream")
					assert.Error(t, retryCtx.Err, "Err should be the stream error")
					return &TypedRetryDecision[*schema.AgenticMessage]{Retry: true}
				}
				return nil
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:          agent,
		EnableStreaming: true,
	})
	iter := runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})

	var lastMsg *schema.AgenticMessage
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			var willRetry *WillRetryError
			if errors.As(ev.Err, &willRetry) {
				continue
			}
			t.Fatalf("unexpected error: %v", ev.Err)
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			if ev.Output.MessageOutput.IsStreaming && ev.Output.MessageOutput.MessageStream != nil {
				sr := ev.Output.MessageOutput.MessageStream
				for {
					chunk, err := sr.Recv()
					if err != nil {
						break
					}
					lastMsg = chunk
				}
			} else if ev.Output.MessageOutput.Message != nil {
				lastMsg = ev.Output.MessageOutput.Message
			}
		}
	}
	require.NotNil(t, lastMsg, "should have received final stream message")
	assert.Contains(t, agenticTextContent(lastMsg), "stream ok")
	assert.Equal(t, int32(2), atomic.LoadInt32(&shouldRetryCalls), "ShouldRetry should be called for both attempts")
}

func TestAgenticFailoverGenerate(t *testing.T) {
	ctx := context.Background()

	m1Err := errors.New("m1 generate failed")
	var m1Calls, m2Calls int32

	m1 := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, m1Err
		},
	}
	m2 := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			atomic.AddInt32(&m2Calls, 1)
			return agenticMsg("failover ok"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "failover-gen-agent",
		Description: "test failover generate",
		Model:       m1,
		ModelFailoverConfig: &TypedModelFailoverConfig[*schema.AgenticMessage]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.AgenticMessage, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *TypedFailoverContext[*schema.AgenticMessage]) (model.BaseModel[*schema.AgenticMessage], []*schema.AgenticMessage, error) {
				assert.Equal(t, uint(1), failoverCtx.FailoverAttempt)
				assert.Nil(t, failoverCtx.LastOutputMessage, "LastOutputMessage should be nil when Generate returns error")
				assert.ErrorIs(t, failoverCtx.LastErr, m1Err)
				return m2, nil, nil
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
	iter := runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})

	msg := drainTypedAgenticEvents(t, iter)
	require.NotNil(t, msg)
	assert.Equal(t, "failover ok", agenticTextContent(msg))
	assert.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	assert.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
}

func TestAgenticFailoverStream_MidStreamError(t *testing.T) {
	ctx := context.Background()

	streamErr := errors.New("m1 mid-stream error")
	var m1Calls, m2Calls int32
	var capturedLastOutput *schema.AgenticMessage

	m1 := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return nil, errors.New("unused")
		},
		streamFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
			atomic.AddInt32(&m1Calls, 1)
			return agenticStreamWithMidError(
				[]*schema.AgenticMessage{agenticMsg("partial chunk")},
				streamErr,
			), nil
		},
	}
	m2 := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			return nil, errors.New("unused")
		},
		streamFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
			atomic.AddInt32(&m2Calls, 1)
			return agenticStreamOK([]*schema.AgenticMessage{agenticMsg("failover stream ok")}), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "failover-stream-agent",
		Description: "test failover stream",
		Model:       m1,
		ModelFailoverConfig: &TypedModelFailoverConfig[*schema.AgenticMessage]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.AgenticMessage, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, failoverCtx *TypedFailoverContext[*schema.AgenticMessage]) (model.BaseModel[*schema.AgenticMessage], []*schema.AgenticMessage, error) {
				capturedLastOutput = failoverCtx.LastOutputMessage
				return m2, nil, nil
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:          agent,
		EnableStreaming: true,
	})
	iter := runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})

	var lastMsg *schema.AgenticMessage
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			var willRetry *WillRetryError
			if errors.As(ev.Err, &willRetry) {
				continue
			}
			t.Fatalf("unexpected error: %v", ev.Err)
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			if ev.Output.MessageOutput.IsStreaming && ev.Output.MessageOutput.MessageStream != nil {
				sr := ev.Output.MessageOutput.MessageStream
				for {
					chunk, err := sr.Recv()
					if err != nil {
						break
					}
					lastMsg = chunk
				}
			} else if ev.Output.MessageOutput.Message != nil {
				lastMsg = ev.Output.MessageOutput.Message
			}
		}
	}

	require.NotNil(t, lastMsg, "should have received final stream from m2")
	assert.Contains(t, agenticTextContent(lastMsg), "failover stream ok")
	assert.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	assert.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	assert.NotNil(t, capturedLastOutput, "failoverCtx.LastOutputMessage should contain partial stream from m1")
}
