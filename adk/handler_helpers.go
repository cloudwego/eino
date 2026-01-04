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
)

func WithInstruction(text string) AgentHandler {
	return &instructionHandler{
		BaseAgentHandler: NewBaseAgentHandler("instruction"),
		text:             text,
	}
}

type instructionHandler struct {
	BaseAgentHandler
	text string
}

func (h *instructionHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
	if config.Instruction == "" {
		config.Instruction = h.text
	} else if h.text != "" {
		config.Instruction = config.Instruction + "\n" + h.text
	}
	return ctx, nil
}

func WithInstructionFunc(fn func(ctx context.Context, instruction string) (context.Context, string, error)) AgentHandler {
	return &instructionFuncHandler{
		BaseAgentHandler: NewBaseAgentHandler("instruction-func"),
		fn:               fn,
	}
}

type instructionFuncHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, instruction string) (context.Context, string, error)
}

func (h *instructionFuncHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
	newCtx, newInstruction, err := h.fn(ctx, config.Instruction)
	if err != nil {
		return ctx, err
	}
	config.Instruction = newInstruction
	return newCtx, nil
}

func WithTools(tools ...tool.BaseTool) AgentHandler {
	return &toolsHandler{
		BaseAgentHandler: NewBaseAgentHandler("tools"),
		tools:            tools,
	}
}

type toolsHandler struct {
	BaseAgentHandler
	tools []tool.BaseTool
}

func (h *toolsHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
	for _, t := range h.tools {
		config.Tools = append(config.Tools, ToolMeta{Tool: t, ReturnDirectly: false})
	}
	return ctx, nil
}

func WithToolsFunc(fn func(ctx context.Context, tools []ToolMeta) (context.Context, []ToolMeta, error)) AgentHandler {
	return &toolsFuncHandler{
		BaseAgentHandler: NewBaseAgentHandler("tools-func"),
		fn:               fn,
	}
}

type toolsFuncHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, tools []ToolMeta) (context.Context, []ToolMeta, error)
}

func (h *toolsFuncHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
	newCtx, newTools, err := h.fn(ctx, config.Tools)
	if err != nil {
		return ctx, err
	}
	config.Tools = newTools
	return newCtx, nil
}

func WithBeforeAgent(fn func(ctx context.Context, config *AgentConfig) (context.Context, error)) AgentHandler {
	return &beforeAgentHandler{
		BaseAgentHandler: NewBaseAgentHandler("before-agent"),
		fn:               fn,
	}
}

type beforeAgentHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, config *AgentConfig) (context.Context, error)
}

func (h *beforeAgentHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
	return h.fn(ctx, config)
}

func WithBeforeModelRewriteHistory(fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)) AgentHandler {
	return &beforeModelRewriteHistoryHandler{
		BaseAgentHandler: NewBaseAgentHandler("before-model-rewrite-history"),
		fn:               fn,
	}
}

type beforeModelRewriteHistoryHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)
}

func (h *beforeModelRewriteHistoryHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return h.fn(ctx, messages)
}

func WithAfterModelRewriteHistory(fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)) AgentHandler {
	return &afterModelRewriteHistoryHandler{
		BaseAgentHandler: NewBaseAgentHandler("after-model-rewrite-history"),
		fn:               fn,
	}
}

type afterModelRewriteHistoryHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)
}

func (h *afterModelRewriteHistoryHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return h.fn(ctx, messages)
}

func WithInvokableToolWrapper(fn func(ctx context.Context, input *ToolCallInput, next func(context.Context, *ToolCallInput) (*ToolCallResult, error)) (*ToolCallResult, error)) AgentHandler {
	return &invokableToolWrapperHandler{
		BaseAgentHandler: NewBaseAgentHandler("invokable-tool-wrapper"),
		fn:               fn,
	}
}

type invokableToolWrapperHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, input *ToolCallInput, next func(context.Context, *ToolCallInput) (*ToolCallResult, error)) (*ToolCallResult, error)
}

func (h *invokableToolWrapperHandler) WrapInvokableToolCall(ctx context.Context, input *ToolCallInput, next func(context.Context, *ToolCallInput) (*ToolCallResult, error)) (*ToolCallResult, error) {
	return h.fn(ctx, input, next)
}

func WithStreamableToolWrapper(fn func(ctx context.Context, input *ToolCallInput, next func(context.Context, *ToolCallInput) (*StreamToolCallResult, error)) (*StreamToolCallResult, error)) AgentHandler {
	return &streamableToolWrapperHandler{
		BaseAgentHandler: NewBaseAgentHandler("streamable-tool-wrapper"),
		fn:               fn,
	}
}

type streamableToolWrapperHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, input *ToolCallInput, next func(context.Context, *ToolCallInput) (*StreamToolCallResult, error)) (*StreamToolCallResult, error)
}

func (h *streamableToolWrapperHandler) WrapStreamableToolCall(ctx context.Context, input *ToolCallInput, next func(context.Context, *ToolCallInput) (*StreamToolCallResult, error)) (*StreamToolCallResult, error) {
	return h.fn(ctx, input, next)
}
