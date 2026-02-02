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

// WithChatModelOptions returns an agent option that specifies model.Option for the chat model in agent.
func WithChatModelOptions(opts ...model.Option) agent.AgentOption {
	return agent.WithComposeOptions(compose.WithChatModelOption(opts...))
}

// WithToolList returns an agent option that specifies compose.ToolsNodeOption for ToolsNode in agent.
// If you also need to pass ToolInfo to the chat model, use WithTools instead.
// Deprecated: This changes tool list for ToolsNode ONLY.
func WithToolList(tools ...tool.BaseTool) agent.AgentOption {
	return agent.WithComposeOptions(compose.WithToolsNodeOption(compose.WithToolList(tools...)))
}

// WithTools is a convenience function that configures a React agent with a list of tools.
// It performs two essential operations:
//  1. Extracts tool information for the chat model to understand available tools
//  2. Registers the actual tool implementations for execution
//
// Parameters:
//   - ctx: The context for the operation, used when calling Info() on each tool
//   - tools: A variadic list of tools that must implement either InvokableTool or StreamableTool interfaces
//
// Returns:
//   - []agent.AgentOption: A slice containing exactly 2 agent options:
//   - Option 1: Configures the chat model with tool schemas via model.WithTools(toolInfos)
//   - Option 2: Registers the tool implementations via compose.WithToolList(tools...)
//   - error: Returns an error if any tool's Info() method fails
//
// Usage Example:
//
//	ctx := context.Background()
//	agentOptions, err := WithTools(ctx, myTool1, myTool2, myTool3)
//	if err != nil {
//	    return fmt.Errorf("failed to configure tools: %w", err)
//	}
//
//	agent, err := react.NewAgent(ctx, &react.AgentConfig{
//	    ToolCallingModel: myModel,
//	    // other config...
//	})
//	if err != nil {
//	    return fmt.Errorf("failed to create agent: %w", err)
//	}
//
//	// Use the tool options with Generate or Stream methods
//	msg, err := agent.Generate(ctx, messages, agentOptions...)
//	// or
//	stream, err := agent.Stream(ctx, messages, agentOptions...)
//
// Comparison with Related Functions:
//   - WithToolList: Only registers tool implementations, doesn't configure the chat model
//   - WithTools: Comprehensive setup that handles both chat model configuration and tool registration
//
// Notes:
//   - The function always returns exactly 2 options when successful
//   - Both returned options should be applied to the agent for proper tool functionality
func WithTools(ctx context.Context, tools ...tool.BaseTool) ([]agent.AgentOption, error) {
	toolInfos := make([]*schema.ToolInfo, 0, len(tools))
	for _, tl := range tools {
		info, err := tl.Info(ctx)
		if err != nil {
			return nil, err
		}

		toolInfos = append(toolInfos, info)
	}

	opts := make([]agent.AgentOption, 2)
	opts[0] = agent.WithComposeOptions(compose.WithChatModelOption(model.WithTools(toolInfos)))
	opts[1] = agent.WithComposeOptions(compose.WithToolsNodeOption(compose.WithToolList(tools...)))
	return opts, nil
}

// Iterator provides a lightweight FIFO stream of values and errors
// produced during agent execution.
type Iterator[T any] struct {
	ch *internal.UnboundedChan[item[T]]
}

// Next retrieves the next value from the iterator.
// It returns the zero value and false when the stream is exhausted.
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

// MessageFuture exposes asynchronous accessors for messages produced
// by Generate and Stream calls.
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
	createToolResultSender := func() toolResultSender {
		return func(toolName, callID, result string) {
			msg := schema.ToolMessage(result, callID, schema.WithToolName(toolName))
			h.sendMessage(msg)
		}
	}
	createStreamToolResultSender := func() streamToolResultSender {
		return func(toolName, callID string, resultStream *schema.StreamReader[string]) {
			cvt := func(in string) (*schema.Message, error) {
				return schema.ToolMessage(in, callID, schema.WithToolName(toolName)), nil
			}
			msgStream := schema.StreamReaderWithConvert(resultStream, cvt)
			h.sendMessageStream(msgStream)
		}
	}
	graphHandler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			h.onGraphStart(ctx, info, input)
			return setToolResultSendersToCtx(ctx, createToolResultSender(), createStreamToolResultSender())
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			h.onGraphStartWithStreamInput(ctx, info, input)
			return setToolResultSendersToCtx(ctx, createToolResultSender(), createStreamToolResultSender())
		}).
		OnEndFn(h.onGraphEnd).
		OnEndWithStreamOutputFn(h.onGraphEndWithStreamOutput).
		OnErrorFn(h.onGraphError).Build()
	cb := ub.NewHandlerHelper().ChatModel(cmHandler).Graph(graphHandler).Handler()

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
	input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	input.Close()

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
