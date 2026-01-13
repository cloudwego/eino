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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func newTestToolWrapperFn(beforeFn, afterFn func()) func(context.Context, tool.BaseTool) (tool.BaseTool, error) {
	return func(_ context.Context, t tool.BaseTool) (tool.BaseTool, error) {
		return &testWrappedTool{
			inner:    t,
			beforeFn: beforeFn,
			afterFn:  afterFn,
		}, nil
	}
}

func newTestStreamToolWrapperFn(streamBeforeFn, streamAfterFn func()) func(context.Context, tool.BaseTool) (tool.BaseTool, error) {
	return func(_ context.Context, t tool.BaseTool) (tool.BaseTool, error) {
		return &testWrappedStreamTool{
			inner:          t,
			streamBeforeFn: streamBeforeFn,
			streamAfterFn:  streamAfterFn,
		}, nil
	}
}

func newResultModifyingToolWrapperFn(modifyFn func(string) string) func(context.Context, tool.BaseTool) (tool.BaseTool, error) {
	return func(_ context.Context, t tool.BaseTool) (tool.BaseTool, error) {
		return &resultModifyingTool{
			inner:    t,
			modifyFn: modifyFn,
		}, nil
	}
}

type resultModifyingTool struct {
	inner    tool.BaseTool
	modifyFn func(string) string
}

func (t *resultModifyingTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *resultModifyingTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	inv, ok := t.inner.(tool.InvokableTool)
	if !ok {
		return "", nil
	}
	result, err := inv.InvokableRun(ctx, args, opts...)
	if err != nil {
		return "", err
	}
	return t.modifyFn(result), nil
}

type testWrappedTool struct {
	inner    tool.BaseTool
	beforeFn func()
	afterFn  func()
}

func (t *testWrappedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *testWrappedTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	if t.beforeFn != nil {
		t.beforeFn()
	}
	inv, ok := t.inner.(tool.InvokableTool)
	if !ok {
		return "", nil
	}
	result, err := inv.InvokableRun(ctx, args, opts...)
	if t.afterFn != nil {
		t.afterFn()
	}
	return result, err
}

type testWrappedStreamTool struct {
	inner          tool.BaseTool
	streamBeforeFn func()
	streamAfterFn  func()
}

func (t *testWrappedStreamTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *testWrappedStreamTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	inv, ok := t.inner.(tool.InvokableTool)
	if !ok {
		return "", nil
	}
	return inv.InvokableRun(ctx, args, opts...)
}

func (t *testWrappedStreamTool) StreamableRun(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	if t.streamBeforeFn != nil {
		t.streamBeforeFn()
	}
	st, ok := t.inner.(tool.StreamableTool)
	if !ok {
		return nil, nil
	}
	result, err := st.StreamableRun(ctx, args, opts...)
	if t.streamAfterFn != nil {
		t.streamAfterFn()
	}
	return result, err
}

func TestHandlerExecutionOrder(t *testing.T) {
	t.Run("MultipleInstructionHandlersPipeline", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var capturedInstruction string
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				if len(msgs) > 0 && msgs[0].Role == schema.System {
					capturedInstruction = msgs[0].Content
				}
				return schema.AssistantMessage("response", nil), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Instruction: "Base instruction.",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithInstruction("Handler 1 addition."),
				WithInstruction("Handler 2 addition."),
				WithInstructionFunc(func(ctx context.Context, instruction string) (context.Context, string, error) {
					return ctx, instruction + "\nHandler 3 dynamic.", nil
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Contains(t, capturedInstruction, "Base instruction.")
		assert.Contains(t, capturedInstruction, "Handler 1 addition.")
		assert.Contains(t, capturedInstruction, "Handler 2 addition.")
		assert.Contains(t, capturedInstruction, "Handler 3 dynamic.")
	})

	t.Run("MiddlewaresBeforeHandlers", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var capturedInstruction string
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				if len(msgs) > 0 && msgs[0].Role == schema.System {
					capturedInstruction = msgs[0].Content
				}
				return schema.AssistantMessage("response", nil), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Instruction: "Base.",
			Model:       cm,
			Middlewares: []AgentMiddleware{
				{AdditionalInstruction: "Middleware instruction."},
			},
			Handlers: []HandlerMiddleware{
				WithInstruction("Handler instruction."),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		middlewareIdx := len(capturedInstruction) - len("Middleware instruction.") - len("\nHandler instruction.")
		handlerIdx := len(capturedInstruction) - len("Handler instruction.")
		assert.True(t, middlewareIdx < handlerIdx, "Middleware should be applied before Handler")
	})
}

func TestToolsHandlerCombinations(t *testing.T) {
	t.Run("MultipleToolsHandlersAppend", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		tool1 := &fakeToolForTest{tarCount: 1}
		tool2 := &fakeToolForTest{tarCount: 2}

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		var capturedToolCount int
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				options := model.GetCommonOptions(&model.Options{}, opts...)
				capturedToolCount = len(options.Tools)
				return schema.AssistantMessage("response", nil), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{tool1},
				},
			},
			Handlers: []HandlerMiddleware{
				WithTools(tool2),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, 2, capturedToolCount)
	})

	t.Run("ToolsFuncCanRemoveTools", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		tool1 := &namedTool{name: "tool1"}
		tool2 := &namedTool{name: "tool2"}
		tool3 := &namedTool{name: "tool3"}

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		var capturedToolNames []string
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				options := model.GetCommonOptions(&model.Options{}, opts...)
				for _, t := range options.Tools {
					capturedToolNames = append(capturedToolNames, t.Name)
				}
				return schema.AssistantMessage("response", nil), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{tool1, tool2, tool3},
				},
			},
			Handlers: []HandlerMiddleware{
				WithToolsFunc(func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]struct{}) (context.Context, []tool.BaseTool, map[string]struct{}, error) {
					filtered := make([]tool.BaseTool, 0)
					for _, t := range tools {
						info, _ := t.Info(ctx)
						if info.Name != "tool2" {
							filtered = append(filtered, t)
						}
					}
					return ctx, filtered, returnDirectly, nil
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Contains(t, capturedToolNames, "tool1")
		assert.NotContains(t, capturedToolNames, "tool2")
		assert.Contains(t, capturedToolNames, "tool3")
	})

	t.Run("ReturnDirectlyModification", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		tool1 := &namedTool{name: "tool1"}

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Using tool", []schema.ToolCall{
				{ID: "call1", Function: schema.FunctionCall{Name: "tool1", Arguments: "{}"}},
			}), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{tool1},
				},
			},
			Handlers: []HandlerMiddleware{
				WithToolsFunc(func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]struct{}) (context.Context, []tool.BaseTool, map[string]struct{}, error) {
					for _, t := range tools {
						info, _ := t.Info(ctx)
						if info.Name == "tool1" {
							returnDirectly[info.Name] = struct{}{}
						}
					}
					return ctx, tools, returnDirectly, nil
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		eventCount := 0
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			eventCount++
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Message != nil &&
				event.Output.MessageOutput.Message.Role == schema.Tool {
				assert.Equal(t, "tool1 result", event.Output.MessageOutput.Message.Content)
			}
		}
		assert.Equal(t, 2, eventCount)
	})

	t.Run("DynamicToolCanBeCalledByModel", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		dynamicToolCalled := false
		dynamicTool := &callableTool{
			name: "dynamic_tool",
			invokeFn: func() {
				dynamicToolCalled = true
			},
		}
		info, _ := dynamicTool.Info(ctx)

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Using dynamic tool", []schema.ToolCall{
				{ID: "call1", Function: schema.FunctionCall{Name: info.Name, Arguments: "{}"}},
			}), nil).Times(1)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("done", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithTools(dynamicTool),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.True(t, dynamicToolCalled, "Dynamic tool should have been called")
	})
}

func TestMessageRewriteHandlers(t *testing.T) {
	t.Run("BeforeModelRewriteHistoryPipeline", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var capturedMsgCount int
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...interface{}) (*schema.Message, error) {
				capturedMsgCount = len(msgs)
				return schema.AssistantMessage("response", nil), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Instruction: "instruction",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithBeforeModelRewriteHistory(func(ctx context.Context, messages []Message) (context.Context, []Message, error) {
					return ctx, append(messages, schema.UserMessage("injected1")), nil
				}),
				WithBeforeModelRewriteHistory(func(ctx context.Context, messages []Message) (context.Context, []Message, error) {
					return ctx, append(messages, schema.UserMessage("injected2")), nil
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("original")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, 4, capturedMsgCount)
	})

	t.Run("AfterModelRewriteHistory", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		afterCalled := false
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithAfterModelRewriteHistory(func(ctx context.Context, messages []Message) (context.Context, []Message, error) {
					afterCalled = true
					assert.True(t, len(messages) > 0)
					lastMsg := messages[len(messages)-1]
					assert.Equal(t, schema.Assistant, lastMsg.Role)
					return ctx, messages, nil
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.True(t, afterCalled)
	})
}

func TestToolCallWrapperHandlers(t *testing.T) {
	t.Run("MultipleToolWrappersPipeline", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		testTool := &namedTool{name: "test_tool"}
		info, _ := testTool.Info(ctx)

		var callOrder []string
		var mu sync.Mutex

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Using tool", []schema.ToolCall{
				{ID: "call1", Function: schema.FunctionCall{Name: info.Name, Arguments: "{}"}},
			}), nil).Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("done", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{testTool},
				},
			},
			Handlers: []HandlerMiddleware{
				WithToolWrapper(newTestToolWrapperFn(
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-before")
						mu.Unlock()
					},
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-after")
						mu.Unlock()
					},
				)),
				WithToolWrapper(newTestToolWrapperFn(
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-before")
						mu.Unlock()
					},
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-after")
						mu.Unlock()
					},
				)),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, []string{"wrapper1-before", "wrapper2-before", "wrapper2-after", "wrapper1-after"}, callOrder)
	})

	t.Run("StreamingToolWrappersPipeline", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		testTool := &streamingNamedTool{name: "streaming_tool"}
		info, _ := testTool.Info(ctx)

		var callOrder []string
		var mu sync.Mutex

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("Using tool", []schema.ToolCall{
					{ID: "call1", Function: schema.FunctionCall{Name: info.Name, Arguments: "{}"}},
				}),
			}), nil).Times(1)
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("done", nil),
			}), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{testTool},
				},
			},
			Handlers: []HandlerMiddleware{
				WithToolWrapper(newTestStreamToolWrapperFn(
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-stream-before")
						mu.Unlock()
					},
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-stream-after")
						mu.Unlock()
					},
				)),
				WithToolWrapper(newTestStreamToolWrapperFn(
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-stream-before")
						mu.Unlock()
					},
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-stream-after")
						mu.Unlock()
					},
				)),
			},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true, CheckPointStore: newBridgeStore()})
		iter := r.Run(ctx, []Message{schema.UserMessage("test")})

		var hasStreamingToolResult bool
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.IsStreaming &&
				event.Output.MessageOutput.Role == schema.Tool {
				hasStreamingToolResult = true
				for {
					_, err := event.Output.MessageOutput.MessageStream.Recv()
					if err != nil {
						break
					}
				}
			}
		}

		assert.True(t, hasStreamingToolResult, "Should have streaming tool result")
		assert.Equal(t, []string{"wrapper1-stream-before", "wrapper2-stream-before", "wrapper2-stream-after", "wrapper1-stream-after"}, callOrder,
			"Streaming wrappers should be called in correct order")
	})

	t.Run("ToolWrapperCanModifyResult", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		testTool := &namedTool{name: "test_tool"}
		info, _ := testTool.Info(ctx)

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Using tool", []schema.ToolCall{
				{ID: "call1", Function: schema.FunctionCall{Name: info.Name, Arguments: "{}"}},
			}), nil).Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("done", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{testTool},
				},
			},
			Handlers: []HandlerMiddleware{
				WithToolWrapper(newResultModifyingToolWrapperFn(func(result string) string {
					return "modified: " + result
				})),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Message != nil &&
				event.Output.MessageOutput.Message.Role == schema.Tool {
				assert.Equal(t, "modified: test_tool result", event.Output.MessageOutput.Message.Content)
			}
		}
	})
}

func TestContextPropagation(t *testing.T) {
	t.Run("ContextPassedThroughBeforeModelHandlers", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		type ctxKey string
		const key1 ctxKey = "key1"
		const key2 ctxKey = "key2"

		var handler2ReceivedValue1 interface{}

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithBeforeModelRewriteHistory(func(ctx context.Context, messages []Message) (context.Context, []Message, error) {
					return context.WithValue(ctx, key1, "value1"), messages, nil
				}),
				WithBeforeModelRewriteHistory(func(ctx context.Context, messages []Message) (context.Context, []Message, error) {
					handler2ReceivedValue1 = ctx.Value(key1)
					return context.WithValue(ctx, key2, "value2"), messages, nil
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, "value1", handler2ReceivedValue1, "Handler 2 should receive context value set by Handler 1")
	})

	t.Run("BeforeAgentContextPropagation", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		type ctxKey string
		const key1 ctxKey = "key1"

		var handler2ReceivedValue interface{}

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithBeforeAgent(func(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
					return context.WithValue(ctx, key1, "value1"), runCtx, nil
				}),
				WithBeforeAgent(func(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
					handler2ReceivedValue = ctx.Value(key1)
					return ctx, runCtx, nil
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, "value1", handler2ReceivedValue, "Handler 2 should receive context value set by Handler 1 during BeforeAgent")
	})
}

func TestCustomHandler(t *testing.T) {
	t.Run("CustomHandlerWithState", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		customHandler := &countingHandler{}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers:    []HandlerMiddleware{customHandler},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, 1, customHandler.beforeAgentCount)
		assert.Equal(t, 1, customHandler.beforeModelCount)
		assert.Equal(t, 1, customHandler.afterModelCount)
	})
}

func TestHandlerErrorHandling(t *testing.T) {
	t.Run("BeforeAgentErrorStopsRun", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithBeforeAgent(func(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
					return ctx, runCtx, assert.AnError
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{
			Messages: []*schema.Message{schema.UserMessage("test")},
		})

		var gotErr error
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				gotErr = event.Err
			}
		}

		assert.Error(t, gotErr)
		assert.Contains(t, gotErr.Error(), "BeforeAgent failed")
	})
}

type namedTool struct {
	name string
}

func (t *namedTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: t.name + " description"}, nil
}

func (t *namedTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return t.name + " result", nil
}

type streamingNamedTool struct {
	name string
}

func (t *streamingNamedTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: t.name + " description"}, nil
}

func (t *streamingNamedTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return t.name + " result", nil
}

func (t *streamingNamedTool) StreamableRun(_ context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	return schema.StreamReaderFromArray([]string{t.name + " stream result"}), nil
}

type callableTool struct {
	name     string
	invokeFn func()
}

func (t *callableTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: t.name + " description"}, nil
}

func (t *callableTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	if t.invokeFn != nil {
		t.invokeFn()
	}
	return t.name + " result", nil
}

type countingHandler struct {
	*BaseHandlerMiddleware
	beforeAgentCount int
	beforeModelCount int
	afterModelCount  int
	mu               sync.Mutex
}

func (h *countingHandler) BeforeAgent(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
	h.mu.Lock()
	h.beforeAgentCount++
	h.mu.Unlock()
	return ctx, runCtx, nil
}

func (h *countingHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	h.mu.Lock()
	h.beforeModelCount++
	h.mu.Unlock()
	return ctx, messages, nil
}

func (h *countingHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	h.mu.Lock()
	h.afterModelCount++
	h.mu.Unlock()
	return ctx, messages, nil
}

func newTestModelWrapperFn(beforeFn, afterFn func()) func(context.Context, model.BaseChatModel) (model.BaseChatModel, error) {
	return func(_ context.Context, m model.BaseChatModel) (model.BaseChatModel, error) {
		return &testWrappedModel{
			inner:    m,
			beforeFn: beforeFn,
			afterFn:  afterFn,
		}, nil
	}
}

type testWrappedModel struct {
	inner    model.BaseChatModel
	beforeFn func()
	afterFn  func()
}

func (m *testWrappedModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.beforeFn != nil {
		m.beforeFn()
	}
	result, err := m.inner.Generate(ctx, input, opts...)
	if m.afterFn != nil {
		m.afterFn()
	}
	return result, err
}

func (m *testWrappedModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.beforeFn != nil {
		m.beforeFn()
	}
	result, err := m.inner.Stream(ctx, input, opts...)
	if m.afterFn != nil {
		m.afterFn()
	}
	return result, err
}

func TestModelWrapperHandlers(t *testing.T) {
	t.Run("MultipleModelWrappersPipeline", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callOrder []string
		var mu sync.Mutex

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithModelWrapper(newTestModelWrapperFn(
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-before")
						mu.Unlock()
					},
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-after")
						mu.Unlock()
					},
				)),
				WithModelWrapper(newTestModelWrapperFn(
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-before")
						mu.Unlock()
					},
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-after")
						mu.Unlock()
					},
				)),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, []string{"wrapper1-before", "wrapper2-before", "wrapper2-after", "wrapper1-after"}, callOrder)
	})

	t.Run("ModelWrapperBeforeAfterCallOrder", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var callOrder []string
		var mu sync.Mutex

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				mu.Lock()
				callOrder = append(callOrder, "model-generate")
				mu.Unlock()
				return schema.AssistantMessage("original response", nil), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithModelWrapper(newTestModelWrapperFn(
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper-before")
						mu.Unlock()
					},
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper-after")
						mu.Unlock()
					},
				)),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, []string{"wrapper-before", "model-generate", "wrapper-after"}, callOrder)
	})

	t.Run("ModelWrapperWithTools", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		testTool := &namedTool{name: "test_tool"}
		info, _ := testTool.Info(ctx)

		var callOrder []string
		var mu sync.Mutex

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				mu.Lock()
				callOrder = append(callOrder, "model-call")
				mu.Unlock()
				return schema.AssistantMessage("Using tool", []schema.ToolCall{
					{ID: "call1", Function: schema.FunctionCall{Name: info.Name, Arguments: "{}"}},
				}), nil
			}).Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				mu.Lock()
				callOrder = append(callOrder, "model-call")
				mu.Unlock()
				return schema.AssistantMessage("done", nil), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{testTool},
				},
			},
			Handlers: []HandlerMiddleware{
				WithModelWrapper(newTestModelWrapperFn(
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper-before")
						mu.Unlock()
					},
					func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper-after")
						mu.Unlock()
					},
				)),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.Equal(t, []string{
			"wrapper-before", "model-call", "wrapper-after",
			"wrapper-before", "model-call", "wrapper-after",
		}, callOrder)
	})
}

type simpleChatModelWithoutCallbacks struct {
	generateFn func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error)
	streamFn   func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

func (m *simpleChatModelWithoutCallbacks) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, input, opts...)
	}
	return schema.AssistantMessage("default response", nil), nil
}

func (m *simpleChatModelWithoutCallbacks) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.streamFn != nil {
		return m.streamFn(ctx, input, opts...)
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("default response", nil)}), nil
}

func (m *simpleChatModelWithoutCallbacks) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func newInputModifyingWrapperFn(inputPrefix string) func(context.Context, model.BaseChatModel) (model.BaseChatModel, error) {
	return func(_ context.Context, m model.BaseChatModel) (model.BaseChatModel, error) {
		return &inputOutputModifyingModel{
			inner:       m,
			inputPrefix: inputPrefix,
		}, nil
	}
}

type inputOutputModifyingModel struct {
	inner       model.BaseChatModel
	inputPrefix string
}

func (m *inputOutputModifyingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	modifiedMessages := make([]*schema.Message, len(input))
	for i, msg := range input {
		if msg.Role == schema.User {
			modifiedMessages[i] = schema.UserMessage(m.inputPrefix + msg.Content)
		} else {
			modifiedMessages[i] = msg
		}
	}
	return m.inner.Generate(ctx, modifiedMessages, opts...)
}

func (m *inputOutputModifyingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	modifiedMessages := make([]*schema.Message, len(input))
	for i, msg := range input {
		if msg.Role == schema.User {
			modifiedMessages[i] = schema.UserMessage(m.inputPrefix + msg.Content)
		} else {
			modifiedMessages[i] = msg
		}
	}
	return m.inner.Stream(ctx, modifiedMessages, opts...)
}

func TestModelWrapper_InputModification(t *testing.T) {
	t.Run("ModelWrapperModifiesInput_Generate", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var modelReceivedInput []*schema.Message
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				modelReceivedInput = input
				return schema.AssistantMessage("original response", nil), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithModelWrapper(newInputModifyingWrapperFn("[WRAPPER]")),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test input")}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		assert.NotNil(t, modelReceivedInput)
		assert.True(t, len(modelReceivedInput) > 0)
		found := false
		for _, msg := range modelReceivedInput {
			if msg.Content == "[WRAPPER]test input" {
				found = true
				break
			}
		}
		assert.True(t, found, "Model should receive wrapper-modified input")
	})

	t.Run("ModelWrapperModifiesInput_Stream", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var modelReceivedInput []*schema.Message
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				modelReceivedInput = input
				return schema.StreamReaderFromArray([]*schema.Message{
					schema.AssistantMessage("chunk1", nil),
					schema.AssistantMessage("chunk2", nil),
				}), nil
			}).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []HandlerMiddleware{
				WithModelWrapper(newInputModifyingWrapperFn("[WRAPPER]")),
			},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true, CheckPointStore: newBridgeStore()})
		iter := r.Run(ctx, []Message{schema.UserMessage("test input")})

		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.IsStreaming &&
				event.Output.MessageOutput.Role == schema.Assistant {
				for {
					_, err := event.Output.MessageOutput.MessageStream.Recv()
					if err != nil {
						break
					}
				}
			}
		}

		assert.NotNil(t, modelReceivedInput)
		assert.True(t, len(modelReceivedInput) > 0)
		found := false
		for _, msg := range modelReceivedInput {
			if msg.Content == "[WRAPPER]test input" {
				found = true
				break
			}
		}
		assert.True(t, found, "Model should receive wrapper-modified input")
	})
}
