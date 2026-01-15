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

package prompt

import (
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

// AgenticCallbackInput is the input for the callback.
type AgenticCallbackInput struct {
	// Variables is the variables for the callback.
	Variables map[string]any
	// AgenticTemplates is the agentic templates for the callback.
	AgenticTemplates []schema.AgenticMessagesTemplate
	// Extra is the extra information for the callback.
	Extra map[string]any
}

// AgenticCallbackOutput is the output for the callback.
type AgenticCallbackOutput struct {
	// AgenticResult is the agentic result for the callback.
	AgenticResult []*schema.AgenticMessage
	// AgenticTemplates is the agentic templates for the callback.
	AgenticTemplates []schema.AgenticMessagesTemplate
	// Extra is the extra information for the callback.
	Extra map[string]any
}

// ConvAgenticCallbackInput converts the callback input to the agentic prompt callback input.
func ConvAgenticCallbackInput(src callbacks.CallbackInput) *AgenticCallbackInput {
	switch t := src.(type) {
	case *AgenticCallbackInput:
		return t
	case map[string]any:
		return &AgenticCallbackInput{
			Variables: t,
		}
	default:
		return nil
	}
}

// ConvAgenticCallbackOutput converts the callback output to the agentic prompt callback output.
func ConvAgenticCallbackOutput(src callbacks.CallbackOutput) *AgenticCallbackOutput {
	switch t := src.(type) {
	case *AgenticCallbackOutput:
		return t
	case []*schema.AgenticMessage:
		return &AgenticCallbackOutput{
			AgenticResult: t,
		}
	default:
		return nil
	}
}
