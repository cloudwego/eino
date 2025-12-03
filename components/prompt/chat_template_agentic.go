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

package prompt

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/schema"
)

// FromAgenticMessages creates a new DefaultAgenticChatTemplate from the given templates and format type.
// eg.
//
//	template := prompt.FromAgenticMessages(schema.FString, &schema.AgenticMessage{})
//	// in chain, or graph
//	chain := compose.NewChain[map[string]any, []*schema.AgenticMessage]()
//	chain.AppendAgenticChatTemplate(template)
func FromAgenticMessages(formatType schema.FormatType, templates ...schema.AgenticMessagesTemplate) *DefaultAgenticChatTemplate {
	return &DefaultAgenticChatTemplate{
		templates:  templates,
		formatType: formatType,
	}
}

type DefaultAgenticChatTemplate struct {
	templates  []schema.AgenticMessagesTemplate
	formatType schema.FormatType
}

func (t *DefaultAgenticChatTemplate) Format(ctx context.Context, vs map[string]any, opts ...Option) (result []*schema.AgenticMessage, err error) {
	ctx = callbacks.EnsureRunInfo(ctx, t.GetType(), components.ComponentOfAgenticPrompt)
	ctx = callbacks.OnStart(ctx, &AgenticCallbackInput{
		Variables: vs,
		Templates: t.templates,
	})
	defer func() {
		if err != nil {
			_ = callbacks.OnError(ctx, err)
		}
	}()

	result = make([]*schema.AgenticMessage, 0, len(t.templates))
	for _, template := range t.templates {
		msgs, err := template.Format(ctx, vs, t.formatType)
		if err != nil {
			return nil, err
		}

		result = append(result, msgs...)
	}

	_ = callbacks.OnEnd(ctx, &AgenticCallbackOutput{
		Result:    result,
		Templates: t.templates,
	})

	return result, nil
}

// GetType returns the type of the chat template (Default).
func (t *DefaultAgenticChatTemplate) GetType() string {
	return "Default"
}

// IsCallbacksEnabled checks if the callbacks are enabled for the chat template.
func (t *DefaultAgenticChatTemplate) IsCallbacksEnabled() bool {
	return true
}
