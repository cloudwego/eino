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
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func buildWrappedModel(ctx context.Context, m model.ToolCallingChatModel, handlers []HandlerMiddleware, retryConfig *ModelRetryConfig) (model.ToolCallingChatModel, error) {
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

func wrapToolFuncToMiddleware(wrapFn func(context.Context, tool.BaseTool) (tool.BaseTool, error)) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				virtualTool := &endpointInvokableTool{
					name:        input.Name,
					callID:      input.CallID,
					arguments:   input.Arguments,
					callOptions: input.CallOptions,
					next:        next,
				}
				wrapped, err := wrapFn(ctx, virtualTool)
				if err != nil {
					return nil, err
				}
				if inv, ok := wrapped.(tool.InvokableTool); ok {
					result, err := inv.InvokableRun(ctx, input.Arguments, input.CallOptions...)
					if err != nil {
						return nil, err
					}
					return &compose.ToolOutput{Result: result}, nil
				}
				return next(ctx, input)
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				virtualTool := &endpointStreamableTool{
					name:           input.Name,
					callID:         input.CallID,
					arguments:      input.Arguments,
					callOptions:    input.CallOptions,
					nextStreamable: next,
				}
				wrapped, err := wrapFn(ctx, virtualTool)
				if err != nil {
					return nil, err
				}
				if st, ok := wrapped.(tool.StreamableTool); ok {
					result, err := st.StreamableRun(ctx, input.Arguments, input.CallOptions...)
					if err != nil {
						return nil, err
					}
					return &compose.StreamToolOutput{Result: result}, nil
				}
				return next(ctx, input)
			}
		},
	}
}

type endpointInvokableTool struct {
	name        string
	callID      string
	arguments   string
	callOptions []tool.Option
	next        compose.InvokableToolEndpoint
}

func (t *endpointInvokableTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *endpointInvokableTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	input := &compose.ToolInput{
		Name:        t.name,
		CallID:      t.callID,
		Arguments:   t.arguments,
		CallOptions: t.callOptions,
	}
	output, err := t.next(ctx, input)
	if err != nil {
		return "", err
	}
	return output.Result, nil
}

type endpointStreamableTool struct {
	name           string
	callID         string
	arguments      string
	callOptions    []tool.Option
	nextStreamable compose.StreamableToolEndpoint
}

func (t *endpointStreamableTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *endpointStreamableTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "", errors.New("InvokableRun not supported on streaming endpoint tool")
}

func (t *endpointStreamableTool) StreamableRun(ctx context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	input := &compose.ToolInput{
		Name:        t.name,
		CallID:      t.callID,
		Arguments:   t.arguments,
		CallOptions: t.callOptions,
	}
	output, err := t.nextStreamable(ctx, input)
	if err != nil {
		return nil, err
	}
	return output.Result, nil
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

func wrapToolWithEventSender(_ context.Context, t tool.BaseTool) (tool.BaseTool, error) {
	return &eventSenderTool{inner: t}, nil
}

type eventSenderTool struct {
	inner tool.BaseTool
}

func (t *eventSenderTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *eventSenderTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	inv, ok := t.inner.(tool.InvokableTool)
	if !ok {
		return "", errors.New("inner tool does not implement InvokableTool")
	}

	result, err := inv.InvokableRun(ctx, args, opts...)
	if err != nil {
		return "", err
	}

	toolName, _ := t.getToolName(ctx)
	callID := compose.GetToolCallID(ctx)

	prePopAction := popToolGenAction(ctx, toolName)
	msg := schema.ToolMessage(result, callID, schema.WithToolName(toolName))
	event := EventFromMessage(msg, nil, schema.Tool, toolName)
	if prePopAction != nil {
		event.Action = prePopAction
	}

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.generator == nil {
			return nil
		}
		if st.HasReturnDirectly && st.ReturnDirectlyToolCallID == callID {
			st.ReturnDirectlyEvent = event
		} else {
			st.generator.Send(event)
		}
		return nil
	})

	return result, nil
}

func (t *eventSenderTool) StreamableRun(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	st, ok := t.inner.(tool.StreamableTool)
	if !ok {
		return nil, errors.New("inner tool does not implement StreamableTool")
	}

	result, err := st.StreamableRun(ctx, args, opts...)
	if err != nil {
		return nil, err
	}

	toolName, _ := t.getToolName(ctx)
	callID := compose.GetToolCallID(ctx)

	prePopAction := popToolGenAction(ctx, toolName)
	streams := result.Copy(2)

	cvt := func(in string) (Message, error) {
		return schema.ToolMessage(in, callID, schema.WithToolName(toolName)), nil
	}
	msgStream := schema.StreamReaderWithConvert(streams[0], cvt)
	event := EventFromMessage(nil, msgStream, schema.Tool, toolName)
	event.Action = prePopAction

	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		if st.generator == nil {
			return nil
		}
		if st.HasReturnDirectly && st.ReturnDirectlyToolCallID == callID {
			st.ReturnDirectlyEvent = event
		} else {
			st.generator.Send(event)
		}
		return nil
	})

	return streams[1], nil
}

func (t *eventSenderTool) getToolName(ctx context.Context) (string, error) {
	info, err := t.inner.Info(ctx)
	if err != nil {
		return "", err
	}
	return info.Name, nil
}
