/*
 * Copyright 2026 CloudWeGo Authors
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

package main

import (
	"context"
	"log"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type toolWrapMiddleware struct {
	adk.BaseChatModelAgentMiddleware
}

func NewToolWrapMiddleware() adk.ChatModelAgentMiddleware {
	return &toolWrapMiddleware{}
}

type toolWrapBaseModel struct {
	inner model.BaseChatModel
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(rs[:max-1]) + "…"
}

func printInputMessagesIfContains(ctx context.Context, stage string, input []*schema.Message) {
	// if !printflag {
	return
	// }
	log.Printf("=========================================================")
	for idx, msg := range input {
		jsonStr, _ := sonic.MarshalString(msg)
		logStr := truncateRunes(jsonStr, 50000)

		ok := strings.Contains(msg.Content, "system-reminder")
		if ok {
			logStr = msg.Content
		}
		log.Printf("[%s] input(%d) :\n%s", stage, idx, logStr)
	}
	log.Printf("=========================================================\n")
}

func (w *toolWrapBaseModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	printInputMessagesIfContains(ctx, "Generate", input)
	return w.inner.Generate(ctx, input, opts...)
}

func (w *toolWrapBaseModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	printInputMessagesIfContains(ctx, "Stream", input)
	return w.inner.Stream(ctx, input, opts...)
}

func (b *toolWrapMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		// log.Printf("Tool %s (call %s) starting with args: %s", tCtx.Name, tCtx.CallID, argumentsInJSON)

		result, err := endpoint(ctx, argumentsInJSON, opts...)

		if err != nil {
			log.Printf("Tool %s failed: %v", tCtx.Name, err)
			return err.Error(), nil
		}

		// log.Printf("Tool %s completed with result: %s", tCtx.Name, result)
		return result, nil
	}, nil

}

func (b *toolWrapMiddleware) WrapStreamableToolCall(ctx context.Context, endpoint adk.StreamableToolCallEndpoint,
	tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		log.Printf("Tool %s (call %s) starting with args: %s", tCtx.Name, tCtx.CallID, argumentsInJSON)

		result, err := endpoint(ctx, argumentsInJSON, opts...)

		if err != nil {
			// log.Printf("Tool %s failed: %v", tCtx.Name, err)
			return schema.StreamReaderFromArray([]string{err.Error()}), nil
		}

		return result, nil
	}, nil
}

func (b *toolWrapMiddleware) WrapModel(_ context.Context, m model.BaseChatModel, _ *adk.ModelContext) (model.BaseChatModel, error) {
	return &toolWrapBaseModel{inner: m}, nil
}
