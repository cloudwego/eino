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

package react

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestWithMessageFuture(t *testing.T) {
	ctx := context.Background()

	// Test with tool calls
	t.Run("test generate with tool calls", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		// Mock model response with tool call
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("final response", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create agent with MessageFuture
		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		// Generate response
		response, err := a.Generate(ctx, []*schema.Message{
			schema.UserMessage("use the greet tool"),
		}, option)
		assert.Nil(t, err)
		assert.Equal(t, "final response", response.Content)

		sIter := future.GetMessageStreams()
		// Should be no messages
		_, hasNext, err := sIter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)

		iter := future.GetMessages()
		// First message should be the assistant message for tool calling
		msg1, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Assistant, msg1.Role)
		assert.Equal(t, 1, len(msg1.ToolCalls))

		// Second message should be the tool response
		msg2, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Tool, msg2.Role)

		// Third message should be the final response
		msg3, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, "final response", msg3.Content)

		// Should be no more messages
		_, hasNext, err = iter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})
	// Test with streaming tool calls
	t.Run("test generate with streaming tool calls", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeStreamToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		// Mock model response with tool call
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("final response", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create agent with MessageFuture
		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		// Generate response
		response, err := a.Generate(ctx, []*schema.Message{
			schema.UserMessage("use the greet tool"),
		}, option)
		assert.Nil(t, err)
		assert.Equal(t, "final response", response.Content)

		// Get messages from future
		iter := future.GetMessages()

		// First message should be the assistant message for tool calling
		msg1, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Assistant, msg1.Role)
		assert.Equal(t, 1, len(msg1.ToolCalls))

		// Second message should be the tool response
		msg2, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Tool, msg2.Role)

		// Third message should be the final response
		msg3, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, "final response", msg3.Content)

		// Should be no more messages
		_, hasNext, err = iter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})

	// Test with non-streaming tool but using agent's Stream interface
	t.Run("test stream with tool calls", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		// Mock model response with tool call
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					},
				})}), nil).
			Times(1)
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final response", nil)}), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create agent with MessageFuture
		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		// Use Stream interface
		stream, err := a.Stream(ctx, []*schema.Message{
			schema.UserMessage("use the greet tool"),
		}, option)
		assert.Nil(t, err)

		// Collect all chunks from stream
		finalResponse, err := schema.ConcatMessageStream(stream)
		assert.Nil(t, err)
		assert.Equal(t, "final response", finalResponse.Content)

		iter := future.GetMessages()
		// Should be no messages
		_, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)

		// Get message streams from future
		sIter := future.GetMessageStreams()

		// First message should be the assistant message for tool calling
		stream1, hasNext, err := sIter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.NotNil(t, stream1)
		msg1, err := schema.ConcatMessageStream(stream1)
		assert.Nil(t, err)
		assert.Equal(t, schema.Assistant, msg1.Role)
		assert.Equal(t, 1, len(msg1.ToolCalls))

		// Second message should be the tool response
		stream2, hasNext, err := sIter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.NotNil(t, stream2)
		msg2, err := schema.ConcatMessageStream(stream2)
		assert.Nil(t, err)
		assert.Equal(t, schema.Tool, msg2.Role)

		// Third message should be the final response
		stream3, hasNext, err := sIter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.NotNil(t, stream3)
		msg3, err := schema.ConcatMessageStream(stream3)
		assert.Nil(t, err)
		assert.Equal(t, "final response", msg3.Content)

		// Should be no more messages
		_, hasNext, err = sIter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})

	t.Run("test stream with streaming tool calls and with concurrent goroutines", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeStreamToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		// Mock model response with tool call
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					},
				})}), nil).
			Times(1)
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final response", nil)}), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create agent with MessageFuture
		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Get message streams from future
			sIter := future.GetMessageStreams()

			// First message should be the assistant message for tool calling
			stream1, hasNext, err_ := sIter.Next()
			assert.Nil(t, err_)
			assert.True(t, hasNext)
			assert.NotNil(t, stream1)
			msg1, err_ := schema.ConcatMessageStream(stream1)
			assert.Nil(t, err_)
			assert.Equal(t, schema.Assistant, msg1.Role)
			assert.Equal(t, 1, len(msg1.ToolCalls))

			// Second message should be the tool response
			stream2, hasNext, err_ := sIter.Next()
			assert.Nil(t, err_)
			assert.True(t, hasNext)
			assert.NotNil(t, stream2)
			msg2, err_ := schema.ConcatMessageStream(stream2)
			assert.Nil(t, err_)
			assert.Equal(t, schema.Tool, msg2.Role)

			// Third message should be the final response
			stream3, hasNext, err_ := sIter.Next()
			assert.Nil(t, err_)
			assert.True(t, hasNext)
			assert.NotNil(t, stream3)
			msg3, err_ := schema.ConcatMessageStream(stream3)
			assert.Nil(t, err_)
			assert.Equal(t, "final response", msg3.Content)

			// Should be no more messages
			_, hasNext, err_ = sIter.Next()
			assert.Nil(t, err_)
			assert.False(t, hasNext)
		}()

		// Use Stream interface
		stream, err := a.Stream(ctx, []*schema.Message{
			schema.UserMessage("use the greet tool"),
		}, option)
		assert.Nil(t, err)

		// Collect all chunks from stream
		finalResponse, err := schema.ConcatMessageStream(stream)
		assert.Nil(t, err)
		assert.Equal(t, "final response", finalResponse.Content)

		wg.Wait()
	})
}

func TestWithToolOptions(t *testing.T) {
	type dummyOpt struct{ val string }
	opt := tool.WrapImplSpecificOptFn(func(o *dummyOpt) { o.val = "mock" })
	agentOpt := WithToolOptions(opt)
	assert.NotNil(t, agentOpt)
	// The returned value should be an agent.AgentOption (function)
	assert.IsType(t, agentOpt, agentOpt)
}

func TestWithChatModelOptions(t *testing.T) {
	opt := model.WithModel("mock-model")
	agentOpt := WithChatModelOptions(opt)
	assert.NotNil(t, agentOpt)
	assert.IsType(t, agentOpt, agentOpt)
}

// dummyBaseTool is a minimal implementation of tool.BaseTool for testing.
type dummyBaseTool struct{}

func (d *dummyBaseTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "dummy"}, nil
}

func (d *dummyBaseTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	return "dummy-response", nil
}

type assertTool struct {
	toolOptVal      string
	receivedToolOpt bool
}
type toolOpt struct{ val string }

func (a *assertTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "assert_tool"}, nil
}
func (a *assertTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	opt := tool.GetImplSpecificOptions(&toolOpt{}, opts...)
	if opt.val == a.toolOptVal {
		a.receivedToolOpt = true
	}
	return "tool-response", nil
}

func TestAgentWithAllOptions(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	// Prepare a tool that asserts it receives the tool option
	toolOptVal := "tool-opt-value"
	to := tool.WrapImplSpecificOptFn(func(o *toolOpt) { o.val = toolOptVal })
	at := &assertTool{toolOptVal: toolOptVal}

	// Prepare a mock chat model that asserts it receives the model option
	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	modelOpt := model.WithModel("test-model")
	modelOptReceived := false
	times := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			times++
			if times == 1 {
				for _, o := range opts {
					opt := model.GetCommonOptions(&model.Options{}, o)
					if opt.Model != nil && *opt.Model == "test-model" {
						modelOptReceived = true
					}
				}

				info, _ := at.Info(ctx)
				return schema.AssistantMessage("hello max",
						[]schema.ToolCall{
							{
								ID: randStr(),
								Function: schema.FunctionCall{
									Name:      info.Name,
									Arguments: "",
								},
							},
						}),
					nil
			}

			return schema.AssistantMessage("ok", nil), nil
		},
	).AnyTimes()
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	agentOpt := WithToolOptions(to)
	agentOpt2 := WithChatModelOptions(modelOpt)
	agentOpt3, err := WithTools(context.Background(), at)
	assert.NoError(t, err)

	a, err := NewAgent(ctx, &AgentConfig{
		ToolCallingModel: cm,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{&dummyBaseTool{}},
		},
		MaxStep: 20,
	})
	assert.NoError(t, err)

	_, err = a.Generate(ctx, []*schema.Message{
		schema.UserMessage("call the tool"),
	}, agentOpt, agentOpt2, agentOpt3[0], agentOpt3[1])
	assert.NoError(t, err)
	assert.True(t, modelOptReceived, "model option should be received by chat model")
	assert.True(t, at.receivedToolOpt, "tool option should be received by tool")
}

type simpleToolForMiddlewareTest struct {
	name   string
	result string
}

func (s *simpleToolForMiddlewareTest) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: s.name,
		Desc: "simple tool for middleware test",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"input": {
					Desc:     "input",
					Required: true,
					Type:     schema.String,
				},
			}),
	}, nil
}

func (s *simpleToolForMiddlewareTest) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return s.result, nil
}

func (s *simpleToolForMiddlewareTest) StreamableRun(_ context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	return schema.StreamReaderFromArray([]string{s.result}), nil
}

func TestMessageFuture_ToolResultMiddleware_EmitsFinalResult(t *testing.T) {
	originalResult := "original_result"
	modifiedResult := "modified_by_middleware"

	resultModifyingMiddleware := compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				output, err := next(ctx, input)
				if err != nil {
					return nil, err
				}
				output.Result = modifiedResult
				return output, nil
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				output, err := next(ctx, input)
				if err != nil {
					return nil, err
				}
				output.Result = schema.StreamReaderFromArray([]string{modifiedResult})
				return output, nil
			}
		},
	}

	t.Run("Invoke", func(t *testing.T) {
		ctx := context.Background()
		testTool := &simpleToolForMiddlewareTest{name: "test_tool", result: originalResult}

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		info, err := testTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"input": "test"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("final response", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools:               []tool.BaseTool{testTool},
				ToolCallMiddlewares: []compose.ToolMiddleware{resultModifyingMiddleware},
			},
			MaxStep: 3,
		})
		assert.NoError(t, err)

		response, err := a.Generate(ctx, []*schema.Message{
			schema.UserMessage("call the tool"),
		}, option)
		assert.NoError(t, err)
		assert.Equal(t, "final response", response.Content)

		iter := future.GetMessages()

		var allMsgs []*schema.Message
		for {
			msg, hasNext, err := iter.Next()
			if err != nil || !hasNext {
				break
			}
			allMsgs = append(allMsgs, msg)
		}

		assert.GreaterOrEqual(t, len(allMsgs), 3, "should have at least 3 messages")
		if len(allMsgs) >= 3 {
			assert.Equal(t, schema.Assistant, allMsgs[0].Role)
			assert.Equal(t, 1, len(allMsgs[0].ToolCalls))

			assert.Equal(t, schema.Tool, allMsgs[1].Role)
			assert.Equal(t, modifiedResult, allMsgs[1].Content,
				"MessageFuture should receive the middleware-modified tool result")
			assert.NotEqual(t, originalResult, allMsgs[1].Content,
				"MessageFuture should NOT receive the original tool result")

			assert.Equal(t, "final response", allMsgs[2].Content)
		}
	})

	t.Run("Stream", func(t *testing.T) {
		ctx := context.Background()
		testTool := &simpleToolForMiddlewareTest{name: "test_tool_stream", result: originalResult}

		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		info, err := testTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"input": "test"}`,
						},
					},
				}),
			}), nil).
			Times(1)
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("final response", nil),
			}), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools:               []tool.BaseTool{testTool},
				ToolCallMiddlewares: []compose.ToolMiddleware{resultModifyingMiddleware},
			},
			MaxStep: 3,
		})
		assert.NoError(t, err)

		response, err := a.Stream(ctx, []*schema.Message{
			schema.UserMessage("call the tool"),
		}, option)
		assert.NoError(t, err)

		var msgs []*schema.Message
		for {
			msg, err := response.Recv()
			if err != nil {
				break
			}
			msgs = append(msgs, msg)
		}
		finalMsg, err := schema.ConcatMessages(msgs)
		assert.NoError(t, err)
		assert.Equal(t, "final response", finalMsg.Content)

		iter := future.GetMessageStreams()

		var allMsgs []*schema.Message
		for {
			msgStream, hasNext, err := iter.Next()
			if err != nil || !hasNext {
				break
			}
			var streamMsgs []*schema.Message
			for {
				msg, err := msgStream.Recv()
				if err != nil {
					break
				}
				streamMsgs = append(streamMsgs, msg)
			}
			if len(streamMsgs) > 0 {
				concated, err := schema.ConcatMessages(streamMsgs)
				if err == nil {
					allMsgs = append(allMsgs, concated)
				}
			}
		}

		assert.GreaterOrEqual(t, len(allMsgs), 3, "should have at least 3 messages")
		if len(allMsgs) >= 3 {
			assert.Equal(t, schema.Assistant, allMsgs[0].Role)
			assert.Equal(t, 1, len(allMsgs[0].ToolCalls))

			assert.Equal(t, schema.Tool, allMsgs[1].Role)
			assert.Equal(t, modifiedResult, allMsgs[1].Content,
				"MessageFuture should receive the middleware-modified tool result")
			assert.NotEqual(t, originalResult, allMsgs[1].Content,
				"MessageFuture should NOT receive the original tool result")

			assert.Equal(t, "final response", allMsgs[2].Content)
		}
	})
}

func TestWithMessageFuture_NestedGraph(t *testing.T) {
	ctx := context.Background()

	t.Run("agent in nested graph", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("final response", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		agentGraph, agentGraphOpts := a.ExportGraph()

		parentGraph := compose.NewGraph[[]*schema.Message, *schema.Message]()
		err = parentGraph.AddGraphNode("agent", agentGraph, agentGraphOpts...)
		assert.NoError(t, err)
		err = parentGraph.AddEdge(compose.START, "agent")
		assert.NoError(t, err)
		err = parentGraph.AddEdge("agent", compose.END)
		assert.NoError(t, err)

		runnable, err := parentGraph.Compile(ctx)
		assert.NoError(t, err)

		option, future := WithMessageFuture()

		response, err := runnable.Invoke(ctx, []*schema.Message{
			schema.UserMessage("use the greet tool"),
		}, agent.GetComposeOptions(option)...)
		assert.Nil(t, err)
		assert.Equal(t, "final response", response.Content)

		iter := future.GetMessages()

		msg1, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Assistant, msg1.Role)
		assert.Equal(t, 1, len(msg1.ToolCalls))

		msg2, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Tool, msg2.Role)

		msg3, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, "final response", msg3.Content)

		_, hasNext, err = iter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})

	t.Run("agent in deeply nested graph", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("final response", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		agentGraph, agentGraphOpts := a.ExportGraph()

		childGraph := compose.NewGraph[[]*schema.Message, *schema.Message]()
		err = childGraph.AddGraphNode("agent", agentGraph, agentGraphOpts...)
		assert.NoError(t, err)
		err = childGraph.AddEdge(compose.START, "agent")
		assert.NoError(t, err)
		err = childGraph.AddEdge("agent", compose.END)
		assert.NoError(t, err)

		parentGraph := compose.NewGraph[[]*schema.Message, *schema.Message]()
		err = parentGraph.AddGraphNode("child", childGraph)
		assert.NoError(t, err)
		err = parentGraph.AddEdge(compose.START, "child")
		assert.NoError(t, err)
		err = parentGraph.AddEdge("child", compose.END)
		assert.NoError(t, err)

		runnable, err := parentGraph.Compile(ctx)
		assert.NoError(t, err)

		option, future := WithMessageFuture()

		response, err := runnable.Invoke(ctx, []*schema.Message{
			schema.UserMessage("use the greet tool"),
		}, agent.GetComposeOptions(option)...)
		assert.Nil(t, err)
		assert.Equal(t, "final response", response.Content)

		iter := future.GetMessages()

		msg1, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Assistant, msg1.Role)
		assert.Equal(t, 1, len(msg1.ToolCalls))

		msg2, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Tool, msg2.Role)

		msg3, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, "final response", msg3.Content)

		_, hasNext, err = iter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})

	t.Run("agent in nested graph with streaming", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{
					schema.AssistantMessage("",
						[]schema.ToolCall{
							{
								ID: "tool-call-1",
								Function: schema.FunctionCall{
									Name:      info.Name,
									Arguments: `{"name": "stream user"}`,
								},
							},
						}),
				}), nil
			}).
			Times(1)
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{
					schema.AssistantMessage("streaming", nil),
					schema.AssistantMessage(" final", nil),
					schema.AssistantMessage(" response", nil),
				}), nil
			}).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		agentGraph, agentGraphOpts := a.ExportGraph()

		parentGraph := compose.NewGraph[[]*schema.Message, *schema.Message]()
		err = parentGraph.AddGraphNode("agent", agentGraph, agentGraphOpts...)
		assert.NoError(t, err)
		err = parentGraph.AddEdge(compose.START, "agent")
		assert.NoError(t, err)
		err = parentGraph.AddEdge("agent", compose.END)
		assert.NoError(t, err)

		runnable, err := parentGraph.Compile(ctx)
		assert.NoError(t, err)

		option, future := WithMessageFuture()

		streamReader, err := runnable.Stream(ctx, []*schema.Message{
			schema.UserMessage("use the greet tool"),
		}, agent.GetComposeOptions(option)...)
		assert.Nil(t, err)

		var finalContent string
		for {
			chunk, err := streamReader.Recv()
			if err == io.EOF {
				break
			}
			assert.Nil(t, err)
			finalContent += chunk.Content
		}
		assert.Contains(t, finalContent, "final")

		siter := future.GetMessageStreams()

		stream1, hasNext, err := siter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		msg1, err := concatStreamMsg(stream1)
		assert.Nil(t, err)
		assert.Equal(t, schema.Assistant, msg1.Role)
		assert.Equal(t, 1, len(msg1.ToolCalls))

		stream2, hasNext, err := siter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		msg2, err := concatStreamMsg(stream2)
		assert.Nil(t, err)
		assert.Equal(t, schema.Tool, msg2.Role)

		stream3, hasNext, err := siter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		msg3, err := concatStreamMsg(stream3)
		assert.Nil(t, err)
		assert.Contains(t, msg3.Content, "final")

		_, hasNext, err = siter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})

	t.Run("agent with multiple tool calls in nested graph", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "user1"}`,
						},
					},
					{
						ID: "tool-call-2",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "user2"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("greeted both users", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		agentGraph, agentGraphOpts := a.ExportGraph()

		parentGraph := compose.NewGraph[[]*schema.Message, *schema.Message]()
		err = parentGraph.AddGraphNode("agent", agentGraph, agentGraphOpts...)
		assert.NoError(t, err)
		err = parentGraph.AddEdge(compose.START, "agent")
		assert.NoError(t, err)
		err = parentGraph.AddEdge("agent", compose.END)
		assert.NoError(t, err)

		runnable, err := parentGraph.Compile(ctx)
		assert.NoError(t, err)

		option, future := WithMessageFuture()

		response, err := runnable.Invoke(ctx, []*schema.Message{
			schema.UserMessage("greet multiple users"),
		}, agent.GetComposeOptions(option)...)
		assert.Nil(t, err)
		assert.Equal(t, "greeted both users", response.Content)

		iter := future.GetMessages()

		msg1, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Assistant, msg1.Role)
		assert.Equal(t, 2, len(msg1.ToolCalls))

		msg2, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Tool, msg2.Role)

		msg3, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Tool, msg3.Role)

		msg4, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, "greeted both users", msg4.Content)

		_, hasNext, err = iter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})

	t.Run("direct agent invoke vs nested graph - same behavior", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		fakeTool := &fakeToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		cm1 := mockModel.NewMockToolCallingChatModel(ctrl)
		cm1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test"}`,
						},
					},
				}), nil).
			Times(1)
		cm1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("done", nil), nil).
			Times(1)
		cm1.EXPECT().WithTools(gomock.Any()).Return(cm1, nil).AnyTimes()

		a1, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm1,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		option1, future1 := WithMessageFuture()
		_, err = a1.Generate(ctx, []*schema.Message{
			schema.UserMessage("test direct"),
		}, option1)
		assert.Nil(t, err)

		directMsgs := collectAllMessages(future1.GetMessages())

		cm2 := mockModel.NewMockToolCallingChatModel(ctrl)
		cm2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test"}`,
						},
					},
				}), nil).
			Times(1)
		cm2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("done", nil), nil).
			Times(1)
		cm2.EXPECT().WithTools(gomock.Any()).Return(cm2, nil).AnyTimes()

		a2, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm2,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		agentGraph, agentGraphOpts := a2.ExportGraph()

		parentGraph := compose.NewGraph[[]*schema.Message, *schema.Message]()
		err = parentGraph.AddGraphNode("agent", agentGraph, agentGraphOpts...)
		assert.NoError(t, err)
		err = parentGraph.AddEdge(compose.START, "agent")
		assert.NoError(t, err)
		err = parentGraph.AddEdge("agent", compose.END)
		assert.NoError(t, err)

		runnable, err := parentGraph.Compile(ctx)
		assert.NoError(t, err)

		option2, future2 := WithMessageFuture()
		_, err = runnable.Invoke(ctx, []*schema.Message{
			schema.UserMessage("test nested"),
		}, agent.GetComposeOptions(option2)...)
		assert.Nil(t, err)

		nestedMsgs := collectAllMessages(future2.GetMessages())

		assert.Equal(t, len(directMsgs), len(nestedMsgs), "should have same number of messages")
		for i := range directMsgs {
			assert.Equal(t, directMsgs[i].Role, nestedMsgs[i].Role, "message %d role should match", i)
		}
	})

	t.Run("multiple react agents in graph - agent calls tool that invokes another agent", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		cm1 := mockModel.NewMockToolCallingChatModel(ctrl)
		cm2 := mockModel.NewMockToolCallingChatModel(ctrl)

		cm2.EXPECT().WithTools(gomock.Any()).Return(cm2, nil).AnyTimes()
		cm2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("inner agent response", nil), nil).
			Times(1)

		innerAgent, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm2,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&fakeToolGreetForTest{}},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		innerAgentTool := &agentAsTool{
			agent: innerAgent,
			name:  "inner_agent",
			desc:  "An inner agent that can greet users",
		}

		cm1.EXPECT().WithTools(gomock.Any()).Return(cm1, nil).AnyTimes()
		cm1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      "inner_agent",
							Arguments: `{"query": "hello"}`,
						},
					},
				}), nil).
			Times(1)
		cm1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("outer agent final response", nil), nil).
			Times(1)

		outerAgent, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm1,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{innerAgentTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		option, future := WithMessageFuture()

		response, err := outerAgent.Generate(ctx, []*schema.Message{
			schema.UserMessage("use the inner agent"),
		}, option)
		assert.Nil(t, err)
		assert.Equal(t, "outer agent final response", response.Content)

		allMsgs := collectAllMessages(future.GetMessages())

		assert.GreaterOrEqual(t, len(allMsgs), 3, "should have at least 3 messages")

		hasOuterToolCall := false
		hasInnerResponse := false
		hasOuterFinalResponse := false
		for _, msg := range allMsgs {
			if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
				hasOuterToolCall = true
			}
			if msg.Content == "inner agent response" {
				hasInnerResponse = true
			}
			if msg.Content == "outer agent final response" {
				hasOuterFinalResponse = true
			}
		}
		assert.True(t, hasOuterToolCall, "should have outer agent tool call")
		assert.True(t, hasInnerResponse, "should have inner agent response")
		assert.True(t, hasOuterFinalResponse, "should have outer agent final response")
	})

	t.Run("two sequential react agents in same graph", func(t *testing.T) {
		ctrl := gomock.NewController(t)

		cm1 := mockModel.NewMockToolCallingChatModel(ctrl)
		cm1.EXPECT().WithTools(gomock.Any()).Return(cm1, nil).AnyTimes()
		cm1.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("agent1 response", nil), nil).
			Times(1)

		cm2 := mockModel.NewMockToolCallingChatModel(ctrl)
		cm2.EXPECT().WithTools(gomock.Any()).Return(cm2, nil).AnyTimes()
		cm2.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("agent2 response", nil), nil).
			Times(1)

		agent1, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm1,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&fakeToolGreetForTest{}},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		agent2, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm2,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&fakeToolGreetForTest{}},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		agent1Graph, agent1Opts := agent1.ExportGraph()
		agent2Graph, agent2Opts := agent2.ExportGraph()

		parentGraph := compose.NewGraph[[]*schema.Message, *schema.Message]()
		err = parentGraph.AddGraphNode("agent1", agent1Graph, agent1Opts...)
		assert.NoError(t, err)

		err = parentGraph.AddLambdaNode("transform", compose.InvokableLambda(func(ctx context.Context, msg *schema.Message) ([]*schema.Message, error) {
			return []*schema.Message{schema.UserMessage("agent2 input: " + msg.Content)}, nil
		}))
		assert.NoError(t, err)

		err = parentGraph.AddGraphNode("agent2", agent2Graph, agent2Opts...)
		assert.NoError(t, err)

		err = parentGraph.AddEdge(compose.START, "agent1")
		assert.NoError(t, err)
		err = parentGraph.AddEdge("agent1", "transform")
		assert.NoError(t, err)
		err = parentGraph.AddEdge("transform", "agent2")
		assert.NoError(t, err)
		err = parentGraph.AddEdge("agent2", compose.END)
		assert.NoError(t, err)

		runnable, err := parentGraph.Compile(ctx)
		assert.NoError(t, err)

		option, future := WithMessageFuture()

		response, err := runnable.Invoke(ctx, []*schema.Message{
			schema.UserMessage("hello"),
		}, agent.GetComposeOptions(option)...)
		assert.Nil(t, err)
		assert.Equal(t, "agent2 response", response.Content)

		allMsgs := collectAllMessages(future.GetMessages())
		assert.Equal(t, 2, len(allMsgs))

		responseContents := make(map[string]bool)
		for _, msg := range allMsgs {
			responseContents[msg.Content] = true
		}
		assert.True(t, responseContents["agent1 response"])
		assert.True(t, responseContents["agent2 response"])
	})
}

type agentAsTool struct {
	agent *Agent
	name  string
	desc  string
}

func (a *agentAsTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: a.name,
		Desc: a.desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type: "string",
				Desc: "The query to send to the inner agent",
			},
		}),
	}, nil
}

func (a *agentAsTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	response, err := a.agent.Generate(ctx, []*schema.Message{
		schema.UserMessage(argumentsInJSON),
	})
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

func collectAllMessages(iter *Iterator[*schema.Message]) []*schema.Message {
	var msgs []*schema.Message
	for {
		msg, hasNext, err := iter.Next()
		if err != nil || !hasNext {
			break
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

func concatStreamMsg(sr *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	var result *schema.Message
	for {
		chunk, err := sr.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if result == nil {
			result = chunk
		} else {
			result.Content += chunk.Content
			if len(chunk.ToolCalls) > 0 {
				result.ToolCalls = append(result.ToolCalls, chunk.ToolCalls...)
			}
		}
	}
	return result, nil
}
