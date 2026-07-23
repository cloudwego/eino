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

// recordingModel records the input it was given on each call and always returns
// a final answer (no tool calls), so every turn is a single model call that ends
// the turn.
type recordingModel struct {
	inputs [][]*schema.Message
}

func (m *recordingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	cp := make([]*schema.Message, len(input))
	copy(cp, input)
	m.inputs = append(m.inputs, cp)
	return schema.AssistantMessage("done", nil), nil
}

func (m *recordingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func drainEvents(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	t.Helper()
	for {
		event, ok := iter.Next()
		if !ok {
			return
		}
		assert.NoError(t, event.Err)
	}
}

func countReminders(msgs []*schema.Message, extraKey string) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role != schema.System {
			continue
		}
		if _, ok := msg.Extra[extraKey]; ok {
			n++
		}
	}
	return n
}

// TestSubagent_ReminderPersistsInPlaceAcrossTurns verifies that the agent-types
// reminder appended in BeforeModelRewriteState is persisted as a durable
// MessageInserted session event, so on the next turn it is reconstructed in
// place (before the turn's prior assistant output) rather than being dropped
// from the event log and re-injected at the tail. Keeping it in place preserves
// the cross-turn prefix.
func TestSubagent_ReminderPersistsInPlaceAcrossTurns(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore[*schema.Message](nil)
	const sessionID = "subagent-reminder-session"

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
	})
	require.NoError(t, err)

	newAgent := func(m model.BaseChatModel) adk.Agent {
		a, aerr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:        "reminder-agent",
			Description: "reminder persistence test agent",
			Model:       m,
			Handlers:    []adk.ChatModelAgentMiddleware{mw},
		})
		require.NoError(t, aerr)
		return a
	}

	// Turn 1.
	model1 := &recordingModel{}
	runner1 := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        newAgent(model1),
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainEvents(t, runner1.Query(ctx, "hello"))

	// The reminder must have been persisted as a durable MessageInserted event.
	res, err := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)
	persistedReminders := 0
	for _, ev := range res.Events {
		if ev.MessageInserted == nil {
			continue
		}
		if _, ok := ev.MessageInserted.Message.Extra[agentTypesReminderExtraKey]; ok {
			persistedReminders++
		}
	}
	assert.Equal(t, 1, persistedReminders, "reminder should be persisted as a MessageInserted event exactly once")

	// Turn 2 on the same session.
	model2 := &recordingModel{}
	runner2 := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        newAgent(model2),
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainEvents(t, runner2.Query(ctx, "again"))

	require.NotEmpty(t, model2.inputs)
	turn2Input := model2.inputs[0]

	// Exactly one reminder is present (reconstructed, not re-appended as a duplicate).
	assert.Equal(t, 1, countReminders(turn2Input, agentTypesReminderExtraKey),
		"turn 2 must see exactly one reminder, reconstructed in place")

	// It sits in place — before the last message (the new user turn) — rather than
	// being re-injected at the tail, which is what preserves the prefix.
	last := turn2Input[len(turn2Input)-1]
	_, lastIsReminder := last.Extra[agentTypesReminderExtraKey]
	assert.False(t, lastIsReminder, "reminder must be reconstructed in place, not appended at the tail")
}
