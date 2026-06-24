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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"sync"
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
		_, _ = middlewares[0].Invokable(invokableEndpoint)(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		streamableEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{"test"})}, nil
		}
		_, _ = middlewares[1].Streamable(streamableEndpoint)(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		enhancedInvokableEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			return &compose.EnhancedInvokableToolOutput{Result: &schema.ToolResult{}}, nil
		}
		_, _ = middlewares[2].EnhancedInvokable(enhancedInvokableEndpoint)(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

		enhancedStreamableEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return &compose.EnhancedStreamableToolOutput{Result: schema.StreamReaderFromArray([]*schema.ToolResult{{}})}, nil
		}
		_, _ = middlewares[3].EnhancedStreamable(enhancedStreamableEndpoint)(context.Background(), &compose.ToolInput{Name: "test", CallID: "1", Arguments: "{}"})

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
		assert.Equal(t, []string{"handler1-before", "handler2-before", "handler2-after", "handler1-after"}, executionOrder)
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
		assert.Equal(t, []string{"handler1-before", "handler2-before", "handler2-after", "handler1-after"}, executionOrder)
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
					&invokableResultModifier{
						BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
						modifiedResult:               modifiedResult,
					},
					NewEventSenderToolWrapper(),
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
					&streamableResultModifier{
						BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
						modifiedResult:               modifiedResult,
					},
					NewEventSenderToolWrapper(),
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
					&enhancedInvokableResultModifier{
						BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
						modifiedResult:               modifiedResult,
					},
					NewEventSenderToolWrapper(),
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
					&enhancedStreamableResultModifier{
						BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
						modifiedResult:               modifiedResult,
					},
					NewEventSenderToolWrapper(),
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

// mockAgenticToolCallingModel is a model.BaseModel[*schema.AgenticMessage] that
// returns a tool call on the first Generate, then a final answer on the second.
type mockAgenticToolCallingModel struct {
	toolCallName string
	callCount    int32
}

func (m *mockAgenticToolCallingModel) Generate(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
	idx := atomic.AddInt32(&m.callCount, 1)
	if idx == 1 {
		return agenticToolCallMsg(m.toolCallName, "tc-1", `{"input":"test"}`), nil
	}
	return agenticMsg("done"), nil
}

func (m *mockAgenticToolCallingModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	r, w := schema.Pipe[*schema.AgenticMessage](1)
	go func() { defer w.Close(); w.Send(msg, nil) }()
	return r, nil
}

// collectAgenticToolEvents filters tool result events from the agentic iterator.
// Agentic tool results have AgenticRole == AgenticRoleTypeUser and contain
// FunctionToolResult content blocks.
func collectAgenticToolEvents(it *AsyncIterator[*agenticAgentEvent]) []*agenticAgentEvent {
	var toolEvents []*agenticAgentEvent
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mo := ev.Output.MessageOutput
		if mo.AgenticRole == schema.AgenticRoleTypeUser {
			toolEvents = append(toolEvents, ev)
		}
	}
	return toolEvents
}

// collectAgenticToolContent extracts text from agentic tool result events.
func collectAgenticToolContent(events []*agenticAgentEvent) []string {
	var contents []string
	for _, ev := range events {
		mo := ev.Output.MessageOutput
		if !mo.IsStreaming && mo.Message != nil {
			for _, cb := range mo.Message.ContentBlocks {
				if cb.FunctionToolResult != nil {
					for _, b := range cb.FunctionToolResult.Content {
						if b.Text != nil {
							contents = append(contents, b.Text.Text)
						}
					}
				}
			}
			continue
		}
		if mo.IsStreaming && mo.MessageStream != nil {
			for {
				msg, err := mo.MessageStream.Recv()
				if err != nil {
					break
				}
				for _, cb := range msg.ContentBlocks {
					if cb.FunctionToolResult != nil {
						for _, b := range cb.FunctionToolResult.Content {
							if b.Text != nil {
								contents = append(contents, b.Text.Text)
							}
						}
					}
				}
			}
		}
	}
	return contents
}

func newAgenticEventSenderToolWrapper() TypedChatModelAgentMiddleware[*schema.AgenticMessage] {
	return &typedEventSenderToolWrapper[*schema.AgenticMessage]{
		TypedBaseChatModelAgentMiddleware: &TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]{},
	}
}

// TestAgenticEventSenderToolHandler exercises the *schema.AgenticMessage branches
// in typedToolInvokeEvent, typedToolStreamEvent, typedToolEnhancedInvokeEvent,
// typedToolEnhancedStreamEvent, plus the helpers textToFunctionToolResultBlocks,
// toolResultToBlocks, and derefString.
func TestAgenticEventSenderToolHandler(t *testing.T) {
	t.Run("Invokable", func(t *testing.T) {
		ctx := context.Background()
		testTool := &invokableTestTool{name: "test_tool", result: "invokable_output"}
		mdl := &mockAgenticToolCallingModel{toolCallName: "test_tool"}

		agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "TestAgent",
			Description: "test",
			Model:       mdl,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}},
			},
			Handlers: []TypedChatModelAgentMiddleware[*schema.AgenticMessage]{newAgenticEventSenderToolWrapper()},
		})
		require.NoError(t, err)

		r := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: false})
		it := r.Query(ctx, "test")

		toolEvents := collectAgenticToolEvents(it)
		assert.Equal(t, 1, len(toolEvents))
		contents := collectAgenticToolContent(toolEvents)
		assert.Contains(t, contents, "invokable_output")
	})

	t.Run("Streamable", func(t *testing.T) {
		ctx := context.Background()
		testTool := &streamableTestTool{name: "test_tool", result: "streamable_output"}
		mdl := &mockAgenticToolCallingModel{toolCallName: "test_tool"}

		agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "TestAgent",
			Description: "test",
			Model:       mdl,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}},
			},
			Handlers: []TypedChatModelAgentMiddleware[*schema.AgenticMessage]{newAgenticEventSenderToolWrapper()},
		})
		require.NoError(t, err)

		r := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: true})
		it := r.Query(ctx, "test")

		toolEvents := collectAgenticToolEvents(it)
		assert.Equal(t, 1, len(toolEvents))
		contents := collectAgenticToolContent(toolEvents)
		assert.Contains(t, contents, "streamable_output")
	})

	t.Run("EnhancedInvokable", func(t *testing.T) {
		ctx := context.Background()
		testTool := &enhancedInvokableTestTool{name: "test_tool", result: "enhanced_output"}
		mdl := &mockAgenticToolCallingModel{toolCallName: "test_tool"}

		agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "TestAgent",
			Description: "test",
			Model:       mdl,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}},
			},
			Handlers: []TypedChatModelAgentMiddleware[*schema.AgenticMessage]{newAgenticEventSenderToolWrapper()},
		})
		require.NoError(t, err)

		r := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: false})
		it := r.Query(ctx, "test")

		toolEvents := collectAgenticToolEvents(it)
		assert.Equal(t, 1, len(toolEvents))
		contents := collectAgenticToolContent(toolEvents)
		assert.Contains(t, contents, "enhanced_output")
	})

	t.Run("EnhancedStreamable", func(t *testing.T) {
		ctx := context.Background()
		testTool := &enhancedStreamableTestTool{name: "test_tool", result: "enhanced_stream_output"}
		mdl := &mockAgenticToolCallingModel{toolCallName: "test_tool"}

		agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "TestAgent",
			Description: "test",
			Model:       mdl,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}},
			},
			Handlers: []TypedChatModelAgentMiddleware[*schema.AgenticMessage]{newAgenticEventSenderToolWrapper()},
		})
		require.NoError(t, err)

		r := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: true})
		it := r.Query(ctx, "test")

		toolEvents := collectAgenticToolEvents(it)
		assert.Equal(t, 1, len(toolEvents))
		contents := collectAgenticToolContent(toolEvents)
		assert.Contains(t, contents, "enhanced_stream_output")
	})

	t.Run("EnhancedInvokableMultimodal", func(t *testing.T) {
		ctx := context.Background()
		imgURL := "https://example.com/img.png"
		testTool := &multimodalEnhancedInvokableTestTool{
			name: "test_tool",
			result: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: "caption"},
					{Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{URL: &imgURL}}},
				},
			},
		}
		mdl := &mockAgenticToolCallingModel{toolCallName: "test_tool"}

		agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "TestAgent",
			Description: "test",
			Model:       mdl,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}},
			},
			Handlers: []TypedChatModelAgentMiddleware[*schema.AgenticMessage]{newAgenticEventSenderToolWrapper()},
		})
		require.NoError(t, err)

		r := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: false})
		it := r.Query(ctx, "test")

		toolEvents := collectAgenticToolEvents(it)
		require.Equal(t, 1, len(toolEvents))

		// Verify multimodal content
		msg := toolEvents[0].Output.MessageOutput.Message
		require.NotNil(t, msg)
		require.Len(t, msg.ContentBlocks, 1)
		ftr := msg.ContentBlocks[0].FunctionToolResult
		require.NotNil(t, ftr)
		require.Len(t, ftr.Content, 2)
		assert.Equal(t, "caption", ftr.Content[0].Text.Text)
		assert.Equal(t, "https://example.com/img.png", ftr.Content[1].Image.URL)
	})

	t.Run("EnhancedStreamableMultimodal", func(t *testing.T) {
		ctx := context.Background()
		audioURL := "https://example.com/audio.mp3"
		testTool := &multimodalEnhancedStreamableTestTool{
			name: "test_tool",
			result: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: "transcript"},
					{Type: schema.ToolPartTypeAudio, Audio: &schema.ToolOutputAudio{MessagePartCommon: schema.MessagePartCommon{URL: &audioURL}}},
				},
			},
		}
		mdl := &mockAgenticToolCallingModel{toolCallName: "test_tool"}

		agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "TestAgent",
			Description: "test",
			Model:       mdl,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}},
			},
			Handlers: []TypedChatModelAgentMiddleware[*schema.AgenticMessage]{newAgenticEventSenderToolWrapper()},
		})
		require.NoError(t, err)

		r := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: true})
		it := r.Query(ctx, "test")

		toolEvents := collectAgenticToolEvents(it)
		require.Equal(t, 1, len(toolEvents))

		// Drain the stream and verify multimodal content
		mo := toolEvents[0].Output.MessageOutput
		require.True(t, mo.IsStreaming)
		var allBlocks []*schema.FunctionToolResultContentBlock
		for {
			msg, err := mo.MessageStream.Recv()
			if err != nil {
				break
			}
			for _, cb := range msg.ContentBlocks {
				if cb.FunctionToolResult != nil {
					allBlocks = append(allBlocks, cb.FunctionToolResult.Content...)
				}
			}
		}
		require.Len(t, allBlocks, 2)
		assert.Equal(t, "transcript", allBlocks[0].Text.Text)
		assert.Equal(t, "https://example.com/audio.mp3", allBlocks[1].Audio.URL)
	})
}

func TestTypedToolStreamEventAgenticMessageSetsStreamingMeta(t *testing.T) {
	event := typedToolStreamEvent[*schema.AgenticMessage](
		"call_1",
		"execute",
		"msg_1",
		schema.StreamReaderFromArray([]string{"first\n", "second\n"}),
	)
	require.NotNil(t, event)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)
	require.True(t, event.Output.MessageOutput.IsStreaming)
	require.NotNil(t, event.Output.MessageOutput.MessageStream)

	first, err := event.Output.MessageOutput.MessageStream.Recv()
	require.NoError(t, err)
	require.Len(t, first.ContentBlocks, 1)
	assert.Equal(t, &schema.StreamingMeta{Index: 0}, first.ContentBlocks[0].StreamingMeta)

	second, err := event.Output.MessageOutput.MessageStream.Recv()
	require.NoError(t, err)
	require.Len(t, second.ContentBlocks, 1)
	assert.Equal(t, &schema.StreamingMeta{Index: 0}, second.ContentBlocks[0].StreamingMeta)

	result, err := schema.ConcatAgenticMessages([]*schema.AgenticMessage{first, second})
	require.NoError(t, err)
	require.Len(t, result.ContentBlocks, 1)
	assert.Nil(t, result.ContentBlocks[0].StreamingMeta)
	require.NotNil(t, result.ContentBlocks[0].FunctionToolResult)
	require.Len(t, result.ContentBlocks[0].FunctionToolResult.Content, 1)
	assert.Equal(t, "first\nsecond\n", result.ContentBlocks[0].FunctionToolResult.Content[0].Text.Text)
}

func TestTypedToolEnhancedStreamEventAgenticMessageSetsStreamingMeta(t *testing.T) {
	event := typedToolEnhancedStreamEvent[*schema.AgenticMessage](
		"call_1",
		"execute",
		"msg_1",
		schema.StreamReaderFromArray([]*schema.ToolResult{
			{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "first\n"}}},
			{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "second\n"}}},
		}),
	)
	require.NotNil(t, event)
	require.NotNil(t, event.Output)
	require.NotNil(t, event.Output.MessageOutput)
	require.True(t, event.Output.MessageOutput.IsStreaming)
	require.NotNil(t, event.Output.MessageOutput.MessageStream)

	first, err := event.Output.MessageOutput.MessageStream.Recv()
	require.NoError(t, err)
	require.Len(t, first.ContentBlocks, 1)
	assert.Equal(t, &schema.StreamingMeta{Index: 0}, first.ContentBlocks[0].StreamingMeta)

	second, err := event.Output.MessageOutput.MessageStream.Recv()
	require.NoError(t, err)
	require.Len(t, second.ContentBlocks, 1)
	assert.Equal(t, &schema.StreamingMeta{Index: 0}, second.ContentBlocks[0].StreamingMeta)

	result, err := schema.ConcatAgenticMessages([]*schema.AgenticMessage{first, second})
	require.NoError(t, err)
	require.Len(t, result.ContentBlocks, 1)
	assert.Nil(t, result.ContentBlocks[0].StreamingMeta)
	require.NotNil(t, result.ContentBlocks[0].FunctionToolResult)
	require.Len(t, result.ContentBlocks[0].FunctionToolResult.Content, 1)
	assert.Equal(t, "first\nsecond\n", result.ContentBlocks[0].FunctionToolResult.Content[0].Text.Text)
}

// multimodalEnhancedInvokableTestTool returns a pre-built multimodal ToolResult.
type multimodalEnhancedInvokableTestTool struct {
	name   string
	result *schema.ToolResult
}

func (t *multimodalEnhancedInvokableTestTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name, Desc: "multimodal test tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Desc: "input", Required: true, Type: schema.String},
		}),
	}, nil
}

func (t *multimodalEnhancedInvokableTestTool) InvokableRun(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
	return t.result, nil
}

// multimodalEnhancedStreamableTestTool returns a pre-built multimodal ToolResult as a stream.
type multimodalEnhancedStreamableTestTool struct {
	name   string
	result *schema.ToolResult
}

func (t *multimodalEnhancedStreamableTestTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name, Desc: "multimodal streaming test tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Desc: "input", Required: true, Type: schema.String},
		}),
	}, nil
}

func (t *multimodalEnhancedStreamableTestTool) StreamableRun(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	return schema.StreamReaderFromArray([]*schema.ToolResult{t.result}), nil
}

func Test_functionToolResultAgenticMessage(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		blocks := []*schema.FunctionToolResultContentBlock{
			{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "result_str"}},
		}
		msg := functionToolResultAgenticMessage("call_1", "tool_name", blocks)
		assert.Equal(t, schema.AgenticRoleTypeUser, msg.Role)
		assert.Len(t, msg.ContentBlocks, 1)
		assert.Equal(t, schema.ContentBlockTypeFunctionToolResult, msg.ContentBlocks[0].Type)
		ftr := msg.ContentBlocks[0].FunctionToolResult
		assert.Equal(t, "call_1", ftr.CallID)
		assert.Equal(t, "tool_name", ftr.Name)
		assert.Len(t, ftr.Content, 1)
		assert.Equal(t, "result_str", ftr.Content[0].Text.Text)
	})

	t.Run("multimodal", func(t *testing.T) {
		blocks := []*schema.FunctionToolResultContentBlock{
			{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "description"}},
			{Type: schema.FunctionToolResultContentBlockTypeImage, Image: &schema.UserInputImage{URL: "https://example.com/img.png"}},
		}
		msg := functionToolResultAgenticMessage("call_2", "vision_tool", blocks)
		assert.Equal(t, schema.AgenticRoleTypeUser, msg.Role)
		ftr := msg.ContentBlocks[0].FunctionToolResult
		assert.Equal(t, "call_2", ftr.CallID)
		assert.Equal(t, "vision_tool", ftr.Name)
		assert.Len(t, ftr.Content, 2)
		assert.Equal(t, "description", ftr.Content[0].Text.Text)
		assert.Equal(t, "https://example.com/img.png", ftr.Content[1].Image.URL)
	})
}

func TestTypedToolEnhancedEventAgenticToolSearchResult(t *testing.T) {
	result := &schema.ToolResult{Parts: []schema.ToolOutputPart{
		{
			Type: schema.ToolPartTypeToolSearchResult,
			ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{
				{Name: "dynamic_tool", Desc: "dynamic tool"},
			}},
		},
	}}

	t.Run("invoke", func(t *testing.T) {
		event, err := typedToolEnhancedInvokeEvent[*schema.AgenticMessage]("call_1", "tool_search", "msg_1", result)
		require.NoError(t, err)
		require.NotNil(t, event)
		require.NotNil(t, event.Output)
		require.NotNil(t, event.Output.MessageOutput)

		msg := event.Output.MessageOutput.Message
		require.NotNil(t, msg)
		require.Len(t, msg.ContentBlocks, 1)
		block := msg.ContentBlocks[0]
		assert.Equal(t, schema.ContentBlockTypeToolSearchResult, block.Type)
		require.NotNil(t, block.ToolSearchFunctionToolResult)
		assert.Equal(t, "call_1", block.ToolSearchFunctionToolResult.CallID)
		assert.Equal(t, "tool_search", block.ToolSearchFunctionToolResult.Name)
		require.NotNil(t, block.ToolSearchFunctionToolResult.Result)
		require.Len(t, block.ToolSearchFunctionToolResult.Result.Tools, 1)
		assert.Equal(t, "dynamic_tool", block.ToolSearchFunctionToolResult.Result.Tools[0].Name)
	})

	t.Run("stream", func(t *testing.T) {
		event := typedToolEnhancedStreamEvent[*schema.AgenticMessage](
			"call_2",
			"tool_search",
			"msg_2",
			schema.StreamReaderFromArray([]*schema.ToolResult{result}),
		)
		require.NotNil(t, event)
		require.NotNil(t, event.Output)
		require.NotNil(t, event.Output.MessageOutput)

		msg, err := event.Output.MessageOutput.MessageStream.Recv()
		require.NoError(t, err)
		require.NotNil(t, msg)
		require.Len(t, msg.ContentBlocks, 1)
		block := msg.ContentBlocks[0]
		assert.Equal(t, schema.ContentBlockTypeToolSearchResult, block.Type)
		assert.Equal(t, &schema.StreamingMeta{Index: 0}, block.StreamingMeta)
		require.NotNil(t, block.ToolSearchFunctionToolResult)
		assert.Equal(t, "call_2", block.ToolSearchFunctionToolResult.CallID)
		assert.Equal(t, "tool_search", block.ToolSearchFunctionToolResult.Name)
		require.NotNil(t, block.ToolSearchFunctionToolResult.Result)
		require.Len(t, block.ToolSearchFunctionToolResult.Result.Tools, 1)
		assert.Equal(t, "dynamic_tool", block.ToolSearchFunctionToolResult.Result.Tools[0].Name)
	})
}

func TestExtractToolIdentifiersToolSearchResult(t *testing.T) {
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.ToolSearchFunctionToolResult{
				CallID: "call_1",
				Name:   "tool_search",
				Result: &schema.ToolSearchResult{},
			}),
		},
	}

	toolName, callID := extractToolIdentifiers(msg)
	assert.Equal(t, "tool_search", toolName)
	assert.Equal(t, "call_1", callID)
}

func TestBuildModelWrappers_FailoverProxyInner(t *testing.T) {
	base := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("ok", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
		},
	}

	failoverCfg := &ModelFailoverConfig[*schema.Message]{
		MaxRetries:     0,
		ShouldFailover: func(context.Context, *schema.Message, error) bool { return false },
		GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
			return base, nil, nil
		},
	}

	wrapped := buildModelWrappers[*schema.Message](base, &modelWrapperConfig{
		failoverConfig: failoverCfg,
	})

	smw, ok := wrapped.(*stateModelWrapper)
	require.True(t, ok)
	_, ok = smw.inner.(*failoverProxyModel)
	require.True(t, ok)
	require.Same(t, base, smw.original)
	require.Same(t, failoverCfg, smw.modelFailoverConfig)
}

func TestStateModelWrapper_Generate_WithFailover(t *testing.T) {
	wantErr := errors.New("first failed")
	var shouldCalls int32
	var m1Calls int32
	var m2Calls int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return schema.AssistantMessage("partial", nil), wantErr
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}
	m2 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("ok", nil), nil
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		},
	}

	failoverCfg := &ModelFailoverConfig[*schema.Message]{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, out *schema.Message, err error) bool {
			atomic.AddInt32(&shouldCalls, 1)
			require.ErrorIs(t, err, wantErr)
			require.NotNil(t, out)
			require.Equal(t, "partial", out.Content)
			return true
		},
		GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
			require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
		failoverConfig: failoverCfg,
	})

	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	got, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "ok", got.Content)
	require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
}

func TestStateModelWrapper_Stream_WithFailover(t *testing.T) {
	streamErr := errors.New("mid error")
	var shouldCalls int32
	var m1Calls int32
	var m2Calls int32

	m1 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unused")
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("p1", nil),
				schema.AssistantMessage("p2", nil),
			}, streamErr), nil
		},
	}
	m2 := &fakeChatModel{
		callbacksEnabled: true,
		generate: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unused")
		},
		stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final", nil)}), nil
		},
	}

	failoverCfg := &ModelFailoverConfig[*schema.Message]{
		MaxRetries: 1,
		ShouldFailover: func(_ context.Context, out *schema.Message, err error) bool {
			atomic.AddInt32(&shouldCalls, 1)
			require.ErrorIs(t, err, streamErr)
			require.NotNil(t, out)
			require.Equal(t, "p1p2", out.Content)
			return true
		},
		GetFailoverModel: func(_ context.Context, failoverCtx *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
			require.Equal(t, uint(1), failoverCtx.FailoverAttempt)
			return m2, nil, nil
		},
	}

	wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
		failoverConfig: failoverCfg,
	})

	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		failoverLastSuccessModel: m1,
	})
	sr, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	msgs, err := drainMessageStream(sr)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "final", msgs[0].Content)
	require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&shouldCalls))
}

func TestFailoverAcceptsAgenticAgent(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("ok"), nil
		},
	}

	fallbackModel := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("fallback"), nil
		},
	}

	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "FailoverAgent",
		Description: "Agent with failover config",
		Model:       m,
		ModelFailoverConfig: &ModelFailoverConfig[*schema.AgenticMessage]{
			MaxRetries: 1,
			ShouldFailover: func(ctx context.Context, outputMessage *schema.AgenticMessage, outputErr error) bool {
				return true
			},
			GetFailoverModel: func(ctx context.Context, failoverCtx *FailoverContext[*schema.AgenticMessage]) (model.BaseModel[*schema.AgenticMessage], []*schema.AgenticMessage, error) {
				return fallbackModel, nil, nil
			},
		},
	})
	require.NoError(t, err)
	assert.NotNil(t, agent)
}

// approvalInfoSpan and approvalResultSpan are isolated copies for use in this
// test file so we don't conflict with the prebuilt/integration_test.go types
// (which live in a different package anyway).
type approvalInfoSpan struct {
	ToolName        string
	ArgumentsInJSON string
	ToolCallID      string
}

type approvalResultSpan struct {
	Approved bool
}

func init() {
	schema.Register[*approvalInfoSpan]()
	schema.Register[*approvalResultSpan]()
}

// approvableSpanTool is an invokable tool that interrupts on first invocation
// and runs to completion on resume after approval.
type approvableSpanTool struct {
	name string
}

func (t *approvableSpanTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "approvable span tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input"},
		}),
	}, nil
}

func (t *approvableSpanTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	wasInterrupted, _, savedArgs := tool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return "", tool.StatefulInterrupt(ctx, &approvalInfoSpan{
			ToolName:        t.name,
			ArgumentsInJSON: argumentsInJSON,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, argumentsInJSON)
	}
	isResumeTarget, hasData, data := tool.GetResumeContext[*approvalResultSpan](ctx)
	if !isResumeTarget || !hasData {
		return "", tool.StatefulInterrupt(ctx, &approvalInfoSpan{
			ToolName:        t.name,
			ArgumentsInJSON: savedArgs,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, savedArgs)
	}
	if data.Approved {
		return fmt.Sprintf("Tool '%s' executed with args: %s", t.name, savedArgs), nil
	}
	return fmt.Sprintf("Tool '%s' rejected", t.name), nil
}

// approvableStreamableSpanTool: streamable variant. Interrupts on first
// invocation by returning a *core.InterruptSignal error before any stream
// chunk is produced; runs to completion on resume.
type approvableStreamableSpanTool struct {
	name string
}

func (t *approvableStreamableSpanTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "approvable streamable span tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input"},
		}),
	}, nil
}

func (t *approvableStreamableSpanTool) StreamableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	wasInterrupted, _, savedArgs := tool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return nil, tool.StatefulInterrupt(ctx, &approvalInfoSpan{
			ToolName:        t.name,
			ArgumentsInJSON: argumentsInJSON,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, argumentsInJSON)
	}
	isResumeTarget, hasData, data := tool.GetResumeContext[*approvalResultSpan](ctx)
	if !isResumeTarget || !hasData {
		return nil, tool.StatefulInterrupt(ctx, &approvalInfoSpan{
			ToolName:        t.name,
			ArgumentsInJSON: savedArgs,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, savedArgs)
	}
	if data.Approved {
		return schema.StreamReaderFromArray([]string{
			fmt.Sprintf("Tool '%s' streamed with args: %s", t.name, savedArgs),
		}), nil
	}
	return schema.StreamReaderFromArray([]string{"rejected"}), nil
}

// alwaysErrorTool errors out hard (non-interrupt) on every invocation.
type alwaysErrorTool struct {
	name string
}

func (t *alwaysErrorTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "always errors",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input"},
		}),
	}, nil
}

func (t *alwaysErrorTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "", errors.New("hard tool failure")
}

// memCheckpointStore is a minimal in-memory CheckPointStore for these tests.
type memCheckpointStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemCheckpointStore() *memCheckpointStore {
	return &memCheckpointStore{data: make(map[string][]byte)}
}

func (s *memCheckpointStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *memCheckpointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	return v, ok, nil
}

// scriptedToolCallingModel is a controllable mock model: each call returns the
// next scripted message.
type scriptedToolCallingModel struct {
	mu       sync.Mutex
	messages []*schema.Message
	pos      int
}

func (m *scriptedToolCallingModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pos >= len(m.messages) {
		return schema.AssistantMessage("done", nil), nil
	}
	msg := m.messages[m.pos]
	m.pos++
	return msg, nil
}

func (m *scriptedToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *scriptedToolCallingModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

// drainAndCollectSpans collects tool span events emitted during iter draining.
func drainAndCollectSpans(t *testing.T, iter *AsyncIterator[*AgentEvent]) (starts, ends []*SessionEvent[*schema.Message], interrupted bool) {
	t.Helper()
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interrupted = true
		}
		if ev.SessionEventVariant.GetEvent() == nil || ev.SessionEventVariant.GetEvent().Span == nil {
			continue
		}
		switch ev.SessionEventVariant.GetEvent().Kind {
		case SessionEventSpanToolCallStart:
			starts = append(starts, ev.SessionEventVariant.GetEvent())
		case SessionEventSpanToolCallEnd:
			ends = append(ends, ev.SessionEventVariant.GetEvent())
		}
	}
	return
}

// setupApprovableSpanAgent constructs a ChatModelAgent with a scripted
// tool-calling model and an in-memory checkpoint store, ready for span tests
// that exercise interrupt/resume.
func setupApprovableSpanAgent(t *testing.T, name string, tools []tool.BaseTool, scriptedAssistant []*schema.Message) (*TypedChatModelAgent[*schema.Message], *memCheckpointStore) {
	t.Helper()
	ctx := context.Background()
	mdl := &scriptedToolCallingModel{messages: scriptedAssistant}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        name,
		Description: "test",
		Model:       mdl,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	require.NoError(t, err)
	store := newMemCheckpointStore()
	return agent, store
}

func TestToolSpan_PermissionInterruptDefersEndSpan(t *testing.T) {
	ctx := context.Background()
	tl := &approvableSpanTool{name: "approve_me"}

	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call_1", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
	}
	agent, store := setupApprovableSpanAgent(t, "agent1", []tool.BaseTool{tl}, scripted)
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})

	checkpointID := "ckpt-1"
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())
	starts, ends, interrupted := drainAndCollectSpans(t, iter)

	assert.True(t, interrupted, "expected an interrupt event from the approvable tool")
	require.Len(t, starts, 1, "exactly one tool_call_start span should be emitted on the interrupted run")
	assert.Empty(t, ends, "no tool_call_end span should be emitted on the interrupted run")
	assert.Equal(t, "call_1", starts[0].Span.Tool.ToolUseID)
	assert.NotEmpty(t, starts[0].Span.Tool.AssistantMessageEventID, "start span must carry assistant message event ID")
	assert.NotEmpty(t, starts[0].Span.ParentSpanID, "start span must carry parent (model) span ID")
}

// runInterruptResumeAndCollectSpans drives a single-tool interrupt+approve
// scenario and returns the start span emitted on the original run plus the
// end span emitted on the resumed run. Used by both the resume happy-path
// test and the dedicated "parent IDs survive resume" assertion.
func runInterruptResumeAndCollectSpans(t *testing.T, agent *TypedChatModelAgent[*schema.Message], store *memCheckpointStore, checkpointID string) (startSpan, endSpan *SessionEvent[*schema.Message]) {
	t.Helper()
	ctx := context.Background()
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter1 := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())

	var (
		starts1      []*SessionEvent[*schema.Message]
		ends1        []*SessionEvent[*schema.Message]
		interruptEvt *AgentEvent
	)
	for {
		ev, ok := iter1.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvt = ev
		}
		if ev.SessionEventVariant.GetEvent() != nil && ev.SessionEventVariant.GetEvent().Span != nil {
			switch ev.SessionEventVariant.GetEvent().Kind {
			case SessionEventSpanToolCallStart:
				starts1 = append(starts1, ev.SessionEventVariant.GetEvent())
			case SessionEventSpanToolCallEnd:
				ends1 = append(ends1, ev.SessionEventVariant.GetEvent())
			}
		}
	}
	require.NotNil(t, interruptEvt)
	require.Len(t, starts1, 1)
	require.Empty(t, ends1)

	var toolInterruptID string
	for _, ictx := range interruptEvt.Action.Interrupted.InterruptContexts {
		if ictx.IsRootCause {
			toolInterruptID = ictx.ID
			break
		}
	}
	require.NotEmpty(t, toolInterruptID)

	resumeIter, err := runner.ResumeWithParams(ctx, checkpointID, &ResumeParams{
		Targets: map[string]any{toolInterruptID: &approvalResultSpan{Approved: true}},
	}, WithTimelineEvents())
	require.NoError(t, err)
	_, ends2, _ := drainAndCollectSpans(t, resumeIter)
	require.Len(t, ends2, 1)
	return starts1[0], ends2[0]
}

func TestToolSpan_PermissionResumeEmitsEndSpan(t *testing.T) {
	tl := &approvableSpanTool{name: "approve_me"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call_resume", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "agent_resume", []tool.BaseTool{tl}, scripted)
	startSpan, endSpan := runInterruptResumeAndCollectSpans(t, agent, store, "ckpt-resume")

	assert.Equal(t, startSpan.Span.SpanID, endSpan.Span.SpanID, "end span must reuse the original SpanID")
	assert.Equal(t, startSpan.EventID, endSpan.Span.Tool.ToolCallStartEventID, "end's ToolCallStartEventID must match start's EventID")
	assert.Equal(t, "ok", endSpan.Span.Status)
	assert.NotEmpty(t, endSpan.Span.Tool.ToolResultMessageEventID)
}

// TestToolSpan_ResumeUsesOriginalTurnParentIDs (plan §4.5.1 #3) verifies that
// the resumed end span's ParentSpanID and AssistantMessageEventID match the
// original turn's model span and assistant message — confirming the in-flight
// span snapshot survived the checkpoint round-trip.
func TestToolSpan_ResumeUsesOriginalTurnParentIDs(t *testing.T) {
	tl := &approvableSpanTool{name: "approve_me"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call_parents", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "agent_parents", []tool.BaseTool{tl}, scripted)
	startSpan, endSpan := runInterruptResumeAndCollectSpans(t, agent, store, "ckpt-parents")

	require.NotEmpty(t, startSpan.Span.ParentSpanID, "start span carries a non-empty parent (model) span ID")
	require.NotEmpty(t, startSpan.Span.Tool.AssistantMessageEventID, "start span carries a non-empty assistant message event ID")
	assert.Equal(t, startSpan.Span.ParentSpanID, endSpan.Span.ParentSpanID, "ParentSpanID survives resume via the in-flight snapshot")
	assert.Equal(t, startSpan.Span.Tool.AssistantMessageEventID, endSpan.Span.Tool.AssistantMessageEventID, "AssistantMessageEventID survives resume via the in-flight snapshot")
}

func TestToolSpan_HardErrorOnFirstRunStillEmitsEnd(t *testing.T) {
	ctx := context.Background()
	tl := &alwaysErrorTool{name: "boom"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "err_call", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
	}
	agent, store := setupApprovableSpanAgent(t, "err_agent", []tool.BaseTool{tl}, scripted)
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID("ckpt-err"), WithTimelineEvents())
	starts, ends, _ := drainAndCollectSpans(t, iter)

	require.Len(t, starts, 1)
	require.Len(t, ends, 1)
	assert.Equal(t, starts[0].Span.SpanID, ends[0].Span.SpanID, "end span shares SpanID with start span")
	assert.Equal(t, "error", ends[0].Span.Status)
}

func TestToolSpan_StreamableInterruptDefersEnd(t *testing.T) {
	ctx := context.Background()
	tl := &approvableStreamableSpanTool{name: "stream_approve_me"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "stream_call", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "stream_agent", []tool.BaseTool{tl}, scripted)
	runner1 := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	checkpointID := "ckpt-stream"
	iter1 := runner1.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())
	var interruptEvt *AgentEvent
	starts1, ends1 := []*SessionEvent[*schema.Message]{}, []*SessionEvent[*schema.Message]{}
	for {
		ev, ok := iter1.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvt = ev
		}
		if ev.SessionEventVariant.GetEvent() != nil && ev.SessionEventVariant.GetEvent().Span != nil {
			switch ev.SessionEventVariant.GetEvent().Kind {
			case SessionEventSpanToolCallStart:
				starts1 = append(starts1, ev.SessionEventVariant.GetEvent())
			case SessionEventSpanToolCallEnd:
				ends1 = append(ends1, ev.SessionEventVariant.GetEvent())
			}
		}
	}
	require.NotNil(t, interruptEvt)
	require.Len(t, starts1, 1, "one start span on interrupted streamable run")
	assert.Empty(t, ends1, "no end span on interrupted streamable run")
	startSpanID := starts1[0].Span.SpanID

	var toolInterruptID string
	for _, ictx := range interruptEvt.Action.Interrupted.InterruptContexts {
		if ictx.IsRootCause {
			toolInterruptID = ictx.ID
			break
		}
	}
	require.NotEmpty(t, toolInterruptID)

	resumeIter, err := runner1.ResumeWithParams(ctx, checkpointID, &ResumeParams{
		Targets: map[string]any{toolInterruptID: &approvalResultSpan{Approved: true}},
	}, WithTimelineEvents())
	require.NoError(t, err)
	starts2, ends2, _ := drainAndCollectSpans(t, resumeIter)
	assert.Empty(t, starts2, "no new start span on streamable resume")
	require.Len(t, ends2, 1, "one end span on streamable resume")
	assert.Equal(t, startSpanID, ends2[0].Span.SpanID, "end span shares SpanID with start span across resume")
	assert.Equal(t, "ok", ends2[0].Span.Status)
}

func TestTypedState_ToolSpansInFlightGobRoundTrip(t *testing.T) {
	original := &typedState[*schema.Message]{
		Messages:                       []*schema.Message{schema.UserMessage("hello")},
		CurrentModelSpanID:             "model-span-1",
		CurrentAssistantMessageEventID: "asst-event-1",
		ToolSpansInFlight: map[string]*toolSpanInFlight{
			"call_a": {
				SpanID:                  "span-a",
				StartEventID:            "start-event-a",
				StartedAt:               time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
				ParentSpanID:            "model-span-1",
				AssistantMessageEventID: "asst-event-1",
			},
			"call_b": {
				SpanID:                  "span-b",
				StartEventID:            "start-event-b",
				StartedAt:               time.Date(2026, 5, 26, 12, 0, 1, 0, time.UTC),
				ParentSpanID:            "model-span-1",
				AssistantMessageEventID: "asst-event-1",
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(original))

	decoded := &typedState[*schema.Message]{}
	require.NoError(t, gob.NewDecoder(&buf).Decode(decoded))

	assert.Equal(t, original.CurrentModelSpanID, decoded.CurrentModelSpanID)
	assert.Equal(t, original.CurrentAssistantMessageEventID, decoded.CurrentAssistantMessageEventID)
	require.Len(t, decoded.ToolSpansInFlight, 2)
	for k, v := range original.ToolSpansInFlight {
		got, ok := decoded.ToolSpansInFlight[k]
		require.Truef(t, ok, "missing key %q after gob round-trip", k)
		assert.Equal(t, v.SpanID, got.SpanID)
		assert.Equal(t, v.StartEventID, got.StartEventID)
		assert.True(t, v.StartedAt.Equal(got.StartedAt), "StartedAt mismatch: %v vs %v", v.StartedAt, got.StartedAt)
		assert.Equal(t, v.ParentSpanID, got.ParentSpanID)
		assert.Equal(t, v.AssistantMessageEventID, got.AssistantMessageEventID)
	}
}

// Sanity guard: ensure compose.IsInterruptRerunError import is preserved (used in wrappers).
var _ = compose.IsInterruptRerunError

// TestToolSpan_PermissionRejectEmitsEndSpan exercises the path where the tool
// is interrupted, then on resume the user rejects (Approved=false). The tool
// returns a rejection result rather than an error, so the end span carries
// Status=ok with a populated ToolResultMessageEventID. Same SpanID across
// the boundary.
func TestToolSpan_PermissionRejectEmitsEndSpan(t *testing.T) {
	ctx := context.Background()
	tl := &approvableSpanTool{name: "reject_me"}

	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "rej_call", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "reject_agent", []tool.BaseTool{tl}, scripted)
	checkpointID := "ckpt-reject"
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter1 := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())

	var (
		starts1      []*SessionEvent[*schema.Message]
		ends1        []*SessionEvent[*schema.Message]
		interruptEvt *AgentEvent
	)
	for {
		ev, ok := iter1.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvt = ev
		}
		if ev.SessionEventVariant.GetEvent() != nil && ev.SessionEventVariant.GetEvent().Span != nil {
			switch ev.SessionEventVariant.GetEvent().Kind {
			case SessionEventSpanToolCallStart:
				starts1 = append(starts1, ev.SessionEventVariant.GetEvent())
			case SessionEventSpanToolCallEnd:
				ends1 = append(ends1, ev.SessionEventVariant.GetEvent())
			}
		}
	}
	require.NotNil(t, interruptEvt)
	require.Len(t, starts1, 1)
	require.Empty(t, ends1)

	startSpanID := starts1[0].Span.SpanID

	var toolInterruptID string
	for _, ictx := range interruptEvt.Action.Interrupted.InterruptContexts {
		if ictx.IsRootCause {
			toolInterruptID = ictx.ID
			break
		}
	}
	require.NotEmpty(t, toolInterruptID)

	resumeIter, err := runner.ResumeWithParams(ctx, checkpointID, &ResumeParams{
		Targets: map[string]any{toolInterruptID: &approvalResultSpan{Approved: false}},
	}, WithTimelineEvents())
	require.NoError(t, err)
	starts2, ends2, _ := drainAndCollectSpans(t, resumeIter)
	assert.Empty(t, starts2)
	require.Len(t, ends2, 1)
	assert.Equal(t, startSpanID, ends2[0].Span.SpanID)
	assert.Equal(t, "ok", ends2[0].Span.Status, "rejection produces a successful return (the deny content) — status is ok, not error")
	assert.NotEmpty(t, ends2[0].Span.Tool.ToolResultMessageEventID)
}

// successOnlyTool runs to completion on the first invocation. Combined with
// the absence of a permission middleware, it exercises the "non-interrupted
// call" path where start and end both fire on the same run with status=ok.
type successOnlyTool struct {
	name   string
	result string
}

func (t *successOnlyTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "always succeeds",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input"},
		}),
	}, nil
}

func (t *successOnlyTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return t.result, nil
}

// TestToolSpan_NonInterruptedCallEmitsBothSpansOnSameRun verifies that a tool
// that runs straight to success produces a tool_call_start + tool_call_end
// pair on the same run, with the in-flight entry cleared at end emission.
// (This corresponds to plan §4.5.1 #6 which used a "gate=deny" example —
// the wire-shape behavior is identical: single run, single span pair, status
// ok, populated ToolResultMessageEventID.)
func TestToolSpan_NonInterruptedCallEmitsBothSpansOnSameRun(t *testing.T) {
	ctx := context.Background()
	tl := &successOnlyTool{name: "noninterrupted_tool", result: "ok"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "noninter_call", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "noninter_agent", []tool.BaseTool{tl}, scripted)
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID("ckpt-noninter"), WithTimelineEvents())
	starts, ends, _ := drainAndCollectSpans(t, iter)

	require.Len(t, starts, 1)
	require.Len(t, ends, 1)
	assert.Equal(t, starts[0].Span.SpanID, ends[0].Span.SpanID)
	assert.Equal(t, "ok", ends[0].Span.Status)
	assert.NotEmpty(t, ends[0].Span.Tool.ToolResultMessageEventID)
}

// TestToolSpan_ParallelInterruptResumesEmitMatchingEnds exercises the parallel
// call scenario: two tool calls (A, B) emitted in a single assistant message,
// both interrupting on first invocation. After the first run we should see
// 2 starts and 0 ends. After resuming both with approval, we expect end spans
// keyed to the matching SpanIDs (one per CallID).
func TestToolSpan_ParallelInterruptResumesEmitMatchingEnds(t *testing.T) {
	ctx := context.Background()
	tl := &approvableSpanTool{name: "parallel_tool"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling 2", []schema.ToolCall{
			{ID: "call_par_a", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"a"}`}},
			{ID: "call_par_b", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"b"}`}},
		}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "parallel_agent", []tool.BaseTool{tl}, scripted)
	checkpointID := "ckpt-parallel"
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter1 := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())

	var (
		starts1      []*SessionEvent[*schema.Message]
		ends1        []*SessionEvent[*schema.Message]
		interruptEvt *AgentEvent
	)
	for {
		ev, ok := iter1.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvt = ev
		}
		if ev.SessionEventVariant.GetEvent() != nil && ev.SessionEventVariant.GetEvent().Span != nil {
			switch ev.SessionEventVariant.GetEvent().Kind {
			case SessionEventSpanToolCallStart:
				starts1 = append(starts1, ev.SessionEventVariant.GetEvent())
			case SessionEventSpanToolCallEnd:
				ends1 = append(ends1, ev.SessionEventVariant.GetEvent())
			}
		}
	}
	require.NotNil(t, interruptEvt)
	require.Len(t, starts1, 2, "expected one tool_call_start for each parallel call")
	assert.Empty(t, ends1)

	// Map CallID -> start SpanID for later assertions.
	callIDToStartSpanID := map[string]string{}
	for _, s := range starts1 {
		callIDToStartSpanID[s.Span.Tool.ToolUseID] = s.Span.SpanID
	}
	require.Contains(t, callIDToStartSpanID, "call_par_a")
	require.Contains(t, callIDToStartSpanID, "call_par_b")

	// Collect interrupt IDs (root causes only).
	var interruptIDs []string
	for _, ictx := range interruptEvt.Action.Interrupted.InterruptContexts {
		if ictx.IsRootCause {
			interruptIDs = append(interruptIDs, ictx.ID)
		}
	}
	require.Len(t, interruptIDs, 2)

	// Approve both at once.
	targets := map[string]any{}
	for _, id := range interruptIDs {
		targets[id] = &approvalResultSpan{Approved: true}
	}
	resumeIter, err := runner.ResumeWithParams(ctx, checkpointID, &ResumeParams{Targets: targets}, WithTimelineEvents())
	require.NoError(t, err)
	starts2, ends2, _ := drainAndCollectSpans(t, resumeIter)
	assert.Empty(t, starts2, "no new starts on resume")
	require.Len(t, ends2, 2, "two ends, one per parallel call")
	for _, e := range ends2 {
		expectedSpanID, ok := callIDToStartSpanID[e.Span.Tool.ToolUseID]
		require.Truef(t, ok, "end span carries unknown CallID %q", e.Span.Tool.ToolUseID)
		assert.Equal(t, expectedSpanID, e.Span.SpanID, "end span SpanID matches the start span for the same CallID")
		assert.Equal(t, "ok", e.Span.Status)
	}
}

func newFakeChatModel(
	gen func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error),
	stream func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error),
) *fakeChatModel {
	if gen == nil {
		gen = func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unused")
		}
	}
	if stream == nil {
		stream = func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("unused")
		}
	}
	return &fakeChatModel{callbacksEnabled: true, generate: gen, stream: stream}
}

func TestRetryThenFailover(t *testing.T) {
	t.Run("Generate_RetryExhaustedTriggersFailover", func(t *testing.T) {
		modelErr := errors.New("model error")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, modelErr
		}, nil)
		m2 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("ok from m2", nil), nil
		}, nil)

		retryCfg := &ModelRetryConfig{
			MaxRetries:  2,
			IsRetryAble: func(_ context.Context, err error) bool { return true },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, fc *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				require.NotNil(t, fc.LastErr)
				return m2, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		require.Equal(t, "ok from m2", msg.Content)

		// m1: 1 (lastSuccess) + 2 retries = 3 calls on lastSuccess attempt,
		// then failover to m2 which also goes through retry wrapper: 1 call succeeds.
		require.Equal(t, int32(3), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	})

	t.Run("Generate_AllExhausted", func(t *testing.T) {
		modelErr := errors.New("always fails")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, modelErr
		}, nil)
		m2 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return nil, modelErr
		}, nil)

		retryCfg := &ModelRetryConfig{
			MaxRetries:  1,
			IsRetryAble: func(_ context.Context, err error) bool { return true },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m2, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)

		// Should be RetryExhaustedError from m2's retry wrapper
		var retryErr *RetryExhaustedError
		require.True(t, errors.As(err, &retryErr))

		// m1: 1 initial + 1 retry = 2 calls
		require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
		// m2: 1 initial + 1 retry = 2 calls
		require.Equal(t, int32(2), atomic.LoadInt32(&m2Calls))
	})

	t.Run("Generate_RetrySucceedsNoFailover", func(t *testing.T) {
		var m1Calls int32
		var failoverCalled int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			n := atomic.AddInt32(&m1Calls, 1)
			if n == 1 {
				return nil, errors.New("transient error")
			}
			return schema.AssistantMessage("ok on retry", nil), nil
		}, nil)

		retryCfg := &ModelRetryConfig{
			MaxRetries:  2,
			IsRetryAble: func(_ context.Context, err error) bool { return true },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&failoverCalled, 1)
				return true
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				t.Fatal("GetFailoverModel should not be called when retry succeeds")
				return nil, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		require.Equal(t, "ok on retry", msg.Content)

		// 2 calls: first fails, second succeeds via retry
		require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
		// ShouldFailover should never be called
		require.Equal(t, int32(0), atomic.LoadInt32(&failoverCalled))
	})

	t.Run("Generate_NonRetryableErrorTriggersFailover", func(t *testing.T) {
		nonRetryableErr := errors.New("non-retryable")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, nonRetryableErr
		}, nil)
		m2 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("ok from m2", nil), nil
		}, nil)

		retryCfg := &ModelRetryConfig{
			MaxRetries: 3,
			IsRetryAble: func(_ context.Context, err error) bool {
				// Only non-retryable errors
				return !errors.Is(err, nonRetryableErr)
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m2, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		require.Equal(t, "ok from m2", msg.Content)

		// m1 called only once — non-retryable error skips retry
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	})

	t.Run("Stream_RetryExhaustedTriggersFailover", func(t *testing.T) {
		streamErr := errors.New("stream mid error")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("partial", nil),
			}, streamErr), nil
		})
		m2 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok from m2", nil)}), nil
		})

		retryCfg := &ModelRetryConfig{
			MaxRetries:  1,
			IsRetryAble: func(_ context.Context, err error) bool { return true },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, fc *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				require.NotNil(t, fc.LastErr)
				return m2, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		msgs, err := drainMessageStream(sr)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.Equal(t, "ok from m2", msgs[0].Content)

		// m1: 1 initial + 1 retry = 2 calls on lastSuccess attempt
		require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	})

	t.Run("Stream_AllExhausted", func(t *testing.T) {
		streamErr := errors.New("always fails mid-stream")
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("p", nil),
			}, streamErr), nil
		})
		m2 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("p", nil),
			}, streamErr), nil
		})

		retryCfg := &ModelRetryConfig{
			MaxRetries:  1,
			IsRetryAble: func(_ context.Context, err error) bool { return true },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m2, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)

		var retryErr *RetryExhaustedError
		require.True(t, errors.As(err, &retryErr))

		// m1: 1 initial + 1 retry = 2 calls
		require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
		// m2: 1 initial + 1 retry = 2 calls
		require.Equal(t, int32(2), atomic.LoadInt32(&m2Calls))
	})

	t.Run("ShouldRetry_Stream_TriggersFailover", func(t *testing.T) {
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("bad from m1", nil)}), nil
		})
		m2 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("good from m2", nil)}), nil
		})

		retryCfg := &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad from m1" {
					return &RetryDecision{Retry: true}
				}
				return &RetryDecision{Retry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m2, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		sr, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		msgs, err := drainMessageStream(sr)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.Equal(t, "good from m2", msgs[0].Content)
		require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	})

	t.Run("ShouldRetry_Generate_TriggersFailover", func(t *testing.T) {
		var m1Calls int32
		var m2Calls int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return schema.AssistantMessage("bad from m1", nil), nil
		}, nil)
		m2 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m2Calls, 1)
			return schema.AssistantMessage("good from m2", nil), nil
		}, nil)

		retryCfg := &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(_ context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad from m1" {
					return &RetryDecision{Retry: true}
				}
				return &RetryDecision{Retry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m2, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.NoError(t, err)
		require.Equal(t, "good from m2", msg.Content)
		require.Equal(t, int32(2), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(1), atomic.LoadInt32(&m2Calls))
	})

	t.Run("Stream_GetFailoverModelReturnsNilModel", func(t *testing.T) {
		streamErr := errors.New("m1 always fails")
		var m1Calls int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, streamErr
		})

		retryCfg := &ModelRetryConfig{
			MaxRetries:  0,
			IsRetryAble: func(_ context.Context, err error) bool { return false },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 1,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return nil, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
		require.Contains(t, err.Error(), "returned nil model at attempt")
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
	})

	t.Run("Stream_ContextCanceledDuringFailover", func(t *testing.T) {
		streamErr := errors.New("m1 fails")
		var m1Calls int32
		var failoverModelCalled int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, streamErr
		})

		ctx, cancel := context.WithCancel(context.Background())

		retryCfg := &ModelRetryConfig{
			MaxRetries:  0,
			IsRetryAble: func(_ context.Context, err error) bool { return false },
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return 0 },
		}

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 3,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				cancel()
				return err != nil
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				atomic.AddInt32(&failoverModelCalled, 1)
				return nil, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			retryConfig:    retryCfg,
			failoverConfig: failoverCfg,
		})

		ctx = withTypedChatModelAgentExecCtx(ctx, &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(0), atomic.LoadInt32(&failoverModelCalled))
	})
}

func TestErrStreamCanceled_Failover(t *testing.T) {
	t.Run("Stream_NeverFailedOver", func(t *testing.T) {
		var m1Calls int32
		var failoverCalled int32

		m1 := newFakeChatModel(nil, func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			atomic.AddInt32(&m1Calls, 1)
			return streamWithMidError([]*schema.Message{
				schema.AssistantMessage("partial", nil),
			}, ErrStreamCanceled), nil
		})

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 2,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&failoverCalled, 1)
				return true
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				t.Fatal("GetFailoverModel should not be called for ErrStreamCanceled")
				return nil, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrStreamCanceled))
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(0), atomic.LoadInt32(&failoverCalled))
	})

	t.Run("Generate_NeverFailedOver", func(t *testing.T) {
		var m1Calls int32
		var failoverCalled int32

		m1 := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&m1Calls, 1)
			return nil, ErrStreamCanceled
		}, nil)

		failoverCfg := &ModelFailoverConfig[*schema.Message]{
			MaxRetries: 2,
			ShouldFailover: func(_ context.Context, _ *schema.Message, err error) bool {
				atomic.AddInt32(&failoverCalled, 1)
				return true
			},
			GetFailoverModel: func(_ context.Context, _ *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				t.Fatal("GetFailoverModel should not be called for ErrStreamCanceled")
				return nil, nil, nil
			},
		}

		wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
			failoverConfig: failoverCfg,
		})

		ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
			failoverLastSuccessModel: m1,
		})
		_, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrStreamCanceled))
		require.Equal(t, int32(1), atomic.LoadInt32(&m1Calls))
		require.Equal(t, int32(0), atomic.LoadInt32(&failoverCalled))
	})
}
