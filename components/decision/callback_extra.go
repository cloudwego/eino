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

package decision

import (
	"github.com/cloudwego/eino/callbacks"
)

// CallbackInput is the input for the decision callback.
type CallbackInput struct {
	// State is the state evaluated by the decider.
	State any
	// Questions are the questions to evaluate, keyed by question id.
	Questions map[string]Question
	// Extra is the extra information for the decision.
	Extra map[string]any
}

// CallbackOutput is the output for the decision callback.
type CallbackOutput struct {
	// Answers is the answers of the decision, keyed by question id.
	Answers map[string]*Answer
	// Extra is the extra information for the decision, eg. the response model and token usage.
	Extra map[string]any
}

// ConvCallbackInput converts the callback input to the decision callback input.
func ConvCallbackInput(src callbacks.CallbackInput) *CallbackInput {
	switch t := src.(type) {
	case *CallbackInput:
		return t
	case *NodeInput:
		return &CallbackInput{
			State:     t.State,
			Questions: t.Questions,
		}
	default:
		return nil
	}
}

// ConvCallbackOutput converts the callback output to the decision callback output.
func ConvCallbackOutput(src callbacks.CallbackOutput) *CallbackOutput {
	switch t := src.(type) {
	case *CallbackOutput:
		return t
	default:
		return nil
	}
}
