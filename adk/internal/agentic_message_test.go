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

package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestHasToolCall(t *testing.T) {
	toolCallTypes := []schema.ContentBlockType{
		schema.ContentBlockTypeFunctionToolCall,
		schema.ContentBlockTypeServerToolCall,
		schema.ContentBlockTypeMCPToolCall,
		schema.ContentBlockTypeMCPToolApprovalRequest,
	}
	for _, blockType := range toolCallTypes {
		assert.True(t, HasToolCall([]*schema.ContentBlock{{Type: blockType}}))
	}

	assert.False(t, HasToolCall([]*schema.ContentBlock{
		nil,
		{Type: schema.ContentBlockTypeReasoning},
		{Type: schema.ContentBlockTypeFunctionToolResult},
	}))
}

func TestHasToolResult(t *testing.T) {
	toolResultTypes := []schema.ContentBlockType{
		schema.ContentBlockTypeToolSearchResult,
		schema.ContentBlockTypeFunctionToolResult,
		schema.ContentBlockTypeServerToolResult,
		schema.ContentBlockTypeMCPToolResult,
		schema.ContentBlockTypeMCPListToolsResult,
		schema.ContentBlockTypeMCPToolApprovalResponse,
	}
	for _, blockType := range toolResultTypes {
		assert.True(t, HasToolResult([]*schema.ContentBlock{{Type: blockType}}))
	}

	assert.False(t, HasToolResult([]*schema.ContentBlock{
		nil,
		{Type: schema.ContentBlockTypeUserInputText},
		{Type: schema.ContentBlockTypeFunctionToolCall},
	}))
}
