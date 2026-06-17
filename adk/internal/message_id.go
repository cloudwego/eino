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

import "github.com/google/uuid"

// EinoMsgIDKey is the Extra key used to store the eino-internal message ID.
const EinoMsgIDKey = "_eino_msg_id"

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
	next := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		next[k] = v
	}
	next[EinoMsgIDKey] = id
	return next
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
