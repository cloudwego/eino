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

package toolsearch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func newTestMiddlewareTypedRole[M adk.MessageType](t *testing.T, tools []tool.BaseTool, disableSystem bool) *typedMiddleware[M] {
	t.Helper()
	ctx := context.Background()
	mw, err := NewTyped[M](ctx, &Config{
		DynamicTools:                        tools,
		UseModelToolSearch:                  false,
		DisableMidConversationSystemMessage: disableSystem,
	})
	require.NoError(t, err)
	return mw.(*typedMiddleware[M])
}

// testReminderRoleDefaultSystemGeneric confirms the default (system role)
// still produces a system-role reminder at the historical position.
func testReminderRoleDefaultSystemGeneric[M adk.MessageType](t *testing.T) {
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	m := newTestMiddlewareTypedRole[M](t, []tool.BaseTool{dynamicA}, false)

	input := []M{makeSystemMsg[M]("sys"), makeUserMsg[M]("hi")}
	got, _, _, didInsert := m.ensureReminder(input)
	require.True(t, didInsert)
	require.Len(t, got, 3)
	// system, user, reminder(system) — reminder after the anchor.
	assert.Equal(t, "system", getMsgRole(got[2]))
	assert.Equal(t, true, getMsgExtra(got[2])[toolSearchReminderExtraKey])
}

// testReminderRoleUserInsertPositionGeneric verifies that in user mode the
// reminder is a user message inserted immediately before the latest user input,
// and beforeMessageID points at that user message.
func testReminderRoleUserInsertPositionGeneric[M adk.MessageType](t *testing.T) {
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	m := newTestMiddlewareTypedRole[M](t, []tool.BaseTool{dynamicA}, true)

	user := makeUserMsg[M]("hi")
	adk.EnsureMessageID(user)
	input := []M{makeSystemMsg[M]("sys"), user}

	got, inserted, beforeMessageID, didInsert := m.ensureReminder(input)
	require.True(t, didInsert)
	require.Len(t, got, 3)
	// system, reminder(user), user — reminder one position before the user input.
	assert.Equal(t, "system", getMsgRole(got[0]))
	assert.Equal(t, "user", getMsgRole(got[1]))
	assert.Equal(t, "user", getMsgRole(got[2]))
	assert.Equal(t, true, getMsgExtra(got[1])[toolSearchReminderExtraKey])
	_, userHasReminder := getMsgExtra(got[2])[toolSearchReminderExtraKey]
	assert.False(t, userHasReminder)
	assert.Equal(t, adk.GetMessageID(user), beforeMessageID)
	assert.Equal(t, "user", getMsgRole(inserted))
}

// testReminderRoleMigrationGeneric seeds a prior system-role reminder and
// verifies that in user mode migrateReminderRole flips it to a user message in
// place, preserving its message ID.
func testReminderRoleMigrationGeneric[M adk.MessageType](t *testing.T) {
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	m := newTestMiddlewareTypedRole[M](t, []tool.BaseTool{dynamicA}, true)

	reminder := makeSystemMsg[M]("<reminder>")
	setMsgExtra(reminder, toolSearchReminderExtraKey, true)
	adk.EnsureMessageID(reminder)
	originalID := adk.GetMessageID(reminder)
	user := makeUserMsg[M]("hi")
	adk.EnsureMessageID(user)
	input := []M{makeSystemMsg[M]("sys"), reminder, user}

	migrated := m.migrateReminderRole(input)
	require.Len(t, migrated, 3)
	// Position 1 (the reminder) flipped to user role, same content and ID.
	assert.Equal(t, "user", getMsgRole(migrated[1]))
	assert.Equal(t, true, getMsgExtra(migrated[1])[toolSearchReminderExtraKey])
	assert.Equal(t, "<reminder>", getMsgContent(migrated[1]))
	assert.Equal(t, originalID, adk.GetMessageID(migrated[1]))
	// The original slice element is untouched (in-memory copy-on-write).
	assert.Equal(t, "system", getMsgRole(input[1]))

	// ensureReminder then recognizes the (now-user) reminder and does not add another.
	got, _, _, didInsert := m.ensureReminder(migrated)
	assert.False(t, didInsert)
	assert.Equal(t, 1, countRemindersGeneric(got))
}

// testReminderRoleUserModeNoUserInputGeneric covers the user-mode fallback:
// with no user-input message to anchor before, the reminder is appended at the
// tail (reminderInsertIndexTS returns len(msgs)).
func testReminderRoleUserModeNoUserInputGeneric[M adk.MessageType](t *testing.T) {
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	m := newTestMiddlewareTypedRole[M](t, []tool.BaseTool{dynamicA}, true)

	input := []M{makeSystemMsg[M]("sys")}
	got, _, _, didInsert := m.ensureReminder(input)
	require.True(t, didInsert)
	require.Len(t, got, 2)
	assert.Equal(t, "system", getMsgRole(got[0]))
	assert.Equal(t, "user", getMsgRole(got[1]))
	assert.Equal(t, true, getMsgExtra(got[1])[toolSearchReminderExtraKey])

	// isUserInputAnchorTS / reminderInsertIndexTS in user mode directly.
	assert.True(t, isUserInputAnchorTS(makeUserMsg[M]("hi")))
	assert.False(t, isUserInputAnchorTS(makeSystemMsg[M]("sys")))
	noUser := []M{makeSystemMsg[M]("sys")}
	assert.Equal(t, len(noUser), reminderInsertIndexTS(noUser, true))
	withUser := []M{makeSystemMsg[M]("sys"), makeUserMsg[M]("hi")}
	assert.Equal(t, 1, reminderInsertIndexTS(withUser, true))
}

func TestToolSearchReminderRole(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		t.Run("DefaultSystem", testReminderRoleDefaultSystemGeneric[*schema.Message])
		t.Run("UserInsertPosition", testReminderRoleUserInsertPositionGeneric[*schema.Message])
		t.Run("UserModeNoUserInput", testReminderRoleUserModeNoUserInputGeneric[*schema.Message])
		t.Run("Migration", testReminderRoleMigrationGeneric[*schema.Message])
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		t.Run("DefaultSystem", testReminderRoleDefaultSystemGeneric[*schema.AgenticMessage])
		t.Run("UserInsertPosition", testReminderRoleUserInsertPositionGeneric[*schema.AgenticMessage])
		t.Run("UserModeNoUserInput", testReminderRoleUserModeNoUserInputGeneric[*schema.AgenticMessage])
		t.Run("Migration", testReminderRoleMigrationGeneric[*schema.AgenticMessage])
	})
}
