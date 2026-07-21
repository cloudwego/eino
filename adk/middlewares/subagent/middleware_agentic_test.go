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
	"github.com/cloudwego/eino/schema"
)

type agenticMockAgent struct {
	name string
	desc string
}

func (a agenticMockAgent) Name(context.Context) string        { return a.name }
func (a agenticMockAgent) Description(context.Context) string { return a.desc }
func (a agenticMockAgent) Run(context.Context, *adk.TypedAgentInput[*schema.AgenticMessage], ...adk.AgentRunOption) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	return nil
}

// TestSubagent_BeforeModelRewriteState_AgenticMessage exercises the
// *schema.AgenticMessage branches of the reminder helpers that the
// *schema.Message tests never reach.
func TestSubagent_BeforeModelRewriteState_AgenticMessage(t *testing.T) {
	ctx := context.Background()
	m := &typedSubagentMiddleware[*schema.AgenticMessage]{
		subAgents: []adk.TypedAgent[*schema.AgenticMessage]{agenticMockAgent{name: "worker", desc: "does work"}},
	}

	user := schema.UserAgenticMessage("hi")
	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{user}}

	_, ns, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	reminder := ns.Messages[1]
	assert.Equal(t, schema.AgenticRoleTypeSystem, reminder.Role)
	_, ok := reminder.Extra[agentTypesReminderExtraKey]
	assert.True(t, ok)

	// Idempotent re-run.
	_, ns2, err := m.BeforeModelRewriteState(ctx, ns, nil)
	require.NoError(t, err)
	require.Len(t, ns2.Messages, 2)

	// No sub-agents: buildAgentTypesSection returns ok=false and the state is untouched.
	empty := &typedSubagentMiddleware[*schema.AgenticMessage]{}
	_, nsEmpty, err := empty.BeforeModelRewriteState(ctx, &adk.TypedChatModelAgentState[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{user},
	}, nil)
	require.NoError(t, err)
	assert.Len(t, nsEmpty.Messages, 1)

	// nil state: returned unchanged (nil-guard path).
	_, nsNil, err := m.BeforeModelRewriteState(ctx, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, nsNil)
}

// TestSubagent_ReminderHelpers_AgenticMessage covers the remaining AgenticMessage
// helper branches directly.
func TestSubagent_ReminderHelpers_AgenticMessage(t *testing.T) {
	user := schema.UserAgenticMessage("u")
	assistant := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
	system := schema.SystemAgenticMessage("s")

	assert.True(t, isReminderAnchor[*schema.AgenticMessage](user))
	assert.True(t, isReminderAnchor[*schema.AgenticMessage](assistant))
	assert.False(t, isReminderAnchor[*schema.AgenticMessage](system))

	assert.True(t, isSystemMessage[*schema.AgenticMessage](system))
	assert.False(t, isSystemMessage[*schema.AgenticMessage](user))

	reminder := newReminder[*schema.AgenticMessage]("k", "content", false)
	assert.True(t, hasExtraKey[*schema.AgenticMessage](reminder, "k"))
	assert.False(t, hasExtraKey[*schema.AgenticMessage](user, "k"))

	assert.Equal(t, "content", reminderContent[*schema.AgenticMessage](reminder))
	assert.Equal(t, "", reminderContent[*schema.AgenticMessage](assistant))
}
