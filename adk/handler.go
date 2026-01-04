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

	"github.com/cloudwego/eino/compose"
)

type ToolCallInput = compose.ToolInput
type ToolCallResult = compose.ToolOutput
type StreamToolCallResult = compose.StreamToolOutput

type AgentHandler interface {
	Name() string

	BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error)

	BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)
	AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)

	WrapInvokableToolCall(
		ctx context.Context,
		input *ToolCallInput,
		next func(context.Context, *ToolCallInput) (*ToolCallResult, error),
	) (*ToolCallResult, error)

	WrapStreamableToolCall(
		ctx context.Context,
		input *ToolCallInput,
		next func(context.Context, *ToolCallInput) (*StreamToolCallResult, error),
	) (*StreamToolCallResult, error)
}

type BaseAgentHandler struct {
	name string
}

func NewBaseAgentHandler(name string) BaseAgentHandler {
	return BaseAgentHandler{name: name}
}

func (b BaseAgentHandler) Name() string {
	return b.name
}

func (b BaseAgentHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
	return ctx, nil
}

func (b BaseAgentHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

func (b BaseAgentHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
	return ctx, messages, nil
}

func (b BaseAgentHandler) WrapInvokableToolCall(
	ctx context.Context,
	input *ToolCallInput,
	next func(context.Context, *ToolCallInput) (*ToolCallResult, error),
) (*ToolCallResult, error) {
	return next(ctx, input)
}

func (b BaseAgentHandler) WrapStreamableToolCall(
	ctx context.Context,
	input *ToolCallInput,
	next func(context.Context, *ToolCallInput) (*StreamToolCallResult, error),
) (*StreamToolCallResult, error) {
	return next(ctx, input)
}
