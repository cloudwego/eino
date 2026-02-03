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
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

type generateEndpoint func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error)
type streamEndpoint func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)

type modelWrapperConfig struct {
	handlers    []handlerInfo
	middlewares []AgentMiddleware
	retryConfig *ModelRetryConfig
	toolInfos   []*schema.ToolInfo
}

func buildModelWrappers(m model.BaseChatModel, config *modelWrapperConfig) model.BaseChatModel {
	var wrapped model.BaseChatModel = m

	if !components.IsCallbacksEnabled(m) {
		wrapped = (&callbackInjectionModelWrapper{}).WrapModel(wrapped)
	}

	wrapped = (&eventSenderModelWrapper{modelRetryConfig: config.retryConfig}).WrapModel(wrapped)

	if config.retryConfig != nil {
		wrapped = newRetryModelWrapper(wrapped, config.retryConfig)
	}

	wrapped = &stateModelWrapper{
		inner:       wrapped,
		original:    m,
		handlers:    config.handlers,
		middlewares: config.middlewares,
		toolInfos:   config.toolInfos,
	}

	return wrapped
}

type callbackInjectionModelWrapper struct{}

func (w *callbackInjectionModelWrapper) WrapModel(m model.BaseChatModel) model.BaseChatModel {
	return &callbackInjectedModel{inner: m}
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

func handlersToToolMiddlewares(handlers []handlerInfo) []compose.ToolMiddleware {
	var middlewares []compose.ToolMiddleware
	for i := len(handlers) - 1; i >= 0; i-- {
		h := handlers[i]
		if !h.hasWrapInvokableToolCall && !h.hasWrapStreamableToolCall && !h.hasWrapEnhancedInvokableToolCall && !h.hasWrapEnhancedStreamableToolCall {
			continue
		}

		m := compose.ToolMiddleware{}

		if h.hasWrapInvokableToolCall {
			handler := h.handler
			m.Invokable = func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
					tCtx := &ToolContext{
						Name:   input.Name,
						CallID: input.CallID,
					}
					wrappedEndpoint := handler.WrapInvokableToolCall(
						func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
							output, err := next(ctx, &compose.ToolInput{
								Name:        input.Name,
								CallID:      input.CallID,
								Arguments:   argumentsInJSON,
								CallOptions: opts,
							})
							if err != nil {
								return "", err
							}
							return output.Result, nil
						},
						tCtx,
					)
					result, err := wrappedEndpoint(ctx, input.Arguments, input.CallOptions...)
					if err != nil {
						return nil, err
					}
					return &compose.ToolOutput{Result: result}, nil
				}
			}
		}

		if h.hasWrapStreamableToolCall {
			handler := h.handler
			m.Streamable = func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
					tCtx := &ToolContext{
						Name:   input.Name,
						CallID: input.CallID,
					}
					wrappedEndpoint := handler.WrapStreamableToolCall(
						func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
							output, err := next(ctx, &compose.ToolInput{
								Name:        input.Name,
								CallID:      input.CallID,
								Arguments:   argumentsInJSON,
								CallOptions: opts,
							})
							if err != nil {
								return nil, err
							}
							return output.Result, nil
						},
						tCtx,
					)
					result, err := wrappedEndpoint(ctx, input.Arguments, input.CallOptions...)
					if err != nil {
						return nil, err
					}
					return &compose.StreamToolOutput{Result: result}, nil
				}
			}
		}

		if h.hasWrapEnhancedInvokableToolCall {
			handler := h.handler
			m.EnhancedInvokable = func(next compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
					tCtx := &ToolContext{
						Name:   input.Name,
						CallID: input.CallID,
					}
					wrappedEndpoint := handler.WrapEnhancedInvokableToolCall(
						func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
							output, err := next(ctx, &compose.ToolInput{
								Name:        input.Name,
								CallID:      input.CallID,
								Arguments:   toolArgument.TextArgument,
								CallOptions: opts,
							})
							if err != nil {
								return nil, err
							}
							return output.Result, nil
						},
						tCtx,
					)
					result, err := wrappedEndpoint(ctx, &schema.ToolArgument{TextArgument: input.Arguments}, input.CallOptions...)
					if err != nil {
						return nil, err
					}
					return &compose.EnhancedInvokableToolOutput{Result: result}, nil
				}
			}
		}

		if h.hasWrapEnhancedStreamableToolCall {
			handler := h.handler
			m.EnhancedStreamable = func(next compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
					tCtx := &ToolContext{
						Name:   input.Name,
						CallID: input.CallID,
					}
					wrappedEndpoint := handler.WrapEnhancedStreamableToolCall(
						func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
							output, err := next(ctx, &compose.ToolInput{
								Name:        input.Name,
								CallID:      input.CallID,
								Arguments:   toolArgument.TextArgument,
								CallOptions: opts,
							})
							if err != nil {
								return nil, err
							}
							return output.Result, nil
						},
						tCtx,
					)
					result, err := wrappedEndpoint(ctx, &schema.ToolArgument{TextArgument: input.Arguments}, input.CallOptions...)
					if err != nil {
						return nil, err
					}
					return &compose.EnhancedStreamableToolOutput{Result: result}, nil
				}
			}
		}

		middlewares = append(middlewares, m)
	}
	return middlewares
}

type eventSenderModelWrapper struct {
	modelRetryConfig *ModelRetryConfig
}

func (w *eventSenderModelWrapper) WrapModel(m model.BaseChatModel) model.BaseChatModel {
	return &eventSenderModel{inner: m, modelRetryConfig: w.modelRetryConfig}
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

	execCtx := getChatModelAgentExecCtx(ctx)
	if execCtx == nil || execCtx.generator == nil {
		return nil, errors.New("generator is nil when sending event in Generate: ensure agent state is properly initialized")
	}

	event := EventFromMessage(result, nil, schema.Assistant, "")
	execCtx.send(event)

	return result, nil
}

func (m *eventSenderModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	result, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}

	execCtx := getChatModelAgentExecCtx(ctx)
	if execCtx == nil || execCtx.generator == nil {
		result.Close()
		return nil, errors.New("generator is nil when sending event in Stream: ensure agent state is properly initialized")
	}

	var retryAttempt int
	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		retryAttempt = st.getRetryAttempt()
		return nil
	})

	streams := result.Copy(3)

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
	execCtx.send(event)

	go func() {
		defer streams[2].Close()
		for {
			msg, err := streams[2].Recv()
			if err != nil {
				break
			}
			_ = msg
		}
	}()

	return streams[1], nil
}

func popToolGenAction(ctx context.Context, toolName string) *AgentAction {
	toolCallID := compose.GetToolCallID(ctx)

	var action *AgentAction
	_ = compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		if len(toolCallID) > 0 {
			if a := st.popToolGenAction(toolCallID); a != nil {
				action = a
				return nil
			}
		}

		if a := st.popToolGenAction(toolName); a != nil {
			action = a
		}

		return nil
	})

	return action
}

type eventSenderToolHandler struct{}

func (h *eventSenderToolHandler) WrapInvokableToolCall(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		output, err := next(ctx, input)
		if err != nil {
			return nil, err
		}

		toolName := input.Name
		callID := input.CallID

		prePopAction := popToolGenAction(ctx, toolName)
		msg := schema.ToolMessage(output.Result, callID, schema.WithToolName(toolName))
		event := EventFromMessage(msg, nil, schema.Tool, toolName)
		if prePopAction != nil {
			event.Action = prePopAction
		}

		execCtx := getChatModelAgentExecCtx(ctx)
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			if st.getReturnDirectlyToolCallID() == callID {
				st.setReturnDirectlyEvent(event)
			} else {
				execCtx.send(event)
			}
			return nil
		})

		return output, nil
	}
}

func (h *eventSenderToolHandler) WrapStreamableToolCall(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		output, err := next(ctx, input)
		if err != nil {
			return nil, err
		}

		toolName := input.Name
		callID := input.CallID

		prePopAction := popToolGenAction(ctx, toolName)
		streams := output.Result.Copy(2)

		cvt := func(in string) (Message, error) {
			return schema.ToolMessage(in, callID, schema.WithToolName(toolName)), nil
		}
		msgStream := schema.StreamReaderWithConvert(streams[0], cvt)
		event := EventFromMessage(nil, msgStream, schema.Tool, toolName)
		event.Action = prePopAction

		execCtx := getChatModelAgentExecCtx(ctx)
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			if st.getReturnDirectlyToolCallID() == callID {
				st.setReturnDirectlyEvent(event)
			} else {
				execCtx.send(event)
			}
			return nil
		})

		return &compose.StreamToolOutput{Result: streams[1]}, nil
	}
}

func (h *eventSenderToolHandler) WrapEnhancedInvokableToolCall(next compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
		output, err := next(ctx, input)
		if err != nil {
			return nil, err
		}

		toolName := input.Name
		callID := input.CallID

		prePopAction := popToolGenAction(ctx, toolName)
		msg := schema.ToolMessage("", callID, schema.WithToolName(toolName))
		msg.UserInputMultiContent, err = output.Result.ToMessageInputParts()
		if err != nil {
			return nil, err
		}
		event := EventFromMessage(msg, nil, schema.Tool, toolName)
		if prePopAction != nil {
			event.Action = prePopAction
		}

		execCtx := getChatModelAgentExecCtx(ctx)
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			if st.getReturnDirectlyToolCallID() == callID {
				st.setReturnDirectlyEvent(event)
			} else {
				execCtx.send(event)
			}
			return nil
		})

		return output, nil
	}
}

func (h *eventSenderToolHandler) WrapEnhancedStreamableToolCall(next compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
		output, err := next(ctx, input)
		if err != nil {
			return nil, err
		}

		toolName := input.Name
		callID := input.CallID

		prePopAction := popToolGenAction(ctx, toolName)
		streams := output.Result.Copy(2)

		cvt := func(in *schema.ToolResult) (Message, error) {
			msg := schema.ToolMessage("", callID, schema.WithToolName(toolName))
			var cvtErr error
			msg.UserInputMultiContent, cvtErr = in.ToMessageInputParts()
			if cvtErr != nil {
				return nil, cvtErr
			}
			return msg, nil
		}
		msgStream := schema.StreamReaderWithConvert(streams[0], cvt)
		event := EventFromMessage(nil, msgStream, schema.Tool, toolName)
		event.Action = prePopAction

		execCtx := getChatModelAgentExecCtx(ctx)
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			if st.getReturnDirectlyToolCallID() == callID {
				st.setReturnDirectlyEvent(event)
			} else {
				execCtx.send(event)
			}
			return nil
		})

		return &compose.EnhancedStreamableToolOutput{Result: streams[1]}, nil
	}
}

type stateModelWrapper struct {
	inner       model.BaseChatModel
	original    model.BaseChatModel
	handlers    []handlerInfo
	middlewares []AgentMiddleware
	toolInfos   []*schema.ToolInfo
}

func (w *stateModelWrapper) IsCallbacksEnabled() bool {
	return true
}

func (w *stateModelWrapper) GetType() string {
	if typer, ok := w.original.(components.Typer); ok {
		return typer.GetType()
	}
	return generic.ParseTypeName(reflect.ValueOf(w.original))
}

func (w *stateModelWrapper) wrapGenerateEndpoint(endpoint generateEndpoint) generateEndpoint {
	for i := len(w.handlers) - 1; i >= 0; i-- {
		if w.handlers[i].hasWrapModel {
			handler := w.handlers[i].handler
			innerEndpoint := endpoint
			baseToolInfos := w.toolInfos
			endpoint = func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				baseOpts := &model.Options{Tools: baseToolInfos}
				commonOpts := model.GetCommonOptions(baseOpts, opts...)
				mc := &ModelContext{Tools: commonOpts.Tools}
				wrappedModel := handler.WrapModel(&endpointModel{generate: innerEndpoint}, mc)
				return wrappedModel.Generate(ctx, input, opts...)
			}
		}
	}
	return endpoint
}

func (w *stateModelWrapper) wrapStreamEndpoint(endpoint streamEndpoint) streamEndpoint {
	for i := len(w.handlers) - 1; i >= 0; i-- {
		if w.handlers[i].hasWrapModel {
			handler := w.handlers[i].handler
			innerEndpoint := endpoint
			baseToolInfos := w.toolInfos
			endpoint = func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
				baseOpts := &model.Options{Tools: baseToolInfos}
				commonOpts := model.GetCommonOptions(baseOpts, opts...)
				mc := &ModelContext{Tools: commonOpts.Tools}
				wrappedModel := handler.WrapModel(&endpointModel{stream: innerEndpoint}, mc)
				return wrappedModel.Stream(ctx, input, opts...)
			}
		}
	}
	return endpoint
}

func (w *stateModelWrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	var stateMessages []Message
	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		stateMessages = st.Messages
		return nil
	})

	state := &ChatModelAgentState{Messages: append(stateMessages, input...)}

	for _, m := range w.middlewares {
		if m.BeforeChatModel != nil {
			if err := m.BeforeChatModel(ctx, state); err != nil {
				return nil, err
			}
		}
	}

	for _, info := range w.handlers {
		if info.hasBeforeModelRewriteState {
			var err error
			ctx, state, err = info.handler.BeforeModelRewriteState(ctx, state)
			if err != nil {
				return nil, err
			}
		}
	}

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		st.Messages = state.Messages
		return nil
	})

	wrappedEndpoint := w.wrapGenerateEndpoint(w.inner.Generate)
	result, err := wrappedEndpoint(ctx, state.Messages, opts...)
	if err != nil {
		return nil, err
	}
	state.Messages = append(state.Messages, result)

	for _, info := range w.handlers {
		if info.hasAfterModelRewriteState {
			ctx, state, err = info.handler.AfterModelRewriteState(ctx, state)
			if err != nil {
				return nil, err
			}
		}
	}

	for _, m := range w.middlewares {
		if m.AfterChatModel != nil {
			if err := m.AfterChatModel(ctx, state); err != nil {
				return nil, err
			}
		}
	}

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		st.Messages = state.Messages
		return nil
	})

	if len(state.Messages) == 0 {
		return nil, errors.New("no messages left in state after model call")
	}
	return state.Messages[len(state.Messages)-1], nil
}

func (w *stateModelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	var stateMessages []Message
	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		stateMessages = st.Messages
		return nil
	})

	state := &ChatModelAgentState{Messages: append(stateMessages, input...)}

	for _, m := range w.middlewares {
		if m.BeforeChatModel != nil {
			if err := m.BeforeChatModel(ctx, state); err != nil {
				return nil, err
			}
		}
	}

	for _, info := range w.handlers {
		if info.hasBeforeModelRewriteState {
			var err error
			ctx, state, err = info.handler.BeforeModelRewriteState(ctx, state)
			if err != nil {
				return nil, err
			}
		}
	}

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		st.Messages = state.Messages
		return nil
	})

	wrappedEndpoint := w.wrapStreamEndpoint(w.inner.Stream)
	stream, err := wrappedEndpoint(ctx, state.Messages, opts...)
	if err != nil {
		return nil, err
	}
	result, err := schema.ConcatMessageStream(stream)
	if err != nil {
		return nil, err
	}
	state.Messages = append(state.Messages, result)

	for _, info := range w.handlers {
		if info.hasAfterModelRewriteState {
			ctx, state, err = info.handler.AfterModelRewriteState(ctx, state)
			if err != nil {
				return nil, err
			}
		}
	}

	for _, m := range w.middlewares {
		if m.AfterChatModel != nil {
			if err := m.AfterChatModel(ctx, state); err != nil {
				return nil, err
			}
		}
	}

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		st.Messages = state.Messages
		return nil
	})

	if len(state.Messages) == 0 {
		return nil, errors.New("no messages left in state after model call")
	}
	return schema.StreamReaderFromArray([]*schema.Message{state.Messages[len(state.Messages)-1]}), nil
}

type endpointModel struct {
	generate generateEndpoint
	stream   streamEndpoint
}

func (m *endpointModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.generate != nil {
		return m.generate(ctx, input, opts...)
	}
	return nil, errors.New("generate endpoint not set")
}

func (m *endpointModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.stream != nil {
		return m.stream(ctx, input, opts...)
	}
	return nil, errors.New("stream endpoint not set")
}
