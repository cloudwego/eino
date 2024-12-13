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

package document

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

// TransformerCallbackInput is the input for the transformer callback.
type TransformerCallbackInput struct {
	// Input is the input documents.
	Input []*schema.Document

	// Extra is the extra information for the callback.
	Extra map[string]any
}

// TransformerCallbackOutput is the output for the transformer callback.
type TransformerCallbackOutput struct {
	// Output is the output documents.
	Output []*schema.Document

	// Extra is the extra information for the callback.
	Extra map[string]any
}

// ConvTransformerCallbackInput converts the callback input to the transformer callback input.
func ConvTransformerCallbackInput(src callbacks.CallbackInput) *TransformerCallbackInput {
	switch t := src.(type) {
	case *TransformerCallbackInput:
		return t
	case []*schema.Document:
		return &TransformerCallbackInput{
			Input: t,
		}
	default:
		return nil
	}
}

// ConvTransformerCallbackOutput converts the callback output to the transformer callback output.
func ConvTransformerCallbackOutput(src callbacks.CallbackOutput) *TransformerCallbackOutput {
	switch t := src.(type) {
	case *TransformerCallbackOutput:
		return t
	case []*schema.Document:
		return &TransformerCallbackOutput{
			Output: t,
		}
	default:
		return nil
	}
}

// TransformerCallbackHandler is the handler for the transformer callback.
type TransformerCallbackHandler struct {
	OnStart func(ctx context.Context, runInfo *callbacks.RunInfo, input *TransformerCallbackInput) context.Context
	OnEnd   func(ctx context.Context, runInfo *callbacks.RunInfo, output *TransformerCallbackOutput) context.Context
	OnError func(ctx context.Context, runInfo *callbacks.RunInfo, err error) context.Context
}

// Needed checks if the callback handler is needed for the given timing.
func (ch *TransformerCallbackHandler) Needed(ctx context.Context, runInfo *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
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