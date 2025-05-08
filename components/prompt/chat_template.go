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

package prompt

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/schema"
)

// DefaultChatTemplate is the default chat template implementation.
type DefaultChatTemplate struct {
	// templates is the templates for the chat template.
	templates []schema.MessagesTemplate
	// formatType is the format type for the chat template.
	formatType schema.FormatType
}

// FromMessages creates a new DefaultChatTemplate from the given templates and format type.
// eg.
//
//	template := prompt.FromMessages(schema.FString, &schema.Message{Content: "Hello, {name}!"}, &schema.Message{Content: "how are you?"})
//	// in chain, or graph
//	chain := compose.NewChain[map[string]any, []*schema.Message]()
//	chain.AppendChatTemplate(template)
func FromMessages(formatType schema.FormatType, templates ...schema.MessagesTemplate) *DefaultChatTemplate {
	return &DefaultChatTemplate{
		templates:  templates,
		formatType: formatType,
	}
}

// Format formats the chat template with the given context and variables.
func (t *DefaultChatTemplate) Format(ctx context.Context,
	vs map[string]any, _ ...Option) (result []*schema.Message, err error) {
	ctx = callbacks.EnsureRunInfo(ctx, t.GetType(), components.ComponentOfPrompt)
	ctx = callbacks.OnStart(ctx, &CallbackInput{
		Variables: vs,
		Templates: t.templates,
	})
	defer func() {
		if err != nil {
			_ = callbacks.OnError(ctx, err)
		}
	}()

	result = make([]*schema.Message, 0, len(t.templates))
	for _, template := range t.templates {
		msgs, err := template.Format(ctx, vs, t.formatType)
		if err != nil {
			return nil, err
		}

		result = append(result, msgs...)
	}

	_ = callbacks.OnEnd(ctx, &CallbackOutput{
		Result:    result,
		Templates: t.templates,
	})

	return result, nil
}

// GetType returns the type of the chat template (Default).
func (t *DefaultChatTemplate) GetType() string {
	return "Default"
}

// IsCallbacksEnabled checks if the callbacks are enabled for the chat template.
func (t *DefaultChatTemplate) IsCallbacksEnabled() bool {
	return true
}
