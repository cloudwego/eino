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
	wrapEnhancedInvokableFn  func(context.Context, EnhancedInvokableToolCallEndpoint, *ToolContext) EnhancedInvokableToolCallEndpoint
	wrapEnhancedStreamableFn func(context.Context, EnhancedStreamableToolCallEndpoint, *ToolContext) EnhancedStreamableToolCallEndpoint
}

func (h *testEnhancedToolWrapperHandler) WrapEnhancedInvokableToolCall(ctx context.Context, endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) (EnhancedInvokableToolCallEndpoint, error) {
	if h.wrapEnhancedInvokableFn != nil {
		return h.wrapEnhancedInvokableFn(ctx, endpoint, tCtx), nil
	}
	return endpoint, nil
}

func (h *testEnhancedToolWrapperHandler) WrapEnhancedStreamableToolCall(ctx context.Context, endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) (EnhancedStreamableToolCallEndpoint, error) {
	if h.wrapEnhancedStreamableFn != nil {
		return h.wrapEnhancedStreamableFn(ctx, endpoint, tCtx), nil
	}
	return endpoint, nil
}

func newTestEnhancedInvokableToolCallWrapper(beforeFn, afterFn func()) func(context.Context, EnhancedInvokableToolCallEndpoint, *ToolContext) EnhancedInvokableToolCallEndpoint {
	return func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
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

func newTestEnhancedStreamableToolCallWrapper(beforeFn, afterFn func()) func(context.Context, EnhancedStreamableToolCallEndpoint, *ToolContext) EnhancedStreamableToolCallEndpoint {
	return func(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, _ *ToolContext) EnhancedStreamableToolCallEndpoint {
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
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedInvokableFn: func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
						called = true
						return endpoint(ctx, toolArgument, opts...)
					}
				},
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 1)
		assert.NotNil(t, middlewares[0].EnhancedInvokable)
		assert.NotNil(t, middlewares[0].Invokable)
		assert.NotNil(t, middlewares[0].Streamable)
		assert.NotNil(t, middlewares[0].EnhancedStreamable)

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
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedStreamableFn: func(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, _ *ToolContext) EnhancedStreamableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
						called = true
						return endpoint(ctx, toolArgument, opts...)
					}
				},
			},
		}

		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 1)
		assert.NotNil(t, middlewares[0].EnhancedStreamable)
		assert.NotNil(t, middlewares[0].Invokable)
		assert.NotNil(t, middlewares[0].Streamable)
		assert.NotNil(t, middlewares[0].EnhancedInvokable)

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

		handlers := []ChatModelAgentMiddleware{
			&testToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapInvokableFn: func(_ context.Context, endpoint InvokableToolCallEndpoint, _ *ToolContext) InvokableToolCallEndpoint {
					return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
						invokableCalled = true
						return endpoint(ctx, argumentsInJSON, opts...)
					}
				},
			},
			&testToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapStreamableFn: func(_ context.Context, endpoint StreamableToolCallEndpoint, _ *ToolContext) StreamableToolCallEndpoint {
					return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
						streamableCalled = true
						return endpoint(ctx, argumentsInJSON, opts...)
					}
				},
			},
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedInvokableFn: func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
						enhancedInvokableCalled = true
						return endpoint(ctx, toolArgument, opts...)
					}
				},
			},
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedStreamableFn: func(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, _ *ToolContext) EnhancedStreamableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
						enhancedStreamableCalled = true
						return endpoint(ctx, toolArgument, opts...)
					}
				},
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
		handlers := []ChatModelAgentMiddleware{}
		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 0)
	})

	t.Run("HandlerWithNoToolWrappers", func(t *testing.T) {
		handlers := []ChatModelAgentMiddleware{
			&BaseChatModelAgentMiddleware{},
		}
		middlewares := handlersToToolMiddlewares(handlers)
		assert.Len(t, middlewares, 1)
	})

	t.Run("EnhancedInvokableToolCallErrorPropagation", func(t *testing.T) {
		expectedErr := errors.New("test error")
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedInvokableFn: func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
						return nil, expectedErr
					}
				},
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
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedStreamableFn: func(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, _ *ToolContext) EnhancedStreamableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
						return nil, expectedErr
					}
				},
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

		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
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
			&testEnhancedToolWrapperHandler{
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

		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
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
			&testEnhancedToolWrapperHandler{
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
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedInvokableFn: func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) EnhancedInvokableToolCallEndpoint {
					capturedCtx = tCtx
					return endpoint
				},
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
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedStreamableFn: func(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) EnhancedStreamableToolCallEndpoint {
					capturedCtx = tCtx
					return endpoint
				},
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

		wrapped, wrapErr := base.WrapEnhancedInvokableToolCall(context.Background(), endpoint, &ToolContext{Name: "test", CallID: "1"})
		assert.NoError(t, wrapErr)
		_, err := wrapped(context.Background(), &schema.ToolArgument{Text: "{}"})

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

		wrapped, wrapErr := base.WrapEnhancedStreamableToolCall(context.Background(), endpoint, &ToolContext{Name: "test", CallID: "1"})
		assert.NoError(t, wrapErr)
		_, err := wrapped(context.Background(), &schema.ToolArgument{Text: "{}"})

		assert.NoError(t, err)
		assert.True(t, called)
	})
}

func TestEnhancedToolArgumentsPropagation(t *testing.T) {
	t.Run("ArgumentsPassedCorrectly", func(t *testing.T) {
		var capturedArgs string
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedInvokableFn: func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
						capturedArgs = toolArgument.Text
						return endpoint(ctx, toolArgument, opts...)
					}
				},
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
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedInvokableFn: func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
						result, err := endpoint(ctx, toolArgument, opts...)
						capturedResult = result
						return result, err
					}
				},
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

		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedInvokableFn: func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
						_, err := endpoint(ctx, toolArgument, opts...)
						if err != nil {
							return nil, err
						}
						return modifiedResult, nil
					}
				},
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
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedInvokableFn: func(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) EnhancedInvokableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
						return endpoint(ctx, toolArgument, opts...)
					}
				},
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
		handlers := []ChatModelAgentMiddleware{
			&testEnhancedToolWrapperHandler{
				BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
				wrapEnhancedStreamableFn: func(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, _ *ToolContext) EnhancedStreamableToolCallEndpoint {
					return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
						return endpoint(ctx, toolArgument, opts...)
					}
				},
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
			fn: func(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
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

	t.Run("AgentEventMessageStreamShouldReflectUserMiddlewareModifications", func(t *testing.T) {
		ctx := context.Background()

		chunk1 := schema.AssistantMessage("Hello ", nil)
		chunk2 := schema.AssistantMessage("World", nil)

		mockModel := &mockStreamingModel{
			chunks: []*schema.Message{chunk1, chunk2},
		}

		streamConsumingWrapModelHandler := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
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

			assert.Equal(t, 1, len(receivedChunks),
				"AgentEvent's MessageStream should contain 1 concatenated chunk (modified by user middleware). "+
					"Got %d chunks instead.",
				len(receivedChunks))

			if len(receivedChunks) >= 1 {
				assert.Equal(t, "Hello World", receivedChunks[0].Content, "Chunk content should be concatenated by user middleware")
			}
		}
	})

	t.Run("AgentEventMessageStreamShouldReflectMultipleUserMiddlewareModifications", func(t *testing.T) {
		ctx := context.Background()

		chunk1 := schema.AssistantMessage("Hello ", nil)
		chunk2 := schema.AssistantMessage("World", nil)

		mockModel := &mockStreamingModel{
			chunks: []*schema.Message{chunk1, chunk2},
		}

		handler1 := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
				return &streamConsumingModelWrapper{inner: m}
			},
		}

		handler2 := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
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

			assert.Equal(t, 1, len(receivedChunks),
				"AgentEvent's MessageStream should contain 1 concatenated chunk (modified by user middleware). "+
					"Got %d chunks instead.",
				len(receivedChunks))

			if len(receivedChunks) >= 1 {
				assert.Equal(t, "Hello World", receivedChunks[0].Content, "Chunk content should be concatenated by user middleware")
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

func TestEventSenderModelWrapperCustomPosition(t *testing.T) {
	t.Run("UserConfiguredEventSenderSkipsDefaultEventSender", func(t *testing.T) {
		ctx := context.Background()

		chunk1 := schema.AssistantMessage("Hello ", nil)
		chunk2 := schema.AssistantMessage("World", nil)

		mockModel := &mockStreamingModel{
			chunks: []*schema.Message{chunk1, chunk2},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       mockModel,
			Handlers:    []ChatModelAgentMiddleware{NewEventSenderModelWrapper()},
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

		assert.Equal(t, 1, len(streamingEvents), "Should have exactly one streaming event (no duplicate from default event sender)")
	})

	t.Run("EventSenderAfterUserMiddlewareByDefault", func(t *testing.T) {
		ctx := context.Background()

		mockModel := &mockStreamingModel{
			chunks: []*schema.Message{
				schema.AssistantMessage("Original", nil),
			},
		}

		modifiedContent := "Modified"
		contentModifyingHandler := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
				return &contentModifyingModelWrapper{inner: m, newContent: modifiedContent}
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       mockModel,
			Handlers:    []ChatModelAgentMiddleware{contentModifyingHandler},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: false,
		})
		iter := r.Run(ctx, []Message{schema.UserMessage("test")})

		var assistantEvents []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Role == schema.Assistant {
				assistantEvents = append(assistantEvents, event)
			}
		}

		assert.GreaterOrEqual(t, len(assistantEvents), 1, "Should have at least one assistant event")
		if len(assistantEvents) > 0 {
			msg := assistantEvents[0].Output.MessageOutput.Message
			assert.Equal(t, modifiedContent, msg.Content, "Event should contain modified content from user middleware")
		}
	})

	t.Run("EventSenderInnermostGetsOriginalOutput", func(t *testing.T) {
		ctx := context.Background()

		originalContent := "Original"
		mockModel := &mockStreamingModel{
			chunks: []*schema.Message{
				schema.AssistantMessage(originalContent, nil),
			},
		}

		modifiedContent := "Modified"
		contentModifyingHandler := &testModelWrapperHandler{
			BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
			fn: func(_ context.Context, m model.BaseChatModel, _ *ModelContext) model.BaseChatModel {
				return &contentModifyingModelWrapper{inner: m, newContent: modifiedContent}
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent",
			Model:       mockModel,
			Handlers: []ChatModelAgentMiddleware{
				contentModifyingHandler,
				NewEventSenderModelWrapper(),
			},
		})
		assert.NoError(t, err)

		r := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: false,
		})
		iter := r.Run(ctx, []Message{schema.UserMessage("test")})

		var assistantEvents []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Role == schema.Assistant {
				assistantEvents = append(assistantEvents, event)
			}
		}

		assert.GreaterOrEqual(t, len(assistantEvents), 1, "Should have at least one assistant event")
		if len(assistantEvents) > 0 {
			msg := assistantEvents[0].Output.MessageOutput.Message
			assert.Equal(t, originalContent, msg.Content, "Event should contain original content (EventSenderModelWrapper is innermost)")
		}
	})
}

type contentModifyingModelWrapper struct {
	inner      model.BaseChatModel
	newContent string
}

func (m *contentModifyingModelWrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	result, err := m.inner.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	result.Content = m.newContent
	return result, nil
}

func (m *contentModifyingModelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	result, err := schema.ConcatMessageStream(stream)
	if err != nil {
		return nil, err
	}
	result.Content = m.newContent
	return schema.StreamReaderFromArray([]*schema.Message{result}), nil
}

type mockToolCallingModel struct {
	mu            sync.Mutex
	generateCalls int
	toolCallName  string
}

func (m *mockToolCallingModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.generateCalls++
	calls := m.generateCalls
	m.mu.Unlock()
	if calls == 1 {
		return schema.AssistantMessage("calling tool", []schema.ToolCall{
			{ID: "tc-1", Function: schema.FunctionCall{Name: m.toolCallName, Arguments: `{"input":"test"}`}},
		}), nil
	}
	return schema.AssistantMessage("done", nil), nil
}

func (m *mockToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *mockToolCallingModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type invokableTestTool struct {
	name   string
	result string
}

func (t *invokableTestTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "test tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Desc: "input", Required: true, Type: schema.String},
		}),
	}, nil
}

func (t *invokableTestTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return t.result, nil
}

type streamableTestTool struct {
	name   string
	result string
}

func (t *streamableTestTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "test tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Desc: "input", Required: true, Type: schema.String},
		}),
	}, nil
}

func (t *streamableTestTool) StreamableRun(_ context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	return schema.StreamReaderFromArray([]string{t.result}), nil
}

type enhancedInvokableTestTool struct {
	name   string
	result string
}

func (t *enhancedInvokableTestTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "test tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Desc: "input", Required: true, Type: schema.String},
		}),
	}, nil
}

func (t *enhancedInvokableTestTool) InvokableRun(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
	return &schema.ToolResult{
		Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: t.result}},
	}, nil
}

type enhancedStreamableTestTool struct {
	name   string
	result string
}

func (t *enhancedStreamableTestTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "test tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Desc: "input", Required: true, Type: schema.String},
		}),
	}, nil
}

func (t *enhancedStreamableTestTool) StreamableRun(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	return schema.StreamReaderFromArray([]*schema.ToolResult{
		{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: t.result}}},
	}), nil
}

type invokableResultModifier struct {
	*BaseChatModelAgentMiddleware
	modifiedResult string
}

func (h *invokableResultModifier) WrapInvokableToolCall(_ context.Context, endpoint InvokableToolCallEndpoint, _ *ToolContext) (InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		_, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return "", err
		}
		return h.modifiedResult, nil
	}, nil
}

type streamableResultModifier struct {
	*BaseChatModelAgentMiddleware
	modifiedResult string
}

func (h *streamableResultModifier) WrapStreamableToolCall(_ context.Context, endpoint StreamableToolCallEndpoint, _ *ToolContext) (StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		sr, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return nil, err
		}
		sr.Close()
		return schema.StreamReaderFromArray([]string{h.modifiedResult}), nil
	}, nil
}

type enhancedInvokableResultModifier struct {
	*BaseChatModelAgentMiddleware
	modifiedResult string
}

func (h *enhancedInvokableResultModifier) WrapEnhancedInvokableToolCall(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, _ *ToolContext) (EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		_, err := endpoint(ctx, toolArgument, opts...)
		if err != nil {
			return nil, err
		}
		return &schema.ToolResult{
			Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: h.modifiedResult}},
		}, nil
	}, nil
}

type enhancedStreamableResultModifier struct {
	*BaseChatModelAgentMiddleware
	modifiedResult string
}

func (h *enhancedStreamableResultModifier) WrapEnhancedStreamableToolCall(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, _ *ToolContext) (EnhancedStreamableToolCallEndpoint, error) {
	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		sr, err := endpoint(ctx, toolArgument, opts...)
		if err != nil {
			return nil, err
		}
		sr.Close()
		return schema.StreamReaderFromArray([]*schema.ToolResult{
			{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: h.modifiedResult}}},
		}), nil
	}, nil
}

func collectToolEvents(it *AsyncIterator[*AgentEvent]) []*AgentEvent {
	var toolEvents []*AgentEvent
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mo := ev.Output.MessageOutput
		if mo.Message != nil && mo.Message.Role == schema.Tool {
			toolEvents = append(toolEvents, ev)
			continue
		}
		if mo.IsStreaming && mo.Role == schema.Tool && mo.MessageStream != nil {
			toolEvents = append(toolEvents, ev)
		}
	}
	return toolEvents
}

func collectToolContent(events []*AgentEvent) []string {
	var contents []string
	for _, ev := range events {
		mo := ev.Output.MessageOutput
		if !mo.IsStreaming && mo.Message != nil {
			if mo.Message.Content != "" {
				contents = append(contents, mo.Message.Content)
			} else if len(mo.Message.UserInputMultiContent) > 0 {
				for _, part := range mo.Message.UserInputMultiContent {
					if part.Text != "" {
						contents = append(contents, part.Text)
					}
				}
			}
			continue
		}
		if mo.IsStreaming && mo.MessageStream != nil {
			var msgs []*schema.Message
			for {
				msg, err := mo.MessageStream.Recv()
				if err != nil {
					break
				}
				msgs = append(msgs, msg)
			}
			if len(msgs) > 0 {
				concated, err := schema.ConcatMessages(msgs)
				if err == nil {
					if concated.Content != "" {
						contents = append(contents, concated.Content)
					} else if len(concated.UserInputMultiContent) > 0 {
						for _, part := range concated.UserInputMultiContent {
							if part.Text != "" {
								contents = append(contents, part.Text)
							}
						}
					}
				}
			}
		}
	}
	return contents
}

func TestEventSenderToolHandler(t *testing.T) {
	t.Run("Invokable", func(t *testing.T) {
		t.Run("DefaultSendsEvent", func(t *testing.T) {
			ctx := context.Background()
			testTool := &invokableTestTool{name: "test_tool", result: "invokable_output"}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: false})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.Equal(t, 1, len(toolEvents))
			contents := collectToolContent(toolEvents)
			assert.Contains(t, contents, "invokable_output")
		})

		t.Run("UserConfiguredSkipsDefault", func(t *testing.T) {
			ctx := context.Background()
			testTool := &invokableTestTool{name: "test_tool", result: "invokable_output"}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
				Handlers: []ChatModelAgentMiddleware{NewEventSenderToolWrapper()},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: false})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.Equal(t, 1, len(toolEvents))
		})

		t.Run("InnermostGetsOriginalOutput", func(t *testing.T) {
			ctx := context.Background()
			originalResult := "original_invokable_output"
			modifiedResult := "modified_invokable_output"
			testTool := &invokableTestTool{name: "test_tool", result: originalResult}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
				Handlers: []ChatModelAgentMiddleware{
					NewEventSenderToolWrapper(),
					&invokableResultModifier{
						BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
						modifiedResult:              modifiedResult,
					},
				},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: false})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.GreaterOrEqual(t, len(toolEvents), 1)
			contents := collectToolContent(toolEvents)
			assert.Contains(t, contents, originalResult)
		})
	})

	t.Run("Streamable", func(t *testing.T) {
		t.Run("DefaultSendsEvent", func(t *testing.T) {
			ctx := context.Background()
			testTool := &streamableTestTool{name: "test_tool", result: "streamable_output"}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.Equal(t, 1, len(toolEvents))
			contents := collectToolContent(toolEvents)
			assert.Contains(t, contents, "streamable_output")
		})

		t.Run("UserConfiguredSkipsDefault", func(t *testing.T) {
			ctx := context.Background()
			testTool := &streamableTestTool{name: "test_tool", result: "streamable_output"}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
				Handlers: []ChatModelAgentMiddleware{NewEventSenderToolWrapper()},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.Equal(t, 1, len(toolEvents))
		})

		t.Run("InnermostGetsOriginalOutput", func(t *testing.T) {
			ctx := context.Background()
			originalResult := "original_streamable_output"
			modifiedResult := "modified_streamable_output"
			testTool := &streamableTestTool{name: "test_tool", result: originalResult}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
				Handlers: []ChatModelAgentMiddleware{
					NewEventSenderToolWrapper(),
					&streamableResultModifier{
						BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
						modifiedResult:              modifiedResult,
					},
				},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.GreaterOrEqual(t, len(toolEvents), 1)
			contents := collectToolContent(toolEvents)
			assert.Contains(t, contents, originalResult)
		})
	})

	t.Run("EnhancedInvokable", func(t *testing.T) {
		t.Run("DefaultSendsEvent", func(t *testing.T) {
			ctx := context.Background()
			testTool := &enhancedInvokableTestTool{name: "test_tool", result: "enhanced_invokable_output"}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: false})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.Equal(t, 1, len(toolEvents))
			contents := collectToolContent(toolEvents)
			assert.Contains(t, contents, "enhanced_invokable_output")
		})

		t.Run("UserConfiguredSkipsDefault", func(t *testing.T) {
			ctx := context.Background()
			testTool := &enhancedInvokableTestTool{name: "test_tool", result: "enhanced_invokable_output"}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
				Handlers: []ChatModelAgentMiddleware{NewEventSenderToolWrapper()},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: false})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.Equal(t, 1, len(toolEvents))
		})

		t.Run("InnermostGetsOriginalOutput", func(t *testing.T) {
			ctx := context.Background()
			originalResult := "original_enhanced_invokable_output"
			modifiedResult := "modified_enhanced_invokable_output"
			testTool := &enhancedInvokableTestTool{name: "test_tool", result: originalResult}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
				Handlers: []ChatModelAgentMiddleware{
					NewEventSenderToolWrapper(),
					&enhancedInvokableResultModifier{
						BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
						modifiedResult:              modifiedResult,
					},
				},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: false})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.GreaterOrEqual(t, len(toolEvents), 1)
			contents := collectToolContent(toolEvents)
			assert.Contains(t, contents, originalResult)
		})
	})

	t.Run("EnhancedStreamable", func(t *testing.T) {
		t.Run("DefaultSendsEvent", func(t *testing.T) {
			ctx := context.Background()
			testTool := &enhancedStreamableTestTool{name: "test_tool", result: "enhanced_streamable_output"}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.Equal(t, 1, len(toolEvents))
			contents := collectToolContent(toolEvents)
			assert.Contains(t, contents, "enhanced_streamable_output")
		})

		t.Run("UserConfiguredSkipsDefault", func(t *testing.T) {
			ctx := context.Background()
			testTool := &enhancedStreamableTestTool{name: "test_tool", result: "enhanced_streamable_output"}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
				Handlers: []ChatModelAgentMiddleware{NewEventSenderToolWrapper()},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.Equal(t, 1, len(toolEvents))
		})

		t.Run("InnermostGetsOriginalOutput", func(t *testing.T) {
			ctx := context.Background()
			originalResult := "original_enhanced_streamable_output"
			modifiedResult := "modified_enhanced_streamable_output"
			testTool := &enhancedStreamableTestTool{name: "test_tool", result: originalResult}
			mockModel := &mockToolCallingModel{toolCallName: "test_tool"}

			agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "TestAgent",
				Description: "Test agent",
				Model:       mockModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{testTool},
					},
				},
				Handlers: []ChatModelAgentMiddleware{
					NewEventSenderToolWrapper(),
					&enhancedStreamableResultModifier{
						BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
						modifiedResult:              modifiedResult,
					},
				},
			})
			assert.NoError(t, err)

			r := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true})
			it := r.Run(ctx, []Message{schema.UserMessage("test")})

			toolEvents := collectToolEvents(it)
			assert.GreaterOrEqual(t, len(toolEvents), 1)
			contents := collectToolContent(toolEvents)
			assert.Contains(t, contents, originalResult)
		})
	})
}
