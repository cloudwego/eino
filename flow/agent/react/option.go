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

package react

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/schema"
	ub "github.com/cloudwego/eino/utils/callbacks"
)

// WithToolOptions returns an agent option that specifies tool.Option for the tools in agent.
func WithToolOptions(opts ...tool.Option) agent.AgentOption {
	return agent.WithComposeOptions(compose.WithToolsNodeOption(compose.WithToolOption(opts...)))
}

// WithToolList returns an agent option that specifies the list of tools can be called which are BaseTool but must implement InvokableTool or StreamableTool.
func WithToolList(tools ...tool.BaseTool) agent.AgentOption {
	return agent.WithComposeOptions(compose.WithToolsNodeOption(compose.WithToolList(tools...)))
}

type Iterator[T any] struct {
	ch *internal.UnboundedChan[item[T]]
}

func (iter *Iterator[T]) Next() (T, bool, error) {
	ch := iter.ch
	if ch == nil {
		var zero T
		return zero, false, nil
	}

	i, ok := ch.Receive()
	if !ok {
		var zero T
		return zero, false, nil
	}

	return i.v, true, i.err
}

type MessageFuture interface {
	// GetMessages returns an iterator for retrieving messages generated during "agent.Generate" calls.
	GetMessages() *Iterator[*schema.Message]

	// GetMessageStreams returns an iterator for retrieving streaming messages generated during "agent.Stream" calls.
	GetMessageStreams() *Iterator[*schema.StreamReader[*schema.Message]]
}

// WithMessageFuture returns an agent option and a MessageFuture interface instance.
// The option configures the agent to collect messages generated during execution,
// while the MessageFuture interface allows users to asynchronously retrieve these messages.
func WithMessageFuture() (agent.AgentOption, MessageFuture) {
	h := &cbHandler{started: make(chan struct{})}

	cmHandler := &ub.ModelCallbackHandler{
		OnEnd:                 h.onChatModelEnd,
		OnEndWithStreamOutput: h.onChatModelEndWithStreamOutput,
	}
	toolHandler := &ub.ToolCallbackHandler{
		OnEnd:                 h.onToolEnd,
		OnEndWithStreamOutput: h.onToolEndWithStreamOutput,
	}
	graphHandler := callbacks.NewHandlerBuilder().
		OnStartFn(h.onGraphStart).
		OnStartWithStreamInputFn(h.onGraphStartWithStreamInput).
		OnEndFn(h.onGraphEnd).
		OnEndWithStreamOutputFn(h.onGraphEndWithStreamOutput).
		OnErrorFn(h.onGraphError).Build()
	cb := ub.NewHandlerHelper().ChatModel(cmHandler).Tool(toolHandler).Graph(graphHandler).Handler()

	option := agent.WithComposeOptions(compose.WithCallbacks(cb))

	return option, h
}

type item[T any] struct {
	v   T
	err error
}

type cbHandler struct {
	msgs  *internal.UnboundedChan[item[*schema.Message]]
	sMsgs *internal.UnboundedChan[item[*schema.StreamReader[*schema.Message]]]

	started chan struct{}
}

func (h *cbHandler) GetMessages() *Iterator[*schema.Message] {
	<-h.started

	return &Iterator[*schema.Message]{ch: h.msgs}
}

func (h *cbHandler) GetMessageStreams() *Iterator[*schema.StreamReader[*schema.Message]] {
	<-h.started

	return &Iterator[*schema.StreamReader[*schema.Message]]{ch: h.sMsgs}
}

func (h *cbHandler) onChatModelEnd(ctx context.Context,
	_ *callbacks.RunInfo, input *model.CallbackOutput) context.Context {

	h.sendMessage(input.Message)

	return ctx
}

func (h *cbHandler) onChatModelEndWithStreamOutput(ctx context.Context,
	_ *callbacks.RunInfo, input *schema.StreamReader[*model.CallbackOutput]) context.Context {

	c := func(output *model.CallbackOutput) (*schema.Message, error) {
		return output.Message, nil
	}
	s := schema.StreamReaderWithConvert(input, c)

	h.sendMessageStream(s)

	return ctx
}

func (h *cbHandler) onToolEnd(ctx context.Context,
	info *callbacks.RunInfo, input *tool.CallbackOutput) context.Context {

	toolCallID := compose.GetToolCallID(ctx)
	toolName := ""
	if info != nil {
		toolName = info.Name
	}
	msg := schema.ToolMessage(input.Response, toolCallID, schema.WithToolName(toolName))

	h.sendMessage(msg)

	return ctx
}

func (h *cbHandler) onToolEndWithStreamOutput(ctx context.Context,
	info *callbacks.RunInfo, input *schema.StreamReader[*tool.CallbackOutput]) context.Context {

	toolCallID := compose.GetToolCallID(ctx)
	toolName := ""
	if info != nil {
		toolName = info.Name
	}
	c := func(output *tool.CallbackOutput) (*schema.Message, error) {
		return schema.ToolMessage(output.Response, toolCallID, schema.WithToolName(toolName)), nil
	}
	s := schema.StreamReaderWithConvert(input, c)

	h.sendMessageStream(s)

	return ctx
}

func (h *cbHandler) onGraphError(ctx context.Context,
	_ *callbacks.RunInfo, err error) context.Context {

	if h.msgs != nil {
		h.msgs.Send(item[*schema.Message]{err: err})
	} else {
		h.sMsgs.Send(item[*schema.StreamReader[*schema.Message]]{err: err})
	}

	return ctx
}

func (h *cbHandler) onGraphEnd(ctx context.Context,
	_ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {

	h.msgs.Close()

	return ctx
}

func (h *cbHandler) onGraphEndWithStreamOutput(ctx context.Context,
	_ *callbacks.RunInfo, _ *schema.StreamReader[callbacks.CallbackOutput]) context.Context {

	h.sMsgs.Close()

	return ctx
}

func (h *cbHandler) onGraphStart(ctx context.Context,
	_ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {

	h.msgs = internal.NewUnboundedChan[item[*schema.Message]]()

	close(h.started)

	return ctx
}

func (h *cbHandler) onGraphStartWithStreamInput(ctx context.Context, _ *callbacks.RunInfo,
	_ *schema.StreamReader[callbacks.CallbackInput]) context.Context {

	h.sMsgs = internal.NewUnboundedChan[item[*schema.StreamReader[*schema.Message]]]()

	close(h.started)

	return ctx
}

func (h *cbHandler) sendMessage(msg *schema.Message) {
	if h.msgs != nil {
		h.msgs.Send(item[*schema.Message]{v: msg})
	} else {
		sMsg := schema.StreamReaderFromArray([]*schema.Message{msg})
		h.sMsgs.Send(item[*schema.StreamReader[*schema.Message]]{v: sMsg})
	}
}

func (h *cbHandler) sendMessageStream(sMsg *schema.StreamReader[*schema.Message]) {
	if h.sMsgs != nil {
		h.sMsgs.Send(item[*schema.StreamReader[*schema.Message]]{v: sMsg})
	} else {
		// concat
		msg, err := schema.ConcatMessageStream(sMsg)

		if err != nil {
			h.msgs.Send(item[*schema.Message]{err: err})
		} else {
			h.msgs.Send(item[*schema.Message]{v: msg})
		}
	}
}
