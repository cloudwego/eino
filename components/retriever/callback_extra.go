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

package retriever

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

// CallbackInput is the input for the retriever callback.
type CallbackInput struct {
	// Query is the query for the retriever.
	Query string

	// TopK is the top k for the retriever, which means the top number of documents to retrieve.
	TopK int
	// Filter is the filter for the retriever.
	Filter string
	// ScoreThreshold is the score threshold for the retriever, eg 0.5 means the score of the document must be greater than 0.5.
	ScoreThreshold *float64

	// Extra is the extra information for the retriever.
	Extra map[string]any
}

// CallbackOutput is the output for the retriever callback.
type CallbackOutput struct {
	// Docs is the documents for the retriever.
	Docs []*schema.Document
	// Extra is the extra information for the retriever.
	Extra map[string]any
}

// ConvCallbackInput converts the callback input to the retriever callback input.
func ConvCallbackInput(src callbacks.CallbackInput) *CallbackInput {
	switch t := src.(type) {
	case *CallbackInput:
		return t
	case string:
		return &CallbackInput{
			Query: t,
		}
	default:
		return nil
	}
}

// ConvCallbackOutput converts the callback output to the retriever callback output.
func ConvCallbackOutput(src callbacks.CallbackOutput) *CallbackOutput {
	switch t := src.(type) {
	case *CallbackOutput:
		return t
	case []*schema.Document:
		return &CallbackOutput{
			Docs: t,
		}
	default:
		return nil
	}
}

// CallbackHandler is the handler for the retriever callback.
type CallbackHandler struct {
	// OnStart is the callback function for the start of the retriever.
	OnStart func(ctx context.Context, runInfo *callbacks.RunInfo, input *CallbackInput) context.Context
	// OnEnd is the callback function for the end of the retriever.
	OnEnd func(ctx context.Context, runInfo *callbacks.RunInfo, output *CallbackOutput) context.Context
	// OnError is the callback function for the error of the retriever.
	OnError func(ctx context.Context, runInfo *callbacks.RunInfo, err error) context.Context
}

// Needed checks if the callback handler is needed for the given timing.
func (ch *CallbackHandler) Needed(ctx context.Context, runInfo *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	switch timing {
	case callbacks.TimingOnStart:
		return ch.OnStart != nil
	case callbacks.TimingOnEnd:
		return ch.OnEnd != nil
	case callbacks.TimingOnError:
		return ch.OnError != nil
	default:
		return false
	}
}