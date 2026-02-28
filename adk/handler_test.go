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

type testInstructionHandler struct {
	*BaseChatModelAgentMiddleware
	text string
}

func (h *testInstructionHandler) BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
	if runCtx.Instruction == "" {
		runCtx.Instruction = h.text
	} else if h.text != "" {
		runCtx.Instruction = runCtx.Instruction + "\n" + h.text
	}
	return ctx, runCtx, nil
}

type testInstructionFuncHandler struct {
	*BaseChatModelAgentMiddleware
	fn func(ctx context.Context, instruction string) (context.Context, string, error)
}

func (h *testInstructionFuncHandler) BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
	newCtx, newInstruction, err := h.fn(ctx, runCtx.Instruction)
	if err != nil {
		return ctx, runCtx, err
	}
	runCtx.Instruction = newInstruction
	return newCtx, runCtx, nil
}

type testToolsHandler struct {
	*BaseChatModelAgentMiddleware
	tools []tool.BaseTool
}

func (h *testToolsHandler) BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
	runCtx.Tools = append(runCtx.Tools, h.tools...)
	return ctx, runCtx, nil
}

type testToolsFuncHandler struct {
	*BaseChatModelAgentMiddleware
	fn func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]bool) (context.Context, []tool.BaseTool, map[string]bool, error)
}

func (h *testToolsFuncHandler) BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
	newCtx, newTools, newReturnDirectly, err := h.fn(ctx, runCtx.Tools, runCtx.ReturnDirectly)
	if err != nil {
		return ctx, runCtx, err
	}
	runCtx.Tools = newTools
	runCtx.ReturnDirectly = newReturnDirectly
	return newCtx, runCtx, nil
}

type testBeforeAgentHandler struct {
	*BaseChatModelAgentMiddleware
	fn func(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error)
}

func (h *testBeforeAgentHandler) BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
	return h.fn(ctx, runCtx)
}

type testBeforeModelRewriteStateHandler struct {
	*BaseChatModelAgentMiddleware
	fn func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error)
}

func (h *testBeforeModelRewriteStateHandler) BeforeModelRewriteState(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
	return h.fn(ctx, state, mc)
}

type testAfterModelRewriteStateHandler struct {
	*BaseChatModelAgentMiddleware
	fn func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error)
}

func (h *testAfterModelRewriteStateHandler) AfterModelRewriteState(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
	return h.fn(ctx, state, mc)
}

type testToolWrapperHandler struct {
	*BaseChatModelAgentMiddleware
	wrapInvokableFn  func(context.Context, InvokableToolCallEndpoint, *ToolContext) InvokableToolCallEndpoint
	wrapStreamableFn func(context.Context, StreamableToolCallEndpoint, *ToolContext) StreamableToolCallEndpoint
}

func (h *testToolWrapperHandler) WrapInvokableToolCall(ctx context.Context, endpoint InvokableToolCallEndpoint, tCtx *ToolContext) (InvokableToolCallEndpoint, error) {
	if h.wrapInvokableFn != nil {
		return h.wrapInvokableFn(ctx, endpoint, tCtx), nil
	}
	return endpoint, nil
}

func (h *testToolWrapperHandler) WrapStreamableToolCall(ctx context.Context, endpoint StreamableToolCallEndpoint, tCtx *ToolContext) (StreamableToolCallEndpoint, error) {
	if h.wrapStreamableFn != nil {
		return h.wrapStreamableFn(ctx, endpoint, tCtx), nil
	}
	return endpoint, nil
}

type testModelWrapperHandler struct {
	*BaseChatModelAgentMiddleware
	fn func(context.Context, model.BaseChatModel, *ModelContext) model.BaseChatModel
}

func (h *testModelWrapperHandler) WrapModel(ctx context.Context, m model.BaseChatModel, mc *ModelContext) (model.BaseChatModel, error) {
	return h.fn(ctx, m, mc), nil
}

func newTestInvokableToolCallWrapper(beforeFn, afterFn func()) func(context.Context, InvokableToolCallEndpoint, *ToolContext) InvokableToolCallEndpoint {
	return func(_ context.Context, endpoint InvokableToolCallEndpoint, _ *ToolContext) InvokableToolCallEndpoint {
		return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
			if beforeFn != nil {
				beforeFn()
			}
			result, err := endpoint(ctx, argumentsInJSON, opts...)
			if afterFn != nil {
				afterFn()
			}
			return result, err
		}
	}
}

func newResultModifyingInvokableToolCallWrapper(modifyFn func(string) string) func(context.Context, InvokableToolCallEndpoint, *ToolContext) InvokableToolCallEndpoint {
	return func(_ context.Context, endpoint InvokableToolCallEndpoint, _ *ToolContext) InvokableToolCallEndpoint {
		return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
			result, err := endpoint(ctx, argumentsInJSON, opts...)
			if err == nil && modifyFn != nil {
				result = modifyFn(result)
			}
			return result, err
		}
	}
}

func newTestStreamableToolCallWrapper(beforeFn, afterFn func()) func(context.Context, StreamableToolCallEndpoint, *ToolContext) StreamableToolCallEndpoint {
	return func(_ context.Context, endpoint StreamableToolCallEndpoint, _ *ToolContext) StreamableToolCallEndpoint {
		return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
			if beforeFn != nil {
				beforeFn()
			}
			result, err := endpoint(ctx, argumentsInJSON, opts...)
			if afterFn != nil {
				afterFn()
			}
			return result, err
		}
	}
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
			Handlers: []ChatModelAgentMiddleware{
				&testInstructionHandler{text: "Handler 1 addition."},
				&testInstructionHandler{text: "Handler 2 addition."},
				&testInstructionFuncHandler{fn: func(ctx context.Context, instruction string) (context.Context, string, error) {
					return ctx, instruction + "\nHandler 3 dynamic.", nil
				}},
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
			Handlers: []ChatModelAgentMiddleware{
				&testInstructionHandler{text: "Handler instruction."},
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
			Handlers: []ChatModelAgentMiddleware{
				&testToolsHandler{tools: []tool.BaseTool{tool2}},
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
			Handlers: []ChatModelAgentMiddleware{
				&testToolsFuncHandler{fn: func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]bool) (context.Context, []tool.BaseTool, map[string]bool, error) {
					filtered := make([]tool.BaseTool, 0)
					for _, t := range tools {
						info, _ := t.Info(ctx)
						if info.Name != "tool2" {
							filtered = append(filtered, t)
						}
					}
					return ctx, filtered, returnDirectly, nil
				}},
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
			Handlers: []ChatModelAgentMiddleware{
				&testToolsFuncHandler{fn: func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]bool) (context.Context, []tool.BaseTool, map[string]bool, error) {
					for _, t := range tools {
						info, _ := t.Info(ctx)
						if info.Name == "tool1" {
							returnDirectly[info.Name] = true
						}
					}
					return ctx, tools, returnDirectly, nil
				}},
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
			Handlers: []ChatModelAgentMiddleware{
				&testToolsHandler{tools: []tool.BaseTool{dynamicTool}},
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
	t.Run("BeforeModelRewriteStatePipeline", func(t *testing.T) {
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
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					state.Messages = append(state.Messages, schema.UserMessage("injected1"))
					return ctx, state, nil
				}},
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					state.Messages = append(state.Messages, schema.UserMessage("injected2"))
					return ctx, state, nil
				}},
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

	t.Run("AfterModelRewriteState", func(t *testing.T) {
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
			Handlers: []ChatModelAgentMiddleware{
				&testAfterModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					afterCalled = true
					assert.True(t, len(state.Messages) > 0)
					lastMsg := state.Messages[len(state.Messages)-1]
					assert.Equal(t, schema.Assistant, lastMsg.Role)
					return ctx, state, nil
				}},
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
			Handlers: []ChatModelAgentMiddleware{
				&testToolWrapperHandler{wrapInvokableFn: newTestInvokableToolCallWrapper(
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
				)},
				&testToolWrapperHandler{wrapInvokableFn: newTestInvokableToolCallWrapper(
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
				)},
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

		assert.Equal(t, []string{"wrapper2-before", "wrapper1-before", "wrapper1-after", "wrapper2-after"}, callOrder)
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
			Handlers: []ChatModelAgentMiddleware{
				&testToolWrapperHandler{wrapStreamableFn: newTestStreamableToolCallWrapper(
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
				)},
				&testToolWrapperHandler{wrapStreamableFn: newTestStreamableToolCallWrapper(
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
				)},
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
		assert.Equal(t, []string{"wrapper2-stream-before", "wrapper1-stream-before", "wrapper1-stream-after", "wrapper2-stream-after"}, callOrder,
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
			Handlers: []ChatModelAgentMiddleware{
				&testToolWrapperHandler{wrapInvokableFn: newResultModifyingInvokableToolCallWrapper(func(result string) string {
					return "modified: " + result
				})},
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

func TestToolContextFunctions(t *testing.T) {
	t.Run("ModelContextToolsField", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		testTool := &namedTool{name: "base_tool"}
		info, _ := testTool.Info(ctx)

		var wrapperSeenTools []*schema.ToolInfo

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
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
			Handlers: []ChatModelAgentMiddleware{
				&testModelWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					fn: func(_ context.Context, m model.BaseChatModel, mc *ModelContext) model.BaseChatModel {
						return &toolChainingTestModel{
							inner: m,
							mc:    mc,
							wrapFn: func(ctx context.Context, opts []model.Option) []model.Option {
								wrapperSeenTools = mc.Tools
								return opts
							},
						}
					},
				},
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

		assert.Len(t, wrapperSeenTools, 1, "Wrapper should see base tool")
		assert.Equal(t, info.Name, wrapperSeenTools[0].Name, "Wrapper should see base_tool")
	})
}

type toolChainingTestModel struct {
	inner  model.BaseChatModel
	mc     *ModelContext
	wrapFn func(ctx context.Context, opts []model.Option) []model.Option
}

func (m *toolChainingTestModel) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.wrapFn != nil {
		opts = m.wrapFn(ctx, opts)
	}
	return m.inner.Generate(ctx, msgs, opts...)
}

func (m *toolChainingTestModel) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.wrapFn != nil {
		opts = m.wrapFn(ctx, opts)
	}
	return m.inner.Stream(ctx, msgs, opts...)
}

func (m *toolChainingTestModel) BindTools(tools []*schema.ToolInfo) error {
	return nil
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
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					return context.WithValue(ctx, key1, "value1"), state, nil
				}},
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					handler2ReceivedValue1 = ctx.Value(key1)
					return context.WithValue(ctx, key2, "value2"), state, nil
				}},
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
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeAgentHandler{fn: func(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
					return context.WithValue(ctx, key1, "value1"), runCtx, nil
				}},
				&testBeforeAgentHandler{fn: func(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
					handler2ReceivedValue = ctx.Value(key1)
					return ctx, runCtx, nil
				}},
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
			Handlers:    []ChatModelAgentMiddleware{customHandler},
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
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeAgentHandler{fn: func(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
					return ctx, runCtx, assert.AnError
				}},
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
	*BaseChatModelAgentMiddleware
	beforeAgentCount int
	beforeModelCount int
	afterModelCount  int
	mu               sync.Mutex
}

func (h *countingHandler) BeforeAgent(ctx context.Context, runCtx *ChatModelAgentContext) (context.Context, *ChatModelAgentContext, error) {
	h.mu.Lock()
	h.beforeAgentCount++
	h.mu.Unlock()
	return ctx, runCtx, nil
}

func (h *countingHandler) BeforeModelRewriteState(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
	h.mu.Lock()
	h.beforeModelCount++
	h.mu.Unlock()
	return ctx, state, nil
}

func (h *countingHandler) AfterModelRewriteState(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
	h.mu.Lock()
	h.afterModelCount++
	h.mu.Unlock()
	return ctx, state, nil
}

func newTestModelWrapperFn(beforeFn, afterFn func()) func(context.Context, model.BaseChatModel, *ModelContext) model.BaseChatModel {
	return func(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
		return &testWrappedModel{
			inner:    m,
			beforeFn: beforeFn,
			afterFn:  afterFn,
		}
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
			Handlers: []ChatModelAgentMiddleware{
				&testModelWrapperHandler{fn: newTestModelWrapperFn(
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
				)},
				&testModelWrapperHandler{fn: newTestModelWrapperFn(
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
				)},
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
			Handlers: []ChatModelAgentMiddleware{
				&testModelWrapperHandler{fn: newTestModelWrapperFn(
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
				)},
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
			Handlers: []ChatModelAgentMiddleware{
				&testModelWrapperHandler{fn: newTestModelWrapperFn(
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
				)},
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

func newInputModifyingWrapperFn(inputPrefix string) func(context.Context, model.BaseChatModel, *ModelContext) model.BaseChatModel {
	return func(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
		return &inputOutputModifyingModel{
			inner:       m,
			inputPrefix: inputPrefix,
		}
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
			Handlers: []ChatModelAgentMiddleware{
				&testModelWrapperHandler{fn: newInputModifyingWrapperFn("[WRAPPER]")},
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
			Handlers: []ChatModelAgentMiddleware{
				&testModelWrapperHandler{fn: newInputModifyingWrapperFn("[WRAPPER]")},
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

func TestRunLocalValueFunctions(t *testing.T) {
	t.Run("SetAndGetRunLocalValue", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var capturedValue any
		var capturedFound bool

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					err := SetRunLocalValue(ctx, "test_key", "test_value")
					assert.NoError(t, err)
					return ctx, state, nil
				}},
				&testAfterModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					val, found, err := GetRunLocalValue(ctx, "test_key")
					assert.NoError(t, err)
					capturedValue = val
					capturedFound = found
					return ctx, state, nil
				}},
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

		assert.True(t, capturedFound, "Value should be found")
		assert.Equal(t, "test_value", capturedValue, "Value should match what was set")
	})

	t.Run("DeleteRunLocalValue", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var valueAfterDelete any
		var foundAfterDelete bool

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					err := SetRunLocalValue(ctx, "delete_key", "delete_value")
					assert.NoError(t, err)

					err = DeleteRunLocalValue(ctx, "delete_key")
					assert.NoError(t, err)
					return ctx, state, nil
				}},
				&testAfterModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					val, found, err := GetRunLocalValue(ctx, "delete_key")
					assert.NoError(t, err)
					valueAfterDelete = val
					foundAfterDelete = found
					return ctx, state, nil
				}},
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

		assert.False(t, foundAfterDelete, "Value should not be found after deletion")
		assert.Nil(t, valueAfterDelete, "Value should be nil after deletion")
	})

	t.Run("GetNonExistentKey", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		var capturedValue any
		var capturedFound bool

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					val, found, err := GetRunLocalValue(ctx, "non_existent_key")
					assert.NoError(t, err)
					capturedValue = val
					capturedFound = found
					return ctx, state, nil
				}},
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

		assert.False(t, capturedFound, "Non-existent key should not be found")
		assert.Nil(t, capturedValue, "Non-existent key should return nil value")
	})

	t.Run("RunLocalValueOutsideContext", func(t *testing.T) {
		ctx := context.Background()

		err := SetRunLocalValue(ctx, "key", "value")
		assert.Error(t, err, "SetRunLocalValue should fail outside agent context")
		assert.Contains(t, err.Error(), "SetRunLocalValue failed")

		_, _, err = GetRunLocalValue(ctx, "key")
		assert.Error(t, err, "GetRunLocalValue should fail outside agent context")
		assert.Contains(t, err.Error(), "GetRunLocalValue failed")

		err = DeleteRunLocalValue(ctx, "key")
		assert.Error(t, err, "DeleteRunLocalValue should fail outside agent context")
		assert.Contains(t, err.Error(), "DeleteRunLocalValue failed")
	})

	t.Run("RunLocalValuePersistsAcrossModelCalls", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		testTool := &namedTool{name: "test_tool"}
		info, _ := testTool.Info(ctx)

		var firstCallValue any
		var secondCallValue any
		callCount := 0

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
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					callCount++
					if callCount == 1 {
						err := SetRunLocalValue(ctx, "persist_key", "persist_value")
						assert.NoError(t, err)
						val, _, _ := GetRunLocalValue(ctx, "persist_key")
						firstCallValue = val
					} else {
						val, _, _ := GetRunLocalValue(ctx, "persist_key")
						secondCallValue = val
					}
					return ctx, state, nil
				}},
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

		assert.Equal(t, "persist_value", firstCallValue, "First call should set value")
		assert.Equal(t, "persist_value", secondCallValue, "Value should persist to second model call")
	})
}

func TestHandlerErrorPropagation(t *testing.T) {
	t.Run("BeforeModelRewriteStateErrorStopsRun", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					return ctx, state, assert.AnError
				}},
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})

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
	})

	t.Run("AfterModelRewriteStateErrorStopsRun", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("response", nil), nil).Times(1)

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []ChatModelAgentMiddleware{
				&testAfterModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					return ctx, state, assert.AnError
				}},
			},
		})
		assert.NoError(t, err)

		iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})

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
	})

	t.Run("MultipleHandlersFirstErrorStops", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		secondHandlerCalled := false

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       cm,
			Handlers: []ChatModelAgentMiddleware{
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					return ctx, state, assert.AnError
				}},
				&testBeforeModelRewriteStateHandler{fn: func(ctx context.Context, state *ChatModelAgentState, mc *ModelContext) (context.Context, *ChatModelAgentState, error) {
					secondHandlerCalled = true
					return ctx, state, nil
				}},
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

		assert.False(t, secondHandlerCalled, "Second handler should not be called after first handler error")
	})
}

func TestToolContextInWrappers(t *testing.T) {
	t.Run("ToolContextHasCorrectNameAndCallID", func(t *testing.T) {
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		testTool := &namedTool{name: "context_test_tool"}
		info, _ := testTool.Info(ctx)

		var capturedToolName string
		var capturedCallID string

		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Using tool", []schema.ToolCall{
				{ID: "test_call_id_123", Function: schema.FunctionCall{Name: info.Name, Arguments: "{}"}},
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
			Handlers: []ChatModelAgentMiddleware{
				&testToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapInvokableFn: func(_ context.Context, endpoint InvokableToolCallEndpoint, tCtx *ToolContext) InvokableToolCallEndpoint {
						capturedToolName = tCtx.Name
						capturedCallID = tCtx.CallID
						return endpoint
					},
				},
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

		assert.Equal(t, "context_test_tool", capturedToolName, "ToolContext should have correct tool name")
		assert.Equal(t, "test_call_id_123", capturedCallID, "ToolContext should have correct call ID")
	})
}
