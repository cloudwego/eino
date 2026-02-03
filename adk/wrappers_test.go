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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type testEnhancedToolWrapperHandler struct {
	*BaseChatModelAgentMiddleware
	wrapEnhancedInvokableFn  func(EnhancedInvokableToolCallEndpoint, *ToolContext) EnhancedInvokableToolCallEndpoint
	wrapEnhancedStreamableFn func(EnhancedStreamableToolCallEndpoint, *ToolContext) EnhancedStreamableToolCallEndpoint
}

func (h *testEnhancedToolWrapperHandler) WrapEnhancedInvokableToolCall(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
	if h.wrapEnhancedInvokableFn != nil {
		return h.wrapEnhancedInvokableFn(endpoint, tCtx)
	}
	return endpoint
}

func (h *testEnhancedToolWrapperHandler) WrapEnhancedStreamableToolCall(endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint {
	if h.wrapEnhancedStreamableFn != nil {
		return h.wrapEnhancedStreamableFn(endpoint, tCtx)
	}
	return endpoint
}

func newTestEnhancedInvokableToolCallWrapper(beforeFn, afterFn func()) func(EnhancedInvokableToolCallEndpoint, *ToolContext) EnhancedInvokableToolCallEndpoint {
	return func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
		return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
			if beforeFn != nil {
				beforeFn()
			}
			result, err := endpoint(ctx, toolArgument, opts...)
			if afterFn != nil {
				afterFn()
			}
			return result, err
		}
	}
}

func newTestEnhancedStreamableToolCallWrapper(beforeFn, afterFn func()) func(EnhancedStreamableToolCallEndpoint, *ToolContext) EnhancedStreamableToolCallEndpoint {
	return func(endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint {
		return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
			if beforeFn != nil {
				beforeFn()
			}
			result, err := endpoint(ctx, toolArgument, opts...)
			if afterFn != nil {
				afterFn()
			}
			return result, err
		}
	}
}

func TestHandlersToToolMiddlewaresEnhanced(t *testing.T) {
	t.Run("OnlyEnhancedInvokableHandler", func(t *testing.T) {
		var called bool
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
							called = true
							return endpoint(ctx, toolArgument, opts...)
						}
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 1)
		assert.NotNil(t, middlewares[0].EnhancedInvokable)
		assert.Nil(t, middlewares[0].Invokable)
		assert.Nil(t, middlewares[0].Streamable)
		assert.Nil(t, middlewares[0].EnhancedStreamable)

		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{
				Result: &schema.ToolResult{
					Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "test"}},
				},
			}, nil
		}

		wrapped := middlewares[0].EnhancedInvokable(mockEndpoint)
		_, err := wrapped(context.Background(), &compose.ToolInput{
			Name:      "test_tool",
			CallID:    "call-1",
			Arguments: `{"input": "test"}`,
		})
		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("OnlyEnhancedStreamableHandler", func(t *testing.T) {
		var called bool
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedStreamableFn: func(endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
							called = true
							return endpoint(ctx, toolArgument, opts...)
						}
					},
				},
				hasWrapEnhancedStreamableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 1)
		assert.NotNil(t, middlewares[0].EnhancedStreamable)
		assert.Nil(t, middlewares[0].Invokable)
		assert.Nil(t, middlewares[0].Streamable)
		assert.Nil(t, middlewares[0].EnhancedInvokable)

		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return &compose.EnhancedStreamableToolOutput{
				Result: schema.StreamReaderFromArray([]*schema.ToolResult{
					{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "test"}}},
				}),
			}, nil
		}

		wrapped := middlewares[0].EnhancedStreamable(mockEndpoint)
		_, err := wrapped(context.Background(), &compose.ToolInput{
			Name:      "test_tool",
			CallID:    "call-1",
			Arguments: `{"input": "test"}`,
		})
		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("MixedHandlers", func(t *testing.T) {
		var invokableCalled, streamableCalled, enhancedInvokableCalled, enhancedStreamableCalled bool

		handlers := []handlerInfo{
			{
				handler: &testToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapInvokableFn: func(endpoint InvokableToolCallEndpoint, tCtx *ToolContext) InvokableToolCallEndpoint {
						return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
							invokableCalled = true
							return endpoint(ctx, argumentsInJSON, opts...)
						}
					},
				},
				hasWrapInvokableToolCall: true,
			},
			{
				handler: &testToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapStreamableFn: func(endpoint StreamableToolCallEndpoint, tCtx *ToolContext) StreamableToolCallEndpoint {
						return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
							streamableCalled = true
							return endpoint(ctx, argumentsInJSON, opts...)
						}
					},
				},
				hasWrapStreamableToolCall: true,
			},
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
							enhancedInvokableCalled = true
							return endpoint(ctx, toolArgument, opts...)
						}
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedStreamableFn: func(endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
							enhancedStreamableCalled = true
							return endpoint(ctx, toolArgument, opts...)
						}
					},
				},
				hasWrapEnhancedStreamableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 4)

		invokableEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			return &compose.ToolOutput{Result: "test"}, nil
		}
		_, _ = middlewares[3].Invokable(invokableEndpoint)(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		streamableEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{"test"})}, nil
		}
		_, _ = middlewares[2].Streamable(streamableEndpoint)(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		enhancedInvokableEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{Result: &schema.ToolResult{}}, nil
		}
		_, _ = middlewares[1].EnhancedInvokable(enhancedInvokableEndpoint)(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		enhancedStreamableEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return &compose.EnhancedStreamableToolOutput{Result: schema.StreamReaderFromArray([]*schema.ToolResult{{}})}, nil
		}
		_, _ = middlewares[0].EnhancedStreamable(enhancedStreamableEndpoint)(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		assert.True(t, invokableCalled)
		assert.True(t, streamableCalled)
		assert.True(t, enhancedInvokableCalled)
		assert.True(t, enhancedStreamableCalled)
	})

	t.Run("NoHandlers", func(t *testing.T) {
		handlers := []handlerInfo{}
		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 0)
	})

	t.Run("HandlerWithNoToolWrappers", func(t *testing.T) {
		handlers := []handlerInfo{
			{
				handler:        &BaseChatModelAgentMiddleware{},
				hasBeforeAgent: true,
			},
		}
		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 0)
	})

	t.Run("EnhancedInvokableToolCallErrorPropagation", func(t *testing.T) {
		expectedErr := errors.New("test error")
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
							return nil, expectedErr
						}
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{Result: &schema.ToolResult{}}, nil
		}

		wrapped := middlewares[0].EnhancedInvokable(mockEndpoint)
		_, err := wrapped(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})

	t.Run("EnhancedStreamableToolCallErrorPropagation", func(t *testing.T) {
		expectedErr := errors.New("test error")
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedStreamableFn: func(endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
							return nil, expectedErr
						}
					},
				},
				hasWrapEnhancedStreamableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return &compose.EnhancedStreamableToolOutput{Result: schema.StreamReaderFromArray([]*schema.ToolResult{})}, nil
		}

		wrapped := middlewares[0].EnhancedStreamable(mockEndpoint)
		_, err := wrapped(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})

	t.Run("MultipleEnhancedInvokableWrappers", func(t *testing.T) {
		var executionOrder []string
		var mu sync.Mutex

		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: newTestEnhancedInvokableToolCallWrapper(
						func() {
							mu.Lock()
							executionOrder = append(executionOrder, "handler1-before")
							mu.Unlock()
						},
						func() {
							mu.Lock()
							executionOrder = append(executionOrder, "handler1-after")
							mu.Unlock()
						},
					),
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: newTestEnhancedInvokableToolCallWrapper(
						func() {
							mu.Lock()
							executionOrder = append(executionOrder, "handler2-before")
							mu.Unlock()
						},
						func() {
							mu.Lock()
							executionOrder = append(executionOrder, "handler2-after")
							mu.Unlock()
						},
					),
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 2)

		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{Result: &schema.ToolResult{}}, nil
		}

		wrapped := middlewares[0].EnhancedInvokable(middlewares[1].EnhancedInvokable(mockEndpoint))
		_, err := wrapped(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})
		assert.NoError(t, err)
		assert.Equal(t, []string{"handler2-before", "handler1-before", "handler1-after", "handler2-after"}, executionOrder)
	})

	t.Run("MultipleEnhancedStreamableWrappers", func(t *testing.T) {
		var executionOrder []string
		var mu sync.Mutex

		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedStreamableFn: newTestEnhancedStreamableToolCallWrapper(
						func() {
							mu.Lock()
							executionOrder = append(executionOrder, "handler1-before")
							mu.Unlock()
						},
						func() {
							mu.Lock()
							executionOrder = append(executionOrder, "handler1-after")
							mu.Unlock()
						},
					),
				},
				hasWrapEnhancedStreamableToolCall: true,
			},
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedStreamableFn: newTestEnhancedStreamableToolCallWrapper(
						func() {
							mu.Lock()
							executionOrder = append(executionOrder, "handler2-before")
							mu.Unlock()
						},
						func() {
							mu.Lock()
							executionOrder = append(executionOrder, "handler2-after")
							mu.Unlock()
						},
					),
				},
				hasWrapEnhancedStreamableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 2)

		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return &compose.EnhancedStreamableToolOutput{Result: schema.StreamReaderFromArray([]*schema.ToolResult{{}})}, nil
		}

		wrapped := middlewares[0].EnhancedStreamable(middlewares[1].EnhancedStreamable(mockEndpoint))
		_, err := wrapped(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})
		assert.NoError(t, err)
		assert.Equal(t, []string{"handler2-before", "handler1-before", "handler1-after", "handler2-after"}, executionOrder)
	})
}

func TestEnhancedToolContextPropagation(t *testing.T) {
	t.Run("ToolContextContainsCorrectInfo", func(t *testing.T) {
		var capturedCtx *ToolContext
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						capturedCtx = tCtx
						return endpoint
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{Result: &schema.ToolResult{}}, nil
		}

		wrapped := middlewares[0].EnhancedInvokable(mockEndpoint)
		_, _ = wrapped(context.Background(), &compose.ToolInput{
			Name:      "my_tool",
			CallID:    "call-123",
			Arguments: `{"key": "value"}`,
		})

		assert.NotNil(t, capturedCtx)
		assert.Equal(t, "my_tool", capturedCtx.Name)
		assert.Equal(t, "call-123", capturedCtx.CallID)
	})

	t.Run("StreamableToolContextContainsCorrectInfo", func(t *testing.T) {
		var capturedCtx *ToolContext
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedStreamableFn: func(endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint {
						capturedCtx = tCtx
						return endpoint
					},
				},
				hasWrapEnhancedStreamableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return &compose.EnhancedStreamableToolOutput{Result: schema.StreamReaderFromArray([]*schema.ToolResult{{}})}, nil
		}

		wrapped := middlewares[0].EnhancedStreamable(mockEndpoint)
		_, _ = wrapped(context.Background(), &compose.ToolInput{
			Name:      "stream_tool",
			CallID:    "call-456",
			Arguments: `{"data": "test"}`,
		})

		assert.NotNil(t, capturedCtx)
		assert.Equal(t, "stream_tool", capturedCtx.Name)
		assert.Equal(t, "call-456", capturedCtx.CallID)
	})
}

func TestBaseChatModelAgentMiddlewareEnhancedDefaults(t *testing.T) {
	t.Run("DefaultEnhancedInvokableReturnsEndpoint", func(t *testing.T) {
		base := &BaseChatModelAgentMiddleware{}

		var called bool
		endpoint := func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
			called = true
			return &schema.ToolResult{}, nil
		}

		wrapped := base.WrapEnhancedInvokableToolCall(endpoint, &ToolContext{Name: "test", CallID: "1"})
		_, err := wrapped(context.Background(), &schema.ToolArgument{TextArgument: "{}"})

		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("DefaultEnhancedStreamableReturnsEndpoint", func(t *testing.T) {
		base := &BaseChatModelAgentMiddleware{}

		var called bool
		endpoint := func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
			called = true
			return schema.StreamReaderFromArray([]*schema.ToolResult{}), nil
		}

		wrapped := base.WrapEnhancedStreamableToolCall(endpoint, &ToolContext{Name: "test", CallID: "1"})
		_, err := wrapped(context.Background(), &schema.ToolArgument{TextArgument: "{}"})

		assert.NoError(t, err)
		assert.True(t, called)
	})
}

func TestEnhancedToolArgumentsPropagation(t *testing.T) {
	t.Run("ArgumentsPassedCorrectly", func(t *testing.T) {
		var capturedArgs string
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
							capturedArgs = toolArgument.TextArgument
							return endpoint(ctx, toolArgument, opts...)
						}
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{Result: &schema.ToolResult{}}, nil
		}

		wrapped := middlewares[0].EnhancedInvokable(mockEndpoint)
		_, _ = wrapped(context.Background(), &compose.ToolInput{
			Name:      "test_tool",
			CallID:    "call-1",
			Arguments: `{"name": "test", "value": 123}`,
		})

		assert.Equal(t, `{"name": "test", "value": 123}`, capturedArgs)
	})
}

func TestEnhancedToolResultPropagation(t *testing.T) {
	t.Run("ResultPassedThroughMiddleware", func(t *testing.T) {
		expectedResult := &schema.ToolResult{
			Parts: []schema.ToolOutputPart{
				{Type: schema.ToolPartTypeText, Text: "original result"},
			},
		}

		var capturedResult *schema.ToolResult
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
							result, err := endpoint(ctx, toolArgument, opts...)
							capturedResult = result
							return result, err
						}
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{Result: expectedResult}, nil
		}

		wrapped := middlewares[0].EnhancedInvokable(mockEndpoint)
		output, err := wrapped(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		assert.NoError(t, err)
		assert.Equal(t, expectedResult, capturedResult)
		assert.Equal(t, expectedResult, output.Result)
	})

	t.Run("ModifiedResultPropagated", func(t *testing.T) {
		modifiedResult := &schema.ToolResult{
			Parts: []schema.ToolOutputPart{
				{Type: schema.ToolPartTypeText, Text: "modified result"},
			},
		}

		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
							_, err := endpoint(ctx, toolArgument, opts...)
							if err != nil {
								return nil, err
							}
							return modifiedResult, nil
						}
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{Result: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "original"}},
			}}, nil
		}

		wrapped := middlewares[0].EnhancedInvokable(mockEndpoint)
		output, err := wrapped(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		assert.NoError(t, err)
		assert.Equal(t, modifiedResult, output.Result)
		assert.Equal(t, "modified result", output.Result.Parts[0].Text)
	})
}

func TestEnhancedToolEndpointErrorFromNext(t *testing.T) {
	t.Run("EnhancedInvokableNextError", func(t *testing.T) {
		expectedErr := errors.New("next endpoint error")
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
							return endpoint(ctx, toolArgument, opts...)
						}
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return nil, expectedErr
		}

		wrapped := middlewares[0].EnhancedInvokable(mockEndpoint)
		_, err := wrapped(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})

	t.Run("EnhancedStreamableNextError", func(t *testing.T) {
		expectedErr := errors.New("next endpoint error")
		handlers := []handlerInfo{
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedStreamableFn: func(endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint {
						return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
							return endpoint(ctx, toolArgument, opts...)
						}
					},
				},
				hasWrapEnhancedStreamableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return nil, expectedErr
		}

		wrapped := middlewares[0].EnhancedStreamable(mockEndpoint)
		_, err := wrapped(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})
}

func TestWrapModelStreamChunksPreserved(t *testing.T) {
	t.Run("AgentEventMessageStreamShouldPreserveChunksWithNoopWrapModel", func(t *testing.T) {
		ctx := context.Background()

		chunk1 := schema.AssistantMessage("Hello ", nil)
		chunk2 := schema.AssistantMessage("World", nil)

		mockModel := &mockStreamingModel{
			chunks: []*schema.Message{chunk1, chunk2},
		}

		noopWrapModelHandler := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(m model.BaseChatModel, mc *ModelContext) model.BaseChatModel {
				return m
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       mockModel,
			Handlers:    []ChatModelAgentMiddleware{noopWrapModelHandler},
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
			},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: true,
		})
		iter := r.Run(ctx, []Message{schema.UserMessage("test")})

		var streamingEvents []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.IsStreaming &&
				event.Output.MessageOutput.Role == schema.Assistant {
				streamingEvents = append(streamingEvents, event)
			}
		}

		assert.GreaterOrEqual(t, len(streamingEvents), 1, "Should have at least one streaming event")

		if len(streamingEvents) > 0 {
			event := streamingEvents[0]
			assert.NotNil(t, event.Output.MessageOutput.MessageStream, "Event should have message stream")

			var receivedChunks []*schema.Message
			for {
				chunk, recvErr := event.Output.MessageOutput.MessageStream.Recv()
				if recvErr != nil {
					break
				}
				receivedChunks = append(receivedChunks, chunk)
			}

			assert.Equal(t, 2, len(receivedChunks),
				"AgentEvent's MessageStream should contain 2 separate chunks, not 1 concatenated chunk. "+
					"Got %d chunks instead. This indicates the stream is being concatenated before being sent to AgentEvent.",
				len(receivedChunks))

			if len(receivedChunks) >= 2 {
				assert.Equal(t, "Hello ", receivedChunks[0].Content, "First chunk content should be preserved")
				assert.Equal(t, "World", receivedChunks[1].Content, "Second chunk content should be preserved")
			}
		}
	})

	t.Run("AgentEventMessageStreamShouldPreserveChunksWithStreamConsumingWrapModel", func(t *testing.T) {
		ctx := context.Background()

		chunk1 := schema.AssistantMessage("Hello ", nil)
		chunk2 := schema.AssistantMessage("World", nil)

		mockModel := &mockStreamingModel{
			chunks: []*schema.Message{chunk1, chunk2},
		}

		streamConsumingWrapModelHandler := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(m model.BaseChatModel, mc *ModelContext) model.BaseChatModel {
				return &streamConsumingModelWrapper{inner: m}
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       mockModel,
			Handlers:    []ChatModelAgentMiddleware{streamConsumingWrapModelHandler},
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
			},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: true,
		})
		iter := r.Run(ctx, []Message{schema.UserMessage("test")})

		var streamingEvents []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.IsStreaming &&
				event.Output.MessageOutput.Role == schema.Assistant {
				streamingEvents = append(streamingEvents, event)
			}
		}

		assert.GreaterOrEqual(t, len(streamingEvents), 1, "Should have at least one streaming event")

		if len(streamingEvents) > 0 {
			event := streamingEvents[0]
			assert.NotNil(t, event.Output.MessageOutput.MessageStream, "Event should have message stream")

			var receivedChunks []*schema.Message
			for {
				chunk, recvErr := event.Output.MessageOutput.MessageStream.Recv()
				if recvErr != nil {
					break
				}
				receivedChunks = append(receivedChunks, chunk)
			}

			assert.Equal(t, 2, len(receivedChunks),
				"AgentEvent's MessageStream should contain 2 separate chunks, not 1 concatenated chunk. "+
					"Got %d chunks instead. This indicates the stream is being concatenated before being sent to AgentEvent.",
				len(receivedChunks))

			if len(receivedChunks) >= 2 {
				assert.Equal(t, "Hello ", receivedChunks[0].Content, "First chunk content should be preserved")
				assert.Equal(t, "World", receivedChunks[1].Content, "Second chunk content should be preserved")
			}
		}
	})

	t.Run("AgentEventMessageStreamShouldPreserveChunksWithMultipleWrapModelHandlers", func(t *testing.T) {
		ctx := context.Background()

		chunk1 := schema.AssistantMessage("Hello ", nil)
		chunk2 := schema.AssistantMessage("World", nil)

		mockModel := &mockStreamingModel{
			chunks: []*schema.Message{chunk1, chunk2},
		}

		handler1 := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(m model.BaseChatModel, mc *ModelContext) model.BaseChatModel {
				return &streamConsumingModelWrapper{inner: m}
			},
		}

		handler2 := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(m model.BaseChatModel, mc *ModelContext) model.BaseChatModel {
				return m
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       mockModel,
			Handlers:    []ChatModelAgentMiddleware{handler1, handler2},
			ModelRetryConfig: &ModelRetryConfig{
				MaxRetries: 3,
			},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: true,
		})
		iter := r.Run(ctx, []Message{schema.UserMessage("test")})

		var streamingEvents []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.IsStreaming &&
				event.Output.MessageOutput.Role == schema.Assistant {
				streamingEvents = append(streamingEvents, event)
			}
		}

		assert.GreaterOrEqual(t, len(streamingEvents), 1, "Should have at least one streaming event")

		if len(streamingEvents) > 0 {
			event := streamingEvents[0]
			assert.NotNil(t, event.Output.MessageOutput.MessageStream, "Event should have message stream")

			var receivedChunks []*schema.Message
			for {
				chunk, recvErr := event.Output.MessageOutput.MessageStream.Recv()
				if recvErr != nil {
					break
				}
				receivedChunks = append(receivedChunks, chunk)
			}

			assert.Equal(t, 2, len(receivedChunks),
				"AgentEvent's MessageStream should contain 2 separate chunks, not 1 concatenated chunk. "+
					"Got %d chunks instead. This indicates the stream is being concatenated before being sent to AgentEvent.",
				len(receivedChunks))

			if len(receivedChunks) >= 2 {
				assert.Equal(t, "Hello ", receivedChunks[0].Content, "First chunk content should be preserved")
				assert.Equal(t, "World", receivedChunks[1].Content, "Second chunk content should be preserved")
			}
		}
	})
}

type mockStreamingModel struct {
	chunks []*schema.Message
}

func (m *mockStreamingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return schema.ConcatMessages(m.chunks)
}

func (m *mockStreamingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](len(m.chunks))
	go func() {
		defer sw.Close()
		for _, chunk := range m.chunks {
			sw.Send(chunk, nil)
		}
	}()
	return sr, nil
}

type streamConsumingModelWrapper struct {
	inner model.BaseChatModel
}

func (m *streamConsumingModelWrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, input, opts...)
}

func (m *streamConsumingModelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	result, err := schema.ConcatMessageStream(stream)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{result}), nil
}

func TestHandlersToToolMiddlewaresConditionCheck(t *testing.T) {
	t.Run("SkipHandlerWithoutToolWrappers", func(t *testing.T) {
		handlers := []handlerInfo{
			{
				handler:                           &BaseChatModelAgentMiddleware{},
				hasBeforeAgent:                    true,
				hasWrapInvokableToolCall:          false,
				hasWrapStreamableToolCall:         false,
				hasWrapEnhancedInvokableToolCall:  false,
				hasWrapEnhancedStreamableToolCall: false,
			},
			{
				handler: &testEnhancedToolWrapperHandler{
					BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
					wrapEnhancedInvokableFn: func(endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
						return endpoint
					},
				},
				hasWrapEnhancedInvokableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 1)
		assert.NotNil(t, middlewares[0].EnhancedInvokable)
	})

	t.Run("AllFourTypesInOneHandler", func(t *testing.T) {
		type allTypesHandler struct {
			*BaseChatModelAgentMiddleware
		}

		handlers := []handlerInfo{
			{
				handler:                           &allTypesHandler{BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{}},
				hasWrapInvokableToolCall:          true,
				hasWrapStreamableToolCall:         true,
				hasWrapEnhancedInvokableToolCall:  true,
				hasWrapEnhancedStreamableToolCall: true,
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 1)
		assert.NotNil(t, middlewares[0].Invokable)
		assert.NotNil(t, middlewares[0].Streamable)
		assert.NotNil(t, middlewares[0].EnhancedInvokable)
		assert.NotNil(t, middlewares[0].EnhancedStreamable)
	})
}
