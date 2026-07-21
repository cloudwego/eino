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

// TestSkill_BeforeModelRewriteState_AgenticMessage exercises the
// *schema.AgenticMessage branches of the reminder helpers (isSystemMessage,
// hasExtraKey, reminderContent, newReminder, isReminderAnchor,
// appendReminderIfChanged) which the *schema.Message tests never reach.
func TestSkill_BeforeModelRewriteState_AgenticMessage(t *testing.T) {
	ctx := context.Background()
	backend := &inMemoryBackend{m: []Skill{{FrontMatter: FrontMatter{Name: "alpha", Description: "first"}}}}
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &TypedConfig[*schema.AgenticMessage]{Backend: backend})
	require.NoError(t, err)
	h := mw.(*typedSkillHandler[*schema.AgenticMessage])

	user := schema.UserAgenticMessage("hi")
	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{user}}

	// First call inserts a reminder after the user message.
	_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	reminder := ns.Messages[1]
	assert.Equal(t, schema.AgenticRoleTypeSystem, reminder.Role)
	_, ok := reminder.Extra[skillsReminderExtraKey]
	assert.True(t, ok)

	// Re-run is idempotent (exercises reminderContent's AgenticMessage
	// single-block path returning the reminder text and matching the section).
	_, ns2, err := h.BeforeModelRewriteState(ctx, ns, nil)
	require.NoError(t, err)
	require.Len(t, ns2.Messages, 2)

	// Skill list changes: a fresh reminder is appended (content-changed path).
	backend.m = append(backend.m, Skill{FrontMatter: FrontMatter{Name: "beta", Description: "second"}})
	_, ns3, err := h.BeforeModelRewriteState(ctx, ns2, nil)
	require.NoError(t, err)
	require.Len(t, ns3.Messages, 3)

	// No skills: buildSkillsSection returns ok=false and the state is untouched.
	empty, err := NewTyped[*schema.AgenticMessage](ctx, &TypedConfig[*schema.AgenticMessage]{Backend: &inMemoryBackend{}})
	require.NoError(t, err)
	hEmpty := empty.(*typedSkillHandler[*schema.AgenticMessage])
	stEmpty := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{user}}
	_, nsEmpty, err := hEmpty.BeforeModelRewriteState(ctx, stEmpty, nil)
	require.NoError(t, err)
	assert.Len(t, nsEmpty.Messages, 1)

	// nil state: returned unchanged (nil-guard path).
	_, nsNil, err := h.BeforeModelRewriteState(ctx, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, nsNil)
}

// TestSkill_BeforeModelRewriteState_AgenticMessage_InsertBeforeTrailing verifies
// that when a non-anchor message (a tool-call assistant) trails the turn's user
// message, the reminder is inserted after the user message and BEFORE that
// trailing message, pinning beforeMessageID to it (the insertAt < len path).
func TestSkill_BeforeModelRewriteState_AgenticMessage_InsertBeforeTrailing(t *testing.T) {
	ctx := context.Background()
	backend := &inMemoryBackend{m: []Skill{{FrontMatter: FrontMatter{Name: "alpha", Description: "first"}}}}
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &TypedConfig[*schema.AgenticMessage]{Backend: backend})
	require.NoError(t, err)
	h := mw.(*typedSkillHandler[*schema.AgenticMessage])

	user := schema.UserAgenticMessage("hi")
	toolCall := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{Type: schema.ContentBlockTypeFunctionToolCall, FunctionToolCall: &schema.FunctionToolCall{}},
		},
	}
	adk.EnsureMessageID(toolCall)
	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{user, toolCall}}

	_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 3)
	// user, reminder, toolCall — reminder lands between the anchor and the trailing
	// tool-call message rather than at the tail.
	reminder := ns.Messages[1]
	assert.Equal(t, schema.AgenticRoleTypeSystem, reminder.Role)
	_, ok := reminder.Extra[skillsReminderExtraKey]
	assert.True(t, ok)
	assert.Equal(t, toolCall, ns.Messages[2])
}

// TestSkill_ReminderHelpers_AgenticMessage covers the remaining AgenticMessage
// helper branches directly.
func TestSkill_ReminderHelpers_AgenticMessage(t *testing.T) {
	user := schema.UserAgenticMessage("u")
	assistant := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
	system := schema.SystemAgenticMessage("s")

	// isReminderAnchor: user and final-assistant are anchors; system is not.
	assert.True(t, isReminderAnchor(user))
	assert.True(t, isReminderAnchor(assistant))
	assert.False(t, isReminderAnchor(system))

	// isSystemMessage / hasExtraKey.
	assert.True(t, isSystemMessage(system))
	assert.False(t, isSystemMessage(user))
	reminder := newReminder[*schema.AgenticMessage]("k", "content", false)
	assert.True(t, hasExtraKey(reminder, "k"))
	assert.False(t, hasExtraKey(user, "k"))

	// reminderContent: single-block reminder returns its text; a multi-role
	// message with no single UserInputText block returns "".
	assert.Equal(t, "content", reminderContent(reminder))
	assert.Equal(t, "", reminderContent(assistant))
}
