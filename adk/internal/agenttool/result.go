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

package agenttool

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

// ExtractTextContent returns the model-facing text represented by a supported message.
func ExtractTextContent(message any) string {
	switch typed := message.(type) {
	case *schema.Message:
		if typed == nil {
			return ""
		}
		return typed.Content
	case *schema.AgenticMessage:
		if typed == nil {
			return ""
		}
		var texts []string
		for _, block := range typed.ContentBlocks {
			if block != nil &&
				block.Type == schema.ContentBlockTypeAssistantGenText &&
				block.AssistantGenText != nil {
				texts = append(texts, block.AssistantGenText.Text)
			}
		}
		return strings.Join(texts, "\n")
	default:
		return ""
	}
}
