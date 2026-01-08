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

// WithInstruction creates a handler that appends instruction text.
func WithInstruction(text string) AgentHandler {
	return &instructionHandler{text: text}
}

type instructionHandler struct {
	BaseAgentHandler
	text string
}

func (h *instructionHandler) BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
	if runCtx.Instruction == "" {
		runCtx.Instruction = h.text
	} else if h.text != "" {
		runCtx.Instruction = runCtx.Instruction + "\n" + h.text
	}
	return ctx, runCtx, nil
}

// WithInstructionFunc creates a handler with custom instruction modification logic.
func WithInstructionFunc(fn func(ctx context.Context, instruction string) (context.Context, string, error)) AgentHandler {
	return &instructionFuncHandler{fn: fn}
}

type instructionFuncHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, instruction string) (context.Context, string, error)
}

func (h *instructionFuncHandler) BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
	newCtx, newInstruction, err := h.fn(ctx, runCtx.Instruction)
	if err != nil {
		return ctx, runCtx, err
	}
	runCtx.Instruction = newInstruction
	return newCtx, runCtx, nil
}

// WithTools creates a handler that adds tools.
func WithTools(tools ...tool.BaseTool) AgentHandler {
	return &toolsHandler{tools: tools}
}

type toolsHandler struct {
	BaseAgentHandler
	tools []tool.BaseTool
}

func (h *toolsHandler) BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
	runCtx.Tools = append(runCtx.Tools, h.tools...)
	return ctx, runCtx, nil
}

// WithToolsFunc creates a handler with custom tools modification logic.
func WithToolsFunc(fn func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]struct{}) (context.Context, []tool.BaseTool, map[string]struct{}, error)) AgentHandler {
	return &toolsFuncHandler{fn: fn}
}

type toolsFuncHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]struct{}) (context.Context, []tool.BaseTool, map[string]struct{}, error)
}

func (h *toolsFuncHandler) BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
	newCtx, newTools, newReturnDirectly, err := h.fn(ctx, runCtx.Tools, runCtx.ReturnDirectly)
	if err != nil {
		return ctx, runCtx, err
	}
	runCtx.Tools = newTools
	runCtx.ReturnDirectly = newReturnDirectly
	return newCtx, runCtx, nil
}

// WithBeforeAgent creates a handler with a generic BeforeAgent hook.
func WithBeforeAgent(fn func(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error)) AgentHandler {
	return &beforeAgentHandler{fn: fn}
}

type beforeAgentHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error)
}

func (h *beforeAgentHandler) BeforeAgent(ctx context.Context, runCtx *AgentRunContext) (context.Context, *AgentRunContext, error) {
	return h.fn(ctx, runCtx)
}

// WithBeforeModelRewriteHistory creates a handler that processes messages before model invocation.
func WithBeforeModelRewriteHistory(fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)) AgentHandler {
	return &beforeModelRewriteHistoryHandler{fn: fn}
}

type beforeModelRewriteHistoryHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)
}

func (h *beforeModelRewriteHistoryHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return h.fn(ctx, messages)
}

// WithAfterModelRewriteHistory creates a handler that processes messages after model invocation.
func WithAfterModelRewriteHistory(fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)) AgentHandler {
	return &afterModelRewriteHistoryHandler{fn: fn}
}

type afterModelRewriteHistoryHandler struct {
	BaseAgentHandler
	fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)
}

func (h *afterModelRewriteHistoryHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return h.fn(ctx, messages)
}

// WithToolCallWrapper creates a handler that wraps tool calls.
func WithToolCallWrapper(wrapper ToolCallWrapper) AgentHandler {
	return &toolCallWrapperHandler{wrapper: wrapper}
}

type toolCallWrapperHandler struct {
	BaseAgentHandler
	wrapper ToolCallWrapper
}

func (h *toolCallWrapperHandler) GetToolCallWrapper() ToolCallWrapper {
	return h.wrapper
}

// WithModelCallWrapper creates a handler that wraps model calls.
func WithModelCallWrapper(wrapper ModelCallWrapper) AgentHandler {
	return &modelCallWrapperHandler{wrapper: wrapper}
}

type modelCallWrapperHandler struct {
	BaseAgentHandler
	wrapper ModelCallWrapper
}

func (h *modelCallWrapperHandler) GetModelCallWrapper() ModelCallWrapper {
	return h.wrapper
}
