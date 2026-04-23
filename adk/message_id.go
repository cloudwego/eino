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

package adk

import (
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/schema"
)

// GetMessageID returns the eino-internal message ID from the given message, or "".
func GetMessageID(msg *schema.Message) string {
	return internal.GetMessageID(msg.Extra)
}

// GetAgenticMessageID returns the eino-internal message ID from the given agentic message, or "".
func GetAgenticMessageID(msg *schema.AgenticMessage) string {
	return internal.GetMessageID(msg.Extra)
}

// EnsureMessageID assigns a UUID v4 message ID if the message doesn't have one.
// Idempotent: if ID already set, no-op.
// Middleware authors should call this before SendEvent if they create messages.
func EnsureMessageID(msg *schema.Message) {
	msg.Extra = internal.EnsureMessageID(msg.Extra)
}

// EnsureAgenticMessageID assigns a UUID v4 message ID if the agentic message doesn't have one.
// Idempotent: if ID already set, no-op.
func EnsureAgenticMessageID(msg *schema.AgenticMessage) {
	msg.Extra = internal.EnsureMessageID(msg.Extra)
}

// EnsureMessageIDs assigns IDs to all messages that don't have one.
func EnsureMessageIDs(msgs []*schema.Message) {
	for _, msg := range msgs {
		EnsureMessageID(msg)
	}
}

// EnsureAgenticMessageIDs assigns IDs to all agentic messages that don't have one.
func EnsureAgenticMessageIDs(msgs []*schema.AgenticMessage) {
	for _, msg := range msgs {
		EnsureAgenticMessageID(msg)
	}
}
