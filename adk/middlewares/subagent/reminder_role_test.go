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

package subagent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
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

func TestSubagentReminderRole_DefaultSystem(t *testing.T) {
	ctx := context.Background()
	m := &typedSubagentMiddleware[*schema.Message]{
		subAgents:      []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
		reminderAsUser: false,
	}
	user := schema.UserMessage("hi")
	state := &adk.TypedChatModelAgentState[*schema.Message]{Messages: []*schema.Message{user}}
	_, ns, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	assert.False(t, rrIsUser(ns.Messages[1]))
	assert.True(t, hasExtraKey(ns.Messages[1], agentTypesReminderExtraKey))
}

func TestSubagentReminderRole_UserInsertPosition(t *testing.T) {
	ctx := context.Background()
	m := &typedSubagentMiddleware[*schema.Message]{
		subAgents:      []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
		reminderAsUser: true,
	}
	sys := schema.SystemMessage("sys")
	user := schema.UserMessage("hi")
	adk.EnsureMessageID(user)
	state := &adk.TypedChatModelAgentState[*schema.Message]{Messages: []*schema.Message{sys, user}}

	_, ns, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 3)
	// sys, reminder(user), user
	assert.True(t, rrIsUser(ns.Messages[1]))
	assert.True(t, hasExtraKey(ns.Messages[1], agentTypesReminderExtraKey))
	assert.False(t, hasExtraKey(ns.Messages[2], agentTypesReminderExtraKey))
	assert.Equal(t, adk.GetMessageID(user), adk.GetMessageID(ns.Messages[2]))
}

func TestSubagentReminderRole_UserModeNoUserInput(t *testing.T) {
	// User mode with no user-input message to anchor before: reminderInsertIndex
	// falls back to the tail, so the reminder is appended at the end.
	ctx := context.Background()
	m := &typedSubagentMiddleware[*schema.Message]{
		subAgents:      []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
		reminderAsUser: true,
	}
	state := &adk.TypedChatModelAgentState[*schema.Message]{Messages: []*schema.Message{schema.SystemMessage("sys")}}

	_, ns, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	assert.False(t, hasExtraKey(ns.Messages[0], agentTypesReminderExtraKey))
	assert.True(t, rrIsUser(ns.Messages[1]))
	assert.True(t, hasExtraKey(ns.Messages[1], agentTypesReminderExtraKey))
}

func testSubagentReminderInsertIndexUserMode[M adk.MessageType](t *testing.T) {
	// isUserInputAnchor only matches a plain user turn (both message types);
	// reminderInsertIndex in user mode returns its index, else the tail.
	assert.True(t, isUserInputAnchor(rrMkUser[M]("hi")))
	assert.False(t, isUserInputAnchor(rrMkSystem[M]("sys")))

	noUser := []M{rrMkSystem[M]("sys")}
	assert.Equal(t, len(noUser), reminderInsertIndex(noUser, true))

	withUser := []M{rrMkSystem[M]("sys"), rrMkUser[M]("hi")}
	assert.Equal(t, 1, reminderInsertIndex(withUser, true))
}

func TestSubagentReminderRole_InsertIndexUserMode(t *testing.T) {
	t.Run("Message", testSubagentReminderInsertIndexUserMode[*schema.Message])
	t.Run("AgenticMessage", testSubagentReminderInsertIndexUserMode[*schema.AgenticMessage])
}

func testSubagentReminderMigration[M adk.MessageType](t *testing.T) {
	prior := newReminder[M](agentTypesReminderExtraKey, "old section", false)
	adk.EnsureMessageID(prior)
	originalID := adk.GetMessageID(prior)
	user := rrMkUser[M]("hi")
	msgs := []M{rrMkSystem[M]("sys"), prior, user}

	migrated := migrateReminderRole(msgs, agentTypesReminderExtraKey, true)
	require.Len(t, migrated, 3)
	assert.True(t, rrIsUser(migrated[1]))
	assert.Equal(t, "old section", reminderContent(migrated[1]))
	assert.Equal(t, originalID, adk.GetMessageID(migrated[1]))
	assert.False(t, rrIsUser(msgs[1]))
}

func TestSubagentReminderRole_Migration(t *testing.T) {
	t.Run("Message", testSubagentReminderMigration[*schema.Message])
	t.Run("AgenticMessage", testSubagentReminderMigration[*schema.AgenticMessage])
}

// TestSubagentReminderRole_MigrationNotPersisted runs two turns on one session:
// turn 1 in system mode (the reminder is persisted as a MessageInserted event),
// turn 2 with a user-mode middleware. The reminder must be shown to turn-2's model
// in user role, but the migration must not add a MessageUpdated event — only the
// original MessageInserted persists.
func TestSubagentReminderRole_MigrationNotPersisted(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore[*schema.Message](nil)
	const sessionID = "subagent-reminder-role-session"

	newAgent := func(m model.BaseChatModel, disableSystem bool) adk.Agent {
		mw, err := New(ctx, &Config{
			SubAgents:                           []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
			DisableMidConversationSystemMessage: disableSystem,
		})
		require.NoError(t, err)
		a, aerr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:        "reminder-role-agent",
			Description: "reminder role migration test agent",
			Model:       m,
			Handlers:    []adk.ChatModelAgentMiddleware{mw},
		})
		require.NoError(t, aerr)
		return a
	}

	// Turn 1: system mode.
	model1 := &recordingModel{}
	runner1 := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        newAgent(model1, false),
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainEvents(t, runner1.Query(ctx, "hello"))

	// Turn 2: user mode.
	model2 := &recordingModel{}
	runner2 := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        newAgent(model2, true),
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainEvents(t, runner2.Query(ctx, "again"))

	require.NotEmpty(t, model2.inputs)
	turn2Input := model2.inputs[0]

	// Exactly one reminder, now shown in user role.
	reminderCount := 0
	for _, msg := range turn2Input {
		if _, ok := msg.Extra[agentTypesReminderExtraKey]; ok {
			reminderCount++
			assert.Equal(t, schema.User, msg.Role, "migrated reminder must be shown to the model in user role")
		}
	}
	assert.Equal(t, 1, reminderCount)

	// The event log must contain the original MessageInserted and NO MessageUpdated.
	res, err := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)
	inserted, updated := 0, 0
	for _, ev := range res.Events {
		if ev.MessageInserted != nil {
			if _, ok := ev.MessageInserted.Message.Extra[agentTypesReminderExtraKey]; ok {
				inserted++
			}
		}
		if ev.MessageUpdated != nil {
			updated++
		}
	}
	assert.Equal(t, 1, inserted, "reminder persisted once as MessageInserted")
	assert.Equal(t, 0, updated, "in-memory role migration must not emit a MessageUpdated event")
}
