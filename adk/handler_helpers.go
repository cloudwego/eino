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

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// WithInstruction creates a handler that appends instruction text.
func WithInstruction(text string) Handler {
	return &instructionHandler{text: text}
}

type instructionHandler struct {
	text string
}

func (h *instructionHandler) Name() string {
	return "instruction"
}

func (h *instructionHandler) ModifyInstruction(_ context.Context, instruction string) (string, error) {
	if instruction == "" {
		return h.text, nil
	}
	if h.text == "" {
		return instruction, nil
	}
	return instruction + "\n" + h.text, nil
}

// WithInstructionFunc creates a handler with custom instruction modification logic.
func WithInstructionFunc(fn func(ctx context.Context, instruction string) (string, error)) Handler {
	return &instructionFuncHandler{fn: fn}
}

type instructionFuncHandler struct {
	fn func(ctx context.Context, instruction string) (string, error)
}

func (h *instructionFuncHandler) Name() string {
	return "instruction-func"
}

func (h *instructionFuncHandler) ModifyInstruction(ctx context.Context, instruction string) (string, error) {
	return h.fn(ctx, instruction)
}

// WithTools creates a handler that adds tools.
func WithTools(tools ...tool.BaseTool) Handler {
	return &toolsHandler{tools: tools}
}

type toolsHandler struct {
	tools []tool.BaseTool
}

func (h *toolsHandler) Name() string {
	return "tools"
}

func (h *toolsHandler) ModifyTools(_ context.Context, config *HandlerToolsConfig) error {
	config.AddTools(h.tools...)
	return nil
}

// WithToolsFunc creates a handler with custom tools modification logic.
func WithToolsFunc(fn func(ctx context.Context, config *HandlerToolsConfig) error) Handler {
	return &toolsFuncHandler{fn: fn}
}

type toolsFuncHandler struct {
	fn func(ctx context.Context, config *HandlerToolsConfig) error
}

func (h *toolsFuncHandler) Name() string {
	return "tools-func"
}

func (h *toolsFuncHandler) ModifyTools(ctx context.Context, config *HandlerToolsConfig) error {
	return h.fn(ctx, config)
}

// WithMessageStatePreProcessor creates a handler that processes and persists messages before chat model invocation.
func WithMessageStatePreProcessor(fn func(ctx context.Context, messages []Message) ([]Message, error)) Handler {
	return &messageStatePreProcessorHandler{fn: fn}
}

type messageStatePreProcessorHandler struct {
	fn func(ctx context.Context, messages []Message) ([]Message, error)
}

func (h *messageStatePreProcessorHandler) Name() string {
	return "message-state-pre-processor"
}

func (h *messageStatePreProcessorHandler) PreProcessMessageState(ctx context.Context, messages []Message) ([]Message, error) {
	return h.fn(ctx, messages)
}

// WithMessageStatePostProcessor creates a handler that processes and persists messages after chat model invocation.
func WithMessageStatePostProcessor(fn func(ctx context.Context, messages []Message) ([]Message, error)) Handler {
	return &messageStatePostProcessorHandler{fn: fn}
}

type messageStatePostProcessorHandler struct {
	fn func(ctx context.Context, messages []Message) ([]Message, error)
}

func (h *messageStatePostProcessorHandler) Name() string {
	return "message-state-post-processor"
}

func (h *messageStatePostProcessorHandler) PostProcessMessageState(ctx context.Context, messages []Message) ([]Message, error) {
	return h.fn(ctx, messages)
}

// WithInvokableToolInterceptor creates a handler that intercepts non-streaming tool calls.
func WithInvokableToolInterceptor(fn func(ctx context.Context, toolName, arguments string, opts []tool.Option, next func(ctx context.Context, arguments string, opts []tool.Option) (string, error)) (string, error)) Handler {
	return &invokableInterceptorHandler{fn: fn}
}

type invokableInterceptorHandler struct {
	fn func(ctx context.Context, toolName, arguments string, opts []tool.Option, next func(ctx context.Context, arguments string, opts []tool.Option) (string, error)) (string, error)
}

func (h *invokableInterceptorHandler) Name() string {
	return "invokable-tool-interceptor"
}

func (h *invokableInterceptorHandler) WrapInvokableToolCall(ctx context.Context, toolName string, arguments string, opts []tool.Option, next func(ctx context.Context, arguments string, opts []tool.Option) (string, error)) (string, error) {
	return h.fn(ctx, toolName, arguments, opts, next)
}

// WithStreamableToolInterceptor creates a handler that intercepts streaming tool calls.
func WithStreamableToolInterceptor(fn func(ctx context.Context, toolName, arguments string, opts []tool.Option, next func(ctx context.Context, arguments string, opts []tool.Option) (*schema.StreamReader[string], error)) (*schema.StreamReader[string], error)) Handler {
	return &streamableInterceptorHandler{fn: fn}
}

type streamableInterceptorHandler struct {
	fn func(ctx context.Context, toolName, arguments string, opts []tool.Option, next func(ctx context.Context, arguments string, opts []tool.Option) (*schema.StreamReader[string], error)) (*schema.StreamReader[string], error)
}

func (h *streamableInterceptorHandler) Name() string {
	return "streamable-tool-interceptor"
}

func (h *streamableInterceptorHandler) WrapStreamableToolCall(ctx context.Context, toolName string, arguments string, opts []tool.Option, next func(ctx context.Context, arguments string, opts []tool.Option) (*schema.StreamReader[string], error)) (*schema.StreamReader[string], error) {
	return h.fn(ctx, toolName, arguments, opts, next)
}
