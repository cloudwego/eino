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
				WithToolsFunc(func(ctx context.Context, tools []ToolMeta) (context.Context, []ToolMeta, error) {
					filtered := make([]ToolMeta, 0)
					for _, t := range tools {
						info, _ := t.Tool.Info(ctx)
						if info.Name != "tool2" {
							filtered = append(filtered, t)
						}
					}
					return ctx, filtered, nil
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
				WithToolsFunc(func(ctx context.Context, tools []ToolMeta) (context.Context, []ToolMeta, error) {
					for i := range tools {
						info, _ := tools[i].Tool.Info(ctx)
						if info.Name == "tool1" {
							tools[i].ReturnDirectly = true
						}
					}
					return ctx, tools, nil
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
				WithToolMiddleware(compose.ToolMiddleware{
					Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
						return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
							mu.Lock()
							callOrder = append(callOrder, "wrapper1-before")
							mu.Unlock()
							result, err := next(ctx, input)
							mu.Lock()
							callOrder = append(callOrder, "wrapper1-after")
							mu.Unlock()
							return result, err
						}
					},
				}),
				WithToolMiddleware(compose.ToolMiddleware{
					Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
						return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
							mu.Lock()
							callOrder = append(callOrder, "wrapper2-before")
							mu.Unlock()
							result, err := next(ctx, input)
							mu.Lock()
							callOrder = append(callOrder, "wrapper2-after")
							mu.Unlock()
							return result, err
						}
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
				WithToolMiddleware(compose.ToolMiddleware{
					Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
						return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
							result, err := next(ctx, input)
							if err == nil {
								result.Result = "modified: " + result.Result
							}
							return result, err
						}
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
				WithBeforeAgent(func(ctx context.Context, config *AgentConfig) (context.Context, error) {
					return context.WithValue(ctx, key1, "value1"), nil
				}),
				WithBeforeAgent(func(ctx context.Context, config *AgentConfig) (context.Context, error) {
					handler2ReceivedValue = ctx.Value(key1)
					return ctx, nil
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
				WithBeforeAgent(func(ctx context.Context, config *AgentConfig) (context.Context, error) {
					return ctx, assert.AnError
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

type countingHandler struct {
	BaseAgentHandler
	beforeAgentCount int
	beforeModelCount int
	afterModelCount  int
	mu               sync.Mutex
}

func (h *countingHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
	h.mu.Lock()
	h.beforeAgentCount++
	h.mu.Unlock()
	return ctx, nil
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
