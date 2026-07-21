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

package skill

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func rrMkUser[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(content)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(content)).(M)
	}
	panic("unreachable")
}

func rrMkSystem[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.SystemMessage(content)).(M)
	case *schema.AgenticMessage:
		return any(schema.SystemAgenticMessage(content)).(M)
	}
	panic("unreachable")
}

func rrIsUser[M adk.MessageType](msg M) bool { return reminderIsUser(msg) }

func rrHandler[M adk.MessageType](t *testing.T, disableSystem bool) *typedSkillHandler[M] {
	t.Helper()
	ctx := context.Background()
	backend := &inMemoryBackend{m: []Skill{{FrontMatter: FrontMatter{Name: "alpha", Description: "first"}}}}
	mw, err := NewTyped(ctx, &TypedConfig[M]{Backend: backend, DisableMidConversationSystemMessage: disableSystem})
	require.NoError(t, err)
	return mw.(*typedSkillHandler[M])
}

func testSkillReminderDefaultSystem[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	h := rrHandler[M](t, false)
	user := rrMkUser[M]("hi")
	state := &adk.TypedChatModelAgentState[M]{Messages: []M{user}}
	_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	// user, reminder(system)
	assert.False(t, rrIsUser(ns.Messages[1]))
	assert.True(t, hasExtraKey(ns.Messages[1], skillsReminderExtraKey))
}

func testSkillReminderUserInsertPosition[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	h := rrHandler[M](t, true)
	sys := rrMkSystem[M]("sys")
	user := rrMkUser[M]("hi")
	adk.EnsureMessageID(user)
	state := &adk.TypedChatModelAgentState[M]{Messages: []M{sys, user}}

	_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 3)
	// sys, reminder(user), user — reminder immediately before the latest user input.
	assert.True(t, rrIsUser(ns.Messages[1]))
	assert.True(t, hasExtraKey(ns.Messages[1], skillsReminderExtraKey))
	assert.False(t, hasExtraKey(ns.Messages[2], skillsReminderExtraKey))
	assert.Equal(t, adk.GetMessageID(user), adk.GetMessageID(ns.Messages[2]))
}

func testSkillReminderMigration[M adk.MessageType](t *testing.T) {
	// Seed a prior system-role reminder; migrating to user mode must flip it in
	// place, preserving content and message ID, without touching the original.
	prior := newReminder[M](skillsReminderExtraKey, "old section", false)
	adk.EnsureMessageID(prior)
	originalID := adk.GetMessageID(prior)
	user := rrMkUser[M]("hi")
	msgs := []M{rrMkSystem[M]("sys"), prior, user}

	migrated := migrateReminderRole(msgs, skillsReminderExtraKey, true)
	require.Len(t, migrated, 3)
	assert.True(t, rrIsUser(migrated[1]))
	assert.Equal(t, "old section", reminderContent(migrated[1]))
	assert.Equal(t, originalID, adk.GetMessageID(migrated[1]))
	// Original element untouched.
	assert.False(t, rrIsUser(msgs[1]))

	// Migrating when already aligned returns the input slice unchanged.
	same := migrateReminderRole(migrated, skillsReminderExtraKey, true)
	assert.True(t, rrIsUser(same[1]))
}

func testSkillReminderUserModeNoUserInput[M adk.MessageType](t *testing.T) {
	// User mode with no user-input message to anchor before: reminderInsertIndex
	// falls back to the tail, so the reminder is appended at the end.
	ctx := context.Background()
	h := rrHandler[M](t, true)
	state := &adk.TypedChatModelAgentState[M]{Messages: []M{rrMkSystem[M]("sys")}}

	_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	// sys, reminder(user) — appended at the tail because there is no user input.
	assert.False(t, hasExtraKey(ns.Messages[0], skillsReminderExtraKey))
	assert.True(t, rrIsUser(ns.Messages[1]))
	assert.True(t, hasExtraKey(ns.Messages[1], skillsReminderExtraKey))
}

func testSkillReminderInsertIndexUserMode[M adk.MessageType](t *testing.T) {
	// isUserInputAnchor only matches a plain user turn; reminderInsertIndex in
	// user mode returns that turn's index, else falls back to the tail.
	assert.True(t, isUserInputAnchor(rrMkUser[M]("hi")))
	assert.False(t, isUserInputAnchor(rrMkSystem[M]("sys")))

	noUser := []M{rrMkSystem[M]("sys")}
	assert.Equal(t, len(noUser), reminderInsertIndex(noUser, true))

	withUser := []M{rrMkSystem[M]("sys"), rrMkUser[M]("hi")}
	assert.Equal(t, 1, reminderInsertIndex(withUser, true))
}

func TestSkillReminderRole(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		t.Run("DefaultSystem", testSkillReminderDefaultSystem[*schema.Message])
		t.Run("UserInsertPosition", testSkillReminderUserInsertPosition[*schema.Message])
		t.Run("UserModeNoUserInput", testSkillReminderUserModeNoUserInput[*schema.Message])
		t.Run("InsertIndexUserMode", testSkillReminderInsertIndexUserMode[*schema.Message])
		t.Run("Migration", testSkillReminderMigration[*schema.Message])
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		t.Run("DefaultSystem", testSkillReminderDefaultSystem[*schema.AgenticMessage])
		t.Run("UserInsertPosition", testSkillReminderUserInsertPosition[*schema.AgenticMessage])
		t.Run("UserModeNoUserInput", testSkillReminderUserModeNoUserInput[*schema.AgenticMessage])
		t.Run("InsertIndexUserMode", testSkillReminderInsertIndexUserMode[*schema.AgenticMessage])
		t.Run("Migration", testSkillReminderMigration[*schema.AgenticMessage])
	})
}
