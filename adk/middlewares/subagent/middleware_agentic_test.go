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

// TestSubagent_BeforeAgent_AgenticMessage exercises the *schema.AgenticMessage
// path of the reminder insertion done in BeforeModelRewriteState.
func TestSubagent_BeforeAgent_AgenticMessage(t *testing.T) {
	ctx := context.Background()
	m := &typedSubagentMiddleware[*schema.AgenticMessage]{
		reminder: buildAgentTypesSectionFromEntries([]agentTypeEntry{{Name: "worker", Description: "does work"}}),
	}

	user := schema.UserAgenticMessage("hi")
	state := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{user}}

	_, ns, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	// The reminder is inserted after the latest user message.
	assert.Equal(t, schema.AgenticRoleTypeUser, ns.Messages[0].Role)
	rem := ns.Messages[1]
	assert.Equal(t, schema.AgenticRoleTypeSystem, rem.Role)
	_, ok := rem.Extra[agentTypesReminderExtraKey]
	assert.True(t, ok)

	// Inserted exactly once: calling again is a no-op (Has guards re-insertion).
	_, ns2, err := m.BeforeModelRewriteState(ctx, ns, nil)
	require.NoError(t, err)
	assert.Len(t, ns2.Messages, 2)

	// No sub-agents (empty reminder): nothing is inserted.
	empty := &typedSubagentMiddleware[*schema.AgenticMessage]{}
	stEmpty := &adk.TypedChatModelAgentState[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{user}}
	_, nsEmpty, err := empty.BeforeModelRewriteState(ctx, stEmpty, nil)
	require.NoError(t, err)
	assert.Len(t, nsEmpty.Messages, 1)

	// nil state: returned unchanged (nil-guard path).
	_, nsNil, err := m.BeforeModelRewriteState(ctx, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, nsNil)
}
