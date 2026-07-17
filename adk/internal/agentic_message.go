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

import "github.com/cloudwego/eino/schema"

// HasToolCall reports whether blocks contain a tool-call protocol block.
func HasToolCall(blocks []*schema.ContentBlock) bool {
	for _, block := range blocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeFunctionToolCall,
			schema.ContentBlockTypeServerToolCall,
			schema.ContentBlockTypeMCPToolCall,
			schema.ContentBlockTypeMCPToolApprovalRequest:
			return true
		}
	}
	return false
}

// HasToolResult reports whether blocks contain a tool-result protocol block.
func HasToolResult(blocks []*schema.ContentBlock) bool {
	for _, block := range blocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeToolSearchResult,
			schema.ContentBlockTypeFunctionToolResult,
			schema.ContentBlockTypeServerToolResult,
			schema.ContentBlockTypeMCPToolResult,
			schema.ContentBlockTypeMCPListToolsResult,
			schema.ContentBlockTypeMCPToolApprovalResponse:
			return true
		}
	}
	return false
}
