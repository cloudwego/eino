/*
 * Copyright 2024 CloudWeGo Authors
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

package reranker

import (
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

// CallbackInput is the input for the reranker callback.
type CallbackInput struct {
	// Request holds the core reranker request payload.
	*Request

	// Config carries model-specific configuration for the reranker.
	Config *Config

	// TopK is the suggested top-k for reranking.
	TopK int

	// ScoreThreshold is an optional score threshold for filtering.
	ScoreThreshold *float64

	// Extra holds extra, implementation-specific information passed to callbacks.
	Extra map[string]any
}

// TokenUsage describes the token usage for a reranker invocation.
type TokenUsage struct {
	// PromptTokens is the number of tokens used in the prompt.
	PromptTokens int
	// CompletionTokens is the number of tokens produced by the reranker.
	CompletionTokens int
	// TotalTokens is the total number of tokens consumed.
	TotalTokens int
}

// CallbackOutput is the output for the reranker callback.
type CallbackOutput struct {
	// Docs is the reranked documents.
	Docs []*schema.Document

	// Config echoes the configuration used for reranking, when available.
	Config *Config

	// TokenUsage records the token consumption from the underlying model, if any.
	TokenUsage *TokenUsage

	// Extra carries implementation-specific output values.
	Extra map[string]any
}

// Config defines model-specific options for reranker callbacks.
type Config struct {
	// Model identifies the underlying reranker implementation.
	Model string
	// ReturnDocuments controls whether the reranker should include document payloads in responses.
	ReturnDocuments bool
}

// ConvCallbackInput converts the generic callbacks.CallbackInput into *reranker.CallbackInput
func ConvCallbackInput(src callbacks.CallbackInput) *CallbackInput {
	switch t := src.(type) {
	case *CallbackInput:
		return t
	case *Request:
		return &CallbackInput{Request: t}
	default:
		return nil
	}
}

// ConvCallbackOutput converts generic callbacks.CallbackOutput into *reranker.CallbackOutput
func ConvCallbackOutput(src callbacks.CallbackOutput) *CallbackOutput {
	switch t := src.(type) {
	case *CallbackOutput:
		return t
	case []*schema.Document:
		return &CallbackOutput{Docs: t}
	default:
		return nil
	}
}
