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
	"github.com/google/uuid"

	"github.com/cloudwego/eino/internal/gmap"
	"github.com/cloudwego/eino/schema"
)

// EinoMsgIDKey is the Extra key used to store the eino-internal message ID.
const EinoMsgIDKey = "_eino_msg_id"

const messageExtraKeySystemReminder = "_eino_adk_system_reminder"

// GetMessageID returns the message ID from Extra, or "" if not set.
// Works with any map[string]any (Message.Extra or AgenticMessage.Extra).
func GetMessageID(extra map[string]any) string {
	if extra == nil {
		return ""
	}
	id, _ := extra[EinoMsgIDKey].(string)
	return id
}

// SetMessageID sets the message ID in Extra and returns the resulting map.
//
// Copy-on-write: the input map is never mutated, because a message's Extra can
// be shared across fan-out stream readers and an in-place write would race with
// concurrent reads (fatal "concurrent map read and map write"). Reassigning the
// returned map to a shared Extra field is still a field-level race that cannot
// be eliminated while Extra is exported; COW only removes the map-level panic.
func SetMessageID(extra map[string]any, id string) map[string]any {
	return gmap.SetCoW(extra, EinoMsgIDKey, any(id))
}

// EnsureMessageID assigns a UUID v4 if no message ID is present.
// Idempotent: if ID already set, no-op.
// Returns the (possibly newly created) Extra map.
func EnsureMessageID(extra map[string]any) map[string]any {
	if GetMessageID(extra) != "" {
		return extra
	}
	return SetMessageID(extra, uuid.NewString())
}

// GetMessageExtra returns the message's Extra map, or nil for a nil message.
// The returned map is shared with the message and must not be modified concurrently.
func GetMessageExtra[M *schema.Message | *schema.AgenticMessage](msg M) map[string]any {
	if msg == nil {
		return nil
	}
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Extra
	case *schema.AgenticMessage:
		return m.Extra
	default:
		panic("unreachable")
	}
}

// SetMessageExtra sets key to value on a non-nil message by replacing its Extra
// with a shallow copy. The original map is unchanged; access to the message itself
// must still be synchronized by the caller.
func SetMessageExtra[M *schema.Message | *schema.AgenticMessage](msg M, key string, value any) {
	extra := gmap.SetCoW(GetMessageExtra(msg), key, value)
	switch m := any(msg).(type) {
	case *schema.Message:
		m.Extra = extra
	case *schema.AgenticMessage:
		m.Extra = extra
	default:
		panic("unreachable")
	}
}

// IsSystemReminder reports whether msg is marked as a middleware reminder.
func IsSystemReminder[M *schema.Message | *schema.AgenticMessage](msg M) bool {
	_, ok := GetMessageExtra(msg)[messageExtraKeySystemReminder]
	return ok
}

// MarkSystemReminder marks a non-nil message as a middleware reminder without
// modifying its original Extra map.
func MarkSystemReminder[M *schema.Message | *schema.AgenticMessage](msg M) {
	SetMessageExtra(msg, messageExtraKeySystemReminder, true)
}

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
