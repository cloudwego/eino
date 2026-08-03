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

// Package systemreminder provides the shared mid-conversation system reminder insertion
// logic used by the skill, subagent and toolsearch middlewares. Each of those
// middlewares injects a mid-conversation system reminder (an "available skills" /
// "available agent types" / "tool search" message) into the message history from its
// BeforeModelRewriteState hook.
//
// The message is placed right after the latest turn boundary — the last user
// message or final assistant answer (see insertIndex) — so it reads as context
// following the user's ask and never lands in the middle of pending
// tool-call/tool-result scaffolding.
//
// Insert also emits a durable adk.SessionEventMessageInserted event so the message
// is reconstructed at the same position on the next turn instead of being dropped
// from the event log (the Runner persists the turn-start query plus model outputs
// and tool results; an inserted message is none of those). Anchoring reconstruction
// to the message it precedes keeps the prefix stable across turns.
//
// Because the message persists across turns, callers control re-insertion:
//   - subagent / toolsearch inject a fixed message exactly once — they call Has to
//     skip insertion when their message already exists in the history.
//   - skill re-injects whenever its skill list changes — it compares an MD5 digest of
//     the current skills (stored via Insert's extra argument, read back via
//     LatestExtra) against the digest carried by the last skill message.
//
// Inserted messages are tagged with adk.MessageExtraKeySystemReminder so the
// framework's genModelInput preserves them instead of stripping a non-leading system
// message as a stale instruction.
package systemreminder

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/schema"
)

// Insert builds a mid-conversation system reminder carrying section (tagged with extraKey and
// adk.MessageExtraKeySystemReminder, plus any entries in extra), inserts it at
// insertIndex, and emits a MessageInserted session event so it is reconstructed in
// place on the next turn. TypedSendEvent is a no-op outside a Runner session. The
// input slice is not mutated; the updated slice is returned.
func Insert[M adk.MessageType](ctx context.Context, messages []M, extraKey, section string, extra map[string]any) []M {
	insertAt := insertIndex(messages)

	msg := newReminderMessage[M](extraKey, section, extra)
	adk.EnsureMessageID(msg)

	result := make([]M, 0, len(messages)+1)
	result = append(result, messages[:insertAt]...)
	result = append(result, msg)
	result = append(result, messages[insertAt:]...)

	// Pin reconstruction to the message the inserted one now precedes; a tail insert
	// leaves this empty, which reconstructs as an append. The anchored message is
	// always one the framework persists, never the regenerated leading instruction.
	var beforeMessageID string
	if insertAt < len(messages) {
		beforeMessageID = adk.GetMessageID(messages[insertAt])
	}
	_ = adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{
		SessionEventVariant: &adk.SessionEventVariant[M]{
			Event: &adk.SessionEvent[M]{
				Kind: adk.SessionEventMessageInserted,
				MessageInserted: &adk.MessageInsertedEvent[M]{
					Message:         msg,
					BeforeMessageID: beforeMessageID,
				},
			},
		},
	})
	return result
}

// Has reports whether a message tagged extraKey already exists in messages. It is how
// subagent/toolsearch enforce insert-once across turns: once their message is in the
// reconstructed history, they skip re-inserting it.
func Has[M adk.MessageType](messages []M, extraKey string) bool {
	for _, msg := range messages {
		if hasExtraKey(msg, extraKey) {
			return true
		}
	}
	return false
}

// LatestExtra returns the value stored under valueKey in the Extra of the last
// message tagged extraKey, or (nil, false) when no such message exists. skill uses it
// to read the MD5 digest carried by the most recent skill message for diffing.
func LatestExtra[M adk.MessageType](messages []M, extraKey, valueKey string) (any, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if !hasExtraKey(messages[i], extraKey) {
			continue
		}
		extra := messageExtra(messages[i])
		if extra == nil {
			return nil, false
		}
		v, ok := extra[valueKey]
		return v, ok
	}
	return nil, false
}

// insertIndex returns the position for a fresh mid-conversation system reminder: right
// after the latest turn boundary — the last user message or final assistant answer
// (see isInsertAnchor) — then past any settled system messages already sitting there
// (sibling or stale inserts), so it lands after them without disturbing their
// positions and never inside pending tool-call/tool-result scaffolding. Falls back to
// the tail when there is no anchor.
func insertIndex[M adk.MessageType](messages []M) int {
	insertAt := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if isInsertAnchor(messages[i]) {
			insertAt = i + 1
			break
		}
	}
	for insertAt < len(messages) && isSystemMessage(messages[insertAt]) {
		insertAt++
	}
	return insertAt
}

// isInsertAnchor reports whether msg is a clean turn boundary a message may be
// inserted after: a user message or a final assistant answer (one without tool calls).
func isInsertAnchor[M adk.MessageType](msg M) bool {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Role == schema.User || (v.Role == schema.Assistant && len(v.ToolCalls) == 0)
	case *schema.AgenticMessage:
		switch v.Role {
		case schema.AgenticRoleTypeUser:
			return !internal.HasToolResult(v.ContentBlocks)
		case schema.AgenticRoleTypeAssistant:
			return !internal.HasToolCall(v.ContentBlocks)
		}
	}
	return false
}

func isSystemMessage[M adk.MessageType](msg M) bool {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Role == schema.System
	case *schema.AgenticMessage:
		return v.Role == schema.AgenticRoleTypeSystem
	}
	return false
}

func messageExtra[M adk.MessageType](msg M) map[string]any {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Extra
	case *schema.AgenticMessage:
		return v.Extra
	}
	return nil
}

func hasExtraKey[M adk.MessageType](msg M, key string) bool {
	extra := messageExtra(msg)
	if extra == nil {
		return false
	}
	_, ok := extra[key]
	return ok
}

// newReminderMessage builds a mid-conversation reminder message carrying
// content, tagged with extraKey (so it is identifiable in history) and
// adk.MessageExtraKeySystemReminder (so the framework preserves it). Its role follows the
// configured internal.GetReminderMessageRole() (System by default, User for models that
// reject non-leading system messages). Any entries in extra are merged into the message
// Extra — used by skill to stash the MD5 digest of the current skill list for later diffing.
func newReminderMessage[M adk.MessageType](extraKey, content string, extra map[string]any) M {
	var zero M
	buildExtra := func() map[string]any {
		e := map[string]any{extraKey: true, adk.MessageExtraKeySystemReminder: true}
		for k, v := range extra {
			e[k] = v
		}
		return e
	}
	asUser := internal.GetReminderMessageRole() == internal.ReminderMessageRoleUser
	switch any(zero).(type) {
	case *schema.Message:
		msg := schema.SystemMessage(content)
		if asUser {
			msg.Role = schema.User
		}
		msg.Extra = buildExtra()
		return any(msg).(M)
	case *schema.AgenticMessage:
		msg := schema.SystemAgenticMessage(content)
		if asUser {
			msg.Role = schema.AgenticRoleTypeUser
		}
		msg.Extra = buildExtra()
		return any(msg).(M)
	}
	panic("unreachable")
}

// NormalizeReminderRoles rewrites the role of every mid-conversation reminder message
// (tagged adk.MessageExtraKeySystemReminder) to the configured
// internal.GetReminderMessageRole(). Middlewares call this from BeforeModelRewriteState so
// reminders reconstructed from history — persisted under a role that may differ from the
// current config — match the current setting before the model call.
//
// Copy-on-write: the input slice and its messages are never mutated; a fresh slice is
// allocated only when at least one role changes, otherwise the input is returned as-is.
// It is idempotent.
func NormalizeReminderRoles[M adk.MessageType](messages []M) []M {
	asUser := internal.GetReminderMessageRole() == internal.ReminderMessageRoleUser
	var result []M
	for i, msg := range messages {
		if !hasExtraKey(msg, adk.MessageExtraKeySystemReminder) {
			continue
		}
		cp, changed := withReminderRole(msg, asUser)
		if !changed {
			continue
		}
		if result == nil {
			result = make([]M, len(messages))
			copy(result, messages)
		}
		result[i] = cp
	}
	if result == nil {
		return messages
	}
	return result
}

// withReminderRole returns a copy of msg with its role set to the configured reminder
// role (User when asUser, else System), reporting whether the role changed. The original
// message is never mutated.
func withReminderRole[M adk.MessageType](msg M, asUser bool) (M, bool) {
	switch v := any(msg).(type) {
	case *schema.Message:
		want := schema.System
		if asUser {
			want = schema.User
		}
		if v.Role == want {
			return msg, false
		}
		cp := *v
		cp.Role = want
		return any(&cp).(M), true
	case *schema.AgenticMessage:
		want := schema.AgenticRoleTypeSystem
		if asUser {
			want = schema.AgenticRoleTypeUser
		}
		if v.Role == want {
			return msg, false
		}
		cp := *v
		cp.Role = want
		return any(&cp).(M), true
	}
	return msg, false
}
