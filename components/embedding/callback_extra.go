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

// Package embedding defines callback payloads for embedding components.
package embedding

import (
	"github.com/cloudwego/eino/callbacks"
)

// TokenUsage is the token usage for the embedding.
type TokenUsage struct {
	// PromptTokens is the number of prompt tokens.
	PromptTokens int
	// CompletionTokens is the number of completion tokens.
	CompletionTokens int
	// TotalTokens is the total number of tokens.
	TotalTokens int
}

// Config is the config for the embedding.
type Config struct {
	// Model is the model name.
	Model string
	// EncodingFormat is the encoding format.
	EncodingFormat string
}

// ComponentExtra is the extra information for the embedding.
type ComponentExtra struct {
	// Config is the config for the embedding.
	Config *Config
	// TokenUsage is the token usage for the embedding.
	TokenUsage *TokenUsage
}

// CallbackInput is the input for the embedding callback.
type CallbackInput struct {
	// Texts is the texts to be embedded.
	Texts []string
	// Config is the config for the embedding.
	Config *Config
	// Extra is the extra information for the callback.
	Extra map[string]any
}

// CallbackOutput is the output for the embedding callback.
type CallbackOutput struct {
	// Embeddings is the embeddings.
	Embeddings [][]float64
	// Config is the config for creating the embedding.
	Config *Config
	// TokenUsage is the token usage for the embedding.
	TokenUsage *TokenUsage
	// Extra is the extra information for the callback.
	Extra map[string]any
}

// ConvCallbackInput converts the callback input to the embedding callback input.
func ConvCallbackInput(src callbacks.CallbackInput) *CallbackInput {
	switch t := src.(type) {
	case *CallbackInput:
		return t
	case []string:
		return &CallbackInput{
			Texts: t,
		}
	default:
		return nil
	}
}

// ConvCallbackOutput converts the callback output to the embedding callback output.
func ConvCallbackOutput(src callbacks.CallbackOutput) *CallbackOutput {
	switch t := src.(type) {
	case *CallbackOutput:
		return t
	case [][]float64:
		return &CallbackOutput{
			Embeddings: t,
		}
	default:
		return nil
	}
}
