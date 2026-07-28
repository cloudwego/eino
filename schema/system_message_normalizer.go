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

package schema

// ConvertNonLeadingSystemMessagesToUser rewrites every system-role message that
// appears AFTER the leading run of system messages to user role, preserving
// content, extra metadata, and message ID. The leading system messages (the
// system prompt) and all non-system messages are returned as-is.
//
// It is copy-on-write: the input slice and its messages are never mutated. A
// fresh slice is allocated only when at least one message changes; otherwise the
// input slice is returned unchanged. It is idempotent.
//
// A model that does not accept system messages injected mid-conversation (e.g.
// reminders after the leading system prompt) can call this inside its own
// Generate/Stream on the input before delegating to the underlying API.
func ConvertNonLeadingSystemMessagesToUser(messages []*Message) []*Message {
	// Skip the leading run of system messages (the system prompt).
	lead := 0
	for lead < len(messages) && messages[lead] != nil && messages[lead].Role == System {
		lead++
	}

	var result []*Message
	for i := lead; i < len(messages); i++ {
		m := messages[i]
		if m == nil || m.Role != System {
			continue
		}
		if result == nil {
			result = make([]*Message, len(messages))
			copy(result, messages)
		}
		cp := *m
		cp.Role = User
		result[i] = &cp
	}
	if result == nil {
		return messages
	}
	return result
}

// ConvertNonLeadingSystemAgenticMessagesToUser is the AgenticMessage counterpart
// of [ConvertNonLeadingSystemMessagesToUser]: it rewrites every system-role
// AgenticMessage appearing after the leading run of system messages to user role.
// Same copy-on-write and idempotency guarantees.
func ConvertNonLeadingSystemAgenticMessagesToUser(messages []*AgenticMessage) []*AgenticMessage {
	// Skip the leading run of system messages (the system prompt).
	lead := 0
	for lead < len(messages) && messages[lead] != nil && messages[lead].Role == AgenticRoleTypeSystem {
		lead++
	}

	var result []*AgenticMessage
	for i := lead; i < len(messages); i++ {
		m := messages[i]
		if m == nil || m.Role != AgenticRoleTypeSystem {
			continue
		}
		if result == nil {
			result = make([]*AgenticMessage, len(messages))
			copy(result, messages)
		}
		cp := *m
		cp.Role = AgenticRoleTypeUser
		result[i] = &cp
	}
	if result == nil {
		return messages
	}
	return result
}
