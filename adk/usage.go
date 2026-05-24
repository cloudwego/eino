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

package adk

import "github.com/cloudwego/eino/schema"

func assistantTokenUsage[M MessageType](msg M) *schema.TokenUsage {
	switch v := any(msg).(type) {
	case *schema.Message:
		if v == nil || v.Role != schema.Assistant || v.ResponseMeta == nil {
			return nil
		}
		return v.ResponseMeta.Usage
	case *schema.AgenticMessage:
		if v == nil || v.Role != schema.AgenticRoleTypeAssistant || v.ResponseMeta == nil {
			return nil
		}
		return v.ResponseMeta.TokenUsage
	default:
		return nil
	}
}

func assistantFinishReason[M MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		if v == nil || v.Role != schema.Assistant || v.ResponseMeta == nil {
			return ""
		}
		return v.ResponseMeta.FinishReason
	case *schema.AgenticMessage:
		if v == nil || v.Role != schema.AgenticRoleTypeAssistant || v.ResponseMeta == nil {
			return ""
		}
		if v.ResponseMeta.ClaudeExtension != nil {
			return v.ResponseMeta.ClaudeExtension.StopReason
		}
		if v.ResponseMeta.GeminiExtension != nil {
			return v.ResponseMeta.GeminiExtension.FinishReason
		}
		return ""
	default:
		return ""
	}
}

func modelUsageFromAssistant[M MessageType](msg M) *ModelUsage {
	usage := assistantTokenUsage(msg)
	if usage == nil {
		return nil
	}
	return &ModelUsage{
		InputTokens:          usage.PromptTokens,
		OutputTokens:         usage.CompletionTokens,
		CacheReadInputTokens: usage.PromptTokenDetails.CachedTokens,
		Raw:                  usage,
	}
}
