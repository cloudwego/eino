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
	"reflect"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

type wrappedChatModel struct {
	inner    model.ToolCallingChatModel
	wrappers []ModelCallWrapper
}

func newWrappedChatModel(inner model.ToolCallingChatModel, wrappers []ModelCallWrapper) model.ToolCallingChatModel {
	needCallback := !components.IsCallbacksEnabled(inner)
	if needCallback {
		wrappers = append(wrappers, &callbackInjectionWrapper{})
	}
	if len(wrappers) == 0 {
		return inner
	}
	return &wrappedChatModel{inner: inner, wrappers: wrappers}
}

func (w *wrappedChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	call := &ModelCall{Messages: input, Options: opts}

	endpoint := func(ctx context.Context, c *ModelCall) (*ModelResult, error) {
		msg, err := w.inner.Generate(ctx, c.Messages, c.Options...)
		if err != nil {
			return nil, err
		}
		return &ModelResult{Message: msg}, nil
	}

	for i := len(w.wrappers) - 1; i >= 0; i-- {
		wrapper := w.wrappers[i]
		next := endpoint
		endpoint = func(ctx context.Context, c *ModelCall) (*ModelResult, error) {
			return wrapper.WrapGenerate(ctx, c, next)
		}
	}

	result, err := endpoint(ctx, call)
	if err != nil {
		return nil, err
	}
	return result.Message, nil
}

func (w *wrappedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	call := &ModelCall{Messages: input, Options: opts}

	endpoint := func(ctx context.Context, c *ModelCall) (*StreamModelResult, error) {
		sr, err := w.inner.Stream(ctx, c.Messages, c.Options...)
		if err != nil {
			return nil, err
		}
		return &StreamModelResult{Stream: sr}, nil
	}

	for i := len(w.wrappers) - 1; i >= 0; i-- {
		wrapper := w.wrappers[i]
		next := endpoint
		endpoint = func(ctx context.Context, c *ModelCall) (*StreamModelResult, error) {
			return wrapper.WrapStream(ctx, c, next)
		}
	}

	result, err := endpoint(ctx, call)
	if err != nil {
		return nil, err
	}
	return result.Stream, nil
}

func (w *wrappedChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	newInner, err := w.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &wrappedChatModel{inner: newInner, wrappers: w.wrappers}, nil
}

func (w *wrappedChatModel) GetType() string {
	if gt, ok := w.inner.(components.Typer); ok {
		return gt.GetType()
	}
	return generic.ParseTypeName(reflect.ValueOf(w.inner))
}

func (w *wrappedChatModel) IsCallbacksEnabled() bool {
	return true
}

func collectModelWrappersFromHandlers(handlers []AgentHandler) []ModelCallWrapper {
	var wrappers []ModelCallWrapper
	for _, h := range handlers {
		wrapper := h.GetModelCallWrapper()
		if wrapper == nil {
			continue
		}
		wrappers = append(wrappers, wrapper)
	}
	return wrappers
}

func toolCallWrappersToMiddlewares(wrappers []ToolCallWrapper) []compose.ToolMiddleware {
	var middlewares []compose.ToolMiddleware
	for _, w := range wrappers {
		wrapper := w
		middlewares = append(middlewares, compose.ToolMiddleware{
			Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
					return wrapper.WrapInvoke(ctx, input, next)
				}
			},
			Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
					return wrapper.WrapStream(ctx, input, next)
				}
			},
		})
	}
	return middlewares
}

type callbackInjectionWrapper struct {
	BaseModelCallWrapper
}

func (w *callbackInjectionWrapper) WrapGenerate(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*ModelResult, error)) (*ModelResult, error) {
	ctx = callbacks.OnStart(ctx, call.Messages)
	result, err := next(ctx, call)
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	callbacks.OnEnd(ctx, result.Message)
	return result, nil
}

func (w *callbackInjectionWrapper) WrapStream(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*StreamModelResult, error)) (*StreamModelResult, error) {
	ctx = callbacks.OnStart(ctx, call.Messages)
	result, err := next(ctx, call)
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	_, wrappedStream := callbacks.OnEndWithStreamOutput(ctx, result.Stream)
	return &StreamModelResult{Stream: wrappedStream}, nil
}

type eventSenderModelWrapper struct {
	modelRetryConfig *ModelRetryConfig
}

func (w *eventSenderModelWrapper) WrapGenerate(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*ModelResult, error)) (*ModelResult, error) {
	result, err := next(ctx, call)
	if err != nil {
		return nil, err
	}

	var gen *AsyncGenerator[*AgentEvent]
	err = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		gen = st.generator
		return nil
	})
	if err != nil {
		return nil, err
	}

	if gen == nil {
		return nil, errors.New("generator is nil when sending event in WrapGenerate")
	}

	event := EventFromMessage(result.Message, nil, schema.Assistant, "")
	gen.Send(event)

	return result, nil
}

func (w *eventSenderModelWrapper) WrapStream(ctx context.Context, call *ModelCall, next func(context.Context, *ModelCall) (*StreamModelResult, error)) (*StreamModelResult, error) {
	result, err := next(ctx, call)
	if err != nil {
		return nil, err
	}

	var (
		gen          *AsyncGenerator[*AgentEvent]
		retryAttempt int
	)
	err = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		gen = st.generator
		retryAttempt = st.retryAttempt
		return nil
	})
	if err != nil {
		result.Stream.Close()
		return nil, err
	}

	if gen == nil {
		result.Stream.Close()
		return nil, errors.New("generator is nil when sending event in WrapStream")
	}

	streams := result.Stream.Copy(2)

	eventStream := streams[0]
	if w.modelRetryConfig != nil {
		convertOpts := []schema.ConvertOption{
			schema.WithErrWrapper(genErrWrapper(ctx, w.modelRetryConfig.MaxRetries,
				retryAttempt, w.modelRetryConfig.IsRetryAble)),
		}
		eventStream = schema.StreamReaderWithConvert(streams[0],
			func(m *schema.Message) (*schema.Message, error) { return m, nil },
			convertOpts...)
	}

	event := EventFromMessage(nil, eventStream, schema.Assistant, "")
	gen.Send(event)

	return &StreamModelResult{Stream: streams[1]}, nil
}
