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

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

type testToolCallWrapper struct {
	BaseToolCallWrapper
	name             string
	beforeFn         func()
	afterFn          func()
	streamBeforeFn   func()
	streamAfterFn    func()
	modifyResultFn   func(string) string
}

func (h *testToolCallWrapper) WrapToolInvoke(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*ToolResult, error)) (*ToolResult, error) {
	if h.beforeFn != nil {
		h.beforeFn()
	}
	result, err := next(ctx, call)
	if h.afterFn != nil {
		h.afterFn()
	}
	if err == nil && h.modifyResultFn != nil {
		result.Result = h.modifyResultFn(result.Result)
	}
	return result, err
}

func (h *testToolCallWrapper) WrapToolStream(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*StreamToolResult, error)) (*StreamToolResult, error) {
	if h.streamBeforeFn != nil {
		h.streamBeforeFn()
	}
	result, err := next(ctx, call)
	if h.streamAfterFn != nil {
		h.streamAfterFn()
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
				WithToolCallWrapper(&testToolCallWrapper{
					name: "wrapper1",
					beforeFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-before")
						mu.Unlock()
					},
					afterFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-after")
						mu.Unlock()
					},
				}),
				WithToolCallWrapper(&testToolCallWrapper{
					name: "wrapper2",
					beforeFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-before")
						mu.Unlock()
					},
					afterFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-after")
						mu.Unlock()
					},
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
			Handlers: []AgentHandler{
				WithToolCallWrapper(&testToolCallWrapper{
					name: "wrapper1",
					streamBeforeFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-stream-before")
						mu.Unlock()
					},
					streamAfterFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-stream-after")
						mu.Unlock()
					},
				}),
				WithToolCallWrapper(&testToolCallWrapper{
					name: "wrapper2",
					streamBeforeFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-stream-before")
						mu.Unlock()
					},
					streamAfterFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-stream-after")
						mu.Unlock()
					},
				}),
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
			Handlers: []AgentHandler{
				WithToolCallWrapper(&testToolCallWrapper{
					name: "modifier",
					modifyResultFn: func(result string) string {
						return "modified: " + result
					},
				}),
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
			Handlers: []AgentHandler{
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
			Handlers: []AgentHandler{
				WithBeforeAgent(func(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
					return context.WithValue(ctx, key1, "value1"), runCtx, nil
				}),
				WithBeforeAgent(func(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
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
			Handlers:    []AgentHandler{customHandler},
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
			Handlers: []AgentHandler{
				WithBeforeAgent(func(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
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
	BaseAgentHandler
	beforeAgentCount int
	beforeModelCount int
	afterModelCount  int
	mu               sync.Mutex
}

func (h *countingHandler) BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
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

type testModelCallWrapper struct {
	BaseModelCallWrapper
	name           string
	beforeFn       func()
	afterFn        func()
	modifyResultFn func(*schema.Message) *schema.Message
}

func (h *testModelCallWrapper) WrapModelGenerate(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*ModelResult, error)) (*ModelResult, error) {
	if h.beforeFn != nil {
		h.beforeFn()
	}
	result, err := next(ctx, call)
	if h.afterFn != nil {
		h.afterFn()
	}
	if err == nil && h.modifyResultFn != nil {
		result.Message = h.modifyResultFn(result.Message)
	}
	return result, err
}

func (h *testModelCallWrapper) WrapModelStream(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*StreamModelResult, error)) (*StreamModelResult, error) {
	if h.beforeFn != nil {
		h.beforeFn()
	}
	result, err := next(ctx, call)
	if h.afterFn != nil {
		h.afterFn()
	}
	return result, err
}

func TestModelCallWrapperHandlers(t *testing.T) {
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
			Handlers: []AgentHandler{
				WithModelCallWrapper(&testModelCallWrapper{
					name: "wrapper1",
					beforeFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-before")
						mu.Unlock()
					},
					afterFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper1-after")
						mu.Unlock()
					},
				}),
				WithModelCallWrapper(&testModelCallWrapper{
					name: "wrapper2",
					beforeFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-before")
						mu.Unlock()
					},
					afterFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper2-after")
						mu.Unlock()
					},
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

		assert.Equal(t, []string{"wrapper1-before", "wrapper2-before", "wrapper2-after", "wrapper1-after"}, callOrder)
	})

	t.Run("ModelWrapperCanModifyResult", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("original response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []AgentHandler{
				WithModelCallWrapper(&testModelCallWrapper{
					name: "modifier",
					modifyResultFn: func(msg *schema.Message) *schema.Message {
						return schema.AssistantMessage("modified: "+msg.Content, nil)
					},
				}),
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})
		var lastContent string
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Message != nil &&
				event.Output.MessageOutput.Message.Role == schema.Assistant {
				lastContent = event.Output.MessageOutput.Message.Content
			}
		}

		assert.Equal(t, "modified: original response", lastContent)
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
			Handlers: []AgentHandler{
				WithModelCallWrapper(&testModelCallWrapper{
					name: "wrapper",
					beforeFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper-before")
						mu.Unlock()
					},
					afterFn: func() {
						mu.Lock()
						callOrder = append(callOrder, "wrapper-after")
						mu.Unlock()
					},
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

type inputOutputModifyingWrapper struct {
	BaseModelCallWrapper
	inputPrefix  string
	outputSuffix string
}

func (w *inputOutputModifyingWrapper) WrapModelGenerate(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*ModelResult, error)) (*ModelResult, error) {
	modifiedMessages := make([]*schema.Message, len(call.Messages))
	for i, msg := range call.Messages {
		if msg.Role == schema.User {
			modifiedMessages[i] = schema.UserMessage(w.inputPrefix + msg.Content)
		} else {
			modifiedMessages[i] = msg
		}
	}
	call.Messages = modifiedMessages

	result, err := next(ctx, call)
	if err != nil {
		return nil, err
	}

	result.Message = schema.AssistantMessage(result.Message.Content+w.outputSuffix, nil)
	return result, nil
}

func (w *inputOutputModifyingWrapper) WrapModelStream(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*StreamModelResult, error)) (*StreamModelResult, error) {
	modifiedMessages := make([]*schema.Message, len(call.Messages))
	for i, msg := range call.Messages {
		if msg.Role == schema.User {
			modifiedMessages[i] = schema.UserMessage(w.inputPrefix + msg.Content)
		} else {
			modifiedMessages[i] = msg
		}
	}
	call.Messages = modifiedMessages

	result, err := next(ctx, call)
	if err != nil {
		return nil, err
	}

	modifiedStream := schema.StreamReaderWithConvert(result.Stream, func(msg *schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage(msg.Content+w.outputSuffix, nil), nil
	})
	return &StreamModelResult{Stream: modifiedStream}, nil
}

func TestWrappedChatModel_CallbackInjection(t *testing.T) {
	t.Run("DirectWrappedChatModel_Generate", func(t *testing.T) {
		var modelReceivedInput []*schema.Message

		cm := &simpleChatModelWithoutCallbacks{
			generateFn: func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				modelReceivedInput = input
				return schema.AssistantMessage("original response", nil), nil
			},
		}

		var callbackCapturedInput []*schema.Message
		var callbackCapturedOutput *schema.Message

		handler := callbacks.NewHandlerBuilder().
			OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
				if msgs, ok := input.([]*schema.Message); ok {
					callbackCapturedInput = msgs
				}
				return ctx
			}).
			OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
				if msg, ok := output.(*schema.Message); ok {
					callbackCapturedOutput = msg
				}
				return ctx
			}).
			Build()

		ctx := context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{}, handler)

		wrapper := &inputOutputModifyingWrapper{
			inputPrefix:  "[WRAPPER]",
			outputSuffix: "[MODIFIED]",
		}
		wrappedModel := newWrappedChatModel(cm, []ModelCallWrapper{wrapper})

		input := []*schema.Message{schema.UserMessage("test input")}
		result, err := wrappedModel.Generate(ctx, input)
		assert.NoError(t, err)

		assert.NotNil(t, modelReceivedInput)
		assert.Len(t, modelReceivedInput, 1)
		assert.Equal(t, "[WRAPPER]test input", modelReceivedInput[0].Content, "Model should receive wrapper-modified input")

		assert.NotNil(t, callbackCapturedInput)
		assert.Len(t, callbackCapturedInput, 1)
		assert.Equal(t, "[WRAPPER]test input", callbackCapturedInput[0].Content, "Callback should capture the same input that model receives (after wrapper modification)")

		assert.NotNil(t, callbackCapturedOutput)
		assert.Equal(t, "original response", callbackCapturedOutput.Content, "Callback should capture original model output (before wrapper modification)")

		assert.Equal(t, "original response[MODIFIED]", result.Content, "Final output should be wrapper-modified")
	})

	t.Run("DirectWrappedChatModel_Stream", func(t *testing.T) {
		var modelReceivedInput []*schema.Message

		cm := &simpleChatModelWithoutCallbacks{
			streamFn: func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				modelReceivedInput = input
				return schema.StreamReaderFromArray([]*schema.Message{
					schema.AssistantMessage("chunk1", nil),
					schema.AssistantMessage("chunk2", nil),
				}), nil
			},
		}

		var callbackCapturedInput []*schema.Message
		var callbackCapturedStreamChunks []*schema.Message
		var mu sync.Mutex

		handler := callbacks.NewHandlerBuilder().
			OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
				if msgs, ok := input.([]*schema.Message); ok {
					callbackCapturedInput = msgs
				}
				return ctx
			}).
			OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
				go func() {
					defer output.Close()
					for {
						chunk, err := output.Recv()
						if err != nil {
							break
						}
						if msg, ok := chunk.(*schema.Message); ok {
							mu.Lock()
							callbackCapturedStreamChunks = append(callbackCapturedStreamChunks, msg)
							mu.Unlock()
						}
					}
				}()
				return ctx
			}).
			Build()

		ctx := context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{}, handler)

		wrapper := &inputOutputModifyingWrapper{
			inputPrefix:  "[WRAPPER]",
			outputSuffix: "[MODIFIED]",
		}
		wrappedModel := newWrappedChatModel(cm, []ModelCallWrapper{wrapper})

		input := []*schema.Message{schema.UserMessage("test input")}
		stream, err := wrappedModel.Stream(ctx, input)
		assert.NoError(t, err)

		var finalChunks []string
		for {
			msg, err := stream.Recv()
			if err != nil {
				break
			}
			finalChunks = append(finalChunks, msg.Content)
		}

		assert.NotNil(t, modelReceivedInput)
		assert.Len(t, modelReceivedInput, 1)
		assert.Equal(t, "[WRAPPER]test input", modelReceivedInput[0].Content, "Model should receive wrapper-modified input")

		assert.NotNil(t, callbackCapturedInput)
		assert.Len(t, callbackCapturedInput, 1)
		assert.Equal(t, "[WRAPPER]test input", callbackCapturedInput[0].Content, "Callback should capture the same input that model receives")

		assert.Contains(t, finalChunks, "chunk1[MODIFIED]", "Final stream should contain wrapper-modified chunks")
		assert.Contains(t, finalChunks, "chunk2[MODIFIED]", "Final stream should contain wrapper-modified chunks")
	})
}
