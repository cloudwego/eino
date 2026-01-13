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

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func buildWrappedModel(ctx context.Context, m model.ToolCallingChatModel, handlers []AgentHandler, retryConfig *ModelRetryConfig) (model.ToolCallingChatModel, error) {
	var wrapped model.BaseChatModel = m

	if !components.IsCallbacksEnabled(m) {
		var err error
		wrapped, err = (&callbackInjectionModelWrapper{}).WrapModel(ctx, wrapped)
		if err != nil {
			return nil, err
		}
	}

	for i := len(handlers) - 1; i >= 0; i-- {
		var err error
		wrapped, err = handlers[i].WrapModel(ctx, wrapped)
		if err != nil {
			return nil, err
		}
	}

	wrapped, err := (&eventSenderModelWrapper{modelRetryConfig: retryConfig}).WrapModel(ctx, wrapped)
	if err != nil {
		return nil, err
	}

	result := &baseChatModelAdapter{inner: wrapped, toolBinder: m}
	if retryConfig != nil {
		return newRetryChatModel(result, retryConfig), nil
	}
	return result, nil
}

type baseChatModelAdapter struct {
	inner      model.BaseChatModel
	toolBinder model.ToolCallingChatModel
}

func (a *baseChatModelAdapter) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return a.inner.Generate(ctx, input, opts...)
}

func (a *baseChatModelAdapter) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return a.inner.Stream(ctx, input, opts...)
}

func (a *baseChatModelAdapter) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	newToolBinder, err := a.toolBinder.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &baseChatModelAdapter{inner: a.inner, toolBinder: newToolBinder}, nil
}

func (a *baseChatModelAdapter) IsCallbacksEnabled() bool {
	return true
}

func toolCallWrappersToMiddlewares(wrappers []ToolCallWrapper) []compose.ToolMiddleware {
	var middlewares []compose.ToolMiddleware
	for _, w := range wrappers {
		wrapper := w
		middlewares = append(middlewares, compose.ToolMiddleware{
			Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
					return wrapper.WrapToolInvoke(ctx, input, next)
				}
			},
			Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
					return wrapper.WrapToolStream(ctx, input, next)
				}
			},
		})
	}
	return middlewares
}

type callbackInjectionModelWrapper struct{}

func (w *callbackInjectionModelWrapper) WrapModel(_ context.Context, m model.BaseChatModel) (model.BaseChatModel, error) {
	return &callbackInjectedModel{inner: m}, nil
}

type callbackInjectedModel struct {
	inner model.BaseChatModel
}

func (m *callbackInjectedModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	ctx = callbacks.OnStart(ctx, input)
	result, err := m.inner.Generate(ctx, input, opts...)
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	callbacks.OnEnd(ctx, result)
	return result, nil
}

func (m *callbackInjectedModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	ctx = callbacks.OnStart(ctx, input)
	result, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	_, wrappedStream := callbacks.OnEndWithStreamOutput(ctx, result)
	return wrappedStream, nil
}

type eventSenderModelWrapper struct {
	modelRetryConfig *ModelRetryConfig
}

func (w *eventSenderModelWrapper) WrapModel(_ context.Context, m model.BaseChatModel) (model.BaseChatModel, error) {
	return &eventSenderModel{inner: m, modelRetryConfig: w.modelRetryConfig}, nil
}

type eventSenderModel struct {
	inner            model.BaseChatModel
	modelRetryConfig *ModelRetryConfig
}

func (m *eventSenderModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	result, err := m.inner.Generate(ctx, input, opts...)
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
		return nil, errors.New("generator is nil when sending event in Generate: ensure agent state is properly initialized")
	}

	event := EventFromMessage(result, nil, schema.Assistant, "")
	gen.Send(event)

	return result, nil
}

func (m *eventSenderModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	result, err := m.inner.Stream(ctx, input, opts...)
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
		result.Close()
		return nil, err
	}

	if gen == nil {
		result.Close()
		return nil, errors.New("generator is nil when sending event in Stream: ensure agent state is properly initialized")
	}

	streams := result.Copy(2)

	eventStream := streams[0]
	if m.modelRetryConfig != nil {
		convertOpts := []schema.ConvertOption{
			schema.WithErrWrapper(genErrWrapper(ctx, m.modelRetryConfig.MaxRetries,
				retryAttempt, m.modelRetryConfig.IsRetryAble)),
		}
		eventStream = schema.StreamReaderWithConvert(streams[0],
			func(msg *schema.Message) (*schema.Message, error) { return msg, nil },
			convertOpts...)
	}

	event := EventFromMessage(nil, eventStream, schema.Assistant, "")
	gen.Send(event)

	return streams[1], nil
}

func popToolGenAction(ctx context.Context, toolName string) *AgentAction {
	toolCallID := compose.GetToolCallID(ctx)

	var action *AgentAction
	_ = compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		if len(toolCallID) > 0 {
			if a := st.ToolGenActions[toolCallID]; a != nil {
				action = a
				delete(st.ToolGenActions, toolCallID)
				return nil
			}
		}

		if a := st.ToolGenActions[toolName]; a != nil {
			action = a
			delete(st.ToolGenActions, toolName)
		}

		return nil
	})

	return action
}

type toolResultEventSenderWrapper struct{}

func (w *toolResultEventSenderWrapper) WrapToolInvoke(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*ToolResult, error)) (*ToolResult, error) {
	result, err := next(ctx, call)
	if err != nil {
		return nil, err
	}

	prePopAction := popToolGenAction(ctx, call.Name)
	msg := schema.ToolMessage(result.Result, call.CallID, schema.WithToolName(call.Name))
	event := EventFromMessage(msg, nil, schema.Tool, call.Name)
	if prePopAction != nil {
		event.Action = prePopAction
	}

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.generator == nil {
			return nil
		}
		if st.HasReturnDirectly && st.ReturnDirectlyToolCallID == call.CallID {
			st.ReturnDirectlyEvent = event
		} else {
			st.generator.Send(event)
		}
		return nil
	})

	return result, nil
}

func (w *toolResultEventSenderWrapper) WrapToolStream(ctx context.Context, call *ToolCall, next func(context.Context, *ToolCall) (*StreamToolResult, error)) (*StreamToolResult, error) {
	result, err := next(ctx, call)
	if err != nil {
		return nil, err
	}

	prePopAction := popToolGenAction(ctx, call.Name)
	streams := result.Result.Copy(2)

	cvt := func(in string) (Message, error) {
		return schema.ToolMessage(in, call.CallID, schema.WithToolName(call.Name)), nil
	}
	msgStream := schema.StreamReaderWithConvert(streams[0], cvt)
	event := EventFromMessage(nil, msgStream, schema.Tool, call.Name)
	event.Action = prePopAction

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.generator == nil {
			return nil
		}
		if st.HasReturnDirectly && st.ReturnDirectlyToolCallID == call.CallID {
			st.ReturnDirectlyEvent = event
		} else {
			st.generator.Send(event)
		}
		return nil
	})

	result.Result = streams[1]
	return result, nil
}
