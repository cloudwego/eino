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

// Package patchtoolcalls provides a middleware that patches dangling tool calls in the message history.
package patchtoolcalls

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/schema"
)

// Config defines the configuration options for the patch tool calls middleware.
type Config struct {
	// PatchedContentGenerator is an optional custom function to generate the content
	// of patched tool messages. If not provided, a default message will be used.
	//
	// Parameters:
	//   - ctx: the context for the operation
	//   - toolName: the name of the tool that was called
	//   - toolCallID: the id of the tool call
	//
	// Returns:
	//   - string: the content to use for the patched tool message
	//   - error: any error that occurred during generation
	PatchedContentGenerator func(ctx context.Context, toolName, toolCallID string) (string, error)
}

// New creates a new patch tool calls middleware with the given configuration.
//
// The middleware scans the message history before each model invocation and inserts
// placeholder tool messages for any tool calls that don't have corresponding responses.
func New(ctx context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	return &middleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		gen:                          cfg.PatchedContentGenerator,
	}, nil
}

type middleware struct {
	*adk.BaseChatModelAgentMiddleware
	gen func(ctx context.Context, toolName, toolCallID string) (string, error)
}

func (m *middleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState,
	mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {

	if len(state.Messages) == 0 {
		return ctx, state, nil
	}

	patched := make([]adk.Message, 0, len(state.Messages))

	for i, msg := range state.Messages {
		patched = append(patched, msg)

		if msg.Role != schema.Assistant || len(msg.ToolCalls) == 0 {
			continue
		}

		for _, tc := range msg.ToolCalls {
			if hasCorrespondingToolMessage(state.Messages[i+1:], tc.ID) {
				continue
			}

			toolMsg, err := m.createPatchedToolMessage(ctx, tc)
			if err != nil {
				return ctx, nil, err
			}
			patched = append(patched, toolMsg)
		}
	}

	nState := *state
	nState.Messages = patched
	return ctx, &nState, nil
}

func hasCorrespondingToolMessage(messages []adk.Message, toolCallID string) bool {
	for _, msg := range messages {
		if msg.Role == schema.Tool && msg.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}

func (m *middleware) createPatchedToolMessage(ctx context.Context, tc schema.ToolCall) (adk.Message, error) {
	if m.gen != nil {
		content, err := m.gen(ctx, tc.Function.Name, tc.ID)
		if err != nil {
			return nil, err
		}
		return schema.ToolMessage(content, tc.ID, schema.WithToolName(tc.Function.Name)), nil
	}
	tpl := internal.SelectPrompt(internal.I18nPrompts{
		English: defaultPatchedToolMessageTemplate,
		Chinese: defaultPatchedToolMessageTemplateChinese,
	})

	return schema.ToolMessage(fmt.Sprintf(tpl, tc.Function.Name, tc.ID), tc.ID, schema.WithToolName(tc.Function.Name)), nil
}

const (
	defaultPatchedToolMessageTemplate        = "Tool call %s with id %s was cancelled - another message came in before it could be completed."
	defaultPatchedToolMessageTemplateChinese = "工具调用 %s（ID 为 %s）已被取消——在其完成之前收到了另一条消息。"
)
