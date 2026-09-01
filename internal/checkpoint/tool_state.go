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

// Package checkpoint contains checkpoint wire types shared across Eino packages.
package checkpoint

import "github.com/cloudwego/eino/schema"

// ToolsNodeInterruptStateV1Version is the first compact ToolsNode state version.
const ToolsNodeInterruptStateV1Version = 1

// ToolsNodeInterruptStateV1 is the versioned ToolsNode checkpoint wire state.
// ToolCalls and ToolCallsSource are mutually exclusive in persisted data.
type ToolsNodeInterruptStateV1 struct {
	// Version must equal ToolsNodeInterruptStateV1Version.
	Version int
	// Role must be schema.Assistant.
	Role schema.RoleType
	// ToolCalls is populated when the calls cannot be referenced from graph state.
	ToolCalls []schema.ToolCall
	// ToolCallsSource identifies an exact message in the owning graph state.
	// It is nil when ToolCalls are stored inline.
	ToolCallsSource *ToolsNodeToolCallsSourceV1

	// ExecutedTools contains successful standard tool results keyed by tool call ID.
	ExecutedTools map[string]string
	// ExecutedEnhancedTools contains successful enhanced tool results keyed by tool call ID.
	ExecutedEnhancedTools map[string]*schema.ToolResult
	// RerunTools contains tool call IDs that must execute again during resume.
	RerunTools []string
}

// ToolsNodeToolCallsSourceV1 identifies ToolCalls in the owning graph state.
type ToolsNodeToolCallsSourceV1 struct {
	// MessageIndex is the zero-based index in the owning graph state's Messages slice.
	MessageIndex int
	// Digest is the lowercase hex SHA-256 digest of the referenced ToolCalls JSON.
	Digest string
}
