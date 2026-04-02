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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/callbacks"
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
	assert.NotNil(t, iter)

	event, ok := iter.Next()
	assert.True(t, ok)
	assert.NotNil(t, event)
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)

	msg := event.Output.MessageOutput.Message
	assert.NotNil(t, msg)
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

	assert.NotNil(t, receivedToolInfos, "model.WithTools option should be passed to Generate")
	assert.Equal(t, 1, len(receivedToolInfos))
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
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.NotNil(t, event.Output.MessageOutput.MessageStream)
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

func TestAgenticRunner(t *testing.T) {
	ctx := context.Background()

	agenticResponse := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "runner response"}),
		},
	}

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticResponse, nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "RunnerAgent",
		Description: "Runner test agent",
		Instruction: "Be helpful.",
		Model:       m,
	})
	assert.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: agent,
	})

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

	afterAgenticModelExecuted := false

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "AgenticMiddlewareAgent",
		Description: "Agentic agent with middleware",
		Instruction: "You are helpful.",
		Model:       m,
		Middlewares: []AgentMiddleware{
			{
				BeforeAgenticModel: func(ctx context.Context, state *TypedChatModelAgentState[*schema.AgenticMessage]) error {
					state.Messages = append(state.Messages, schema.UserAgenticMessage("extra"))
					return nil
				},
				AfterAgenticModel: func(ctx context.Context, state *TypedChatModelAgentState[*schema.AgenticMessage]) error {
					assert.Len(t, state.Messages, 4)
					afterAgenticModelExecuted = true
					return nil
				},
			},
		},
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
	assert.NotNil(t, event.Output.MessageOutput.Message)
	assert.Equal(t, schema.AgenticRoleTypeAssistant, event.Output.MessageOutput.Message.Role)
	_, ok = iter.Next()
	assert.False(t, ok)
	assert.True(t, afterAgenticModelExecuted)
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
		Description: "Test AfterAgenticModel",
		Instruction: "You are helpful.",
		Model:       m,
		Middlewares: []AgentMiddleware{
			{
				AfterAgenticModel: func(ctx context.Context, state *TypedChatModelAgentState[*schema.AgenticMessage]) error {
					capturedMessages = make([]*schema.AgenticMessage, len(state.Messages))
					copy(capturedMessages, state.Messages)
					state.Messages = append(state.Messages, agenticAssistantMessage("appended content"))
					return nil
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
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)

	msg := event.Output.MessageOutput.Message
	assert.NotNil(t, msg)
	assert.Equal(t, "original content", msg.ContentBlocks[0].AssistantGenText.Text)

	_, ok = iterator.Next()
	assert.False(t, ok)

	assert.Len(t, capturedMessages, 3)
}

func TestAgenticChatModelAgentRun_ErrorHandling(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return nil, errors.New("model error")
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "AgenticErrorAgent",
		Description: "Test error handling",
		Instruction: "You are helpful.",
		Model:       m,
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
	assert.NotNil(t, event.Err)
	assert.Contains(t, event.Err.Error(), "model error")

	_, ok = iterator.Next()
	assert.False(t, ok)
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
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

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
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

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
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

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
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
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
	assert.Equal(t, 1, len(mockAgent_.lastInput.Messages))
	assert.True(t, mockAgent_.enableStreaming)

	event, ok := iterator.Next()
	assert.True(t, ok)
	assert.Equal(t, "AgenticStreamQueryAgent", event.AgentName)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.True(t, event.Output.MessageOutput.IsStreaming)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestAgenticTransferToAgent(t *testing.T) {
	ctx := context.Background()

	parentAgent := &myAgenticAgent{
		name: "ParentAgent",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticAssistantMessage("I'll transfer this to the child agent"),
						},
					},
				})
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					Action: NewTransferToAgentAction("ChildAgent"),
				})
			}()
			return iter
		},
	}

	childAgent := &myAgenticAgent{
		name: "ChildAgent",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticAssistantMessage("Hello from child agent"),
						},
					},
				})
			}()
			return iter
		},
	}

	flowAgent, err := TypedSetSubAgents[*schema.AgenticMessage](ctx, TypedAgent[*schema.AgenticMessage](parentAgent), []TypedAgent[*schema.AgenticMessage]{childAgent})
	assert.NoError(t, err)
	assert.NotNil(t, flowAgent)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Please transfer this to the child agent"),
		},
	}
	ctx, _ = initTypedRunCtx[*schema.AgenticMessage](ctx, flowAgent.Name(ctx), input)
	iterator := flowAgent.Run(ctx, input)

	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.MessageOutput)

	event2, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event2.Err)
	assert.NotNil(t, event2.Action)
	assert.NotNil(t, event2.Action.TransferToAgent)
	assert.Equal(t, "ChildAgent", event2.Action.TransferToAgent.DestAgentName)

	event3, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event3.Err)
	assert.NotNil(t, event3.Output)
	assert.NotNil(t, event3.Output.MessageOutput)
	msg := event3.Output.MessageOutput.Message
	assert.NotNil(t, msg)
	assert.Equal(t, "Hello from child agent", msg.ContentBlocks[0].AssistantGenText.Text)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestAgenticTransferToAgentWithDesignatedCallback(t *testing.T) {
	ctx := context.Background()

	parentAgent := &myAgenticAgent{
		name: "ParentAgent",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticAssistantMessage("Transferring"),
						},
					},
				})
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					Action: NewTransferToAgentAction("ChildAgent"),
				})
			}()
			return iter
		},
	}

	childAgent := &myAgenticAgent{
		name: "ChildAgent",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			go func() {
				defer generator.Close()
				generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
					Output: &TypedAgentOutput[*schema.AgenticMessage]{
						MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
							Message: agenticAssistantMessage("Hello from child agent"),
						},
					},
				})
			}()
			return iter
		},
	}

	flowAgent, err := TypedSetSubAgents[*schema.AgenticMessage](ctx, TypedAgent[*schema.AgenticMessage](parentAgent), []TypedAgent[*schema.AgenticMessage]{childAgent})
	assert.NoError(t, err)

	handler := callbacks.NewHandlerBuilder().OnStartFn(
		func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			return ctx
		}).Build()

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Please transfer"),
		},
	}
	ctx, _ = initTypedRunCtx[*schema.AgenticMessage](ctx, flowAgent.Name(ctx), input)
	iterator := flowAgent.Run(ctx, input, WithCallbacks(handler).DesignateAgent("ChildAgent"))

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.GreaterOrEqual(t, len(events), 2)

	var foundTransfer bool
	var foundChildOutput bool
	for _, event := range events {
		if event.Action != nil && event.Action.TransferToAgent != nil {
			foundTransfer = true
			assert.Equal(t, "ChildAgent", event.Action.TransferToAgent.DestAgentName)
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			msg := event.Output.MessageOutput.Message
			if len(msg.ContentBlocks) > 0 && msg.ContentBlocks[0].AssistantGenText != nil &&
				msg.ContentBlocks[0].AssistantGenText.Text == "Hello from child agent" {
				foundChildOutput = true
			}
		}
	}
	assert.True(t, foundTransfer)
	assert.True(t, foundChildOutput)
}

func TestAgenticSequentialAgent(t *testing.T) {
	ctx := context.Background()

	agent1 := &mockAgenticAgent{
		name:        "Agent1",
		description: "First agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "Agent1",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticAssistantMessage("Response from Agent1"),
					},
				},
			},
		},
	}

	agent2 := &mockAgenticAgent{
		name:        "Agent2",
		description: "Second agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "Agent2",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticAssistantMessage("Response from Agent2"),
					},
				},
			},
		},
	}

	sequentialAgent, err := NewTypedSequentialAgent[*schema.AgenticMessage](ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
		Name:        "SequentialTestAgent",
		Description: "Test sequential agent",
		SubAgents:   []TypedAgent[*schema.AgenticMessage]{agent1, agent2},
	})
	assert.NoError(t, err)
	assert.NotNil(t, sequentialAgent)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Test input"),
		},
	}

	ctx, _ = initTypedRunCtx[*schema.AgenticMessage](ctx, sequentialAgent.Name(ctx), input)

	iterator := sequentialAgent.Run(ctx, input)

	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.MessageOutput)
	assert.Equal(t, "Response from Agent1", event1.Output.MessageOutput.Message.ContentBlocks[0].AssistantGenText.Text)

	event2, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event2.Err)
	assert.NotNil(t, event2.Output)
	assert.NotNil(t, event2.Output.MessageOutput)
	assert.Equal(t, "Response from Agent2", event2.Output.MessageOutput.Message.ContentBlocks[0].AssistantGenText.Text)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestAgenticSequentialAgentWithExit(t *testing.T) {
	ctx := context.Background()

	agent1 := &mockAgenticAgent{
		name:        "Agent1",
		description: "First agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "Agent1",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticAssistantMessage("Response from Agent1"),
					},
				},
				Action: &AgentAction{
					Exit: true,
				},
			},
		},
	}

	agent2 := &mockAgenticAgent{
		name:        "Agent2",
		description: "Second agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "Agent2",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticAssistantMessage("Response from Agent2"),
					},
				},
			},
		},
	}

	sequentialAgent, err := NewTypedSequentialAgent[*schema.AgenticMessage](ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
		Name:        "SequentialTestAgent",
		Description: "Test sequential agent",
		SubAgents:   []TypedAgent[*schema.AgenticMessage]{agent1, agent2},
	})
	assert.NoError(t, err)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Test input"),
		},
	}

	ctx, _ = initTypedRunCtx[*schema.AgenticMessage](ctx, sequentialAgent.Name(ctx), input)

	iterator := sequentialAgent.Run(ctx, input)

	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Action)
	assert.True(t, event1.Action.Exit)

	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestAgenticParallelAgent(t *testing.T) {
	ctx := context.Background()

	agent1 := &mockAgenticAgent{
		name:        "Agent1",
		description: "First agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "Agent1",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticAssistantMessage("Response from Agent1"),
					},
				},
			},
		},
	}

	agent2 := &mockAgenticAgent{
		name:        "Agent2",
		description: "Second agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "Agent2",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticAssistantMessage("Response from Agent2"),
					},
				},
			},
		},
	}

	parallelAgent, err := NewTypedParallelAgent[*schema.AgenticMessage](ctx, &TypedParallelAgentConfig[*schema.AgenticMessage]{
		Name:        "ParallelTestAgent",
		Description: "Test parallel agent",
		SubAgents:   []TypedAgent[*schema.AgenticMessage]{agent1, agent2},
	})
	assert.NoError(t, err)
	assert.NotNil(t, parallelAgent)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Test input"),
		},
	}

	ctx, _ = initTypedRunCtx[*schema.AgenticMessage](ctx, parallelAgent.Name(ctx), input)

	iterator := parallelAgent.Run(ctx, input)

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.Equal(t, 2, len(events))

	for _, event := range events {
		assert.Nil(t, event.Err)
		assert.NotNil(t, event.Output)
		assert.NotNil(t, event.Output.MessageOutput)

		msg := event.Output.MessageOutput.Message
		assert.NotNil(t, msg)

		if event.AgentName == "Agent1" {
			assert.Equal(t, "Response from Agent1", msg.ContentBlocks[0].AssistantGenText.Text)
		} else if event.AgentName == "Agent2" {
			assert.Equal(t, "Response from Agent2", msg.ContentBlocks[0].AssistantGenText.Text)
		} else {
			t.Fatalf("Unexpected source agent name: %s", event.AgentName)
		}
	}
}

func TestAgenticLoopAgent(t *testing.T) {
	ctx := context.Background()

	agent := &mockAgenticAgent{
		name:        "LoopAgent",
		description: "Loop agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "LoopAgent",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticAssistantMessage("Loop iteration"),
					},
				},
			},
		},
	}

	loopAgent, err := NewTypedLoopAgent[*schema.AgenticMessage](ctx, &TypedLoopAgentConfig[*schema.AgenticMessage]{
		Name:          "LoopTestAgent",
		Description:   "Test loop agent",
		SubAgents:     []TypedAgent[*schema.AgenticMessage]{agent},
		MaxIterations: 3,
	})
	assert.NoError(t, err)
	assert.NotNil(t, loopAgent)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Test input"),
		},
	}

	ctx, _ = initTypedRunCtx[*schema.AgenticMessage](ctx, loopAgent.Name(ctx), input)

	iterator := loopAgent.Run(ctx, input)

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.Equal(t, 3, len(events))

	for _, event := range events {
		assert.Nil(t, event.Err)
		assert.NotNil(t, event.Output)
		assert.NotNil(t, event.Output.MessageOutput)

		msg := event.Output.MessageOutput.Message
		assert.NotNil(t, msg)
		assert.Equal(t, "Loop iteration", msg.ContentBlocks[0].AssistantGenText.Text)
	}
}

func TestAgenticLoopAgentWithBreakLoop(t *testing.T) {
	ctx := context.Background()

	agent := &mockAgenticAgent{
		name:        "LoopAgent",
		description: "Loop agent",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "LoopAgent",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticAssistantMessage("Loop iteration with break loop"),
					},
				},
				Action: NewBreakLoopAction("LoopAgent"),
			},
		},
	}

	loopAgent, err := NewTypedLoopAgent[*schema.AgenticMessage](ctx, &TypedLoopAgentConfig[*schema.AgenticMessage]{
		Name:          "LoopTestAgent",
		Description:   "Test loop agent",
		SubAgents:     []TypedAgent[*schema.AgenticMessage]{agent},
		MaxIterations: 3,
	})
	assert.NoError(t, err)
	assert.NotNil(t, loopAgent)

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{
			schema.UserAgenticMessage("Test input"),
		},
	}
	ctx, _ = initTypedRunCtx[*schema.AgenticMessage](ctx, loopAgent.Name(ctx), input)

	iterator := loopAgent.Run(ctx, input)

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.Equal(t, 1, len(events))

	event := events[0]
	assert.Nil(t, event.Err)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.NotNil(t, event.Action)
	assert.NotNil(t, event.Action.BreakLoop)
	assert.True(t, event.Action.BreakLoop.Done)
	assert.Equal(t, "LoopAgent", event.Action.BreakLoop.From)
	assert.Equal(t, 0, event.Action.BreakLoop.CurrentIterations)

	msg := event.Output.MessageOutput.Message
	assert.NotNil(t, msg)
	assert.Equal(t, "Loop iteration with break loop", msg.ContentBlocks[0].AssistantGenText.Text)
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

	assert.NotNil(t, interruptEvent)
	assert.Equal(t, data, interruptEvent.Action.Interrupted.Data)
	assert.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts[0].ID)
	assert.True(t, interruptEvent.Action.Interrupted.InterruptContexts[0].IsRootCause)
	assert.Equal(t, data, interruptEvent.Action.Interrupted.InterruptContexts[0].Info)
	assert.Equal(t, Address{{Type: AddressSegmentAgent, ID: "myAgenticAgent"}},
		interruptEvent.Action.Interrupted.InterruptContexts[0].Address)
}

func TestAgenticMultiAgentInterrupt(t *testing.T) {
	ctx := context.Background()
	sa1 := &myAgenticAgent{
		name: "sa1",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
				AgentName: "sa1",
				Action: &AgentAction{
					TransferToAgent: &TransferToAgentAction{
						DestAgentName: "sa2",
					},
				},
			})
			generator.Close()
			return iter
		},
	}
	sa2 := &myAgenticAgent{
		name: "sa2",
		runFn: func(ctx context.Context, input *TypedAgentInput[*schema.AgenticMessage], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			intEvent := TypedStatefulInterrupt[*schema.AgenticMessage](ctx, "hello world", "temp state")
			intEvent.Action.Interrupted.Data = "hello world"
			generator.Send(intEvent)
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
			assert.NotNil(t, info)
			assert.Equal(t, info.Data, "hello world")

			assert.True(t, info.WasInterrupted)
			assert.NotNil(t, info.InterruptState)
			assert.Equal(t, "temp state", info.InterruptState)

			assert.True(t, info.IsResumeTarget)
			assert.NotNil(t, info.ResumeData)
			assert.Equal(t, "resume data", info.ResumeData)

			iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
			generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{
				AgentName: "sa2",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{Message: schema.UserAgenticMessage(info.ResumeData.(string))},
				},
			})
			generator.Close()
			return iter
		},
	}
	a, err := TypedSetSubAgents[*schema.AgenticMessage](ctx, TypedAgent[*schema.AgenticMessage](sa1), []TypedAgent[*schema.AgenticMessage]{sa2})
	assert.NoError(t, err)
	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           a,
		EnableStreaming: false,
		CheckPointStore: newMyStore(),
	})
	iter := runner.Query(ctx, "", WithCheckPointID("1"))

	var transferEvent *TypedAgentEvent[*schema.AgenticMessage]
	var interruptEvent *TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.TransferToAgent != nil {
			transferEvent = event
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			interruptEvent = event
		}
	}

	assert.NotNil(t, transferEvent)
	assert.NotNil(t, transferEvent.Action.TransferToAgent)

	assert.NotNil(t, interruptEvent)
	assert.NotNil(t, interruptEvent.Action.Interrupted)
	assert.Equal(t, 1, len(interruptEvent.Action.Interrupted.InterruptContexts))
	assert.Equal(t, "hello world", interruptEvent.Action.Interrupted.InterruptContexts[0].Info)
	assert.True(t, interruptEvent.Action.Interrupted.InterruptContexts[0].IsRootCause)
	assert.Equal(t, Address{
		{Type: AddressSegmentAgent, ID: "sa1"},
		{Type: AddressSegmentAgent, ID: "sa2"},
	}, interruptEvent.Action.Interrupted.InterruptContexts[0].Address)
	assert.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts[0].ID)
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

func TestCascadingFrom_SetSubAgentsOf(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.AssistantGenText{Text: "response"}),
				},
			}, nil
		},
	}

	parent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "Parent",
		Description: "Parent agent",
		Model:       m,
	})
	assert.NoError(t, err)

	child, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "Child",
		Description: "Child agent",
		Model:       m,
	})
	assert.NoError(t, err)

	flow, err := TypedSetSubAgents(ctx, TypedAgent[*schema.AgenticMessage](parent), []TypedAgent[*schema.AgenticMessage]{child})
	assert.NoError(t, err)
	assert.NotNil(t, flow)
}

func TestCascadingFrom_WorkflowAgents(t *testing.T) {
	ctx := context.Background()

	newModel := func() *mockAgenticModel {
		return &mockAgenticModel{
			generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
				return &schema.AgenticMessage{
					Role: schema.AgenticRoleTypeAssistant,
					ContentBlocks: []*schema.ContentBlock{
						schema.NewContentBlock(&schema.AssistantGenText{Text: "wf response"}),
					},
				}, nil
			},
		}
	}

	makeSubs := func(names ...string) []TypedAgent[*schema.AgenticMessage] {
		var agents []TypedAgent[*schema.AgenticMessage]
		for _, name := range names {
			a, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
				Name:        name,
				Description: name + " agent",
				Model:       newModel(),
			})
			assert.NoError(t, err)
			agents = append(agents, a)
		}
		return agents
	}

	t.Run("SequentialFrom", func(t *testing.T) {
		seq, err := NewTypedSequentialAgent(ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
			Name:        "SeqFrom",
			Description: "Sequential from",
			SubAgents:   makeSubs("S1", "S2"),
		})
		assert.NoError(t, err)
		assert.NotNil(t, seq)
	})

	t.Run("ParallelFrom", func(t *testing.T) {
		par, err := NewTypedParallelAgent(ctx, &TypedParallelAgentConfig[*schema.AgenticMessage]{
			Name:        "ParFrom",
			Description: "Parallel from",
			SubAgents:   makeSubs("P1", "P2"),
		})
		assert.NoError(t, err)
		assert.NotNil(t, par)
	})

	t.Run("LoopFrom", func(t *testing.T) {
		loop, err := NewTypedLoopAgent(ctx, &TypedLoopAgentConfig[*schema.AgenticMessage]{
			Name:          "LoopFrom",
			Description:   "Loop from",
			SubAgents:     makeSubs("L1", "L2"),
			MaxIterations: 5,
		})
		assert.NoError(t, err)
		assert.NotNil(t, loop)
	})
}

func TestCascadingTyped_NewTypedAgentTool(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.AssistantGenText{Text: "tool response"}),
				},
			}, nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "ToolAgent",
		Description: "Agent wrapped as tool",
		Model:       m,
	})
	assert.NoError(t, err)

	agentTool := NewTypedAgentTool(ctx, TypedAgent[*schema.AgenticMessage](agent))
	assert.NotNil(t, agentTool)

	info, err := agentTool.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "ToolAgent", info.Name)
}

func TestCascadingTyped_TypedInterrupt(t *testing.T) {
	ctx := context.Background()
	ctx = AppendAddressSegment(ctx, AddressSegmentAgent, "test-agent")

	event := TypedInterrupt[*schema.AgenticMessage](ctx, "please confirm")
	assert.NotNil(t, event)
	assert.NotNil(t, event.Action)
	assert.NotNil(t, event.Action.Interrupted)
}

func TestCascadingTyped_TypedStatefulInterrupt(t *testing.T) {
	ctx := context.Background()
	ctx = AppendAddressSegment(ctx, AddressSegmentAgent, "test-agent")

	type myState struct {
		Count int
	}

	event := TypedStatefulInterrupt[*schema.AgenticMessage](ctx, "please confirm", &myState{Count: 42})
	assert.NotNil(t, event)
	assert.NotNil(t, event.Action)
	assert.NotNil(t, event.Action.Interrupted)
}

func TestCascadingTyped_TypedEventFromMessage(t *testing.T) {
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "hello"}),
		},
	}

	event := TypedEventFromMessage(msg, nil, schema.Assistant, "")
	assert.NotNil(t, event)
	assert.NotNil(t, event.Output)
	assert.NotNil(t, event.Output.MessageOutput)
	assert.Equal(t, msg, event.Output.MessageOutput.Message)
	assert.Equal(t, schema.RoleType(schema.Assistant), event.Output.MessageOutput.Role)
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

	fa := toTypedFlowAgent[*schema.AgenticMessage](ctx, agent)

	info := &ResumeInfo{WasInterrupted: true}
	iter := fa.Resume(ctx, info)

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
	assert.True(t, foundErr, "should get error for non-resumable agent")
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

func TestCoverage_TypedSendTransferEvents_Agentic(t *testing.T) {
	iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
	go func() {
		typedSendTransferEvents(gen, []string{"agent-b", "agent-c"})
		gen.Close()
	}()

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	require.Len(t, events, 4)
	assert.NotNil(t, events[0].Output)
	assert.NotNil(t, events[1].Action)
	assert.Equal(t, "agent-b", events[1].Action.TransferToAgent.DestAgentName)
	assert.NotNil(t, events[2].Output)
	assert.NotNil(t, events[3].Action)
	assert.Equal(t, "agent-c", events[3].Action.TransferToAgent.DestAgentName)
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
	assert.NotNil(t, retEvent)
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

func TestCoverage_RewriteAgenticMessage(t *testing.T) {
	t.Run("AssistantMessage", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "I said hello"}),
				{
					Type:             schema.ContentBlockTypeFunctionToolCall,
					FunctionToolCall: &schema.FunctionToolCall{Name: "search", Arguments: `{"q":"test"}`},
				},
				nil,
			},
		}
		rewritten := rewriteAgenticMessage(msg, "AgentA")
		assert.Equal(t, schema.AgenticRoleTypeUser, rewritten.Role)
		assert.Contains(t, rewritten.ContentBlocks[0].UserInputText.Text, "AgentA")
		assert.Contains(t, rewritten.ContentBlocks[0].UserInputText.Text, "I said hello")
		assert.Contains(t, rewritten.ContentBlocks[0].UserInputText.Text, "search")
	})

	t.Run("UserToolResultMessage", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						Name:   "search",
						Result: "found it",
					},
				},
				nil,
			},
		}
		rewritten := rewriteAgenticMessage(msg, "AgentB")
		assert.Equal(t, schema.AgenticRoleTypeUser, rewritten.Role)
		assert.Contains(t, rewritten.ContentBlocks[0].UserInputText.Text, "AgentB")
		assert.Contains(t, rewritten.ContentBlocks[0].UserInputText.Text, "found it")
	})

	t.Run("PreserveUserInputBlocks", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "text"}),
				{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "user text"}},
				{Type: schema.ContentBlockTypeUserInputImage, UserInputImage: &schema.UserInputImage{}},
			},
		}
		rewritten := rewriteAgenticMessage(msg, "AgentC")
		assert.True(t, len(rewritten.ContentBlocks) >= 3)
	})
}

func TestCoverage_FlowAgent_WorkflowSubAgent(t *testing.T) {
	ctx := context.Background()

	step1 := &mockAgenticAgent{
		name:        "step1",
		description: "step 1",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "step1",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticMsg("step1 done"),
					},
				},
			},
		},
	}

	step2 := &mockAgenticAgent{
		name:        "step2",
		description: "step 2",
		responses: []*TypedAgentEvent[*schema.AgenticMessage]{
			{
				AgentName: "step2",
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: agenticMsg("step2 done"),
					},
				},
			},
		},
	}

	seqAgent, err := NewTypedSequentialAgent[*schema.AgenticMessage](ctx, &TypedSequentialAgentConfig[*schema.AgenticMessage]{
		Name:      "seq",
		SubAgents: []TypedAgent[*schema.AgenticMessage]{step1, step2},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: seqAgent,
	})

	iter := runner.Run(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("start seq"),
	})

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	require.NotEmpty(t, events)
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

	copied := copyAgenticEvent(original)
	assert.Equal(t, original.AgentName, copied.AgentName)
	assert.Equal(t, len(original.RunPath), len(copied.RunPath))
	assert.Equal(t, original.Action, copied.Action)

	copied.RunPath[0].agentName = "mutated"
	assert.NotEqual(t, original.RunPath[0].agentName, copied.RunPath[0].agentName)
}

func TestCoverage_GenAgenticTransferMessages(t *testing.T) {
	aMsg, tMsg := genAgenticTransferMessages("target-agent")
	require.NotNil(t, aMsg)
	require.NotNil(t, tMsg)

	assert.Equal(t, schema.AgenticRoleTypeAssistant, aMsg.Role)
	assert.Equal(t, schema.AgenticRoleTypeUser, tMsg.Role)

	var hasToolCall bool
	for _, block := range aMsg.ContentBlocks {
		if block.Type == schema.ContentBlockTypeFunctionToolCall {
			hasToolCall = true
			assert.Equal(t, TransferToAgentToolName, block.FunctionToolCall.Name)
		}
	}
	assert.True(t, hasToolCall)

	var hasToolResult bool
	for _, block := range tMsg.ContentBlocks {
		if block.Type == schema.ContentBlockTypeFunctionToolResult {
			hasToolResult = true
			assert.Equal(t, TransferToAgentToolName, block.FunctionToolResult.Name)
			assert.Contains(t, block.FunctionToolResult.Result, "target-agent")
		}
	}
	assert.True(t, hasToolResult)
}

func TestCoverage_FlowAgent_HistoryRewriter(t *testing.T) {
	ctx := context.Background()

	var receivedMessages []*schema.AgenticMessage
	m := &mockAgenticModel{
		generateFn: func(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			receivedMessages = input
			return agenticMsg("done"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "rewriter-agent",
		Description: "agent with history rewriter",
		Model:       m,
	})
	require.NoError(t, err)

	histRewriter := TypedHistoryRewriter[*schema.AgenticMessage](func(_ context.Context, entries []*TypedHistoryEntry[*schema.AgenticMessage]) ([]*schema.AgenticMessage, error) {
		var msgs []*schema.AgenticMessage
		for _, e := range entries {
			msgs = append(msgs, e.Message)
		}
		msgs = append(msgs, schema.UserAgenticMessage("injected by rewriter"))
		return msgs, nil
	})

	fa := toTypedFlowAgent[*schema.AgenticMessage](ctx, agent, TypedWithHistoryRewriter[*schema.AgenticMessage](histRewriter))

	input := &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("original")},
	}
	iter := fa.Run(ctx, input)

	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	found := false
	for _, msg := range receivedMessages {
		for _, b := range msg.ContentBlocks {
			if b.UserInputText != nil && b.UserInputText.Text == "injected by rewriter" {
				found = true
			}
		}
	}
	assert.True(t, found, "history rewriter should inject messages")
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
	assert.True(t, foundErr, "should propagate model error")
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
	assert.NotNil(t, stream)
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
