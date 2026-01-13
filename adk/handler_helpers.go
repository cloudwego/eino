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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
)

// WithInstruction creates a handler that appends instruction text.
func WithInstruction(text string) HandlerMiddleware {
	return &instructionHandler{text: text}
}

type instructionHandler struct {
	*BaseHandlerMiddleware
	text string
}

func (h *instructionHandler) BeforeAgent(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
	if runCtx.Instruction == "" {
		runCtx.Instruction = h.text
	} else if h.text != "" {
		runCtx.Instruction = runCtx.Instruction + "\n" + h.text
	}
	return ctx, runCtx, nil
}

// WithInstructionFunc creates a handler with custom instruction modification logic.
func WithInstructionFunc(fn func(ctx context.Context, instruction string) (context.Context, string, error)) HandlerMiddleware {
	return &instructionFuncHandler{fn: fn}
}

type instructionFuncHandler struct {
	*BaseHandlerMiddleware
	fn func(ctx context.Context, instruction string) (context.Context, string, error)
}

func (h *instructionFuncHandler) BeforeAgent(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
	newCtx, newInstruction, err := h.fn(ctx, runCtx.Instruction)
	if err != nil {
		return ctx, runCtx, err
	}
	runCtx.Instruction = newInstruction
	return newCtx, runCtx, nil
}

// WithTools creates a handler that adds tools.
func WithTools(tools ...tool.BaseTool) HandlerMiddleware {
	return &toolsHandler{tools: tools}
}

type toolsHandler struct {
	*BaseHandlerMiddleware
	tools []tool.BaseTool
}

func (h *toolsHandler) BeforeAgent(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
	runCtx.Tools = append(runCtx.Tools, h.tools...)
	return ctx, runCtx, nil
}

// WithToolsFunc creates a handler with custom tools modification logic.
func WithToolsFunc(fn func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]struct{}) (context.Context, []tool.BaseTool, map[string]struct{}, error)) HandlerMiddleware {
	return &toolsFuncHandler{fn: fn}
}

type toolsFuncHandler struct {
	*BaseHandlerMiddleware
	fn func(ctx context.Context, tools []tool.BaseTool, returnDirectly map[string]struct{}) (context.Context, []tool.BaseTool, map[string]struct{}, error)
}

func (h *toolsFuncHandler) BeforeAgent(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
	newCtx, newTools, newReturnDirectly, err := h.fn(ctx, runCtx.Tools, runCtx.ReturnDirectly)
	if err != nil {
		return ctx, runCtx, err
	}
	runCtx.Tools = newTools
	runCtx.ReturnDirectly = newReturnDirectly
	return newCtx, runCtx, nil
}

// WithBeforeAgent creates a handler with a generic BeforeAgent hook.
func WithBeforeAgent(fn func(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error)) HandlerMiddleware {
	return &beforeHandlerMiddleware{fn: fn}
}

type beforeHandlerMiddleware struct {
	*BaseHandlerMiddleware
	fn func(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error)
}

func (h *beforeHandlerMiddleware) BeforeAgent(ctx context.Context, runCtx *AgentContext) (context.Context, *AgentContext, error) {
	return h.fn(ctx, runCtx)
}

// WithBeforeModelRewriteHistory creates a handler that processes messages before model invocation.
func WithBeforeModelRewriteHistory(fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)) HandlerMiddleware {
	return &beforeModelRewriteHistoryHandler{fn: fn}
}

type beforeModelRewriteHistoryHandler struct {
	*BaseHandlerMiddleware
	fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)
}

func (h *beforeModelRewriteHistoryHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return h.fn(ctx, messages)
}

// WithAfterModelRewriteHistory creates a handler that processes messages after model invocation.
func WithAfterModelRewriteHistory(fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)) HandlerMiddleware {
	return &afterModelRewriteHistoryHandler{fn: fn}
}

type afterModelRewriteHistoryHandler struct {
	*BaseHandlerMiddleware
	fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)
}

func (h *afterModelRewriteHistoryHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return h.fn(ctx, messages)
}

// WithToolWrapper creates a handler that wraps tools.
// The wrapper function is converted to compose.ToolMiddleware internally.
func WithToolWrapper(fn func(context.Context, tool.BaseTool) (tool.BaseTool, error)) HandlerMiddleware {
	return &toolWrapperHandler{fn: fn}
}

type toolWrapperHandler struct {
	*BaseHandlerMiddleware
	fn func(context.Context, tool.BaseTool) (tool.BaseTool, error)
}

func (h *toolWrapperHandler) WrapTool(ctx context.Context, t tool.BaseTool) (tool.BaseTool, error) {
	return h.fn(ctx, t)
}

// WithModelWrapper creates a handler that wraps the chat model.
// The wrapper is applied once when the agent is built, not per-call.
func WithModelWrapper(fn func(context.Context, model.BaseChatModel) (model.BaseChatModel, error)) HandlerMiddleware {
	return &modelWrapperHandler{fn: fn}
}

type modelWrapperHandler struct {
	*BaseHandlerMiddleware
	fn func(context.Context, model.BaseChatModel) (model.BaseChatModel, error)
}

func (h *modelWrapperHandler) WrapModel(ctx context.Context, m model.BaseChatModel) (model.BaseChatModel, error) {
	return h.fn(ctx, m)
}
